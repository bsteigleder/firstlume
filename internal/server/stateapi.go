package server

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
	"unicode"

	"github.com/go-chi/chi/v5"

	"github.com/bsteigleder/firstlume/internal/api"
	"github.com/bsteigleder/firstlume/internal/store"
)

// Terraform's http backend locks and unlocks with these methods by default.
func init() {
	chi.RegisterMethod("LOCK")
	chi.RegisterMethod("UNLOCK")
}

const (
	defaultStateMaxBytes = 64 << 20
	maxLockInfoBody      = 64 << 10
	maxStateKey          = 512
)

// stateRoutes serve Terraform's http backend (spec 0002, 4.3) at
// /state/v1/stacks/{stack}/{unit key}; the stack's own URL is its root unit.
func (s *Server) stateRoutes(r chi.Router) {
	r.Use(s.requireStateActor)
	r.HandleFunc("/stacks/{stack}", s.stateProtocol)
	r.HandleFunc("/stacks/{stack}/*", s.stateProtocol)
}

// requireStateActor authenticates Basic credentials whose password is a state
// token, scoped to one stack (R3.1, R3.2). Any failure is the same 401, so
// the response says nothing about whether the stack exists.
func (s *Server) requireStateActor(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, password, ok := r.BasicAuth()
		if !ok {
			w.Header().Set("WWW-Authenticate", `Basic realm="firstlume state"`)
			writeError(w, http.StatusUnauthorized, "invalid or missing state token")
			return
		}
		claims, err := s.keys.ParseStateToken(password)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "invalid or missing state token")
			return
		}
		actor, err := s.store.StateRunActor(r.Context(), claims.RunID, api.Phase(claims.Phase))
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusUnauthorized, "the run is no longer executing this phase")
			return
		}
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxStateActor, actor)))
	})
}

func (s *Server) stateProtocol(w http.ResponseWriter, r *http.Request) {
	actor := r.Context().Value(ctxStateActor).(*store.StateActor)
	if chi.URLParam(r, "stack") != actor.StackID {
		writeError(w, http.StatusNotFound, "stack not found")
		return
	}
	key, err := stateKey(chi.URLParam(r, "*"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.readState(w, r, actor, key)
	case http.MethodPost:
		s.writeState(w, r, actor, key)
	case "LOCK", "UNLOCK":
		s.lockState(w, r, actor, key)
	default:
		w.Header().Set("Allow", "GET, POST, LOCK, UNLOCK")
		writeError(w, http.StatusMethodNotAllowed, "the state backend supports GET, POST, LOCK and UNLOCK")
	}
}

// stateKey normalizes a unit key: "." and "" are the root, "./a/" is "a".
func stateKey(raw string) (string, error) {
	if strings.Contains(raw, "%") {
		var err error
		if raw, err = url.PathUnescape(raw); err != nil {
			return "", errors.New("invalid unit key")
		}
	}
	if raw == "" {
		return "", nil
	}
	if strings.HasPrefix(raw, "/") || len(raw) > maxStateKey || strings.ContainsFunc(raw, unicode.IsControl) {
		return "", errors.New("unit key must be a relative path of up to 512 characters")
	}
	key := path.Clean(raw)
	if key == ".." || strings.HasPrefix(key, "../") {
		return "", errors.New("unit key must not leave the stack")
	}
	if key == "." {
		key = ""
	}
	return key, nil
}

func (s *Server) readState(w http.ResponseWriter, r *http.Request, actor *store.StateActor, key string) {
	data, err := s.store.ReadState(r.Context(), actor, key)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if data == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}

func (s *Server) writeState(w http.ResponseWriter, r *http.Request, actor *store.StateActor, key string) {
	if !actor.CanWrite {
		writeError(w, http.StatusForbidden, "a plan never writes state: only the apply phase can")
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.cfg.StateMaxBytes))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "state larger than the configured limit")
		return
	}
	if want := r.Header.Get("Content-MD5"); want != "" {
		sum := md5.Sum(data)
		if base64.StdEncoding.EncodeToString(sum[:]) != want {
			writeError(w, http.StatusBadRequest, "Content-MD5 does not match the body")
			return
		}
	}
	err = s.store.WriteState(r.Context(), actor, key, r.URL.Query().Get("ID"), data)
	if s.stateError(w, r, err, http.StatusLocked) {
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) lockState(w http.ResponseWriter, r *http.Request, actor *store.StateActor, key string) {
	info, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxLockInfoBody))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "lock info too large")
		return
	}
	if r.Method == "LOCK" {
		if s.stateError(w, r, s.store.LockState(r.Context(), actor, key, info), http.StatusLocked) {
			return
		}
	} else if s.stateError(w, r, s.store.UnlockState(r.Context(), actor, key, info), http.StatusConflict) {
		return
	}
	w.WriteHeader(http.StatusOK)
}

// stateError writes the response for a failed state operation and reports
// whether there was one. A held lock answers with the holder's LockInfo, which
// Terraform prints.
func (s *Server) stateError(w http.ResponseWriter, r *http.Request, err error, lockedStatus int) bool {
	var locked *store.StateLockedError
	var conflict *store.StateConflictError
	switch {
	case err == nil:
		return false
	case errors.As(err, &locked):
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(lockedStatus)
		_, _ = w.Write(locked.Info)
	case errors.As(err, &conflict):
		writeError(w, http.StatusConflict, conflict.Reason)
	case errors.Is(err, store.ErrInvalidState), errors.Is(err, store.ErrInvalid):
		writeError(w, http.StatusUnprocessableEntity, err.Error())
	default:
		s.internalError(w, r, err)
	}
	return true
}

// User API: the stack's state as seen in Firstlume.

func (s *Server) listStateUnits(w http.ResponseWriter, r *http.Request) {
	units, err := s.store.ListStateUnits(r.Context(), orgID(r), id(r))
	if err != nil {
		s.storeError(w, r, err, "stack")
		return
	}
	writeJSON(w, http.StatusOK, units)
}

func (s *Server) deleteState(w http.ResponseWriter, r *http.Request) {
	var req api.DeleteStateRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	st, err := s.store.GetStack(r.Context(), orgID(r), id(r))
	if err != nil {
		s.storeError(w, r, err, "stack")
		return
	}
	if req.Confirm != st.Name {
		writeError(w, http.StatusUnprocessableEntity, "type the stack's name to confirm deleting its state")
		return
	}
	if err := s.store.DeleteState(r.Context(), orgID(r), st.ID, adminIdentity); err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeError(w, http.StatusConflict, "the state can't be deleted while a run is active or a unit is locked")
			return
		}
		s.storeError(w, r, err, "stack")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// purgeState removes deleted states past their retention and orphan objects.
func (s *Server) purgeState(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			n, err := s.store.PurgeState(ctx, store.OrphanAge)
			if err != nil {
				s.log.Error("purging state", "err", err)
			} else if n > 0 {
				s.log.Info("purged state objects", "count", n)
			}
		}
	}
}
