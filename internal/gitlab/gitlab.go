// Package gitlab is a small client for the parts of the GitLab REST API (v4)
// Firstlume uses: checking a group or user, listing its projects and branches,
// reading a commit's file tree and files, comparing two commits and finding
// the merge request of a commit (spec 0010). It works with gitlab.com and with
// a self-managed GitLab, also one served under a path.
package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var (
	// ErrUnauthorized means the token was rejected or lacks access.
	ErrUnauthorized = errors.New("gitlab: token rejected")
	// ErrMissingScope means GitLab took the token but it can't use the API:
	// it needs the read_api scope (spec 0010, L3).
	ErrMissingScope = errors.New("gitlab: token lacks the read_api scope")
	// ErrNotFound means the group, user, project, branch or file does not exist (or is not visible).
	ErrNotFound = errors.New("gitlab: not found")
	// ErrTreeTooLarge means a repository has more files than Firstlume reads.
	ErrTreeTooLarge = errors.New("gitlab: repository tree too large")
)

const (
	pageSize     = 100
	treePageSize = 1000
	// MaxTreeEntries bounds how much of a repository a discovery reads.
	MaxTreeEntries = 200_000
	maxFileSize    = 256 << 10
	// MaxCompareFiles is how many files GitLab lists in a comparison by
	// default (diff_max_files). It cuts there without saying so (L8).
	MaxCompareFiles = 1000
)

type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

func New(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

// AccountKind tells groups from users, whose projects are listed differently.
type AccountKind string

const (
	Group AccountKind = "group"
	User  AccountKind = "user"
)

type Project struct {
	ID                int64  `json:"id"`
	Path              string `json:"path"`
	PathWithNamespace string `json:"path_with_namespace"`
	DefaultBranch     string `json:"default_branch"`
	Visibility        string `json:"visibility"`
	EmptyRepo         bool   `json:"empty_repo"`
	Archived          bool   `json:"archived"`
	HTTPURLToRepo     string `json:"http_url_to_repo"`
	WebURL            string `json:"web_url"`
	Namespace         struct {
		FullPath string `json:"full_path"`
	} `json:"namespace"`
}

type TreeEntry struct {
	Path string `json:"path"`
	Type string `json:"type"` // "blob", "tree" or "commit" (a submodule)
}

// PathEscape escapes a path for one URL segment, slashes included, as GitLab
// wants for a project or group path ("acme%2Fplatform%2Finfra").
func PathEscape(s string) string { return strings.ReplaceAll(url.PathEscape(s), "/", "%2F") }

// get calls the API and decodes JSON into out. It returns the response headers.
func (c *Client) get(ctx context.Context, path string, query url.Values, out any) (http.Header, error) {
	u := c.baseURL + "/api/v4" + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	return c.getURL(ctx, u, out)
}

func (c *Client) getURL(ctx context.Context, u string, out any) (http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("PRIVATE-TOKEN", c.token)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gitlab: %w", err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, ErrUnauthorized
	case resp.StatusCode == http.StatusForbidden:
		var e struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&e)
		if e.Error == "insufficient_scope" {
			return nil, ErrMissingScope
		}
		return nil, ErrUnauthorized
	case resp.StatusCode == http.StatusNotFound:
		return nil, ErrNotFound
	case resp.StatusCode >= 300:
		return nil, fmt.Errorf("gitlab: %s answered %s", req.URL.Path, resp.Status)
	}
	if b, ok := out.(*[]byte); ok {
		*b, err = io.ReadAll(io.LimitReader(resp.Body, maxFileSize+1))
		if err == nil && len(*b) > maxFileSize {
			err = fmt.Errorf("gitlab: file is larger than %d bytes", maxFileSize)
		}
		return resp.Header, err
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return nil, fmt.Errorf("gitlab: decoding %s: %w", req.URL.Path, err)
	}
	return resp.Header, nil
}

// CheckAccount finds whether account is a group or a user the token can see,
// and returns its path as GitLab writes it. A group is found only by its full
// path (L1).
func (c *Client) CheckAccount(ctx context.Context, account string) (AccountKind, string, error) {
	var ns struct {
		Kind     string `json:"kind"`
		FullPath string `json:"full_path"`
	}
	if _, err := c.get(ctx, "/namespaces/"+PathEscape(account), nil, &ns); err != nil {
		return "", "", err
	}
	switch ns.Kind {
	case "group":
		return Group, ns.FullPath, nil
	case "user":
		return User, ns.FullPath, nil
	}
	return "", "", ErrNotFound
}

// ListProjects returns the account's projects the token can see: for a group
// also those of its subgroups, never archived ones, and never projects from
// elsewhere shared with the group (L2).
func (c *Client) ListProjects(ctx context.Context, account string, kind AccountKind) ([]Project, error) {
	path := "/groups/" + PathEscape(account) + "/projects"
	q := url.Values{"archived": {"false"}, "per_page": {strconv.Itoa(pageSize)}, "order_by": {"path"}, "sort": {"asc"}}
	if kind == User {
		path = "/users/" + PathEscape(account) + "/projects"
	} else {
		q.Set("include_subgroups", "true")
		q.Set("with_shared", "false")
	}
	var all []Project
	for page := 1; ; page++ {
		q.Set("page", strconv.Itoa(page))
		var batch []Project
		h, err := c.get(ctx, path, q, &batch)
		if err != nil {
			return nil, err
		}
		for _, p := range batch {
			if !p.Archived {
				all = append(all, p)
			}
		}
		if next := h.Get("X-Next-Page"); next == "" || len(batch) == 0 {
			return all, nil
		}
	}
}

func (c *Client) GetProject(ctx context.Context, fullPath string) (*Project, error) {
	var p Project
	if _, err := c.get(ctx, "/projects/"+PathEscape(fullPath), nil, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// ListBranches returns a project's branch names, following pagination.
func (c *Client) ListBranches(ctx context.Context, fullPath string) ([]string, error) {
	var out []string
	for page := 1; ; page++ {
		var batch []struct {
			Name string `json:"name"`
		}
		h, err := c.get(ctx, "/projects/"+PathEscape(fullPath)+"/repository/branches",
			url.Values{"per_page": {strconv.Itoa(pageSize)}, "page": {strconv.Itoa(page)}}, &batch)
		if err != nil {
			return nil, err
		}
		for _, b := range batch {
			out = append(out, b.Name)
		}
		if h.Get("X-Next-Page") == "" || len(batch) == 0 {
			return out, nil
		}
	}
}

// ResolveBranch returns the commit a branch points to.
func (c *Client) ResolveBranch(ctx context.Context, fullPath, branch string) (string, error) {
	var b struct {
		Commit struct {
			ID string `json:"id"`
		} `json:"commit"`
	}
	if _, err := c.get(ctx, "/projects/"+PathEscape(fullPath)+"/repository/branches/"+PathEscape(branch), nil, &b); err != nil {
		return "", err
	}
	return b.Commit.ID, nil
}

// Tree lists every file and directory at a commit, following keyset
// pagination. It fails with ErrTreeTooLarge rather than returning a partial
// listing.
func (c *Client) Tree(ctx context.Context, fullPath, sha string) ([]TreeEntry, error) {
	q := url.Values{"recursive": {"true"}, "ref": {sha}, "per_page": {strconv.Itoa(treePageSize)}, "pagination": {"keyset"}}
	next := c.baseURL + "/api/v4/projects/" + PathEscape(fullPath) + "/repository/tree?" + q.Encode()
	var all []TreeEntry
	for next != "" {
		var batch []TreeEntry
		h, err := c.getURL(ctx, next, &batch)
		if err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if len(all) > MaxTreeEntries {
			return nil, ErrTreeTooLarge
		}
		next = ""
		if len(batch) > 0 {
			if n := nextLink(h.Get("Link")); n != "" && sameOrigin(n, c.baseURL) {
				next = n
			}
		}
	}
	return all, nil
}

// nextLink returns the rel="next" URL of a Link header, or "".
func nextLink(header string) string {
	for _, part := range strings.Split(header, ",") {
		u, params, ok := strings.Cut(strings.TrimSpace(part), ";")
		if !ok || !strings.Contains(params, `rel="next"`) {
			continue
		}
		return strings.Trim(strings.TrimSpace(u), "<>")
	}
	return ""
}

// sameOrigin keeps the token from following a link to another host.
func sameOrigin(link, base string) bool {
	a, err1 := url.Parse(link)
	b, err2 := url.Parse(base)
	return err1 == nil && err2 == nil && a.Scheme == b.Scheme && a.Host == b.Host
}

// File returns a file's content at a commit (up to 256 KiB).
func (c *Client) File(ctx context.Context, fullPath, sha, path string) ([]byte, error) {
	var data []byte
	_, err := c.get(ctx, "/projects/"+PathEscape(fullPath)+"/repository/files/"+PathEscape(path)+"/raw", url.Values{"ref": {sha}}, &data)
	return data, err
}

// Comparison is what changed from one commit to another. GitLab sums the
// files of all commits.
type Comparison struct {
	Commits []struct {
		ID string `json:"id"`
	} `json:"commits"`
	Diffs []struct {
		OldPath string `json:"old_path"`
		NewPath string `json:"new_path"`
	} `json:"diffs"`
	CompareTimeout bool `json:"compare_timeout"`
}

// Compare lists the commits reachable from head and not from base, and the files they change.
func (c *Client) Compare(ctx context.Context, fullPath, base, head string) (*Comparison, error) {
	var out Comparison
	if _, err := c.get(ctx, "/projects/"+PathEscape(fullPath)+"/repository/compare", url.Values{"from": {base}, "to": {head}}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CommitMergeRequest finds the merge request a commit belongs to, like the
// merge commit of a merged one (L17). It prefers a merged one.
func (c *Client) CommitMergeRequest(ctx context.Context, fullPath, sha string) (int, string, error) {
	var mrs []struct {
		IID    int    `json:"iid"`
		WebURL string `json:"web_url"`
		State  string `json:"state"`
	}
	if _, err := c.get(ctx, "/projects/"+PathEscape(fullPath)+"/repository/commits/"+PathEscape(sha)+"/merge_requests", nil, &mrs); err != nil {
		return 0, "", err
	}
	if len(mrs) == 0 {
		return 0, "", ErrNotFound
	}
	best := mrs[0]
	for _, mr := range mrs {
		if mr.State == "merged" {
			best = mr
			break
		}
	}
	return best.IID, best.WebURL, nil
}
