package server

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/bsteigleder/firstlume/internal/api"
)

// TestRegistryCredentialSettings covers CAF3.2 on the server: credentials are
// validated, inherited by host, never returned, kept when sent without a
// password, handed to the executor and frozen by the plan.
func TestRegistryCredentialSettings(t *testing.T) {
	h := newHarnessWith(t, Config{})
	ctx := context.Background()
	f := h.folder("", "prod")
	st := h.stack(f.ID, "charts", "")

	for name, bad := range map[string]api.RegistryCredential{
		"kind":               {Host: "harbor.example.com", Kind: "helm", Username: "u", Password: "p"},
		"no username":        {Host: "harbor.example.com", Password: "p"},
		"terraform user":     {Host: "tf.example.com", Kind: "terraform", Username: "u", Password: "p"},
		"terraform port":     {Host: "tf.example.com:8443", Kind: "terraform", Password: "p"},
		"url, not a host":    {Host: "https://harbor.example.com", Username: "u", Password: "p"},
		"password and vault": {Host: "harbor.example.com", Username: "u", Password: "p", PasswordVault: &api.VaultRef{Path: "secret/x", Field: "p"}},
		"vault path":         {Host: "harbor.example.com", Username: "u", PasswordVault: &api.VaultRef{Path: "secret/../x", Field: "p"}},
		"no password yet":    {Host: "harbor.example.com", Username: "u"},
	} {
		var e api.ErrorResponse
		h.call("PUT", "/settings", api.LevelSettings{Registries: []api.RegistryCredential{bad}}, http.StatusUnprocessableEntity, &e)
		if e.Error == "" {
			t.Errorf("%s: no message", name)
		}
	}

	h.settings("", api.LevelSettings{Registries: []api.RegistryCredential{
		{Host: "Harbor.Example.com", Username: "robot$org", Password: "org-password"},
		{Host: "tf.example.com", Kind: "terraform", Password: "tf-token"},
	}})
	h.settings("/folders/"+f.ID, api.LevelSettings{RequireApproval: ptr(false), Registries: []api.RegistryCredential{
		{Host: "harbor.example.com", Username: "robot$prod", Password: "prod-password"},
		{Host: "ghcr.io", Username: "bot", PasswordVault: &api.VaultRef{Path: "secret/ci/ghcr", Field: "token"}},
	}})

	var got api.SettingsResponse
	raw := h.call("GET", "/stacks/"+st.ID+"/settings", nil, http.StatusOK, &got)
	if strings.Contains(string(raw), "password\":\"") || strings.Contains(string(raw), "prod-password") || strings.Contains(string(raw), "tf-token") {
		t.Fatalf("a password was returned: %s", raw)
	}
	eff := map[string]api.EffectiveRegistry{}
	for _, r := range got.Effective.Registries {
		eff[r.Host] = r
	}
	if len(eff) != 3 || eff["harbor.example.com"].Username != "robot$prod" || eff["harbor.example.com"].Origin.ID != f.ID ||
		eff["harbor.example.com"].Overrides == nil || eff["tf.example.com"].Origin.Kind != api.LevelOrganization ||
		eff["ghcr.io"].PasswordVault == nil || !eff["harbor.example.com"].HasPassword {
		t.Fatalf("effective registries %+v", got.Effective.Registries)
	}

	// Saving without the field keeps the credentials; without a password, the stored one.
	h.settings("/folders/"+f.ID, api.LevelSettings{RequireApproval: ptr(false)})
	h.call("GET", "/folders/"+f.ID+"/settings", nil, http.StatusOK, &got)
	if len(got.Level.Registries) != 2 {
		t.Fatalf("registries dropped by a save without them: %+v", got.Level.Registries)
	}
	h.settings("/folders/"+f.ID, api.LevelSettings{RequireApproval: ptr(false), Registries: []api.RegistryCredential{
		{Host: "harbor.example.com", Username: "robot$prod2"},
		{Host: "ghcr.io", Username: "bot", PasswordVault: &api.VaultRef{Path: "secret/ci/ghcr", Field: "token"}},
	}})

	run := h.run(st.ID, api.RunApply)
	exec, spec := h.spec(run.ID)
	execRegs := map[string]api.ExecRegistry{}
	for _, r := range spec.Registries {
		execRegs[r.Host] = r
	}
	if execRegs["harbor.example.com"].Password != "prod-password" || execRegs["harbor.example.com"].Username != "robot$prod2" ||
		execRegs["tf.example.com"].Password != "tf-token" || execRegs["ghcr.io"].PasswordVault.Field != "token" {
		t.Fatalf("plan spec registries %+v", spec.Registries)
	}
	// Changed after the plan: the apply still gets the plan's.
	h.settings("", api.LevelSettings{Registries: []api.RegistryCredential{{Host: "tf.example.com", Kind: "terraform", Password: "new-token"}}})
	if _, err := exec.Complete(ctx, api.CompleteRequest{Success: true, HasChanges: true}); err != nil {
		t.Fatal(err)
	}
	_, spec = h.spec(run.ID)
	for _, r := range spec.Registries {
		if r.Host == "tf.example.com" && r.Password != "tf-token" {
			t.Fatalf("the apply got a credential changed after the plan: %+v", r)
		}
	}
	if spec.Phase != api.PhaseApply || len(spec.Registries) != 3 {
		t.Fatalf("apply spec registries %+v", spec.Registries)
	}
}
