package stackconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	valid := "version: 1\nengine: terraform\nengine_version: 1.15.6\n"
	c, err := Parse([]byte(valid + "terragrunt_version: 0.77.0\nrun_all: true\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Engine != "terraform" || c.EngineVersion != "1.15.6" || c.TerragruntVersion != "0.77.0" || !c.RunAll {
		t.Fatalf("unexpected config %+v", c)
	}

	bad := map[string]string{
		"unknown field":   valid + "triggers: {}\n",
		"typo":            "version: 1\nengine: terraform\nengine_verison: 1.15.6\n",
		"wrong version":   "version: 2\nengine: terraform\nengine_version: 1.15.6\n",
		"missing version": "engine: terraform\nengine_version: 1.15.6\n",
		"bad engine":      "version: 1\nengine: pulumi\nengine_version: 1.15.6\n",
		"loose version":   "version: 1\nengine: opentofu\nengine_version: '1.9'\n",
		"bad terragrunt":  valid + "terragrunt_version: latest\n",
		"run_all alone":   valid + "run_all: true\n",
		"not yaml":        "{{{",
	}
	for name, in := range bad {
		if _, err := Parse([]byte(in)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestLoadNamesTheFile(t *testing.T) {
	dir := t.TempDir()
	_, err := Load(dir, "environments/prod")
	if err == nil || !strings.Contains(err.Error(), "environments/prod/.firstlume/config.yaml not found") {
		t.Fatalf("missing file: %v", err)
	}

	if err := os.MkdirAll(filepath.Join(dir, Dir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, RelPath), []byte("version: 1\nengine: terraform\nengine_version: 1.9\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = Load(dir, "environments/prod")
	if err == nil || !strings.Contains(err.Error(), "environments/prod/.firstlume/config.yaml: engine_version") {
		t.Fatalf("invalid file: %v", err)
	}
}

const base = "version: 1\nengine: terraform\nengine_version: 1.15.6\n"

func TestParseDeclaration(t *testing.T) {
	c, d, err := ParseDeclaration([]byte(base + `
files:
  - path: sp*/*/connection.hcl
    format: hcl
    block: locals
    values:
      host: { vault: "secret/infra/prod/${name}", field: ip }
      port: 22
      tags: [a, "b"]
      nested: { "with.dot": true, none: null }
  - path: ssh/id_ed25519
    format: raw
    content: { var: SSH_KEY }
    sensitive: true
env:
  TF_LOG: ""
  TOKEN: { var: API_TOKEN }
terraform:
  zone: sp1
  limits: { cpu: 2 }
registries:
  - host: harbor.example.com:8443
    username: robot$infra
    password: { vault: secret/infra/harbor, field: token }
  - host: registry.example.com
    kind: terraform
    password: { var: TF_REGISTRY_TOKEN }
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Files) != 2 || len(d.Env) != 2 || len(d.Terraform) != 2 || len(d.Registries) != 2 {
		t.Fatalf("declaration %+v", d)
	}
	f := d.Files[0]
	if f.Line != 6 || f.Block != "locals" || f.Values.Map[0].Value.Kind != VaultRef || f.Values.Map[1].Value.Text != "22" {
		t.Fatalf("first file %+v", f)
	}
	s := c.Declaration
	if s == nil || !s.Files[0].Sensitive || s.Files[0].Refs[0] != "vault:secret/infra/prod/${name}#ip" ||
		!s.Files[1].Sensitive || s.Files[1].Refs[0] != "var:SSH_KEY" || s.Env[0].Ref != "" || s.Env[1].Ref != "var:API_TOKEN" ||
		s.Registries[0].Refs[0] != "vault:secret/infra/harbor#token" {
		t.Fatalf("summary %+v", s)
	}

	if c, d, err := ParseDeclaration([]byte(base)); err != nil || d != nil || c.Declaration != nil {
		t.Fatalf("a config without a declaration: %+v %+v %v", c, d, err)
	}
}

func TestParseDeclarationErrors(t *testing.T) {
	bad := map[string]string{
		"unknown format":       "files:\n  - path: a.txt\n    format: toml\n    values: {a: 1}\n",
		"no format":            "files:\n  - path: a.txt\n    values: {a: 1}\n",
		"absolute":             "files:\n  - path: /etc/a\n    format: raw\n    content: x\n",
		"dot dot":              "files:\n  - path: a/../../b\n    format: raw\n    content: x\n",
		"git":                  "files:\n  - path: .git/hooks/pre-commit\n    format: raw\n    content: x\n",
		"firstlume":            "files:\n  - path: .firstlume/config.yaml\n    format: raw\n    content: x\n",
		"pattern in name":      "files:\n  - path: a/*.hcl\n    format: raw\n    content: x\n",
		"double star":          "files:\n  - path: '**/a.hcl'\n    format: raw\n    content: x\n",
		"values in raw":        "files:\n  - path: a\n    format: raw\n    values: {a: 1}\n",
		"content in hcl":       "files:\n  - path: a\n    format: hcl\n    content: x\n",
		"values not a map":     "files:\n  - path: a\n    format: json\n    values: [1]\n",
		"bad hcl attribute":    "files:\n  - path: a\n    format: hcl\n    values: {'a b': 1}\n",
		"block outside hcl":    "files:\n  - path: a\n    format: json\n    block: locals\n    values: {a: 1}\n",
		"env file with a list": "files:\n  - path: a\n    format: env\n    values: {A: [1]}\n",
		"unknown file key":     "files:\n  - path: a\n    format: raw\n    content: x\n    mode: '0600'\n",
		"malformed source":     "files:\n  - path: a\n    format: raw\n    content: {vault: secret/a}\n",
		"source with extra":    "files:\n  - path: a\n    format: raw\n    content: {var: A, field: b}\n",
		"vault path dot dot":   "files:\n  - path: a\n    format: raw\n    content: {vault: secret/../x, field: b}\n",
		"reserved env":         "env:\n  TF_HTTP_PASSWORD: x\n",
		"env list":             "env:\n  A: [1]\n",
		"bad tf name":          "terraform:\n  'a b': 1\n",
		"registry no password": "registries:\n  - host: h.example.com\n    username: u\n",
		"registry bad host":    "registries:\n  - host: https://h.example.com\n    username: u\n    password: p\n",
		"terraform with user":  "registries:\n  - host: h.example.com\n    kind: terraform\n    username: u\n    password: p\n",
		"infinite":             "terraform:\n  a: .inf\n",
		"unknown top key":      "secrets: {}\n",
	}
	for name, in := range bad {
		_, _, err := ParseDeclaration([]byte(base + in))
		if err == nil {
			t.Errorf("%s: expected an error", name)
			continue
		}
		if name != "unknown top key" && !strings.Contains(err.Error(), "line ") {
			t.Errorf("%s: the error should point to a line: %v", name, err)
		}
	}
}
