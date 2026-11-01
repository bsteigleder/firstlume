package server

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bsteigleder/firstlume/internal/api"
	"github.com/bsteigleder/firstlume/internal/vault"
)

const testIssuer = "https://firstlume.test"

// vaultAdmin configures a real Vault (make dev-vault) with its root token.
type vaultAdmin struct {
	t           *testing.T
	addr, token string
	id          string
}

func newVaultAdmin(t *testing.T) *vaultAdmin {
	addr, token := os.Getenv("FIRSTLUME_TEST_VAULT_ADDR"), os.Getenv("FIRSTLUME_TEST_VAULT_TOKEN")
	if addr == "" || token == "" {
		t.Skip("set FIRSTLUME_TEST_VAULT_ADDR and FIRSTLUME_TEST_VAULT_TOKEN (make dev-vault) to run this test")
	}
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return &vaultAdmin{t: t, addr: addr, token: token, id: "s" + hex.EncodeToString(b)}
}

func (v *vaultAdmin) must(method, path string, body any) {
	v.t.Helper()
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(method, v.addr+"/v1/"+path, bytes.NewReader(raw))
	req.Header.Set("X-Vault-Token", v.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		v.t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		out, _ := io.ReadAll(resp.Body)
		v.t.Fatalf("%s %s: %d %s", method, path, resp.StatusCode, out)
	}
}

func (v *vaultAdmin) cleanup(method, path string) {
	v.t.Cleanup(func() {
		req, _ := http.NewRequest(method, v.addr+"/v1/"+path, nil)
		req.Header.Set("X-Vault-Token", v.token)
		if resp, err := http.DefaultClient.Do(req); err == nil {
			resp.Body.Close()
		}
	})
}

// TestVaultIntegration covers CAF2.4 and CAF2.7 and the server's side of
// CAF2.1 to CAF2.3: the run JWT, its public key, the inherited role, the
// Vault connection and the permission check, against a real Vault.
func TestVaultIntegration(t *testing.T) {
	v := newVaultAdmin(t)
	h := newHarnessWith(t, Config{PublicURL: testIssuer})
	ctx := context.Background()

	// Discovery and keys, without authentication (R3.4).
	resp, err := http.Get(h.url + "/.well-known/openid-configuration")
	if err != nil {
		t.Fatal(err)
	}
	var disc map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&disc)
	resp.Body.Close()
	if disc["issuer"] != testIssuer || disc["jwks_uri"] != testIssuer+"/.well-known/jwks.json" {
		t.Fatalf("discovery %v", disc)
	}
	resp, err = http.Get(h.url + "/.well-known/jwks.json")
	if err != nil {
		t.Fatal(err)
	}
	var jwks struct {
		Keys []map[string]string `json:"keys"`
	}
	rawJWKS, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err := json.Unmarshal(rawJWKS, &jwks); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("jwks: %d %s\n%s", resp.StatusCode, rawJWKS, h.logs.String())
	}
	pemKey := h.text("/vault/public-key")
	block, _ := pem.Decode([]byte(pemKey))
	pubAny, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil || len(jwks.Keys) != 1 || jwks.Keys[0]["alg"] != "ES256" {
		t.Fatalf("keys: %v %+v", err, jwks)
	}
	pub := pubAny.(*ecdsa.PublicKey)

	// The connection is validated, then stored.
	for _, bad := range []api.PutVaultConnectionRequest{{Address: "vault:8200"}, {Address: "https://v.example.com/v1"},
		{Address: "https://v.example.com", CACert: "not a pem"}, {Address: "https://v.example.com", AuthPath: "../x"}} {
		h.call("PUT", "/vault", bad, http.StatusUnprocessableEntity, nil)
	}
	h.call("GET", "/vault", nil, http.StatusNotFound, nil)
	var conn api.VaultConnection
	h.call("PUT", "/vault", api.PutVaultConnectionRequest{Address: v.addr + "/", AuthPath: v.id}, http.StatusOK, &conn)
	if conn.Address != v.addr || conn.Audience != "vault" || conn.AuthPath != v.id {
		t.Fatalf("connection %+v", conn)
	}

	// Vault trusts this server's key; the role dev reads only dev's secrets.
	v.must("POST", "sys/auth/"+v.id, map[string]any{"type": "jwt"})
	v.cleanup("DELETE", "sys/auth/"+v.id)
	v.must("POST", "auth/"+v.id+"/config", map[string]any{"jwt_validation_pubkeys": []string{pemKey}, "bound_issuer": testIssuer})
	v.must("POST", "secret/data/"+v.id+"/dev/n01", map[string]any{"data": map[string]any{"ip": "10.1.0.1"}})
	v.must("POST", "secret/data/"+v.id+"/prod/n01", map[string]any{"data": map[string]any{"ip": "10.2.0.1"}})
	v.cleanup("DELETE", "secret/metadata/"+v.id+"?recursive=true")
	v.must("PUT", "sys/policies/acl/"+v.id+"-dev", map[string]any{"policy": `path "secret/data/` + v.id + `/dev/*" { capabilities = ["read"] }`})
	v.cleanup("DELETE", "sys/policies/acl/"+v.id+"-dev")
	role := func(name string, claims map[string]any) {
		v.must("POST", "auth/"+v.id+"/role/"+name, map[string]any{
			"role_type": "jwt", "user_claim": "sub", "bound_audiences": []string{"vault"}, "bound_claims_type": "glob",
			"bound_claims": claims, "token_policies": []string{v.id + "-dev"}, "token_ttl": "5m"})
	}
	role("dev", map[string]any{"stack_path": []string{"dev/*", "*/dev/*"}})
	role("dev-apply", map[string]any{"stack_path": "*/dev/*", "phase": "apply"})

	// The role is an inherited setting (R3.2).
	infra := h.folder("", "infra")
	dev := h.folder(infra.ID, "dev")
	st := h.stack(dev.ID, "machines", "")
	h.call("PUT", "/folders/"+infra.ID+"/settings", api.LevelSettings{VaultRole: ptr("bad role!")}, http.StatusUnprocessableEntity, nil)
	h.settings("/folders/"+infra.ID, api.LevelSettings{VaultRole: ptr("dev"), RequireApproval: ptr(false)})
	var eff api.SettingsResponse
	h.call("GET", "/stacks/"+st.ID+"/settings", nil, http.StatusOK, &eff)
	if eff.Effective.VaultRole.Value != "dev" || eff.Effective.VaultRole.Origin.ID != infra.ID {
		t.Fatalf("effective role %+v", eff.Effective.VaultRole)
	}

	// The plan phase gets a JWT about its stack (R3.3) that Vault accepts.
	run := h.run(st.ID, api.RunApply)
	exec, spec := h.spec(run.ID)
	if spec.Vault == nil || spec.Vault.Role != "dev" || spec.Vault.AuthPath != v.id || spec.Vault.HashKey == "" {
		t.Fatalf("spec vault %+v", spec.Vault)
	}
	claims, err := vault.VerifyES256(spec.Vault.JWT, pub)
	if err != nil {
		t.Fatal(err)
	}
	exp := time.Unix(int64(claims["exp"].(float64)), 0)
	if claims["iss"] != testIssuer || claims["aud"] != "vault" || claims["sub"] != "stack:"+st.ID || claims["stack_path"] != "infra/dev/machines" ||
		claims["phase"] != "plan" || claims["run_id"] != run.ID || claims["org"] != h.org || time.Until(exp) > 15*time.Minute || time.Until(exp) < 14*time.Minute {
		t.Fatalf("claims %v", claims)
	}
	if ids := claims["folder_ids"].([]any); len(ids) != 2 || ids[1] != dev.ID {
		t.Fatalf("folder ids %v", ids)
	}
	c, _ := vault.New(vault.Config{Address: v.addr})
	if err := c.Login(ctx, v.id, "dev", spec.Vault.JWT); err != nil {
		t.Fatal(err)
	}
	if data, err := c.ReadSecret(ctx, "secret/"+v.id+"/dev/n01"); err != nil || data["ip"] != "10.1.0.1" {
		t.Fatalf("read with the run's token: %v %v", data, err)
	}
	if _, err := c.ReadSecret(ctx, "secret/"+v.id+"/prod/n01"); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("the dev role read prod: %v", err)
	}
	refused, _ := vault.New(vault.Config{Address: v.addr})
	if err := refused.Login(ctx, v.id, "dev-apply", spec.Vault.JWT); err == nil {
		t.Fatal("the apply-only role accepted a plan's JWT")
	}

	// The plan's hashes reach its apply (R3.8), and the apply's JWT says so.
	hashes := []byte(`{"secret/x#ip":"abc"}`)
	if err := exec.PutArtifact(ctx, api.VaultHashesArtifact, hashes); err != nil {
		t.Fatal(err)
	}
	planKey := spec.Vault.HashKey
	if _, err := exec.Complete(ctx, api.CompleteRequest{Success: true, HasChanges: true}); err != nil {
		t.Fatal(err)
	}
	applyExec, applySpec := h.spec(run.ID)
	if got, err := applyExec.GetArtifact(ctx, api.VaultHashesArtifact); err != nil || string(got) != string(hashes) {
		t.Fatalf("apply's view of the plan's hashes: %s %v", got, err)
	}
	applyClaims, _ := vault.VerifyES256(applySpec.Vault.JWT, pub)
	if applyClaims["phase"] != "apply" || applySpec.Vault.HashKey != planKey {
		t.Fatalf("apply claims %v, same hash key %v", applyClaims, applySpec.Vault.HashKey == planKey)
	}
	if err := c.Login(ctx, v.id, "dev-apply", applySpec.Vault.JWT); err != nil {
		t.Fatalf("the apply-only role refused the apply: %v", err)
	}

	// The check says what the role may read, without reading (CAF2.7).
	var check api.VaultCheckResult
	h.call("POST", "/stacks/"+st.ID+"/vault/check", api.VaultCheckRequest{
		Paths: []string{"secret/" + v.id + "/dev/n01", "secret/" + v.id + "/prod/n01"}}, http.StatusOK, &check)
	if check.Error != "" || len(check.Paths) != 2 || !check.Paths[0].CanRead || check.Paths[1].CanRead {
		t.Fatalf("check %+v", check)
	}
	h.settings("/stacks/"+st.ID, api.LevelSettings{VaultRole: ptr("nope")})
	h.call("POST", "/stacks/"+st.ID+"/vault/check", api.VaultCheckRequest{Paths: []string{"secret/x"}}, http.StatusOK, &check)
	if !strings.Contains(check.Error, "login with role nope failed") {
		t.Fatalf("check with a missing role %+v", check)
	}

	// Without a connection, phases get no Vault login.
	h.call("DELETE", "/vault", nil, http.StatusNoContent, nil)
	other := h.stack(dev.ID, "other", "")
	_, spec = h.spec(h.run(other.ID, api.RunPlan).ID)
	if spec.Vault != nil {
		t.Fatalf("a phase got Vault without a connection: %+v", spec.Vault)
	}
	if strings.Contains(h.logs.String(), spec.StateToken[:20]) || strings.Contains(h.logs.String(), applySpec.Vault.JWT[:40]) {
		t.Fatal("a token reached the server log")
	}
}
