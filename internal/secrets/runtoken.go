package secrets

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const runTokenPrefix = "flr_"

// RunClaims identifies the single run phase an executor is allowed to act on.
type RunClaims struct {
	RunID     string `json:"rid"`
	Phase     string `json:"ph"`
	ExpiresAt int64  `json:"exp"`
}

var ErrInvalidToken = errors.New("invalid token")

// IssueRunToken creates a short-lived token scoped to one run phase.
func (k *Keys) IssueRunToken(runID, phase string, ttl time.Duration) (string, error) {
	payload, err := json.Marshal(RunClaims{RunID: runID, Phase: phase, ExpiresAt: time.Now().Add(ttl).Unix()})
	if err != nil {
		return "", err
	}
	body := base64.RawURLEncoding.EncodeToString(payload)
	sig := base64.RawURLEncoding.EncodeToString(k.Sign([]byte(runTokenPrefix + body)))
	return runTokenPrefix + body + "." + sig, nil
}

// ParseRunToken validates the signature and expiry of a run token.
func (k *Keys) ParseRunToken(token string) (*RunClaims, error) {
	rest, ok := strings.CutPrefix(token, runTokenPrefix)
	if !ok {
		return nil, ErrInvalidToken
	}
	body, sigB64, ok := strings.Cut(rest, ".")
	if !ok {
		return nil, ErrInvalidToken
	}
	sig, err := base64.RawURLEncoding.DecodeString(sigB64)
	if err != nil || !k.Verify([]byte(runTokenPrefix+body), sig) {
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
