package server

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bsteigleder/firstlume/internal/api"
)

type tarEntry struct {
	name string
	body []byte
	// link makes the entry a symbolic link to it.
	link string
}

func tarGz(t *testing.T, entries ...tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Mode: 0o644, Size: int64(len(e.body)), Typeflag: tar.TypeReg}
		if e.link != "" {
			hdr.Typeflag, hdr.Linkname, hdr.Size = tar.TypeSymlink, e.link, 0
		}
		must(t, tw.WriteHeader(hdr))
		if e.link == "" {
			_, err := tw.Write(e.body)
			must(t, err)
		}
	}
	must(t, tw.Close())
	must(t, gz.Close())
	return buf.Bytes()
}

// importState sends an import and decodes the result.
func (h *harness) importState(stackID string, dryRun bool, file []byte, fields map[string][]string, want int) api.StateImportResult {
	h.t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for name, values := range fields {
		for _, v := range values {
			must(h.t, mw.WriteField(name, v))
		}
	}
	fw, err := mw.CreateFormFile("file", "bundle.tar.gz")
	must(h.t, err)
	_, err = fw.Write(file)
	must(h.t, err)
	must(h.t, mw.Close())
	url := h.url + "/api/v1/orgs/" + h.org + "/stacks/" + stackID + "/state/import"
	if dryRun {
		url += "?dry_run=true"
	}
	req, _ := http.NewRequest("POST", url, &body)
	req.Header.Set("Authorization", "Bearer "+testAdminToken)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		h.t.Fatalf("import: status %d, want %d: %s", resp.StatusCode, want, raw)
	}
	var res api.StateImportResult
	_ = json.Unmarshal(raw, &res)
	return res
}

func importUnits(res api.StateImportResult) map[string]api.StateImportUnit {
	out := map[string]api.StateImportUnit{}
	for _, u := range res.Units {
		out[u.Key] = u
	}
	return out
}

// TestStateImport covers CAS3.1, CAS3.3 and CAS3.4 with bundles built here.
func TestStateImport(t *testing.T) { forEachStorage(t, testStateImport) }

func testStateImport(t *testing.T, storage stateStorage) {
	h := storage.harness(t, Config{StateMaxBytes: 64 << 10})
	st := h.stack("", "platform", "")
	bundle := tarGz(t,
		tarEntry{name: "./environments/prod/sp1/vm1/terraform.tfstate", body: tfState(7, "lin-vm1", 2, "s3cr3t-value")},
		tarEntry{name: "environments/prod/sp1/vm1/terraform.tfstate.backup", body: tfState(6, "lin-vm1", 2, "")},
		tarEntry{name: "environments/prod/argocd/terraform.tfstate", body: tfState(3, "lin-argo", 1, "")},
		tarEntry{name: "environments/prod/terraform.tfstate", body: tfState(1, "lin-root", 0, "")},
		tarEntry{name: "environments/prod/net/.terraform/terraform.tfstate", body: []byte(`{"version":3,"backend":{}}`)},
		tarEntry{name: "environments/prod/._terraform.tfstate", body: []byte{0, 5, 22, 7}},
	)
	prefix := map[string][]string{"prefix": {"environments/prod/"}}

	// The simulation writes nothing (CAS3.1).
	res := h.importState(st.ID, true, bundle, prefix, http.StatusOK)
	units := importUnits(res)
	if !res.DryRun || len(units) != 3 || len(res.Ignored) != 3 {
		t.Fatalf("simulation %+v", res)
	}
	if u := units["sp1/vm1"]; u.Serial != 7 || u.Resources != 2 || u.Status != "new" || u.Action != "create" || u.Version != 0 {
		t.Fatalf("vm1 in the simulation %+v", u)
	}
	if _, ok := units[""]; !ok {
		t.Fatalf("the prefix's own state should be the root unit: %+v", units)
	}
	if len(h.units(st.ID)) != 0 {
		t.Fatal("the simulation wrote state")
	}

	res = h.importState(st.ID, false, bundle, prefix, http.StatusOK)
	for _, u := range res.Units {
		if u.Version != 1 || u.Action != "create" {
			t.Fatalf("import %+v", u)
		}
	}
	if u := h.units(st.ID)["sp1/vm1"]; u.Version != 1 || u.Serial != 7 || u.Resources != 2 || u.WrittenBy != "import (admin-token)" {
		t.Fatalf("vm1 after the import %+v", u)
	}
	h.assertStoredEncrypted(st.ID, "s3cr3t-value")

	// Units that already have state: same, newer, older or conflict; only
	// the ones marked change, and they keep their history (CAS3.3).
	again := tarGz(t,
		tarEntry{name: "sp1/vm1/terraform.tfstate", body: tfState(7, "lin-vm1", 2, "s3cr3t-value")},
		tarEntry{name: "argocd/terraform.tfstate", body: tfState(9, "lin-argo", 2, "new")},
		tarEntry{name: "terraform.tfstate", body: tfState(1, "lin-other", 0, "")},
		tarEntry{name: "edge/terraform.tfstate", body: tfState(1, "lin-edge", 1, "")},
	)
	res = h.importState(st.ID, true, again, nil, http.StatusOK)
	units = importUnits(res)
	for key, want := range map[string]string{"sp1/vm1": "same/skip", "argocd": "newer/skip", "": "conflict/skip", "edge": "new/create"} {
		if got := units[key].Status + "/" + units[key].Action; got != want {
			t.Fatalf("%q: %s, want %s (%+v)", key, got, want, units[key])
		}
	}
	res = h.importState(st.ID, false, again, map[string][]string{"replace": {"argocd", "sp1/vm1"}}, http.StatusOK)
	units = importUnits(res)
	if units["argocd"].Action != "replace" || units["argocd"].Version != 2 || units["sp1/vm1"].Action != "skip" || units[""].Action != "skip" {
		t.Fatalf("import with replacements %+v", res.Units)
	}
	got := h.units(st.ID)
	if got["argocd"].Version != 2 || got["argocd"].Serial != 9 || got[""].Serial != 1 || got["edge"].Version != 1 {
		t.Fatalf("units after replacing %+v", got)
	}

	// Replacing with an older serial raises it above the current one, so a
	// plan saved before can't be applied over it.
	older := tarGz(t, tarEntry{name: "argocd/terraform.tfstate", body: tfState(2, "lin-argo", 1, "old")})
	res = h.importState(st.ID, false, older, map[string][]string{"replace": {"argocd"}}, http.StatusOK)
	if u := res.Units[0]; u.Status != "older" || u.Serial != 10 || u.Version != 3 {
		t.Fatalf("replacing with an older state %+v", u)
	}
	_, spec := h.applyPhase(st.ID)
	state := h.wantState("GET", h.stateURL(st.ID, "argocd"), spec.StateToken, nil, http.StatusOK)
	if !strings.Contains(state, `"serial": 10`) || !strings.Contains(state, `"old"`) {
		t.Fatalf("replaced state %s", state)
	}

	// A locked unit can't be imported into; nothing in the bundle is written.
	h.wantState("LOCK", h.stateURL(st.ID, "edge"), spec.StateToken, lockInfo("L1"), http.StatusOK)
	locked := tarGz(t,
		tarEntry{name: "fresh/terraform.tfstate", body: tfState(1, "lin-fresh", 1, "")},
		tarEntry{name: "edge/terraform.tfstate", body: tfState(5, "lin-edge", 1, "")},
	)
	res = h.importState(st.ID, true, locked, map[string][]string{"replace": {"edge"}}, http.StatusOK)
	if u := importUnits(res)["edge"]; !strings.HasPrefix(u.LockedBy, "run ") {
		t.Fatalf("the simulation should show the lock: %+v", u)
	}
	h.importState(st.ID, false, locked, map[string][]string{"replace": {"edge"}}, http.StatusConflict)
	if _, ok := h.units(st.ID)["fresh"]; ok {
		t.Fatal("an import refused for a lock wrote another unit")
	}

	// A bad bundle is refused whole, listing everything wrong (CAS3.4).
	bad := tarGz(t,
		tarEntry{name: "ok/terraform.tfstate", body: tfState(1, "lin-ok", 1, "")},
		tarEntry{name: "../x/terraform.tfstate", body: tfState(1, "lin-x", 1, "")},
		tarEntry{name: "link/terraform.tfstate", link: "/etc/passwd"},
		tarEntry{name: "notes/README.md", body: []byte("hi")},
		tarEntry{name: "broken/terraform.tfstate", body: []byte(`{"hello":"world"}`)},
		tarEntry{name: "big/terraform.tfstate", body: tfState(1, "lin-big", 1, strings.Repeat("x", 70<<10))},
	)
	res = h.importState(st.ID, true, bad, map[string][]string{"replace": {"nowhere"}}, http.StatusUnprocessableEntity)
	problems := map[string]string{}
	for _, p := range res.Problems {
		problems[p.Path] = p.Error
	}
	for path, want := range map[string]string{
		"../x/terraform.tfstate":   "leaves the bundle",
		"link/terraform.tfstate":   "links",
		"notes/README.md":          "not a state",
		"broken/terraform.tfstate": "not a Terraform state",
		"big/terraform.tfstate":    "larger than the limit",
		"nowhere":                  "no state for this unit",
	} {
		if !strings.Contains(problems[path], want) {
			t.Errorf("%s: problem %q, want it to mention %q (%+v)", path, problems[path], want, res.Problems)
		}
	}
	h.importState(st.ID, false, bad, nil, http.StatusUnprocessableEntity)
	if _, ok := h.units(st.ID)["ok"]; ok {
		t.Fatal("a refused bundle wrote state")
	}
	h.importState(st.ID, true, []byte("not a bundle"), nil, http.StatusUnprocessableEntity)
	outside := tarGz(t, tarEntry{name: "staging/a/terraform.tfstate", body: tfState(1, "lin-a", 1, "")})
	h.importState(st.ID, true, outside, map[string][]string{"prefix": {"prod"}}, http.StatusUnprocessableEntity)

	// A single state goes to the unit named in the request (R9.1).
	res = h.importState(st.ID, false, tfState(4, "lin-single", 1, ""), map[string][]string{"unit": {"./single/"}}, http.StatusOK)
	if u := h.units(st.ID)["single"]; len(res.Units) != 1 || u.Version != 1 || u.Serial != 4 {
		t.Fatalf("single state import %+v / %+v", res, u)
	}

	var events int
	must(t, h.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM state_events WHERE stack_id = $1 AND kind = 'state_imported'`, st.ID).Scan(&events))
	if events != 7 {
		t.Fatalf("%d import events, want 7", events)
	}
}

// TestStateTokens covers the stack's state tokens (R7) through the protocol.
func TestStateTokens(t *testing.T) {
	h := newHarnessWith(t, Config{})
	ctx := context.Background()
	st := h.stack("", "platform", "")
	other := h.stack("", "other", "")
	root := h.stateURL(st.ID, "")

	var ci, reader api.CreateStateTokenResponse
	h.call("POST", "/stacks/"+st.ID+"/state/tokens", api.CreateStateTokenRequest{Name: "ci", Write: true}, http.StatusCreated, &ci)
	h.call("POST", "/stacks/"+st.ID+"/state/tokens", api.CreateStateTokenRequest{Name: "reader"}, http.StatusCreated, &reader)
	h.call("POST", "/stacks/"+st.ID+"/state/tokens", api.CreateStateTokenRequest{Name: "ci", Write: true}, http.StatusConflict, nil)
	past := time.Now().Add(-time.Hour)
	h.call("POST", "/stacks/"+st.ID+"/state/tokens", api.CreateStateTokenRequest{Name: "old", ExpiresAt: &past}, http.StatusUnprocessableEntity, nil)
	if !strings.HasPrefix(ci.Token, "flst_") || ci.StateToken.Name != "ci" || !ci.StateToken.Write {
		t.Fatalf("created token %+v", ci)
	}

	// Write token: everything, recorded with its name (R7.5).
	h.wantState("LOCK", root, ci.Token, lockInfo("T1"), http.StatusOK)
	held := h.wantState("LOCK", root, ci.Token, lockInfo("T2"), http.StatusLocked)
	if !strings.Contains(held, "token ci") {
		t.Fatalf("the lock should name the token: %s", held)
	}
	h.wantState("POST", root+"?ID=T1", ci.Token, tfState(1, "lin", 1, "x"), http.StatusOK)
	h.wantState("UNLOCK", root, ci.Token, lockInfo("T1"), http.StatusOK)
	if u := h.units(st.ID)[""]; u.WrittenBy != "token ci" || u.Version != 1 {
		t.Fatalf("unit written by a token %+v", u)
	}
	var tokenID *string
	must(t, h.pool.QueryRow(ctx, `SELECT v.token_id::text FROM state_versions v JOIN state_units u ON u.id = v.unit_id
		WHERE u.stack_id = $1`, st.ID).Scan(&tokenID))
	if tokenID == nil || *tokenID != ci.StateToken.ID {
		t.Fatalf("the version should record the token: %v", tokenID)
	}

	// Read token: only GET; a plan uses -lock=false (R7.3).
	h.wantState("GET", root, reader.Token, nil, http.StatusOK)
	h.wantState("LOCK", root, reader.Token, lockInfo("R1"), http.StatusForbidden)
	h.wantState("POST", root, reader.Token, tfState(2, "lin", 1, "y"), http.StatusForbidden)
	h.wantState("GET", h.stateURL(other.ID, ""), reader.Token, nil, http.StatusNotFound)

	// A token's lock is not released by itself: only by UNLOCK (R7.4).
	h.wantState("LOCK", root, ci.Token, lockInfo("T3"), http.StatusOK)

	var list []api.StateToken
	h.call("GET", "/stacks/"+st.ID+"/state/tokens", nil, http.StatusOK, &list)
	if len(list) != 2 || list[0].Name != "ci" || list[0].LastUsedAt == nil {
		t.Fatalf("tokens %+v", list)
	}
	for _, tok := range []string{ci.Token, reader.Token} {
		if strings.Contains(h.logs.String(), tok) || strings.Contains(h.logs.String(), "flst_") {
			t.Fatal("a state token reached the server log")
		}
	}

	// Revoked or expired: 401 (CAS3.5).
	h.call("DELETE", "/stacks/"+st.ID+"/state/tokens/"+ci.StateToken.ID, nil, http.StatusNoContent, nil)
	h.call("DELETE", "/stacks/"+st.ID+"/state/tokens/"+ci.StateToken.ID, nil, http.StatusNotFound, nil)
	h.wantState("GET", root, ci.Token, nil, http.StatusUnauthorized)
	if u := h.units(st.ID)[""]; u.Lock == nil || u.Lock.Holder != "token ci" {
		t.Fatalf("revoking a token must not release its lock: %+v", u)
	}
	_, err := h.pool.Exec(ctx, `UPDATE state_tokens SET expires_at = now() - interval '1 second' WHERE id = $1`, reader.StateToken.ID)
	must(t, err)
	h.wantState("GET", root, reader.Token, nil, http.StatusUnauthorized)
	h.wantState("GET", root, "flst_made-up", nil, http.StatusUnauthorized)

	// The same name can be used again once revoked; deleting the stack's
	// state works once the token's lock is gone.
	h.call("POST", "/stacks/"+st.ID+"/state/tokens", api.CreateStateTokenRequest{Name: "ci", Write: true}, http.StatusCreated, nil)
	var events int
	must(t, h.pool.QueryRow(ctx, `SELECT count(*) FROM state_events WHERE stack_id = $1 AND kind IN ('token_created', 'token_revoked')`,
		st.ID).Scan(&events))
	if events != 4 {
		t.Fatalf("%d token events, want 4", events)
	}
}

// TestStateImportWithRealTerragrunt covers CAS3.2 and CAS3.5: a bundle made by
// Terragrunt with local state is imported, and the same code then plans with
// "No changes" through Firstlume and from outside with a read token; a write
// token applies from outside.
func TestStateImportWithRealTerragrunt(t *testing.T) {
	tg := tool(t, "FIRSTLUME_TEST_TERRAGRUNT", "terragrunt")
	tfBin := tool(t, "FIRSTLUME_TEST_TERRAFORM", "terraform")
	h := newHarnessWith(t, Config{})
	st := h.stack("", "platform", "")
	dir := t.TempDir()
	env := filepath.Join(dir, "environments", "tg")
	files := map[string]string{
		"root.hcl":           "",
		"net/terragrunt.hcl": `include "root" { path = find_in_parent_folders("root.hcl") }`,
		"net/main.tf": `
variable "cidr" {
  type    = string
  default = "10.0.0.0/16"
}
resource "terraform_data" "vpc" { input = var.cidr }
output "cidr" { value = terraform_data.vpc.output }
`,
		"app/terragrunt.hcl": `
include "root" { path = find_in_parent_folders("root.hcl") }
dependency "net" {
  config_path = "../net"
  mock_outputs = { cidr = "mock" }
  mock_outputs_allowed_terraform_commands = ["plan", "validate"]
}
inputs = { cidr = dependency.net.outputs.cidr }
`,
		"app/main.tf": `
variable "cidr" { type = string }
resource "terraform_data" "app" { input = "app in ${var.cidr}" }
`,
	}
	for name, content := range files {
		p := filepath.Join(env, filepath.FromSlash(name))
		must(t, os.MkdirAll(filepath.Dir(p), 0o755))
		must(t, os.WriteFile(p, []byte(content), 0o644))
	}

	// Today's pipeline: local state per unit (Terragrunt keeps it in the
	// unit's .terragrunt-cache), bundled after the apply.
	local := &cli{t: t, bin: tg, dir: env, env: toolEnv(h, st.ID, "", "TG_TF_PATH="+tfBin)}
	local.must("run", "--all", "--non-interactive", "--", "apply", "-input=false", "-auto-approve")
	var entries []tarEntry
	must(t, filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() != "terraform.tfstate" {
			return err
		}
		body, err := os.ReadFile(p)
		rel, _ := filepath.Rel(dir, p)
		entries = append(entries, tarEntry{name: "./" + filepath.ToSlash(rel), body: body})
		_ = os.Remove(p) // the pipeline starts from a fresh clone
		return err
	}))
	if len(entries) != 2 {
		t.Fatalf("expected the states of net and app, got %d", len(entries))
	}
	res := h.importState(st.ID, false, tarGz(t, entries...), map[string][]string{"prefix": {"environments/tg"}}, http.StatusOK)
	if units := importUnits(res); units["net"].Version != 1 || units["app"].Version != 1 {
		t.Fatalf("import %+v", res.Units)
	}

	// The root.hcl now uses Firstlume's backend (R1.3).
	must(t, os.WriteFile(filepath.Join(env, "root.hcl"), []byte(terragruntRoot), 0o644))
	for _, unit := range []string{"net", "app"} {
		must(t, os.RemoveAll(filepath.Join(env, unit, ".terragrunt-cache")))
	}
	run := h.run(st.ID, api.RunPlan)
	_, spec := h.spec(run.ID)
	viaRun := &cli{t: t, bin: tg, dir: env, env: toolEnv(h, st.ID, spec.StateToken, "TG_TF_PATH="+tfBin)}
	assertNoChanges(t, viaRun.must("run", "--all", "--non-interactive", "--", "plan", "-input=false", "-detailed-exitcode"))

	var reader, ci api.CreateStateTokenResponse
	h.call("POST", "/stacks/"+st.ID+"/state/tokens", api.CreateStateTokenRequest{Name: "woodpecker-plan"}, http.StatusCreated, &reader)
	h.call("POST", "/stacks/"+st.ID+"/state/tokens", api.CreateStateTokenRequest{Name: "woodpecker", Write: true}, http.StatusCreated, &ci)
	outside := &cli{t: t, bin: tg, dir: env, env: toolEnv(h, st.ID, reader.Token, "TG_TF_PATH="+tfBin)}
	assertNoChanges(t, outside.must("run", "--all", "--non-interactive", "--", "plan", "-input=false", "-lock=false", "-detailed-exitcode"))
	if out, err := outside.run("run", "--all", "--non-interactive", "--", "plan", "-input=false"); err == nil || !strings.Contains(out, "invalid auth") {
		t.Fatalf("a read token must not lock: %v\n%s", err, out)
	}

	// "State only": the pipeline applies with a write token (CAS3.5).
	outside.env = toolEnv(h, st.ID, ci.Token, "TG_TF_PATH="+tfBin, "TF_VAR_cidr=10.1.0.0/16")
	outside.must("run", "--all", "--non-interactive", "--", "apply", "-input=false", "-auto-approve")
	units := h.units(st.ID)
	for _, key := range []string{"net", "app"} {
		if u := units[key]; u.Version != 2 || u.WrittenBy != "token woodpecker" || u.Lock != nil {
			t.Fatalf("%s after the pipeline's apply: %+v", key, u)
		}
	}
	h.call("DELETE", "/stacks/"+st.ID+"/state/tokens/"+ci.StateToken.ID, nil, http.StatusNoContent, nil)
	if out, err := outside.run("run", "--all", "--non-interactive", "--", "plan", "-input=false"); err == nil || !strings.Contains(out, "requires auth") {
		t.Fatalf("a revoked token still works: %v\n%s", err, out)
	}
}

func assertNoChanges(t *testing.T, out string) {
	t.Helper()
	if strings.Count(out, "No changes.") != 2 || strings.Contains(out, "to add") {
		t.Fatalf("expected No changes in both units:\n%s", out)
	}
}

// TestStateConcurrentAppliesWithToken covers CAS3.6: two applies on the same
// unit from outside. Without waiting, the second fails on the lock; waiting,
// it runs after the first. The state stays consistent either way.
func TestStateConcurrentAppliesWithToken(t *testing.T) {
	bin := tool(t, "FIRSTLUME_TEST_TERRAFORM", "terraform")
	h := newHarnessWith(t, Config{})
	st := h.stack("", "network", "")
	var ci api.CreateStateTokenResponse
	h.call("POST", "/stacks/"+st.ID+"/state/tokens", api.CreateStateTokenRequest{Name: "ci", Write: true}, http.StatusCreated, &ci)
	code := []byte(`
terraform {
  backend "http" {}
}
variable "n" { type = string }
resource "terraform_data" "slow" {
  triggers_replace = var.n
  provisioner "local-exec" { command = "sleep 3" }
}
`)
	workspace := func(n string) *cli {
		dir := t.TempDir()
		must(t, os.WriteFile(filepath.Join(dir, "main.tf"), code, 0o644))
		c := &cli{t: t, bin: bin, dir: dir, env: toolEnv(h, st.ID, ci.Token, "TF_VAR_n="+n)}
		c.must("init", "-input=false")
		return c
	}
	a, b := workspace("a"), workspace("b")
	a.must("apply", "-input=false", "-auto-approve")

	// A holds the lock while its provisioner sleeps.
	start := func(c *cli, n string) (*exec.Cmd, *bytes.Buffer) {
		var out bytes.Buffer
		cmd := exec.Command(c.bin, "apply", "-input=false", "-auto-approve")
		cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = c.dir, append(c.env, "TF_VAR_n="+n), &out, &out
		must(t, cmd.Start())
		return cmd, &out
	}
	waitLocked := func() {
		for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
			if h.units(st.ID)[""].Lock != nil {
				return
			}
		}
		t.Fatal("the first apply never locked the unit")
	}
	cmd, out := start(a, "a2")
	waitLocked()
	failed, err := b.run("apply", "-input=false", "-auto-approve", "-lock-timeout=0s")
	if err == nil || !strings.Contains(failed, "Error acquiring the state lock") || !strings.Contains(failed, "token ci") {
		t.Fatalf("the second apply should fail on the lock: %v\n%s", err, failed)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("first apply: %v\n%s", err, out)
	}

	cmd, out = start(a, "a3")
	waitLocked()
	b.env = append(b.env, "TF_VAR_n=b3")
	b.must("apply", "-input=false", "-auto-approve", "-lock-timeout=60s")
	if err := cmd.Wait(); err != nil {
		t.Fatalf("first apply: %v\n%s", err, out)
	}
	// Each apply may write more than once; what matters is that every write
	// was accepted in order and the last one is current.
	u := h.units(st.ID)[""]
	if u.Version < 3 || u.Serial < int64(u.Version) || u.Lock != nil {
		t.Fatalf("after the concurrent applies %+v", u)
	}
	if state := h.wantState("GET", h.stateURL(st.ID, ""), ci.Token, nil, http.StatusOK); !strings.Contains(state, `"b3"`) {
		t.Fatalf("the current state is not the last apply's: %s", state)
	}
	if plan := b.must("plan", "-input=false", "-detailed-exitcode"); !strings.Contains(plan, "No changes") {
		t.Fatalf("the last apply's state should be current:\n%s", plan)
	}
}
