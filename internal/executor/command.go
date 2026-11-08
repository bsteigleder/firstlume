package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/bsteigleder/firstlume/internal/api"
	"github.com/bsteigleder/firstlume/internal/command"
)

// commandFailed is a line of a command run that exited with a code other than 0.
type commandFailed struct {
	line, code int
}

func (e *commandFailed) Error() string {
	return fmt.Sprintf("line %d exited with code %d", e.line, e.code)
}

// commandTools is what the commit being run declares, for the check (R12).
func commandTools(cfg *api.StackConfig) command.Tools {
	return command.Tools{Known: true, Engine: cfg.Engine, Terragrunt: cfg.TerragruntVersion != ""}
}

// checkCommand checks the command again with the configuration of the commit
// being run, and that the phase matches its access to the state: the plan
// phase's token can't write, the apply phase's can.
func (e *executor) checkCommand() ([]command.Line, error) {
	s := e.spec
	access := command.Access(s.StateAccess)
	want := api.PhasePlan
	if access == command.AccessWrite {
		want = api.PhaseApply
	}
	if s.Phase != want {
		return nil, fmt.Errorf("a command with %s access can't run in the %s phase", s.StateAccess, s.Phase)
	}
	return command.Check(s.Command, commandTools(e.stack), access)
}

// readOnlyRefusal is what Terraform's http backend prints when Firstlume
// refuses a write (R20).
var readOnlyRefusal = regexp.MustCompile(`HTTP error:? 403`)

// runCommand runs each line in order, without a shell, in the stack's folder,
// and stops at the first that fails (R13, R14).
func (e *executor) runCommand(ctx context.Context, r *runner, lines []command.Line, workDir, tgBin string) error {
	access := "read only"
	if e.spec.StateAccess == string(command.AccessWrite) {
		access = "may change the state"
	}
	fmt.Fprintf(e.out, "==> Command (%s)\n", access)
	realRoot, err := filepath.EvalSymlinks(workDir)
	if err != nil {
		return err
	}
	for _, l := range lines {
		for _, d := range l.Dirs {
			if err := insideRoot(realRoot, filepath.Join(workDir, filepath.FromSlash(d))); err != nil {
				return fmt.Errorf("line %d: %w", l.Number, err)
			}
		}
	}
	for _, l := range lines {
		// A stop keeps the next line from starting; the one running is
		// interrupted by the runner (and, with write access, never killed).
		if e.stopped() {
			fmt.Fprintf(e.out, "\n==> Stopped before line %d: it and the lines after it didn’t run.\n", l.Number)
			return loggedError{errStopped}
		}
		fmt.Fprintf(e.out, "\n$ %s\n", quoteArgs(l.Args))
		lr := *r
		if l.Args[0] == "terragrunt" {
			lr.bin = tgBin
		} else {
			lr.bin = e.engineBin
		}
		code, refused, err := lr.runLine(ctx, l.Args[1:])
		if err != nil {
			return err
		}
		// A line that still ended well is done; the loop stops before the next.
		if code != 0 && e.stopped() {
			fmt.Fprintf(e.out, "==> Line %d was interrupted by the stop.", l.Number)
			if l.Number != lines[len(lines)-1].Number {
				fmt.Fprint(e.out, " Lines after it didn’t run.")
			}
			fmt.Fprintln(e.out)
			return loggedError{errStopped}
		}
		if code != 0 {
			fmt.Fprintf(e.out, "==> Exit code %d (line %d).", code, l.Number)
			if l.Number != lines[len(lines)-1].Number {
				fmt.Fprint(e.out, " Lines after it didn’t run.")
			}
			fmt.Fprintln(e.out)
			if refused && e.spec.StateAccess != string(command.AccessWrite) {
				fmt.Fprintln(e.out, "==> The state is read only in this run, so the backend refused a write.")
			}
			return loggedError{&commandFailed{line: l.Number, code: code}}
		}
	}
	fmt.Fprintln(e.out, "==> Exit code 0")
	return nil
}

// stopped reports whether someone asked to stop the run.
func (e *executor) stopped() bool {
	return e.stop != nil && e.stop.ctx.Err() != nil
}

// runLine runs one line and returns its exit code, and whether the state
// backend refused a write while it ran. err is set only when it could not run.
func (r *runner) runLine(ctx context.Context, args []string) (code int, refused bool, err error) {
	cmd, release := r.command(ctx, args...)
	defer release()
	// The user's command doesn't say -no-color, and logs are read in a browser.
	plain := &ansiStrip{w: r.out}
	watch := &refusalWatch{w: plain}
	cmd.Stdout = watch
	cmd.Stderr = watch
	err = cmd.Run()
	plain.Flush()
	// Once it ran, its own exit status counts: after a stop Go reports the
	// cancellation even when the line still ended well. -1 is a signal.
	if cmd.ProcessState != nil {
		return cmd.ProcessState.ExitCode(), watch.seen(), nil
	}
	return -1, false, err
}

// refusalWatch passes output through and notes a refused state write.
type refusalWatch struct {
	w    io.Writer
	mu   sync.Mutex
	tail []byte
	hit  bool
}

func (rw *refusalWatch) Write(p []byte) (int, error) {
	rw.mu.Lock()
	buf := append(rw.tail, p...)
	if readOnlyRefusal.Match(buf) {
		rw.hit = true
	}
	if i := bytes.LastIndexByte(buf, '\n'); i >= 0 {
		buf = buf[i+1:]
	}
	if len(buf) > 256 {
		buf = buf[len(buf)-256:]
	}
	rw.tail = append([]byte(nil), buf...)
	rw.mu.Unlock()
	return rw.w.Write(p)
}

func (rw *refusalWatch) seen() bool {
	rw.mu.Lock()
	defer rw.mu.Unlock()
	return rw.hit
}

// ansiEscape matches the color and style codes terminals interpret.
var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

// ansiStrip drops terminal escape codes from what it passes on, holding back
// a code cut between two writes until the rest arrives.
type ansiStrip struct {
	w    io.Writer
	mu   sync.Mutex
	held []byte
}

func (a *ansiStrip) Write(p []byte) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	buf := append(a.held, p...)
	a.held = nil
	if i := bytes.LastIndexByte(buf, 0x1b); i >= 0 && !ansiEscape.Match(buf[i:]) && len(buf)-i < 32 {
		a.held = append([]byte(nil), buf[i:]...)
		buf = buf[:i]
	}
	if _, err := a.w.Write(ansiEscape.ReplaceAll(buf, nil)); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Flush passes on what was held back.
func (a *ansiStrip) Flush() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.held) > 0 {
		_, _ = a.w.Write(a.held)
		a.held = nil
	}
}

// quoteArgs shows a line as it could be typed again.
func quoteArgs(args []string) string {
	out := make([]string, len(args))
	for i, a := range args {
		if a == "" || strings.ContainsAny(a, " \t\"'#\\|;&$`<>(){}*?~!") {
			a = "'" + strings.ReplaceAll(a, "'", `'"'"'`) + "'"
		}
		out[i] = a
	}
	return strings.Join(out, " ")
}
