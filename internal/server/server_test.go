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
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bsteigleder/firstlume/internal/api"
	"github.com/bsteigleder/firstlume/internal/apiclient"
	"github.com/bsteigleder/firstlume/internal/secrets"
	"github.com/bsteigleder/firstlume/internal/store"
)

const testAdminToken = "test-admin-token-0123456789"

type harness struct {
	t     *testing.T
	url   string
	org   string // slug
	store *store.Store
	pool  *pgxpool.Pool
	// blobs is where the server stores state versions.
	blobs store.Blobs
	agent *apiclient.Client
	logs  *syncBuffer
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// newHarness needs a disposable Postgres database, e.g.
// FIRSTLUME_TEST_DATABASE_URL=postgres://firstlume:firstlume@localhost:5432/firstlume_test?sslmode=disable
func newHarness(t *testing.T) *harness {
	t.Helper()
	return newHarnessWith(t, Config{})
}

// newHarnessWith starts the server with cfg (the admin token is set here).
func newHarnessWith(t *testing.T, cfg Config) *harness {
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
	slug := "t-" + randomSuffix()
	orgID, err := st.EnsureOrganization(ctx, slug)
	if err != nil {
		t.Fatal(err)
	}
	agentToken, _ := secrets.RandomToken("fla_")
	if err := st.EnsureAgent(ctx, orgID, "test", agentToken); err != nil {
		t.Fatal(err)
	}

	logs := &syncBuffer{}
	log := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	cfg.AdminToken = testAdminToken
	// With OIDC the public URL is the test server's, known once it listens.
	ts := httptest.NewUnstartedServer(nil)
	ts.Start()
	t.Cleanup(ts.Close)
	if cfg.Auth.OIDC != nil && cfg.Auth.PublicURL == "" {
		cfg.Auth.PublicURL = ts.URL
	}
	srv := New(cfg, st, keys, log)
	ts.Config.Handler = srv.Handler()
	return &harness{t: t, url: ts.URL, org: slug, store: st, pool: pool, blobs: store.PostgresBlobs(pool), agent: apiclient.New(ts.URL, agentToken), logs: logs}
}

func randomSuffix() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x", b)
}

// call performs an admin API request and decodes the response into out.
// Paths starting with "/api/" are used as is; others are relative to the
// harness organization.
func (h *harness) call(method, path string, in any, wantStatus int, out any) []byte {
	h.t.Helper()
	if !strings.HasPrefix(path, "/api/") {
		path = "/api/v1/orgs/" + h.org + path
	}
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
	return raw
}

func (h *harness) folder(parentID, name string) api.Folder {
	h.t.Helper()
	var f api.Folder
	h.call("POST", "/folders", api.CreateFolderRequest{ParentID: parentID, Name: name}, http.StatusCreated, &f)
	return f
}

// stack creates a stack whose source is unique to it, unless path is given.
func (h *harness) stack(folderID, name, path string) api.Stack {
	h.t.Helper()
	if path == "" {
		path = "stacks/" + randomSuffix()
	}
	var st api.Stack
	h.call("POST", "/stacks", api.CreateStackRequest{
		FolderID: folderID, Name: name, Repository: "https://example.com/acme/infra.git", Path: path, GitToken: "ghp_stacktoken",
	}, http.StatusCreated, &st)
	return st
}

func (h *harness) settings(level string, s api.LevelSettings) api.SettingsResponse {
	h.t.Helper()
	var out api.SettingsResponse
	h.call("PUT", level+"/settings", s, http.StatusOK, &out)
	return out
}

func (h *harness) run(stackID string, typ api.RunType) api.Run {
	h.t.Helper()
	var run api.Run
	h.call("POST", "/stacks/"+stackID+"/runs", api.CreateRunRequest{Type: typ}, http.StatusCreated, &run)
	return run
}

func (h *harness) claim() *api.Assignment {
	h.t.Helper()
	a, err := h.agent.Claim(context.Background())
	if err != nil {
		h.t.Fatal(err)
	}
	return a
}

// spec claims the next phase and returns its executor client and spec.
func (h *harness) spec(wantRun string) (*apiclient.Client, *api.ExecSpec) {
	h.t.Helper()
	asg := h.claim()
	if asg == nil || asg.RunID != wantRun {
		h.t.Fatalf("expected to claim %s, got %+v", wantRun, asg)
	}
	exec := apiclient.New(h.url, asg.Token)
	spec, err := exec.Spec(context.Background())
	if err != nil {
		h.t.Fatal(err)
	}
	return exec, spec
}

func vars(spec *api.ExecSpec) map[string]string {
	out := map[string]string{}
	for _, v := range spec.Variables {
		out[string(v.Category)+"/"+v.Key] = v.Value
	}
	return out
}

func ptr[T any](v T) *T { return &v }

func TestApplyRunLifecycle(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	st := h.stack("", "network", "environments/prod")
	h.settings("/stacks/"+st.ID, api.LevelSettings{Variables: []api.Variable{
		{Key: "region", Category: api.CategoryTerraform, Value: "us-east-1"},
		{Key: "DB_PASSWORD", Category: api.CategoryEnv, Sensitive: true, Value: "hunter2"},
	}})

	run := h.run(st.ID, api.RunApply)
	if run.Status != api.StatusQueued || run.Ref != "main" || run.StackID != st.ID {
		t.Fatalf("unexpected run %+v", run)
	}
	// A second run on the same stack must wait for the first.
	second := h.run(st.ID, api.RunPlan)

	exec, spec := h.spec(run.ID)
	if next := h.claim(); next != nil {
		t.Fatalf("claimed %s while the stack was locked", next.RunID)
	}
	if spec.GitToken != "ghp_stacktoken" || spec.Path != "environments/prod" || spec.StackID != st.ID {
		t.Fatalf("unexpected spec %+v", spec)
	}
	if v := vars(spec); len(v) != 2 || v["env/DB_PASSWORD"] != "hunter2" || v["terraform/region"] != "us-east-1" {
		t.Fatalf("unexpected variables %v", v)
	}

	sha := strings.Repeat("a", 40)
	cfg := &api.StackConfig{Version: 1, Engine: "terraform", EngineVersion: "1.15.6"}
	must(t, exec.SetCommit(ctx, api.CommitRequest{SHA: sha, AuthorName: "Ana", AuthorEmail: "ana@example.com", Message: "Add VPC", Config: cfg}))
	must(t, exec.AppendLog(ctx, []byte("Terraform will perform the following actions\n")))
	must(t, exec.PutArtifact(ctx, "plan", []byte("binary-plan")))
	done, err := exec.Complete(ctx, api.CompleteRequest{Success: true, HasChanges: true, PlanSummary: &api.PlanSummary{Add: 2}})
	must(t, err)
	if done.Status != api.StatusAwaitingApproval || done.CommitSHA != sha || done.PlanSummary.Add != 2 ||
		done.CommitAuthorEmail != "ana@example.com" || done.CommitMessage != "Add VPC" {
		t.Fatalf("unexpected run after plan %+v", done)
	}
	h.call("GET", "/stacks/"+st.ID, nil, http.StatusOK, &st)
	if st.Config == nil || st.Config.EngineVersion != "1.15.6" {
		t.Fatalf("stack config not recorded: %+v", st.Config)
	}

	// The plan token is useless once the phase is over.
	if err := exec.AppendLog(ctx, []byte("late\n")); !errors.Is(err, apiclient.ErrNoLongerOwner) {
		t.Fatalf("late log append: %v", err)
	}
	if h.claim() != nil {
		t.Fatal("claimed work while waiting for approval")
	}

	h.call("POST", "/runs/"+run.ID+"/approve", nil, http.StatusOK, &run)
	if run.Status != api.StatusApplyQueued || run.ApprovedBy != adminIdentity {
		t.Fatalf("unexpected run after approve %+v", run)
	}
	h.call("POST", "/runs/"+run.ID+"/approve", nil, http.StatusConflict, nil)

	exec, spec = h.spec(run.ID)
	if spec.Phase != api.PhaseApply || spec.CommitSHA != sha {
		t.Fatalf("apply spec %+v, want the planned commit", spec)
	}
	plan, err := exec.GetArtifact(ctx, "plan")
	must(t, err)
	if string(plan) != "binary-plan" {
		t.Fatalf("plan artifact = %q", plan)
	}
	if err := exec.SetCommit(ctx, api.CommitRequest{SHA: sha}); err == nil {
		t.Fatal("commit must not change during apply")
	}
	must(t, exec.AppendLog(ctx, []byte("Apply complete!\n")))
	done, err = exec.Complete(ctx, api.CompleteRequest{Success: true})
	must(t, err)
	if done.Status != api.StatusApplied || done.FinishedAt == nil {
		t.Fatalf("unexpected final run %+v", done)
	}

	logs := h.text("/runs/" + run.ID + "/logs")
	if logs != "Terraform will perform the following actions\nApply complete!\n" {
		t.Fatalf("logs = %q", logs)
	}
	// Now the queued plan run is unblocked.
	if asg := h.claim(); asg == nil || asg.RunID != second.ID {
		t.Fatalf("expected the second run, got %+v", asg)
	}
}

func TestAutoApproveAndFailure(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	st := h.stack("", "network", "")
	h.settings("", api.LevelSettings{RequireApproval: ptr(false)})

	var run api.Run
	h.call("POST", "/stacks/"+st.ID+"/runs", api.CreateRunRequest{Type: api.RunApply, Ref: "feature/x"}, http.StatusCreated, &run)
	if run.Ref != "feature/x" {
		t.Fatalf("ref = %q", run.Ref)
	}
	exec, _ := h.spec(run.ID)
	done, err := exec.Complete(ctx, api.CompleteRequest{Success: true, HasChanges: true})
	must(t, err)
	if done.Status != api.StatusApplyQueued {
		t.Fatalf("no approval required: should queue the apply, got %s", done.Status)
	}

	// The executor dies: the agent reports it and the run fails.
	asg := h.claim()
	must(t, h.agent.ReportFailure(ctx, asg.RunID, asg.Phase, "OOMKilled"))
	h.call("GET", "/runs/"+run.ID, nil, http.StatusOK, &run)
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
	st := h.stack("", "network", "")

	run := h.run(st.ID, api.RunApply)
	queued := h.run(st.ID, api.RunDestroy)
	h.call("POST", "/runs/"+queued.ID+"/cancel", nil, http.StatusOK, &queued)
	if queued.Status != api.StatusCanceled {
		t.Fatalf("cancel: %s", queued.Status)
	}

	exec, _ := h.spec(run.ID)
	done, err := exec.Complete(ctx, api.CompleteRequest{Success: true, HasChanges: false})
	must(t, err)
	if done.Status != api.StatusNoChanges {
		t.Fatalf("status = %s, want no_changes", done.Status)
	}
	h.call("POST", "/runs/"+run.ID+"/cancel", nil, http.StatusConflict, nil)
	if h.claim() != nil {
		t.Fatal("canceled run was claimed")
	}
}

func TestAuthAndValidation(t *testing.T) {
	h := newHarness(t)

	req, _ := http.NewRequest("GET", h.url+"/api/v1/orgs", nil)
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

	bad := []api.CreateStackRequest{
		{Name: "x", Repository: "--upload-pack=evil"},
		{Name: "x", Repository: "https://h/r.git", Path: "../../etc"},
		{Name: "x", Repository: "https://h/r.git", Path: "/etc"},
		{Name: "x", Repository: "https://h/r.git", Ref: "-oops"},
		{Name: "a/b", Repository: "https://h/r.git"},
		{Name: "   ", Repository: "https://h/r.git"},
	}
	for _, b := range bad {
		h.call("POST", "/stacks", b, http.StatusUnprocessableEntity, nil)
	}
	h.call("PUT", "/settings", api.LevelSettings{Variables: []api.Variable{
		{Key: "FIRSTLUME_RUN_TOKEN", Category: api.CategoryEnv, Value: "x"},
	}}, http.StatusUnprocessableEntity, nil)
	h.call("PUT", "/settings", api.LevelSettings{DriftIntervalMinutes: ptr(5)}, http.StatusUnprocessableEntity, nil)
	// A new sensitive variable needs a value.
	h.call("PUT", "/settings", api.LevelSettings{Variables: []api.Variable{
		{Key: "TOKEN", Category: api.CategoryEnv, Sensitive: true},
	}}, http.StatusUnprocessableEntity, nil)
	h.call("GET", "/runs/not-a-uuid", nil, http.StatusNotFound, nil)
	h.call("GET", "/api/v1/orgs/no-such-org/tree", nil, http.StatusNotFound, nil)
	h.call("POST", "/api/v1/orgs", api.CreateOrganizationRequest{Slug: "Bad Slug", Name: "x"}, http.StatusUnprocessableEntity, nil)
}

// CA1.1: nothing of one organization is visible from another.
func TestOrganizationIsolation(t *testing.T) {
	h := newHarness(t)
	f := h.folder("", "Ambientes")
	st := h.stack(f.ID, "prod", "")
	run := h.run(st.ID, api.RunPlan)

	var other api.Organization
	h.call("POST", "/api/v1/orgs", api.CreateOrganizationRequest{Slug: "o-" + randomSuffix(), Name: "Other"}, http.StatusCreated, &other)
	o := "/api/v1/orgs/" + other.Slug
	var tree api.Tree
	h.call("GET", o+"/tree", nil, http.StatusOK, &tree)
	if len(tree.Folders) != 0 || len(tree.Stacks) != 0 {
		t.Fatalf("other organization sees %+v", tree)
	}
	h.call("GET", o+"/stacks/"+st.ID, nil, http.StatusNotFound, nil)
	h.call("GET", o+"/stacks/"+st.ID+"/settings", nil, http.StatusNotFound, nil)
	h.call("GET", o+"/folders/"+f.ID+"/settings", nil, http.StatusNotFound, nil)
	h.call("GET", o+"/runs/"+run.ID, nil, http.StatusNotFound, nil)
	h.call("POST", o+"/runs/"+run.ID+"/cancel", nil, http.StatusNotFound, nil)
	h.call("POST", o+"/folders", api.CreateFolderRequest{ParentID: f.ID, Name: "x"}, http.StatusUnprocessableEntity, nil)
	h.call("POST", o+"/stacks", api.CreateStackRequest{FolderID: f.ID, Name: "x", Repository: "https://h/r.git"}, http.StatusUnprocessableEntity, nil)
	var active []api.ActiveRun
	h.call("GET", o+"/runs?active=true", nil, http.StatusOK, &active)
	if len(active) != 0 {
		t.Fatalf("other organization sees active runs %+v", active)
	}

	// An agent of the other organization never gets this organization's runs.
	ctx := context.Background()
	otherID, err := h.store.EnsureOrganization(ctx, other.Slug)
	must(t, err)
	tok, _ := secrets.RandomToken("fla_")
	must(t, h.store.EnsureAgent(ctx, otherID, "other", tok))
	if a, err := apiclient.New(h.url, tok).Claim(ctx); err != nil || a != nil {
		t.Fatalf("other organization's agent claimed %+v (%v)", a, err)
	}
	if a := h.claim(); a == nil || a.RunID != run.ID {
		t.Fatalf("own agent should claim the run, got %+v", a)
	}
}

// CA1.2–CA1.4.
func TestFolders(t *testing.T) {
	h := newHarness(t)
	parent := ""
	var deepest api.Folder
	for i := 1; i <= api.MaxFolderDepth; i++ {
		deepest = h.folder(parent, fmt.Sprintf("level %d", i))
		if len(deepest.Path) != i {
			t.Fatalf("level %d has path %v", i, deepest.Path)
		}
		parent = deepest.ID
	}
	raw := h.call("POST", "/folders", api.CreateFolderRequest{ParentID: deepest.ID, Name: "too deep"}, http.StatusUnprocessableEntity, nil)
	if !strings.Contains(string(raw), "at most 10 levels") {
		t.Fatalf("depth error: %s", raw)
	}

	prod := h.folder("", "Prod")
	h.call("POST", "/folders", api.CreateFolderRequest{Name: "prod"}, http.StatusConflict, nil)
	h.folder(prod.ID, "prod") // same name under another parent is fine
	h.call("POST", "/folders", api.CreateFolderRequest{ParentID: prod.ID, Name: "PROD"}, http.StatusConflict, nil)

	h.call("DELETE", "/folders/"+prod.ID, nil, http.StatusConflict, nil)
	empty := h.folder("", "empty")
	h.call("PATCH", "/folders/"+empty.ID, api.UpdateFolderRequest{Name: "renamed"}, http.StatusOK, &empty)
	if empty.Name != "renamed" {
		t.Fatalf("rename: %+v", empty)
	}
	h.call("DELETE", "/folders/"+empty.ID, nil, http.StatusNoContent, nil)
	h.call("DELETE", "/folders/"+empty.ID, nil, http.StatusNotFound, nil)

	var tree api.Tree
	h.call("GET", "/tree", nil, http.StatusOK, &tree)
	if len(tree.Folders) != api.MaxFolderDepth+2 {
		t.Fatalf("tree has %d folders", len(tree.Folders))
	}
}

// CA1.5–CA1.7.
func TestStacks(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	f := h.folder("", "Ambientes")

	root := h.stack("", "edge", "./environments/edge/")
	if root.FolderID != "" || root.Path != "environments/edge" || root.Ref != "main" {
		t.Fatalf("unexpected stack %+v", root)
	}
	prod := h.stack(f.ID, "prod", "environments/prod")
	if prod.FolderID != f.ID {
		t.Fatalf("stack not in its folder: %+v", prod)
	}

	// Same repository, ref and path: refused, naming the existing stack.
	raw := h.call("POST", "/stacks", api.CreateStackRequest{
		FolderID: f.ID, Name: "again", Repository: "https://example.com/acme/infra.git", Path: "environments/prod",
	}, http.StatusConflict, nil)
	if !strings.Contains(string(raw), `\"prod\"`) && !strings.Contains(string(raw), `"prod"`) {
		t.Fatalf("conflict should name the stack: %s", raw)
	}
	// Same repository and path on another branch is a different stack.
	h.call("POST", "/stacks", api.CreateStackRequest{
		FolderID: f.ID, Name: "prod-next", Repository: "https://example.com/acme/infra.git", Ref: "next", Path: "environments/prod",
	}, http.StatusCreated, nil)
	// Names are unique per folder, ignoring case.
	h.call("POST", "/stacks", api.CreateStackRequest{FolderID: f.ID, Name: "PROD", Repository: "https://h/r.git"}, http.StatusConflict, nil)
	h.stack("", "prod", "") // fine at the top level

	// Each stack has its own lock: a run on one does not hold the other's queue.
	a := h.run(prod.ID, api.RunApply)
	b := h.run(root.ID, api.RunPlan)
	first := h.claim()
	second := h.claim()
	if first == nil || second == nil || first.RunID == second.RunID ||
		!(first.RunID == a.ID || first.RunID == b.ID) || !(second.RunID == a.ID || second.RunID == b.ID) {
		t.Fatalf("both stacks should run at once: %+v %+v", first, second)
	}

	h.call("DELETE", "/stacks/"+prod.ID, nil, http.StatusConflict, nil) // run in progress
	_, err := apiclient.New(h.url, first.Token).Complete(ctx, api.CompleteRequest{Success: true})
	must(t, err)
	_, err = apiclient.New(h.url, second.Token).Complete(ctx, api.CompleteRequest{Success: true})
	must(t, err)
	h.call("DELETE", "/stacks/"+prod.ID, nil, http.StatusNoContent, nil)
}

// CA1.8, CA1.9, CA1.11: the example in spec 0001, R6.11.
func TestInheritance(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.settings("", api.LevelSettings{RequireApproval: ptr(true), Variables: []api.Variable{
		{Key: "region", Category: api.CategoryTerraform, Value: "us-east-1"},
		{Key: "DB_PASSWORD", Category: api.CategoryEnv, Sensitive: true, Value: "secret-a"},
	}})
	ambientes := h.folder("", "Ambientes")
	prod := h.folder(ambientes.ID, "prod")
	dev := h.folder(ambientes.ID, "dev")
	h.settings("/folders/"+prod.ID, api.LevelSettings{DriftIntervalMinutes: ptr(360), Variables: []api.Variable{
		{Key: "DB_PASSWORD", Category: api.CategoryEnv, Sensitive: true, Value: "secret-b"},
	}})
	h.settings("/folders/"+dev.ID, api.LevelSettings{RequireApproval: ptr(false)})
	kube01 := h.stack(prod.ID, "kube01", "")
	dev01 := h.stack(dev.ID, "dev01", "")
	h.settings("/stacks/"+dev01.ID, api.LevelSettings{Variables: []api.Variable{
		{Key: "region", Category: api.CategoryTerraform, Value: "sa-east-1"},
	}})

	// What the executors get.
	r1 := h.run(kube01.ID, api.RunApply)
	exec1, spec1 := h.spec(r1.ID)
	if v := vars(spec1); v["terraform/region"] != "us-east-1" || v["env/DB_PASSWORD"] != "secret-b" || len(v) != 2 {
		t.Fatalf("kube01 variables %v", v)
	}
	r2 := h.run(dev01.ID, api.RunApply)
	exec2, spec2 := h.spec(r2.ID)
	if v := vars(spec2); v["terraform/region"] != "sa-east-1" || v["env/DB_PASSWORD"] != "secret-a" || len(v) != 2 {
		t.Fatalf("dev01 variables %v", v)
	}
	// Approval: required for kube01 (organization), not for dev01 (dev folder).
	d1, err := exec1.Complete(ctx, api.CompleteRequest{Success: true, HasChanges: true})
	must(t, err)
	d2, err := exec2.Complete(ctx, api.CompleteRequest{Success: true, HasChanges: true})
	must(t, err)
	if d1.Status != api.StatusAwaitingApproval || d2.Status != api.StatusApplyQueued {
		t.Fatalf("approval: kube01 %s, dev01 %s", d1.Status, d2.Status)
	}

	// What the UI shows: effective values with their origin, secrets without values.
	var s api.SettingsResponse
	h.call("GET", "/stacks/"+kube01.ID+"/settings", nil, http.StatusOK, &s)
	eff := map[string]api.EffectiveVariable{}
	for _, v := range s.Effective.Variables {
		eff[v.Key] = v
	}
	if pw := eff["DB_PASSWORD"]; pw.Value != "" || pw.Origin.Kind != api.LevelFolder || pw.Origin.ID != prod.ID ||
		pw.Origin.Name != "prod" || pw.Overrides == nil || pw.Overrides.Kind != api.LevelOrganization {
		t.Fatalf("kube01 DB_PASSWORD %+v", pw)
	}
	if rg := eff["region"]; rg.Value != "us-east-1" || rg.Origin.Kind != api.LevelOrganization || rg.Overrides != nil {
		t.Fatalf("kube01 region %+v", rg)
	}
	if s.Effective.RequireApproval.Value != true || s.Effective.RequireApproval.Origin.Kind != api.LevelOrganization {
		t.Fatalf("kube01 approval %+v", s.Effective.RequireApproval)
	}
	if s.Effective.DriftIntervalMinutes.Value != 360 || s.Effective.DriftIntervalMinutes.Origin.ID != prod.ID {
		t.Fatalf("kube01 drift %+v", s.Effective.DriftIntervalMinutes)
	}
	if len(s.Level.Variables) != 0 || s.Level.RequireApproval != nil {
		t.Fatalf("kube01 defines nothing itself: %+v", s.Level)
	}

	h.call("GET", "/folders/"+prod.ID+"/settings", nil, http.StatusOK, &s)
	if len(s.Level.Variables) != 1 || s.Level.Variables[0].Value != "" || *s.Level.DriftIntervalMinutes != 360 {
		t.Fatalf("prod level %+v", s.Level)
	}
	for _, v := range s.Effective.Variables {
		if v.Key == "DB_PASSWORD" && (v.Overrides == nil || v.Overrides.Kind != api.LevelOrganization) {
			t.Fatalf("prod DB_PASSWORD should override the organization: %+v", v)
		}
	}

	// Nothing defined anywhere: approval required, drift off.
	h.settings("", api.LevelSettings{})
	lone := h.stack("", "lone", "")
	h.call("GET", "/stacks/"+lone.ID+"/settings", nil, http.StatusOK, &s)
	if !s.Effective.RequireApproval.Value || s.Effective.RequireApproval.Origin.Kind != api.LevelDefault ||
		s.Effective.DriftIntervalMinutes.Value != 0 || len(s.Effective.Variables) != 0 {
		t.Fatalf("defaults %+v", s.Effective)
	}
}

// CA1.10: the apply runs with the variables its plan ran with.
func TestApplyUsesPlanVariables(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.settings("", api.LevelSettings{Variables: []api.Variable{{Key: "size", Category: api.CategoryTerraform, Value: "small"}}})
	st := h.stack("", "app", "")
	run := h.run(st.ID, api.RunApply)
	exec, spec := h.spec(run.ID)
	if vars(spec)["terraform/size"] != "small" {
		t.Fatalf("plan variables %v", vars(spec))
	}
	_, err := exec.Complete(ctx, api.CompleteRequest{Success: true, HasChanges: true})
	must(t, err)

	h.settings("", api.LevelSettings{Variables: []api.Variable{{Key: "size", Category: api.CategoryTerraform, Value: "huge"}}})
	h.call("POST", "/runs/"+run.ID+"/approve", nil, http.StatusOK, nil)
	_, spec = h.spec(run.ID)
	if spec.Phase != api.PhaseApply || vars(spec)["terraform/size"] != "small" {
		t.Fatalf("apply variables %v, want the plan's", vars(spec))
	}
}

// CA1.12: secret values never come back from the API nor reach the server log.
func TestSecretsNeverReturned(t *testing.T) {
	h := newHarness(t)
	f := h.folder("", "Ambientes")
	st := h.stack(f.ID, "prod", "")
	secretsByLevel := map[string]string{"": "org-s3cr3t", "/folders/" + f.ID: "folder-s3cr3t", "/stacks/" + st.ID: "stack-s3cr3t"}
	var bodies []string
	for level, secret := range secretsByLevel {
		bodies = append(bodies, string(h.call("PUT", level+"/settings", api.LevelSettings{Variables: []api.Variable{
			{Key: "PASSWORD", Category: api.CategoryEnv, Sensitive: true, Value: secret},
		}}, http.StatusOK, nil)))
		bodies = append(bodies, string(h.call("GET", level+"/settings", nil, http.StatusOK, nil)))
		// Saving again without the value keeps it.
		h.call("PUT", level+"/settings", api.LevelSettings{Variables: []api.Variable{
			{Key: "PASSWORD", Category: api.CategoryEnv, Sensitive: true},
		}}, http.StatusOK, nil)
	}
	bodies = append(bodies, string(h.call("GET", "/tree", nil, http.StatusOK, nil)))
	bodies = append(bodies, string(h.call("GET", "/stacks/"+st.ID, nil, http.StatusOK, nil)))
	for _, secret := range secretsByLevel {
		for _, b := range bodies {
			if strings.Contains(b, secret) {
				t.Fatalf("API returned %q: %s", secret, b)
			}
		}
		if strings.Contains(h.logs.String(), secret) {
			t.Fatalf("server log contains %q", secret)
		}
	}
	// The executor gets the nearest one, still intact after the re-save.
	run := h.run(st.ID, api.RunPlan)
	if _, spec := h.spec(run.ID); vars(spec)["env/PASSWORD"] != "stack-s3cr3t" {
		t.Fatalf("executor variables %v", vars(spec))
	}
}

func TestDriftDetection(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.settings("", api.LevelSettings{RequireApproval: ptr(false)}) // must not make a drift run apply
	f := h.folder("", "monitored")
	watched := h.stack(f.ID, "watched", "")
	unwatched := h.stack("", "unwatched", "")
	h.settings("/folders/"+f.ID, api.LevelSettings{DriftIntervalMinutes: ptr(60)})

	n, err := h.store.ScheduleDriftRuns(ctx)
	must(t, err)
	if n < 1 {
		t.Fatalf("expected a drift run to be scheduled, got %d", n)
	}
	var runs []api.Run
	h.call("GET", "/stacks/"+watched.ID+"/runs", nil, http.StatusOK, &runs)
	if len(runs) != 1 || runs[0].Type != api.RunDrift || runs[0].CreatedBy != api.DriftActor {
		t.Fatalf("unexpected runs %+v", runs)
	}
	h.call("GET", "/stacks/"+unwatched.ID+"/runs", nil, http.StatusOK, &runs)
	if len(runs) != 0 {
		t.Fatalf("drift off by default, got %+v", runs)
	}
	_, err = h.store.ScheduleDriftRuns(ctx)
	must(t, err)
	h.call("GET", "/stacks/"+watched.ID+"/runs", nil, http.StatusOK, &runs)
	if len(runs) != 1 {
		t.Fatalf("drift run scheduled twice: %d runs", len(runs))
	}

	// The plan finds changes: the run ends as drifted and nothing is applied.
	exec, _ := h.spec(runs[0].ID)
	done, err := exec.Complete(ctx, api.CompleteRequest{Success: true, HasChanges: true, PlanSummary: &api.PlanSummary{Change: 1}})
	must(t, err)
	if done.Status != api.StatusDrifted {
		t.Fatalf("status = %s, want drifted", done.Status)
	}
	if a := h.claim(); a != nil {
		t.Fatalf("a drift run must never reach the apply phase, claimed %+v", a)
	}
	h.call("GET", "/stacks/"+watched.ID, nil, http.StatusOK, &watched)
	if watched.Drift == nil || watched.Drift.Status != api.DriftDrifted || watched.Drift.RunID != done.ID {
		t.Fatalf("unexpected drift %+v", watched.Drift)
	}

	// A manual check that finds nothing marks the stack in sync.
	manual := h.run(watched.ID, api.RunDrift)
	exec, _ = h.spec(manual.ID)
	_, err = exec.Complete(ctx, api.CompleteRequest{Success: true})
	must(t, err)
	h.call("GET", "/stacks/"+watched.ID, nil, http.StatusOK, &watched)
	if watched.Drift == nil || watched.Drift.Status != api.DriftInSync || watched.Drift.RunID != manual.ID {
		t.Fatalf("unexpected drift %+v", watched.Drift)
	}
}

// CA1.16 (API): the organization-wide queue of active runs.
func TestActiveRuns(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	amb := h.folder("", "Ambientes")
	prod := h.folder(amb.ID, "prod")
	a := h.stack(prod.ID, "kube01", "")
	b := h.stack(amb.ID, "edge", "")
	c := h.stack("", "dns", "")
	ra := h.run(a.ID, api.RunApply)
	h.run(b.ID, api.RunPlan)
	h.run(c.ID, api.RunPlan)
	exec, _ := h.spec(ra.ID)
	must(t, exec.SetCommit(ctx, api.CommitRequest{SHA: strings.Repeat("b", 40), AuthorName: "Ana", AuthorEmail: "ana@example.com", Message: "Bump nodes"}))
	_, err := exec.Complete(ctx, api.CompleteRequest{Success: true, HasChanges: true})
	must(t, err)

	var active []api.ActiveRun
	h.call("GET", "/runs?active=true", nil, http.StatusOK, &active)
	if len(active) != 3 {
		t.Fatalf("active runs %+v", active)
	}
	first := active[0]
	if first.ID != ra.ID || first.Status != api.StatusAwaitingApproval || first.StackName != "kube01" ||
		strings.Join(first.FolderPath, "/") != "Ambientes/prod" || first.CommitAuthorName != "Ana" || first.CommitMessage != "Bump nodes" {
		t.Fatalf("awaiting approval should come first with its context: %+v", first)
	}
	for _, r := range active {
		if r.StackID == c.ID && len(r.FolderPath) != 0 {
			t.Fatalf("top-level stack has a folder path: %+v", r)
		}
	}
	h.call("GET", "/runs", nil, http.StatusBadRequest, nil)
}

// CA1b.1–CA1b.3, CA1b.6, CA1b.7: changes, confirmation of destructive changes,
// steps with their own logs, and what the run was configured with.
func TestPlanReview(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	prod := h.folder("", "prod")
	h.settings("/folders/"+prod.ID, api.LevelSettings{ConfirmDestructive: ptr(true), Variables: []api.Variable{
		{Key: "region", Category: api.CategoryTerraform, Value: "sa-east-1"},
		{Key: "DB_PASSWORD", Category: api.CategoryEnv, Sensitive: true, Value: "pl4n-s3cret"},
	}})
	st := h.stack(prod.ID, "network", "")
	run := h.run(st.ID, api.RunApply)
	exec, _ := h.spec(run.ID)

	// The executor reports its steps; logs land on the step that was running.
	step := func(name string, status api.StepStatus) {
		t.Helper()
		must(t, exec.Step(ctx, api.StepRequest{Name: name, Kind: api.StepCore, Status: status}))
	}
	step("Checkout", api.StepRunning)
	must(t, exec.AppendLog(ctx, []byte("cloning\n")))
	cfg := &api.StackConfig{Version: 1, Engine: "terraform", EngineVersion: "1.15.8", TerragruntVersion: "1.2.1", RunAll: true}
	must(t, exec.SetCommit(ctx, api.CommitRequest{SHA: strings.Repeat("c", 40), Config: cfg}))
	step("Checkout", api.StepSucceeded)
	step("Init", api.StepRunning)
	must(t, exec.AppendLog(ctx, []byte("initializing\n")))
	step("Init", api.StepSucceeded)
	step("Plan", api.StepRunning)
	must(t, exec.AppendLog(ctx, []byte("planning\n")))
	if err := exec.Step(ctx, api.StepRequest{Name: "Nope", Kind: api.StepCore, Status: api.StepSucceeded}); err == nil {
		t.Fatal("finishing a step that never started was accepted")
	}

	str := func(v string) *string { return &v }
	changes := []api.ResourceChange{
		{Unit: "db", Address: "aws_db_instance.legacy", Type: "aws_db_instance", Name: "legacy", Action: api.ActionDelete},
		{Unit: "net", Address: "aws_subnet.a", Type: "aws_subnet", Name: "a", Action: api.ActionReplace,
			Attributes: []api.AttributeChange{{Path: "cidr_block", Before: str("10.0.1.0/24"), After: str("10.0.16.0/20"), ForcesReplacement: true}}},
		{Unit: "net", Address: "aws_vpc.main", Type: "aws_vpc", Name: "main", Action: api.ActionUpdate},
		{Unit: "db", Address: "aws_db_instance.orders", Type: "aws_db_instance", Name: "orders", Action: api.ActionCreate},
	}
	must(t, exec.PutChanges(ctx, changes))
	step("Plan", api.StepSucceeded)
	done, err := exec.Complete(ctx, api.CompleteRequest{Success: true, HasChanges: true,
		PlanSummary: &api.PlanSummary{Add: 1, Change: 1, Replace: 1, Destroy: 1}})
	must(t, err)
	if done.Status != api.StatusAwaitingApproval || done.PlanSummary.Replace != 1 {
		t.Fatalf("unexpected run %+v", done)
	}

	var got []api.ResourceChange
	h.call("GET", "/runs/"+run.ID+"/changes", nil, http.StatusOK, &got)
	if len(got) != 4 || got[1].Action != api.ActionReplace || !got[1].Attributes[0].ForcesReplacement {
		t.Fatalf("changes %+v", got)
	}
	var steps []api.RunStep
	h.call("GET", "/runs/"+run.ID+"/steps", nil, http.StatusOK, &steps)
	if len(steps) != 3 || steps[0].Name != "Checkout" || steps[2].Name != "Plan" || steps[2].Status != api.StepSucceeded ||
		steps[1].StartedAt == nil || steps[1].FinishedAt == nil {
		t.Fatalf("steps %+v", steps)
	}
	if l := h.text("/runs/" + run.ID + "/logs?step=Init"); l != "initializing\n" {
		t.Fatalf("Init log = %q", l)
	}

	// What the run was configured with, captured at plan time.
	h.call("GET", "/runs/"+run.ID, nil, http.StatusOK, &run)
	if run.Config == nil || !run.Config.RunAll || run.SettingsUsed == nil || !run.SettingsUsed.ConfirmDestructive.Value ||
		run.SettingsUsed.ConfirmDestructive.Origin.ID != prod.ID || len(run.SettingsUsed.Variables) != 2 {
		t.Fatalf("run configuration %+v", run)
	}
	for _, v := range run.SettingsUsed.Variables {
		if v.Sensitive && v.Value != "" {
			t.Fatalf("settings used leaks a secret: %+v", v)
		}
	}
	h.settings("/folders/"+prod.ID, api.LevelSettings{ConfirmDestructive: ptr(true), Variables: []api.Variable{
		{Key: "region", Category: api.CategoryTerraform, Value: "us-east-1"},
		{Key: "DB_PASSWORD", Category: api.CategoryEnv, Sensitive: true},
	}})
	var again api.Run
	h.call("GET", "/runs/"+run.ID, nil, http.StatusOK, &again)
	for _, v := range again.SettingsUsed.Variables {
		if v.Key == "region" && v.Value != "sa-east-1" {
			t.Fatalf("a run shows what it ran with, not today's value: %+v", v)
		}
	}

	// Confirmation required: nothing, or only part of the destructive changes, is refused.
	raw := h.call("POST", "/runs/"+run.ID+"/approve", nil, http.StatusConflict, nil)
	if !strings.Contains(string(raw), "db:aws_db_instance.legacy") || !strings.Contains(string(raw), "net:aws_subnet.a") {
		t.Fatalf("refusal should name the missing resources: %s", raw)
	}
	h.call("POST", "/runs/"+run.ID+"/approve", api.ApproveRequest{Acknowledged: []string{"db:aws_db_instance.legacy"}}, http.StatusConflict, nil)
	h.call("POST", "/runs/"+run.ID+"/approve", api.ApproveRequest{Acknowledged: []string{
		"db:aws_db_instance.legacy", "net:aws_subnet.a", "db:aws_db_instance.orders"}}, http.StatusOK, &run)
	if run.Status != api.StatusApplyQueued || len(run.Acknowledged) != 2 {
		t.Fatalf("approved run %+v", run)
	}

	if a := h.claim(); a == nil || a.RunID != run.ID || a.Phase != api.PhaseApply {
		t.Fatalf("expected the approved apply, got %+v", a)
	}

	// The same plan on a stack outside prod (default settings) needs no confirmation.
	other := h.stack("", "other", "")
	r2 := h.run(other.ID, api.RunApply)
	exec2, _ := h.spec(r2.ID)
	must(t, exec2.PutChanges(ctx, changes))
	_, err = exec2.Complete(ctx, api.CompleteRequest{Success: true, HasChanges: true})
	must(t, err)
	h.call("POST", "/runs/"+r2.ID+"/approve", nil, http.StatusOK, nil)

	// Changes can only be sent during the plan phase.
	if err := exec2.PutChanges(ctx, changes); err == nil {
		t.Fatal("changes accepted after the plan phase")
	}
}

// fakeGitea serves the parts of the Gitea API the connection uses: the
// organization "acme" with 60 repositories, one of them (infra) with stacks.
type fakeGitea struct {
	token string
	url   string
}

func (f *fakeGitea) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "token "+f.token {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	if page == 0 {
		page = 1
	}
	repo := func(name string) map[string]any {
		return map[string]any{"full_name": "acme/" + name, "name": name, "default_branch": "main", "private": name == "infra",
			"clone_url": f.url + "/acme/" + name + ".git", "html_url": f.url + "/acme/" + name, "owner": map[string]string{"login": "acme"}}
	}
	files := map[string]string{
		"environments/prod/.firstlume/config.yaml": "version: 1\nengine: terraform\nengine_version: 1.15.6\n",
		"environments/edge/.firstlume/config.yaml": "version: 1\nengine: opentofu\nengine_version: 1.10.2\n",
		"legacy/vpn/.firstlume/config.yaml":        "version: 1\nengine: terraform\nengine_version: latest\n",
	}
	switch p := strings.TrimPrefix(r.URL.Path, "/api/v1"); {
	case p == "/orgs/acme":
		writeJSON(w, http.StatusOK, map[string]string{"username": "acme"})
	case p == "/orgs/acme/repos":
		var out []map[string]any
		for i := (page-1)*50 + 1; i <= 60 && i <= page*50; i++ {
			name := fmt.Sprintf("repo%02d", i)
			if i == 1 {
				name = "infra"
			}
			out = append(out, repo(name))
		}
		w.Header().Set("X-Total-Count", "60")
		writeJSON(w, http.StatusOK, out)
	case p == "/repos/acme/infra":
		writeJSON(w, http.StatusOK, repo("infra"))
	case p == "/repos/acme/infra/branches":
		writeJSON(w, http.StatusOK, []map[string]string{{"name": "main"}, {"name": "next"}})
	case p == "/repos/acme/infra/branches/main":
		writeJSON(w, http.StatusOK, map[string]any{"commit": map[string]string{"id": "c0ffee"}})
	case p == "/repos/acme/infra/git/trees/c0ffee":
		var tree []map[string]string
		for f := range files {
			dir := strings.TrimSuffix(f, "/config.yaml")
			tree = append(tree, map[string]string{"path": dir, "type": "tree"}, map[string]string{"path": f, "type": "blob"})
		}
		writeJSON(w, http.StatusOK, map[string]any{"tree": tree, "truncated": false, "total_count": len(tree)})
	case strings.HasPrefix(p, "/repos/acme/infra/raw/"):
		content, ok := files[strings.TrimPrefix(p, "/repos/acme/infra/raw/")]
		if !ok || q.Get("ref") != "c0ffee" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(content))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// CA2.1–CA2.9: the Git connection, listing, discovery and stacks created from it.
func TestGitConnection(t *testing.T) {
	h := newHarness(t)
	fg := &fakeGitea{token: "gitea-s3rvice-token"}
	gs := httptest.NewServer(fg)
	defer gs.Close()
	fg.url = gs.URL

	h.call("GET", "/git/connection", nil, http.StatusNotFound, nil)
	h.call("GET", "/git/repos", nil, http.StatusNotFound, nil)
	bad := map[string]api.PutGitConnectionRequest{
		"rejected":  {URL: gs.URL, Account: "acme", Token: "wrong"},
		"not found": {URL: gs.URL, Account: "nobody", Token: fg.token},
		"url must":  {URL: "ftp://x", Account: "acme", Token: fg.token},
		"required":  {URL: gs.URL, Account: "acme"},
	}
	for want, req := range bad {
		raw := h.call("PUT", "/git/connection", req, http.StatusUnprocessableEntity, nil)
		if !strings.Contains(string(raw), want) {
			t.Fatalf("%q: %s", want, raw)
		}
	}
	var conn api.GitConnectionResponse
	raw := h.call("PUT", "/git/connection", api.PutGitConnectionRequest{URL: gs.URL + "/", Account: "acme", Token: fg.token}, http.StatusOK, &conn)
	if conn.Repositories != 60 || conn.AccountKind != "organization" || conn.URL != gs.URL || strings.Contains(string(raw), fg.token) {
		t.Fatalf("connection %s", raw)
	}
	// Saving again without a token keeps the stored one.
	h.call("PUT", "/git/connection", api.PutGitConnectionRequest{URL: gs.URL, Account: "acme"}, http.StatusOK, nil)

	var repos []api.GitRepo
	h.call("GET", "/git/repos", nil, http.StatusOK, &repos)
	if len(repos) != 60 || repos[0].FullName != "acme/infra" || !repos[0].Private {
		t.Fatalf("%d repos, first %+v", len(repos), repos[0])
	}
	var branches []string
	h.call("GET", "/git/repos/acme/infra/branches", nil, http.StatusOK, &branches)
	if strings.Join(branches, ",") != "main,next" {
		t.Fatalf("branches %v", branches)
	}
	h.call("GET", "/git/repos/someone-else/infra/discover", nil, http.StatusNotFound, nil)

	var found api.DiscoverResponse
	h.call("GET", "/git/repos/acme/infra/discover", nil, http.StatusOK, &found)
	if found.Ref != "main" || found.Commit != "c0ffee" || len(found.Stacks) != 3 {
		t.Fatalf("discover %+v", found)
	}
	byPath := map[string]api.DiscoveredStack{}
	for _, d := range found.Stacks {
		byPath[d.Path] = d
	}
	if c := byPath["environments/edge"].Config; c == nil || c.Engine != "opentofu" {
		t.Fatalf("edge %+v", byPath["environments/edge"])
	}
	if !strings.Contains(byPath["legacy/vpn"].Error, "engine_version") || byPath["legacy/vpn"].Config != nil {
		t.Fatalf("invalid config %+v", byPath["legacy/vpn"])
	}

	// A stack from the connection clones with its service token.
	var st api.Stack
	h.call("POST", "/stacks", api.CreateStackRequest{GitRepository: "acme/infra", Name: "prod", Path: "environments/prod"}, http.StatusCreated, &st)
	if st.Repository != gs.URL+"/acme/infra.git" || st.Ref != "main" || st.ConnectionID == "" || st.HasGitToken {
		t.Fatalf("stack %+v", st)
	}
	h.call("POST", "/stacks", api.CreateStackRequest{GitRepository: "other/infra", Name: "x"}, http.StatusUnprocessableEntity, nil)
	h.call("GET", "/git/repos/acme/infra/discover?ref=main", nil, http.StatusOK, &found)
	for _, d := range found.Stacks {
		if (d.Path == "environments/prod") != (d.UsedBy != nil) || (d.UsedBy != nil && d.UsedBy.ID != st.ID) {
			t.Fatalf("in-use marking: %+v", d)
		}
	}
	run := h.run(st.ID, api.RunPlan)
	if _, spec := h.spec(run.ID); spec.GitToken != fg.token || spec.Repository != st.Repository {
		t.Fatalf("executor spec repository %q, token from the connection: %v", spec.Repository, spec.GitToken == fg.token)
	}

	// The service token can never be pointed at another repository.
	other := "https://evil.example.com/x.git"
	h.call("PATCH", "/stacks/"+st.ID, api.UpdateStackRequest{Repository: &other}, http.StatusUnprocessableEntity, nil)
	path := "environments/edge"
	h.call("PATCH", "/stacks/"+st.ID, api.UpdateStackRequest{Path: &path}, http.StatusOK, nil)
	h.call("DELETE", "/git/connection", nil, http.StatusConflict, nil)

	if strings.Contains(h.logs.String(), fg.token) {
		t.Fatal("the service token reached the server log")
	}
}

func (h *harness) text(path string) string {
	h.t.Helper()
	req, _ := http.NewRequest("GET", h.url+"/api/v1/orgs/"+h.org+path, nil)
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

// An organization can change its address; the old one keeps resolving to it
// and no other organization can take it.
func TestChangeOrganizationSlug(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	old := "/api/v1/orgs/" + h.org
	var o api.Organization
	h.call("GET", old, nil, http.StatusOK, &o)

	slug := "n-" + randomSuffix()
	h.call("PATCH", old, api.UpdateOrganizationRequest{Name: o.Name, Slug: "Bad Slug"}, http.StatusUnprocessableEntity, nil)
	var changed api.Organization
	h.call("PATCH", old, api.UpdateOrganizationRequest{Name: o.Name, Slug: slug}, http.StatusOK, &changed)
	if changed.ID != o.ID || changed.Slug != slug {
		t.Fatalf("changed to %+v", changed)
	}
	var got api.Organization
	h.call("GET", old, nil, http.StatusOK, &got)
	if got.ID != o.ID || got.Slug != slug {
		t.Fatalf("old address resolved to %+v", got)
	}
	if id, err := h.store.EnsureOrganization(ctx, h.org); err != nil || id != o.ID {
		t.Fatalf("bootstrap by old slug gave %q (%v), want %q", id, err, o.ID)
	}

	// Neither the old nor the current address can be taken by another organization.
	h.call("POST", "/api/v1/orgs", api.CreateOrganizationRequest{Slug: h.org, Name: "x"}, http.StatusConflict, nil)
	h.call("POST", "/api/v1/orgs", api.CreateOrganizationRequest{Slug: slug, Name: "x"}, http.StatusConflict, nil)
	var other api.Organization
	h.call("POST", "/api/v1/orgs", api.CreateOrganizationRequest{Slug: "o-" + randomSuffix(), Name: "Other"}, http.StatusCreated, &other)
	h.call("PATCH", "/api/v1/orgs/"+other.Slug, api.UpdateOrganizationRequest{Name: "Other", Slug: h.org}, http.StatusConflict, nil)
	h.call("PATCH", "/api/v1/orgs/"+other.Slug, api.UpdateOrganizationRequest{Name: "Other", Slug: slug}, http.StatusConflict, nil)

	// The organization can take its old address back.
	h.call("PATCH", "/api/v1/orgs/"+slug, api.UpdateOrganizationRequest{Name: o.Name, Slug: h.org}, http.StatusOK, &changed)
	if changed.Slug != h.org {
		t.Fatalf("taking the old address back gave %+v", changed)
	}
	h.call("GET", "/api/v1/orgs/"+slug, nil, http.StatusOK, &got)
	if got.ID != o.ID || got.Slug != h.org {
		t.Fatalf("second address resolved to %+v", got)
	}
}
