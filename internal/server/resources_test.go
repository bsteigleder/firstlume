package server

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/bsteigleder/firstlume/internal/api"
)

// resourcesState has a module, count and for_each instances, a data source
// and a secret attribute value that must never reach the API.
const resourcesState = `{"version":4,"terraform_version":"1.15.8","serial":3,"lineage":"lin-res","outputs":{},"resources":[
 {"mode":"data","type":"aws_caller_identity","name":"me","provider":"provider[\"registry.terraform.io/hashicorp/aws\"]",
  "instances":[{"attributes":{"account_id":"123"}}]},
 {"mode":"managed","type":"aws_vpc","name":"main","provider":"provider[\"registry.terraform.io/hashicorp/aws\"]",
  "instances":[{"attributes":{"cidr_block":"10.0.0.0/16"}}]},
 {"module":"module.db","mode":"managed","type":"aws_db_instance","name":"app","provider":"provider[\"registry.terraform.io/hashicorp/aws\"].west",
  "instances":[{"attributes":{"password":"hunter2-secret"},"sensitive_attributes":[],"dependencies":["aws_vpc.main"]}]},
 {"mode":"managed","type":"cloudflare_dns_record","name":"api","provider":"provider[\"registry.terraform.io/cloudflare/cloudflare\"]",
  "instances":[{"index_key":"eu","attributes":{}},{"index_key":"us","attributes":{}}]},
 {"mode":"managed","type":"aws_subnet","name":"private","provider":"provider[\"registry.terraform.io/hashicorp/aws\"]",
  "instances":[{"index_key":0,"attributes":{}}]}
]}`

func TestStackResources(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	st := h.stack("", "resources", "")
	h.settings("/stacks/"+st.ID, api.LevelSettings{RequireApproval: ptr(false)})

	// Nothing applied yet: no units and nothing outside.
	var got api.StackResources
	h.call("GET", "/stacks/"+st.ID+"/resources", nil, http.StatusOK, &got)
	if len(got.Units) != 0 || got.StateOutside || got.Drift != nil {
		t.Fatalf("empty stack: %+v", got)
	}

	// An apply that creates the VPC and the database and writes the state.
	run := h.run(st.ID, api.RunApply)
	exec, _ := h.spec(run.ID)
	must(t, exec.PutChanges(ctx, []api.ResourceChange{
		{Address: "aws_vpc.main", Type: "aws_vpc", Name: "main", Action: api.ActionCreate},
		{Address: "module.db.aws_db_instance.app", Type: "aws_db_instance", Name: "app", Action: api.ActionCreate},
	}))
	_, err := exec.Complete(ctx, api.CompleteRequest{Success: true, HasChanges: true})
	must(t, err)
	exec, spec := h.spec(run.ID)
	h.wantState("POST", h.stateURL(st.ID, ""), spec.StateToken, []byte(resourcesState), http.StatusOK)
	_, err = exec.Complete(ctx, api.CompleteRequest{Success: true, HasChanges: true})
	must(t, err)

	raw := h.call("GET", "/stacks/"+st.ID+"/resources", nil, http.StatusOK, &got)
	if strings.Contains(string(raw), "hunter2") || strings.Contains(string(raw), "10.0.0.0/16") {
		t.Fatalf("attribute values in the response: %s", raw)
	}
	if len(got.Units) != 1 || got.Units[0].Key != "" || got.Units[0].Version != 1 || got.Units[0].Error != "" {
		t.Fatalf("units: %+v", got.Units)
	}
	byAddr := map[string]api.StateResource{}
	var order []string
	for _, r := range got.Units[0].Resources {
		byAddr[r.Address] = r
		order = append(order, r.Address)
	}
	want := []string{"data.aws_caller_identity.me", "aws_vpc.main", "module.db.aws_db_instance.app",
		`cloudflare_dns_record.api["eu"]`, `cloudflare_dns_record.api["us"]`, "aws_subnet.private[0]"}
	if strings.Join(order, " ") != strings.Join(want, " ") {
		t.Fatalf("addresses %q, want %q", order, want)
	}
	if r := byAddr["data.aws_caller_identity.me"]; r.Mode != "data" || r.Provider != "registry.terraform.io/hashicorp/aws" {
		t.Errorf("data source: %+v", r)
	}
	db := byAddr["module.db.aws_db_instance.app"]
	if db.Module != "module.db" || db.Provider != "registry.terraform.io/hashicorp/aws" || len(db.DependsOn) != 1 ||
		len(db.History) != 1 || db.History[0].Action != api.ActionCreate || db.History[0].RunID != run.ID {
		t.Errorf("database: %+v", db)
	}
	if r := byAddr[`cloudflare_dns_record.api["us"]`]; r.Instances != 2 || r.Provider != "registry.terraform.io/cloudflare/cloudflare" || len(r.History) != 0 {
		t.Errorf("for_each instance: %+v", r)
	}

	// A drift check that finds the VPC changed shows on it until an apply fixes it.
	drift := h.run(st.ID, api.RunDrift)
	exec, _ = h.spec(drift.ID)
	must(t, exec.PutChanges(ctx, []api.ResourceChange{{Address: "aws_vpc.main", Type: "aws_vpc", Name: "main", Action: api.ActionUpdate}}))
	done, err := exec.Complete(ctx, api.CompleteRequest{Success: true, HasChanges: true})
	must(t, err)
	if done.Status != api.StatusDrifted {
		t.Fatalf("drift run ended %s", done.Status)
	}
	h.call("GET", "/stacks/"+st.ID+"/resources", nil, http.StatusOK, &got)
	if got.Drift == nil || got.Drift.RunID != drift.ID || got.Drift.Resources != 1 {
		t.Fatalf("drift: %+v", got.Drift)
	}
	for _, r := range got.Units[0].Resources {
		if (r.Address == "aws_vpc.main") != (r.Drift == api.ActionUpdate) {
			t.Errorf("%s drift %q", r.Address, r.Drift)
		}
	}

	// An apply that writes no state here: its code keeps the state elsewhere.
	other := h.stack("", "outside", "")
	h.settings("/stacks/"+other.ID, api.LevelSettings{RequireApproval: ptr(false)})
	run = h.run(other.ID, api.RunApply)
	exec, _ = h.spec(run.ID)
	_, err = exec.Complete(ctx, api.CompleteRequest{Success: true, HasChanges: true})
	must(t, err)
	exec, _ = h.spec(run.ID)
	_, err = exec.Complete(ctx, api.CompleteRequest{Success: true, HasChanges: true})
	must(t, err)
	h.call("GET", "/stacks/"+other.ID+"/resources", nil, http.StatusOK, &got)
	if len(got.Units) != 0 || !got.StateOutside {
		t.Fatalf("state outside: %+v", got)
	}
}
