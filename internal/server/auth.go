package server

import (
	"context"
	"crypto/subtle"
	"errors"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-chi/chi/v5"
	"golang.org/x/oauth2"

	"github.com/bsteigleder/firstlume/internal/api"
	"github.com/bsteigleder/firstlume/internal/secrets"
	"github.com/bsteigleder/firstlume/internal/store"
)

// AuthConfig is how people sign in (spec 0001, R7.11 to R7.14).
type AuthConfig struct {
	// PublicURL is where browsers reach Firstlume, e.g.
	// https://firstlume.example.com. The provider sends users back to
	// PublicURL + "/auth/callback", and session requests that change data
	// must come from this origin (R7.4). Required with OIDC.
	PublicURL string
	// OIDC is the sign-in provider; nil leaves only the admin token.
	OIDC *OIDCConfig
	// AdminTokenLogin offers "Use admin token" on the login screen (R7.5).
	// The token always works for the API.
	AdminTokenLogin bool
	// SessionTTL defaults to 12 hours (R7.3).
	SessionTTL time.Duration
	// DiscoveryRetry is how long to wait before reading the provider's
	// discovery document again after a failure. Defaults to a minute.
	DiscoveryRetry time.Duration
}

type OIDCConfig struct {
	// ProviderName labels the button: "Sign in with <ProviderName>".
	ProviderName string
	// Issuer is the "issuer" of the provider's discovery document; a
	// trailing slash that differs from it is tolerated.
	Issuer       string
	ClientID     string
	ClientSecret string
	Scopes       []string
	// GroupsClaim names the claim holding the user's groups (R7.11).
	GroupsClaim string
	// AllowedGroups, when not empty, lets in only members of one of them.
	AllowedGroups []string
}

const (
	sessionCookie   = "firstlume_session"
	loginAttemptTTL = 10 * time.Minute
)

// Reasons a login fails, sent to the login screen as ?login_error= (R7.14).
const (
	loginErrUnavailable = "unavailable"
	loginErrState       = "state"
	loginErrProvider    = "provider"
	loginErrDenied      = "denied"
)

// principal is who is calling /api/v1: the admin token or a signed-in user.
type principal struct {
	user *api.User
}

type principalKey struct{}

func principalFrom(ctx context.Context) principal {
	p, _ := ctx.Value(principalKey{}).(principal)
	return p
}

// oidcClient reads the provider's discovery document lazily, so a provider
// that is down when Firstlume starts doesn't stop it, and its button turns on
// once the provider answers (R7.12).
type oidcClient struct {
	cfg   OIDCConfig
	retry time.Duration

	mu       sync.Mutex
	provider *oidc.Provider
	trying   bool
	lastTry  time.Time
}

// get returns the provider, trying discovery when it is unknown and the last
// failure is older than the retry interval. Only one caller tries at a time.
func (c *oidcClient) get(ctx context.Context) (*oidc.Provider, error) {
	c.mu.Lock()
	if c.provider != nil {
		p := c.provider
		c.mu.Unlock()
		return p, nil
	}
	if c.trying || (!c.lastTry.IsZero() && time.Since(c.lastTry) < c.retry) {
		c.mu.Unlock()
		return nil, nil
	}
	c.trying = true
	c.lastTry = time.Now()
	c.mu.Unlock()

	dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	p, err := oidc.NewProvider(dctx, c.cfg.Issuer)
	if err != nil && strings.Contains(err.Error(), "did not match the issuer") {
		// Gitea answers "https://gitea.example.com" while others keep the
		// trailing slash; accept the issuer with or without it.
		alt := strings.TrimSuffix(c.cfg.Issuer, "/")
		if alt == c.cfg.Issuer {
			alt += "/"
		}
		if p2, err2 := oidc.NewProvider(dctx, alt); err2 == nil {
			p, err = p2, nil
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.trying = false
	if err != nil {
		return nil, err
	}
	c.provider = p
	return p, nil
}

func (s *Server) authRoutes(r chi.Router) {
	r.Get("/config", s.authConfig)
	r.Get("/login", s.authLogin)
	r.Get("/callback", s.authCallback)
	r.Post("/logout", s.authLogout)
}

// warmOIDC reads the discovery document at startup, so the first login
// doesn't wait for it and a misconfigured issuer shows up in the log early.
func (s *Server) warmOIDC(ctx context.Context) {
	if s.oidc == nil {
		return
	}
	if _, err := s.oidc.get(ctx); err != nil {
		s.log.Warn("OIDC provider unavailable, sign-in is off until it answers", "issuer", s.oidc.cfg.Issuer, "err", err)
		return
	}
	s.log.Info("OIDC provider ready", "issuer", s.oidc.cfg.Issuer)
}

// provider returns the OIDC provider, or nil while it is unavailable.
func (s *Server) provider(ctx context.Context) *oidc.Provider {
	if s.oidc == nil {
		return nil
	}
	p, err := s.oidc.get(ctx)
	if err != nil {
		s.log.Warn("OIDC provider unavailable", "issuer", s.oidc.cfg.Issuer, "err", err)
	}
	return p
}

func (s *Server) oauth2Config(p *oidc.Provider) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     s.oidc.cfg.ClientID,
		ClientSecret: s.oidc.cfg.ClientSecret,
		Endpoint:     p.Endpoint(),
		RedirectURL:  strings.TrimSuffix(s.cfg.Auth.PublicURL, "/") + "/auth/callback",
		Scopes:       s.oidc.cfg.Scopes,
	}
}

func (s *Server) authConfig(w http.ResponseWriter, r *http.Request) {
	out := api.AuthConfig{AdminTokenLogin: s.cfg.Auth.AdminTokenLogin}
	if s.oidc != nil {
		out.OIDC = &api.AuthProvider{Name: s.oidc.cfg.ProviderName, Available: s.provider(r.Context()) != nil}
	}
	writeJSON(w, http.StatusOK, out)
}

// authLogin starts the authorization code flow with PKCE, state and nonce
// (R7.1).
func (s *Server) authLogin(w http.ResponseWriter, r *http.Request) {
	p := s.provider(r.Context())
	if p == nil {
		loginFailed(w, r, loginErrUnavailable)
		return
	}
	state, err := secrets.RandomToken("")
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	nonce, err := secrets.RandomToken("")
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	verifier := oauth2.GenerateVerifier()
	attempt := store.LoginAttempt{CodeVerifier: verifier, Nonce: nonce, ReturnTo: safeReturnTo(r.URL.Query().Get("return_to"))}
	if err := s.store.CreateLoginAttempt(r.Context(), state, attempt, loginAttemptTTL); err != nil {
		s.internalError(w, r, err)
		return
	}
	http.Redirect(w, r, s.oauth2Config(p).AuthCodeURL(state, oauth2.S256ChallengeOption(verifier), oidc.Nonce(nonce)), http.StatusFound)
}

func (s *Server) authCallback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		s.log.Warn("OIDC provider refused the login", "error", e, "description", q.Get("error_description"))
		loginFailed(w, r, loginErrProvider)
		return
	}
	attempt, err := s.store.ConsumeLoginAttempt(ctx, q.Get("state"))
	if errors.Is(err, store.ErrNotFound) {
		loginFailed(w, r, loginErrState)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	p := s.provider(ctx)
	if p == nil {
		loginFailed(w, r, loginErrUnavailable)
		return
	}
	tok, err := s.oauth2Config(p).Exchange(ctx, q.Get("code"), oauth2.VerifierOption(attempt.CodeVerifier))
	if err != nil {
		s.log.Warn("OIDC code exchange failed", "err", err)
		loginFailed(w, r, loginErrProvider)
		return
	}
	rawID, _ := tok.Extra("id_token").(string)
	if rawID == "" {
		s.log.Warn("OIDC provider sent no id_token; is the openid scope configured?")
		loginFailed(w, r, loginErrProvider)
		return
	}
	idt, err := p.Verifier(&oidc.Config{ClientID: s.oidc.cfg.ClientID}).Verify(ctx, rawID)
	if err != nil {
		s.log.Warn("OIDC id_token rejected", "err", err)
		loginFailed(w, r, loginErrProvider)
		return
	}
	if subtle.ConstantTimeCompare([]byte(idt.Nonce), []byte(attempt.Nonce)) != 1 {
		loginFailed(w, r, loginErrState)
		return
	}
	claims := map[string]any{}
	if err := idt.Claims(&claims); err != nil {
		s.log.Warn("OIDC id_token claims unreadable", "err", err)
		loginFailed(w, r, loginErrProvider)
		return
	}
	// Some providers put profile and groups only in userinfo.
	if _, ok := claims[s.oidc.cfg.GroupsClaim]; !ok {
		if info, err := p.UserInfo(ctx, oauth2.StaticTokenSource(tok)); err == nil {
			extra := map[string]any{}
			if info.Claims(&extra) == nil {
				for k, v := range extra {
					if _, ok := claims[k]; !ok {
						claims[k] = v
					}
				}
			}
		}
	}

	groups, hasGroups := stringList(claims[s.oidc.cfg.GroupsClaim])
	login := firstString(claims, "preferred_username", "nickname", "email")
	if login == "" {
		login = idt.Subject
	}
	if allowed := s.oidc.cfg.AllowedGroups; len(allowed) > 0 {
		if !hasGroups {
			// Gitea leaves the claim out for someone in no organization, so
			// this is usually just "not a member". If nobody gets in, the
			// provider isn't sending groups: check the groups scope.
			s.log.Warn("login refused: the token has no groups claim, so the user is in none of the allowed groups; "+
				"if every login fails, check that the provider sends the claim (scope groups)",
				"claim", s.oidc.cfg.GroupsClaim, "login", login)
			loginFailed(w, r, loginErrDenied)
			return
		}
		if !slices.ContainsFunc(groups, func(g string) bool { return slices.Contains(allowed, g) }) {
			s.log.Info("login refused: not in an allowed group", "login", login)
			loginFailed(w, r, loginErrDenied)
			return
		}
	}

	u, err := s.store.UpsertUser(ctx, api.User{
		Issuer: idt.Issuer, Subject: idt.Subject, Login: login,
		Name: firstString(claims, "name"), Email: firstString(claims, "email"), AvatarURL: firstString(claims, "picture"),
		Groups: groups,
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	token, err := s.store.CreateSession(ctx, u.ID, s.cfg.Auth.SessionTTL)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/",
		MaxAge: int(s.cfg.Auth.SessionTTL.Seconds()), HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: s.secureCookies(),
	})
	s.log.Info("signed in", "login", u.Login)
	http.Redirect(w, r, attempt.ReturnTo, http.StatusFound)
}

func (s *Server) authLogout(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(sessionCookie)
	if err == nil && c.Value != "" {
		if !s.sameOrigin(r) {
			writeError(w, http.StatusForbidden, "cross-site request refused")
			return
		}
		if err := s.store.RevokeSession(r.Context(), c.Value); err != nil {
			s.internalError(w, r, err)
			return
		}
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: s.secureCookies()})
	w.WriteHeader(http.StatusNoContent)
}

// requireUser lets in the admin token (Authorization: Bearer) or a session
// cookie. Until Marco 3b a signed-in user can do what the admin token does
// (D8).
func (s *Server) requireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tok := bearer(r); tok != "" {
			if subtle.ConstantTimeCompare([]byte(tok), []byte(s.cfg.AdminToken)) != 1 {
				writeError(w, http.StatusUnauthorized, "invalid or missing token")
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		c, err := r.Cookie(sessionCookie)
		if err != nil || c.Value == "" {
			writeError(w, http.StatusUnauthorized, "invalid or missing token")
			return
		}
		u, err := s.store.SessionUser(r.Context(), c.Value)
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusUnauthorized, "session expired or signed out")
			return
		}
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		if !safeMethod(r.Method) {
			if !s.sameOrigin(r) {
				writeError(w, http.StatusForbidden, "cross-site request refused")
				return
			}
			if !allowedContentType(r) {
				writeError(w, http.StatusUnsupportedMediaType, "send application/json")
				return
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, principal{user: u})))
	})
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	if p.user == nil {
		writeJSON(w, http.StatusOK, api.Me{Kind: adminIdentity, Organizations: []api.Organization{}})
		return
	}
	orgs, err := s.store.UserOrganizations(r.Context(), p.user.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, api.Me{Kind: "user", User: p.user, Organizations: orgs})
}

func (s *Server) purgeAuth(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.store.PurgeAuth(ctx); err != nil {
				s.log.Error("purging sessions", "err", err)
			}
		}
	}
}

func (s *Server) secureCookies() bool {
	return strings.HasPrefix(s.cfg.Auth.PublicURL, "https://")
}

// sameOrigin reports whether the request's Origin is Firstlume's public URL.
// Browsers send Origin on every request that changes data, so a missing one
// is refused too (R7.4).
func (s *Server) sameOrigin(r *http.Request) bool {
	pub, err := url.Parse(s.cfg.Auth.PublicURL)
	if err != nil || pub.Host == "" {
		return false
	}
	return r.Header.Get("Origin") == pub.Scheme+"://"+pub.Host
}

func safeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

// allowedContentType accepts JSON, and multipart for the state bundle import.
// HTML forms can't send JSON, and the Origin check stops a cross-site
// multipart form.
func allowedContentType(r *http.Request) bool {
	ct := r.Header.Get("Content-Type")
	if ct == "" {
		return r.ContentLength == 0
	}
	mt, _, err := mime.ParseMediaType(ct)
	return err == nil && (mt == "application/json" || mt == "multipart/form-data")
}

// safeReturnTo keeps the post-login redirect inside Firstlume.
func safeReturnTo(p string) string {
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.HasPrefix(p, "/\\") {
		return "/"
	}
	return p
}

func loginFailed(w http.ResponseWriter, r *http.Request, reason string) {
	http.Redirect(w, r, "/?login_error="+reason, http.StatusFound)
}

func firstString(claims map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := claims[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// stringList reads a claim that should be a list of strings. A provider may
// send a single string instead.
func stringList(v any) ([]string, bool) {
	switch x := v.(type) {
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			if s, ok := e.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out, true
	case string:
		if x == "" {
			return []string{}, true
		}
		return []string{x}, true
	default:
		return []string{}, false
	}
}
