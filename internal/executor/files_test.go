package executor

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/bsteigleder/firstlume/internal/api"
	"github.com/bsteigleder/firstlume/internal/stackconfig"
)

const configBase = "version: 1\nengine: terraform\nengine_version: 1.15.6\n"

// tricky has everything a format must escape: quotes, backslashes, newlines,
// template sequences, accents and a value that looks like another type.
const tricky = "a \"quoted\" \\ ${not.a.ref} %{if x} é\nsecond line: yes"

func declaration(t *testing.T, yamlText string) *stackconfig.Declaration {
	t.Helper()
	_, d, err := stackconfig.ParseDeclaration([]byte(configBase + yamlText))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func mkdirs(t *testing.T, root string, dirs ...string) {
	t.Helper()
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

const privateKey = "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQ\nAAAAMwAAAAtzc2gtZWQyNTUxOQAAACD3x\n-----END OPENSSH PRIVATE KEY-----\n"

func TestPrepareWritesFiles(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "sp1/n01", "sp1/n02", "sp2/n03", "sp1/.terragrunt-cache", "docs")
	d := declaration(t, `
files:
  - path: sp*/*/connection.hcl
    format: hcl
    block: locals
    values:
      host: "${name}.example"
      dir: "${dir}"
      user: { var: SSH_USER }
      port: 22
      note: { text: `+jsonString(tricky)+` }
  - path: vars.auto.tfvars.json
    format: json
    values: { zone: sp1, sizes: [1, 2.5], on: true, none: null }
  - path: app/config.yaml
    format: yaml
    values: { answer: "yes", count: 3 }
  - path: app/.env
    format: env
    values: { GREETING: "it's", EMPTY: null }
  - path: ssh/id_ed25519
    format: raw
    content: { var: SSH_KEY }
env:
  TOKEN: from-the-repository
  EXTRA: "1"
terraform:
  zone: sp1
  limits: { cpu: 2 }
  unset: null
`)
	vars := []api.ExecVariable{
		{Key: "SSH_USER", Category: api.CategoryTerraform, Value: "root"},
		{Key: "SSH_KEY", Category: api.CategoryEnv, Sensitive: true, Value: privateKey},
		{Key: "TOKEN", Category: api.CategoryEnv, Value: "from-the-settings"},
		{Key: "certs/ca.pem", Category: api.CategoryFile, Value: "CA"},
		{Key: "certs/client.key", Category: api.CategoryFile, Sensitive: true, Value: "client-key-line-1\nclient-key-line-2\n"},
	}
	masker := NewMasker(nil)
	var log bytes.Buffer
	p, err := prepare(root, d, vars, masker, &log)
	if err != nil {
		t.Fatal(err)
	}

	// One connection.hcl per machine directory, none in hidden or unrelated ones.
	for _, n := range []string{"sp1/n01", "sp1/n02", "sp2/n03"} {
		got := readFile(t, filepath.Join(root, n, "connection.hcl"))
		name := filepath.Base(n)
		if !strings.HasPrefix(got, "locals {\n  host = \""+name+".example\"\n  dir = \""+n+"\"\n  user = \"root\"\n  port = 22\n") {
			t.Fatalf("%s/connection.hcl:\n%s", n, got)
		}
	}
	for _, missing := range []string{"sp1/.terragrunt-cache/connection.hcl", "docs/connection.hcl"} {
		if _, err := os.Stat(filepath.Join(root, missing)); err == nil {
			t.Fatalf("%s should not be written", missing)
		}
	}
	if len(p.files) != 9 {
		t.Fatalf("written files %v", p.files)
	}

	var tfvars map[string]any
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(root, "vars.auto.tfvars.json"))), &tfvars); err != nil {
		t.Fatal(err)
	}
	if tfvars["zone"] != "sp1" || tfvars["on"] != true || tfvars["none"] != nil || tfvars["sizes"].([]any)[1] != 2.5 {
		t.Fatalf("tfvars %v", tfvars)
	}
	var cfg map[string]any
	if err := yaml.Unmarshal([]byte(readFile(t, filepath.Join(root, "app/config.yaml"))), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg["answer"] != "yes" || cfg["count"] != 3 {
		t.Fatalf("yaml %v", cfg)
	}
	cmd := exec.Command("sh", "-c", `. ./app/.env && printf '%s|%s' "$GREETING" "$EMPTY"`)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil || string(out) != "it's|" {
		t.Fatalf("env file read by the shell: %q %v", out, err)
	}

	// The key is written as is, private, and masked in the log.
	key := filepath.Join(root, "ssh/id_ed25519")
	if readFile(t, key) != privateKey {
		t.Fatal("raw content changed")
	}
	if info, _ := os.Stat(key); info.Mode().Perm() != 0o600 {
		t.Fatalf("sensitive file mode %v", info.Mode())
	}
	if info, _ := os.Stat(filepath.Join(root, "vars.auto.tfvars.json")); info.Mode().Perm() != 0o644 {
		t.Fatalf("file mode %v", info.Mode())
	}
	if masked := masker.Mask(privateKey); strings.Contains(masked, "b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQ") {
		t.Fatalf("the key is not masked: %s", masked)
	}
	if readFile(t, filepath.Join(root, "certs/ca.pem")) != "CA" {
		t.Fatal("the file variable was not written")
	}
	if info, _ := os.Stat(filepath.Join(root, "certs/client.key")); info.Mode().Perm() != 0o600 {
		t.Fatalf("sensitive file variable mode %v", info.Mode())
	}
	if masked := masker.Mask("cat: client-key-line-2"); masked != "cat: ***" {
		t.Fatalf("a sensitive file variable is not masked: %s", masked)
	}

	// Declared variables: the settings win for TOKEN, and say so.
	env := strings.Join(p.env, "\n")
	for _, want := range []string{"EXTRA=1", "TF_VAR_zone=sp1", `TF_VAR_limits={"cpu":2}`} {
		if !strings.Contains(env, want) {
			t.Errorf("env %q lacks %s", env, want)
		}
	}
	if strings.Contains(env, "TOKEN=") || strings.Contains(env, "TF_VAR_unset") {
		t.Fatalf("env %q", env)
	}
	if !strings.Contains(log.String(), "env TOKEN is also set in the stack's settings, which win") ||
		strings.Contains(log.String(), "from-the-settings") || strings.Contains(log.String(), "from-the-repository") {
		t.Fatalf("log:\n%s", log.String())
	}
}

func TestPrepareRefuses(t *testing.T) {
	cases := map[string]struct {
		decl string
		vars []api.ExecVariable
		prep func(t *testing.T, root string)
		want string
	}{
		"pattern without matches": {
			decl: "files:\n  - path: zone*/*/c.hcl\n    format: raw\n    content: x\n",
			want: "no directory matches zone*/*",
		},
		"file in the repository": {
			decl: "files:\n  - path: main.tf\n    format: raw\n    content: x\n",
			prep: func(t *testing.T, root string) { must(t, os.WriteFile(filepath.Join(root, "main.tf"), nil, 0o644)) },
			want: "already exists in the repository",
		},
		"written twice": {
			decl: "files:\n  - path: a.txt\n    format: raw\n    content: x\n",
			vars: []api.ExecVariable{{Key: "a.txt", Category: api.CategoryFile, Value: "y"}},
			want: "a.txt is written by both",
		},
		"symbolic link out of the stack": {
			decl: "files:\n  - path: out/a.txt\n    format: raw\n    content: x\n",
			prep: func(t *testing.T, root string) { must(t, os.Symlink(t.TempDir(), filepath.Join(root, "out"))) },
			want: "points outside",
		},
		"missing variable": {
			decl: "terraform:\n  a: {var: NOPE}\n",
			want: "variable NOPE is not set",
		},
		"vault before F2": {
			decl: "files:\n  - path: a.txt\n    format: raw\n    content: {vault: secret/a, field: b}\n",
			want: "Vault connection",
		},
		"registries before F3": {
			decl: "registries:\n  - host: h.example.com\n    username: u\n    password: p\n",
			want: "Marco F3",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if c.prep != nil {
				c.prep(t, root)
			}
			_, err := prepare(root, declaration(t, c.decl), c.vars, NewMasker(nil), &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want an error with %q", err, c.want)
			}
			if name != "file in the repository" {
				if _, err := os.Stat(filepath.Join(root, "a.txt")); err == nil {
					t.Fatal("a refused preparation wrote files")
				}
			}
		})
	}
}

// jsonString quotes s for YAML: a JSON string is a valid YAML string.
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func testTool(t *testing.T, envVar, name string) string {
	t.Helper()
	if p := os.Getenv(envVar); p != "" {
		return p
	}
	p, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("%s not found: set %s to run this test", name, envVar)
	}
	return p
}

// TestPreparedFilesWithRealTools covers CAF1.1, CAF1.3 and CAF1.5: Terragrunt
// reads the generated connection.hcl with read_terragrunt_config, Terraform
// reads the JSON tfvars and the declared TF_VAR_, and every value arrives
// exactly as declared.
func TestPreparedFilesWithRealTools(t *testing.T) {
	tg := testTool(t, "FIRSTLUME_TEST_TERRAGRUNT", "terragrunt")
	tf := testTool(t, "FIRSTLUME_TEST_TERRAFORM", "terraform")
	root := t.TempDir()
	unit := `
locals {
  c = read_terragrunt_config("connection.hcl")
}
inputs = {
  host = local.c.locals.host
  port = local.c.locals.port
  note = local.c.locals.note
}
`
	module := `
variable "host" { type = string }
variable "port" { type = number }
variable "note" { type = string }
variable "zone" { type = string }
variable "limits" { type = map(number) }
variable "sizes" { type = list(number) }
output "all" {
  value = { host = var.host, port = var.port, note = var.note, zone = var.zone, limits = var.limits, sizes = var.sizes }
}
`
	for _, n := range []string{"sp1/n01", "sp2/n02"} {
		mkdirs(t, root, n)
		must(t, os.WriteFile(filepath.Join(root, n, "terragrunt.hcl"), []byte(unit), 0o644))
		must(t, os.WriteFile(filepath.Join(root, n, "main.tf"), []byte(module), 0o644))
	}
	d := declaration(t, `
files:
  - path: sp*/*/connection.hcl
    format: hcl
    block: locals
    values:
      host: "${name}.example"
      port: 22
      note: `+jsonString(tricky)+`
  - path: sp*/*/sizes.auto.tfvars.json
    format: json
    values: { sizes: [1, 2.5] }
terraform:
  zone: { var: ZONE }
  limits: { cpu: 2, memory: 4096 }
`)
	p, err := prepare(root, d, []api.ExecVariable{{Key: "ZONE", Category: api.CategoryEnv, Value: tricky}}, NewMasker(nil), &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	env := append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "TG_TF_PATH=" + tf,
		"TF_IN_AUTOMATION=1", "CHECKPOINT_DISABLE=1", "TG_NON_INTERACTIVE=true", "NO_COLOR=1", "TG_NO_COLOR=true"}, p.env...)
	run := func(dir string, args ...string) string {
		cmd := exec.Command(tg, args...)
		cmd.Dir, cmd.Env = dir, env
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("terragrunt %s: %v\n%s%s", strings.Join(args, " "), err, out, stderr.String())
		}
		return string(out)
	}
	run(root, "run", "--all", "--non-interactive", "--", "apply", "-input=false", "-auto-approve")
	for _, n := range []string{"sp1/n01", "sp2/n02"} {
		var got struct {
			Host   string             `json:"host"`
			Port   int                `json:"port"`
			Note   string             `json:"note"`
			Zone   string             `json:"zone"`
			Limits map[string]float64 `json:"limits"`
			Sizes  []float64          `json:"sizes"`
		}
		out := run(filepath.Join(root, n), "output", "-json", "all")
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("%s output: %v\n%s", n, err, out)
		}
		if got.Host != filepath.Base(n)+".example" || got.Port != 22 || got.Note != tricky || got.Zone != tricky ||
			got.Limits["memory"] != 4096 || len(got.Sizes) != 2 || got.Sizes[1] != 2.5 {
			t.Fatalf("%s got %+v", n, got)
		}
	}
}
