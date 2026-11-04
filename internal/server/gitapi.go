package server

import (
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/bsteigleder/firstlume/internal/api"
	"github.com/bsteigleder/firstlume/internal/gitea"
	"github.com/bsteigleder/firstlume/internal/store"
)

var gitAccountRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,99}$`)

func (s *Server) gitRoutes(r chi.Router) {
	r.Get("/git/connection", s.getGitConnection)
	r.Put("/git/connection", s.putGitConnection)
	r.Delete("/git/connection", s.deleteGitConnection)
	r.Post("/git/webhook-secret", s.rotateWebhookSecret)
	r.Get("/git/events", s.listGitEvents)
	r.Get("/git/repos", s.listGitRepos)
	r.Get("/git/repos/{owner}/{repo}/branches", s.listGitBranches)
	r.Get("/git/repos/{owner}/{repo}/discover", s.discoverStacks)
}

// gitClient returns a client for the organization's connection, or writes the error.
func (s *Server) gitClient(w http.ResponseWriter, r *http.Request) (*gitea.Client, *store.GitConnection, bool) {
	conn, err := s.store.GetGitConnection(r.Context(), orgID(r))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "this organization has no Git connection yet")
		return nil, nil, false
	}
	if err != nil {
		s.internalError(w, r, err)
		return nil, nil, false
	}
	return gitea.New(conn.URL, conn.Token), conn, true
}

// gitError maps errors from the Git provider for requests using the stored connection.
func (s *Server) gitError(w http.ResponseWriter, r *http.Request, err error, what string) {
	switch {
	case errors.Is(err, gitea.ErrUnauthorized):
		writeError(w, http.StatusBadGateway, "Gitea rejected the service token: update it in the organization's Git connection")
	case errors.Is(err, gitea.ErrNotFound):
		writeError(w, http.StatusNotFound, what+" not found in Gitea")
	case errors.Is(err, gitea.ErrTreeTooLarge):
		writeError(w, http.StatusUnprocessableEntity, "this repository has too many files to search for .firstlume folders")
	default:
		s.log.Warn("git provider request failed", "path", r.URL.Path, "err", err)
		writeError(w, http.StatusBadGateway, "could not get "+what+" from Gitea: "+err.Error())
	}
}

// repoParam reads {owner}/{repo}, which must belong to the connected account.
func repoParam(w http.ResponseWriter, r *http.Request, conn *store.GitConnection) (string, string, bool) {
	owner, repo := chi.URLParam(r, "owner"), chi.URLParam(r, "repo")
	if !strings.EqualFold(owner, conn.Account) || !gitAccountRe.MatchString(repo) {
		writeError(w, http.StatusNotFound, "repository not found in the connected account")
		return "", "", false
	}
	return owner, repo, true
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
		req.Provider = "gitea"
	}
	base, err := cleanGitURL(req.URL)
	switch {
	case req.Provider != "gitea":
		writeError(w, http.StatusUnprocessableEntity, "provider must be gitea")
		return
	case err != nil:
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	case !gitAccountRe.MatchString(req.Account):
		writeError(w, http.StatusUnprocessableEntity, "account must be a Gitea organization or user name")
		return
	}
	token := req.Token
	if token == "" {
		existing, err := s.store.GetGitConnection(r.Context(), orgID(r))
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, "a service token is required")
			return
		}
		token = existing.Token
	}

	client := gitea.New(base, token)
	kind, err := client.CheckAccount(r.Context(), req.Account)
	switch {
	case errors.Is(err, gitea.ErrUnauthorized):
		writeError(w, http.StatusUnprocessableEntity, "Gitea rejected the token")
		return
	case errors.Is(err, gitea.ErrNotFound):
		writeError(w, http.StatusUnprocessableEntity, "account "+req.Account+" not found, or not visible to this token")
		return
	case err != nil:
		writeError(w, http.StatusUnprocessableEntity, "could not reach Gitea at "+base+": "+err.Error())
		return
	}
	repos, err := client.ListRepos(r.Context(), req.Account, kind)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "could not list the account's repositories: "+err.Error())
		return
	}
	conn, err := s.store.PutGitConnection(r.Context(), orgID(r), api.GitConnection{
		Provider: req.Provider, URL: base, Account: req.Account, AccountKind: string(kind),
	}, token)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, api.GitConnectionResponse{GitConnection: *conn, Repositories: len(repos)})
}

func cleanGitURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("url must be the Gitea address, like https://git.example.com")
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
	repos, err := client.ListRepos(r.Context(), conn.Account, gitea.AccountKind(conn.AccountKind))
	if err != nil {
		s.gitError(w, r, err, "repositories")
		return
	}
	out := make([]api.GitRepo, 0, len(repos))
	for _, repo := range repos {
		out = append(out, api.GitRepo{
			FullName: repo.FullName, Owner: repo.Owner.Login, Name: repo.Name, DefaultBranch: repo.DefaultBranch,
			Private: repo.Private, Empty: repo.Empty, WebURL: repo.HTMLURL,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) listGitBranches(w http.ResponseWriter, r *http.Request) {
	client, conn, ok := s.gitClient(w, r)
	if !ok {
		return
	}
	owner, repo, ok := repoParam(w, r, conn)
	if !ok {
		return
	}
	branches, err := client.ListBranches(r.Context(), owner, repo)
	if err != nil {
		s.gitError(w, r, err, "repository")
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
	owner, name, ok := repoParam(w, r, conn)
	if !ok {
		return
	}
	ctx := r.Context()
	repo, err := client.GetRepo(ctx, owner, name)
	if err != nil {
		s.gitError(w, r, err, "repository")
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
	if out.Commit, err = client.ResolveBranch(ctx, owner, name, ref); err != nil {
		s.gitError(w, r, err, "branch "+ref)
		return
	}
	found, err := client.Discover(ctx, owner, name, out.Commit)
	if err != nil {
		s.gitError(w, r, err, "repository files")
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
	owner, name, found := strings.Cut(req.GitRepository, "/")
	if !found || !strings.EqualFold(owner, conn.Account) || !gitAccountRe.MatchString(name) {
		writeError(w, http.StatusUnprocessableEntity, "git_repository must be owner/name in the connected account "+conn.Account)
		return false
	}
	repo, err := client.GetRepo(r.Context(), owner, name)
	if err != nil {
		s.gitError(w, r, err, "repository")
		return false
	}
	req.Repository, req.ConnectionID, req.GitToken = repo.CloneURL, conn.ID, ""
	if req.Ref == "" {
		req.Ref = repo.DefaultBranch
	}
	return true
}
