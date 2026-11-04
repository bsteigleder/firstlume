package server

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/bsteigleder/firstlume/internal/api"
)

const triggerConfig = "version: 1\nengine: terraform\nengine_version: 1.9.8\n"

// pushGitea adds to fakeGitea what processing a push asks for. Comparisons not
// set are empty, which is what going back from the new commit to the old one
// gives on a normal push.
type pushGitea struct {
	fakeGitea
	mu sync.Mutex
	// compares maps "base...head" to the files of each commit.
	compares map[string][][]string
	// files maps "sha:path" to content; a stack's config is served by default.
	files       map[string]string
	failCompare bool
	// total, when set, is the total_commits Gitea answers.
	total int
	// pull is the pull request of any commit, when set.
	pull int
}

func (f *pushGitea) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "token "+f.token {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	p := strings.TrimPrefix(r.URL.Path, "/api/v1/repos/acme/infra/")
	switch {
	case strings.HasPrefix(p, "compare/"):
		if f.failCompare {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		type file struct {
			Filename string `json:"filename"`
		}
		type commit struct {
			SHA   string `json:"sha"`
			Files []file `json:"files"`
		}
		commits := []commit{}
		for i, names := range f.compares[strings.TrimPrefix(p, "compare/")] {
			c := commit{SHA: fmt.Sprintf("c%d", i), Files: []file{}}
			for _, n := range names {
				c.Files = append(c.Files, file{n})
			}
			commits = append(commits, c)
		}
		total := len(commits)
		if f.total != 0 {
			total = f.total
		}
		writeJSON(w, http.StatusOK, map[string]any{"total_commits": total, "commits": commits})
	case strings.HasPrefix(p, "raw/"):
		name := strings.TrimPrefix(p, "raw/")
		if content, ok := f.files[r.URL.Query().Get("ref")+":"+name]; ok {
			_, _ = w.Write([]byte(content))
			return
		}
		if strings.HasSuffix(name, ".firstlume/config.yaml") {
			_, _ = w.Write([]byte(triggerConfig))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	case strings.HasPrefix(p, "commits/") && strings.HasSuffix(p, "/pull"):
		if f.pull == 0 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"number": f.pull, "html_url": fmt.Sprintf("%s/acme/infra/pulls/%d", f.url, f.pull)})
	default:
		f.fakeGitea.ServeHTTP(w, r)
	}
}

type triggerHarness struct {
	*harness
	gitea   *pushGitea
	hookURL string
	secret  string
}

func newTriggerHarness(t *testing.T) *triggerHarness {
	t.Helper()
	h := newHarness(t)
	g := &pushGitea{fakeGitea: fakeGitea{token: "gitea-s3rvice-token"}, compares: map[string][][]string{}, files: map[string]string{}}
	gs := httptest.NewServer(g)
	t.Cleanup(gs.Close)
	g.url = gs.URL
	h.call("PUT", "/git/connection", api.PutGitConnectionRequest{URL: gs.URL, Account: "acme", Token: g.token}, http.StatusOK, nil)
	var hook api.WebhookSecret
	h.call("POST", "/git/webhook-secret", nil, http.StatusOK, &hook)
	if !strings.Contains(hook.URL, "/webhooks/v1/gitea/") || !slices.Contains(hook.Events, "push") || hook.Secret == "" {
		t.Fatalf("webhook = %+v", hook)
	}
	// The address the server gives out may not be the test server's.
	_, id, _ := strings.Cut(hook.URL, "/webhooks/v1/gitea/")
	return &triggerHarness{harness: h, gitea: g, hookURL: h.url + "/webhooks/v1/gitea/" + id, secret: hook.Secret}
}

func (h *triggerHarness) gitStack(name, path string, mode string) api.Stack {
	h.t.Helper()
	var st api.Stack
	h.call("POST", "/stacks", api.CreateStackRequest{GitRepository: "acme/infra", Name: name, Path: path}, http.StatusCreated, &st)
	if mode != "" {
		h.settings("/stacks/"+st.ID, api.LevelSettings{GitOnPush: &mode})
	}
	return st
}

func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// deliver posts a webhook and returns the status.
func (h *triggerHarness) deliver(event, delivery, signature string, body []byte) int {
	h.t.Helper()
	req, _ := http.NewRequest("POST", h.hookURL, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gitea-Event", event)
	if delivery != "" {
		req.Header.Set("X-Gitea-Delivery", delivery)
	}
	if signature != "" {
		req.Header.Set("X-Gitea-Signature", signature)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

func pushBody(ref, before, after string, total int) []byte {
	body, _ := json.Marshal(map[string]any{
		"ref": ref, "before": before, "after": after, "total_commits": total,
		"repository": map[string]any{"full_name": "acme/infra"},
		"sender":     map[string]any{"login": "ana"},
	})
	return body
}

var deliveries int

// push delivers a push on main and processes it, returning the event's record.
func (h *triggerHarness) push(before, after string, total int) api.GitEvent {
	h.t.Helper()
	return h.pushRef("refs/heads/main", before, after, total)
}

func (h *triggerHarness) pushRef(ref, before, after string, total int) api.GitEvent {
	h.t.Helper()
	deliveries++
	body := pushBody(ref, before, after, total)
	if got := h.deliver("push", fmt.Sprintf("d-%s-%d", randomSuffix(), deliveries), sign(h.secret, body), body); got != http.StatusAccepted {
		h.t.Fatalf("delivery status = %d", got)
	}
	h.srv.drainGitEvents(context.Background())
	return h.lastEvent()
}

func (h *triggerHarness) events(query string) []api.GitEvent {
	h.t.Helper()
	var out []api.GitEvent
	h.call("GET", "/git/events"+query, nil, http.StatusOK, &out)
	return out
}

func (h *triggerHarness) lastEvent() api.GitEvent {
	h.t.Helper()
	ev := h.events("")
	if len(ev) == 0 {
		h.t.Fatal("no events")
	}
	return ev[0]
}

func decision(t *testing.T, ev api.GitEvent, stack string) api.GitEventStack {
	t.Helper()
	for _, d := range ev.Stacks {
		if d.StackName == stack {
			return d
		}
	}
	t.Fatalf("no decision about %s in %+v", stack, ev.Stacks)
	return api.GitEventStack{}
}

func (h *triggerHarness) runsOf(stackID string) []api.Run {
	h.t.Helper()
	var runs []api.Run
	h.call("GET", "/stacks/"+stackID+"/runs", nil, http.StatusOK, &runs)
	return runs
}

func sha(c string) string { return strings.Repeat(c, 40) }

// CAG1.1: only a valid signature gets in, and a delivery counts once.
func TestWebhookSignature(t *testing.T) {
	h := newTriggerHarness(t)
	body := pushBody("refs/heads/main", sha("a"), sha("b"), 1)

	for name, signature := range map[string]string{
		"missing":   "",
		"wrong":     sign("another secret", body),
		"not hex":   "zz",
		"truncated": sign(h.secret, body)[:20],
	} {
		if got := h.deliver("push", "one", signature, body); got != http.StatusUnauthorized {
			t.Errorf("%s signature: status %d", name, got)
		}
	}
	if ev := h.events(""); len(ev) != 0 {
		t.Fatalf("rejected deliveries left events: %+v", ev)
	}

	if got := h.deliver("push", "one", sign(h.secret, body), body); got != http.StatusAccepted {
		t.Fatalf("valid delivery: status %d", got)
	}
	if got := h.deliver("push", "one", sign(h.secret, body), body); got != http.StatusAccepted {
		t.Fatalf("repeated delivery: status %d", got)
	}
	if ev := h.events(""); len(ev) != 1 || ev[0].Sender != "ana" || ev[0].Ref != "main" || ev[0].Status != "pending" {
		t.Fatalf("events = %+v", ev)
	}

	// A signature made for the old secret stops working when it's replaced.
	var hook api.WebhookSecret
	h.call("POST", "/git/webhook-secret", nil, http.StatusOK, &hook)
	if got := h.deliver("push", "two", sign(h.secret, body), body); got != http.StatusUnauthorized {
		t.Errorf("old secret: status %d", got)
	}
	if got := h.deliver("push", "two", sign(hook.Secret, body), body); got != http.StatusAccepted {
		t.Errorf("new secret: status %d", got)
	}

	// Unknown connections answer the same as a wrong signature.
	req, _ := http.NewRequest("POST", h.url+"/webhooks/v1/gitea/00000000-0000-0000-0000-000000000000", bytes.NewReader(body))
	req.Header.Set("X-Gitea-Signature", sign(h.secret, body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("unknown connection: status %d", resp.StatusCode)
	}

	huge := bytes.Repeat([]byte("x"), webhookMaxBytes+1)
	if got := h.deliver("push", "three", sign(hook.Secret, huge), huge); got != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized body: status %d", got)
	}

	var conn api.GitConnection
	h.call("GET", "/git/connection", nil, http.StatusOK, &conn)
	if !conn.WebhookSecretSet {
		t.Error("the connection doesn't say it has a secret")
	}
}

// The payloads Gitea 1.27.3 really sends.
func TestWebhookRealPayloads(t *testing.T) {
	h := newTriggerHarness(t)
	st := h.gitStack("a", "a", api.GitOnPushPlan)
	read := func(name string) []byte {
		data, err := os.ReadFile("testdata/gitea/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}

	tag := read("push_tag.json")
	if got := h.deliver("push", "tag", sign(h.secret, tag), tag); got != http.StatusAccepted {
		t.Fatalf("tag: status %d", got)
	}
	if ev := h.events(""); len(ev) != 0 {
		t.Fatalf("a tag was kept: %+v", ev)
	}

	body := read("push.json")
	var p giteaPush
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatal(err)
	}
	h.gitea.compares[p.Before+"..."+p.After] = [][]string{{"a/main.tf"}}
	if got := h.deliver("push", "real", sign(h.secret, body), body); got != http.StatusAccepted {
		t.Fatalf("push: status %d", got)
	}
	h.srv.drainGitEvents(context.Background())
	ev := h.lastEvent()
	if ev.Repository != "acme/infra" || ev.Ref != "main" || ev.BeforeSHA != p.Before || ev.AfterSHA != p.After || ev.Sender != "devadmin" {
		t.Fatalf("event = %+v", ev)
	}
	if d := decision(t, ev, "a"); d.Outcome != api.OutcomeRunCreated {
		t.Fatalf("decision = %+v", d)
	}
	runs := h.runsOf(st.ID)
	if len(runs) != 1 || runs[0].CommitSHA != p.After || runs[0].CreatedBy != "devadmin" {
		t.Fatalf("runs = %+v", runs)
	}

	fresh := read("push_new_branch.json")
	if got := h.deliver("push", "fresh", sign(h.secret, fresh), fresh); got != http.StatusAccepted {
		t.Fatalf("new branch: status %d", got)
	}
	h.srv.drainGitEvents(context.Background())
	if ev := h.lastEvent(); ev.AffectsAll != api.AffectedNewBranch {
		t.Fatalf("event = %+v", ev)
	}
}

// CAG1.2: with the trigger off nothing runs, and turning it on starts nothing.
func TestGitTriggerOff(t *testing.T) {
	h := newTriggerHarness(t)
	st := h.gitStack("a", "a", "")
	h.gitea.compares[sha("a")+"..."+sha("b")] = [][]string{{"a/main.tf"}}

	if d := decision(t, h.push(sha("a"), sha("b"), 1), "a"); d.Outcome != api.OutcomeTriggerOff {
		t.Fatalf("decision = %+v", d)
	}
	if runs := h.runsOf(st.ID); len(runs) != 0 {
		t.Fatalf("runs = %+v", runs)
	}

	on := api.GitOnPushApply
	out := h.settings("/stacks/"+st.ID, api.LevelSettings{GitOnPush: &on})
	if out.Effective.GitOnPush.Value != on || *out.Level.GitOnPush != on {
		t.Fatalf("settings = %+v", out.Effective.GitOnPush)
	}
	if runs := h.runsOf(st.ID); len(runs) != 0 {
		t.Fatalf("turning it on started %+v", runs)
	}

	raw := h.call("PUT", "/stacks/"+st.ID+"/settings", map[string]any{"git_on_push": "destroy"}, http.StatusUnprocessableEntity, nil)
	if !strings.Contains(string(raw), "git_on_push") {
		t.Errorf("error = %s", raw)
	}
}

// CAG1.3: inherited from a folder, and the stack can override it.
func TestGitTriggerInherited(t *testing.T) {
	h := newTriggerHarness(t)
	folder := h.folder("", "prod")
	var inFolder, overrides api.Stack
	h.call("POST", "/stacks", api.CreateStackRequest{FolderID: folder.ID, GitRepository: "acme/infra", Name: "a", Path: "a"}, http.StatusCreated, &inFolder)
	h.call("POST", "/stacks", api.CreateStackRequest{FolderID: folder.ID, GitRepository: "acme/infra", Name: "b", Path: "b"}, http.StatusCreated, &overrides)
	off, plan := api.GitOnPushOff, api.GitOnPushPlan
	h.settings("/folders/"+folder.ID, api.LevelSettings{GitOnPush: &plan})
	h.settings("/stacks/"+overrides.ID, api.LevelSettings{GitOnPush: &off})

	var got api.SettingsResponse
	h.call("GET", "/stacks/"+inFolder.ID+"/settings", nil, http.StatusOK, &got)
	if got.Effective.GitOnPush.Value != plan || got.Effective.GitOnPush.Origin.ID != folder.ID {
		t.Fatalf("effective = %+v", got.Effective.GitOnPush)
	}

	h.gitea.compares[sha("a")+"..."+sha("b")] = [][]string{{"a/main.tf", "b/main.tf"}}
	ev := h.push(sha("a"), sha("b"), 1)
	if d := decision(t, ev, "a"); d.Outcome != api.OutcomeRunCreated {
		t.Errorf("inheriting: %+v", d)
	}
	if d := decision(t, ev, "b"); d.Outcome != api.OutcomeTriggerOff {
		t.Errorf("overriding: %+v", d)
	}
}

// CAG1.4, CAG1.5, CAG1.11: only the stack that changed runs, even when the
// payload wouldn't tell, and the event says why for each stack.
func TestGitPushAffectsOnlyChangedStacks(t *testing.T) {
	h := newTriggerHarness(t)
	a := h.gitStack("a", "stacks/a", api.GitOnPushPlan)
	b := h.gitStack("b", "stacks/b", api.GitOnPushPlan)

	// Eight commits, with the stack's file only in the first: a payload lists the last five.
	commits := [][]string{{"stacks/a/main.tf"}}
	for i := range 7 {
		commits = append(commits, []string{fmt.Sprintf("docs/%d.txt", i)})
	}
	h.gitea.compares[sha("a")+"..."+sha("b")] = commits
	h.gitea.pull = 12

	ev := h.push(sha("a"), sha("b"), 8)
	if ev.Status != "done" || ev.AffectsAll != "" {
		t.Fatalf("event = %+v", ev)
	}
	da, db := decision(t, ev, "a"), decision(t, ev, "b")
	if da.Outcome != api.OutcomeRunCreated || da.Reason != api.AffectedFiles || da.RunID == "" {
		t.Fatalf("a: %+v", da)
	}
	if db.Outcome != api.OutcomeNotAffected || db.RunID != "" || !slices.Contains(db.Files, "stacks/a/main.tf") || len(db.Files) != 8 {
		t.Fatalf("b: %+v", db)
	}

	runs := h.runsOf(a.ID)
	if len(runs) != 1 || runs[0].ID != da.RunID {
		t.Fatalf("runs of a = %+v", runs)
	}
	r := runs[0]
	if r.Type != api.RunPlan || r.Ref != "main" || r.CommitSHA != sha("b") || r.CreatedBy != "ana" {
		t.Errorf("run = %+v", r)
	}
	tr := r.Trigger
	if tr == nil || tr.Kind != api.TriggerPush || tr.Actor != "ana" || tr.EventID != ev.ID || tr.BeforeSHA != sha("a") ||
		tr.PullRequest != 12 || !strings.HasSuffix(tr.PullRequestURL, "/pulls/12") {
		t.Errorf("trigger = %+v", tr)
	}
	if runs := h.runsOf(b.ID); len(runs) != 0 {
		t.Errorf("runs of b = %+v", runs)
	}

	// The stack's view has only its own decision.
	own := h.events("?stack=" + b.ID)
	if len(own) != 1 || len(own[0].Stacks) != 1 || own[0].Stacks[0].StackName != "b" {
		t.Errorf("events of b = %+v", own)
	}
	h.call("GET", "/git/events?stack=nope", nil, http.StatusUnprocessableEntity, nil)
}

// CAG1.6: the repository can declare more paths, and files that don't count.
func TestGitPushDeclaredPaths(t *testing.T) {
	h := newTriggerHarness(t)
	st := h.gitStack("a", "stacks/a", api.GitOnPushPlan)
	h.gitea.files[sha("b")+":stacks/a/.firstlume/config.yaml"] = triggerConfig + "triggers:\n  paths: [\"modules/**\"]\n  ignore: [\"**/*.md\"]\n"
	h.gitea.files[sha("c")+":stacks/a/.firstlume/config.yaml"] = h.gitea.files[sha("b")+":stacks/a/.firstlume/config.yaml"]

	h.gitea.compares[sha("a")+"..."+sha("b")] = [][]string{{"modules/vpc/main.tf"}}
	if d := decision(t, h.push(sha("a"), sha("b"), 1), "a"); d.Outcome != api.OutcomeRunCreated {
		t.Errorf("declared path: %+v", d)
	}
	h.gitea.compares[sha("b")+"..."+sha("c")] = [][]string{{"stacks/a/README.md"}}
	if d := decision(t, h.push(sha("b"), sha("c"), 1), "a"); d.Outcome != api.OutcomeNotAffected {
		t.Errorf("only ignored files: %+v", d)
	}
	if runs := h.runsOf(st.ID); len(runs) != 1 {
		t.Errorf("runs = %+v", runs)
	}

	// R4.5: a config that can't be understood counts, for the run to show the error.
	h.gitea.files[sha("d")+":stacks/a/.firstlume/config.yaml"] = "version: [nope"
	h.gitea.compares[sha("c")+"..."+sha("d")] = [][]string{{"docs/x.txt"}}
	if d := decision(t, h.push(sha("c"), sha("d"), 1), "a"); d.Outcome != api.OutcomeRunCreated || d.Reason != api.AffectedConfigUnreadable {
		t.Errorf("invalid config: %+v", d)
	}
}

// CAG1.7: with no way of knowing what changed, every stack of the branch counts.
func TestGitPushUnknownChangesAffectAll(t *testing.T) {
	for name, tc := range map[string]struct {
		setup  func(g *pushGitea)
		before string
		want   string
	}{
		"force push": {func(g *pushGitea) {
			g.compares[sha("b")+"..."+sha("a")] = [][]string{{"x.txt"}}
			g.compares[sha("a")+"..."+sha("b")] = [][]string{{"docs/x.txt"}}
		}, sha("a"), api.AffectedForcePush},
		"new branch":        {func(g *pushGitea) {}, sha("0"), api.AffectedNewBranch},
		"compare fails":     {func(g *pushGitea) { g.failCompare = true }, sha("a"), api.AffectedCompareFailed},
		"fewer than pushed": {func(g *pushGitea) { g.compares[sha("a")+"..."+sha("b")] = [][]string{{"docs/x.txt"}} }, sha("a"), api.AffectedCompareIncomplete},
		"no files listed":   {func(g *pushGitea) { g.compares[sha("a")+"..."+sha("b")] = [][]string{{}, {}, {}} }, sha("a"), api.AffectedCompareIncomplete},
	} {
		t.Run(name, func(t *testing.T) {
			h := newTriggerHarness(t)
			a := h.gitStack("a", "stacks/a", api.GitOnPushPlan)
			b := h.gitStack("b", "stacks/b", api.GitOnPushPlan)
			tc.setup(h.gitea)
			ev := h.push(tc.before, sha("b"), 3)
			if ev.AffectsAll != tc.want {
				t.Fatalf("affects_all = %q, want %q", ev.AffectsAll, tc.want)
			}
			for _, st := range []api.Stack{a, b} {
				if d := decision(t, ev, st.Name); d.Outcome != api.OutcomeRunCreated || d.Reason != tc.want {
					t.Errorf("%s: %+v", st.Name, d)
				}
			}
		})
	}
}

// CAG1.8: a tag, a deleted branch and another branch run nothing.
func TestGitPushIgnored(t *testing.T) {
	h := newTriggerHarness(t)
	st := h.gitStack("a", "a", api.GitOnPushApply)

	tag := pushBody("refs/tags/v1", sha("0"), sha("b"), 0)
	deleted := pushBody("refs/heads/main", sha("b"), sha("0"), 0)
	foreign, _ := json.Marshal(map[string]any{"ref": "refs/heads/main", "before": sha("a"), "after": sha("b"),
		"repository": map[string]any{"full_name": "other/infra"}, "sender": map[string]any{"login": "ana"}})
	optionLike := pushBody("refs/heads/--upload-pack=x", sha("a"), sha("b"), 1)
	for name, body := range map[string][]byte{"tag": tag, "deleted branch": deleted, "another account": foreign, "bad ref": optionLike} {
		if got := h.deliver("push", name, sign(h.secret, body), body); got != http.StatusAccepted {
			t.Errorf("%s: status %d", name, got)
		}
	}
	for _, kind := range []string{"create", "delete", "issue_comment", "pull_request"} {
		body := pushBody("refs/heads/main", sha("a"), sha("b"), 1)
		if got := h.deliver(kind, kind, sign(h.secret, body), body); got != http.StatusAccepted {
			t.Errorf("%s: status %d", kind, got)
		}
	}
	if ev := h.events(""); len(ev) != 0 {
		t.Fatalf("kept = %+v", ev)
	}

	h.gitea.compares[sha("a")+"..."+sha("b")] = [][]string{{"a/main.tf"}}
	if d := decision(t, h.pushRef("refs/heads/feature/x", sha("a"), sha("b"), 1), "a"); d.Outcome != api.OutcomeOtherBranch {
		t.Errorf("another branch: %+v", d)
	}
	if runs := h.runsOf(st.ID); len(runs) != 0 {
		t.Errorf("runs = %+v", runs)
	}
}

// CAG1.9: apply stops for approval on the event's commit, and approving applies
// exactly that commit, even with newer ones on the branch.
func TestGitPushApplyWaitsForApproval(t *testing.T) {
	h := newTriggerHarness(t)
	ctx := context.Background()
	st := h.gitStack("a", "a", api.GitOnPushApply)
	h.gitea.compares[sha("a")+"..."+sha("b")] = [][]string{{"a/main.tf"}}
	d := decision(t, h.push(sha("a"), sha("b"), 1), "a")

	exec, spec := h.spec(d.RunID)
	if spec.CommitSHA != sha("b") || spec.Ref != "main" || spec.Type != api.RunApply || spec.StackID != st.ID {
		t.Fatalf("spec = %+v", spec)
	}
	if _, err := exec.Complete(ctx, api.CompleteRequest{Success: true, HasChanges: true}); err != nil {
		t.Fatal(err)
	}
	var run api.Run
	h.call("GET", "/runs/"+d.RunID, nil, http.StatusOK, &run)
	if run.Status != api.StatusAwaitingApproval {
		t.Fatalf("status = %s", run.Status)
	}
	h.call("POST", "/runs/"+d.RunID+"/approve", api.ApproveRequest{}, http.StatusOK, nil)
	_, spec = h.spec(d.RunID)
	if spec.Phase != api.PhaseApply || spec.CommitSHA != sha("b") {
		t.Fatalf("apply spec = %+v", spec)
	}
}

// Without approval required the push goes straight to apply, as any other run.
func TestGitPushApplyWithoutApproval(t *testing.T) {
	h := newTriggerHarness(t)
	ctx := context.Background()
	mode, no := api.GitOnPushApply, false
	var st api.Stack
	h.call("POST", "/stacks", api.CreateStackRequest{GitRepository: "acme/infra", Name: "a", Path: "a"}, http.StatusCreated, &st)
	h.settings("/stacks/"+st.ID, api.LevelSettings{GitOnPush: &mode, RequireApproval: &no})
	h.gitea.compares[sha("a")+"..."+sha("b")] = [][]string{{"a/main.tf"}}
	d := decision(t, h.push(sha("a"), sha("b"), 1), "a")

	exec, _ := h.spec(d.RunID)
	if _, err := exec.Complete(ctx, api.CompleteRequest{Success: true, HasChanges: true}); err != nil {
		t.Fatal(err)
	}
	_, spec := h.spec(d.RunID)
	if spec.Phase != api.PhaseApply {
		t.Fatalf("spec = %+v", spec)
	}
}

// CAG1.10: a new push clears what's waiting from earlier pushes, and nothing else.
func TestGitPushSupersedes(t *testing.T) {
	h := newTriggerHarness(t)
	ctx := context.Background()
	st := h.gitStack("a", "a", api.GitOnPushApply)
	status := func(id string) api.Run {
		t.Helper()
		var r api.Run
		h.call("GET", "/runs/"+id, nil, http.StatusOK, &r)
		return r
	}
	push := func(before, after string) string {
		t.Helper()
		h.gitea.compares[sha(before)+"..."+sha(after)] = [][]string{{"a/main.tf"}}
		return decision(t, h.push(sha(before), sha(after), 1), "a").RunID
	}

	// Awaiting approval: discarded, so it doesn't hold the new one back.
	first := push("a", "b")
	exec, _ := h.spec(first)
	if _, err := exec.Complete(ctx, api.CompleteRequest{Success: true, HasChanges: true}); err != nil {
		t.Fatal(err)
	}
	second := push("b", "c")
	if r := status(first); r.Status != api.StatusDiscarded || !strings.Contains(r.Error, "superseded by "+sha("c")[:12]) {
		t.Fatalf("first = %+v", r)
	}

	// Queued: canceled.
	third := push("c", "d")
	if r := status(second); r.Status != api.StatusCanceled || !strings.Contains(r.Error, "superseded by") {
		t.Fatalf("second = %+v", r)
	}

	// Executing: finishes, and the new one waits.
	h.spec(third)
	fourth := push("d", "e")
	if r := status(third); r.Status != api.StatusPlanning {
		t.Fatalf("third = %+v", r)
	}
	if r := status(fourth); r.Status != api.StatusQueued {
		t.Fatalf("fourth = %+v", r)
	}
	if a := h.claim(); a != nil {
		t.Fatalf("the new run started beside the executing one: %+v", a)
	}

	// A run started by a person is never touched.
	other := h.gitStack("b", "b", api.GitOnPushPlan)
	manual := h.run(other.ID, api.RunPlan)
	h.gitea.compares[sha("e")+"..."+sha("f")] = [][]string{{"b/main.tf"}}
	h.push(sha("e"), sha("f"), 1)
	if r := status(manual.ID); r.Status != api.StatusQueued || r.Trigger == nil || r.Trigger.Kind != api.TriggerManual {
		t.Fatalf("manual = %+v", r)
	}
	if runs := h.runsOf(st.ID); len(runs) != 4 {
		t.Fatalf("runs = %+v", runs)
	}
}

// R5.3: one run per push, whichever way it arrives again. Going back to an old
// commit with a force push is a new push, not a repeat.
func TestGitPushOnePerPush(t *testing.T) {
	h := newTriggerHarness(t)
	st := h.gitStack("a", "a", api.GitOnPushPlan)
	h.gitea.compares[sha("a")+"..."+sha("b")] = [][]string{{"a/main.tf"}}
	d := decision(t, h.push(sha("a"), sha("b"), 1), "a")

	again := decision(t, h.push(sha("a"), sha("b"), 1), "a")
	if again.Outcome != api.OutcomeDuplicate || again.RunID != d.RunID {
		t.Fatalf("resent under another id: %+v", again)
	}
	if runs := h.runsOf(st.ID); len(runs) != 1 {
		t.Fatalf("runs = %+v", runs)
	}

	h.gitea.compares[sha("b")+"..."+sha("c")] = [][]string{{"a/main.tf"}}
	h.push(sha("b"), sha("c"), 1)
	h.gitea.compares[sha("a")+"..."+sha("c")] = nil
	h.gitea.compares[sha("b")+"..."+sha("c")] = [][]string{{"a/main.tf"}}
	// Back to b, from c: c isn't in b's history, so it reads as a force push.
	h.gitea.compares[sha("c")+"..."+sha("b")] = [][]string{{"a/main.tf"}}
	back := decision(t, h.push(sha("c"), sha("b"), 1), "a")
	if back.Outcome != api.OutcomeRunCreated || back.RunID == d.RunID {
		t.Fatalf("back to an old commit: %+v", back)
	}
}

// Events of a branch run in order, whatever the failures, and a failure is retried.
func TestGitEventsInOrder(t *testing.T) {
	h := newTriggerHarness(t)
	ctx := context.Background()
	st := h.gitStack("a", "a", api.GitOnPushPlan)
	h.gitea.compares[sha("a")+"..."+sha("b")] = [][]string{{"a/main.tf"}}
	h.gitea.compares[sha("b")+"..."+sha("c")] = [][]string{{"a/main.tf"}}

	first := pushBody("refs/heads/main", sha("a"), sha("b"), 1)
	second := pushBody("refs/heads/main", sha("b"), sha("c"), 1)
	h.deliver("push", "first", sign(h.secret, first), first)
	h.deliver("push", "second", sign(h.secret, second), second)

	claimed, err := h.store.ClaimGitEvent(ctx)
	if err != nil || claimed.AfterSHA != sha("b") {
		t.Fatalf("claim = %+v, %v", claimed, err)
	}
	if next, err := h.store.ClaimGitEvent(ctx); err == nil {
		t.Fatalf("claimed %+v while an older event of the branch is being processed", next)
	}
	if err := h.store.RetryGitEvent(ctx, claimed.ID, fmt.Errorf("boom")); err != nil {
		t.Fatal(err)
	}
	if _, err := h.pool.Exec(ctx, `UPDATE git_events SET not_before = now() WHERE id = $1`, claimed.ID); err != nil {
		t.Fatal(err)
	}
	h.srv.drainGitEvents(ctx)

	runs := h.runsOf(st.ID)
	if len(runs) != 2 || runs[0].CommitSHA != sha("c") || runs[0].Status != api.StatusQueued || runs[1].Status != api.StatusCanceled {
		t.Fatalf("runs = %+v", runs)
	}
	for _, ev := range h.events("") {
		if ev.Status != "done" || ev.Error != "" {
			t.Errorf("event = %+v", ev)
		}
	}
}

// Moving a stack shows when the trigger it inherits changes.
func TestMovePreviewShowsGitTrigger(t *testing.T) {
	h := newTriggerHarness(t)
	plan := api.GitOnPushPlan
	folder := h.folder("", "prod")
	h.settings("/folders/"+folder.ID, api.LevelSettings{GitOnPush: &plan})
	st := h.gitStack("a", "a", "")
	var out api.MovePreview
	h.call("GET", "/stacks/"+st.ID+"/move?folder_id="+folder.ID, nil, http.StatusOK, &out)
	for _, c := range out.Changes {
		if c.Setting == "git_on_push" && c.After.Value == plan && c.Before.Value == api.GitOnPushOff {
			return
		}
	}
	t.Fatalf("changes = %+v", out.Changes)
}

// Runs not started by Git say so.
func TestRunTriggerKinds(t *testing.T) {
	h := newTriggerHarness(t)
	st := h.gitStack("a", "a", "")
	if r := h.run(st.ID, api.RunPlan); r.Trigger == nil || r.Trigger.Kind != api.TriggerManual {
		t.Errorf("manual: %+v", r.Trigger)
	}
	var drift api.Run
	h.call("POST", "/stacks/"+st.ID+"/runs", api.CreateRunRequest{Type: api.RunDrift}, http.StatusCreated, &drift)
	if drift.Trigger == nil || drift.Trigger.Kind != api.TriggerDrift {
		t.Errorf("drift: %+v", drift.Trigger)
	}
}
