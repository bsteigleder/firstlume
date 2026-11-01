package executor

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/bsteigleder/firstlume/internal/api"
	"github.com/bsteigleder/firstlume/internal/apiclient"
)

// lockedBuffer is a bytes.Buffer safe for the concurrent writes of a command's
// stdout and stderr.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// reports collects what an applyStream sends, keeping the latest snapshot of
// each resource.
type reports struct {
	mu     sync.Mutex
	calls  int
	latest map[string]api.ApplyProgress
	// seen is called with every report, in order.
	seen func(api.ApplyProgressRequest)
}

func (r *reports) send(_ context.Context, req api.ApplyProgressRequest) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if r.latest == nil {
		r.latest = map[string]api.ApplyProgress{}
	}
	for _, p := range req.Resources {
		r.latest[p.Key] = p
	}
	if r.seen != nil {
		r.seen(req)
	}
	return nil
}

func actions(steps []api.ApplyStep) string {
	var out []string
	for _, st := range steps {
		s := string(st.Action)
		switch {
		case st.Failed:
			s += ":failed"
		case st.FinishedAt == nil:
			s += ":running"
		}
		out = append(out, s)
	}
	return strings.Join(out, ",")
}

// TestApplyStreamFromRecordedOutput replays the output of a real Terraform
// 1.15 apply where one resource fails and one depends on it.
func TestApplyStreamFromRecordedOutput(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "apply_failed.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var log lockedBuffer
	var rep reports
	s := newApplyStream(&log, NewMasker([]string{"boom"}).Mask, "", rep.send)
	// Odd-sized writes: lines arrive split, as they do from a pipe.
	for len(raw) > 0 {
		n := min(37, len(raw))
		_, _ = s.Write(raw[:n])
		raw = raw[n:]
	}
	_ = s.Close()
	s.flush(context.Background())

	want := map[string]string{
		"terraform_data.fast":     "delete,create",
		"terraform_data.slow":     "delete,create",
		"terraform_data.replaced": "delete,create",
		"terraform_data.breaks":   "delete,create:failed",
	}
	for key, w := range want {
		if got := actions(rep.latest[key].Steps); got != w {
			t.Errorf("%s steps = %q, want %q", key, got, w)
		}
	}
	if _, ok := rep.latest["terraform_data.after_breaks"]; ok {
		t.Errorf("after_breaks never started but was reported: %+v", rep.latest["terraform_data.after_breaks"])
	}
	errs := rep.latest["terraform_data.breaks"].Errors
	if len(errs) != 1 || errs[0].Summary != "local-exec provisioner error" || errs[0].File != "main.tf" || errs[0].Line != 18 {
		t.Fatalf("breaks errors = %+v", errs)
	}
	if strings.Contains(errs[0].Detail, "boom") || !strings.Contains(errs[0].Detail, "Output: ***") {
		t.Fatalf("detail not masked: %q", errs[0].Detail)
	}

	text := log.String()
	for _, l := range strings.Split(strings.TrimSpace(text), "\n") {
		if strings.HasPrefix(l, "{") {
			t.Fatalf("JSON line in the log: %s", l)
		}
	}
	for _, w := range []string{
		"terraform_data.slow: Creation complete after 12s",
		"│ Error: local-exec provisioner error",
		"│   with terraform_data.breaks,",
		`│   on main.tf line 18, in resource "terraform_data" "breaks":`,
	} {
		if !strings.Contains(text, w) {
			t.Errorf("log lacks %q:\n%s", w, text)
		}
	}

	got := s.failure(errors.New("terraform apply exited with code 1"))
	if got.Error() != "terraform_data.breaks: local-exec provisioner error" {
		t.Fatalf("failure = %q", got)
	}
}

// TestApplyStreamGeneralErrorAndPlainOutput covers R1.6 and R1.7.
func TestApplyStreamGeneralErrorAndPlainOutput(t *testing.T) {
	var log lockedBuffer
	var rep reports
	s := newApplyStream(&log, func(s string) string { return s }, "", rep.send)
	_, _ = s.Write([]byte(`{"@message":"Error: Failed to load plugin schemas","type":"diagnostic","diagnostic":{"severity":"error","summary":"Failed to load plugin schemas","detail":"no provider"}}` + "\n"))
	_ = s.Close()
	if got := s.failure(errors.New("exit 1")); got.Error() != "Failed to load plugin schemas" {
		t.Fatalf("failure = %q", got)
	}
	s.flush(context.Background())
	if rep.calls != 1 || len(rep.latest) != 0 {
		t.Fatalf("want one empty report saying progress is tracked, got %d calls, %v", rep.calls, rep.latest)
	}

	// Not JSON: the log keeps the lines and nothing is reported.
	var plainLog lockedBuffer
	var none reports
	p := newApplyStream(&plainLog, func(s string) string { return s }, "", none.send)
	_, _ = p.Write([]byte("Apply complete! Resources: 1 added, 0 changed, 0 destroyed.\npartial"))
	_ = p.Close()
	p.flush(context.Background())
	if none.calls != 0 || plainLog.String() != "Apply complete! Resources: 1 added, 0 changed, 0 destroyed.\npartial\n" {
		t.Fatalf("calls %d, log %q", none.calls, plainLog.String())
	}
	exitErr := errors.New("exit 1")
	if p.failure(exitErr) != exitErr {
		t.Fatal("plain output must keep the exit error")
	}
}

func TestApplyStreamStopsWhenRunMovesOn(t *testing.T) {
	calls := 0
	s := newApplyStream(&lockedBuffer{}, func(s string) string { return s }, "unit/a",
		func(context.Context, api.ApplyProgressRequest) error { calls++; return apiclient.ErrNoLongerOwner })
	_, _ = s.Write([]byte(`{"@message":"x: Creating...","type":"apply_start","hook":{"resource":{"addr":"x.y"},"action":"create"}}` + "\n"))
	s.flush(context.Background())
	_, _ = s.Write([]byte(`{"@message":"x: done","type":"apply_complete","hook":{"resource":{"addr":"x.y"},"action":"create"}}` + "\n"))
	s.flush(context.Background())
	if calls != 1 {
		t.Fatalf("kept sending after the run moved on: %d calls", calls)
	}
	if _, ok := s.res["unit/a:x.y"]; !ok {
		t.Fatalf("unit not in the key: %v", s.res)
	}
}

const applyDemo = `
variable "fail" {
  type    = bool
  default = false
}
variable "rev" {
  type    = string
  default = "1"
}
resource "terraform_data" "fast" {
  input = "a"
}
resource "terraform_data" "slow" {
  triggers_replace = [var.rev]
  provisioner "local-exec" {
    command = "sleep 3"
  }
}
resource "terraform_data" "breaks" {
  triggers_replace = [var.rev]
  provisioner "local-exec" {
    command     = var.fail ? "echo $SECRET >&2; exit 1" : "true"
    environment = { SECRET = "s3cr3t-value" }
  }
}
resource "terraform_data" "after_breaks" {
  input = terraform_data.breaks.id
}
`

func TestApplyStreamWithRealTerraform(t *testing.T) {
	applyDemoRun(t, testTool(t, "FIRSTLUME_TEST_TERRAFORM", "terraform"))
}

func TestApplyStreamWithRealOpenTofu(t *testing.T) {
	applyDemoRun(t, testTool(t, "FIRSTLUME_TEST_TOFU", "tofu"))
}

// applyDemoRun covers CAP1.1 to CAP1.5 at the executor: a replace reports its
// two steps, a failing resource its error (masked), a resource that depends
// on it is never reported, a slow one is reported while it runs, and the log
// has Terraform's usual lines and no JSON.
func applyDemoRun(t *testing.T, bin string) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(applyDemo), 0o644); err != nil {
		t.Fatal(err)
	}
	env := append(inheritedEnv(), "HOME="+t.TempDir(), "TF_IN_AUTOMATION=1", "CHECKPOINT_DISABLE=1")
	var log lockedBuffer
	r := &runner{dir: dir, bin: bin, env: env, out: &log}
	ctx := context.Background()
	for _, args := range [][]string{
		{"init", "-input=false", "-no-color"},
		{"apply", "-input=false", "-no-color", "-auto-approve"},
	} {
		if _, err := r.run(ctx, args...); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, log.String())
		}
	}
	planPath := filepath.Join(dir, "plan.tfplan")
	if code, err := r.run(ctx, "plan", "-input=false", "-no-color", "-detailed-exitcode", "-out="+planPath,
		"-var", "fail=true", "-var", "rev=2", "-replace=terraform_data.fast"); code != 2 {
		t.Fatalf("plan: %d %v\n%s", code, err, log.String())
	}

	var mu sync.Mutex
	slowSeenRunning := false
	rep := reports{seen: func(req api.ApplyProgressRequest) {
		for _, p := range req.Resources {
			if p.Key == "terraform_data.slow" && strings.HasSuffix(actions(p.Steps), "create:running") {
				mu.Lock()
				slowSeenRunning = true
				mu.Unlock()
			}
		}
	}}
	var applyLog lockedBuffer
	ar := &runner{dir: dir, bin: bin, env: env, out: &applyLog}
	s := newApplyStream(&applyLog, NewMasker([]string{"s3cr3t-value"}).Mask, "", rep.send)
	stop := s.start(ctx)
	_, err := ar.runTo(ctx, s, "apply", "-input=false", "-no-color", "-json", "-auto-approve", planPath)
	_ = s.Close()
	stop()
	if err == nil {
		t.Fatal("apply should fail")
	}

	want := map[string]string{
		"terraform_data.fast":   "delete,create",
		"terraform_data.slow":   "delete,create",
		"terraform_data.breaks": "delete,create:failed",
	}
	for key, w := range want {
		if got := actions(rep.latest[key].Steps); got != w {
			t.Errorf("%s steps = %q, want %q", key, got, w)
		}
	}
	if p, ok := rep.latest["terraform_data.after_breaks"]; ok {
		t.Errorf("after_breaks depends on the failure and must not start: %+v", p)
	}
	if !slowSeenRunning {
		t.Error("the slow resource was never reported while it ran")
	}
	errs := rep.latest["terraform_data.breaks"].Errors
	if len(errs) == 0 || errs[0].File != "main.tf" || errs[0].Line == 0 || !strings.Contains(errs[0].Summary, "local-exec") {
		t.Fatalf("breaks errors = %+v", errs)
	}
	if strings.Contains(errs[0].Detail, "s3cr3t-value") {
		t.Fatalf("secret in the error detail: %q", errs[0].Detail)
	}
	if got := s.failure(err).Error(); !strings.HasPrefix(got, "terraform_data.breaks: ") {
		t.Fatalf("failure = %q", got)
	}

	text := applyLog.String()
	for _, l := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "{") {
			t.Fatalf("JSON line in the log: %s", l)
		}
	}
	for _, w := range []string{"terraform_data.fast: Creation complete after", "│ Error: ", "│   with terraform_data.breaks,"} {
		if !strings.Contains(text, w) {
			t.Errorf("log lacks %q:\n%s", w, text)
		}
	}
}

func TestApplyStreamWithRealTerragrunt(t *testing.T) {
	tg := testTool(t, "FIRSTLUME_TEST_TERRAGRUNT", "terragrunt")
	terragruntApplyRun(t, tg, testTool(t, "FIRSTLUME_TEST_TERRAFORM", "terraform"))
}

func TestApplyStreamWithRealTerragruntAndOpenTofu(t *testing.T) {
	tg := testTool(t, "FIRSTLUME_TEST_TERRAGRUNT", "terragrunt")
	terragruntApplyRun(t, tg, testTool(t, "FIRSTLUME_TEST_TOFU", "tofu"))
}

// terragruntApplyRun covers CAP2.1 to CAP2.3: in run --all, each resource is
// reported under its unit even when two units share an address, a failure in
// one unit marks nothing in another, a unit that depends on the failed one
// never runs, and the log keeps the unit on each line.
func terragruntApplyRun(t *testing.T, tg, engine string) {
	root := t.TempDir()
	files := map[string]string{
		"root.hcl":           "",
		"net/terragrunt.hcl": `include "root" { path = find_in_parent_folders("root.hcl") }`,
		"net/main.tf": `
resource "terraform_data" "shared" { input = "net" }
resource "terraform_data" "breaks" {
  provisioner "local-exec" { command = "echo net is down >&2; exit 1" }
}`,
		"app/terragrunt.hcl": `include "root" { path = find_in_parent_folders("root.hcl") }`,
		"app/main.tf":        `resource "terraform_data" "shared" { input = "app" }`,
		"web/terragrunt.hcl": `
include "root" { path = find_in_parent_folders("root.hcl") }
dependencies { paths = ["../net"] }`,
		"web/main.tf": `resource "terraform_data" "site" { input = "web" }`,
	}
	for name, content := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	work := t.TempDir()
	plans := filepath.Join(work, "plans")
	env := append(inheritedEnv(), "HOME="+t.TempDir())
	env = append(env, toolEnv(engine, filepath.Join(work, "plugin-cache"), true)...)
	var log lockedBuffer
	r := &runner{dir: root, bin: tg, env: env, out: &log}
	ctx := context.Background()
	if code, err := r.run(ctx, "run", "--all", "--non-interactive", "--out-dir", plans, "--json-out-dir", filepath.Join(work, "pj"), "--",
		"plan", "-input=false", "-no-color", "-detailed-exitcode"); code != 2 {
		t.Fatalf("plan: %d %v\n%s", code, err, log.String())
	}

	wrap, err := wrapperEnv(engine, plans, filepath.Join(work, "engine"))
	if err != nil {
		t.Fatal(err)
	}
	var applyLog lockedBuffer
	var rep reports
	ar := &runner{dir: root, bin: tg, env: append(append([]string(nil), env...), wrap...), out: &applyLog}
	s := newApplyStream(&applyLog, func(s string) string { return s }, "", rep.send)
	stop := s.start(ctx)
	_, err = ar.runTo(ctx, s, "run", "--all", "--non-interactive", "--out-dir", plans, "--", "apply", "-input=false", "-no-color", "-json")
	_ = s.Close()
	stop()
	if err == nil {
		t.Fatalf("apply should fail:\n%s", applyLog.String())
	}

	want := map[string]string{
		"net:terraform_data.shared": "create",
		"app:terraform_data.shared": "create",
		"net:terraform_data.breaks": "create:failed",
	}
	for key, w := range want {
		if got := actions(rep.latest[key].Steps); got != w {
			t.Errorf("%s steps = %q, want %q\n%s", key, got, w, applyLog.String())
		}
	}
	for key := range rep.latest {
		if _, ok := want[key]; !ok {
			t.Errorf("unexpected resource %q (web depends on net and must not run)", key)
		}
	}
	if errs := rep.latest["net:terraform_data.breaks"].Errors; len(errs) == 0 || errs[0].File != "main.tf" {
		t.Errorf("breaks errors = %+v", errs)
	}
	if got := s.failure(err).Error(); !strings.HasPrefix(got, "[net] terraform_data.breaks: ") {
		t.Errorf("failure = %q", got)
	}

	text := applyLog.String()
	if strings.Contains(text, unitTag) {
		t.Fatalf("unit tag leaked into the log:\n%s", text)
	}
	for _, l := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "{") || strings.HasPrefix(l, "[net] {") || strings.HasPrefix(l, "[app] {") {
			t.Fatalf("JSON line in the log: %s", l)
		}
	}
	for _, w := range []string{"[app] terraform_data.shared: Creation complete", "[net] │ Error: ", "[net] │   with terraform_data.breaks,",
		"[net] " + filepath.Base(engine) + ": Initializing"} {
		if !strings.Contains(text, w) {
			t.Errorf("log lacks %q:\n%s", w, text)
		}
	}
}

func TestUnitOf(t *testing.T) {
	plans := t.TempDir()
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"apply", "-json", filepath.Join(plans, "net", "tfplan.tfplan")}, "net"},
		{[]string{"apply", "-json", filepath.Join(plans, "sp1", "n01", "tfplan.tfplan")}, "sp1/n01"},
		{[]string{"apply", "-json", filepath.Join(plans, "tfplan.tfplan")}, ""},
		{[]string{"apply", "-json", "/elsewhere/net/tfplan.tfplan"}, ""},
		{[]string{"apply", "-json", "-auto-approve"}, ""},
	}
	for _, c := range cases {
		if got := unitOf(c.args, plans); got != c.want {
			t.Errorf("unitOf(%v) = %q, want %q", c.args, got, c.want)
		}
	}
}
