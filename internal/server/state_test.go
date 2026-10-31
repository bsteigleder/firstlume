package server

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bsteigleder/firstlume/internal/api"
	"github.com/bsteigleder/firstlume/internal/apiclient"
)

// applyPhase starts an apply run on the stack (approval off) and returns it in its apply phase.
func (h *harness) applyPhase(stackID string) (*apiclient.Client, *api.ExecSpec) {
	h.t.Helper()
	h.settings("/stacks/"+stackID, api.LevelSettings{RequireApproval: ptr(false)})
	run := h.run(stackID, api.RunApply)
	exec, _ := h.spec(run.ID)
	if _, err := exec.Complete(context.Background(), api.CompleteRequest{Success: true, HasChanges: true}); err != nil {
		h.t.Fatal(err)
	}
	exec, spec := h.spec(run.ID)
	if spec.Phase != api.PhaseApply || spec.StateToken == "" {
		h.t.Fatalf("unexpected apply spec %+v", spec)
	}
	return exec, spec
}

func (h *harness) stateURL(stackID, key string) string {
	u := h.url + "/state/v1/stacks/" + stackID
	if key != "" {
		u += "/" + key
	}
	return u
}

// state sends a state protocol request and returns the status and body.
func (h *harness) state(method, url, token string, body []byte) (int, string) {
	h.t.Helper()
	req, _ := http.NewRequest(method, url, bytes.NewReader(body))
	if token != "" {
		req.SetBasicAuth("firstlume", token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

func (h *harness) wantState(method, url, token string, body []byte, want int) string {
	h.t.Helper()
	got, raw := h.state(method, url, token, body)
	if got != want {
		h.t.Fatalf("%s %s: status %d, want %d: %s", method, url, got, want, raw)
	}
	return raw
}

func tfState(serial int, lineage string, managed int, extra string) []byte {
	var res []string
	for i := range managed {
		res = append(res, fmt.Sprintf(`{"mode":"managed","type":"terraform_data","name":"r%d","instances":[{"attributes":{"input":%q}}]}`, i, extra))
	}
	res = append(res, `{"mode":"data","type":"external","name":"d","instances":[]}`)
	return fmt.Appendf(nil, `{"version":4,"terraform_version":"1.15.6","serial":%d,"lineage":%q,"outputs":{},"resources":[%s]}`,
		serial, lineage, strings.Join(res, ","))
}

func lockInfo(id string) []byte {
	return fmt.Appendf(nil, `{"ID":%q,"Operation":"OperationTypeApply","Who":"root@pod","Version":"1.15.6","Path":""}`, id)
}

func (h *harness) units(stackID string) map[string]api.StateUnit {
	h.t.Helper()
	var list []api.StateUnit
	h.call("GET", "/stacks/"+stackID+"/state", nil, http.StatusOK, &list)
	out := map[string]api.StateUnit{}
	for _, u := range list {
		out[u.Key] = u
	}
	return out
}

func TestStateProtocol(t *testing.T) {
	h := newHarnessWith(t, Config{StateMaxBytes: 64 << 10})
	ctx := context.Background()
	st := h.stack("", "network", "")
	other := h.stack("", "other", "")
	root := h.stateURL(st.ID, "")

	// Plan phase: read and lock only (R5.2).
	h.settings("/stacks/"+st.ID, api.LevelSettings{RequireApproval: ptr(false)})
	run := h.run(st.ID, api.RunApply)
	planExec, planSpec := h.spec(run.ID)
	plan := planSpec.StateToken
	if plan == "" {
		t.Fatal("the plan spec has no state token")
	}
	h.wantState("GET", root, plan, nil, http.StatusNoContent)
	h.wantState("POST", root, plan, tfState(1, "lin-a", 1, "x"), http.StatusForbidden)
	h.wantState("LOCK", root, plan, lockInfo("plan-lock"), http.StatusOK)
	h.wantState("GET", root, "", nil, http.StatusUnauthorized)
	h.wantState("GET", root, "fls_forged.token", nil, http.StatusUnauthorized)
	h.wantState("GET", h.stateURL(other.ID, ""), plan, nil, http.StatusNotFound)
	h.wantState("DELETE", root, plan, nil, http.StatusMethodNotAllowed)

	// The plan ends holding the lock: it is released with the phase (R5.3), and its token stops working.
	if _, err := planExec.Complete(ctx, api.CompleteRequest{Success: true, HasChanges: true}); err != nil {
		t.Fatal(err)
	}
	h.wantState("GET", root, plan, nil, http.StatusUnauthorized)

	applyExec, applySpec := h.spec(run.ID)
	tok := applySpec.StateToken
	v1 := tfState(1, "lin-a", 2, "s3cr3t-value")
	h.wantState("POST", root, tok, v1, http.StatusOK)
	if got := h.wantState("GET", root, tok, nil, http.StatusOK); got != string(v1) {
		t.Fatalf("read %s, want what was written", got)
	}
	h.wantState("POST", root, tok, v1, http.StatusOK) // same serial and content: no new version
	h.wantState("POST", root, tok, tfState(0, "lin-a", 2, "x"), http.StatusConflict)
	h.wantState("POST", root, tok, tfState(2, "lin-b", 2, "x"), http.StatusConflict)
	h.wantState("POST", root, tok, tfState(1, "lin-a", 2, "changed"), http.StatusConflict)
	h.wantState("POST", root, tok, []byte(`{"hello":"world"}`), http.StatusUnprocessableEntity)
	h.wantState("POST", root, tok, []byte(`not json`), http.StatusUnprocessableEntity)
	h.wantState("POST", root, tok, tfState(2, "lin-a", 1, strings.Repeat("x", 70<<10)), http.StatusRequestEntityTooLarge)
	req, _ := http.NewRequest("POST", root, bytes.NewReader(tfState(2, "lin-a", 1, "x")))
	req.SetBasicAuth("firstlume", tok)
	req.Header.Set("Content-MD5", "AAAAAAAAAAAAAAAAAAAAAA==")
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("a wrong Content-MD5 was accepted: %v %v", resp, err)
	}

	// Locks (R3.3, R3.4).
	h.wantState("LOCK", root, tok, lockInfo("L1"), http.StatusOK)
	h.wantState("LOCK", root, tok, lockInfo("L1"), http.StatusOK)
	held := h.wantState("LOCK", root, tok, lockInfo("L2"), http.StatusLocked)
	if !strings.Contains(held, `"L1"`) || !strings.Contains(held, "run "+run.ID[:8]+" (apply)") {
		t.Fatalf("lock conflict should describe the holder: %s", held)
	}
	h.wantState("POST", root+"?ID=L2", tok, tfState(2, "lin-a", 1, "x"), http.StatusLocked)
	h.wantState("POST", root, tok, tfState(2, "lin-a", 1, "x"), http.StatusLocked)
	h.wantState("POST", root+"?ID=L1", tok, tfState(2, "lin-a", 1, "x"), http.StatusOK)
	h.wantState("UNLOCK", root, tok, lockInfo("L2"), http.StatusConflict)
	h.wantState("UNLOCK", root, tok, nil, http.StatusConflict) // force-unlock is the State tab's job
	h.wantState("UNLOCK", root, tok, lockInfo("L1"), http.StatusOK)
	h.wantState("UNLOCK", root, tok, lockInfo("L1"), http.StatusOK)
	h.wantState("LOCK", root, tok, []byte(`{"Operation":"x"}`), http.StatusUnprocessableEntity)

	// Units are keyed by their path; "./net/" is "net".
	h.wantState("POST", h.stateURL(st.ID, "net"), tok, tfState(1, "lin-n", 0, ""), http.StatusOK)
	h.wantState("GET", h.stateURL(st.ID, "./net/"), tok, nil, http.StatusOK)
	h.wantState("GET", h.stateURL(st.ID, "%2E%2E/escape"), tok, nil, http.StatusBadRequest)
	h.wantState("LOCK", h.stateURL(st.ID, "app"), tok, lockInfo("A1"), http.StatusOK)

	units := h.units(st.ID)
	if u := units[""]; u.Version != 2 || u.Serial != 2 || u.Resources != 1 || u.Lock != nil || !strings.HasPrefix(u.WrittenBy, "run ") {
		t.Fatalf("root unit %+v", u)
	}
	if u := units["net"]; u.Version != 1 || u.Resources != 0 {
		t.Fatalf("net unit %+v", u)
	}
	if u := units["app"]; u.Version != 0 || u.Lock == nil || u.Lock.ID != "A1" || u.Lock.RunID != run.ID {
		t.Fatalf("app unit %+v", u)
	}

	// Stored encrypted (R4.2).
	var leaks int
	if err := h.pool.QueryRow(ctx, `SELECT count(*) FROM state_objects WHERE position($1::bytea in data) > 0`,
		[]byte("s3cr3t-value")).Scan(&leaks); err != nil || leaks != 0 {
		t.Fatalf("state stored in clear (%d objects, %v)", leaks, err)
	}
	for _, secret := range []string{tok, plan} {
		if strings.Contains(h.logs.String(), secret) {
			t.Fatal("a state token reached the server log")
		}
	}

	// R6: a stack whose state manages resources can't be deleted until the state is.
	h.call("DELETE", "/stacks/"+st.ID+"/state", api.DeleteStateRequest{Confirm: st.Name}, http.StatusConflict, nil) // run active
	if _, err := applyExec.Complete(ctx, api.CompleteRequest{Success: true}); err != nil {
		t.Fatal(err)
	}
	h.wantState("GET", root, tok, nil, http.StatusUnauthorized)
	h.call("DELETE", "/stacks/"+st.ID, nil, http.StatusConflict, nil)
	h.call("DELETE", "/stacks/"+st.ID+"/state", api.DeleteStateRequest{Confirm: "wrong"}, http.StatusUnprocessableEntity, nil)
	h.call("DELETE", "/stacks/"+st.ID+"/state", api.DeleteStateRequest{Confirm: st.Name}, http.StatusNoContent, nil)
	if units := h.units(st.ID); len(units) != 0 {
		t.Fatalf("units after deleting the state: %+v", units)
	}
	h.call("DELETE", "/stacks/"+st.ID, nil, http.StatusNoContent, nil)
	var kept int
	if err := h.pool.QueryRow(ctx, `SELECT count(*) FROM state_units WHERE stack_id = $1 AND deleted_at IS NOT NULL`, st.ID).Scan(&kept); err != nil || kept != 3 {
		t.Fatalf("deleted state should be kept for 30 days: %d units, %v", kept, err)
	}
}

func TestStateLocksReleasedWhenExecutorDies(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	st := h.stack("", "network", "")
	_, spec := h.applyPhase(st.ID)
	h.wantState("LOCK", h.stateURL(st.ID, "net"), spec.StateToken, lockInfo("dead"), http.StatusOK)

	// The pod is OOM-killed: the agent reports it and the run fails (CAS1.7).
	if err := h.agent.ReportFailure(ctx, spec.RunID, api.PhaseApply, "OOMKilled"); err != nil {
		t.Fatal(err)
	}
	if u := h.units(st.ID); len(u) != 0 {
		t.Fatalf("the dead run's lock is still held: %+v", u)
	}
	var events int
	if err := h.pool.QueryRow(ctx, `SELECT count(*) FROM state_events WHERE stack_id = $1 AND kind = 'lock_released'`, st.ID).
		Scan(&events); err != nil || events != 1 {
		t.Fatalf("lock release not recorded: %d, %v", events, err)
	}
	_, next := h.applyPhase(st.ID)
	h.wantState("LOCK", h.stateURL(st.ID, "net"), next.StateToken, lockInfo("next"), http.StatusOK)
}

// tool returns a real binary for protocol tests (CGS2): the env var's path, or PATH.
func tool(t *testing.T, envVar, name string) string {
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

type cli struct {
	t   *testing.T
	bin string
	dir string
	env []string
}

func (c *cli) run(args ...string) (string, error) {
	cmd := exec.Command(c.bin, args...)
	cmd.Dir = c.dir
	cmd.Env = c.env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (c *cli) must(args ...string) string {
	c.t.Helper()
	out, err := c.run(args...)
	if err != nil {
		c.t.Fatalf("%s %s: %v\n%s", filepath.Base(c.bin), strings.Join(args, " "), err, out)
	}
	return out
}

func toolEnv(h *harness, stackID, token string, extra ...string) []string {
	base := h.stateURL(stackID, "")
	return append([]string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + h.t.TempDir(),
		"TF_IN_AUTOMATION=1", "CHECKPOINT_DISABLE=1", "NO_COLOR=1", "TG_NO_COLOR=true", "TG_NON_INTERACTIVE=true",
		"TF_HTTP_ADDRESS=" + base, "TF_HTTP_LOCK_ADDRESS=" + base, "TF_HTTP_UNLOCK_ADDRESS=" + base,
		"TF_HTTP_USERNAME=firstlume", "TF_HTTP_PASSWORD=" + token,
		"FIRSTLUME_STATE_URL=" + base,
	}, extra...)
}

// TestStateWithRealTerraform covers CAS1.1, CAS1.5, CAS1.6 and CAS1.9 with Terraform itself.
func TestStateWithRealTerraform(t *testing.T) {
	bin := tool(t, "FIRSTLUME_TEST_TERRAFORM", "terraform")
	h := newHarness(t)
	ctx := context.Background()
	st := h.stack("", "network", "")
	dir := t.TempDir()
	must(t, os.WriteFile(filepath.Join(dir, "main.tf"), []byte(`
terraform {
  backend "http" {}
}
variable "secret" {
  type      = string
  sensitive = true
}
variable "label" {
  type = string
}
resource "terraform_data" "a" {
  input = "${var.secret}-${var.label}"
}
`), 0o644))

	exec, spec := h.applyPhase(st.ID)
	tf := &cli{t: t, bin: bin, dir: dir, env: toolEnv(h, st.ID, spec.StateToken, "TF_VAR_secret=s3cr3t-value", "TF_VAR_label=one")}
	tf.must("init", "-input=false")
	tf.must("apply", "-input=false", "-auto-approve")
	tf.must("apply", "-input=false", "-auto-approve") // no changes: no new version
	if u := h.units(st.ID)[""]; u.Version != 1 || u.Resources != 1 {
		t.Fatalf("after two applies %+v", u)
	}
	if out := tf.must("plan", "-input=false", "-detailed-exitcode"); !strings.Contains(out, "No changes") {
		t.Fatalf("second plan: %s", out)
	}

	// Locked by someone else: Terraform names the holder (CAS1.5).
	h.wantState("LOCK", h.stateURL(st.ID, ""), spec.StateToken, lockInfo("someone-else"), http.StatusOK)
	tf.env = append(tf.env, "TF_VAR_label=two")
	out, err := tf.run("apply", "-input=false", "-auto-approve", "-lock-timeout=0s")
	if err == nil || !strings.Contains(out, "someone-else") || !strings.Contains(out, "run "+spec.RunID[:8]+" (apply)") {
		t.Fatalf("apply on a locked unit: %v\n%s", err, out)
	}
	h.wantState("UNLOCK", h.stateURL(st.ID, ""), spec.StateToken, lockInfo("someone-else"), http.StatusOK)
	tf.must("apply", "-input=false", "-auto-approve")
	if u := h.units(st.ID)[""]; u.Version != 2 || u.Lock != nil {
		t.Fatalf("after the change %+v", u)
	}
	if _, err := exec.Complete(ctx, api.CompleteRequest{Success: true}); err != nil {
		t.Fatal(err)
	}

	// A plan phase can't write, even if its Terraform tries (CAS1.6).
	run := h.run(st.ID, api.RunPlan)
	_, planSpec := h.spec(run.ID)
	tf.env = append(toolEnv(h, st.ID, planSpec.StateToken, "TF_VAR_secret=s3cr3t-value"), "TF_VAR_label=three")
	out = tf.must("plan", "-input=false")
	if !strings.Contains(out, "0 to add, 1 to change, 0 to destroy") {
		t.Fatalf("plan with the plan token: %s", out)
	}
	out, err = tf.run("apply", "-input=false", "-auto-approve")
	if err == nil || !strings.Contains(out, "403") {
		t.Fatalf("apply with a plan token: %v\n%s", err, out)
	}
	if u := h.units(st.ID)[""]; u.Version != 2 {
		t.Fatalf("a plan token wrote state: %+v", u)
	}

	// CAS1.9: the secret isn't stored in clear, and no token reaches the server log.
	var leaks int
	if err := h.pool.QueryRow(ctx, `SELECT count(*) FROM state_objects WHERE position($1::bytea in data) > 0`,
		[]byte("s3cr3t-value")).Scan(&leaks); err != nil || leaks != 0 {
		t.Fatalf("state stored in clear (%d objects, %v)", leaks, err)
	}
	for _, tok := range []string{spec.StateToken, planSpec.StateToken} {
		if strings.Contains(h.logs.String(), tok) {
			t.Fatal("a state token reached the server log")
		}
	}
}

const terragruntRoot = `
remote_state {
  backend  = "http"
  generate = { path = "backend.tf", if_exists = "overwrite_terragrunt" }
  config = {
    address        = "${get_env("FIRSTLUME_STATE_URL")}/${path_relative_to_include()}"
    lock_address   = "${get_env("FIRSTLUME_STATE_URL")}/${path_relative_to_include()}"
    unlock_address = "${get_env("FIRSTLUME_STATE_URL")}/${path_relative_to_include()}"
  }
}
`

// TestStateWithRealTerragrunt covers CAS1.2 and CAS1.3: one state per unit,
// dependency outputs across units, and a failing unit not losing the others.
func TestStateWithRealTerragrunt(t *testing.T) {
	tg := tool(t, "FIRSTLUME_TEST_TERRAGRUNT", "terragrunt")
	tfBin := tool(t, "FIRSTLUME_TEST_TERRAFORM", "terraform")
	h := newHarness(t)
	st := h.stack("", "platform", "")
	dir := t.TempDir()
	files := map[string]string{
		"root.hcl":           terragruntRoot,
		"net/terragrunt.hcl": `include "root" { path = find_in_parent_folders("root.hcl") }`,
		"net/main.tf": `
resource "terraform_data" "vpc" { input = "10.0.0.0/16" }
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
variable "fail" {
  type    = bool
  default = false
}
resource "terraform_data" "app" {
  input = var.cidr
  lifecycle {
    postcondition {
      condition     = !var.fail
      error_message = "app failed on purpose"
    }
  }
}
`,
	}
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		must(t, os.MkdirAll(filepath.Dir(p), 0o755))
		must(t, os.WriteFile(p, []byte(content), 0o644))
	}

	_, spec := h.applyPhase(st.ID)
	run := &cli{t: t, bin: tg, dir: dir, env: toolEnv(h, st.ID, spec.StateToken, "TG_TF_PATH="+tfBin, "TF_VAR_fail=true")}
	out, err := run.run("run", "--all", "--non-interactive", "--", "apply", "-input=false", "-auto-approve")
	if err == nil || !strings.Contains(out, "app failed on purpose") {
		t.Fatalf("the app unit should fail: %v\n%s", err, out)
	}
	units := h.units(st.ID)
	if u := units["net"]; u.Version != 1 || u.Resources != 1 {
		t.Fatalf("net must keep its state although app failed (CAS1.3): %+v\n%s", units, out)
	}

	run.env = toolEnv(h, st.ID, spec.StateToken, "TG_TF_PATH="+tfBin)
	run.must("run", "--all", "--non-interactive", "--", "apply", "-input=false", "-auto-approve")
	units = h.units(st.ID)
	if len(units) != 2 || units["net"].Version != 1 || units["app"].Resources != 1 {
		t.Fatalf("units after a good apply: %+v", units)
	}
	if out := run.must("run", "--all", "--non-interactive", "--", "plan", "-input=false"); strings.Contains(out, "to add") {
		t.Fatalf("a plan after apply should have no changes:\n%s", out)
	}
}
