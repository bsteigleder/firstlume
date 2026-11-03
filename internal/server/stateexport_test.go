package server

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/bsteigleder/firstlume/internal/api"
)

func (h *harness) export(stackID, query string, want int) (*http.Response, []byte) {
	h.t.Helper()
	req, _ := http.NewRequest("GET", h.url+"/api/v1/orgs/"+h.org+"/stacks/"+stackID+"/state/export"+query, nil)
	req.Header.Set("Authorization", "Bearer "+testAdminToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		h.t.Fatalf("export%s: status %d, want %d: %s", query, resp.StatusCode, want, raw)
	}
	return resp, raw
}

func untar(t *testing.T, raw []byte) map[string]string {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	must(t, err)
	tr := tar.NewReader(gz)
	out := map[string]string{}
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		must(t, err)
		body, err := io.ReadAll(tr)
		must(t, err)
		out[hdr.Name] = string(body)
	}
}

// TestStateExport covers CAS4.9: the bundle goes back in through the import.
func TestStateExport(t *testing.T) { forEachStorage(t, testStateExport) }

func testStateExport(t *testing.T, storage stateStorage) {
	h := storage.harness(t, Config{StateMaxBytes: 64 << 10})
	ctx := context.Background()
	st := h.stack("", "prod env", "")

	h.export(st.ID, "", http.StatusNotFound)
	h.export(st.ID, "?unit=network", http.StatusNotFound)
	h.export("00000000-0000-0000-0000-000000000000", "", http.StatusNotFound)

	root, network, addons := tfState(3, "lin-root", 1, "s3cr3t-value"), tfState(7, "lin-net", 2, ""), tfState(9, "lin-add", 1, "")
	h.importState(st.ID, false, tarGz(t,
		tarEntry{name: "terraform.tfstate", body: root},
		tarEntry{name: "network/terraform.tfstate", body: network},
		tarEntry{name: "k8s/addons/terraform.tfstate", body: addons},
	), nil, http.StatusOK)

	// The whole stack, in the clear and never cached.
	resp, raw := h.export(st.ID, "", http.StatusOK)
	if got := resp.Header.Get("Content-Type"); got != "application/gzip" {
		t.Fatalf("content type %q", got)
	}
	if got := resp.Header.Get("Content-Disposition"); !strings.Contains(got, `filename=prod-env-state-`) || !strings.Contains(got, `.tar.gz`) {
		t.Fatalf("content disposition %q", got)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("cache control %q", got)
	}
	files := untar(t, raw)
	want := map[string]string{"terraform.tfstate": string(root), "network/terraform.tfstate": string(network), "k8s/addons/terraform.tfstate": string(addons)}
	if len(files) != len(want) {
		t.Fatalf("bundle has %v", files)
	}
	for name, body := range want {
		if files[name] != body {
			t.Fatalf("%s in the bundle: %q, want %q", name, files[name], body)
		}
	}

	// It imports as is into another stack.
	other := h.stack("", "copy", "")
	h.importState(other.ID, false, raw, nil, http.StatusOK)
	if units := h.units(other.ID); len(units) != 3 || units["network"].Resources != 2 || units["k8s/addons"].Resources != 1 || units[""].Resources != 1 {
		t.Fatalf("units after importing the export: %+v", units)
	}

	// One unit is a plain state; an empty unit is the root.
	resp, raw = h.export(st.ID, "?unit=k8s/addons", http.StatusOK)
	if string(raw) != string(addons) || resp.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("one unit: %s %s", resp.Header.Get("Content-Type"), raw)
	}
	if got := resp.Header.Get("Content-Disposition"); !strings.Contains(got, `filename=prod-env-k8s-addons-state-`) || !strings.Contains(got, `.tfstate`) {
		t.Fatalf("content disposition %q", got)
	}
	if _, raw = h.export(st.ID, "?unit=", http.StatusOK); string(raw) != string(root) {
		t.Fatalf("root unit: %s", raw)
	}
	h.export(st.ID, "?unit=nope", http.StatusNotFound)
	h.export(st.ID, "?unit=/abs", http.StatusBadRequest)

	// Each exported unit is recorded: 3 for the stack, 2 for the single units.
	var events int
	must(t, h.pool.QueryRow(ctx,
		`SELECT count(*) FROM state_events WHERE stack_id = $1 AND kind = 'state_exported' AND actor <> '' AND detail ? 'version'`, st.ID).Scan(&events))
	if events != 5 {
		t.Fatalf("export events: %d", events)
	}

	// A unit that can't be read fails the whole export, and nothing is recorded.
	_, err := h.pool.Exec(ctx, `
		UPDATE state_versions SET object_key = 'gone' WHERE unit_id = (SELECT id FROM state_units WHERE stack_id = $1 AND key = 'network')`, st.ID)
	must(t, err)
	_, raw = h.export(st.ID, "", http.StatusServiceUnavailable)
	if !strings.Contains(string(raw), "could not read the state of network") {
		t.Fatalf("unreadable unit: %s", raw)
	}
	must(t, h.pool.QueryRow(ctx, `SELECT count(*) FROM state_events WHERE stack_id = $1 AND kind = 'state_exported'`, st.ID).Scan(&events))
	if events != 5 {
		t.Fatalf("export events after failing: %d", events)
	}
}

// TestDeleteStackWithState covers CAS4.10.
func TestDeleteStackWithState(t *testing.T) { forEachStorage(t, testDeleteStackWithState) }

func testDeleteStackWithState(t *testing.T, storage stateStorage) {
	h := storage.harness(t, Config{StateMaxBytes: 64 << 10})
	ctx := context.Background()
	st := h.stack("", "prod", "")
	h.importState(st.ID, false, tarGz(t, tarEntry{name: "network/terraform.tfstate", body: tfState(7, "lin-net", 2, "")}), nil, http.StatusOK)

	withState := api.DeleteStackRequest{DeleteState: true, Confirm: st.Name}

	// Without the body or with the wrong name, stack and state stay (R6.1).
	h.call("DELETE", "/stacks/"+st.ID, nil, http.StatusConflict, nil)
	h.call("DELETE", "/stacks/"+st.ID, api.DeleteStackRequest{Confirm: st.Name}, http.StatusConflict, nil)
	h.call("DELETE", "/stacks/"+st.ID, api.DeleteStackRequest{DeleteState: true, Confirm: "wrong"}, http.StatusUnprocessableEntity, nil)
	h.call("DELETE", "/stacks/"+st.ID, api.DeleteStackRequest{DeleteState: true}, http.StatusUnprocessableEntity, nil)
	if units := h.units(st.ID); units["network"].Resources != 2 {
		t.Fatalf("units after refusals: %+v", units)
	}

	// A locked unit refuses it, with or without the state.
	var tok api.CreateStateTokenResponse
	h.call("POST", "/stacks/"+st.ID+"/state/tokens", api.CreateStateTokenRequest{Name: "ci", Write: true}, http.StatusCreated, &tok)
	url := h.stateURL(st.ID, "network")
	h.wantState("LOCK", url, tok.Token, lockInfo("lock-1"), http.StatusOK)
	h.call("DELETE", "/stacks/"+st.ID, withState, http.StatusConflict, nil)
	h.wantState("UNLOCK", url, tok.Token, lockInfo("lock-1"), http.StatusOK)

	// A run in progress too.
	run := h.run(st.ID, api.RunPlan)
	h.call("DELETE", "/stacks/"+st.ID, withState, http.StatusConflict, nil)
	exec, _ := h.spec(run.ID)
	if _, err := exec.Complete(ctx, api.CompleteRequest{}); err != nil {
		t.Fatal(err)
	}

	// With the name, both go together, and the state is kept for 30 days (R6.2).
	h.call("DELETE", "/stacks/"+st.ID, withState, http.StatusNoContent, nil)
	h.call("GET", "/stacks/"+st.ID, nil, http.StatusNotFound, nil)
	var kept, events int
	must(t, h.pool.QueryRow(ctx, `SELECT count(*) FROM state_units WHERE stack_id = $1 AND deleted_at IS NOT NULL`, st.ID).Scan(&kept))
	must(t, h.pool.QueryRow(ctx, `SELECT count(*) FROM state_events WHERE stack_id = $1 AND kind = 'state_deleted'`, st.ID).Scan(&events))
	if kept != 1 || events != 1 {
		t.Fatalf("deleted state: %d units kept, %d events", kept, events)
	}

	// A stack with nothing still needs no body at all.
	empty := h.stack("", "empty", "")
	h.call("DELETE", "/stacks/"+empty.ID, nil, http.StatusNoContent, nil)
}
