// Package gitprovider is what the server knows about a Git provider: Gitea or
// GitLab behind one interface (spec 0010, R1.1). Repositories are named by
// their full path, which in GitLab may have several levels
// ("acme/platform/infra").
package gitprovider

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Provider kinds, as stored in git_connections.provider.
const (
	Gitea  = "gitea"
	GitLab = "gitlab"
)

var (
	// ErrUnauthorized means the token was rejected or lacks access.
	ErrUnauthorized = errors.New("token rejected")
	// ErrMissingScope means the provider took the token but it lacks a scope the API needs.
	ErrMissingScope = errors.New("token lacks a scope")
	// ErrNotFound means the account, repository, branch or file does not exist (or is not visible).
	ErrNotFound = errors.New("not found")
	// ErrTreeTooLarge means a repository has more files than Firstlume reads.
	ErrTreeTooLarge = errors.New("repository tree too large")
	// ErrUnknownProvider means a connection names a provider Firstlume doesn't know.
	ErrUnknownProvider = errors.New("unknown Git provider")
)

// Repo is a repository (a GitLab project).
type Repo struct {
	// FullName is the path: "owner/name", or "group/subgroup/name" in GitLab.
	FullName string
	// Owner is the account or namespace holding it: everything before the name.
	Owner         string
	Name          string
	DefaultBranch string
	Private       bool
	Empty         bool
	CloneURL      string
	WebURL        string
}

type TreeEntry struct {
	Path string
	Type string // "blob" or "tree"
}

// Comparison is what changed between two commits.
type Comparison struct {
	// Commits is how many commits are reachable from head and not from base.
	Commits int
	// Files changed, each once.
	Files []string
	// Incomplete means the provider may have left commits or files out.
	Incomplete bool
}

// Provider is a Git provider seen through one connection's service token.
type Provider interface {
	// Kind is Gitea or GitLab.
	Kind() string
	// CheckAccount finds whether account exists for the token, its kind
	// ("organization", "group" or "user") and its path as the provider writes it.
	CheckAccount(ctx context.Context, account string) (kind, path string, err error)
	ListRepos(ctx context.Context, account, kind string) ([]Repo, error)
	GetRepo(ctx context.Context, fullName string) (*Repo, error)
	ListBranches(ctx context.Context, fullName string) ([]string, error)
	ResolveBranch(ctx context.Context, fullName, branch string) (string, error)
	// Tree lists every file and directory at a commit, or fails with ErrTreeTooLarge.
	Tree(ctx context.Context, fullName, sha string) ([]TreeEntry, error)
	// File returns a file's content at a commit (up to 256 KiB).
	File(ctx context.Context, fullName, sha, path string) ([]byte, error)
	// Compare tells what head has that base hasn't.
	Compare(ctx context.Context, fullName, base, head string) (*Comparison, error)
	// CommitPull finds the pull or merge request a commit came from.
	CommitPull(ctx context.Context, fullName, sha string) (number int, url string, err error)
}

// New returns the provider of a connection.
func New(kind, baseURL, token string) (Provider, error) {
	switch kind {
	case Gitea, "":
		return newGitea(baseURL, token), nil
	case GitLab:
		return newGitLab(baseURL, token), nil
	}
	return nil, fmt.Errorf("%w %q", ErrUnknownProvider, kind)
}

// Name is how screens and messages call a provider.
func Name(kind string) string {
	if kind == GitLab {
		return "GitLab"
	}
	return "Gitea"
}

// Known tells whether kind is a provider Firstlume can connect to.
func Known(kind string) bool { return kind == Gitea || kind == GitLab }

var (
	giteaNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,99}$`)
	// A GitLab path segment: letters, digits, "_", "-" and ".", not starting
	// with "-" or "." (so never ".."), and not ending in ".git" or ".atom".
	gitlabSegmentRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,254}$`)
)

// maxGroupDepth is how many levels a GitLab group path may have (R2.3).
const maxGroupDepth = 20

func validGitLabPath(p string, maxSegments int) bool {
	segs := strings.Split(p, "/")
	if len(segs) == 0 || len(segs) > maxSegments {
		return false
	}
	for _, s := range segs {
		l := strings.ToLower(s)
		if !gitlabSegmentRe.MatchString(s) || strings.HasSuffix(l, ".git") || strings.HasSuffix(l, ".atom") {
			return false
		}
	}
	return true
}

// ValidAccount tells whether account can name an account of the provider: a
// Gitea organization or user, or a GitLab group path or user (R2.3).
func ValidAccount(kind, account string) bool {
	if kind == GitLab {
		return validGitLabPath(account, maxGroupDepth)
	}
	return giteaNameRe.MatchString(account)
}

// InAccount tells whether a repository path belongs to the connected account:
// "account/name" in Gitea; in GitLab also any subgroup below it (R3.2). The
// comparison of the account ignores case, as both providers do.
func InAccount(kind, account, fullName string) bool {
	if kind == GitLab {
		if len(fullName) <= len(account)+1 || !strings.EqualFold(fullName[:len(account)], account) || fullName[len(account)] != '/' {
			return false
		}
		return validGitLabPath(fullName, maxGroupDepth+1)
	}
	owner, name, ok := strings.Cut(fullName, "/")
	return ok && strings.EqualFold(owner, account) && giteaNameRe.MatchString(name)
}

// SplitFullName returns the owner (namespace) and name of a repository path.
func SplitFullName(fullName string) (owner, name string) {
	i := strings.LastIndex(fullName, "/")
	if i < 0 {
		return "", fullName
	}
	return fullName[:i], fullName[i+1:]
}
