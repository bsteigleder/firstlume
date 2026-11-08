package server

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/bsteigleder/firstlume/internal/api"
	"github.com/bsteigleder/firstlume/internal/command"
	"github.com/bsteigleder/firstlume/internal/gitea"
	"github.com/bsteigleder/firstlume/internal/store"
)

// stackTools is what the stack's last run read from .firstlume/config.yaml.
// A stack that never ran isn't known yet: the executor checks again with the
// configuration of the commit it runs.
func stackTools(st *api.Stack) command.Tools {
	if st.Config == nil || st.Config.Engine == "" {
		return command.Tools{}
	}
	return command.Tools{Known: true, Engine: st.Config.Engine, Terragrunt: st.Config.TerragruntVersion != ""}
}

func commandAccess(s string) command.Access {
	if s == "" {
		return command.AccessRead
	}
	return command.Access(s)
}

// checkCommand validates a command for the dialog, as typed (spec 0009, S1-S6).
func (s *Server) checkCommand(w http.ResponseWriter, r *http.Request) {
	var req api.CheckCommandRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	st, err := s.store.GetStack(r.Context(), orgID(r), id(r))
	if err != nil {
		s.storeError(w, r, err, "stack")
		return
	}
	access := commandAccess(req.StateAccess)
	if !access.Valid() {
		writeError(w, http.StatusUnprocessableEntity, "state_access must be read or write")
		return
	}
	lines, err := command.Check(req.Command, stackTools(st), access)
	out := api.CheckCommandResponse{OK: err == nil, Lines: len(lines)}
	var ce *command.Error
	if errors.As(err, &ce) {
		out.Error = &api.CommandError{Line: ce.Line, Message: ce.Error()}
	}
	for _, l := range lines {
		out.Writes = out.Writes || l.Writes
	}
	writeJSON(w, http.StatusOK, out)
}

// createCommandRun queues a command run after checking the command (R1, R2).
func (s *Server) createCommandRun(w http.ResponseWriter, r *http.Request, req api.CreateRunRequest) {
	access := commandAccess(req.StateAccess)
	if !access.Valid() {
		writeError(w, http.StatusUnprocessableEntity, "state_access must be read or write")
		return
	}
	st, err := s.store.GetStack(r.Context(), orgID(r), id(r))
	if err != nil {
		s.storeError(w, r, err, "stack")
		return
	}
	if _, err := command.Check(req.Command, stackTools(st), access); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	ref := req.Ref
	if ref == "" {
		ref = st.Ref
	}
	commit := s.resolveCommit(r.Context(), orgID(r), st, ref)
	run, err := s.store.CreateCommandRun(r.Context(), orgID(r), st.ID, req.Ref, commit, req.Command, string(access), actorName(r))
	if err != nil {
		s.storeError(w, r, err, "stack")
		return
	}
	writeJSON(w, http.StatusCreated, run)
}

var fullSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

// resolveCommit fixes the commit a command run will run when it can (R3): a
// full SHA as is, a branch through the organization's Git connection. When it
// can't, "" leaves it to the checkout, as for other runs.
func (s *Server) resolveCommit(ctx context.Context, orgID string, st *api.Stack, ref string) string {
	if fullSHA.MatchString(ref) {
		return ref
	}
	if st.ConnectionID == "" {
		return ""
	}
	owner, name, ok := repoOfCloneURL(st.Repository)
	if !ok {
		return ""
	}
	conn, err := s.store.GetGitConnection(ctx, orgID)
	// The connection's token never goes to a repository of another account.
	if errors.Is(err, store.ErrNotFound) || (err == nil && (conn.ID != st.ConnectionID || !strings.EqualFold(owner, conn.Account))) {
		return ""
	}
	if err != nil {
		s.log.Warn("reading the Git connection to fix a command run's commit", "stack", st.ID, "err", err)
		return ""
	}
	sha, err := gitea.New(conn.URL, conn.Token).ResolveBranch(ctx, owner, name, ref)
	if err != nil {
		s.log.Info("couldn't resolve a command run's ref; the checkout will", "stack", st.ID, "ref", ref, "err", err)
		return ""
	}
	return sha
}
