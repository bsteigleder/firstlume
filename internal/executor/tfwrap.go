package executor

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
)

// In `terragrunt run --all`, Terragrunt passes each unit's `apply -json`
// output through untouched, so two units with the same resource address
// cannot be told apart (spec 0004, D1). The firstlume binary then stands in as
// the engine Terragrunt runs (TG_TF_PATH): for an `apply -json` it tags each
// output line with the unit, taken from the saved plan's path
// (<plans>/<unit>/tfplan.tfplan, as in the plan phase); any other command
// becomes the real engine.
const (
	wrapEngineEnv = "FIRSTLUME_WRAP_ENGINE"
	wrapPlansEnv  = "FIRSTLUME_WRAP_PLANS"
	// unitTag starts a tagged line: unitTag + unit + "\t" + the engine's line.
	unitTag = "\x1ffirstlume-unit:"
)

// EngineWrapper runs the engine on behalf of Terragrunt when the process was
// started as one. It reports whether it did, and the exit code to use.
func EngineWrapper() (int, bool) {
	engine := os.Getenv(wrapEngineEnv)
	if engine == "" {
		return 0, false
	}
	args := os.Args[1:]
	if len(args) == 0 || args[0] != "apply" || !contains(args, "-json") {
		// Become the engine: same process, signals and exit code.
		err := syscall.Exec(engine, append([]string{engine}, args...), os.Environ())
		fmt.Fprintf(os.Stderr, "firstlume: running %s: %v\n", engine, err)
		return 1, true
	}
	return wrapApply(engine, args, unitOf(args, os.Getenv(wrapPlansEnv))), true
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// unitOf finds the unit from the plan file, the last argument.
func unitOf(args []string, plansDir string) string {
	plan := args[len(args)-1]
	if plansDir == "" || !strings.HasSuffix(plan, ".tfplan") {
		return ""
	}
	for _, resolve := range []func(string) (string, error){
		func(p string) (string, error) { return filepath.Abs(p) },
		filepath.EvalSymlinks,
	} {
		dir, err1 := resolve(plansDir)
		file, err2 := resolve(plan)
		if err1 != nil || err2 != nil {
			continue
		}
		if rel, err := filepath.Rel(dir, filepath.Dir(file)); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
			return filepath.ToSlash(rel)
		}
	}
	return ""
}

// wrapApply runs the apply and tags each line of its stdout with the unit.
func wrapApply(engine string, args []string, unit string) int {
	cmd := exec.Command(engine, args...)
	cmd.Stdin, cmd.Stderr = os.Stdin, os.Stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		fmt.Fprintf(os.Stderr, "firstlume: %v\n", err)
		return 1
	}
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "firstlume: running %s: %v\n", engine, err)
		return 1
	}
	// Terragrunt interrupts its engine to cancel: pass it on, so Terraform can
	// stop cleanly and release its locks.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	go func() {
		for s := range sigs {
			_ = cmd.Process.Signal(s)
		}
	}()
	w := bufio.NewWriter(os.Stdout)
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 64<<10), maxBufferedLine)
	for sc.Scan() {
		if unit != "" {
			w.WriteString(unitTag + unit + "\t")
		}
		w.Write(sc.Bytes())
		w.WriteByte('\n')
		w.Flush()
	}
	err = cmd.Wait()
	signal.Stop(sigs)
	close(sigs)
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "firstlume: %v\n", err)
		return 1
	}
	return 0
}

// wrapperEnv makes Terragrunt run the engine through this binary, by a link
// in linkDir named like the engine (terraform or tofu): Terragrunt shows that
// name in its log and may tell the engines apart by it.
func wrapperEnv(engineBin, plansDir, linkDir string) ([]string, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(linkDir, 0o755); err != nil {
		return nil, err
	}
	link := filepath.Join(linkDir, filepath.Base(engineBin))
	if err := os.Remove(link); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := os.Symlink(self, link); err != nil {
		return nil, err
	}
	return []string{
		"TG_TF_PATH=" + link,
		"TERRAGRUNT_TFPATH=" + link,
		wrapEngineEnv + "=" + engineBin,
		wrapPlansEnv + "=" + plansDir,
	}, nil
}
