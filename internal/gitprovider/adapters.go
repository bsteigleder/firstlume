package gitprovider

import (
	"context"
	"errors"
	"fmt"

	"github.com/bsteigleder/firstlume/internal/gitea"
	"github.com/bsteigleder/firstlume/internal/gitlab"
)

// mapErr turns a client's error into this package's, keeping its text.
func mapErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, gitea.ErrUnauthorized), errors.Is(err, gitlab.ErrUnauthorized):
		return fmt.Errorf("%w: %v", ErrUnauthorized, err)
	case errors.Is(err, gitlab.ErrMissingScope):
		return fmt.Errorf("%w: %v", ErrMissingScope, err)
	case errors.Is(err, gitea.ErrNotFound), errors.Is(err, gitlab.ErrNotFound):
		return fmt.Errorf("%w: %v", ErrNotFound, err)
	case errors.Is(err, gitea.ErrTreeTooLarge), errors.Is(err, gitlab.ErrTreeTooLarge):
		return fmt.Errorf("%w: %v", ErrTreeTooLarge, err)
	}
	return err
}

type giteaProvider struct{ c *gitea.Client }

func newGitea(baseURL, token string) Provider { return giteaProvider{gitea.New(baseURL, token)} }

func (giteaProvider) Kind() string { return Gitea }

func (p giteaProvider) CheckAccount(ctx context.Context, account string) (string, string, error) {
	kind, err := p.c.CheckAccount(ctx, account)
	return string(kind), account, mapErr(err)
}

func giteaRepo(r gitea.Repo) Repo {
	return Repo{
		FullName: r.FullName, Owner: r.Owner.Login, Name: r.Name, DefaultBranch: r.DefaultBranch,
		Private: r.Private, Empty: r.Empty, CloneURL: r.CloneURL, WebURL: r.HTMLURL,
	}
}

func (p giteaProvider) ListRepos(ctx context.Context, account, kind string) ([]Repo, error) {
	repos, err := p.c.ListRepos(ctx, account, gitea.AccountKind(kind))
	if err != nil {
		return nil, mapErr(err)
	}
	out := make([]Repo, 0, len(repos))
	for _, r := range repos {
		out = append(out, giteaRepo(r))
	}
	return out, nil
}

func (p giteaProvider) GetRepo(ctx context.Context, fullName string) (*Repo, error) {
	owner, name := SplitFullName(fullName)
	r, err := p.c.GetRepo(ctx, owner, name)
	if err != nil {
		return nil, mapErr(err)
	}
	out := giteaRepo(*r)
	return &out, nil
}

func (p giteaProvider) ListBranches(ctx context.Context, fullName string) ([]string, error) {
	owner, name := SplitFullName(fullName)
	b, err := p.c.ListBranches(ctx, owner, name)
	return b, mapErr(err)
}

func (p giteaProvider) ResolveBranch(ctx context.Context, fullName, branch string) (string, error) {
	owner, name := SplitFullName(fullName)
	sha, err := p.c.ResolveBranch(ctx, owner, name, branch)
	return sha, mapErr(err)
}

func (p giteaProvider) Tree(ctx context.Context, fullName, sha string) ([]TreeEntry, error) {
	owner, name := SplitFullName(fullName)
	entries, err := p.c.Tree(ctx, owner, name, sha)
	if err != nil {
		return nil, mapErr(err)
	}
	out := make([]TreeEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, TreeEntry{Path: e.Path, Type: e.Type})
	}
	return out, nil
}

func (p giteaProvider) File(ctx context.Context, fullName, sha, path string) ([]byte, error) {
	owner, name := SplitFullName(fullName)
	b, err := p.c.File(ctx, owner, name, sha, path)
	return b, mapErr(err)
}

// Compare reads Gitea's commit-by-commit comparison. Fewer commits than it
// counts, or commits without any file (a Gitea that doesn't list them), mean
// it can't be trusted (spec 0006, R4.3).
func (p giteaProvider) Compare(ctx context.Context, fullName, base, head string) (*Comparison, error) {
	owner, name := SplitFullName(fullName)
	c, err := p.c.Compare(ctx, owner, name, base, head)
	if err != nil {
		return nil, mapErr(err)
	}
	out := &Comparison{Commits: len(c.Commits), Incomplete: len(c.Commits) < c.TotalCommits}
	seen := map[string]bool{}
	for _, commit := range c.Commits {
		for _, f := range commit.Files {
			if !seen[f.Filename] {
				seen[f.Filename] = true
				out.Files = append(out.Files, f.Filename)
			}
		}
	}
	if out.Commits > 0 && len(out.Files) == 0 {
		out.Incomplete = true
	}
	return out, nil
}

func (p giteaProvider) CommitPull(ctx context.Context, fullName, sha string) (int, string, error) {
	owner, name := SplitFullName(fullName)
	n, u, err := p.c.CommitPull(ctx, owner, name, sha)
	return n, u, mapErr(err)
}

type gitlabProvider struct{ c *gitlab.Client }

func newGitLab(baseURL, token string) Provider { return gitlabProvider{gitlab.New(baseURL, token)} }

func (gitlabProvider) Kind() string { return GitLab }

func (p gitlabProvider) CheckAccount(ctx context.Context, account string) (string, string, error) {
	kind, path, err := p.c.CheckAccount(ctx, account)
	return string(kind), path, mapErr(err)
}

func gitlabRepo(pr gitlab.Project) Repo {
	owner := pr.Namespace.FullPath
	if owner == "" {
		owner, _ = SplitFullName(pr.PathWithNamespace)
	}
	return Repo{
		FullName: pr.PathWithNamespace, Owner: owner, Name: pr.Path, DefaultBranch: pr.DefaultBranch,
		Private: pr.Visibility != "public", Empty: pr.EmptyRepo, CloneURL: pr.HTTPURLToRepo, WebURL: pr.WebURL,
	}
}

func (p gitlabProvider) ListRepos(ctx context.Context, account, kind string) ([]Repo, error) {
	projects, err := p.c.ListProjects(ctx, account, gitlab.AccountKind(kind))
	if err != nil {
		return nil, mapErr(err)
	}
	out := make([]Repo, 0, len(projects))
	for _, pr := range projects {
		// with_shared=false already leaves out projects from elsewhere; a
		// path outside the account would still not be one of its own.
		if InAccount(GitLab, account, pr.PathWithNamespace) {
			out = append(out, gitlabRepo(pr))
		}
	}
	return out, nil
}

func (p gitlabProvider) GetRepo(ctx context.Context, fullName string) (*Repo, error) {
	pr, err := p.c.GetProject(ctx, fullName)
	if err != nil {
		return nil, mapErr(err)
	}
	if pr.Archived {
		return nil, fmt.Errorf("%w: project %s is archived", ErrNotFound, fullName)
	}
	out := gitlabRepo(*pr)
	return &out, nil
}

func (p gitlabProvider) ListBranches(ctx context.Context, fullName string) ([]string, error) {
	b, err := p.c.ListBranches(ctx, fullName)
	return b, mapErr(err)
}

func (p gitlabProvider) ResolveBranch(ctx context.Context, fullName, branch string) (string, error) {
	sha, err := p.c.ResolveBranch(ctx, fullName, branch)
	return sha, mapErr(err)
}

func (p gitlabProvider) Tree(ctx context.Context, fullName, sha string) ([]TreeEntry, error) {
	entries, err := p.c.Tree(ctx, fullName, sha)
	if err != nil {
		return nil, mapErr(err)
	}
	out := make([]TreeEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, TreeEntry{Path: e.Path, Type: e.Type})
	}
	return out, nil
}

func (p gitlabProvider) File(ctx context.Context, fullName, sha, path string) ([]byte, error) {
	b, err := p.c.File(ctx, fullName, sha, path)
	return b, mapErr(err)
}

// Compare reads GitLab's comparison, which sums the files of all commits. It
// stops at 1000 files without saying so, so reaching that many, or a
// timeout, counts as incomplete (spec 0010, R5.6). A rename counts both paths.
func (p gitlabProvider) Compare(ctx context.Context, fullName, base, head string) (*Comparison, error) {
	c, err := p.c.Compare(ctx, fullName, base, head)
	if err != nil {
		return nil, mapErr(err)
	}
	out := &Comparison{Commits: len(c.Commits), Incomplete: c.CompareTimeout || len(c.Diffs) >= gitlab.MaxCompareFiles}
	seen := map[string]bool{}
	add := func(f string) {
		if f != "" && !seen[f] {
			seen[f] = true
			out.Files = append(out.Files, f)
		}
	}
	for _, d := range c.Diffs {
		add(d.NewPath)
		add(d.OldPath)
	}
	return out, nil
}

func (p gitlabProvider) CommitPull(ctx context.Context, fullName, sha string) (int, string, error) {
	n, u, err := p.c.CommitMergeRequest(ctx, fullName, sha)
	return n, u, mapErr(err)
}
