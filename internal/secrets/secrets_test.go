package secrets

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func testKeys(t *testing.T) *Keys {
	t.Helper()
	master := make([]byte, 32)
	if _, err := rand.Read(master); err != nil {
		t.Fatal(err)
	}
	k, err := ParseMasterKey(base64.StdEncoding.EncodeToString(master))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestEncryptRoundTrip(t *testing.T) {
	k := testKeys(t)
	plain := []byte("super secret value")
	ct, err := k.Encrypt(plain)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ct, plain) {
		t.Fatal("ciphertext contains plaintext")
	}
	got, err := k.Decrypt(ct)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("got %q, want %q", got, plain)
	}

	ct[len(ct)-1] ^= 0xff
	if _, err := k.Decrypt(ct); err == nil {
		t.Fatal("tampered ciphertext decrypted")
	}
}

func TestDecryptWithOtherKeyFails(t *testing.T) {
	ct, err := testKeys(t).Encrypt([]byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testKeys(t).Decrypt(ct); err == nil {
		t.Fatal("decrypted with a different key")
	}
}

func TestParseMasterKeyRejectsWrongLength(t *testing.T) {
	if _, err := ParseMasterKey(base64.StdEncoding.EncodeToString([]byte("short"))); err == nil {
		t.Fatal("expected error for short key")
	}
}

func TestRunToken(t *testing.T) {
	k := testKeys(t)
	tok, err := k.IssueRunToken("run-1", "plan", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	c, err := k.ParseRunToken(tok)
	if err != nil {
		t.Fatal(err)
	}
	if c.RunID != "run-1" || c.Phase != "plan" {
		t.Fatalf("unexpected claims %+v", c)
	}

	if _, err := testKeys(t).ParseRunToken(tok); err == nil {
		t.Fatal("token accepted by a different key")
	}

	body, sig, _ := strings.Cut(strings.TrimPrefix(tok, runTokenPrefix), ".")
	forged := runTokenPrefix + body + "x." + sig
	if _, err := k.ParseRunToken(forged); err == nil {
		t.Fatal("forged token accepted")
	}

	expired, err := k.IssueRunToken("run-1", "plan", -time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.ParseRunToken(expired); err == nil {
		t.Fatal("expired token accepted")
	}
}
