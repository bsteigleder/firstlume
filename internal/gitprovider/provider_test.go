package gitprovider

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

// memory is a Provider holding one repository in memory.
type memory struct {
	Provider
	tree  []TreeEntry
	files map[string]string
}

func (m memory) Tree(ctx context.Context, fullName, sha string) ([]TreeEntry, error) {
	if fullName != "acme/platform/infra" || sha != "abc" {
		return nil, ErrNotFound
	}
	return m.tree, nil
}

func (m memory) File(ctx context.Context, fullName, sha, path string) ([]byte, error) {
	c, ok := m.files[path]
	if !ok {
		return nil, ErrNotFound
	}
	return []byte(c), nil
}

func TestDiscover(t *testing.T) {
	m := memory{files: map[string]string{
		"environments/prod/.firstlume/config.yaml": "version: 1\nengine: terraform\nengine_version: 1.15.6\n",
		".firstlume/config.yaml":                   "version: 1\nengine: opentofu\nengine_version: 1.10.2\n",
		"legacy/vpn/.firstlume/config.yaml":        "version: 1\nengine: terraform\nengine_version: latest\n",
	}}
	for i := 0; i < 20000; i++ {
		m.tree = append(m.tree, TreeEntry{Path: fmt.Sprintf("modules/m%05d/main.tf", i), Type: "blob"})
	}
	for _, e := range []TreeEntry{
		{".firstlume", "tree"}, {".firstlume/config.yaml", "blob"},
		{"environments/prod/.firstlume", "tree"}, {"environments/prod/.firstlume/config.yaml", "blob"},
		{"legacy/vpn/.firstlume", "tree"}, {"legacy/vpn/.firstlume/config.yaml", "blob"},
		{"deep/a/b/c/d/e/.firstlume", "tree"}, {"deep/a/b/c/d/e/.firstlume/README.md", "blob"},
	} {
		m.tree = append(m.tree, e)
	}

	found, err := Discover(context.Background(), m, "acme/platform/infra", "abc")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Found{}
	for _, x := range found {
		got[x.Path] = x
	}
	if len(found) != 4 {
		t.Fatalf("found %+v", found)
	}
	if c := got["."].Config; c == nil || c.Engine != "opentofu" {
		t.Fatalf("root stack: %+v", got["."])
	}
	if c := got["environments/prod"].Config; c == nil || c.EngineVersion != "1.15.6" {
		t.Fatalf("prod: %+v", got["environments/prod"])
	}
	if e := got["legacy/vpn"].Error; !strings.Contains(e, "legacy/vpn/.firstlume/config.yaml: engine_version") {
		t.Fatalf("invalid config: %q", e)
	}
	if e := got["deep/a/b/c/d/e"].Error; e != "deep/a/b/c/d/e/.firstlume/config.yaml not found" {
		t.Fatalf("missing config: %q", e)
	}
}

func TestAccountsAndPaths(t *testing.T) {
	for _, c := range []struct {
		kind, account string
		ok            bool
	}{
		{Gitea, "infra", true}, {Gitea, "acme/infra", false}, {Gitea, "-x", false},
		{GitLab, "acme", true}, {GitLab, "acme/platform/net", true}, {GitLab, "acme//x", false},
		{GitLab, "acme/../x", false}, {GitLab, "acme/.hidden", false}, {GitLab, "acme/x.git", false},
		{GitLab, strings.Repeat("a/", 20) + "a", false}, {GitLab, "", false},
	} {
		if got := ValidAccount(c.kind, c.account); got != c.ok {
			t.Errorf("ValidAccount(%s, %q) = %v", c.kind, c.account, got)
		}
	}
	for _, c := range []struct {
		kind, account, repo string
		ok                  bool
	}{
		{Gitea, "acme", "acme/infra", true}, {Gitea, "acme", "ACME/infra", true}, {Gitea, "acme", "acme/a/b", false},
		{Gitea, "acme", "other/infra", false},
		{GitLab, "acme/platform", "acme/platform/infra", true}, {GitLab, "acme/platform", "acme/platform/net/dns", true},
		{GitLab, "acme/platform", "Acme/Platform/infra", true},
		{GitLab, "acme/platform", "acme/platformx/infra", false}, {GitLab, "acme/platform", "acme/platform", false},
		{GitLab, "acme/platform", "acme/outside", false}, {GitLab, "acme/platform", "acme/platform/../x", false},
		{GitLab, "acme/platform", "acme/platform/", false},
	} {
		if got := InAccount(c.kind, c.account, c.repo); got != c.ok {
			t.Errorf("InAccount(%s, %q, %q) = %v", c.kind, c.account, c.repo, got)
		}
	}
	if o, n := SplitFullName("acme/platform/infra"); o != "acme/platform" || n != "infra" {
		t.Fatalf("split: %q %q", o, n)
	}
}

// TestGitLabCompare: GitLab stops at 1000 files without saying so, so 1000
// counts as incomplete; renames count both paths (spec 0010, R5.6).
func TestGitLabCompare(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(r.URL.Query().Get("to"))
		diffs := []map[string]string{{"old_path": "old/name.tf", "new_path": "new/name.tf"}}
		for i := 1; i < n; i++ {
			diffs = append(diffs, map[string]string{"old_path": fmt.Sprintf("f%d", i), "new_path": fmt.Sprintf("f%d", i)})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"commits": []map[string]string{{"id": "x"}, {"id": "y"}}, "diffs": diffs})
	}))
	defer srv.Close()
	p, err := New(GitLab, srv.URL, "t")
	if err != nil {
		t.Fatal(err)
	}
	cmp, err := p.Compare(context.Background(), "acme/infra", "a", "3")
	if err != nil || cmp.Incomplete || cmp.Commits != 2 || len(cmp.Files) != 4 || cmp.Files[0] != "new/name.tf" || cmp.Files[1] != "old/name.tf" {
		t.Fatalf("small: %+v %v", cmp, err)
	}
	if cmp, _ := p.Compare(context.Background(), "acme/infra", "a", "1000"); !cmp.Incomplete {
		t.Fatal("1000 files must count as incomplete")
	}
}

func TestErrorsAndNames(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"insufficient_scope"}`))
	}))
	defer srv.Close()
	p, _ := New(GitLab, srv.URL, "t")
	if _, _, err := p.CheckAccount(context.Background(), "acme"); !errors.Is(err, ErrMissingScope) {
		t.Fatalf("scope: %v", err)
	}
	if _, err := New("github", "x", "t"); !errors.Is(err, ErrUnknownProvider) {
		t.Fatalf("unknown: %v", err)
	}
	if Name(GitLab) != "GitLab" || Name(Gitea) != "Gitea" || !Known(GitLab) || Known("github") {
		t.Fatal("names")
	}
}
