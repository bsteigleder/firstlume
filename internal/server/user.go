package server

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/bsteigleder/firstlume/internal/api"
	"github.com/bsteigleder/firstlume/internal/store"
)

// adminIdentity is recorded as the actor for requests made with the admin token.
const adminIdentity = "admin-token"

func (s *Server) userRoutes(r chi.Router) {
	r.Get("/orgs", s.listOrganizations)
	r.Post("/orgs", s.createOrganization)
	r.Route("/orgs/{org}", func(r chi.Router) {
		r.Use(s.requireOrg)
		r.Get("/", s.getOrganization)
		r.Patch("/", s.updateOrganization)
		r.Delete("/", s.deleteOrganization)
		r.Get("/tree", s.tree)
		r.Get("/settings", s.getSettings(api.LevelOrganization))
		r.Put("/settings", s.putSettings(api.LevelOrganization))

		r.Post("/folders", s.createFolder)
		r.With(s.requireID).Patch("/folders/{id}", s.renameFolder)
		r.With(s.requireID).Delete("/folders/{id}", s.deleteFolder)
		r.With(s.requireID).Get("/folders/{id}/settings", s.getSettings(api.LevelFolder))
		r.With(s.requireID).Put("/folders/{id}/settings", s.putSettings(api.LevelFolder))

		r.Post("/stacks", s.createStack)
		r.With(s.requireID).Get("/stacks/{id}", s.getStack)
		r.With(s.requireID).Patch("/stacks/{id}", s.updateStack)
		r.With(s.requireID).Delete("/stacks/{id}", s.deleteStack)
		r.With(s.requireID).Get("/stacks/{id}/settings", s.getSettings(api.LevelStack))
		r.With(s.requireID).Put("/stacks/{id}/settings", s.putSettings(api.LevelStack))
		r.With(s.requireID).Get("/stacks/{id}/runs", s.listRuns)
		r.With(s.requireID).Post("/stacks/{id}/runs", s.createRun)
		r.With(s.requireID).Get("/stacks/{id}/state", s.listStateUnits)
		r.With(s.requireID).Get("/stacks/{id}/resources", s.stackResources)
		r.With(s.requireID).Delete("/stacks/{id}/state", s.deleteState)
		r.With(s.requireID).Post("/stacks/{id}/state/import", s.importState)
		r.With(s.requireID).Get("/stacks/{id}/declaration", s.getDeclaration)
		r.With(s.requireID).Post("/stacks/{id}/vault/check", s.vaultCheck)
		r.Get("/vault", s.getVaultConnection)
		r.Put("/vault", s.putVaultConnection)
		r.Delete("/vault", s.deleteVaultConnection)
		r.Get("/vault/public-key", s.vaultPublicKey)
		r.Get("/state-storage", s.getStateStorage)
		r.Put("/state-storage", s.putStateStorage)
		r.Post("/state-storage/check", s.postStateStorageCheck)
		r.With(s.requireID).Get("/stacks/{id}/state/tokens", s.listStateTokens)
		r.With(s.requireID).Post("/stacks/{id}/state/tokens", s.createStateToken)
		r.With(s.requireID).Delete("/stacks/{id}/state/tokens/{token}", s.revokeStateToken)

		r.Get("/runs", s.listActiveRuns)
		r.With(s.requireID).Get("/runs/{id}", s.getRun)
		r.With(s.requireID).Post("/runs/{id}/approve", s.approveRun)
		r.With(s.requireID).Post("/runs/{id}/discard", s.discardRun)
		r.With(s.requireID).Post("/runs/{id}/cancel", s.cancelRun)
		r.With(s.requireID).Get("/runs/{id}/logs", s.runLogs)
		r.With(s.requireID).Get("/runs/{id}/changes", s.runChanges)
		r.With(s.requireID).Get("/runs/{id}/steps", s.runSteps)

		s.gitRoutes(r)

		r.Get("/agents", s.listAgents)
		r.Post("/agents", s.createAgent)
		r.With(s.requireID).Delete("/agents/{id}", s.deleteAgent)
	})
}

// requireOrg resolves the organization slug in the URL.
func (s *Server) requireOrg(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		org, err := s.store.GetOrganizationBySlug(r.Context(), chi.URLParam(r, "org"))
		if err != nil {
			s.storeError(w, r, err, "organization")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxOrg, org)))
	})
}

func orgFrom(ctx context.Context) *api.Organization {
	return ctx.Value(ctxOrg).(*api.Organization)
}

func orgID(r *http.Request) string { return orgFrom(r.Context()).ID }

// requireID rejects malformed ids up front so they surface as 404s, not database errors.
func (s *Server) requireID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !validUUID(chi.URLParam(r, "id")) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func id(r *http.Request) string { return chi.URLParam(r, "id") }

// --- organizations ---

func (s *Server) listOrganizations(w http.ResponseWriter, r *http.Request) {
	out, err := s.store.ListOrganizations(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) createOrganization(w http.ResponseWriter, r *http.Request) {
	var req api.CreateOrganizationRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := validateSlug(req.Slug); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	name, err := cleanName("name", req.Name)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	req.Name = name
	var o *api.Organization
	if u := principalFrom(r.Context()).user; u != nil {
		// The creator becomes its administrator (R7.7).
		o, err = s.store.CreateOrganizationBy(r.Context(), req, u.ID)
	} else {
		o, err = s.store.CreateOrganization(r.Context(), req)
	}
	if err != nil {
		s.storeError(w, r, err, "organization")
		return
	}
	writeJSON(w, http.StatusCreated, o)
}

func (s *Server) getOrganization(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, orgFrom(r.Context()))
}

func (s *Server) updateOrganization(w http.ResponseWriter, r *http.Request) {
	var req api.UpdateOrganizationRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	name, err := cleanName("name", req.Name)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if req.Slug != "" {
		if err := validateSlug(req.Slug); err != nil {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
	}
	o, err := s.store.UpdateOrganization(r.Context(), orgID(r), name, req.Slug)
	if err != nil {
		s.storeError(w, r, err, "organization")
		return
	}
	writeJSON(w, http.StatusOK, o)
}

func (s *Server) deleteOrganization(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteOrganization(r.Context(), orgID(r)); err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeError(w, http.StatusConflict, "only an organization without folders or stacks can be deleted")
			return
		}
		s.storeError(w, r, err, "organization")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) tree(w http.ResponseWriter, r *http.Request) {
	out, err := s.store.Tree(r.Context(), orgID(r))
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// --- folders ---

func (s *Server) createFolder(w http.ResponseWriter, r *http.Request) {
	var req api.CreateFolderRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	name, err := cleanName("name", req.Name)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if req.ParentID != "" && !validUUID(req.ParentID) {
		writeError(w, http.StatusUnprocessableEntity, "parent_id must be a folder id")
		return
	}
	f, err := s.store.CreateFolder(r.Context(), orgID(r), req.ParentID, name)
	if err != nil {
		s.storeError(w, r, err, "folder")
		return
	}
	writeJSON(w, http.StatusCreated, f)
}

func (s *Server) renameFolder(w http.ResponseWriter, r *http.Request) {
	var req api.UpdateFolderRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	name, err := cleanName("name", req.Name)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	f, err := s.store.RenameFolder(r.Context(), orgID(r), id(r), name)
	if err != nil {
		s.storeError(w, r, err, "folder")
		return
	}
	writeJSON(w, http.StatusOK, f)
}

func (s *Server) deleteFolder(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteFolder(r.Context(), orgID(r), id(r)); err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeError(w, http.StatusConflict, "only an empty folder can be deleted: move or delete its folders and stacks first")
			return
		}
		s.storeError(w, r, err, "folder")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- settings ---

func levelOf(kind api.LevelKind, r *http.Request) api.Level {
	if kind == api.LevelOrganization {
		return api.Level{Kind: kind, ID: orgID(r)}
	}
	return api.Level{Kind: kind, ID: id(r)}
}

func (s *Server) getSettings(kind api.LevelKind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		out, err := s.store.GetSettings(r.Context(), orgID(r), levelOf(kind, r))
		if err != nil {
			s.storeError(w, r, err, string(kind))
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func (s *Server) putSettings(kind api.LevelKind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req api.LevelSettings
		if !decodeJSON(w, r, &req) {
			return
		}
		if err := validateLevelSettings(&req); err != nil {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		if err := s.store.PutSettings(r.Context(), orgID(r), levelOf(kind, r), req); err != nil {
			s.storeError(w, r, err, string(kind))
			return
		}
		s.getSettings(kind)(w, r)
	}
}

// --- stacks ---

func (s *Server) createStack(w http.ResponseWriter, r *http.Request) {
	var req api.CreateStackRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.GitRepository != "" && !s.resolveGitRepository(w, r, &req) {
		return
	}
	if err := normalizeStack(&req); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	st, err := s.store.CreateStack(r.Context(), orgID(r), req)
	if err != nil {
		s.storeError(w, r, err, "stack")
		return
	}
	writeJSON(w, http.StatusCreated, st)
}

func (s *Server) getStack(w http.ResponseWriter, r *http.Request) {
	st, err := s.store.GetStack(r.Context(), orgID(r), id(r))
	if err != nil {
		s.storeError(w, r, err, "stack")
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) updateStack(w http.ResponseWriter, r *http.Request) {
	var req api.UpdateStackRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := normalizeStackUpdate(&req); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	cur, err := s.store.GetStack(r.Context(), orgID(r), id(r))
	if err != nil {
		s.storeError(w, r, err, "stack")
		return
	}
	// A connection stack clones with the organization's service token, which
	// must never be sent to another repository.
	if cur.ConnectionID != "" && ((req.Repository != nil && *req.Repository != cur.Repository) || req.GitToken != nil) {
		writeError(w, http.StatusUnprocessableEntity, "this stack comes from the Git connection: its repository and token can't change; create another stack instead")
		return
	}
	st, err := s.store.UpdateStack(r.Context(), orgID(r), id(r), req)
	if err != nil {
		s.storeError(w, r, err, "stack")
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) deleteStack(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteStack(r.Context(), orgID(r), id(r), adminIdentity); err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeError(w, http.StatusConflict, "a stack with a run in progress cannot be deleted")
			return
		}
		if errors.Is(err, store.ErrStateNotEmpty) {
			writeError(w, http.StatusConflict, "the stack's state still manages resources: run a destroy, or delete the state first")
			return
		}
		s.storeError(w, r, err, "stack")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- runs ---

func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 && v <= 200 {
		limit = v
	}
	if _, err := s.store.GetStack(r.Context(), orgID(r), id(r)); err != nil {
		s.storeError(w, r, err, "stack")
		return
	}
	out, err := s.store.ListRuns(r.Context(), orgID(r), id(r), limit)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) createRun(w http.ResponseWriter, r *http.Request) {
	var req api.CreateRunRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if !req.Type.Valid() {
		writeError(w, http.StatusUnprocessableEntity, "type must be plan, apply, destroy or drift")
		return
	}
	if req.Ref != "" {
		if err := validateRef(req.Ref); err != nil {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
	}
	run, err := s.store.CreateRun(r.Context(), orgID(r), id(r), req.Type, req.Ref, adminIdentity)
	if err != nil {
		s.storeError(w, r, err, "stack")
		return
	}
	writeJSON(w, http.StatusCreated, run)
}

func (s *Server) listActiveRuns(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("active") != "true" {
		writeError(w, http.StatusBadRequest, "only ?active=true is supported; list a stack's runs under /stacks/{id}/runs")
		return
	}
	// failed_within (e.g. "1h") also lists the runs that failed that recently.
	var failedWithin time.Duration
	if v := r.URL.Query().Get("failed_within"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 || d > 7*24*time.Hour {
			writeError(w, http.StatusBadRequest, "failed_within must be a duration between 1s and 168h, such as 1h")
			return
		}
		failedWithin = d
	}
	out, err := s.store.ListActiveRuns(r.Context(), orgID(r), failedWithin)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getRun(w http.ResponseWriter, r *http.Request) {
	run, err := s.store.GetRun(r.Context(), orgID(r), id(r))
	if err != nil {
		s.storeError(w, r, err, "run")
		return
	}
	if run.ApplyTotals, err = s.store.ApplyTotals(r.Context(), orgID(r), run.ID); err != nil {
		s.storeError(w, r, err, "run")
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (s *Server) approveRun(w http.ResponseWriter, r *http.Request) {
	var req api.ApproveRequest
	if r.ContentLength != 0 && !decodeJSON(w, r, &req) {
		return
	}
	run, err := s.store.ApproveRun(r.Context(), orgID(r), id(r), adminIdentity, req.Acknowledged)
	if err != nil {
		s.storeError(w, r, err, "run")
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (s *Server) runChanges(w http.ResponseWriter, r *http.Request) {
	out, err := s.store.GetChanges(r.Context(), orgID(r), id(r))
	if err != nil {
		s.storeError(w, r, err, "run")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) runSteps(w http.ResponseWriter, r *http.Request) {
	out, err := s.store.ListSteps(r.Context(), orgID(r), id(r))
	if err != nil {
		s.storeError(w, r, err, "run")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) discardRun(w http.ResponseWriter, r *http.Request) {
	run, err := s.store.DiscardRun(r.Context(), orgID(r), id(r))
	if err != nil {
		s.storeError(w, r, err, "run")
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (s *Server) cancelRun(w http.ResponseWriter, r *http.Request) {
	run, err := s.store.CancelRun(r.Context(), orgID(r), id(r))
	if err != nil {
		s.storeError(w, r, err, "run")
		return
	}
	writeJSON(w, http.StatusOK, run)
}

// --- agents ---

func (s *Server) listAgents(w http.ResponseWriter, r *http.Request) {
	out, err := s.store.ListAgents(r.Context(), orgID(r))
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) createAgent(w http.ResponseWriter, r *http.Request) {
	var req api.CreateAgentRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := validateName("name", req.Name); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	out, err := s.store.CreateAgent(r.Context(), orgID(r), req.Name)
	if err != nil {
		s.storeError(w, r, err, "agent")
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

// getDeclaration returns what the last commit a run read declares in
// .firstlume/config.yaml (spec 0003), without values.
func (s *Server) getDeclaration(w http.ResponseWriter, r *http.Request) {
	st, err := s.store.GetStack(r.Context(), orgID(r), id(r))
	if err != nil {
		s.storeError(w, r, err, "stack")
		return
	}
	out := &api.Declaration{}
	if st.Config != nil && st.Config.Declaration != nil {
		out = st.Config.Declaration
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) deleteAgent(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteAgent(r.Context(), orgID(r), id(r)); err != nil {
		s.storeError(w, r, err, "agent")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
