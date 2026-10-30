// Package agent polls the control plane for work and launches executors.
//
// The agent only makes outbound requests, so it can run inside a private
// network (another cluster, a customer VPC) without the control plane ever
// needing to reach it.
package agent

import (
	"context"
	"log/slog"
	"time"

	"github.com/bsteigleder/firstlume/internal/api"
	"github.com/bsteigleder/firstlume/internal/apiclient"
)

// Driver launches executors and reports the ones that died without reporting.
type Driver interface {
	Start(ctx context.Context, a *api.Assignment) error
	// Active returns how many executors are still running.
	Active(ctx context.Context) (int, error)
	// Failures returns executors that ended abnormally since the last call.
	Failures(ctx context.Context) ([]Failure, error)
}

type Failure struct {
	RunID  string
	Phase  api.Phase
	Reason string
}

type Config struct {
	Concurrency  int
	PollInterval time.Duration
}

type Agent struct {
	cfg    Config
	client *apiclient.Client
	driver Driver
	log    *slog.Logger
}

func New(cfg Config, client *apiclient.Client, driver Driver, log *slog.Logger) *Agent {
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 4
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 5 * time.Second
	}
	return &Agent{cfg: cfg, client: client, driver: driver, log: log}
}

func (a *Agent) Run(ctx context.Context) error {
	a.log.Info("agent started", "concurrency", a.cfg.Concurrency, "poll_interval", a.cfg.PollInterval)
	for {
		claimed := a.tick(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if claimed {
			continue // there may be more work queued; ask again right away
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(a.cfg.PollInterval):
		}
	}
}

// tick reports dead executors, then claims and starts at most one phase.
func (a *Agent) tick(ctx context.Context) bool {
	failures, err := a.driver.Failures(ctx)
	if err != nil {
		a.log.Error("checking executors", "err", err)
	}
	for _, f := range failures {
		a.log.Warn("executor failed", "run_id", f.RunID, "phase", f.Phase, "reason", f.Reason)
		if err := a.client.ReportFailure(ctx, f.RunID, f.Phase, f.Reason); err != nil {
			a.log.Error("reporting executor failure", "run_id", f.RunID, "err", err)
		}
	}

	active, err := a.driver.Active(ctx)
	if err != nil {
		a.log.Error("counting executors", "err", err)
		return false
	}
	if active >= a.cfg.Concurrency {
		return false
	}

	asg, err := a.client.Claim(ctx)
	if err != nil {
		if ctx.Err() == nil {
			a.log.Error("claiming work", "err", err)
		}
		return false
	}
	if asg == nil {
		return false
	}
	a.log.Info("starting executor", "run_id", asg.RunID, "phase", asg.Phase)
	if err := a.driver.Start(ctx, asg); err != nil {
		a.log.Error("starting executor", "run_id", asg.RunID, "err", err)
		if rerr := a.client.ReportFailure(ctx, asg.RunID, asg.Phase, "agent could not start executor: "+err.Error()); rerr != nil {
			a.log.Error("reporting start failure", "run_id", asg.RunID, "err", rerr)
		}
	}
	return true
}
