package executor

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bsteigleder/firstlume/internal/api"
)

func TestTerraformTokenVar(t *testing.T) {
	for host, want := range map[string]string{
		"app.terraform.io":            "TF_TOKEN_app_terraform_io",
		"tf-registry.example.com":     "TF_TOKEN_tf__registry_example_com",
		"Registry.Example-Corp.local": "TF_TOKEN_registry_example__corp_local",
	} {
		if got := terraformTokenVar(host); got != want {
			t.Errorf("%s: %s, want %s", host, got, want)
		}
	}
}

// TestRegistryCredentials covers CAF3.2 to CAF3.4 without a registry: the
// files and variables written, the settings winning per host, and masking.
func TestRegistryCredentials(t *testing.T) {
	home := t.TempDir()
	d := declaration(t, `
registries:
  - host: harbor.example.com
    username: from-the-repository
    password: repository-password
  - host: ghcr.io
    username: bot
    password: { var: GHCR_TOKEN }
  - host: tf-registry.example.com
    kind: terraform
    password: terraform-token-1
`)
	vars := []api.ExecVariable{{Key: "GHCR_TOKEN", Category: api.CategoryEnv, Sensitive: true, Value: "ghcr-token-123"}}
	settings := []api.ExecRegistry{{Host: "harbor.example.com", Kind: "oci", Username: "robot$infra", Password: "harbor-password-1"},
		{Host: "docker.io", Kind: "oci", Username: "me", Password: "hub-password-1"}}
	masker := NewMasker(nil)
	var log bytes.Buffer
	p, err := prepare(context.Background(), t.TempDir(), home, d, vars, settings, newVaultSource(nil, masker), masker, &log)
	if err != nil {
		t.Fatal(err)
	}
	env := strings.Join(p.env, "\n")
	dockerDir := filepath.Join(home, ".docker")
	helmConfig := filepath.Join(home, ".config/helm/registry/config.json")
	for _, want := range []string{"DOCKER_CONFIG=" + dockerDir, "HELM_REGISTRY_CONFIG=" + helmConfig,
		"TF_TOKEN_tf__registry_example_com=terraform-token-1"} {
		if !strings.Contains(env, want) {
			t.Errorf("env lacks %s:\n%s", want, env)
		}
	}
	var cfg struct {
		Auths map[string]struct{ Auth string } `json:"auths"`
	}
	raw := readFile(t, filepath.Join(dockerDir, "config.json"))
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	decode := func(host string) string {
		b, _ := base64.StdEncoding.DecodeString(cfg.Auths[host].Auth)
		return string(b)
	}
	if decode("harbor.example.com") != "robot$infra:harbor-password-1" || decode("ghcr.io") != "bot:ghcr-token-123" ||
		decode("https://index.docker.io/v1/") != "me:hub-password-1" || len(cfg.Auths) != 4 {
		t.Fatalf("config.json %s", raw)
	}
	if readFile(t, helmConfig) != raw {
		t.Fatal("Helm's registry config differs from Docker's")
	}
	if info, _ := os.Stat(filepath.Join(dockerDir, "config.json")); info.Mode().Perm() != 0o600 {
		t.Fatalf("config.json mode %v", info.Mode())
	}
	if !strings.Contains(log.String(), "registry harbor.example.com is also set in the stack's settings, which win") {
		t.Fatalf("log:\n%s", log.String())
	}
	for _, secret := range []string{"harbor-password-1", "terraform-token-1", "ghcr-token-123", cfg.Auths["harbor.example.com"].Auth} {
		if masked := masker.Mask("x " + secret + " y"); strings.Contains(masked, secret) || strings.Contains(log.String(), secret) {
			t.Fatalf("%s is not masked: %s", secret, masked)
		}
	}
}

// TestRegistryCredentialsWithHelm covers CAF3.1 and CAF3.4 with a real
// registry (make dev-registry) and Helm reading only what Firstlume wrote:
// the pull works with the credential, from settings or from Vault, and fails
// without it.
func TestRegistryCredentialsWithHelm(t *testing.T) {
	host, user, password := os.Getenv("FIRSTLUME_TEST_REGISTRY"), os.Getenv("FIRSTLUME_TEST_REGISTRY_USER"), os.Getenv("FIRSTLUME_TEST_REGISTRY_PASSWORD")
	if host == "" || password == "" {
		t.Skip("set FIRSTLUME_TEST_REGISTRY, _USER and _PASSWORD (make dev-registry) to run this test")
	}
	helm := testTool(t, "FIRSTLUME_TEST_HELM", "helm")
	pull := func(registries []api.ExecRegistry, vault *vaultSource) (string, error) {
		home := t.TempDir()
		masker := NewMasker(nil)
		if vault == nil {
			vault = newVaultSource(nil, masker)
		}
		p, err := prepare(context.Background(), t.TempDir(), home, nil, nil, registries, vault, masker, &bytes.Buffer{})
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(helm, "pull", "oci://"+host+"/charts/hello", "--version", "0.1.0", "--plain-http", "-d", t.TempDir())
		cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + home,
			"HELM_REGISTRY_CONFIG=" + filepath.Join(home, "none.json")}, p.env...)
		out, err := cmd.CombinedOutput()
		return masker.Mask(string(out)), err
	}

	if out, err := pull(nil, nil); err == nil {
		t.Fatalf("pulled without a credential:\n%s", out)
	}
	if out, err := pull([]api.ExecRegistry{{Host: host, Kind: "oci", Username: user, Password: "wrong"}}, nil); err == nil {
		t.Fatalf("pulled with a wrong password:\n%s", out)
	}
	if out, err := pull([]api.ExecRegistry{{Host: host, Kind: "oci", Username: user, Password: password}}, nil); err != nil {
		t.Fatalf("pull with the credential: %v\n%s", err, out)
	}

	if os.Getenv("FIRSTLUME_TEST_VAULT_ADDR") == "" {
		return
	}
	v := newTestVault(t)
	v.must("POST", "secret/data/"+v.id+"/registry", map[string]any{"data": map[string]any{"password": password}})
	v.role("ci", map[string]any{"stack_path": "*"}, "secret/data/"+v.id+"/*")
	fromVault := []api.ExecRegistry{{Host: host, Kind: "oci", Username: user,
		PasswordVault: &api.VaultRef{Path: "secret/" + v.id + "/registry", Field: "password"}}}
	masker := NewMasker(nil)
	if out, err := pull(fromVault, newVaultSource(v.spec("ci", "dev/charts", "plan"), masker)); err != nil {
		t.Fatalf("pull with the password from Vault: %v\n%s", err, out)
	}
}
