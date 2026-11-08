package server

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/bsteigleder/firstlume/internal/api"
	"github.com/bsteigleder/firstlume/internal/gitprovider"
	"github.com/bsteigleder/firstlume/internal/store"
)

func (s *Server) gitRoutes(r chi.Router) {
	r.Get("/git/connection", s.getGitConnection)
	r.Put("/git/connection", s.putGitConnection)
	r.Delete("/git/connection", s.deleteGitConnection)
	r.Post("/git/webhook-secret", s.rotateWebhookSecret)
	r.Get("/git/events", s.listGitEvents)
	r.Get("/git/repos", s.listGitRepos)
	// A GitLab path has several levels, so the repository goes in the query
	// (spec 0010, R3.1). The routes by {owner}/{repo} stay for Gitea.
	r.Get("/git/repo/branches", s.listGitBranches)
	r.Get("/git/repo/discover", s.discoverStacks)
	r.Get("/git/repos/{owner}/{repo}/branches", s.listGitBranches)
	r.Get("/git/repos/{owner}/{repo}/discover", s.discoverStacks)
}

// gitClient returns the provider of the organization's connection, or writes the error.
func (s *Server) gitClient(w http.ResponseWriter, r *http.Request) (gitprovider.Provider, *store.GitConnection, bool) {
	conn, err := s.store.GetGitConnection(r.Context(), orgID(r))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "this organization has no Git connection yet")
		return nil, nil, false
	}
	if err != nil {
		s.internalError(w, r, err)
		return nil, nil, false
	}
	client, err := gitprovider.New(conn.Provider, conn.URL, conn.Token)
	if err != nil {
		s.internalError(w, r, err)
		return nil, nil, false
	}
	return client, conn, true
}

// gitError maps errors from the Git provider for requests using the stored connection.
func (s *Server) gitError(w http.ResponseWriter, r *http.Request, err error, what, provider string) {
	name := gitprovider.Name(provider)
	switch {
	case errors.Is(err, gitprovider.ErrUnauthorized):
		writeError(w, http.StatusBadGateway, name+" rejected the service token: update it in the organization's Git connection")
	case errors.Is(err, gitprovider.ErrMissingScope):
		writeError(w, http.StatusBadGateway, name+" accepted the service token, but it can't use the API: it needs the read_api scope")
	case errors.Is(err, gitprovider.ErrNotFound):
		writeError(w, http.StatusNotFound, what+" not found in "+name)
	case errors.Is(err, gitprovider.ErrTreeTooLarge):
		writeError(w, http.StatusUnprocessableEntity, "this repository has too many files to search for .firstlume folders")
	default:
		s.log.Warn("git provider request failed", "path", r.URL.Path, "err", err)
		writeError(w, http.StatusBadGateway, "could not get "+what+" from "+name+": "+err.Error())
	}
}

// repoParam reads the repository of a request, from ?repo= or from
// {owner}/{repo}. It must belong to the connected account.
func repoParam(w http.ResponseWriter, r *http.Request, conn *store.GitConnection) (string, bool) {
	full := r.URL.Query().Get("repo")
	if owner := chi.URLParam(r, "owner"); owner != "" {
		full = owner + "/" + chi.URLParam(r, "repo")
	}
	if !gitprovider.InAccount(conn.Provider, conn.Account, full) {
		writeError(w, http.StatusNotFound, "repository not found in the connected account")
		return "", false
	}
	return full, true
}

// repoPathOf reads a repository's path out of a stack's clone URL, under the
// connection's own path for a GitLab served under one (spec 0010, R3.3). The
// host isn't compared: a provider may give clone URLs on its public name.
func repoPathOf(baseURL, cloneURL string) string {
	c, err := url.Parse(cloneURL)
	if err != nil {
		return ""
	}
	p := strings.TrimSuffix(strings.Trim(c.Path, "/"), ".git")
	if b, err := url.Parse(baseURL); err == nil {
		if prefix := strings.Trim(b.Path, "/"); prefix != "" && strings.HasPrefix(strings.ToLower(p), strings.ToLower(prefix)+"/") {
			p = p[len(prefix)+1:]
		}
	}
	return p
}

func (s *Server) getGitConnection(w http.ResponseWriter, r *http.Request) {
	conn, err := s.store.GetGitConnection(r.Context(), orgID(r))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "this organization has no Git connection yet")
			return
		}
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, conn.GitConnection)
}

// putGitConnection checks that the token reaches the account before saving.
func (s *Server) putGitConnection(w http.ResponseWriter, r *http.Request) {
	var req api.PutGitConnectionRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Provider == "" {
		req.Provider = gitprovider.Gitea
	}
	name := gitprovider.Name(req.Provider)
	req.Account = strings.Trim(strings.TrimSpace(req.Account), "/")
	base, err := cleanGitURL(req.URL, name)
	switch {
	case !gitprovider.Known(req.Provider):
		writeError(w, http.StatusUnprocessableEntity, "provider must be gitea or gitlab")
		return
	case err != nil:
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	case !gitprovider.ValidAccount(req.Provider, req.Account):
		if req.Provider == gitprovider.GitLab {
			writeError(w, http.StatusUnprocessableEntity, "account must be a GitLab group's full path, like acme/platform, or a user name")
		} else {
			writeError(w, http.StatusUnprocessableEntity, "account must be a Gitea organization or user name")
		}
		return
	}
	token := req.Token
	if token == "" {
		existing, err := s.store.GetGitConnection(r.Context(), orgID(r))
		// A stored token only goes back to the same provider and address.
		if err != nil || existing.Provider != req.Provider || existing.URL != base {
			writeError(w, http.StatusUnprocessableEntity, "a service token is required")
			return
		}
		token = existing.Token
	}

	client, err := gitprovider.New(req.Provider, base, token)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	kind, account, err := client.CheckAccount(r.Context(), req.Account)
	switch {
	case errors.Is(err, gitprovider.ErrUnauthorized):
		writeError(w, http.StatusUnprocessableEntity, name+" rejected the token")
		return
	case errors.Is(err, gitprovider.ErrMissingScope):
		writeError(w, http.StatusUnprocessableEntity, name+" accepted the token, but it can't use the API: add the read_api scope, or create a token with read_api and read_repository")
		return
	case errors.Is(err, gitprovider.ErrNotFound):
		if req.Provider == gitprovider.GitLab {
			writeError(w, http.StatusUnprocessableEntity, "no group or user "+req.Account+" visible to this token: use the full path, like acme/platform, and check that the bot is a member of the group")
		} else {
			writeError(w, http.StatusUnprocessableEntity, "account "+req.Account+" not found, or not visible to this token")
		}
		return
	case err != nil:
		writeError(w, http.StatusUnprocessableEntity, "could not reach "+name+" at "+base+": "+err.Error())
		return
	}
	if account == "" || !gitprovider.ValidAccount(req.Provider, account) {
		account = req.Account
	}
	repos, err := client.ListRepos(r.Context(), account, kind)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "could not list the account's repositories: "+err.Error())
		return
	}
	conn, err := s.store.PutGitConnection(r.Context(), orgID(r), api.GitConnection{
		Provider: req.Provider, URL: base, Account: account, AccountKind: kind,
	}, token)
	if errors.Is(err, store.ErrConflict) {
		writeError(w, http.StatusConflict, "stacks clone through this organization's current Git connection: delete them before connecting "+name)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, api.GitConnectionResponse{GitConnection: *conn, Repositories: len(repos)})
}

// cleanGitURL checks a provider's address. It may have a path, for a GitLab
// served under one (R2.2).
func cleanGitURL(raw, name string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("url must be the " + name + " address, like https://git.example.com")
	}
	return strings.TrimRight(u.String(), "/"), nil
}

func (s *Server) deleteGitConnection(w http.ResponseWriter, r *http.Request) {
	err := s.store.DeleteGitConnection(r.Context(), orgID(r))
	switch {
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, "stacks still clone through this connection: delete them or point them elsewhere first")
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "this organization has no Git connection")
	case err != nil:
		s.internalError(w, r, err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) listGitRepos(w http.ResponseWriter, r *http.Request) {
	client, conn, ok := s.gitClient(w, r)
	if !ok {
		return
	}
	repos, err := client.ListRepos(r.Context(), conn.Account, conn.AccountKind)
	if err != nil {
		s.gitError(w, r, err, "repositories", conn.Provider)
		return
	}
	out := make([]api.GitRepo, 0, len(repos))
	for _, repo := range repos {
		out = append(out, api.GitRepo{
			FullName: repo.FullName, Owner: repo.Owner, Name: repo.Name, DefaultBranch: repo.DefaultBranch,
			Private: repo.Private, Empty: repo.Empty, WebURL: repo.WebURL,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) listGitBranches(w http.ResponseWriter, r *http.Request) {
	client, conn, ok := s.gitClient(w, r)
	if !ok {
		return
	}
	full, ok := repoParam(w, r, conn)
	if !ok {
		return
	}
	branches, err := client.ListBranches(r.Context(), full)
	if err != nil {
		s.gitError(w, r, err, "repository", conn.Provider)
		return
	}
	if branches == nil {
		branches = []string{}
	}
	writeJSON(w, http.StatusOK, branches)
}

// discoverStacks lists the .firstlume folders of a repository branch, marking
// invalid ones and those another stack already manages.
func (s *Server) discoverStacks(w http.ResponseWriter, r *http.Request) {
	client, conn, ok := s.gitClient(w, r)
	if !ok {
		return
	}
	full, ok := repoParam(w, r, conn)
	if !ok {
		return
	}
	ctx := r.Context()
	repo, err := client.GetRepo(ctx, full)
	if err != nil {
		s.gitError(w, r, err, "repository", conn.Provider)
		return
	}
	ref := r.URL.Query().Get("ref")
	if ref == "" {
		ref = repo.DefaultBranch
	}
	if err := validateRef(ref); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	out := api.DiscoverResponse{Repository: repo.FullName, Ref: ref, Stacks: []api.DiscoveredStack{}}
	if repo.Empty {
		writeJSON(w, http.StatusOK, out)
		return
	}
	if out.Commit, err = client.ResolveBranch(ctx, full, ref); err != nil {
		s.gitError(w, r, err, "branch "+ref, conn.Provider)
		return
	}
	found, err := gitprovider.Discover(ctx, client, full, out.Commit)
	if err != nil {
		s.gitError(w, r, err, "repository files", conn.Provider)
		return
	}
	used, err := s.store.StacksUsing(ctx, orgID(r), repo.CloneURL, ref)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	for _, f := range found {
		d := api.DiscoveredStack{Path: f.Path, Config: f.Config, Error: f.Error}
		if st, ok := used[f.Path]; ok {
			d.UsedBy = &st
		}
		out.Stacks = append(out.Stacks, d)
	}
	writeJSON(w, http.StatusOK, out)
}

// resolveGitRepository fills a stack request that picked a repository of the
// connection: its clone URL, default branch and the connection to clone with.
func (s *Server) resolveGitRepository(w http.ResponseWriter, r *http.Request, req *api.CreateStackRequest) bool {
	client, conn, ok := s.gitClient(w, r)
	if !ok {
		return false
	}
	if !gitprovider.InAccount(conn.Provider, conn.Account, req.GitRepository) {
		what := "owner/name"
		if conn.Provider == gitprovider.GitLab {
			what = "the project's full path"
		}
		writeError(w, http.StatusUnprocessableEntity, "git_repository must be "+what+" in the connected account "+conn.Account)
		return false
	}
	repo, err := client.GetRepo(r.Context(), req.GitRepository)
	if err != nil {
		s.gitError(w, r, err, "repository", conn.Provider)
		return false
	}
	req.Repository, req.ConnectionID, req.GitToken = repo.CloneURL, conn.ID, ""
	if req.Ref == "" {
		req.Ref = repo.DefaultBranch
	}
	return true
}
