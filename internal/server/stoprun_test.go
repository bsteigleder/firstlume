package server

import (
	"context"
	"net/http"
	"testing"

	"github.com/bsteigleder/firstlume/internal/api"
)

// Stopping a run that is executing (canvas boards 66-67).
func TestStopRun(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	st := h.stack("", "network", "")

	// A queued run is canceled at once, and records who did it.
	run := h.run(st.ID, api.RunPlan)
	queued := h.run(st.ID, api.RunPlan)
	h.call("POST", "/runs/"+queued.ID+"/cancel", nil, http.StatusOK, &queued)
	if queued.Status != api.StatusCanceled || queued.StopRequestedBy == "" || queued.StopRequestedAt == nil {
		t.Fatalf("queued cancel: %+v", queued)
	}

	// A plan executing keeps running until the executor hears of it.
	exec, _ := h.spec(run.ID)
	if hb, err := exec.Heartbeat(ctx); err != nil || hb.StopRequested {
		t.Fatalf("heartbeat before the stop: %+v, %v", hb, err)
	}
	var stopping api.Run
	h.call("POST", "/runs/"+run.ID+"/cancel", nil, http.StatusOK, &stopping)
	if stopping.Status != api.StatusPlanning || stopping.StopRequestedAt == nil || stopping.FinishedAt != nil {
		t.Fatalf("stopping: %+v", stopping)
	}
	// Asking again keeps the first request.
	var again api.Run
	h.call("POST", "/runs/"+run.ID+"/cancel", nil, http.StatusOK, &again)
	if !again.StopRequestedAt.Equal(*stopping.StopRequestedAt) {
		t.Fatalf("second request moved the time: %v, %v", again.StopRequestedAt, stopping.StopRequestedAt)
	}
	hb, err := exec.Heartbeat(ctx)
	must(t, err)
	if !hb.StopRequested || hb.StopRequestedBy != stopping.StopRequestedBy {
		t.Fatalf("heartbeat: %+v", hb)
	}
	// Even a plan that finished goes no further.
	done, err := exec.Complete(ctx, api.CompleteRequest{Success: true, HasChanges: true})
	must(t, err)
	if done.Status != api.StatusCanceled || done.FinishedAt == nil {
		t.Fatalf("stopped plan: %+v", done)
	}

	// An apply interrupted ends canceled, not failed.
	exec, _ = h.applyPhase(st.ID)
	var applying api.Run
	applyRun := h.activeRun(st.ID)
	h.call("POST", "/runs/"+applyRun+"/cancel", nil, http.StatusOK, &applying)
	if applying.Status != api.StatusApplying || applying.StopRequestedAt == nil {
		t.Fatalf("stopping apply: %+v", applying)
	}
	done, err = exec.Complete(ctx, api.CompleteRequest{Success: false, Error: "stopped by admin"})
	must(t, err)
	if done.Status != api.StatusCanceled || done.Error != "stopped by admin" {
		t.Fatalf("stopped apply: %+v", done)
	}

	// An apply that finished every change before stopping is applied.
	exec, _ = h.applyPhase(st.ID)
	h.call("POST", "/runs/"+h.activeRun(st.ID)+"/cancel", nil, http.StatusOK, nil)
	done, err = exec.Complete(ctx, api.CompleteRequest{Success: true})
	must(t, err)
	if done.Status != api.StatusApplied {
		t.Fatalf("apply finished before stopping: %s", done.Status)
	}

	// Awaiting approval is discarded, not stopped; a finished run can't be.
	h.settings("/stacks/"+st.ID, api.LevelSettings{RequireApproval: ptr(true)})
	held := h.run(st.ID, api.RunApply)
	exec, _ = h.spec(held.ID)
	_, err = exec.Complete(ctx, api.CompleteRequest{Success: true, HasChanges: true})
	must(t, err)
	h.call("POST", "/runs/"+held.ID+"/cancel", nil, http.StatusConflict, nil)
	h.call("POST", "/runs/"+done.ID+"/cancel", nil, http.StatusConflict, nil)
}

// activeRun is the id of the stack's run that is executing.
func (h *harness) activeRun(stackID string) string {
	h.t.Helper()
	var id string
	if err := h.pool.QueryRow(context.Background(),
		`SELECT id FROM runs WHERE stack_id = $1 AND status IN ('planning', 'applying')`, stackID).Scan(&id); err != nil {
		h.t.Fatal(err)
	}
	return id
}
