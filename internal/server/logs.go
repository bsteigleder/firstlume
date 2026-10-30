package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bsteigleder/firstlume/internal/api"
)

const logPageSize = 500

// runLogs returns the full log as text, or streams it as Server-Sent Events
// when called with ?follow=true or Accept: text/event-stream.
//
// Streaming polls the database rather than using LISTEN/NOTIFY, so it works
// unchanged behind any number of API replicas.
func (s *Server) runLogs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	runID := id(r)
	org := orgID(r)
	if _, err := s.store.GetRun(ctx, org, runID); err != nil {
		s.storeError(w, r, err, "run")
		return
	}

	follow := r.URL.Query().Get("follow") == "true" || strings.Contains(r.Header.Get("Accept"), "text/event-stream")
	if !follow {
		s.plainLogs(w, r, runID)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	var cursor int64
	if v, err := strconv.ParseInt(r.Header.Get("Last-Event-ID"), 10, 64); err == nil {
		cursor = v
	}
	ticker := time.NewTicker(s.cfg.LogPollInterval)
	defer ticker.Stop()
	for {
		// Read the status before the logs: if the run was already finished, every
		// chunk it will ever have is visible to the read that follows.
		run, err := s.store.GetRun(ctx, org, runID)
		if err != nil {
			return
		}
		for {
			chunks, err := s.store.ListLogs(ctx, runID, cursor, logPageSize)
			if err != nil {
				return
			}
			for _, c := range chunks {
				data, _ := json.Marshal(map[string]string{"phase": string(c.Phase), "step": c.Step, "data": c.Data})
				fmt.Fprintf(w, "id: %d\nevent: log\ndata: %s\n\n", c.ID, data)
				cursor = c.ID
			}
			if len(chunks) < logPageSize {
				break
			}
		}
		if waitingForInput(run.Status) || run.Status.IsTerminal() {
			data, _ := json.Marshal(map[string]string{"status": string(run.Status)})
			fmt.Fprintf(w, "event: end\ndata: %s\n\n", data)
			flusher.Flush()
			return
		}
		fmt.Fprint(w, ": keepalive\n\n")
		flusher.Flush()

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// waitingForInput reports statuses where no executor is producing logs until a user acts.
func waitingForInput(s api.RunStatus) bool {
	return s == api.StatusAwaitingApproval
}

// plainLogs writes the whole log, or one step's with ?step=NAME (and optionally &phase=plan|apply).
func (s *Server) plainLogs(w http.ResponseWriter, r *http.Request, runID string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	step, phase := r.URL.Query().Get("step"), api.Phase(r.URL.Query().Get("phase"))
	var cursor int64
	for {
		chunks, err := s.store.ListLogs(r.Context(), runID, cursor, logPageSize)
		if err != nil {
			s.log.Error("listing logs", "run_id", runID, "err", err)
			return
		}
		for _, c := range chunks {
			cursor = c.ID
			if (step != "" && c.Step != step) || (phase != "" && c.Phase != phase) {
				continue
			}
			_, _ = w.Write([]byte(c.Data))
		}
		if len(chunks) < logPageSize {
			return
		}
	}
}
