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
