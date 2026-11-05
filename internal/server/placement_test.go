package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/bsteigleder/firstlume/internal/api"
	"github.com/bsteigleder/firstlume/internal/apiclient"
)

var accepts = api.ClaimRequest{AcceptsPlacement: true}

func raw(s string) json.RawMessage { return json.RawMessage(s) }

// cancel frees a stack for the next run of a test.
func (h *harness) cancel(runID string) {
	h.t.Helper()
	if _, err := h.pool.Exec(context.Background(), `UPDATE runs SET status = 'canceled' WHERE id = $1`, runID); err != nil {
		h.t.Fatal(err)
	}
}

// newAgent registers one more agent and returns its client.
func (h *harness) newAgent(name string, p api.Placement) (*apiclient.Client, api.Agent) {
	h.t.Helper()
	var out api.CreateAgentResponse
	h.call("POST", "/agents", api.CreateAgentRequest{Name: name, Placement: p}, http.StatusCreated, &out)
	return apiclient.New(h.url, out.Token), out.Agent
}

func claim(t *testing.T, c *apiclient.Client, req api.ClaimRequest) *api.Assignment {
	t.Helper()
	a, err := c.Claim(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// CAC1.1, CAC1.2, CAC1.3, CAC1.11: each field inherits on its own.
func TestPlacementInheritance(t *testing.T) {
	h := newHarness(t)
	f := h.folder("", "prod")
	st := h.stack(f.ID, "cluster", "")

	first := h.run(st.ID, api.RunPlan)
	if first.Placement != nil {
		t.Fatalf("no placement expected: %+v", first.Placement)
	}
	if a := claim(t, h.agent, accepts); !a.Placement.IsZero() {
		t.Fatalf("assignment placement = %+v", a.Placement)
	}
	h.cancel(first.ID)

	h.settings("", api.LevelSettings{Placement: api.Placement{PodAntiAffinity: raw(`{}`), NodeSelector: raw(`{"zone":"org"}`)}})
	h.settings("/folders/"+f.ID, api.LevelSettings{Placement: api.Placement{NodeSelector: raw(`{"zone":"east"}`)}})
	var got api.SettingsResponse
	h.call("GET", "/stacks/"+st.ID+"/settings", nil, http.StatusOK, &got)
	if eff := got.Effective.Placement; string(eff.NodeSelector.Value) != `{"zone":"east"}` || eff.NodeSelector.Origin.ID != f.ID ||
		eff.PodAntiAffinity.Origin.Kind != api.LevelOrganization || eff.Tolerations != nil {
		t.Fatalf("effective = %+v", eff)
	}

	run := h.run(st.ID, api.RunPlan)
	if run.Placement == nil || run.Placement.NodeSelector == nil || run.Placement.NodeSelector.Origin.ID != f.ID {
		t.Fatalf("run placement = %+v", run.Placement)
	}
	a := claim(t, h.agent, accepts)
	if a == nil || a.RunID != run.ID || string(a.Placement.NodeSelector) != `{"zone":"east"}` || string(a.Placement.PodAntiAffinity) != `{}` {
		t.Fatalf("assignment = %+v", a)
	}
	h.cancel(run.ID)

	// The stack overrides one field; an empty one cancels what's inherited.
	h.settings("/stacks/"+st.ID, api.LevelSettings{Placement: api.Placement{NodeSelector: raw(`{}`)}})
	h.call("GET", "/stacks/"+st.ID+"/settings", nil, http.StatusOK, &got)
	if string(got.Level.Placement.NodeSelector) != `{}` || got.Effective.Placement.NodeSelector.Origin.Kind != api.LevelStack {
		t.Fatalf("settings = %+v", got)
	}
	// Clearing a level keeps nothing behind.
	h.settings("/stacks/"+st.ID, api.LevelSettings{})
	got = api.SettingsResponse{}
	h.call("GET", "/stacks/"+st.ID+"/settings", nil, http.StatusOK, &got)
	if !got.Level.Placement.IsZero() || got.Effective.Placement.NodeSelector.Origin.ID != f.ID {
		t.Fatalf("after clearing = %+v", got)
	}
}

// CAC1.4: what is invalid is refused, naming the field.
func TestPlacementValidation(t *testing.T) {
	h := newHarness(t)
	for _, c := range []struct {
		p    api.Placement
		want string
	}{
		{api.Placement{Tolerations: raw(`[{"key":"a","operator":"Maybe"}]`)}, "Tolerations"},
		{api.Placement{PodAntiAffinity: raw(`{"preferredDuringSchedulingIgnoredDuringExecution":[{"weight":1,"podAffinityTerm":{}}]}`)}, "topologyKey"},
		{api.Placement{NodeAffinity: raw(`{"typo":1}`)}, "Node affinity"},
	} {
		body := h.call("PUT", "/settings", api.LevelSettings{Placement: c.p, Variables: []api.Variable{}}, http.StatusUnprocessableEntity, nil)
		if !strings.Contains(string(body), c.want) {
			t.Errorf("settings: %s, want %q", body, c.want)
		}
		body = h.call("POST", "/agents", api.CreateAgentRequest{Name: "bad", Placement: c.p}, http.StatusUnprocessableEntity, nil)
		if !strings.Contains(string(body), c.want) {
			t.Errorf("agent: %s, want %q", body, c.want)
		}
	}
	_, a := h.newAgent("fine", api.Placement{})
	h.call("PUT", "/agents/"+a.ID+"/placement", api.Placement{Tolerations: raw(`[{"key":"a","operator":"Maybe"}]`)}, http.StatusUnprocessableEntity, nil)
}

// CAC1.5, CAC1.6: recorded when the run is created, whatever its origin, and the apply uses it.
func TestPlacementRecordedOnTheRun(t *testing.T) {
	h := newHarness(t)
	st := h.stack("", "cluster", "")
	h.settings("/stacks/"+st.ID, api.LevelSettings{Placement: api.Placement{NodeSelector: raw(`{"zone":"east"}`)}, RequireApproval: ptr(true)})

	run := h.run(st.ID, api.RunApply)
	ctx := context.Background()
	asg := claim(t, h.agent, accepts)
	if asg == nil || asg.RunID != run.ID {
		t.Fatalf("assignment = %+v", asg)
	}
	exec := apiclient.New(h.url, asg.Token)
	if _, err := exec.Spec(ctx); err != nil {
		t.Fatal(err)
	}
	must(t, exec.SetCommit(ctx, api.CommitRequest{SHA: strings.Repeat("a", 40), Config: &api.StackConfig{Version: 1, Engine: "terraform", EngineVersion: "1.15.6"}}))
	must(t, exec.PutArtifact(ctx, "plan", []byte("binary-plan")))
	if _, err := exec.Complete(ctx, api.CompleteRequest{Success: true, HasChanges: true, PlanSummary: &api.PlanSummary{Add: 1}}); err != nil {
		t.Fatal(err)
	}

	// Changing the setting now doesn't touch this run.
	h.settings("/stacks/"+st.ID, api.LevelSettings{Placement: api.Placement{NodeSelector: raw(`{"zone":"west"}`)}, RequireApproval: ptr(true)})
	h.call("POST", "/runs/"+run.ID+"/approve", nil, http.StatusOK, nil)
	a := claim(t, h.agent, accepts)
	if a == nil || a.Phase != api.PhaseApply || string(a.Placement.NodeSelector) != `{"zone":"east"}` {
		t.Fatalf("apply = %+v", a)
	}
	h.cancel(run.ID)

	// Drift.
	h.settings("/stacks/"+st.ID, api.LevelSettings{Placement: api.Placement{NodeSelector: raw(`{"zone":"west"}`)}, DriftIntervalMinutes: ptr(15)})
	if _, err := h.store.ScheduleDriftRuns(context.Background()); err != nil {
		t.Fatal(err)
	}
	a = claim(t, h.agent, accepts)
	if a == nil || string(a.Placement.NodeSelector) != `{"zone":"west"}` {
		t.Fatalf("drift = %+v", a)
	}
	h.cancel(a.RunID)

}

// CAC1.6: a run from a push records it too.
func TestPlacementOfPushRun(t *testing.T) {
	h := newTriggerHarness(t)
	st := h.gitStack("cluster", "infra", api.GitOnPushPlan)
	h.settings("/stacks/"+st.ID, api.LevelSettings{GitOnPush: ptr(api.GitOnPushPlan), Placement: api.Placement{NodeSelector: raw(`{"zone":"west"}`)}})
	h.gitea.mu.Lock()
	h.gitea.compares = map[string][][]string{sha("a") + "..." + sha("b"): {{"infra/main.tf"}}}
	h.gitea.mu.Unlock()
	h.push(sha("a"), sha("b"), 1)
	runs := h.runsOf(st.ID)
	if len(runs) != 1 || runs[0].Placement == nil || string(runs[0].Placement.NodeSelector.Value) != `{"zone":"west"}` {
		t.Fatalf("runs = %+v", runs)
	}
}

// CAC1.7, CAC1.12, CAC1.13, CAC1.14: who takes what.
func TestPlacementAgents(t *testing.T) {
	h := newHarness(t)
	st := h.stack("", "cluster", "")
	refuses := api.ClaimRequest{}

	// An agent with nothing set, not accepting: only runs without placement.
	h.settings("/stacks/"+st.ID, api.LevelSettings{Placement: api.Placement{NodeSelector: raw(`{"zone":"east"}`)}})
	run := h.run(st.ID, api.RunPlan)
	if a := claim(t, h.agent, refuses); a != nil {
		t.Fatalf("an agent that doesn't accept took %+v", a)
	}
	withPlacement, created := h.newAgent("east", api.Placement{Tolerations: raw(`[{"operator":"Exists"}]`)})
	if string(created.Placement.Tolerations) != `[{"operator":"Exists"}]` {
		t.Fatalf("created = %+v", created)
	}
	a := claim(t, withPlacement, accepts)
	if a == nil || a.RunID != run.ID || string(a.Placement.NodeSelector) != `{"zone":"east"}` || string(a.Placement.Tolerations) != `[{"operator":"Exists"}]` {
		t.Fatalf("assignment = %+v", a)
	}
	h.cancel(run.ID)

	// A field on the stack wins over the agent's.
	h.settings("/stacks/"+st.ID, api.LevelSettings{Placement: api.Placement{Tolerations: raw(`[]`)}})
	run = h.run(st.ID, api.RunPlan)
	if a = claim(t, withPlacement, accepts); a == nil || string(a.Placement.Tolerations) != `[]` {
		t.Fatalf("assignment = %+v", a)
	}
	h.cancel(run.ID)

	// A run with nothing set still gets the agent's own placement.
	h.settings("/stacks/"+st.ID, api.LevelSettings{})
	run = h.run(st.ID, api.RunPlan)
	if a = claim(t, withPlacement, accepts); a == nil || a.RunID != run.ID || string(a.Placement.Tolerations) != `[{"operator":"Exists"}]` {
		t.Fatalf("assignment = %+v", a)
	}
	h.cancel(run.ID)

	// With a placement of its own and no permission, it takes nothing, not even
	// a run without placement. Turning the option on brings it back.
	run = h.run(st.ID, api.RunPlan)
	if a = claim(t, withPlacement, refuses); a != nil {
		t.Fatalf("a stopped agent took %+v", a)
	}
	// Editing applies from the next phase.
	var edited api.Agent
	h.call("PUT", "/agents/"+created.ID+"/placement", api.Placement{NodeSelector: raw(`{"zone":"new"}`)}, http.StatusOK, &edited)
	a = claim(t, withPlacement, accepts)
	if a == nil || a.RunID != run.ID || string(a.Placement.NodeSelector) != `{"zone":"new"}` || len(a.Placement.Tolerations) != 0 {
		t.Fatalf("after editing = %+v", a)
	}
	h.cancel(run.ID)

	// The run says what its phase used, whatever changes on the agent later.
	h.settings("/stacks/"+st.ID, api.LevelSettings{Placement: api.Placement{PodAffinity: raw(`{}`)}})
	run = h.run(st.ID, api.RunPlan)
	report := api.ClaimRequest{AcceptsPlacement: true, InstallPlacement: api.Placement{NodeSelector: raw(`{"zone":"install"}`), Tolerations: raw(`[{"operator":"Exists"}]`)}}
	h.call("PUT", "/agents/"+created.ID+"/placement", api.Placement{Tolerations: raw(`[]`)}, http.StatusOK, nil)
	if a = claim(t, withPlacement, report); a == nil || a.RunID != run.ID {
		t.Fatalf("assignment = %+v", a)
	}
	h.call("PUT", "/agents/"+created.ID+"/placement", api.Placement{}, http.StatusOK, nil)
	var read api.Run
	h.call("GET", "/runs/"+run.ID, nil, http.StatusOK, &read)
	used := read.PlacementUsed
	if used == nil || used.PodAffinity.Origin.Kind != api.LevelStack || used.Tolerations.Origin.Kind != api.LevelAgent || used.Tolerations.Origin.Name != "east" ||
		string(used.Tolerations.Value) != `[]` || used.NodeSelector.Origin.Kind != api.LevelInstallation || used.PodAntiAffinity != nil {
		t.Fatalf("used = %+v", used)
	}
	h.cancel(run.ID)

	// What agents report shows up.
	var agents []api.Agent
	h.call("GET", "/agents", nil, http.StatusOK, &agents)
	seen := map[string]api.Agent{}
	for _, ag := range agents {
		seen[ag.Name] = ag
	}
	east := seen["east"]
	if east.AcceptsPlacement == nil || !*east.AcceptsPlacement || east.InstallPlacement == nil || string(east.InstallPlacement.NodeSelector) != `{"zone":"install"}` {
		t.Fatalf("east = %+v", east)
	}
	if test := seen["test"]; test.AcceptsPlacement == nil || *test.AcceptsPlacement {
		t.Fatalf("test = %+v", test)
	}
}

// An agent from before the spec sends no body, and counts as not accepting.
func TestClaimWithoutBody(t *testing.T) {
	h := newHarness(t)
	st := h.stack("", "cluster", "")
	h.settings("/stacks/"+st.ID, api.LevelSettings{Placement: api.Placement{NodeSelector: raw(`{"zone":"east"}`)}})
	h.run(st.ID, api.RunPlan)
	req, _ := http.NewRequest("POST", h.url+"/agent/v1/claim", nil)
	req.Header.Set("Authorization", "Bearer "+h.agentToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

// CAC3.3: moving a stack lists each field that changes.
func TestMoveListsPlacement(t *testing.T) {
	h := newHarness(t)
	east := h.folder("", "east")
	west := h.folder("", "west")
	st := h.stack(east.ID, "cluster", "")
	h.settings("/folders/"+east.ID, api.LevelSettings{Placement: api.Placement{NodeSelector: raw(`{"zone":"east"}`)}})
	h.settings("/folders/"+west.ID, api.LevelSettings{Placement: api.Placement{NodeSelector: raw(`{"zone":"west"}`), Tolerations: raw(`[]`)}})
	var preview api.MovePreview
	h.call("GET", "/stacks/"+st.ID+"/move?folder_id="+west.ID, nil, http.StatusOK, &preview)
	changes := map[string]api.MoveChange{}
	for _, c := range preview.Changes {
		changes[c.Setting] = c
	}
	sel, tol := changes["placement_node_selector"], changes["placement_tolerations"]
	if sel.Before == nil || sel.Before.Value != `{"zone": "east"}` || sel.After == nil || sel.After.Origin.ID != west.ID {
		t.Fatalf("node selector = %+v", sel)
	}
	if tol.Before != nil || tol.After == nil {
		t.Fatalf("tolerations = %+v", tol)
	}
}
