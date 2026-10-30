// Package executor runs one phase (plan or apply) of a run. It is what the
// Kubernetes Job (or the local driver) starts, and talks to the control plane
// with a token scoped to that single run phase.
package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/bsteigleder/firstlume/internal/api"
	"github.com/bsteigleder/firstlume/internal/apiclient"
)

type Config struct {
	APIURL   string
	Token    string
	WorkDir  string // scratch space for this phase; wiped by the caller
	ToolsDir string // cache for downloaded binaries; may be shared
	Local    io.Writer
}

const planFile = "plan.tfplan"

// Run executes the phase and reports the outcome. It returns an error only when
// the outcome could not be reported to the control plane.
func Run(ctx context.Context, cfg Config) error {
	client := apiclient.New(cfg.APIURL, cfg.Token)
	spec, err := client.Spec(ctx)
	if err != nil {
		return fmt.Errorf("fetching run spec: %w", err)
	}

	var sensitive []string
	if spec.GitToken != "" {
		sensitive = append(sensitive, spec.GitToken)
	}
	for _, v := range spec.Variables {
		if v.Sensitive {
			sensitive = append(sensitive, v.Value)
		}
	}
	logs := NewLogShipper(NewMasker(sensitive), cfg.Local, client.AppendLog)

	hbCtx, stopHeartbeat := context.WithCancel(ctx)
	go heartbeat(hbCtx, client)

	e := &executor{cfg: cfg, spec: spec, client: client, out: logs}
	res, runErr := e.run(ctx)
	stopHeartbeat()

	if errors.Is(runErr, apiclient.ErrNoLongerOwner) {
		logs.Close()
		return runErr
	}
	if runErr != nil {
		fmt.Fprintf(logs, "\n==> Error: %v\n", runErr)
		res = api.CompleteRequest{Success: false, Error: runErr.Error()}
	}
	if err := logs.Close(); err != nil && !errors.Is(err, apiclient.ErrNoLongerOwner) {
		fmt.Fprintf(os.Stderr, "warning: some log output was not delivered: %v\n", err)
	}

	// Report with a fresh context: if we are shutting down we still want the
	// outcome recorded.
	reportCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	run, err := client.Complete(reportCtx, res)
	if err != nil {
		return fmt.Errorf("reporting result: %w", err)
	}
	fmt.Fprintf(os.Stderr, "run %s phase %s finished: status %s\n", spec.RunID, spec.Phase, run.Status)
	return nil
}

func heartbeat(ctx context.Context, client *apiclient.Client) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_ = client.Heartbeat(ctx)
		}
	}
}

type executor struct {
	cfg    Config
	spec   *api.ExecSpec
	client *apiclient.Client
	out    io.Writer
}

func (e *executor) run(ctx context.Context) (api.CompleteRequest, error) {
	s := e.spec
	fmt.Fprintf(e.out, "==> Run %s: %s (%s phase)\n", s.RunID, s.Type, s.Phase)

	srcDir := filepath.Join(e.cfg.WorkDir, "src")
	homeDir := filepath.Join(e.cfg.WorkDir, "home")
	if err := os.MkdirAll(homeDir, 0o700); err != nil {
		return api.CompleteRequest{}, err
	}
	gitEnvs := gitEnv(s.GitToken)
	baseEnv := append(inheritedEnv(), "HOME="+homeDir)
	baseEnv = append(baseEnv, gitEnvs...)

	fmt.Fprintf(e.out, "==> Checking out %s @ %s\n", s.Repository, describeTarget(s))
	sha, err := checkout(ctx, srcDir, s.Repository, s.Ref, s.CommitSHA, baseEnv, e.out)
	if err != nil {
		return api.CompleteRequest{}, err
	}
	fmt.Fprintf(e.out, "==> Commit %s\n", sha)
	if s.Phase == api.PhasePlan {
		if err := e.client.SetCommit(ctx, sha); err != nil {
			return api.CompleteRequest{}, err
		}
	}

	inst := &Installer{Dir: e.cfg.ToolsDir, Log: e.out}
	engineBin, err := inst.Install(ctx, s.Engine, s.EngineVersion)
	if err != nil {
		return api.CompleteRequest{}, err
	}
	bin := engineBin
	if s.TerragruntVersion != "" {
		if bin, err = inst.Install(ctx, "terragrunt", s.TerragruntVersion); err != nil {
			return api.CompleteRequest{}, err
		}
	}

	workDir, err := resolveWorkDir(srcDir, s.Path)
	if err != nil {
		return api.CompleteRequest{}, err
	}
	env := append(baseEnv, toolEnv(engineBin, filepath.Join(e.cfg.WorkDir, "plugin-cache"))...)
	env = append(env, userEnv(s.Variables)...)
	r := &runner{dir: workDir, bin: bin, env: env, out: e.out}
	planPath := filepath.Join(e.cfg.WorkDir, planFile)

	if s.Phase == api.PhasePlan {
		return e.plan(ctx, r, planPath)
	}
	return e.apply(ctx, r, planPath)
}

func (e *executor) plan(ctx context.Context, r *runner, planPath string) (api.CompleteRequest, error) {
	if _, err := r.run(ctx, "init", "-input=false", "-no-color"); err != nil {
		return api.CompleteRequest{}, err
	}
	args := []string{"plan", "-input=false", "-no-color", "-detailed-exitcode", "-out=" + planPath}
	if e.spec.Type == api.RunDestroy {
		args = append(args, "-destroy")
	}
	code, err := r.run(ctx, args...)
	if code != 0 && code != 2 {
		return api.CompleteRequest{}, err
	}
	res := api.CompleteRequest{Success: true, HasChanges: code == 2}

	if res.HasChanges {
		raw, err := r.capture(ctx, "show", "-json", "-no-color", planPath)
		if err != nil {
			return api.CompleteRequest{}, fmt.Errorf("reading plan: %w", err)
		}
		if res.PlanSummary, err = SummarizePlan(raw); err != nil {
			return api.CompleteRequest{}, fmt.Errorf("parsing plan: %w", err)
		}
		label := "Plan"
		if e.spec.Type == api.RunDrift {
			label = "Drift detected"
		}
		fmt.Fprintf(e.out, "==> %s: %d to add, %d to change, %d to destroy\n",
			label, res.PlanSummary.Add, res.PlanSummary.Change, res.PlanSummary.Destroy)
		if e.spec.Type.Applies() {
			data, err := os.ReadFile(planPath)
			if err != nil {
				return api.CompleteRequest{}, err
			}
			if err := e.client.PutArtifact(ctx, "plan", data); err != nil {
				return api.CompleteRequest{}, fmt.Errorf("uploading plan: %w", err)
			}
		}
	} else {
		if e.spec.Type == api.RunDrift {
			fmt.Fprintln(e.out, "==> No drift: the infrastructure matches the code")
		} else {
			fmt.Fprintln(e.out, "==> No changes")
		}
	}
	return res, nil
}

func (e *executor) apply(ctx context.Context, r *runner, planPath string) (api.CompleteRequest, error) {
	data, err := e.client.GetArtifact(ctx, "plan")
	if err != nil {
		return api.CompleteRequest{}, fmt.Errorf("downloading plan: %w", err)
	}
	if err := os.WriteFile(planPath, data, 0o600); err != nil {
		return api.CompleteRequest{}, err
	}
	if _, err := r.run(ctx, "init", "-input=false", "-no-color"); err != nil {
		return api.CompleteRequest{}, err
	}
	if _, err := r.run(ctx, "apply", "-input=false", "-no-color", "-auto-approve", planPath); err != nil {
		return api.CompleteRequest{}, err
	}
	return api.CompleteRequest{Success: true}, nil
}

func describeTarget(s *api.ExecSpec) string {
	if s.CommitSHA != "" {
		return s.Ref + " (" + short(s.CommitSHA) + ")"
	}
	return s.Ref
}

func resolveWorkDir(srcDir, rel string) (string, error) {
	dir := filepath.Join(srcDir, filepath.FromSlash(rel))
	if dir != srcDir && !strings.HasPrefix(dir, srcDir+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes the repository", rel)
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("path %q not found in repository", rel)
	}
	return dir, nil
}

// inheritedEnv passes the process environment through (it carries cloud
// credentials such as IRSA or Workload Identity settings) but strips our own
// variables so the run token never reaches user code.
func inheritedEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "FIRSTLUME_") {
			env = append(env, kv)
		}
	}
	return env
}

func toolEnv(engineBin, pluginCache string) []string {
	_ = os.MkdirAll(pluginCache, 0o755)
	return []string{
		"TF_IN_AUTOMATION=1",
		"TF_INPUT=0",
		"CHECKPOINT_DISABLE=1",
		"TF_PLUGIN_CACHE_DIR=" + pluginCache,
		// Terragrunt: use the pinned engine binary, never prompt.
		"TG_TF_PATH=" + engineBin,
		"TERRAGRUNT_TFPATH=" + engineBin,
		"TG_NON_INTERACTIVE=true",
		"TERRAGRUNT_NON_INTERACTIVE=true",
	}
}

func userEnv(vars []api.ExecVariable) []string {
	var env []string
	for _, v := range vars {
		switch v.Category {
		case api.CategoryEnv:
			env = append(env, v.Key+"="+v.Value)
		case api.CategoryTerraform:
			env = append(env, "TF_VAR_"+v.Key+"="+v.Value)
		}
	}
	return env
}

type runner struct {
	dir string
	bin string
	env []string
	out io.Writer
}

func (r *runner) command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, r.bin, args...)
	cmd.Dir = r.dir
	cmd.Env = r.env
	// On cancellation, interrupt instead of kill so Terraform can release state
	// locks and persist partial state; force-kill only if it hangs.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGINT) }
	cmd.WaitDelay = 2 * time.Minute
	return cmd
}

// run streams output to the log and returns the exit code.
func (r *runner) run(ctx context.Context, args ...string) (int, error) {
	fmt.Fprintf(r.out, "\n==> %s %s\n", filepath.Base(r.bin), strings.Join(args, " "))
	cmd := r.command(ctx, args...)
	cmd.Stdout = r.out
	cmd.Stderr = r.out
	err := cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), fmt.Errorf("%s %s exited with code %d", filepath.Base(r.bin), args[0], exitErr.ExitCode())
	}
	if err != nil {
		return -1, err
	}
	return 0, nil
}

// capture returns stdout without logging it (used for machine-readable output).
func (r *runner) capture(ctx context.Context, args ...string) ([]byte, error) {
	cmd := r.command(ctx, args...)
	cmd.Stderr = r.out
	return cmd.Output()
}
