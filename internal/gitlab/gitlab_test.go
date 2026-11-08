package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// fake serves the subset of the GitLab API the client uses, the way GitLab
// 19.5 answered it (spec 0010, section 1.1). It sits under /gitlab, like a
// self-managed GitLab served under a path.
type fake struct {
	token     string
	readOnly  string // a token with read_repository only
	projects  int
	tree      []TreeEntry
	files     map[string]string
	sawEscape bool
}

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Header.Get("PRIVATE-TOKEN") {
	case f.token:
	case f.readOnly:
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"insufficient_scope","scope":"api read_api"}`))
		return
	default:
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"401 Unauthorized"}`))
		return
	}
	// GitLab wants paths escaped as one segment.
	raw := strings.TrimPrefix(r.URL.EscapedPath(), "/gitlab/api/v4")
	if strings.Contains(raw, "acme%2Fplatform") {
		f.sawEscape = true
	}
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	if page == 0 {
		page = 1
	}
	enc := json.NewEncoder(w)
	switch {
	case raw == "/namespaces/acme%2Fplatform":
		_ = enc.Encode(map[string]any{"kind": "group", "full_path": "acme/platform"})
	case raw == "/namespaces/ana":
		_ = enc.Encode(map[string]any{"kind": "user", "full_path": "ana"})
	case raw == "/groups/acme%2Fplatform/projects":
		if q.Get("include_subgroups") != "true" || q.Get("with_shared") != "false" || q.Get("archived") != "false" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		per, _ := strconv.Atoi(q.Get("per_page"))
		var out []map[string]any
		for i := (page-1)*per + 1; i <= f.projects && i <= page*per; i++ {
			out = append(out, map[string]any{
				"path": fmt.Sprintf("p%03d", i), "path_with_namespace": fmt.Sprintf("acme/platform/net/p%03d", i),
				"namespace": map[string]string{"full_path": "acme/platform/net"}, "default_branch": "main", "visibility": "private",
			})
		}
		if page*per < f.projects {
			w.Header().Set("X-Next-Page", strconv.Itoa(page+1))
		} else {
			w.Header().Set("X-Next-Page", "")
		}
		_ = enc.Encode(out)
	case raw == "/projects/acme%2Fplatform%2Finfra/repository/tree":
		if q.Get("pagination") != "keyset" || q.Get("ref") != "abc" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		per, _ := strconv.Atoi(q.Get("per_page"))
		lo, _ := strconv.Atoi(q.Get("page_token"))
		hi := min(lo+per, len(f.tree))
		if hi < len(f.tree) {
			next := *r.URL
			next.Scheme, next.Host = "http", r.Host
			nq := next.Query()
			nq.Set("page_token", strconv.Itoa(hi))
			next.RawQuery = nq.Encode()
			w.Header().Set("Link", `<`+next.String()+`>; rel="next"`)
		}
		_ = enc.Encode(f.tree[lo:hi])
	case strings.HasPrefix(raw, "/projects/acme%2Fplatform%2Finfra/repository/files/") && strings.HasSuffix(raw, "/raw"):
		name := strings.TrimSuffix(strings.TrimPrefix(raw, "/projects/acme%2Fplatform%2Finfra/repository/files/"), "/raw")
		content, ok := f.files[strings.ReplaceAll(name, "%2F", "/")]
		if !ok || q.Get("ref") != "abc" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"404 File Not Found"}`))
			return
		}
		_, _ = w.Write([]byte(content))
	case raw == "/projects/acme%2Fplatform%2Finfra/repository/branches/feature%2Fvpn":
		_ = enc.Encode(map[string]any{"name": "feature/vpn", "commit": map[string]string{"id": "def"}})
	case raw == "/projects/acme%2Fplatform%2Finfra/repository/compare":
		n, _ := strconv.Atoi(q.Get("to"))
		diffs := make([]map[string]string, n)
		for i := range diffs {
			diffs[i] = map[string]string{"old_path": fmt.Sprintf("f%d", i), "new_path": fmt.Sprintf("f%d", i)}
		}
		_ = enc.Encode(map[string]any{"commits": []map[string]string{{"id": "x"}}, "diffs": diffs, "compare_timeout": false})
	case raw == "/projects/acme%2Fplatform%2Finfra/repository/commits/m1/merge_requests":
		_ = enc.Encode([]map[string]any{{"iid": 3, "state": "closed", "web_url": "u3"}, {"iid": 7, "state": "merged", "web_url": "u7"}})
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"404 Not Found"}`))
	}
}

func TestAccountsAndProjects(t *testing.T) {
	f := &fake{token: "t0k", readOnly: "repo-only", projects: 234}
	srv := httptest.NewServer(f)
	defer srv.Close()
	ctx := context.Background()
	c := New(srv.URL+"/gitlab/", "t0k")

	if k, p, err := c.CheckAccount(ctx, "acme/platform"); err != nil || k != Group || p != "acme/platform" {
		t.Fatalf("group: %v %v %v", k, p, err)
	}
	if k, _, err := c.CheckAccount(ctx, "ana"); err != nil || k != User {
		t.Fatalf("user: %v %v", k, err)
	}
	if _, _, err := c.CheckAccount(ctx, "platform"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("short subgroup name: %v", err)
	}
	if _, _, err := New(srv.URL+"/gitlab", "wrong").CheckAccount(ctx, "ana"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("bad token: %v", err)
	}
	if _, _, err := New(srv.URL+"/gitlab", "repo-only").CheckAccount(ctx, "ana"); !errors.Is(err, ErrMissingScope) {
		t.Fatalf("read_repository only: %v", err)
	}
	projects, err := c.ListProjects(ctx, "acme/platform", Group)
	if err != nil || len(projects) != 234 || projects[233].PathWithNamespace != "acme/platform/net/p234" {
		t.Fatalf("pagination: %d projects, %v", len(projects), err)
	}
	if !f.sawEscape {
		t.Fatal("the group path was not escaped as one segment")
	}
}

func TestTreeFilesAndCompare(t *testing.T) {
	f := &fake{token: "t0k", files: map[string]string{"environments/net/.firstlume/config.yaml": "version: 1\n"}}
	for i := 0; i < 2500; i++ {
		f.tree = append(f.tree, TreeEntry{Path: fmt.Sprintf("m/f%04d", i), Type: "blob"})
	}
	srv := httptest.NewServer(f)
	defer srv.Close()
	ctx := context.Background()
	c := New(srv.URL+"/gitlab", "t0k")

	tree, err := c.Tree(ctx, "acme/platform/infra", "abc")
	if err != nil || len(tree) != 2500 || tree[2499].Path != "m/f2499" {
		t.Fatalf("keyset pagination: %d entries, %v", len(tree), err)
	}
	data, err := c.File(ctx, "acme/platform/infra", "abc", "environments/net/.firstlume/config.yaml")
	if err != nil || string(data) != "version: 1\n" {
		t.Fatalf("file: %q %v", data, err)
	}
	if _, err := c.File(ctx, "acme/platform/infra", "abc", "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing file: %v", err)
	}
	if sha, err := c.ResolveBranch(ctx, "acme/platform/infra", "feature/vpn"); err != nil || sha != "def" {
		t.Fatalf("branch with a slash: %q %v", sha, err)
	}
	cmp, err := c.Compare(ctx, "acme/platform/infra", "a", "1000")
	if err != nil || len(cmp.Diffs) != 1000 {
		t.Fatalf("compare: %v %v", cmp, err)
	}
	if n, u, err := c.CommitMergeRequest(ctx, "acme/platform/infra", "m1"); err != nil || n != 7 || u != "u7" {
		t.Fatalf("merge request: %d %q %v", n, u, err)
	}
}

func TestTreeDoesNotFollowLinksElsewhere(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("the token went to another host: %s", r.URL)
	}))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Link", `<`+other.URL+`/steal>; rel="next"`)
		_, _ = w.Write([]byte(`[{"path":"a","type":"blob"}]`))
	}))
	defer srv.Close()
	tree, err := New(srv.URL, "t0k").Tree(context.Background(), "acme/infra", "abc")
	if err != nil || len(tree) != 1 {
		t.Fatalf("%v %v", tree, err)
	}
}

func TestNextLink(t *testing.T) {
	h := `<https://g/api/v4/x?page_token=a>; rel="first", <https://g/api/v4/x?page_token=b>; rel="next"`
	if got := nextLink(h); got != "https://g/api/v4/x?page_token=b" {
		t.Fatalf("got %q", got)
	}
	if nextLink(`<https://g/x>; rel="first"`) != "" {
		t.Fatal("no next link")
	}
}
