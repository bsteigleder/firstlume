package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bsteigleder/firstlume/internal/api"
	"github.com/bsteigleder/firstlume/internal/apiclient"
)

// fakeEngine writes a script that runs until interrupted, as Terraform does:
// it says so, cleans up and exits with an error.
func fakeEngine(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "terraform")
	script := "#!/bin/sh\ntrap 'echo interrupted, saving state; exit 1' INT\necho started\nwhile true; do sleep 0.05; done\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func TestStopInterruptsTheCommand(t *testing.T) {
	stop := newStopSignal()
	var out bytes.Buffer
	r := &runner{dir: t.TempDir(), bin: fakeEngine(t), out: &out, stop: stop.ctx, noKill: true}
	go func() {
		time.Sleep(300 * time.Millisecond)
		stop.request("Ana Souza")
	}()
	done := make(chan error, 1)
	go func() { _, err := r.run(context.Background(), "apply"); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("an interrupted command should fail")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the stop did not interrupt the command")
	}
	if !strings.Contains(out.String(), "interrupted, saving state") {
		t.Fatalf("the command was not interrupted gracefully: %q", out.String())
	}
	if by, ok := stop.requested(); !ok || by != "Ana Souza" {
		t.Fatalf("requested = %q, %v", by, ok)
	}
	// Only the first request counts.
	if stop.request("someone else") {
		t.Fatal("second request reported as first")
	}

	// Once stopped, nothing new starts.
	out.Reset()
	if _, err := r.run(context.Background(), "apply"); err == nil || strings.Contains(out.String(), "started") {
		t.Fatalf("a command started after the stop: %v, %q", err, out.String())
	}
}

func TestStepsAfterStopAreSkipped(t *testing.T) {
	var steps []api.StepRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var st api.StepRequest
		_ = json.NewDecoder(r.Body).Decode(&st)
		steps = append(steps, st)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	stop := newStopSignal()
	e := &executor{client: apiclient.New(srv.URL, "t"), out: &bytes.Buffer{}, stop: stop}
	ran := 0
	err := e.runSteps(context.Background(), []step{
		{name: "Checkout", run: func() error { ran++; stop.request("Ana"); return nil }},
		{name: "Init", run: func() error { ran++; return nil }},
		{name: "Plan", run: func() error { ran++; return nil }},
	})
	if err == nil || ran != 1 {
		t.Fatalf("err %v, ran %d", err, ran)
	}
	var got []string
	for _, s := range steps {
		got = append(got, s.Name+":"+string(s.Status))
	}
	want := "Checkout:running Checkout:succeeded Init:skipped Plan:skipped"
	if strings.Join(got, " ") != want {
		t.Fatalf("steps %v, want %s", got, want)
	}
}

func TestHeartbeatHearsTheStop(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/exec/v1/heartbeat" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(api.HeartbeatResponse{StopRequested: true, StopRequestedBy: "Ana Souza"})
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	heard := make(chan string, 1)
	go heartbeat(ctx, apiclient.New(srv.URL, "t"), 10*time.Millisecond, func(by string) {
		select {
		case heard <- by:
		default:
		}
	})
	select {
	case by := <-heard:
		if by != "Ana Souza" {
			t.Fatalf("by %q", by)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stop not heard")
	}
}
