// Package secrets handles encryption at rest and signed tokens.
//
// Everything is derived from a single 32-byte master key so operators only
// have one secret to manage and back up.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
)

const (
	keyVersion1 byte = 1
	masterLen        = 32
)

// Keys holds the keys derived from the master key.
type Keys struct {
	aead    cipher.AEAD
	signKey []byte
}

// ParseMasterKey decodes a base64 (standard or URL encoding) 32-byte master key.
func ParseMasterKey(encoded string) (*Keys, error) {
	var raw []byte
	var err error
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if raw, err = enc.DecodeString(encoded); err == nil {
			break
		}
	}
	if err != nil {
		return nil, fmt.Errorf("master key must be base64: %w", err)
	}
	if len(raw) != masterLen {
		return nil, fmt.Errorf("master key must be %d bytes, got %d", masterLen, len(raw))
	}
	return NewKeys(raw)
}

// NewKeys derives the encryption and signing keys from a raw master key.
func NewKeys(master []byte) (*Keys, error) {
	if len(master) != masterLen {
		return nil, fmt.Errorf("master key must be %d bytes", masterLen)
	}
	block, err := aes.NewCipher(derive(master, "firstlume/encryption/v1"))
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Keys{aead: aead, signKey: derive(master, "firstlume/signing/v1")}, nil
}

func derive(master []byte, label string) []byte {
	m := hmac.New(sha256.New, master)
	m.Write([]byte(label))
	return m.Sum(nil)
}

// Encrypt seals plaintext. Output layout: version(1) | nonce | ciphertext+tag.
func (k *Keys) Encrypt(plaintext []byte) ([]byte, error) {
	nonce := make([]byte, k.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	out := make([]byte, 0, 1+len(nonce)+len(plaintext)+k.aead.Overhead())
	out = append(out, keyVersion1)
	out = append(out, nonce...)
	return k.aead.Seal(out, nonce, plaintext, []byte{keyVersion1}), nil
}

// Decrypt opens data produced by Encrypt.
func (k *Keys) Decrypt(data []byte) ([]byte, error) {
	ns := k.aead.NonceSize()
	if len(data) < 1+ns+k.aead.Overhead() {
		return nil, errors.New("ciphertext too short")
	}
	if data[0] != keyVersion1 {
		return nil, fmt.Errorf("unknown key version %d", data[0])
	}
	return k.aead.Open(nil, data[1:1+ns], data[1+ns:], data[:1])
}

// Sign returns an HMAC-SHA256 of msg.
func (k *Keys) Sign(msg []byte) []byte {
	m := hmac.New(sha256.New, k.signKey)
	m.Write(msg)
	return m.Sum(nil)
}

// Verify checks a signature produced by Sign in constant time.
func (k *Keys) Verify(msg, sig []byte) bool {
	return hmac.Equal(k.Sign(msg), sig)
}

// HashToken returns the SHA-256 of an opaque bearer token, for storage.
func HashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// RandomToken returns a URL-safe random token with the given prefix.
func RandomToken(prefix string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(b), nil
}
