package server

import (
	"context"
	"errors"
	"path"
	"strings"
	"time"

	"github.com/bsteigleder/firstlume/internal/api"
	"github.com/bsteigleder/firstlume/internal/gitprovider"
	"github.com/bsteigleder/firstlume/internal/stackconfig"
	"github.com/bsteigleder/firstlume/internal/store"
)

// gitEventRetention is how long events are kept (spec 0006, R10.3).
const gitEventRetention = 30 * 24 * time.Hour

// processGitEvents works through accepted webhooks. Every replica runs it; a
// claim skips what another replica is working on.
func (s *Server) processGitEvents(ctx context.Context) {
	poll := time.NewTicker(5 * time.Second)
	defer poll.Stop()
	purge := time.NewTicker(time.Hour)
	defer purge.Stop()
	for {
		s.drainGitEvents(ctx)
		select {
		case <-ctx.Done():
			return
		case <-s.gitEvents:
		case <-poll.C:
		case <-purge.C:
			if _, err := s.store.PurgeGitEvents(ctx, gitEventRetention); err != nil {
				s.log.Error("purging git events", "err", err)
			}
		}
	}
}

// drainGitEvents processes the events that are ready, until none is left.
func (s *Server) drainGitEvents(ctx context.Context) {
	for ctx.Err() == nil {
		ev, err := s.store.ClaimGitEvent(ctx)
		if errors.Is(err, store.ErrNotFound) {
			return
		}
		if err == nil {
			err = s.processGitEvent(ctx, ev)
		} else if ev == nil {
			s.log.Error("claiming a git event", "err", err)
			return
		}
		if err != nil {
			s.log.Warn("processing a git event failed", "event", ev.ID, "attempt", ev.Attempts, "err", err)
			if err := s.store.RetryGitEvent(ctx, ev.ID, err); err != nil {
				s.log.Error("retrying a git event", "event", ev.ID, "err", err)
				return
			}
		}
	}
}

// processGitEvent decides what a push means for every stack of its repository
// and records why. An error means the event should be tried again; what it
// has already created is not created twice.
func (s *Server) processGitEvent(ctx context.Context, ev *store.ClaimedGitEvent) error {
	all, err := s.store.StacksOfConnection(ctx, ev.ConnectionID)
	if err != nil {
		return err
	}
	client, err := gitprovider.New(ev.Connection.Provider, ev.Connection.URL, ev.Connection.Token)
	if err != nil {
		return err
	}

	p := &pushProcessor{srv: s, ev: ev, client: client}
	decisions := []api.GitEventStack{}
	for _, st := range all {
		if !strings.EqualFold(repoPathOf(ev.Connection.URL, st.Repository), ev.Repository) {
			continue
		}
		d := api.GitEventStack{StackID: st.ID, StackName: st.Name}
		if err := p.decide(ctx, st, &d); err != nil {
			if !errors.Is(err, store.ErrNotFound) { // a stack deleted meanwhile has nothing to decide
				return err
			}
			continue
		}
		decisions = append(decisions, d)
	}
	return s.store.FinishGitEvent(ctx, ev.ID, p.affectsAll, decisions)
}

// pushProcessor holds what one push needs across its stacks: the provider is
// asked only when a stack has a trigger on, and only once.
type pushProcessor struct {
	srv    *Server
	ev     *store.ClaimedGitEvent
	client gitprovider.Provider

	changesRead bool
	files       []string
	affectsAll  string

	pullRead bool
	pull     int
	pullURL  string
}

func (p *pushProcessor) decide(ctx context.Context, st store.ConnectionStack, d *api.GitEventStack) error {
	if st.Ref != p.ev.Ref {
		d.Outcome = api.OutcomeOtherBranch
		return nil
	}
	mode, err := p.srv.store.GitOnPush(ctx, p.ev.OrganizationID, st.ID)
	if err != nil {
		return err
	}
	if mode == api.GitOnPushOff {
		d.Outcome = api.OutcomeTriggerOff
		return nil
	}
	p.readChanges(ctx)
	// A delivery that repeats a push, like the provider's test button, ends
	// on a commit the stack already had a decision for. A force push back to
	// an old commit isn't a repeat (spec 0010, R5.9).
	if p.affectsAll != api.AffectedForcePush {
		handled, err := p.srv.store.AlreadyHandled(ctx, p.ev.ConnectionID, p.ev.Repository, st.ID, p.ev.BeforeSHA, p.ev.AfterSHA)
		if err != nil {
			return err
		}
		if handled {
			d.Outcome = api.OutcomeAlreadyHandled
			return nil
		}
	}

	switch reason := p.affected(ctx, st); reason {
	case "":
		d.Outcome, d.Files = api.OutcomeNotAffected, p.files
		return nil
	default:
		d.Reason, d.Files = reason, p.files
	}

	typ := api.RunPlan
	if mode == api.GitOnPushApply {
		typ = api.RunApply
	}
	p.readPull(ctx)
	run, created, err := p.srv.store.CreatePushRun(ctx, store.PushRun{
		OrganizationID: p.ev.OrganizationID, StackID: st.ID, EventID: p.ev.ID, Type: typ,
		BeforeSHA: p.ev.BeforeSHA, AfterSHA: p.ev.AfterSHA, Actor: p.ev.Sender,
		PullRequest: p.pull, PullRequestURL: p.pullURL,
	})
	if err != nil {
		return err
	}
	d.RunID = run.ID
	d.Outcome = api.OutcomeRunCreated
	if !created {
		d.Outcome = api.OutcomeDuplicate
	}
	return nil
}

// affected returns why the stack counts as affected, or "" when it doesn't.
func (p *pushProcessor) affected(ctx context.Context, st store.ConnectionStack) string {
	if p.affectsAll != "" {
		return p.affectsAll
	}
	// Read at the event's commit. A file that can't be read or understood
	// counts as affected, so the run fails and shows why instead of nothing
	// happening (R4.5).
	data, err := p.client.File(ctx, p.ev.Repository, p.ev.AfterSHA, path.Join(st.Path, stackconfig.RelPath))
	if err != nil {
		p.srv.log.Warn("reading a stack's config for a push", "stack", st.ID, "commit", p.ev.AfterSHA, "err", err)
		return api.AffectedConfigUnreadable
	}
	cfg, err := stackconfig.Parse(data)
	if err != nil {
		return api.AffectedConfigUnreadable
	}
	if stackconfig.Affected(st.Path, cfg.Triggers, p.files) {
		return api.AffectedFiles
	}
	return ""
}

// readChanges finds which files the push changed. When there's no knowing,
// every stack of the branch counts as affected: running too much ends in "no
// changes", running too little would leave a change unapplied (R4.3, R4.4).
func (p *pushProcessor) readChanges(ctx context.Context) {
	if p.changesRead {
		return
	}
	p.changesRead = true
	if zeroSHA(p.ev.BeforeSHA) {
		p.affectsAll = api.AffectedNewBranch
		return
	}
	// Commits only the old head has mean history was rewritten: comparing
	// the two ways would only show the new ones.
	back, err := p.client.Compare(ctx, p.ev.Repository, p.ev.AfterSHA, p.ev.BeforeSHA)
	if err != nil {
		p.srv.log.Warn("comparing a push", "event", p.ev.ID, "err", err)
		p.affectsAll = api.AffectedCompareFailed
		return
	}
	if back.Commits > 0 {
		p.affectsAll = api.AffectedForcePush
		return
	}
	cmp, err := p.client.Compare(ctx, p.ev.Repository, p.ev.BeforeSHA, p.ev.AfterSHA)
	if err != nil {
		p.srv.log.Warn("comparing a push", "event", p.ev.ID, "err", err)
		p.affectsAll = api.AffectedCompareFailed
		return
	}
	if cmp.Incomplete || cmp.Commits < p.ev.TotalCommits {
		p.affectsAll = api.AffectedCompareIncomplete
		return
	}
	p.files = cmp.Files
}

// readPull finds the pull request the commit came from, when there is one. Not
// finding it doesn't stop the run (R5.2).
func (p *pushProcessor) readPull(ctx context.Context) {
	if p.pullRead {
		return
	}
	p.pullRead = true
	number, u, err := p.client.CommitPull(ctx, p.ev.Repository, p.ev.AfterSHA)
	if err != nil {
		return
	}
	p.pull, p.pullURL = number, u
}
