package gitea

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

// fake serves the subset of the Gitea API the client uses.
type fake struct {
	token string
	repos int
	tree  []TreeEntry
	files map[string]string
}

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "token "+f.token {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	if page == 0 {
		page = 1
	}
	p := strings.TrimPrefix(r.URL.Path, "/api/v1")
	switch {
	case p == "/orgs/acme":
		_ = json.NewEncoder(w).Encode(map[string]string{"username": "acme"})
	case p == "/users/ana":
		_ = json.NewEncoder(w).Encode(map[string]string{"login": "ana"})
	case p == "/orgs/acme/repos":
		limit, _ := strconv.Atoi(q.Get("limit"))
		var out []map[string]any
		for i := (page-1)*limit + 1; i <= f.repos && i <= page*limit; i++ {
			out = append(out, map[string]any{"full_name": fmt.Sprintf("acme/r%02d", i), "name": fmt.Sprintf("r%02d", i), "default_branch": "main"})
		}
		w.Header().Set("X-Total-Count", strconv.Itoa(f.repos))
		_ = json.NewEncoder(w).Encode(out)
	case strings.HasPrefix(p, "/repos/acme/infra/git/trees/abc"):
		per, _ := strconv.Atoi(q.Get("per_page"))
		lo, hi := (page-1)*per, page*per
		if hi > len(f.tree) {
			hi = len(f.tree)
		}
		if lo > hi {
			lo = hi
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"tree": f.tree[lo:hi], "truncated": hi < len(f.tree), "page": page, "total_count": len(f.tree)})
	case strings.HasPrefix(p, "/repos/acme/infra/raw/"):
		if q.Get("ref") != "abc" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		content, ok := f.files[strings.TrimPrefix(p, "/repos/acme/infra/raw/")]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(content))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func TestAccountsAndRepos(t *testing.T) {
	f := &fake{token: "t0k", repos: 123}
	srv := httptest.NewServer(f)
	defer srv.Close()
	ctx := context.Background()
	c := New(srv.URL+"/", "t0k")

	if k, err := c.CheckAccount(ctx, "acme"); err != nil || k != Organization {
		t.Fatalf("acme: %v %v", k, err)
	}
	if k, err := c.CheckAccount(ctx, "ana"); err != nil || k != User {
		t.Fatalf("ana: %v %v", k, err)
	}
	if _, err := c.CheckAccount(ctx, "nobody"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing account: %v", err)
	}
	if _, err := New(srv.URL, "wrong").CheckAccount(ctx, "acme"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("bad token: %v", err)
	}
	repos, err := c.ListRepos(ctx, "acme", Organization)
	if err != nil || len(repos) != 123 || repos[122].FullName != "acme/r123" {
		t.Fatalf("pagination: %d repos, %v", len(repos), err)
	}
}

func TestDiscover(t *testing.T) {
	f := &fake{token: "t0k", files: map[string]string{
		"environments/prod/.firstlume/config.yaml": "version: 1\nengine: terraform\nengine_version: 1.15.6\n",
		".firstlume/config.yaml":                   "version: 1\nengine: opentofu\nengine_version: 1.10.2\n",
		"legacy/vpn/.firstlume/config.yaml":        "version: 1\nengine: terraform\nengine_version: latest\n",
	}}
	// 20,000 unrelated files around the interesting ones, read over 20 pages.
	for i := 0; i < 20000; i++ {
		f.tree = append(f.tree, TreeEntry{Path: fmt.Sprintf("modules/m%05d/main.tf", i), Type: "blob"})
	}
	for _, e := range []TreeEntry{
		{".firstlume", "tree"}, {".firstlume/config.yaml", "blob"},
		{"environments/prod/.firstlume", "tree"}, {"environments/prod/.firstlume/config.yaml", "blob"},
		{"legacy/vpn/.firstlume", "tree"}, {"legacy/vpn/.firstlume/config.yaml", "blob"},
		{"deep/a/b/c/d/e/.firstlume", "tree"}, {"deep/a/b/c/d/e/.firstlume/README.md", "blob"},
	} {
		f.tree = append(f.tree, e)
	}
	srv := httptest.NewServer(f)
	defer srv.Close()

	found, err := New(srv.URL, "t0k").Discover(context.Background(), "acme", "infra", "abc")
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
