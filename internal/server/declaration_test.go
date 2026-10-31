package server

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/bsteigleder/firstlume/internal/api"
	"github.com/bsteigleder/firstlume/internal/stackconfig"
)

// TestFileVariables covers the settings side of CAF1.4 and CAF1.6: file
// variables are validated, inherited, handed to the executor and frozen by
// the plan.
func TestFileVariables(t *testing.T) {
	h := newHarnessWith(t, Config{})
	ctx := context.Background()
	f := h.folder("", "prod")
	st := h.stack(f.ID, "network", "")

	for _, bad := range []string{"/etc/passwd", "a/../b", ".git/config", ".firstlume/config.yaml", "a/*/b.hcl", ""} {
		h.call("PUT", "/folders/"+f.ID+"/settings", api.LevelSettings{Variables: []api.Variable{
			{Key: bad, Category: api.CategoryFile, Value: "x"}}}, http.StatusUnprocessableEntity, nil)
	}
	h.call("PUT", "/folders/"+f.ID+"/settings", api.LevelSettings{Variables: []api.Variable{
		{Key: "big.txt", Category: api.CategoryFile, Value: strings.Repeat("x", 257<<10)}}}, http.StatusUnprocessableEntity, nil)

	h.settings("/folders/"+f.ID, api.LevelSettings{RequireApproval: ptr(false), Variables: []api.Variable{
		{Key: "certs/ca.pem", Category: api.CategoryFile, Value: "CA v1"},
		{Key: "ssh/id_ed25519", Category: api.CategoryFile, Value: "KEY", Sensitive: true},
	}})
	var eff api.SettingsResponse
	h.call("GET", "/stacks/"+st.ID+"/settings", nil, http.StatusOK, &eff)
	if len(eff.Effective.Variables) != 2 || eff.Effective.Variables[1].Value != "" || eff.Effective.Variables[1].Origin.Kind != api.LevelFolder {
		t.Fatalf("effective file variables %+v", eff.Effective.Variables)
	}

	run := h.run(st.ID, api.RunApply)
	exec, spec := h.spec(run.ID)
	if got := fileVars(spec); got["certs/ca.pem"] != "CA v1" || got["ssh/id_ed25519"] != "KEY" {
		t.Fatalf("plan spec file variables %v", got)
	}
	// Changed after the plan: the apply still gets the plan's (CAF1.6).
	h.settings("/folders/"+f.ID, api.LevelSettings{RequireApproval: ptr(false), Variables: []api.Variable{
		{Key: "certs/ca.pem", Category: api.CategoryFile, Value: "CA v2"}}})
	if _, err := exec.Complete(ctx, api.CompleteRequest{Success: true, HasChanges: true}); err != nil {
		t.Fatal(err)
	}
	_, spec = h.spec(run.ID)
	if got := fileVars(spec); spec.Phase != api.PhaseApply || got["certs/ca.pem"] != "CA v1" || got["ssh/id_ed25519"] != "KEY" {
		t.Fatalf("apply spec file variables %v", got)
	}
}

func fileVars(spec *api.ExecSpec) map[string]string {
	out := map[string]string{}
	for _, v := range spec.Variables {
		if v.Category == api.CategoryFile {
			out[v.Key] = v.Value
		}
	}
	return out
}

// TestDeclarationRoute covers CAF1.7: the last commit's declaration, without values.
func TestDeclarationRoute(t *testing.T) {
	h := newHarnessWith(t, Config{})
	st := h.stack("", "machines", "")
	var empty api.Declaration
	h.call("GET", "/stacks/"+st.ID+"/declaration", nil, http.StatusOK, &empty)
	if len(empty.Files) != 0 {
		t.Fatalf("a stack without runs %+v", empty)
	}

	cfg, err := stackconfig.Parse([]byte(`version: 1
engine: terraform
engine_version: 1.15.6
files:
  - path: sp*/*/connection.hcl
    format: hcl
    block: locals
    values:
      host: { vault: "secret/infra/${name}", field: ip }
      password: literal-in-git
env:
  TOKEN: { var: API_TOKEN }
`))
	if err != nil {
		t.Fatal(err)
	}
	run := h.run(st.ID, api.RunPlan)
	exec, _ := h.spec(run.ID)
	if err := exec.SetCommit(context.Background(), api.CommitRequest{SHA: strings.Repeat("a", 40), Config: cfg}); err != nil {
		t.Fatal(err)
	}
	var d api.Declaration
	raw := h.call("GET", "/stacks/"+st.ID+"/declaration", nil, http.StatusOK, &d)
	if len(d.Files) != 1 || d.Files[0].Path != "sp*/*/connection.hcl" || !d.Files[0].Sensitive ||
		d.Files[0].Refs[0] != "vault:secret/infra/${name}#ip" || d.Env[0].Ref != "var:API_TOKEN" {
		t.Fatalf("declaration %s", raw)
	}
	if strings.Contains(string(raw), "literal-in-git") {
		t.Fatalf("the declaration route returned a value: %s", raw)
	}
}
