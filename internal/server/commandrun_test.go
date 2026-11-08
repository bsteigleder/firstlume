package server

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/bsteigleder/firstlume/internal/api"
	"github.com/bsteigleder/firstlume/internal/apiclient"
)

var takesCommands = api.ClaimRequest{AcceptsCommands: true}

func (h *harness) commandRun(stackID, cmd, access string) api.Run {
	h.t.Helper()
	var run api.Run
	h.call("POST", "/stacks/"+stackID+"/runs", api.CreateRunRequest{Type: api.RunCommand, Command: cmd, StateAccess: access},
		http.StatusCreated, &run)
	return run
}

// commandSpec claims the next phase as an agent that takes command runs.
func (h *harness) commandSpec(wantRun string) (*apiclient.Client, *api.ExecSpec) {
	h.t.Helper()
	asg := claim(h.t, h.agent, takesCommands)
	if asg == nil || asg.RunID != wantRun {
		h.t.Fatalf("expected to claim %s, got %+v", wantRun, asg)
	}
	exec := apiclient.New(h.url, asg.Token)
	spec, err := exec.Spec(context.Background())
	if err != nil {
		h.t.Fatal(err)
	}
	if asg.Type != api.RunCommand || spec.Command == "" {
		h.t.Fatalf("assignment %+v, spec %+v", asg, spec)
	}
	return exec, spec
}

// CAC1, CAC3: what can't be created.
func TestCommandRunValidation(t *testing.T) {
	h := newHarness(t)
	st := h.stack("", "network", "")
	path := "/stacks/" + st.ID + "/runs"
	for _, req := range []api.CreateRunRequest{
		{Type: api.RunCommand},
		{Type: api.RunCommand, Command: "ssh host"},
		{Type: api.RunCommand, Command: "terraform state list | grep x"},
		{Type: api.RunCommand, Command: "terraform state list", StateAccess: "admin"},
		{Type: api.RunPlan, Command: "terraform state list"},
		{Type: api.RunPlan, StateAccess: "read"},
	} {
		h.call("POST", path, req, http.StatusUnprocessableEntity, nil)
	}
	body := h.call("POST", path, api.CreateRunRequest{Type: api.RunCommand, Command: "terraform state rm a"}, http.StatusUnprocessableEntity, nil)
	if !strings.Contains(string(body), "Read only: choose “May change the state” to run state rm") {
		t.Fatalf("read-only refusal: %s", body)
	}

	// The dialog's check says the line and the reason without creating anything.
	var check api.CheckCommandResponse
	h.call("POST", "/stacks/"+st.ID+"/commands/check", api.CheckCommandRequest{Command: "terraform state list\nnc -z db 22"},
		http.StatusOK, &check)
	if check.OK || check.Error == nil || check.Error.Line != 2 || !strings.Contains(check.Error.Message, "nc isn’t allowed") {
		t.Fatalf("check: %+v", check)
	}
	h.call("POST", "/stacks/"+st.ID+"/commands/check", api.CheckCommandRequest{Command: "terraform state mv a b", StateAccess: "write"},
		http.StatusOK, &check)
	if !check.OK || !check.Writes || check.Lines != 1 {
		t.Fatalf("check: %+v", check)
	}
	var runs []api.Run
	h.call("GET", "/stacks/"+st.ID+"/runs", nil, http.StatusOK, &runs)
	if len(runs) != 0 {
		t.Fatalf("refused commands created runs: %+v", runs)
	}

	// Once a run read the stack's configuration, its engine is the one allowed.
	if _, err := h.pool.Exec(context.Background(), `UPDATE stacks SET config = '{"engine":"opentofu","engine_version":"1.9.0"}' WHERE id = $1`, st.ID); err != nil {
		t.Fatal(err)
	}
	body = h.call("POST", path, api.CreateRunRequest{Type: api.RunCommand, Command: "terraform state list"}, http.StatusUnprocessableEntity, nil)
	if !strings.Contains(string(body), "start with tofu") {
		t.Fatalf("engine: %s", body)
	}
	h.commandRun(st.ID, "tofu state list", "")
}

// CAC5: read access runs in the plan phase, without approval, and succeeds.
func TestCommandRunReadOnly(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	st := h.stack("", "network", "")
	h.settings("/stacks/"+st.ID, api.LevelSettings{RequireApproval: ptr(true)})
	run := h.commandRun(st.ID, "terraform state list", "")
	if run.Status != api.StatusQueued || run.StateAccess != "read" || run.Command != "terraform state list" {
		t.Fatalf("created: %+v", run)
	}

	// Agents from before command runs never get one.
	if a := claim(t, h.agent, api.ClaimRequest{}); a != nil {
		t.Fatalf("an agent that doesn't take commands claimed %+v", a)
	}
	exec, spec := h.commandSpec(run.ID)
	if spec.Phase != api.PhasePlan || spec.StateAccess != "read" || spec.StateToken == "" {
		t.Fatalf("spec: %+v", spec)
	}
	// The plan phase's token reads but never writes.
	root := h.stateURL(st.ID, "")
	h.wantState("POST", root, spec.StateToken, tfState(1, "lin-a", 1, "x"), http.StatusForbidden)
	must(t, exec.SetCommit(ctx, api.CommitRequest{SHA: "1111111111111111111111111111111111111111"}))
	done, err := exec.Complete(ctx, api.CompleteRequest{Success: true})
	must(t, err)
	if done.Status != api.StatusSucceeded || done.FinishedAt == nil || done.ExitCode != nil {
		t.Fatalf("done: %+v", done)
	}
}

// CAC6: write access waits for approval before anything runs, then runs in the
// apply phase and says which state versions it wrote.
func TestCommandRunWriteWithApproval(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	st := h.stack("", "network", "")
	h.settings("/stacks/"+st.ID, api.LevelSettings{RequireApproval: ptr(true), ConfirmDestructive: ptr(true)})
	ref := strings.Repeat("ab", 20)
	var run api.Run
	h.call("POST", "/stacks/"+st.ID+"/runs", api.CreateRunRequest{Type: api.RunCommand, Command: "terraform state mv a b",
		StateAccess: "write", Ref: ref}, http.StatusCreated, &run)
	if run.Status != api.StatusAwaitingApproval || run.CommitSHA != ref {
		t.Fatalf("created: %+v", run)
	}
	if a := claim(t, h.agent, takesCommands); a != nil {
		t.Fatalf("claimed before approval: %+v", a)
	}
	// A plan of the same stack waits behind it.
	plan := h.run(st.ID, api.RunPlan)
	if a := claim(t, h.agent, takesCommands); a != nil {
		t.Fatalf("claimed while the command waits for approval: %+v", a)
	}

	// No plan to confirm: approving needs no acknowledged resources (R10).
	var approved api.Run
	h.call("POST", "/runs/"+run.ID+"/approve", nil, http.StatusOK, &approved)
	if approved.Status != api.StatusApplyQueued || approved.ApprovedBy == "" {
		t.Fatalf("approved: %+v", approved)
	}
	exec, spec := h.commandSpec(run.ID)
	if spec.Phase != api.PhaseApply || spec.CommitSHA != ref || spec.StateAccess != "write" {
		t.Fatalf("spec: %+v", spec)
	}
	// Variables come as for a plan, since there was none.
	if spec.Variables == nil {
		t.Fatalf("no variables: %+v", spec)
	}
	must(t, exec.SetCommit(ctx, api.CommitRequest{SHA: ref, Config: &api.StackConfig{Engine: "terraform", EngineVersion: "1.15.6"}}))
	root := h.stateURL(st.ID, "")
	h.wantState("POST", root, spec.StateToken, tfState(1, "lin-a", 1, "x"), http.StatusOK)
	h.wantState("POST", root, spec.StateToken, tfState(2, "lin-a", 1, "y"), http.StatusOK)
	done, err := exec.Complete(ctx, api.CompleteRequest{Success: true})
	must(t, err)
	if done.Status != api.StatusSucceeded {
		t.Fatalf("done: %+v", done)
	}
	var read api.Run
	h.call("GET", "/runs/"+run.ID, nil, http.StatusOK, &read)
	if len(read.StateWrites) != 1 || read.StateWrites[0].Key != "" || read.StateWrites[0].Version != 2 {
		t.Fatalf("state writes: %+v", read.StateWrites)
	}

	// The plan waiting behind it runs now.
	h.spec(plan.ID)
}

// CAC5, CAC7, CAC8: write access without approval runs at once in the apply
// phase; the queue holds other runs; a failed line is recorded; a stop cancels.
func TestCommandRunWriteWithoutApproval(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	st := h.stack("", "network", "")
	h.settings("/stacks/"+st.ID, api.LevelSettings{RequireApproval: ptr(false)})

	// Behind a plan, it waits its turn, queued.
	plan := h.run(st.ID, api.RunPlan)
	run := h.commandRun(st.ID, "terraform import a b\nterraform state list", "write")
	if run.Status != api.StatusQueued {
		t.Fatalf("created behind a plan: %+v", run)
	}
	planExec, _ := h.spec(plan.ID)
	if a := claim(t, h.agent, takesCommands); a != nil {
		t.Fatalf("claimed while a plan runs: %+v", a)
	}
	_, err := planExec.Complete(ctx, api.CompleteRequest{Success: true})
	must(t, err)

	exec, spec := h.commandSpec(run.ID)
	if spec.Phase != api.PhaseApply {
		t.Fatalf("spec: %+v", spec)
	}
	// While it runs, nothing else of the stack starts.
	other := h.run(st.ID, api.RunPlan)
	if a := claim(t, h.agent, takesCommands); a != nil {
		t.Fatalf("claimed while the command runs: %+v", a)
	}
	code, line := 1, 1
	done, err := exec.Complete(ctx, api.CompleteRequest{Success: false, Error: "Line 1 exited with code 1", ExitCode: &code, FailedLine: &line})
	must(t, err)
	if done.Status != api.StatusFailed || done.ExitCode == nil || *done.ExitCode != 1 || done.FailedLine == nil || *done.FailedLine != 1 {
		t.Fatalf("failed: %+v", done)
	}
	h.cancel(other.ID)

	// A stop that interrupted it ends it canceled.
	run = h.commandRun(st.ID, "terraform state list", "read")
	exec, _ = h.commandSpec(run.ID)
	h.call("POST", "/runs/"+run.ID+"/cancel", nil, http.StatusOK, nil)
	done, err = exec.Complete(ctx, api.CompleteRequest{Success: false, Error: "stopped by admin"})
	must(t, err)
	if done.Status != api.StatusCanceled || done.ExitCode != nil {
		t.Fatalf("stopped: %+v", done)
	}

	// Every line ran before the stop reached it: what it did is done, as an
	// apply that finished is applied.
	run = h.commandRun(st.ID, "terraform state list", "read")
	exec, _ = h.commandSpec(run.ID)
	h.call("POST", "/runs/"+run.ID+"/cancel", nil, http.StatusOK, nil)
	done, err = exec.Complete(ctx, api.CompleteRequest{Success: true})
	must(t, err)
	if done.Status != api.StatusSucceeded {
		t.Fatalf("finished before the stop: %+v", done)
	}
}

// R9: a write that needs approval, created behind another run, waits for
// approval only once its turn comes.
func TestCommandRunWaitsForApprovalOnItsTurn(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	st := h.stack("", "network", "")
	h.settings("/stacks/"+st.ID, api.LevelSettings{RequireApproval: ptr(true)})
	plan := h.run(st.ID, api.RunPlan)
	run := h.commandRun(st.ID, "terraform state rm a", "write")
	if run.Status != api.StatusQueued {
		t.Fatalf("created: %+v", run)
	}
	exec, _ := h.spec(plan.ID)
	_, err := exec.Complete(ctx, api.CompleteRequest{Success: true})
	must(t, err)
	if a := claim(t, h.agent, takesCommands); a != nil {
		t.Fatalf("claimed a command that needs approval: %+v", a)
	}
	var got api.Run
	h.call("GET", "/runs/"+run.ID, nil, http.StatusOK, &got)
	if got.Status != api.StatusAwaitingApproval {
		t.Fatalf("on its turn: %+v", got)
	}
	h.call("POST", "/runs/"+run.ID+"/discard", nil, http.StatusOK, &got)
	if got.Status != api.StatusDiscarded {
		t.Fatalf("discarded: %+v", got)
	}
}
