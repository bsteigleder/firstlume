package server

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/bsteigleder/firstlume/internal/api"
)

// adminIdentity is recorded as the actor until user accounts exist.
const adminIdentity = "admin"

func (s *Server) userRoutes(r chi.Router) {
	r.Get("/templates", s.listTemplates)
	r.Post("/templates", s.createTemplate)
	r.With(s.requireID).Get("/templates/{id}", s.getTemplate)
	r.With(s.requireID).Delete("/templates/{id}", s.deleteTemplate)

	r.Get("/environments", s.listEnvironments)
	r.Post("/environments", s.createEnvironment)
	r.With(s.requireID).Get("/environments/{id}", s.getEnvironment)
	r.With(s.requireID).Delete("/environments/{id}", s.deleteEnvironment)
	r.With(s.requireID).Get("/environments/{id}/variables", s.listVariables)
	r.With(s.requireID).Put("/environments/{id}/variables", s.putVariables)
	r.With(s.requireID).Get("/environments/{id}/runs", s.listRuns)
	r.With(s.requireID).Post("/environments/{id}/runs", s.createRun)

	r.With(s.requireID).Get("/runs/{id}", s.getRun)
	r.With(s.requireID).Post("/runs/{id}/approve", s.approveRun)
	r.With(s.requireID).Post("/runs/{id}/discard", s.discardRun)
	r.With(s.requireID).Post("/runs/{id}/cancel", s.cancelRun)
	r.With(s.requireID).Get("/runs/{id}/logs", s.runLogs)

	r.Get("/agents", s.listAgents)
	r.Post("/agents", s.createAgent)
	r.With(s.requireID).Delete("/agents/{id}", s.deleteAgent)
}

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

func (s *Server) listTemplates(w http.ResponseWriter, r *http.Request) {
	out, err := s.store.ListTemplates(r.Context(), s.cfg.OrganizationID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) createTemplate(w http.ResponseWriter, r *http.Request) {
	var req api.CreateTemplateRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := validateTemplate(&req); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	t, err := s.store.CreateTemplate(r.Context(), s.cfg.OrganizationID, req)
	if err != nil {
		s.storeError(w, r, err, "template")
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

func (s *Server) getTemplate(w http.ResponseWriter, r *http.Request) {
	t, err := s.store.GetTemplate(r.Context(), s.cfg.OrganizationID, id(r))
	if err != nil {
		s.storeError(w, r, err, "template")
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) deleteTemplate(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteTemplate(r.Context(), s.cfg.OrganizationID, id(r)); err != nil {
		s.storeError(w, r, err, "template")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listEnvironments(w http.ResponseWriter, r *http.Request) {
	out, err := s.store.ListEnvironments(r.Context(), s.cfg.OrganizationID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) createEnvironment(w http.ResponseWriter, r *http.Request) {
	var req api.CreateEnvironmentRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := validateName("name", req.Name); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if !validUUID(req.TemplateID) {
		writeError(w, http.StatusUnprocessableEntity, "template_id must be a template id")
		return
	}
	if req.Ref != "" {
		if err := validateRef(req.Ref); err != nil {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
	}
	e, err := s.store.CreateEnvironment(r.Context(), s.cfg.OrganizationID, req)
	if err != nil {
		s.storeError(w, r, err, "environment")
		return
	}
	writeJSON(w, http.StatusCreated, e)
}

func (s *Server) getEnvironment(w http.ResponseWriter, r *http.Request) {
	e, err := s.store.GetEnvironment(r.Context(), s.cfg.OrganizationID, id(r))
	if err != nil {
		s.storeError(w, r, err, "environment")
		return
	}
	writeJSON(w, http.StatusOK, e)
}

func (s *Server) deleteEnvironment(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteEnvironment(r.Context(), s.cfg.OrganizationID, id(r)); err != nil {
		s.storeError(w, r, err, "environment")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listVariables(w http.ResponseWriter, r *http.Request) {
	out, err := s.store.ListVariables(r.Context(), s.cfg.OrganizationID, id(r))
	if err != nil {
		s.storeError(w, r, err, "environment")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) putVariables(w http.ResponseWriter, r *http.Request) {
	var req api.PutVariablesRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := validateVariables(req.Variables); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if err := s.store.PutVariables(r.Context(), s.cfg.OrganizationID, id(r), req.Variables); err != nil {
		s.storeError(w, r, err, "variables (a new sensitive variable needs a value)")
		return
	}
	s.listVariables(w, r)
}

func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 && v <= 200 {
		limit = v
	}
	if _, err := s.store.GetEnvironment(r.Context(), s.cfg.OrganizationID, id(r)); err != nil {
		s.storeError(w, r, err, "environment")
		return
	}
	out, err := s.store.ListRuns(r.Context(), s.cfg.OrganizationID, id(r), limit)
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
		writeError(w, http.StatusUnprocessableEntity, "type must be plan, apply or destroy")
		return
	}
	if req.Ref != "" {
		if err := validateRef(req.Ref); err != nil {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
	}
	run, err := s.store.CreateRun(r.Context(), s.cfg.OrganizationID, id(r), req.Type, req.Ref, adminIdentity)
	if err != nil {
		s.storeError(w, r, err, "environment")
		return
	}
	writeJSON(w, http.StatusCreated, run)
}

func (s *Server) getRun(w http.ResponseWriter, r *http.Request) {
	run, err := s.store.GetRun(r.Context(), s.cfg.OrganizationID, id(r))
	if err != nil {
		s.storeError(w, r, err, "run")
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (s *Server) approveRun(w http.ResponseWriter, r *http.Request) {
	run, err := s.store.ApproveRun(r.Context(), s.cfg.OrganizationID, id(r), adminIdentity)
	if err != nil {
		s.storeError(w, r, err, "run")
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (s *Server) discardRun(w http.ResponseWriter, r *http.Request) {
	run, err := s.store.DiscardRun(r.Context(), s.cfg.OrganizationID, id(r))
	if err != nil {
		s.storeError(w, r, err, "run")
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (s *Server) cancelRun(w http.ResponseWriter, r *http.Request) {
	run, err := s.store.CancelRun(r.Context(), s.cfg.OrganizationID, id(r))
	if err != nil {
		s.storeError(w, r, err, "run")
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (s *Server) listAgents(w http.ResponseWriter, r *http.Request) {
	out, err := s.store.ListAgents(r.Context(), s.cfg.OrganizationID)
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
	out, err := s.store.CreateAgent(r.Context(), s.cfg.OrganizationID, req.Name)
	if err != nil {
		s.storeError(w, r, err, "agent")
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) deleteAgent(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteAgent(r.Context(), s.cfg.OrganizationID, id(r)); err != nil {
		s.storeError(w, r, err, "agent")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
