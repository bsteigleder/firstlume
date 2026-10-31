package secrets

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const (
	runTokenPrefix = "flr_"
	// State tokens let Terraform, inside a run, reach the stack's state. They
	// are signed separately from run tokens, so neither works as the other.
	stateTokenPrefix = "fls_"
)

// RunClaims identifies the single run phase an executor is allowed to act on.
type RunClaims struct {
	RunID     string `json:"rid"`
	Phase     string `json:"ph"`
	ExpiresAt int64  `json:"exp"`
}

var ErrInvalidToken = errors.New("invalid token")

// IssueRunToken creates a short-lived token scoped to one run phase.
func (k *Keys) IssueRunToken(runID, phase string, ttl time.Duration) (string, error) {
	return k.issue(runTokenPrefix, runID, phase, ttl)
}

// ParseRunToken validates the signature and expiry of a run token.
func (k *Keys) ParseRunToken(token string) (*RunClaims, error) {
	return k.parse(runTokenPrefix, token)
}

// IssueStateToken creates the token a run phase's Terraform uses for the
// stack's state backend.
func (k *Keys) IssueStateToken(runID, phase string, ttl time.Duration) (string, error) {
	return k.issue(stateTokenPrefix, runID, phase, ttl)
}

// ParseStateToken validates the signature and expiry of a state token.
func (k *Keys) ParseStateToken(token string) (*RunClaims, error) {
	return k.parse(stateTokenPrefix, token)
}

func (k *Keys) issue(prefix, runID, phase string, ttl time.Duration) (string, error) {
	payload, err := json.Marshal(RunClaims{RunID: runID, Phase: phase, ExpiresAt: time.Now().Add(ttl).Unix()})
	if err != nil {
		return "", err
	}
	body := base64.RawURLEncoding.EncodeToString(payload)
	sig := base64.RawURLEncoding.EncodeToString(k.Sign([]byte(prefix + body)))
	return prefix + body + "." + sig, nil
}

func (k *Keys) parse(prefix, token string) (*RunClaims, error) {
	rest, ok := strings.CutPrefix(token, prefix)
	if !ok {
		return nil, ErrInvalidToken
	}
	body, sigB64, ok := strings.Cut(rest, ".")
	if !ok {
		return nil, ErrInvalidToken
	}
	sig, err := base64.RawURLEncoding.DecodeString(sigB64)
	if err != nil || !k.Verify([]byte(prefix+body), sig) {
		return nil, ErrInvalidToken
	}
	payload, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return nil, ErrInvalidToken
	}
	var c RunClaims
	if err := json.Unmarshal(payload, &c); err != nil {
		return nil, ErrInvalidToken
	}
	if time.Now().Unix() > c.ExpiresAt {
		return nil, ErrInvalidToken
	}
	return &c, nil
}
