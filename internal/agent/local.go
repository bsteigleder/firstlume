package agent

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/bsteigleder/firstlume/internal/api"
)

// LocalDriver runs each executor as a subprocess of the agent. It is meant for
// development and for hosts without Kubernetes; it provides no isolation.
type LocalDriver struct {
	Binary        string // path to the firstlume binary
	ExecutorAPI   string
	WorkDir       string
	ToolsDir      string
	Output        io.Writer
	KeepWorkspace bool

	mu       sync.Mutex
	active   int
	failures []Failure
}

func (d *LocalDriver) Start(ctx context.Context, a *api.Assignment) error {
	if err := os.MkdirAll(d.WorkDir, 0o755); err != nil {
		return err
	}
	ws, err := os.MkdirTemp(d.WorkDir, "run-"+a.RunID[:8]+"-"+string(a.Phase)+"-")
	if err != nil {
		return err
	}
	// Executors outlive a single poll, so they must not be tied to the request context.
	cmd := exec.Command(d.Binary, "exec")
	cmd.Env = append(executorBaseEnv(),
		"FIRSTLUME_API_URL="+d.ExecutorAPI,
		"FIRSTLUME_RUN_TOKEN="+a.Token,
		"FIRSTLUME_WORK_DIR="+ws,
		"FIRSTLUME_TOOLS_DIR="+d.ToolsDir,
	)
	cmd.Stdout = d.Output
	cmd.Stderr = d.Output
	if err := cmd.Start(); err != nil {
		os.RemoveAll(ws)
		return err
	}

	d.mu.Lock()
	d.active++
	d.mu.Unlock()
	go func() {
		err := cmd.Wait()
		if !d.KeepWorkspace {
			os.RemoveAll(ws)
		}
		d.mu.Lock()
		defer d.mu.Unlock()
		d.active--
		if err != nil {
			d.failures = append(d.failures, Failure{RunID: a.RunID, Phase: a.Phase, Reason: fmt.Sprintf("executor exited: %v", err)})
		}
	}()
	return nil
}

func (d *LocalDriver) Active(context.Context) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.active, nil
}

func (d *LocalDriver) Failures(context.Context) ([]Failure, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := d.failures
	d.failures = nil
	return out, nil
}

// executorBaseEnv passes the agent's environment through without its own
// credentials, which executors must never see.
func executorBaseEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "FIRSTLUME_") {
			env = append(env, kv)
		}
	}
	return env
}
