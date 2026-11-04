// Package gitea is a small client for the parts of the Gitea API Firstlume
// uses: checking an account, listing its repositories and branches, and
// reading a commit's file tree and files to find .firstlume folders.
package gitea

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
	ErrUnauthorized = errors.New("gitea: token rejected")
	// ErrNotFound means the account, repository, branch or file does not exist (or is not visible).
	ErrNotFound = errors.New("gitea: not found")
	// ErrTreeTooLarge means a repository has more files than Firstlume reads.
	ErrTreeTooLarge = errors.New("gitea: repository tree too large")
)

const (
	pageSize     = 50
	treePageSize = 1000
	// MaxTreeEntries bounds how much of a repository a discovery reads.
	MaxTreeEntries = 200_000
	maxFileSize    = 256 << 10
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

// AccountKind tells organizations from users, whose repositories are listed differently.
type AccountKind string

const (
	Organization AccountKind = "organization"
	User         AccountKind = "user"
)

type Repo struct {
	FullName      string `json:"full_name"`
	Name          string `json:"name"`
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
	Empty         bool   `json:"empty"`
	CloneURL      string `json:"clone_url"`
	HTMLURL       string `json:"html_url"`
	Owner         struct {
		Login string `json:"login"`
	} `json:"owner"`
}

type TreeEntry struct {
	Path string `json:"path"`
	Type string `json:"type"` // "blob" or "tree"
}

// get calls the API and decodes JSON into out. It returns the response headers.
func (c *Client) get(ctx context.Context, path string, query url.Values, out any) (http.Header, error) {
	u := c.baseURL + "/api/v1" + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "token "+c.token)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gitea: %w", err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, ErrUnauthorized
	case resp.StatusCode == http.StatusNotFound:
		return nil, ErrNotFound
	case resp.StatusCode >= 300:
		return nil, fmt.Errorf("gitea: %s answered %s", path, resp.Status)
	}
	if b, ok := out.(*[]byte); ok {
		*b, err = io.ReadAll(io.LimitReader(resp.Body, maxFileSize+1))
		if err == nil && len(*b) > maxFileSize {
			err = fmt.Errorf("gitea: %s is larger than %d bytes", path, maxFileSize)
		}
		return resp.Header, err
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return nil, fmt.Errorf("gitea: decoding %s: %w", path, err)
	}
	return resp.Header, nil
}

func esc(s string) string { return url.PathEscape(s) }

// CheckAccount finds whether account is an organization or a user the token can see.
func (c *Client) CheckAccount(ctx context.Context, account string) (AccountKind, error) {
	var v struct{}
	_, err := c.get(ctx, "/orgs/"+esc(account), nil, &v)
	if err == nil {
		return Organization, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return "", err
	}
	if _, err := c.get(ctx, "/users/"+esc(account), nil, &v); err != nil {
		return "", err
	}
	return User, nil
}

// ListRepos returns every repository of the account the token can see, following pagination.
func (c *Client) ListRepos(ctx context.Context, account string, kind AccountKind) ([]Repo, error) {
	path := "/orgs/" + esc(account) + "/repos"
	if kind == User {
		path = "/users/" + esc(account) + "/repos"
	}
	var all []Repo
	for page := 1; ; page++ {
		var batch []Repo
		h, err := c.get(ctx, path, url.Values{"limit": {strconv.Itoa(pageSize)}, "page": {strconv.Itoa(page)}}, &batch)
		if err != nil {
			return nil, err
		}
		all = append(all, batch...)
		total, _ := strconv.Atoi(h.Get("X-Total-Count"))
		if len(batch) == 0 || len(batch) < pageSize || (total > 0 && len(all) >= total) {
			return all, nil
		}
	}
}

func (c *Client) GetRepo(ctx context.Context, owner, name string) (*Repo, error) {
	var r Repo
	if _, err := c.get(ctx, "/repos/"+esc(owner)+"/"+esc(name), nil, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// ListBranches returns a repository's branch names, following pagination.
func (c *Client) ListBranches(ctx context.Context, owner, name string) ([]string, error) {
	var out []string
	for page := 1; ; page++ {
		var batch []struct {
			Name string `json:"name"`
		}
		h, err := c.get(ctx, "/repos/"+esc(owner)+"/"+esc(name)+"/branches",
			url.Values{"limit": {strconv.Itoa(pageSize)}, "page": {strconv.Itoa(page)}}, &batch)
		if err != nil {
			return nil, err
		}
		for _, b := range batch {
			out = append(out, b.Name)
		}
		total, _ := strconv.Atoi(h.Get("X-Total-Count"))
		if len(batch) < pageSize || (total > 0 && len(out) >= total) {
			return out, nil
		}
	}
}

// ResolveBranch returns the commit a branch points to.
func (c *Client) ResolveBranch(ctx context.Context, owner, name, branch string) (string, error) {
	var b struct {
		Commit struct {
			ID string `json:"id"`
		} `json:"commit"`
	}
	if _, err := c.get(ctx, "/repos/"+esc(owner)+"/"+esc(name)+"/branches/"+esc(branch), nil, &b); err != nil {
		return "", err
	}
	return b.Commit.ID, nil
}

// Tree lists every file and directory at a commit, reading all pages. It
// fails with ErrTreeTooLarge rather than returning a partial listing.
func (c *Client) Tree(ctx context.Context, owner, name, sha string) ([]TreeEntry, error) {
	var all []TreeEntry
	for page := 1; ; page++ {
		var t struct {
			Tree       []TreeEntry `json:"tree"`
			Truncated  bool        `json:"truncated"`
			TotalCount int         `json:"total_count"`
		}
		_, err := c.get(ctx, "/repos/"+esc(owner)+"/"+esc(name)+"/git/trees/"+esc(sha),
			url.Values{"recursive": {"true"}, "per_page": {strconv.Itoa(treePageSize)}, "page": {strconv.Itoa(page)}}, &t)
		if err != nil {
			return nil, err
		}
		if t.TotalCount > MaxTreeEntries {
			return nil, ErrTreeTooLarge
		}
		all = append(all, t.Tree...)
		if !t.Truncated || len(t.Tree) == 0 || len(all) >= t.TotalCount {
			if t.TotalCount > 0 && len(all) < t.TotalCount {
				return nil, fmt.Errorf("gitea: read %d of %d entries of %s/%s", len(all), t.TotalCount, owner, name)
			}
			return all, nil
		}
	}
}

// Comparison is what changed from one commit to another, commit by commit.
type Comparison struct {
	TotalCommits int `json:"total_commits"`
	Commits      []struct {
		SHA   string `json:"sha"`
		Files []struct {
			Filename string `json:"filename"`
		} `json:"files"`
	} `json:"commits"`
}

// Compare lists the commits reachable from head and not from base, with their files.
func (c *Client) Compare(ctx context.Context, owner, name, base, head string) (*Comparison, error) {
	var out Comparison
	if _, err := c.get(ctx, "/repos/"+esc(owner)+"/"+esc(name)+"/compare/"+esc(base)+"..."+esc(head), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CommitPull finds the pull request a commit belongs to, like the merge commit of a PR.
func (c *Client) CommitPull(ctx context.Context, owner, name, sha string) (int, string, error) {
	var pr struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
	}
	if _, err := c.get(ctx, "/repos/"+esc(owner)+"/"+esc(name)+"/commits/"+esc(sha)+"/pull", nil, &pr); err != nil {
		return 0, "", err
	}
	return pr.Number, pr.HTMLURL, nil
}

// File returns a file's content at a commit (up to 256 KiB).
func (c *Client) File(ctx context.Context, owner, name, sha, path string) ([]byte, error) {
	segments := strings.Split(path, "/")
	for i, s := range segments {
		segments[i] = esc(s)
	}
	var data []byte
	_, err := c.get(ctx, "/repos/"+esc(owner)+"/"+esc(name)+"/raw/"+strings.Join(segments, "/"), url.Values{"ref": {sha}}, &data)
	return data, err
}
