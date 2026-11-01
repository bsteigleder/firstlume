package vault

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"strings"
)

// Run JWTs (spec 0003, R3.3) are ES256 tokens Firstlume signs for a run phase
// and Vault's jwt auth method verifies with Firstlume's public key.

// SigningKey signs run JWTs.
type SigningKey struct {
	priv *ecdsa.PrivateKey
	// ID is the key's "kid": derived from the public key.
	ID string
}

// NewSigningKey creates a P-256 key and returns it with its PKCS#8 encoding.
func NewSigningKey() (*SigningKey, []byte, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, nil, err
	}
	k, err := ParseSigningKey(der)
	return k, der, err
}

// ParseSigningKey reads a PKCS#8 P-256 key.
func ParseSigningKey(der []byte) (*SigningKey, error) {
	key, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, err
	}
	priv, ok := key.(*ecdsa.PrivateKey)
	if !ok || priv.Curve != elliptic.P256() {
		return nil, errors.New("the signing key is not a P-256 key")
	}
	pub, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(pub)
	return &SigningKey{priv: priv, ID: hex.EncodeToString(sum[:8])}, nil
}

// Sign returns a compact JWS of claims.
func (k *SigningKey) Sign(claims any) (string, error) {
	header, _ := json.Marshal(map[string]string{"alg": "ES256", "typ": "JWT", "kid": k.ID})
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signing := b64(header) + "." + b64(payload)
	digest := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, k.priv, digest[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return signing + "." + b64(sig), nil
}

// PublicPEM is the public key, as Vault's jwt_validation_pubkeys takes it.
func (k *SigningKey) PublicPEM() string {
	der, _ := x509.MarshalPKIXPublicKey(&k.priv.PublicKey)
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

// JWKS is the public key as a JSON Web Key Set.
func (k *SigningKey) JWKS() map[string]any {
	pub := k.priv.PublicKey
	x := make([]byte, 32)
	y := make([]byte, 32)
	pub.X.FillBytes(x)
	pub.Y.FillBytes(y)
	return map[string]any{"keys": []map[string]string{{
		"kty": "EC", "crv": "P-256", "x": b64(x), "y": b64(y), "kid": k.ID, "use": "sig", "alg": "ES256",
	}}}
}

// Public returns the public key, for verifying in tests and checks.
func (k *SigningKey) Public() crypto.PublicKey { return &k.priv.PublicKey }

// VerifyES256 checks a compact JWS against pub and returns its claims.
func VerifyES256(token string, pub *ecdsa.PublicKey) (map[string]any, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("not a compact JWS")
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(sig) != 64 {
		return nil, errors.New("bad signature encoding")
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !ecdsa.Verify(pub, digest[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		return nil, errors.New("invalid signature")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, err
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, fmt.Errorf("claims: %w", err)
	}
	return claims, nil
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
