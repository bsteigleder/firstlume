package executor

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bsteigleder/firstlume/internal/api"
	"github.com/bsteigleder/firstlume/internal/vault"
)

// testVault is a real Vault (make dev-vault) configured, under names of its
// own, to trust a signing key created by the test.
type testVault struct {
	t     *testing.T
	addr  string
	token string
	// id prefixes the test's mounts, secrets, policies and roles.
	id  string
	key *vault.SigningKey
}

func newTestVault(t *testing.T) *testVault {
	t.Helper()
	addr, token := os.Getenv("FIRSTLUME_TEST_VAULT_ADDR"), os.Getenv("FIRSTLUME_TEST_VAULT_TOKEN")
	if addr == "" || token == "" {
		t.Skip("set FIRSTLUME_TEST_VAULT_ADDR and FIRSTLUME_TEST_VAULT_TOKEN (make dev-vault) to run this test")
	}
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	key, _, err := vault.NewSigningKey()
	if err != nil {
		t.Fatal(err)
	}
	v := &testVault{t: t, addr: addr, token: token, id: "t" + hex.EncodeToString(b), key: key}
	v.must("POST", "sys/auth/"+v.id, map[string]any{"type": "jwt"})
	t.Cleanup(func() { v.call("DELETE", "sys/auth/"+v.id, nil) })
	v.must("POST", "auth/"+v.id+"/config", map[string]any{"jwt_validation_pubkeys": []string{key.PublicPEM()}, "bound_issuer": "https://firstlume.test"})
	v.must("POST", "sys/mounts/"+v.id+"-kv1", map[string]any{"type": "kv", "options": map[string]string{"version": "1"}})
	t.Cleanup(func() { v.call("DELETE", "sys/mounts/"+v.id+"-kv1", nil) })
	t.Cleanup(func() { v.call("DELETE", "secret/metadata/"+v.id+"?recursive=true", nil) })
	return v
}

func (v *testVault) call(method, path string, body any) (int, string) {
	var rd io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, v.addr+"/v1/"+path, rd)
	req.Header.Set("X-Vault-Token", v.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		v.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

func (v *testVault) must(method, path string, body any) {
	v.t.Helper()
	if code, raw := v.call(method, path, body); code >= 300 {
		v.t.Fatalf("%s %s: %d %s", method, path, code, raw)
	}
}

// role creates a role whose policy reads the given secret paths (API paths).
func (v *testVault) role(name string, claims map[string]any, paths ...string) {
	v.t.Helper()
	var policy strings.Builder
	for _, p := range paths {
		policy.WriteString(`path "` + p + `" { capabilities = ["read"] }` + "\n")
	}
	v.must("PUT", "sys/policies/acl/"+v.id+"-"+name, map[string]any{"policy": policy.String()})
	v.t.Cleanup(func() { v.call("DELETE", "sys/policies/acl/"+v.id+"-"+name, nil) })
	v.must("POST", "auth/"+v.id+"/role/"+name, map[string]any{
		"role_type": "jwt", "user_claim": "sub", "bound_audiences": []string{"vault"},
		"bound_claims_type": "glob", "bound_claims": claims,
		"token_policies": []string{v.id + "-" + name}, "token_ttl": "5m",
	})
}

// spec signs a run JWT for a stack and phase and returns the phase's Vault login.
func (v *testVault) spec(role, stackPath, phase string) *api.ExecVault {
	v.t.Helper()
	now := time.Now()
	jwt, err := v.key.Sign(map[string]any{
		"iss": "https://firstlume.test", "aud": "vault", "sub": "stack:test", "stack_path": stackPath, "phase": phase,
		"iat": now.Unix(), "exp": now.Add(15 * time.Minute).Unix(),
	})
	if err != nil {
		v.t.Fatal(err)
	}
	return &api.ExecVault{Address: v.addr, AuthPath: v.id, Role: role, JWT: jwt,
		HashKey: base64.RawURLEncoding.EncodeToString([]byte("hash-key-of-the-run"))}
}

const testKey = "-----BEGIN OPENSSH PRIVATE KEY-----\nAAAAC3NzaC1lZDI1NTE5AAAAIOv4ul3l2z1kD2\nAAAAQGQ0YzE2ZjQ1ZTk4NzIxNDZjODA0YzE5\n-----END OPENSSH PRIVATE KEY-----\n"

// TestVaultSource covers CAF2.1 to CAF2.3, CAF2.5, CAF2.6 and CAF2.8 against
// a real Vault: values from KV v2 and KV v1 into files and variables, masked;
// a role that can't read prod; a role bound to the apply phase; missing
// fields, roles and Vault; the apply detecting a changed secret.
func TestVaultSource(t *testing.T) {
	v := newTestVault(t)
	ctx := context.Background()
	for _, n := range []string{"n01", "n02"} {
		v.must("POST", "secret/data/"+v.id+"/dev/"+n, map[string]any{"data": map[string]any{"ip": "10.1.0." + n[2:], "port": 2222}})
	}
	v.must("POST", "secret/data/"+v.id+"/dev/ssh", map[string]any{"data": map[string]any{"private_key": testKey}})
	v.must("POST", "secret/data/"+v.id+"/prod/n01", map[string]any{"data": map[string]any{"ip": "10.2.0.1"}})
	v.must("POST", v.id+"-kv1/legacy", map[string]any{"token": "kv1-s3cr3t-token"})
	v.role("dev", map[string]any{"stack_path": []string{"dev/*"}}, "secret/data/"+v.id+"/dev/*", v.id+"-kv1/*")
	v.role("apply-only", map[string]any{"phase": "apply"}, "secret/data/"+v.id+"/dev/*")

	root := t.TempDir()
	mkdirs(t, root, "n01", "n02")
	d := declaration(t, `
files:
  - path: n0*/connection.hcl
    format: hcl
    block: locals
    values:
      host: { vault: "secret/`+v.id+`/dev/${name}", field: ip }
      port: { vault: "secret/`+v.id+`/dev/${name}", field: port }
  - path: ssh/id
    format: raw
    content: { vault: secret/`+v.id+`/dev/ssh, field: private_key }
terraform:
  legacy_token: { vault: `+v.id+`-kv1/legacy, field: token }
`)
	masker := NewMasker(nil)
	src := newVaultSource(v.spec("dev", "dev/machines", "plan"), masker)
	var log bytes.Buffer
	p, err := prepare(ctx, root, t.TempDir(), d, nil, nil, src, masker, &log)
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(root, "n02/connection.hcl")); got != "locals {\n  host = \"10.1.0.2\"\n  port = \"2222\"\n}\n" {
		t.Fatalf("n02/connection.hcl:\n%s", got)
	}
	if readFile(t, filepath.Join(root, "ssh/id")) != testKey {
		t.Fatal("the key from Vault changed")
	}
	for _, f := range []string{"n01/connection.hcl", "ssh/id"} {
		if info, _ := os.Stat(filepath.Join(root, f)); info.Mode().Perm() != 0o600 {
			t.Fatalf("%s from Vault is not private: %v", f, info.Mode())
		}
	}
	if !strings.Contains(strings.Join(p.env, "\n"), "TF_VAR_legacy_token=kv1-s3cr3t-token") {
		t.Fatalf("env %v", p.env)
	}
	for _, secret := range []string{"10.1.0.2", "kv1-s3cr3t-token", "AAAAC3NzaC1lZDI1NTE5AAAAIOv4ul3l2z1kD2", src.client.Token(), src.spec.JWT} {
		if masked := masker.Mask("x " + secret + " y"); strings.Contains(masked, secret) {
			t.Fatalf("not masked: %s", masked)
		}
	}
	if strings.Contains(log.String(), "10.1.0") || len(src.hashes) != 6 {
		t.Fatalf("log %s / hashes %v", log.String(), src.hashes)
	}

	// The apply reads again; a secret changed since the plan is detected (CAF2.5).
	planHashes := src.hashes
	token := src.client.Token()
	if err := src.revoke(ctx); err != nil {
		t.Fatal(err)
	}
	if code, _ := (&testVault{t: t, addr: v.addr, token: token}).call("GET", "auth/token/lookup-self", nil); code != http.StatusForbidden {
		t.Fatalf("the revoked token still works: %d", code)
	}
	again := func() *vaultSource {
		root := t.TempDir()
		mkdirs(t, root, "n01", "n02")
		s := newVaultSource(v.spec("dev", "dev/machines", "apply"), NewMasker(nil))
		if _, err := prepare(ctx, root, t.TempDir(), d, nil, nil, s, NewMasker(nil), io.Discard); err != nil {
			t.Fatal(err)
		}
		return s
	}
	if changed := again().changedSince(planHashes); len(changed) != 0 {
		t.Fatalf("nothing changed, but %v", changed)
	}
	v.must("POST", "secret/data/"+v.id+"/dev/n02", map[string]any{"data": map[string]any{"ip": "10.1.0.99", "port": 2222}})
	if changed := again().changedSince(planHashes); strings.Join(changed, ",") != "secret/"+v.id+"/dev/n02#ip" {
		t.Fatalf("changed %v", changed)
	}

	refuse := func(name string, spec *api.ExecVault, decl, want string, unwanted ...string) {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			_, err := prepare(ctx, root, t.TempDir(), declaration(t, decl), nil, nil, newVaultSource(spec, NewMasker(nil)), NewMasker(nil), io.Discard)
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("got %v, want %q", err, want)
			}
			for _, u := range unwanted {
				if strings.Contains(err.Error(), u) {
					t.Fatalf("the error shows %q: %v", u, err)
				}
			}
		})
	}
	prodFile := "files:\n  - path: a.txt\n    format: raw\n    content: {vault: secret/" + v.id + "/prod/n01, field: ip}\n"
	devFile := "files:\n  - path: a.txt\n    format: raw\n    content: {vault: secret/" + v.id + "/dev/n01, field: ip}\n"
	refuse("prod with the dev role", v.spec("dev", "dev/machines", "plan"), prodFile, "secret/"+v.id+"/prod/n01: Vault answered 403", "10.2.0.1")
	refuse("role bound to apply", v.spec("apply-only", "dev/machines", "plan"), devFile, "logging in to Vault with role apply-only")
	refuse("stack outside dev", v.spec("dev", "prod/machines", "plan"), devFile, "logging in to Vault with role dev")
	refuse("missing field", v.spec("dev", "dev/machines", "plan"),
		"files:\n  - path: a.txt\n    format: raw\n    content: {vault: secret/"+v.id+"/dev/n01, field: nope}\n", "has no field nope")
	refuse("missing secret", v.spec("dev", "dev/machines", "plan"),
		"files:\n  - path: a.txt\n    format: raw\n    content: {vault: secret/"+v.id+"/dev/n09, field: ip}\n", "dev/n09")
	refuse("no role", v.spec("", "dev/machines", "plan"), devFile, "no Vault role is configured")
	down := v.spec("dev", "dev/machines", "plan")
	down.Address = "http://127.0.0.1:1"
	refuse("Vault down", down, devFile, "can't reach Vault")
}

// TestVaultPathStaysInPlace covers CAF2.8: once ${dir} and ${name} are
// replaced, a Vault path can't climb out of what was declared. Directory
// names can't hold "/", and the source refuses ".." and empty segments.
func TestVaultPathStaysInPlace(t *testing.T) {
	src := newVaultSource(&api.ExecVault{Address: "http://127.0.0.1:1", Role: "r"}, NewMasker(nil))
	for _, p := range []string{"secret/dev/../prod/n01", "secret/dev//n01", "secret/./x"} {
		if _, err := src.read(context.Background(), p, "ip"); err == nil || !strings.Contains(err.Error(), "is not valid") {
			t.Errorf("%s: %v", p, err)
		}
	}
}
