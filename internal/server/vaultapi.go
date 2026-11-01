package server

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bsteigleder/firstlume/internal/api"
	"github.com/bsteigleder/firstlume/internal/store"
	"github.com/bsteigleder/firstlume/internal/vault"
)

// Vault (spec 0003, F2): run JWTs, their public key, each organization's
// Vault connection and the permission check.

func (s *Server) openIDConfiguration(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                s.cfg.PublicURL,
		"jwks_uri":                              s.cfg.PublicURL + "/.well-known/jwks.json",
		"id_token_signing_alg_values_supported": []string{"ES256"},
		"response_types_supported":              []string{"id_token"},
		"subject_types_supported":               []string{"public"},
	})
}

func (s *Server) jwks(w http.ResponseWriter, r *http.Request) {
	key, err := s.store.SigningKey(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, key.JWKS())
}

func (s *Server) vaultPublicKey(w http.ResponseWriter, r *http.Request) {
	key, err := s.store.SigningKey(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/x-pem-file")
	_, _ = w.Write([]byte(key.PublicPEM()))
}

func (s *Server) getVaultConnection(w http.ResponseWriter, r *http.Request) {
	c, err := s.store.GetVaultConnection(r.Context(), orgID(r))
	if err != nil {
		s.storeError(w, r, err, "Vault connection")
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) putVaultConnection(w http.ResponseWriter, r *http.Request) {
	var req api.PutVaultConnectionRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := validateVaultConnection(&req); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	c, err := s.store.PutVaultConnection(r.Context(), orgID(r), req)
	if err != nil {
		s.storeError(w, r, err, "Vault connection")
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func validateVaultConnection(req *api.PutVaultConnectionRequest) error {
	req.Address = strings.TrimRight(strings.TrimSpace(req.Address), "/")
	if _, err := vault.New(vault.Config{Address: req.Address, CACert: req.CACert}); err != nil {
		return err
	}
	if u, _ := url.Parse(req.Address); u.Path != "" || u.RawQuery != "" || u.User != nil {
		return errors.New("the Vault address is scheme, host and port only, like https://vault.example.com:8200")
	}
	req.AuthPath = strings.Trim(req.AuthPath, "/")
	for _, f := range []struct{ name, value string }{{"auth_path", req.AuthPath}, {"namespace", req.Namespace}, {"audience", req.Audience}} {
		if len(f.value) > 256 || strings.ContainsAny(f.value, " \t\r\n?#") || strings.Contains(f.value, "..") {
			return errors.New(f.name + " has invalid characters")
		}
	}
	return nil
}

func (s *Server) deleteVaultConnection(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteVaultConnection(r.Context(), orgID(r)); err != nil {
		s.storeError(w, r, err, "Vault connection")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// runJWT signs the JWT of a phase of a stack's run.
func (s *Server) runJWT(ctx context.Context, sv *store.StackVault, runID string, runType api.RunType, phase api.Phase) (string, error) {
	key, err := s.store.SigningKey(ctx)
	if err != nil {
		return "", err
	}
	claimPhase := string(phase)
	if runType == api.RunDrift {
		claimPhase = "drift"
	}
	return key.Sign(sv.RunJWTClaims(s.cfg.PublicURL, runID, runType, claimPhase, time.Now()))
}

// execVault gives a phase its Vault login, when the organization has a connection.
func (s *Server) execVault(ctx context.Context, spec *api.ExecSpec) (*api.ExecVault, error) {
	sv, err := s.store.StackVault(ctx, "", spec.StackID)
	if sv == nil || err != nil {
		return nil, err
	}
	jwt, err := s.runJWT(ctx, sv, spec.RunID, spec.Type, spec.Phase)
	if err != nil {
		return nil, err
	}
	c := sv.Connection
	return &api.ExecVault{
		Address: c.Address, CACert: c.CACert, Namespace: c.Namespace, AuthPath: c.AuthPath, Role: sv.Role, JWT: jwt,
		HashKey: base64.RawURLEncoding.EncodeToString(s.keys.Sign([]byte("vault-hash/" + spec.RunID))),
	}, nil
}

// vaultCheck logs in as the stack would and asks Vault, for each path,
// whether the role can read it. Nothing is read (spec 0003, section 5).
func (s *Server) vaultCheck(w http.ResponseWriter, r *http.Request) {
	var req api.VaultCheckRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	switch req.Phase {
	case "":
		req.Phase = api.PhasePlan
	case api.PhasePlan, api.PhaseApply:
	default:
		writeError(w, http.StatusUnprocessableEntity, "phase must be plan or apply")
		return
	}
	if len(req.Paths) > 50 {
		writeError(w, http.StatusUnprocessableEntity, "at most 50 paths at a time")
		return
	}
	sv, err := s.store.StackVault(r.Context(), orgID(r), id(r))
	if err != nil {
		s.storeError(w, r, err, "stack")
		return
	}
	if sv == nil {
		writeError(w, http.StatusConflict, "the organization has no Vault connection")
		return
	}
	res := api.VaultCheckResult{Role: sv.Role}
	if sv.Role == "" {
		res.Error = "no Vault role is configured for this stack"
		writeJSON(w, http.StatusOK, res)
		return
	}
	c, err := vault.New(vault.Config{Address: sv.Connection.Address, CACert: sv.Connection.CACert, Namespace: sv.Connection.Namespace})
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	jwt, err := s.runJWT(r.Context(), sv, "check", api.RunPlan, req.Phase)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := c.Login(ctx, sv.Connection.AuthPath, sv.Role, jwt); err != nil {
		res.Error = "login with role " + sv.Role + " failed: " + err.Error()
		writeJSON(w, http.StatusOK, res)
		return
	}
	defer func() { _ = c.Revoke(context.WithoutCancel(ctx)) }()
	for _, p := range req.Paths {
		check := api.VaultPathCheck{Path: p}
		if ok, err := c.CanRead(ctx, p); err != nil {
			check.Error = err.Error()
		} else {
			check.CanRead = ok
		}
		res.Paths = append(res.Paths, check)
	}
	writeJSON(w, http.StatusOK, res)
}
