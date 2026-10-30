package server

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/bsteigleder/firstlume/internal/api"
	"github.com/bsteigleder/firstlume/internal/store"
)

func (s *Server) agentRoutes(r chi.Router) {
	r.Post("/claim", s.claimRun)
	r.With(s.requireID).Post("/runs/{id}/fail", s.reportFailure)
}

// claimRun gives the calling agent the next runnable phase, or 204 when idle.
func (s *Server) claimRun(w http.ResponseWriter, r *http.Request) {
	agent := agentFrom(r.Context())
	run, phase, err := s.store.ClaimRun(r.Context(), agent)
	if errors.Is(err, store.ErrNotFound) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	token, err := s.keys.IssueRunToken(run.ID, string(phase), s.cfg.RunTokenTTL)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.log.Info("run claimed", "run_id", run.ID, "phase", phase, "agent", agent.Name)
	writeJSON(w, http.StatusOK, api.Assignment{
		RunID:   run.ID,
		StackID: run.StackID,
		Phase:   phase,
		Token:   token,
	})
}

// reportFailure lets the agent fail a run whose executor could not start or died.
func (s *Server) reportFailure(w http.ResponseWriter, r *http.Request) {
	var req api.ReportFailureRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Phase != api.PhasePlan && req.Phase != api.PhaseApply {
		writeError(w, http.StatusUnprocessableEntity, "phase must be plan or apply")
		return
	}
	agent := agentFrom(r.Context())
	if err := s.store.FailPhase(r.Context(), agent.OrganizationID, id(r), req.Phase, req.Reason); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.log.Warn("run failed by agent", "run_id", id(r), "phase", req.Phase, "reason", req.Reason)
	w.WriteHeader(http.StatusNoContent)
}
