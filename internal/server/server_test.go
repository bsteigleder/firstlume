package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/bsteigleder/firstlume/internal/api"
	"github.com/bsteigleder/firstlume/internal/apiclient"
	"github.com/bsteigleder/firstlume/internal/secrets"
	"github.com/bsteigleder/firstlume/internal/store"
)

const testAdminToken = "test-admin-token-0123456789"

type harness struct {
	t     *testing.T
	url   string
	store *store.Store
	agent *apiclient.Client
}

// newHarness needs a disposable Postgres database, e.g.
// FIRSTLUME_TEST_DATABASE_URL=postgres://firstlume:firstlume@localhost:5432/firstlume_test?sslmode=disable
func newHarness(t *testing.T) *harness {
	t.Helper()
	dbURL := os.Getenv("FIRSTLUME_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("FIRSTLUME_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := store.Connect(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := store.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	master := make([]byte, 32)
	_, _ = rand.Read(master)
	keys, err := secrets.NewKeys(master)
	if err != nil {
		t.Fatal(err)
	}
	st := store.New(pool, keys)

	// Each test gets its own organization, so tests don't see each other's runs.
	orgID, err := st.EnsureOrganization(ctx, "test-"+t.Name()+"-"+randomSuffix())
	if err != nil {
		t.Fatal(err)
	}
	agentToken, _ := secrets.RandomToken("fla_")
	if err := st.EnsureAgent(ctx, orgID, "test", agentToken); err != nil {
		t.Fatal(err)
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(Config{OrganizationID: orgID, AdminToken: testAdminToken}, st, keys, log)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return &harness{t: t, url: ts.URL, store: st, agent: apiclient.New(ts.URL, agentToken)}
}

func randomSuffix() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x", b)
}

// call performs an admin API request and decodes the response into out.
func (h *harness) call(method, path string, in any, wantStatus int, out any) {
	h.t.Helper()
	var body io.Reader
	if in != nil {
		raw, _ := json.Marshal(in)
		body = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, h.url+path, body)
	req.Header.Set("Authorization", "Bearer "+testAdminToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != wantStatus {
		h.t.Fatalf("%s %s: status %d, want %d: %s", method, path, resp.StatusCode, wantStatus, raw)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			h.t.Fatalf("decoding %s: %v", raw, err)
		}
	}
}

func (h *harness) environment(autoApprove bool) api.Environment {
	h.t.Helper()
	var tmpl api.Template
	h.call("POST", "/api/v1/templates", api.CreateTemplateRequest{
		Name: "network-" + randomSuffix(), Repository: "https://github.com/example/infra.git", Path: "network",
		Engine: "terraform", EngineVersion: "1.9.8", GitToken: "ghp_templatetoken",
	}, http.StatusCreated, &tmpl)
	if !tmpl.HasGitToken || tmpl.DefaultRef != "main" {
		h.t.Fatalf("unexpected template %+v", tmpl)
	}
	var env api.Environment
	h.call("POST", "/api/v1/environments", api.CreateEnvironmentRequest{
		TemplateID: tmpl.ID, Name: "prod-" + randomSuffix(), AutoApprove: autoApprove,
	}, http.StatusCreated, &env)
	return env
}

func (h *harness) claim() *api.Assignment {
	h.t.Helper()
	a, err := h.agent.Claim(context.Background())
	if err != nil {
		h.t.Fatal(err)
	}
	return a
}

func TestApplyRunLifecycle(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	env := h.environment(false)

	var vars []api.Variable
	h.call("PUT", "/api/v1/environments/"+env.ID+"/variables", api.PutVariablesRequest{Variables: []api.Variable{
		{Key: "region", Category: api.CategoryTerraform, Value: "us-east-1"},
		{Key: "DB_PASSWORD", Category: api.CategoryEnv, Sensitive: true, Value: "hunter2"},
	}}, http.StatusOK, &vars)
	for _, v := range vars {
		if v.Sensitive && v.Value != "" {
			t.Fatal("API echoed a sensitive value")
		}
	}

	var run api.Run
	h.call("POST", "/api/v1/environments/"+env.ID+"/runs", api.CreateRunRequest{Type: api.RunApply}, http.StatusCreated, &run)
	if run.Status != api.StatusQueued || run.Ref != "main" {
		t.Fatalf("unexpected run %+v", run)
	}

	// A second run on the same environment must wait for the first.
	var second api.Run
	h.call("POST", "/api/v1/environments/"+env.ID+"/runs", api.CreateRunRequest{Type: api.RunPlan}, http.StatusCreated, &second)

	asg := h.claim()
	if asg == nil || asg.RunID != run.ID || asg.Phase != api.PhasePlan {
		t.Fatalf("unexpected assignment %+v", asg)
	}
	if next := h.claim(); next != nil {
		t.Fatalf("claimed %s while the environment was locked", next.RunID)
	}

	exec := apiclient.New(h.url, asg.Token)
	spec, err := exec.Spec(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if spec.GitToken != "ghp_templatetoken" || spec.Path != "network" || len(spec.Variables) != 2 {
		t.Fatalf("unexpected spec %+v", spec)
	}
	for _, v := range spec.Variables {
		if v.Key == "DB_PASSWORD" && v.Value != "hunter2" {
			t.Fatal("sensitive variable was not decrypted for the executor")
		}
	}

	sha := strings.Repeat("a", 40)
	must(t, exec.SetCommit(ctx, sha))
	must(t, exec.AppendLog(ctx, []byte("Terraform will perform the following actions\n")))
	must(t, exec.PutArtifact(ctx, "plan", []byte("binary-plan")))
	done, err := exec.Complete(ctx, api.CompleteRequest{Success: true, HasChanges: true, PlanSummary: &api.PlanSummary{Add: 2}})
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != api.StatusAwaitingApproval || done.CommitSHA != sha || done.PlanSummary.Add != 2 {
		t.Fatalf("unexpected run after plan %+v", done)
	}

	// The plan token is useless once the phase is over.
	if err := exec.AppendLog(ctx, []byte("late\n")); !errors.Is(err, apiclient.ErrNoLongerOwner) {
		t.Fatalf("late log append: %v", err)
	}
	if h.claim() != nil {
		t.Fatal("claimed work while waiting for approval")
	}

	h.call("POST", "/api/v1/runs/"+run.ID+"/approve", nil, http.StatusOK, &run)
	if run.Status != api.StatusApplyQueued || run.ApprovedBy != "admin" {
		t.Fatalf("unexpected run after approve %+v", run)
	}
	h.call("POST", "/api/v1/runs/"+run.ID+"/approve", nil, http.StatusConflict, nil)

	asg = h.claim()
	if asg == nil || asg.RunID != run.ID || asg.Phase != api.PhaseApply {
		t.Fatalf("unexpected apply assignment %+v", asg)
	}
	exec = apiclient.New(h.url, asg.Token)
	spec, err = exec.Spec(ctx)
	must(t, err)
	if spec.CommitSHA != sha {
		t.Fatalf("apply spec commit = %q, want the planned commit", spec.CommitSHA)
	}
	plan, err := exec.GetArtifact(ctx, "plan")
	must(t, err)
	if string(plan) != "binary-plan" {
		t.Fatalf("plan artifact = %q", plan)
	}
	if err := exec.SetCommit(ctx, sha); err == nil {
		t.Fatal("commit must not change during apply")
	}
	must(t, exec.AppendLog(ctx, []byte("Apply complete!\n")))
	done, err = exec.Complete(ctx, api.CompleteRequest{Success: true})
	must(t, err)
	if done.Status != api.StatusApplied || done.FinishedAt == nil {
		t.Fatalf("unexpected final run %+v", done)
	}

	logs := h.text("/api/v1/runs/" + run.ID + "/logs")
	if logs != "Terraform will perform the following actions\nApply complete!\n" {
		t.Fatalf("logs = %q", logs)
	}

	// Now the queued plan run is unblocked.
	if asg = h.claim(); asg == nil || asg.RunID != second.ID {
		t.Fatalf("expected the second run, got %+v", asg)
	}
}

func TestAutoApproveAndFailure(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	env := h.environment(true)

	var run api.Run
	h.call("POST", "/api/v1/environments/"+env.ID+"/runs", api.CreateRunRequest{Type: api.RunApply, Ref: "feature/x"}, http.StatusCreated, &run)
	if run.Ref != "feature/x" {
		t.Fatalf("ref = %q", run.Ref)
	}
	asg := h.claim()
	exec := apiclient.New(h.url, asg.Token)
	done, err := exec.Complete(ctx, api.CompleteRequest{Success: true, HasChanges: true})
	must(t, err)
	if done.Status != api.StatusApplyQueued {
		t.Fatalf("auto-approve should queue the apply, got %s", done.Status)
	}

	// The executor dies: the agent reports it and the run fails.
	asg = h.claim()
	must(t, h.agent.ReportFailure(ctx, asg.RunID, asg.Phase, "OOMKilled"))
	h.call("GET", "/api/v1/runs/"+run.ID, nil, http.StatusOK, &run)
	if run.Status != api.StatusFailed || run.Error != "OOMKilled" {
		t.Fatalf("unexpected run %+v", run)
	}
	// A late completion from the dead executor is rejected.
	if _, err := apiclient.New(h.url, asg.Token).Complete(ctx, api.CompleteRequest{Success: true}); !errors.Is(err, apiclient.ErrNoLongerOwner) {
		t.Fatalf("late completion: %v", err)
	}
}

func TestNoChangesAndCancel(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	env := h.environment(false)

	var run, queued api.Run
	h.call("POST", "/api/v1/environments/"+env.ID+"/runs", api.CreateRunRequest{Type: api.RunApply}, http.StatusCreated, &run)
	h.call("POST", "/api/v1/environments/"+env.ID+"/runs", api.CreateRunRequest{Type: api.RunDestroy}, http.StatusCreated, &queued)
	h.call("POST", "/api/v1/runs/"+queued.ID+"/cancel", nil, http.StatusOK, &queued)
	if queued.Status != api.StatusCanceled {
		t.Fatalf("cancel: %s", queued.Status)
	}

	asg := h.claim()
	done, err := apiclient.New(h.url, asg.Token).Complete(ctx, api.CompleteRequest{Success: true, HasChanges: false})
	must(t, err)
	if done.Status != api.StatusNoChanges {
		t.Fatalf("status = %s, want no_changes", done.Status)
	}
	h.call("POST", "/api/v1/runs/"+run.ID+"/cancel", nil, http.StatusConflict, nil)
	if h.claim() != nil {
		t.Fatal("canceled run was claimed")
	}
}

func TestAuthAndValidation(t *testing.T) {
	h := newHarness(t)

	req, _ := http.NewRequest("GET", h.url+"/api/v1/templates", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	resp, err := http.DefaultClient.Do(req)
	must(t, err)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong admin token: %d", resp.StatusCode)
	}

	if _, err := apiclient.New(h.url, "fla_bogus").Claim(context.Background()); err == nil {
		t.Fatal("bogus agent token accepted")
	}
	if _, err := apiclient.New(h.url, "flr_bogus.sig").Spec(context.Background()); err == nil {
		t.Fatal("bogus run token accepted")
	}

	bad := []api.CreateTemplateRequest{
		{Name: "x", Repository: "--upload-pack=evil", Engine: "terraform", EngineVersion: "1.9.8"},
		{Name: "x", Repository: "https://h/r.git", Engine: "terraform", EngineVersion: "latest"},
		{Name: "x", Repository: "https://h/r.git", Engine: "pulumi", EngineVersion: "1.0.0"},
		{Name: "x", Repository: "https://h/r.git", Engine: "terraform", EngineVersion: "1.9.8", Path: "../../etc"},
		{Name: "x", Repository: "https://h/r.git", Engine: "terraform", EngineVersion: "1.9.8", DefaultRef: "-oops"},
	}
	for _, b := range bad {
		h.call("POST", "/api/v1/templates", b, http.StatusUnprocessableEntity, nil)
	}

	env := h.environment(false)
	h.call("PUT", "/api/v1/environments/"+env.ID+"/variables", api.PutVariablesRequest{Variables: []api.Variable{
		{Key: "FIRSTLUME_RUN_TOKEN", Category: api.CategoryEnv, Value: "x"},
	}}, http.StatusUnprocessableEntity, nil)
	h.call("GET", "/api/v1/runs/not-a-uuid", nil, http.StatusNotFound, nil)
}

func (h *harness) text(path string) string {
	h.t.Helper()
	req, _ := http.NewRequest("GET", h.url+path, nil)
	req.Header.Set("Authorization", "Bearer "+testAdminToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return string(raw)
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
