// Package executor runs one phase (plan or apply) of a run. It is what the
// Kubernetes Job (or the local driver) starts, and talks to the control plane
// with a token scoped to that single run phase.
package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/bsteigleder/firstlume/internal/api"
	"github.com/bsteigleder/firstlume/internal/apiclient"
	"github.com/bsteigleder/firstlume/internal/stackconfig"
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
	for _, tok := range []string{spec.GitToken, spec.StateToken} {
		if tok != "" {
			sensitive = append(sensitive, tok)
		}
	}
	for _, v := range spec.Variables {
		if v.Sensitive {
			sensitive = append(sensitive, v.Value)
		}
	}
	masker := NewMasker(sensitive)
	logs := NewLogShipper(masker, cfg.Local, client.AppendLog)

	hbCtx, stopHeartbeat := context.WithCancel(ctx)
	go heartbeat(hbCtx, client)

	e := &executor{cfg: cfg, spec: spec, client: client, out: logs, mask: masker.Mask, masker: masker, logs: logs,
		vault: newVaultSource(spec.Vault, masker)}
	res, runErr := e.run(ctx)
	if err := e.vault.revoke(context.WithoutCancel(ctx)); err != nil {
		fmt.Fprintf(logs, "==> Warning: could not revoke the Vault token: %v\n", err)
	}
	stopHeartbeat()

	if errors.Is(runErr, apiclient.ErrNoLongerOwner) {
		logs.Close()
		return runErr
	}
	if runErr != nil {
		if !errors.As(runErr, new(loggedError)) {
			fmt.Fprintf(logs, "\n==> Error: %v\n", runErr)
		}
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
	// mask hides secret values in what is sent back besides logs (plan changes).
	mask   func(string) string
	masker *Masker
	stack  *api.StackConfig
	// decl is what the stack's config declares the run needs (spec 0003).
	decl *stackconfig.Declaration
	// prepared is what the Prepare step wrote and exported.
	prepared *prepared
	// vault reads the declaration's Vault secrets.
	vault *vaultSource
	// logs is the shipper behind out, flushed between steps.
	logs *LogShipper
}

func (e *executor) run(ctx context.Context) (api.CompleteRequest, error) {
	s := e.spec
	fmt.Fprintf(e.out, "==> Run %s: %s (%s phase)\n", s.RunID, s.Type, s.Phase)

	srcDir := filepath.Join(e.cfg.WorkDir, "src")
	homeDir := filepath.Join(e.cfg.WorkDir, "home")
	if err := os.MkdirAll(homeDir, 0o700); err != nil {
		return api.CompleteRequest{}, err
	}
	gitEnvs := gitEnv(s.Repository, s.GitToken)
	baseEnv := append(inheritedEnv(), "HOME="+homeDir)
	baseEnv = append(baseEnv, gitEnvs...)
	planPath := filepath.Join(e.cfg.WorkDir, planFile)

	var workDir string
	var r *runner
	var res api.CompleteRequest
	last := "Plan"
	if s.Phase == api.PhaseApply {
		last = "Apply"
	}
	err := e.runSteps(ctx, []step{
		{name: "Checkout", run: func() error {
			fmt.Fprintf(e.out, "==> Checking out %s @ %s\n", s.Repository, describeTarget(s))
			sha, err := checkout(ctx, srcDir, s.Repository, s.Ref, s.CommitSHA, baseEnv, e.out)
			if err != nil {
				return err
			}
			fmt.Fprintf(e.out, "==> Commit %s\n", sha)
			if workDir, err = resolveWorkDir(srcDir, s.Path); err != nil {
				return err
			}
			// The tooling comes from the commit being run, so plan and apply (same
			// commit) always agree, and upgrading Terraform is just a commit.
			cfg, decl, cfgErr := stackconfig.LoadDeclaration(workDir, s.Path)
			e.stack, e.decl = cfg, decl
			if s.Phase == api.PhasePlan {
				commit := api.CommitRequest{SHA: sha, Config: cfg}
				if commit.AuthorName, commit.AuthorEmail, commit.Message, err = commitInfo(ctx, srcDir, baseEnv); err != nil {
					fmt.Fprintf(e.out, "==> Warning: could not read the commit author: %v\n", err)
				}
				if err := e.client.SetCommit(ctx, commit); err != nil {
					return err
				}
			}
			if cfgErr != nil {
				return cfgErr
			}
			fmt.Fprintf(e.out, "==> %s %s%s (from %s)\n", cfg.Engine, cfg.EngineVersion, terragruntSuffix(cfg),
				filepath.ToSlash(filepath.Join(s.Path, stackconfig.RelPath)))
			return nil
		}},
		{name: "Prepare", skip: func() bool { return !e.needsPrepare() }, run: func() (err error) {
			if e.prepared, err = prepare(ctx, workDir, e.decl, s.Variables, e.vault, e.masker, e.out); err != nil {
				return err
			}
			return e.checkVaultValues(ctx)
		}},
		{name: "Init", run: func() error {
			cfg := e.stack
			inst := &Installer{Dir: e.cfg.ToolsDir, Log: e.out}
			engineBin, err := inst.Install(ctx, cfg.Engine, cfg.EngineVersion)
			if err != nil {
				return err
			}
			bin := engineBin
			if cfg.TerragruntVersion != "" {
				if bin, err = inst.Install(ctx, "terragrunt", cfg.TerragruntVersion); err != nil {
					return err
				}
			}
			env := append(baseEnv, toolEnv(engineBin, filepath.Join(e.cfg.WorkDir, "plugin-cache"))...)
			env = append(env, stateEnv(e.cfg.APIURL, s)...)
			env = append(env, userEnv(s.Variables)...)
			if e.prepared != nil {
				env = append(env, e.prepared.env...)
			}
			r = &runner{dir: workDir, bin: bin, env: env, out: e.out}
			if cfg.RunAll {
				fmt.Fprintln(e.out, "==> terragrunt initializes each unit as part of run --all")
				return nil
			}
			if _, err = r.run(ctx, "init", "-input=false", "-no-color"); err != nil {
				return err
			}
			return checkBackends(workDir, "", e.out)
		}},
		{name: last, run: func() (err error) {
			if s.Phase == api.PhasePlan {
				if res, err = e.plan(ctx, r, planPath); err == nil && e.stack.RunAll {
					// Terragrunt initialized the units while planning: check them now.
					err = checkBackends(workDir, e.plansJSONDir(), e.out)
				}
			} else {
				res, err = e.apply(ctx, r, planPath)
			}
			return err
		}},
	})
	return res, err
}

// step is one reported stage of a run phase.
type step struct {
	name string
	run  func() error
	// skip, when set and true at its turn, leaves the step out entirely.
	skip func() bool
}

// checkVaultValues makes the apply use the Vault values its plan used (R3.8):
// the plan stores their keyed hashes, the apply compares.
func (e *executor) checkVaultValues(ctx context.Context) error {
	if e.spec.Phase == api.PhasePlan {
		if len(e.vault.hashes) == 0 {
			return nil
		}
		data, err := json.Marshal(e.vault.hashes)
		if err != nil {
			return err
		}
		return e.client.PutArtifact(ctx, api.VaultHashesArtifact, data)
	}
	plan := map[string]string{}
	data, err := e.client.GetArtifact(ctx, api.VaultHashesArtifact)
	var se *apiclient.StatusError
	switch {
	case errors.As(err, &se) && se.Code == http.StatusNotFound:
	case err != nil:
		return err
	default:
		if err := json.Unmarshal(data, &plan); err != nil {
			return err
		}
	}
	if changed := e.vault.changedSince(plan); len(changed) > 0 {
		return fmt.Errorf("the secret in %s changed since the plan: plan again", strings.Join(changed, ", "))
	}
	return nil
}

// needsPrepare reports whether the phase has files or variables to prepare.
func (e *executor) needsPrepare() bool {
	if e.decl != nil {
		return true
	}
	for _, v := range e.spec.Variables {
		if v.Category == api.CategoryFile {
			return true
		}
	}
	return false
}

// loggedError is an error already written to the run's log.
type loggedError struct{ error }

func (e loggedError) Unwrap() error { return e.error }

// runSteps runs steps in order, reporting each to the control plane so the UI
// can show them and attribute their logs. After a failure the remaining steps
// are reported as skipped.
func (e *executor) runSteps(ctx context.Context, steps []step) error {
	for i, st := range steps {
		if st.skip != nil && st.skip() {
			continue
		}
		if err := e.reportStep(ctx, st.name, api.StepRunning); err != nil {
			return err
		}
		err := st.run()
		status := api.StepSucceeded
		if err != nil {
			status = api.StepFailed
			fmt.Fprintf(e.out, "\n==> Error: %v\n", err) // inside the step, so it shows in its log
			err = loggedError{err}
		}
		if rerr := e.reportStep(ctx, st.name, status); rerr != nil && err == nil {
			err = rerr
		}
		if err != nil {
			for _, rest := range steps[i+1:] {
				_ = e.reportStep(ctx, rest.name, api.StepSkipped)
			}
			return err
		}
	}
	return nil
}

// reportStep flushes the log first, so output lands on the step that produced
// it. Only losing ownership of the run is fatal; other reporting errors are logged.
func (e *executor) reportStep(ctx context.Context, name string, status api.StepStatus) error {
	if e.logs != nil {
		e.logs.Flush()
	}
	err := e.client.Step(ctx, api.StepRequest{Name: name, Kind: api.StepCore, Status: status})
	if errors.Is(err, apiclient.ErrNoLongerOwner) {
		return err
	}
	if err != nil {
		fmt.Fprintf(e.out, "==> Warning: could not report step %s: %v\n", name, err)
	}
	return nil
}

func (e *executor) plan(ctx context.Context, r *runner, planPath string) (api.CompleteRequest, error) {
	var changes []api.ResourceChange
	var hasChanges bool
	var err error
	if e.stack.RunAll {
		hasChanges, changes, err = e.planAll(ctx, r)
	} else {
		hasChanges, changes, err = e.planOne(ctx, r, planPath)
	}
	if err != nil {
		return api.CompleteRequest{}, err
	}
	res := api.CompleteRequest{Success: true, HasChanges: hasChanges}
	if !hasChanges {
		if e.spec.Type == api.RunDrift {
			fmt.Fprintln(e.out, "==> No drift: the infrastructure matches the code")
		} else {
			fmt.Fprintln(e.out, "==> No changes")
		}
		return res, nil
	}

	res.PlanSummary = Summarize(changes)
	label := "Plan"
	if e.spec.Type == api.RunDrift {
		label = "Drift detected"
	}
	sum := res.PlanSummary
	fmt.Fprintf(e.out, "==> %s: %d to create, %d to update, %d to replace, %d to delete\n",
		label, sum.Add, sum.Change, sum.Replace, sum.Destroy)
	if err := e.client.PutChanges(ctx, changes); err != nil {
		return api.CompleteRequest{}, fmt.Errorf("uploading the plan's changes: %w", err)
	}
	if e.spec.Type.Applies() {
		var data []byte
		if e.stack.RunAll {
			data, err = packDir(e.allPlansDir())
		} else {
			data, err = os.ReadFile(planPath)
		}
		if err != nil {
			return api.CompleteRequest{}, err
		}
		if err := e.client.PutArtifact(ctx, "plan", data); err != nil {
			return api.CompleteRequest{}, fmt.Errorf("uploading plan: %w", err)
		}
	}
	return res, nil
}

func (e *executor) planArgs(extra ...string) []string {
	args := append([]string{"plan", "-input=false", "-no-color", "-detailed-exitcode"}, extra...)
	if e.spec.Type == api.RunDestroy {
		args = append(args, "-destroy")
	}
	return args
}

// planOne plans the single root module (or Terragrunt unit) in the stack's directory.
func (e *executor) planOne(ctx context.Context, r *runner, planPath string) (bool, []api.ResourceChange, error) {
	code, err := r.run(ctx, e.planArgs("-out="+planPath)...)
	if code != 0 && code != 2 {
		return false, nil, err
	}
	if code == 0 {
		return false, nil, nil
	}
	raw, err := r.capture(ctx, "show", "-json", "-no-color", planPath)
	if err != nil {
		return false, nil, fmt.Errorf("reading plan: %w", err)
	}
	changes, err := ExtractChanges(raw, "", e.mask)
	if err != nil {
		return false, nil, fmt.Errorf("parsing plan: %w", err)
	}
	return true, changes, nil
}

func (e *executor) allPlansDir() string { return filepath.Join(e.cfg.WorkDir, "plans") }

func (e *executor) plansJSONDir() string { return filepath.Join(e.cfg.WorkDir, "plans-json") }

// planAll runs `terragrunt run --all` over every unit below the stack's
// directory. Terragrunt saves each unit's plan (and its JSON) under a
// directory tree mirroring the units, which travels to the apply phase whole.
func (e *executor) planAll(ctx context.Context, r *runner) (bool, []api.ResourceChange, error) {
	jsonDir := e.plansJSONDir()
	args := append([]string{"run", "--all", "--non-interactive", "--out-dir", e.allPlansDir(), "--json-out-dir", jsonDir, "--"},
		e.planArgs()...)
	code, err := r.run(ctx, args...)
	if code != 0 && code != 2 { // with --all, 2 means at least one unit has changes
		return false, nil, err
	}
	if code == 0 {
		return false, nil, nil
	}
	var changes []api.ResourceChange
	err = filepath.WalkDir(jsonDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() != "tfplan.json" {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		unit, err := filepath.Rel(jsonDir, filepath.Dir(path))
		if err != nil {
			return err
		}
		unitChanges, err := ExtractChanges(raw, filepath.ToSlash(unit), e.mask)
		if err != nil {
			return fmt.Errorf("parsing the plan of %s: %w", unit, err)
		}
		changes = append(changes, unitChanges...)
		return nil
	})
	if err != nil {
		return false, nil, err
	}
	return true, changes, nil
}

func (e *executor) apply(ctx context.Context, r *runner, planPath string) (api.CompleteRequest, error) {
	data, err := e.client.GetArtifact(ctx, "plan")
	if err != nil {
		return api.CompleteRequest{}, fmt.Errorf("downloading plan: %w", err)
	}
	if e.stack.RunAll {
		if err := unpackDir(data, e.allPlansDir()); err != nil {
			return api.CompleteRequest{}, fmt.Errorf("unpacking plans: %w", err)
		}
		// With --out-dir, Terragrunt applies each unit's saved plan.
		if _, err := r.run(ctx, "run", "--all", "--non-interactive", "--out-dir", e.allPlansDir(), "--",
			"apply", "-input=false", "-no-color"); err != nil {
			return api.CompleteRequest{}, err
		}
		return api.CompleteRequest{Success: true}, nil
	}
	if err := os.WriteFile(planPath, data, 0o600); err != nil {
		return api.CompleteRequest{}, err
	}
	if _, err := r.run(ctx, "apply", "-input=false", "-no-color", "-auto-approve", planPath); err != nil {
		return api.CompleteRequest{}, err
	}
	return api.CompleteRequest{Success: true}, nil
}

func terragruntSuffix(c *api.StackConfig) string {
	switch {
	case c.TerragruntVersion == "":
		return ""
	case c.RunAll:
		return " with terragrunt " + c.TerragruntVersion + " (run --all)"
	}
	return " with terragrunt " + c.TerragruntVersion
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
		// Logs are shown in a browser: no ANSI color codes.
		"TG_NO_COLOR=true",
		"NO_COLOR=1",
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
