package server

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bsteigleder/firstlume/internal/api"
)

// fakeGitLab serves the subset of the GitLab API Firstlume uses, under
// /gitlab like a self-managed GitLab served under a path, with the group
// acme/platform and its subgroup acme/platform/net (spec 0010).
type fakeGitLab struct {
	mu       sync.Mutex
	url      string
	token    string
	readOnly string
	// compares maps "from...to" to the files changed; unset is none.
	compares map[string][]string
	// mr is the merge request of any commit, when set.
	mr int
}

func (f *fakeGitLab) project(path string) map[string]any {
	ns, name := path[:strings.LastIndex(path, "/")], path[strings.LastIndex(path, "/")+1:]
	return map[string]any{
		"id": len(path), "path": name, "path_with_namespace": path, "namespace": map[string]string{"full_path": ns},
		"default_branch": "main", "visibility": "private", "empty_repo": false,
		"http_url_to_repo": f.url + "/gitlab/" + path + ".git", "web_url": f.url + "/gitlab/" + path,
	}
}

func (f *fakeGitLab) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.Header.Get("PRIVATE-TOKEN") {
	case f.token:
	case f.readOnly:
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"insufficient_scope"}`))
		return
	default:
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	p := strings.TrimPrefix(r.URL.EscapedPath(), "/gitlab/api/v4")
	q := r.URL.Query()
	const infra = "/projects/acme%2Fplatform%2Finfra"
	switch {
	case p == "/namespaces/acme%2Fplatform":
		writeJSON(w, http.StatusOK, map[string]string{"kind": "group", "full_path": "acme/platform"})
	case p == "/groups/acme%2Fplatform/projects":
		writeJSON(w, http.StatusOK, []map[string]any{f.project("acme/platform/infra"), f.project("acme/platform/net/dns")})
	case p == infra:
		writeJSON(w, http.StatusOK, f.project("acme/platform/infra"))
	case p == "/projects/acme%2Foutside":
		writeJSON(w, http.StatusOK, f.project("acme/outside"))
	case p == infra+"/repository/branches":
		writeJSON(w, http.StatusOK, []map[string]string{{"name": "main"}, {"name": "feature/vpn"}})
	case p == infra+"/repository/branches/main":
		writeJSON(w, http.StatusOK, map[string]any{"name": "main", "commit": map[string]string{"id": "c0ffee"}})
	case p == infra+"/repository/tree":
		writeJSON(w, http.StatusOK, []map[string]string{
			{"path": "net", "type": "tree"}, {"path": "net/.firstlume", "type": "tree"}, {"path": "net/.firstlume/config.yaml", "type": "blob"},
			{"path": "app", "type": "tree"}, {"path": "app/.firstlume", "type": "tree"}, {"path": "app/.firstlume/config.yaml", "type": "blob"},
		})
	case strings.HasPrefix(p, infra+"/repository/files/") && strings.HasSuffix(p, "%2F.firstlume%2Fconfig.yaml/raw"):
		_, _ = w.Write([]byte(triggerConfig))
	case p == infra+"/repository/compare":
		files := f.compares[q.Get("from")+"..."+q.Get("to")]
		commits := []map[string]string{}
		if files != nil {
			commits = append(commits, map[string]string{"id": "x"})
		}
		diffs := []map[string]string{}
		for _, n := range files {
			diffs = append(diffs, map[string]string{"old_path": n, "new_path": n})
		}
		writeJSON(w, http.StatusOK, map[string]any{"commits": commits, "diffs": diffs, "compare_timeout": false})
	case strings.HasPrefix(p, infra+"/repository/commits/") && strings.HasSuffix(p, "/merge_requests"):
		if f.mr == 0 {
			writeJSON(w, http.StatusOK, []any{})
			return
		}
		writeJSON(w, http.StatusOK, []map[string]any{{"iid": f.mr, "state": "merged", "web_url": fmt.Sprintf("%s/gitlab/acme/platform/infra/-/merge_requests/%d", f.url, f.mr)}})
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"404 Not Found"}`))
	}
}

type gitlabHarness struct {
	*harness
	gitlab  *fakeGitLab
	hookURL string
	secret  string
}

func newGitLabHarness(t *testing.T) *gitlabHarness {
	t.Helper()
	h := newHarness(t)
	g := &fakeGitLab{token: "glpat-s3rvice", readOnly: "glpat-repo-only", compares: map[string][]string{}}
	gs := httptest.NewServer(g)
	t.Cleanup(gs.Close)
	g.url = gs.URL
	var conn api.GitConnectionResponse
	h.call("PUT", "/git/connection", api.PutGitConnectionRequest{Provider: "gitlab", URL: gs.URL + "/gitlab/", Account: "acme/platform", Token: g.token}, http.StatusOK, &conn)
	if conn.Provider != "gitlab" || conn.Account != "acme/platform" || conn.AccountKind != "group" || conn.Repositories != 2 || conn.URL != gs.URL+"/gitlab" {
		t.Fatalf("connection = %+v", conn)
	}
	return &gitlabHarness{harness: h, gitlab: g}
}

func (h *gitlabHarness) hook() {
	h.t.Helper()
	var hook api.WebhookSecret
	h.call("POST", "/git/webhook-secret", nil, http.StatusOK, &hook)
	if hook.Provider != "gitlab" || !strings.Contains(hook.URL, "/webhooks/v1/gitlab/") || len(hook.Events) != 1 || hook.Events[0] != "Push events" {
		h.t.Fatalf("webhook = %+v", hook)
	}
	// GitLab takes a signing token only in this exact form (L13).
	if _, ok := signingKey(hook.Secret); !ok || len(hook.Secret) != len("whsec_")+44 || !strings.HasSuffix(hook.Secret, "=") {
		h.t.Fatalf("secret %q isn't a GitLab signing token", hook.Secret)
	}
	_, id, _ := strings.Cut(hook.URL, "/webhooks/v1/gitlab/")
	h.hookURL, h.secret = h.url+"/webhooks/v1/gitlab/"+id, hook.Secret
}

func (h *gitlabHarness) runsOf(stackID string) []api.Run {
	h.t.Helper()
	var runs []api.Run
	h.call("GET", "/stacks/"+stackID+"/runs", nil, http.StatusOK, &runs)
	return runs
}

func gitlabPushBody(project, ref, before, after string, total int) []byte {
	body, _ := json.Marshal(map[string]any{
		"object_kind": "push", "event_name": "push", "ref": ref, "before": before, "after": after,
		"checkout_sha": after, "total_commits_count": total, "user_username": "ana", "commits": []any{},
		"project": map[string]any{"path_with_namespace": project},
	})
	return body
}

// gitlabSign signs like GitLab 19 with a signing token (L13).
func gitlabSign(secret, id string, ts int64, body []byte) string {
	key, _ := signingKey(secret)
	mac := hmac.New(sha256.New, key)
	fmt.Fprintf(mac, "%s.%d.", id, ts)
	mac.Write(body)
	return "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// deliver posts to url with the given headers and returns the status.
func (h *gitlabHarness) deliver(url string, headers map[string]string, body []byte) int {
	h.t.Helper()
	req, _ := http.NewRequest("POST", url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// signed returns GitLab's headers for a signed push delivery.
func (h *gitlabHarness) signed(key string, body []byte) map[string]string {
	ts := time.Now().Unix()
	return map[string]string{
		"X-Gitlab-Event": "Push Hook", "Idempotency-Key": key, "Webhook-Id": key,
		"Webhook-Timestamp": strconv.FormatInt(ts, 10), "Webhook-Signature": gitlabSign(h.secret, key, ts, body),
		"X-Gitlab-Event-Uuid": randomSuffix(),
	}
}

func (h *gitlabHarness) push(before, after string, total int) api.GitEvent {
	h.t.Helper()
	deliveries++
	body := gitlabPushBody("acme/platform/infra", "refs/heads/main", before, after, total)
	if got := h.deliver(h.hookURL, h.signed(fmt.Sprintf("k-%s-%d", randomSuffix(), deliveries), body), body); got != http.StatusAccepted {
		h.t.Fatalf("delivery status = %d", got)
	}
	h.srv.drainGitEvents(context.Background())
	var ev []api.GitEvent
	h.call("GET", "/git/events", nil, http.StatusOK, &ev)
	return ev[0]
}

func (h *gitlabHarness) stack(name, path, mode string) api.Stack {
	h.t.Helper()
	var st api.Stack
	h.call("POST", "/stacks", api.CreateStackRequest{GitRepository: "acme/platform/infra", Name: name, Path: path}, http.StatusCreated, &st)
	if mode != "" {
		h.settings("/stacks/"+st.ID, api.LevelSettings{GitOnPush: &mode})
	}
	return st
}

// CAL2.1 to CAL2.3, CAL2.6, CAL2.7: connecting to a group with subgroups, under
// a path, and creating stacks from it.
func TestGitLabConnection(t *testing.T) {
	h := newGitLabHarness(t)
	g := h.gitlab
	base := g.url + "/gitlab"

	for _, c := range []struct {
		account, token, want string
	}{
		{"platform", g.token, "use the full path"},
		{"acme/platform", g.readOnly, "read_api"},
		{"acme/platform", "nope", "GitLab rejected the token"},
		{"acme//platform", g.token, "full path"},
	} {
		body := h.call("PUT", "/git/connection", api.PutGitConnectionRequest{Provider: "gitlab", URL: base, Account: c.account, Token: c.token}, http.StatusUnprocessableEntity, nil)
		if !strings.Contains(string(body), c.want) {
			t.Errorf("%s with %s: %s", c.account, c.token, body)
		}
	}

	var repos []api.GitRepo
	h.call("GET", "/git/repos", nil, http.StatusOK, &repos)
	if len(repos) != 2 || repos[1].FullName != "acme/platform/net/dns" || repos[1].Owner != "acme/platform/net" || repos[1].Name != "dns" {
		t.Fatalf("repos = %+v", repos)
	}
	var branches []string
	h.call("GET", "/git/repo/branches?repo=acme/platform/infra", nil, http.StatusOK, &branches)
	if len(branches) != 2 || branches[1] != "feature/vpn" {
		t.Fatalf("branches = %v", branches)
	}
	h.call("GET", "/git/repo/branches?repo=acme/outside", nil, http.StatusNotFound, nil)
	h.call("GET", "/git/repo/branches?repo=acme/platform/../outside", nil, http.StatusNotFound, nil)

	var found api.DiscoverResponse
	h.call("GET", "/git/repo/discover?repo=acme/platform/infra", nil, http.StatusOK, &found)
	if found.Commit != "c0ffee" || found.Ref != "main" || len(found.Stacks) != 2 || found.Stacks[0].Path != "app" || found.Stacks[0].Config == nil {
		t.Fatalf("discover = %+v", found)
	}

	st := h.stack("net", "net", "")
	if st.Repository != base+"/acme/platform/infra.git" || st.Ref != "main" || st.ConnectionID == "" {
		t.Fatalf("stack = %+v", st)
	}
	h.call("POST", "/stacks", api.CreateStackRequest{GitRepository: "acme/outside", Name: "out", Path: "."}, http.StatusUnprocessableEntity, nil)
	h.call("GET", "/git/repo/discover?repo=acme/platform/infra", nil, http.StatusOK, &found)
	if found.Stacks[1].UsedBy == nil || found.Stacks[1].UsedBy.ID != st.ID {
		t.Fatalf("discover after creating = %+v", found.Stacks)
	}

	// The provider can't change while stacks clone through the connection,
	// and a stored token never goes to another provider or address.
	h.call("PUT", "/git/connection", api.PutGitConnectionRequest{Provider: "gitea", URL: base, Account: "acme", Token: g.token}, http.StatusUnprocessableEntity, nil)
	h.call("PUT", "/git/connection", api.PutGitConnectionRequest{Provider: "gitlab", URL: "https://elsewhere.example", Account: "acme/platform"}, http.StatusUnprocessableEntity, nil)
	h.call("PUT", "/git/connection", api.PutGitConnectionRequest{Provider: "gitlab", URL: base, Account: "acme/platform"}, http.StatusOK, nil)

	gitea := &fakeGitea{token: "gitea-token"}
	gs := httptest.NewServer(gitea)
	defer gs.Close()
	gitea.url = gs.URL
	body := h.call("PUT", "/git/connection", api.PutGitConnectionRequest{Provider: "gitea", URL: gs.URL, Account: "acme", Token: gitea.token}, http.StatusConflict, nil)
	if !strings.Contains(string(body), "delete them before connecting Gitea") {
		t.Fatalf("switching provider: %s", body)
	}
}

// CAL3.1, CAL3.5: who may deliver, and what is kept.
func TestGitLabWebhookAuth(t *testing.T) {
	h := newGitLabHarness(t)
	h.hook()
	body := gitlabPushBody("acme/platform/infra", "refs/heads/main", sha("a"), sha("b"), 1)
	good := h.signed("one", body)

	old := h.signed("one", body)
	ts := time.Now().Add(-6 * time.Minute).Unix()
	old["Webhook-Timestamp"], old["Webhook-Signature"] = strconv.FormatInt(ts, 10), gitlabSign(h.secret, "one", ts, body)
	tampered := h.signed("one", body)
	tampered["Webhook-Id"] = "other"
	for name, headers := range map[string]map[string]string{
		"nothing":          {"X-Gitlab-Event": "Push Hook"},
		"wrong token":      {"X-Gitlab-Event": "Push Hook", "X-Gitlab-Token": "whsec_nope"},
		"wrong signature":  {"X-Gitlab-Event": "Push Hook", "Webhook-Id": "one", "Webhook-Timestamp": good["Webhook-Timestamp"], "Webhook-Signature": "v1,AAAA"},
		"old signature":    old,
		"other message id": tampered,
		// A valid signature never excuses a wrong token, nor the other way round.
		"signature wins": {"X-Gitlab-Event": "Push Hook", "X-Gitlab-Token": h.secret, "Webhook-Id": "one", "Webhook-Timestamp": good["Webhook-Timestamp"], "Webhook-Signature": "v1,AAAA"},
	} {
		if got := h.deliver(h.hookURL, headers, body); got != http.StatusUnauthorized {
			t.Errorf("%s: status %d", name, got)
		}
	}
	// The Gitea route never takes a GitLab connection's deliveries.
	if got := h.deliver(strings.Replace(h.hookURL, "/gitlab/", "/gitea/", 1), good, body); got != http.StatusUnauthorized {
		t.Errorf("gitea route: status %d", got)
	}
	var ev []api.GitEvent
	h.call("GET", "/git/events", nil, http.StatusOK, &ev)
	if len(ev) != 0 {
		t.Fatalf("rejected deliveries left events: %+v", ev)
	}

	if got := h.deliver(h.hookURL, good, body); got != http.StatusAccepted {
		t.Fatalf("signed: status %d", got)
	}
	// GitLab delivering again keeps the Idempotency-Key and changes the event UUID (L12).
	again := h.signed("one", body)
	if got := h.deliver(h.hookURL, again, body); got != http.StatusAccepted {
		t.Fatalf("again: status %d", got)
	}
	// A GitLab without signing tokens sends the secret as the secret token.
	byToken := map[string]string{"X-Gitlab-Event": "Push Hook", "X-Gitlab-Token": h.secret, "Idempotency-Key": "two"}
	if got := h.deliver(h.hookURL, byToken, gitlabPushBody("acme/platform/infra", "refs/heads/main", sha("b"), sha("c"), 1)); got != http.StatusAccepted {
		t.Fatalf("token: status %d", got)
	}
	// Tags, other events and projects outside the account are dropped.
	tag := gitlabPushBody("acme/platform/infra", "refs/tags/v1", sha("0"), sha("c"), 1)
	tagHeaders := h.signed("three", tag)
	tagHeaders["X-Gitlab-Event"] = "Tag Push Hook"
	outside := gitlabPushBody("acme/outside", "refs/heads/main", sha("c"), sha("d"), 1)
	deleted := gitlabPushBody("acme/platform/infra", "refs/heads/old", sha("c"), sha("0"), 0)
	for name, d := range map[string]struct {
		headers map[string]string
		body    []byte
	}{"tag": {tagHeaders, tag}, "outside": {h.signed("four", outside), outside}, "deleted branch": {h.signed("five", deleted), deleted}} {
		if got := h.deliver(h.hookURL, d.headers, d.body); got != http.StatusAccepted {
			t.Errorf("%s: status %d", name, got)
		}
	}
	h.call("GET", "/git/events", nil, http.StatusOK, &ev)
	if len(ev) != 2 || ev[1].Repository != "acme/platform/infra" || ev[1].Sender != "ana" || ev[1].Ref != "main" {
		t.Fatalf("events = %+v", ev)
	}
}

// CAL3.2 to CAL3.4, CAL3.6: pushes start runs as with Gitea; the test button
// and a cut comparison.
func TestGitLabPushRuns(t *testing.T) {
	h := newGitLabHarness(t)
	h.hook()
	net := h.stack("net", "net", api.GitOnPushPlan)
	app := h.stack("app", "app", api.GitOnPushPlan)
	g := h.gitlab

	g.compares[sha("a")+"..."+sha("b")] = []string{"net/main.tf"}
	g.mr = 42
	ev := h.push(sha("a"), sha("b"), 1)
	d := decision(t, ev, "net")
	if d.Outcome != api.OutcomeRunCreated || decision(t, ev, "app").Outcome != api.OutcomeNotAffected {
		t.Fatalf("push = %+v", ev.Stacks)
	}
	var run api.Run
	h.call("GET", "/runs/"+d.RunID, nil, http.StatusOK, &run)
	if run.Trigger == nil || run.Trigger.PullRequest != 42 || !strings.HasSuffix(run.Trigger.PullRequestURL, "/-/merge_requests/42") || run.CommitSHA != sha("b") {
		t.Fatalf("run = %+v", run.Trigger)
	}

	// GitLab's Test button repeats the last push from an older commit (L14).
	g.compares[sha("9")+"..."+sha("b")] = []string{"net/main.tf", "app/main.tf"}
	ev = h.push(sha("9"), sha("b"), 3)
	for _, name := range []string{"net", "app"} {
		if d := decision(t, ev, name); d.Outcome != api.OutcomeAlreadyHandled {
			t.Fatalf("test delivery, %s: %+v", name, d)
		}
	}
	if runs := h.runsOf(net.ID); len(runs) != 1 {
		t.Fatalf("runs = %+v", runs)
	}

	// 1000 files is where GitLab cuts the list (L8).
	many := make([]string, 1000)
	for i := range many {
		many[i] = fmt.Sprintf("docs/f%04d.md", i)
	}
	g.compares[sha("b")+"..."+sha("c")] = many
	ev = h.push(sha("b"), sha("c"), 1)
	if ev.AffectsAll != api.AffectedCompareIncomplete || decision(t, ev, "app").Outcome != api.OutcomeRunCreated {
		t.Fatalf("cut comparison = %+v", ev)
	}

	// Force push: the old head has a commit the new one hasn't.
	g.compares[sha("d")+"..."+sha("c")] = []string{"x"}
	ev = h.push(sha("c"), sha("d"), 1)
	if ev.AffectsAll != api.AffectedForcePush {
		t.Fatalf("force push = %+v", ev)
	}
	if runs := h.runsOf(app.ID); len(runs) != 2 {
		t.Fatalf("app runs = %d", len(runs))
	}
}

// R5.9 for Gitea too: its test delivery (before == after) runs nothing new.
func TestGiteaTestDeliveryAlreadyHandled(t *testing.T) {
	h := newTriggerHarness(t)
	st := h.gitStack("a", "a", api.GitOnPushPlan)
	h.gitea.compares[sha("a")+"..."+sha("b")] = [][]string{{"a/main.tf"}}
	h.push(sha("a"), sha("b"), 1)
	ev := h.push(sha("b"), sha("b"), 0)
	if d := decision(t, ev, "a"); d.Outcome != api.OutcomeAlreadyHandled {
		t.Fatalf("test delivery = %+v", d)
	}
	if runs := h.runsOf(st.ID); len(runs) != 1 {
		t.Fatalf("runs = %+v", runs)
	}
}

// CAL1.3: the routes by ?repo= answer like the ones by {owner}/{repo} for Gitea.
func TestGiteaRepoQueryRoutes(t *testing.T) {
	h := newTriggerHarness(t)
	var byPath, byQuery []string
	h.call("GET", "/git/repos/acme/infra/branches", nil, http.StatusOK, &byPath)
	h.call("GET", "/git/repo/branches?repo=acme/infra", nil, http.StatusOK, &byQuery)
	if strings.Join(byPath, ",") != strings.Join(byQuery, ",") || len(byQuery) == 0 {
		t.Fatalf("branches: %v vs %v", byPath, byQuery)
	}
	var a, b api.DiscoverResponse
	h.call("GET", "/git/repos/acme/infra/discover?ref=main", nil, http.StatusOK, &a)
	h.call("GET", "/git/repo/discover?repo=acme/infra&ref=main", nil, http.StatusOK, &b)
	if a.Commit != b.Commit || len(a.Stacks) != len(b.Stacks) || len(b.Stacks) == 0 {
		t.Fatalf("discover: %+v vs %+v", a, b)
	}
	h.call("GET", "/git/repo/branches?repo=acme/a/b", nil, http.StatusNotFound, nil)
	h.call("GET", "/git/repo/branches?repo=other/infra", nil, http.StatusNotFound, nil)
}
