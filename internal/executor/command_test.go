package executor

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bsteigleder/firstlume/internal/api"
	"github.com/bsteigleder/firstlume/internal/command"
)

// scriptEngine is a fake terraform that prints its arguments and exits with
// the code given by FAKE_EXIT_<subcommand>, reading stdin to prove it is empty.
func scriptEngine(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "terraform")
	script := `#!/bin/sh
echo "ran: $*"
input=$(cat)
[ -n "$input" ] && echo "got input: $input"
case "$1" in
  fail) echo "Error: something broke" >&2; exit 3 ;;
  refused) echo "Error: Failed to persist state: HTTP error: 403" >&2; exit 1 ;;
  slow) exec sleep 5 ;;
  finishes) trap "" INT; sleep 1; echo "finished anyway"; exit 0 ;;
esac
exit 0
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func commandExecutor(t *testing.T, access string) (*executor, *bytes.Buffer) {
	var out bytes.Buffer
	return &executor{
		out:       &out,
		spec:      &api.ExecSpec{Type: api.RunCommand, StateAccess: access},
		engineBin: scriptEngine(t),
	}, &out
}

func lines(args ...[]string) []command.Line {
	var out []command.Line
	for i, a := range args {
		out = append(out, command.Line{Number: i + 1, Args: a})
	}
	return out
}

func TestRunCommandRunsLinesInOrder(t *testing.T) {
	e, out := commandExecutor(t, "read")
	dir := t.TempDir()
	r := &runner{dir: dir, out: out}
	err := e.runCommand(context.Background(), r, lines(
		[]string{"terraform", "state", "list"},
		[]string{"terraform", "state", "show", `module.a.x["k"]`},
	), dir, "")
	if err != nil {
		t.Fatal(err)
	}
	log := out.String()
	for _, want := range []string{
		"==> Command (read only)",
		"$ terraform state list\nran: state list",
		`$ terraform state show 'module.a.x["k"]'` + "\nran: state show module.a.x[\"k\"]",
		"==> Exit code 0",
	} {
		if !strings.Contains(log, want) {
			t.Errorf("log lacks %q:\n%s", want, log)
		}
	}
	if strings.Contains(log, "got input") {
		t.Errorf("the command got input:\n%s", log)
	}
}

// CAC8: the line that fails stops the rest, and its code and number are kept.
func TestRunCommandStopsAtTheFirstFailure(t *testing.T) {
	e, out := commandExecutor(t, "write")
	dir := t.TempDir()
	r := &runner{dir: dir, out: out}
	err := e.runCommand(context.Background(), r, lines(
		[]string{"terraform", "state", "list"},
		[]string{"terraform", "fail"},
		[]string{"terraform", "never"},
	), dir, "")
	var cf *commandFailed
	if !errors.As(err, &cf) || cf.line != 2 || cf.code != 3 {
		t.Fatalf("err = %v, want line 2 code 3", err)
	}
	log := out.String()
	if strings.Contains(log, "ran: never") || !strings.Contains(log, "==> Exit code 3 (line 2). Lines after it didn’t run.") {
		t.Fatalf("log:\n%s", log)
	}
}

// R21: a stop interrupts the line running and no line starts after it.
func TestRunCommandStops(t *testing.T) {
	e, out := commandExecutor(t, "read")
	e.stop = newStopSignal()
	dir := t.TempDir()
	r := &runner{dir: dir, out: out, stop: e.stop.ctx}
	go func() {
		time.Sleep(300 * time.Millisecond)
		e.stop.request("ana")
	}()
	start := time.Now()
	err := e.runCommand(context.Background(), r, lines(
		[]string{"terraform", "state", "list"},
		[]string{"terraform", "slow"},
		[]string{"terraform", "never"},
	), dir, "")
	if !errors.Is(err, errStopped) || time.Since(start) > 4*time.Second {
		t.Fatalf("err = %v after %s", err, time.Since(start))
	}
	log := out.String()
	if strings.Contains(log, "ran: never") || strings.Contains(log, "Exit code") ||
		!strings.Contains(log, "==> Line 2 was interrupted by the stop. Lines after it didn’t run.") {
		t.Fatalf("log:\n%s", log)
	}

	// A line that ends well despite the stop is done; the next doesn't start.
	e, out = commandExecutor(t, "write")
	e.stop = newStopSignal()
	go func() {
		time.Sleep(300 * time.Millisecond)
		e.stop.request("ana")
	}()
	err = e.runCommand(context.Background(), &runner{dir: dir, out: out, stop: e.stop.ctx, noKill: true}, lines(
		[]string{"terraform", "finishes"},
		[]string{"terraform", "never"},
	), dir, "")
	log = out.String()
	if !errors.Is(err, errStopped) || strings.Contains(log, "ran: never") || strings.Contains(log, "interrupted") ||
		!strings.Contains(log, "finished anyway") || !strings.Contains(log, "==> Stopped before line 2: it and the lines after it didn’t run.") {
		t.Fatalf("err = %v, log:\n%s", err, log)
	}

	// A stop heard between lines keeps the next one from starting.
	e, out = commandExecutor(t, "write")
	e.stop = newStopSignal()
	e.stop.request("ana")
	err = e.runCommand(context.Background(), &runner{dir: dir, out: out, stop: e.stop.ctx}, lines([]string{"terraform", "state", "list"}), dir, "")
	if !errors.Is(err, errStopped) || strings.Contains(out.String(), "ran:") || !strings.Contains(out.String(), "Stopped before line 1") {
		t.Fatalf("err = %v, log:\n%s", err, out)
	}
}

// R20: a write the backend refused in a read-only run is explained.
func TestRunCommandExplainsAReadOnlyRefusal(t *testing.T) {
	e, out := commandExecutor(t, "read")
	dir := t.TempDir()
	err := e.runCommand(context.Background(), &runner{dir: dir, out: out}, lines([]string{"terraform", "refused"}), dir, "")
	if err == nil || !strings.Contains(out.String(), "The state is read only in this run, so the backend refused a write.") {
		t.Fatalf("err = %v, log:\n%s", err, out)
	}
}

// S5: a folder named in the command can't be a link out of the stack.
func TestRunCommandRefusesLinksOutOfTheStack(t *testing.T) {
	e, out := commandExecutor(t, "read")
	dir := t.TempDir()
	if err := os.Symlink(t.TempDir(), filepath.Join(dir, "units")); err != nil {
		t.Fatal(err)
	}
	l := command.Line{Number: 1, Args: []string{"terraform", "-chdir=units/x", "state", "list"}, Dirs: []string{"units/x"}}
	err := e.runCommand(context.Background(), &runner{dir: dir, out: out}, []command.Line{l}, dir, "")
	if err == nil || !strings.Contains(err.Error(), "outside the stack") || strings.Contains(out.String(), "ran:") {
		t.Fatalf("err = %v, log:\n%s", err, out)
	}
}

// R12: the phase must match the access, and the command is checked with the
// commit's tools.
func TestCheckCommandAgainstPhaseAndTools(t *testing.T) {
	e := &executor{stack: &api.StackConfig{Engine: "opentofu"}}
	e.spec = &api.ExecSpec{Type: api.RunCommand, StateAccess: "write", Phase: api.PhasePlan, Command: "tofu state rm a"}
	if _, err := e.checkCommand(); err == nil {
		t.Fatal("a write command ran in the plan phase")
	}
	e.spec.Phase = api.PhaseApply
	if _, err := e.checkCommand(); err != nil {
		t.Fatal(err)
	}
	e.spec.Command = "terraform state rm a"
	if _, err := e.checkCommand(); err == nil || !strings.Contains(err.Error(), "start with tofu") {
		t.Fatalf("err = %v", err)
	}
	e.spec.Command = "ssh host"
	if _, err := e.checkCommand(); err == nil {
		t.Fatal("ssh passed the executor's check")
	}
}

func TestANSIStrip(t *testing.T) {
	var out bytes.Buffer
	a := &ansiStrip{w: &out}
	for _, chunk := range []string{"\x1b[31m\x1b[31m╷\x1b[0m\x1b[0m\n│ \x1b[1m\x1b[3", "1mError: \x1b[0mbad\n", "done \x1b"} {
		if _, err := a.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	a.Flush()
	if got, want := out.String(), "╷\n│ Error: bad\ndone \x1b"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
