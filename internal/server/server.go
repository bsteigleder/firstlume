// Package server implements the control-plane HTTP API.
package server

import (
	"context"
	"crypto/subtle"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/bsteigleder/firstlume/internal/secrets"
	"github.com/bsteigleder/firstlume/internal/store"
)

type Config struct {
	Addr             string
	OrganizationID   string
	AdminToken       string
	RunTokenTTL      time.Duration
	HeartbeatTimeout time.Duration
	LogPollInterval  time.Duration
}

type Server struct {
	cfg   Config
	store *store.Store
	keys  *secrets.Keys
	log   *slog.Logger
}

func New(cfg Config, st *store.Store, keys *secrets.Keys, log *slog.Logger) *Server {
	if cfg.RunTokenTTL == 0 {
		cfg.RunTokenTTL = 6 * time.Hour
	}
	if cfg.HeartbeatTimeout == 0 {
		cfg.HeartbeatTimeout = 10 * time.Minute
	}
	if cfg.LogPollInterval == 0 {
		cfg.LogPollInterval = time.Second
	}
	return &Server{cfg: cfg, store: st, keys: keys, log: log}
}

func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.RealIP, s.requestLogger, middleware.Recoverer)

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	r.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := s.store.Ping(r.Context()); err != nil {
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	r.Route("/api/v1", func(r chi.Router) {
		r.Use(s.requireAdmin)
		s.userRoutes(r)
	})
	r.Route("/agent/v1", func(r chi.Router) {
		r.Use(s.requireAgent)
		s.agentRoutes(r)
	})
	r.Route("/exec/v1", func(r chi.Router) {
		r.Use(s.requireRunToken)
		s.execRoutes(r)
	})
	return r
}

// Run serves HTTP and runs background maintenance until ctx is canceled.
func (s *Server) Run(ctx context.Context) error {
	srv := &http.Server{
		Addr:              s.cfg.Addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go s.sweepStaleRuns(ctx)

	errc := make(chan error, 1)
	go func() {
		s.log.Info("server listening", "addr", s.cfg.Addr)
		errc <- srv.ListenAndServe()
	}()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (s *Server) sweepStaleRuns(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			n, err := s.store.FailStaleRuns(ctx, s.cfg.HeartbeatTimeout)
			if err != nil {
				s.log.Error("failing stale runs", "err", err)
			} else if n > 0 {
				s.log.Warn("failed runs with no heartbeat", "count", n)
			}
		}
	}
}

func (s *Server) requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		start := time.Now()
		next.ServeHTTP(ww, r)
		if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
			return
		}
		s.log.Info("http request", "method", r.Method, "path", r.URL.Path, "status", ww.Status(),
			"duration", time.Since(start).Round(time.Millisecond), "request_id", middleware.GetReqID(r.Context()))
	})
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if tok, ok := strings.CutPrefix(h, "Bearer "); ok {
		return strings.TrimSpace(tok)
	}
	return ""
}

type ctxKey int

const (
	ctxAgent ctxKey = iota
	ctxRunClaims
)

func (s *Server) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := bearer(r)
		if tok == "" || subtle.ConstantTimeCompare([]byte(tok), []byte(s.cfg.AdminToken)) != 1 {
			writeError(w, http.StatusUnauthorized, "invalid or missing token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requireAgent(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := bearer(r)
		if tok == "" {
			writeError(w, http.StatusUnauthorized, "missing token")
			return
		}
		agent, err := s.store.AuthenticateAgent(r.Context(), tok)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusUnauthorized, "invalid token")
				return
			}
			s.internalError(w, r, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxAgent, agent)))
	})
}

func (s *Server) requireRunToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, err := s.keys.ParseRunToken(bearer(r))
		if err != nil {
			writeError(w, http.StatusUnauthorized, "invalid run token")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxRunClaims, claims)))
	})
}

func agentFrom(ctx context.Context) *store.AgentIdentity {
	return ctx.Value(ctxAgent).(*store.AgentIdentity)
}

func runClaimsFrom(ctx context.Context) *secrets.RunClaims {
	return ctx.Value(ctxRunClaims).(*secrets.RunClaims)
}
