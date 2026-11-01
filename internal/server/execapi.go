package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/bsteigleder/firstlume/internal/api"
	"github.com/bsteigleder/firstlume/internal/store"
)

const (
	maxLogBody      = 1 << 20
	maxArtifactBody = 128 << 20
	// A plan's change list can be large (thousands of resources in run --all stacks).
	maxChangesBody = 32 << 20
)

var commitRe = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// artifactNames lists what executors may store, so the table can't be used as a blob dump.
var artifactNames = map[string]bool{"plan": true, api.VaultHashesArtifact: true}

func (s *Server) execRoutes(r chi.Router) {
	r.Get("/spec", s.execSpec)
	r.Post("/commit", s.execCommit)
	r.Post("/logs", s.execLogs)
	r.Post("/heartbeat", s.execHeartbeat)
	r.Put("/artifacts/{name}", s.execPutArtifact)
	r.Get("/artifacts/{name}", s.execGetArtifact)
	r.Post("/complete", s.execComplete)
	r.Put("/changes", s.execChanges)
	r.Post("/steps", s.execStep)
}

func phaseOf(r *http.Request) (string, api.Phase) {
	c := runClaimsFrom(r.Context())
	return c.RunID, api.Phase(c.Phase)
}

// execError reports a run that is no longer in the token's phase as 409, which
// tells the executor to stop: someone else owns the run now.
func (s *Server) execError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, store.ErrConflict) || errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusConflict, "run is no longer executing this phase")
		return
	}
	s.internalError(w, r, err)
}

func (s *Server) execSpec(w http.ResponseWriter, r *http.Request) {
	runID, phase := phaseOf(r)
	spec, err := s.store.ExecSpec(r.Context(), runID, phase)
	if err != nil {
		s.execError(w, r, err)
		return
	}
	// Only usable while the run executes this phase, whatever its expiry says.
	if spec.StateToken, err = s.keys.IssueStateToken(runID, string(phase), s.cfg.RunTokenTTL); err != nil {
		s.internalError(w, r, err)
		return
	}
	if spec.Vault, err = s.execVault(r.Context(), spec); err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, spec)
}

func (s *Server) execCommit(w http.ResponseWriter, r *http.Request) {
	runID, phase := phaseOf(r)
	var req api.CommitRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if phase != api.PhasePlan || !commitRe.MatchString(req.SHA) {
		writeError(w, http.StatusUnprocessableEntity, "commit can only be set during plan, as a full hex SHA")
		return
	}
	if err := s.store.SetCommit(r.Context(), runID, req); err != nil {
		s.execError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) execLogs(w http.ResponseWriter, r *http.Request) {
	runID, phase := phaseOf(r)
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxLogBody))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "log chunk too large")
		return
	}
	if len(body) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err := s.store.AppendLog(r.Context(), runID, phase, string(body)); err != nil {
		s.execError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) execHeartbeat(w http.ResponseWriter, r *http.Request) {
	runID, phase := phaseOf(r)
	if err := s.store.Heartbeat(r.Context(), runID, phase); err != nil {
		s.execError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) execPutArtifact(w http.ResponseWriter, r *http.Request) {
	runID, phase := phaseOf(r)
	name := chi.URLParam(r, "name")
	if !artifactNames[name] || phase != api.PhasePlan {
		writeError(w, http.StatusUnprocessableEntity, "unknown artifact or wrong phase")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxArtifactBody))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "artifact too large")
		return
	}
	if err := s.store.SaveArtifact(r.Context(), runID, phase, name, body); err != nil {
		s.execError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) execGetArtifact(w http.ResponseWriter, r *http.Request) {
	runID, phase := phaseOf(r)
	name := chi.URLParam(r, "name")
	if !artifactNames[name] {
		writeError(w, http.StatusNotFound, "unknown artifact")
		return
	}
	data, err := s.store.LoadArtifact(r.Context(), runID, phase, name)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "artifact not found")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	_, _ = w.Write(data)
}

func (s *Server) execChanges(w http.ResponseWriter, r *http.Request) {
	runID, phase := phaseOf(r)
	var changes []api.ResourceChange
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxChangesBody))
	if err := dec.Decode(&changes); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid changes: %v", err))
		return
	}
	if phase != api.PhasePlan {
		writeError(w, http.StatusUnprocessableEntity, "changes can only be set during plan")
		return
	}
	for _, c := range changes {
		switch c.Action {
		case api.ActionCreate, api.ActionUpdate, api.ActionReplace, api.ActionDelete:
		default:
			writeError(w, http.StatusUnprocessableEntity, fmt.Sprintf("unknown action %q", c.Action))
			return
		}
	}
	if err := s.store.SetChanges(r.Context(), runID, changes); err != nil {
		s.execError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) execStep(w http.ResponseWriter, r *http.Request) {
	runID, phase := phaseOf(r)
	var req api.StepRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	n := utf8.RuneCountInString(req.Name)
	validStatus := req.Status == api.StepRunning || req.Status == api.StepSucceeded || req.Status == api.StepFailed || req.Status == api.StepSkipped
	if n == 0 || n > 100 || (req.Kind != api.StepCore && req.Kind != api.StepHook) || !validStatus {
		writeError(w, http.StatusUnprocessableEntity, "a step needs a name (1-100 characters), a kind (core or hook) and a status")
		return
	}
	if err := s.store.RecordStep(r.Context(), runID, phase, req); err != nil {
		if errors.Is(err, store.ErrInvalid) {
			writeError(w, http.StatusUnprocessableEntity, "no running step with that name")
			return
		}
		s.execError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) execComplete(w http.ResponseWriter, r *http.Request) {
	runID, phase := phaseOf(r)
	var req api.CompleteRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	run, err := s.store.CompletePhase(r.Context(), runID, phase, req)
	if err != nil {
		s.execError(w, r, err)
		return
	}
	s.log.Info("run phase completed", "run_id", runID, "phase", phase, "success", req.Success, "status", run.Status)
	writeJSON(w, http.StatusOK, run)
}
