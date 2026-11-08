package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/bsteigleder/firstlume/internal/api"
)

// activeStatuses are the statuses of a run that holds its stack's lock or waits for it.
const activeStatuses = `('queued', 'planning', 'awaiting_approval', 'apply_queued', 'applying')`

func runColumnsAs(alias string) string {
	cols := []string{"id", "stack_id", "type", "status", "ref", "commit_sha", "commit_author_name", "commit_author_email",
		"commit_message", "has_changes", "plan_summary", "error", "agent_id", "created_by", "approved_by", "approved_at",
		"started_at", "finished_at", "created_at", "updated_at", "acknowledged", "config", "settings_used", "unit_backends", "placement", "placement_used",
		"trigger_kind", "trigger_actor", "trigger_event_id", "trigger_before_sha", "trigger_pull_request", "trigger_pull_request_url",
		"stop_requested_at", "stop_requested_by", "command", "state_access", "exit_code", "failed_line"}
	for i, c := range cols {
		cols[i] = alias + "." + c
	}
	return strings.Join(cols, ", ")
}

var runColumns = runColumnsAs("r")

// runScan reads run columns that may all be NULL (from a LEFT JOIN).
type runScan struct {
	id, stackID, typ, status, ref, commit, authorName, authorEmail, message    *string
	hasChanges                                                                 *bool
	summary                                                                    []byte
	errMsg, agentID, createdBy, approvedBy                                     *string
	approvedAt, startedAt, finishedAt, createdAt, updatedAt                    *time.Time
	acknowledged, config, settingsUsed, unitBackends, placement, placementUsed []byte
	triggerKind, triggerActor, triggerEvent, triggerBefore, triggerPRURL       *string
	triggerPR                                                                  *int
	stopAt                                                                     *time.Time
	stopBy                                                                     *string
	command, stateAccess                                                       *string
	exitCode, failedLine                                                       *int
}

func newRunScan() *runScan { return &runScan{} }

func (s *runScan) dest() []any {
	return []any{&s.id, &s.stackID, &s.typ, &s.status, &s.ref, &s.commit, &s.authorName, &s.authorEmail, &s.message,
		&s.hasChanges, &s.summary, &s.errMsg, &s.agentID, &s.createdBy, &s.approvedBy, &s.approvedAt,
		&s.startedAt, &s.finishedAt, &s.createdAt, &s.updatedAt, &s.acknowledged, &s.config, &s.settingsUsed, &s.unitBackends, &s.placement, &s.placementUsed,
		&s.triggerKind, &s.triggerActor, &s.triggerEvent, &s.triggerBefore, &s.triggerPR, &s.triggerPRURL,
		&s.stopAt, &s.stopBy, &s.command, &s.stateAccess, &s.exitCode, &s.failedLine}
}

func (s *runScan) result() (*api.Run, error) {
	r := &api.Run{
		ID: deref(s.id), StackID: deref(s.stackID), Type: api.RunType(deref(s.typ)), Status: api.RunStatus(deref(s.status)),
		Ref: deref(s.ref), CommitSHA: deref(s.commit), CommitAuthorName: deref(s.authorName),
		CommitAuthorEmail: deref(s.authorEmail), CommitMessage: deref(s.message), HasChanges: s.hasChanges,
		Error: deref(s.errMsg), AgentID: deref(s.agentID), CreatedBy: deref(s.createdBy), ApprovedBy: deref(s.approvedBy),
		ApprovedAt: s.approvedAt, StartedAt: s.startedAt, FinishedAt: s.finishedAt,
		StopRequestedAt: s.stopAt, StopRequestedBy: deref(s.stopBy),
		Command: deref(s.command), StateAccess: deref(s.stateAccess), ExitCode: s.exitCode, FailedLine: s.failedLine,
	}
	if s.createdAt != nil {
		r.CreatedAt = *s.createdAt
	}
	r.Trigger = s.trigger(r.Type)
	if s.updatedAt != nil {
		r.UpdatedAt = *s.updatedAt
	}
	if s.summary != nil {
		r.PlanSummary = &api.PlanSummary{}
		if err := json.Unmarshal(s.summary, r.PlanSummary); err != nil {
			return nil, err
		}
	}
	if len(s.placement) > 0 && !bytes.Equal(s.placement, []byte("{}")) {
		r.Placement = &api.EffectivePlacement{}
		if err := json.Unmarshal(s.placement, r.Placement); err != nil {
			return nil, err
		}
	}
	if s.placementUsed != nil {
		r.PlacementUsed = &api.EffectivePlacement{}
		if err := json.Unmarshal(s.placementUsed, r.PlacementUsed); err != nil {
			return nil, err
		}
	}
	if s.acknowledged != nil {
		if err := json.Unmarshal(s.acknowledged, &r.Acknowledged); err != nil {
			return nil, err
		}
	}
	if s.config != nil {
		r.Config = &api.StackConfig{}
		if err := json.Unmarshal(s.config, r.Config); err != nil {
			return nil, err
		}
	}
	if s.settingsUsed != nil {
		r.SettingsUsed = &api.RunSettings{}
		if err := json.Unmarshal(s.settingsUsed, r.SettingsUsed); err != nil {
			return nil, err
		}
	}
	if s.unitBackends != nil {
		if err := json.Unmarshal(s.unitBackends, &r.UnitBackends); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// trigger reads why the run exists. Runs not started by Git have no columns set.
func (s *runScan) trigger(typ api.RunType) *api.RunTrigger {
	switch {
	case s.id == nil:
		return nil
	case s.triggerKind == nil && typ == api.RunDrift:
		return &api.RunTrigger{Kind: api.TriggerDrift}
	case s.triggerKind == nil:
		return &api.RunTrigger{Kind: api.TriggerManual}
	}
	t := &api.RunTrigger{Kind: *s.triggerKind, Actor: deref(s.triggerActor), EventID: deref(s.triggerEvent),
		BeforeSHA: deref(s.triggerBefore), PullRequestURL: deref(s.triggerPRURL)}
	if s.triggerPR != nil {
		t.PullRequest = *s.triggerPR
	}
	return t
}

func scanRun(row pgx.Row) (*api.Run, error) {
	s := newRunScan()
	if err := row.Scan(s.dest()...); err != nil {
		return nil, notFound(err)
	}
	return s.result()
}

// CreateRun queues a run. The ref defaults to the stack's.
func (s *Store) CreateRun(ctx context.Context, orgID, stackID string, typ api.RunType, ref, createdBy string) (*api.Run, error) {
	placement, err := runPlacement(ctx, s.pool, orgID, stackID)
	if err != nil {
		return nil, err
	}
	return scanRun(s.pool.QueryRow(ctx, `
		WITH inserted AS (
			INSERT INTO runs (organization_id, stack_id, type, status, ref, created_by, placement)
			SELECT st.organization_id, st.id, $3, 'queued', coalesce(nullif($4, ''), st.ref), $5, $6::jsonb
			FROM stacks st WHERE st.organization_id = $1 AND st.id = $2
			RETURNING *
		)
		SELECT `+runColumns+` FROM inserted r`,
		orgID, stackID, typ, ref, createdBy, placement))
}

// CreateCommandRun queues a command run (spec 0009). The command was checked
// by the caller. commit, when the caller could resolve the ref, fixes what
// runs, so what is approved is what runs even if the branch moves.
func (s *Store) CreateCommandRun(ctx context.Context, orgID, stackID, ref, commit, command, access, createdBy string) (*api.Run, error) {
	placement, err := runPlacement(ctx, s.pool, orgID, stackID)
	if err != nil {
		return nil, err
	}
	var run *api.Run
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// The stack's row serializes this with claims (ClaimRun locks it too).
		var busy bool
		err := tx.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM runs r WHERE r.stack_id = st.id AND r.status IN `+activeStatuses+`)
			FROM stacks st WHERE st.organization_id = $1 AND st.id = $2 FOR UPDATE`, orgID, stackID).Scan(&busy)
		if err != nil {
			return notFound(err)
		}
		// A write that needs approval and has its turn already waits for it,
		// so the approver sees it at once; otherwise ClaimRun does it when its
		// turn comes (R9).
		status := api.StatusQueued
		if access == "write" && !busy {
			eff, _, err := s.effective(ctx, tx, orgID, api.Level{Kind: api.LevelStack, ID: stackID})
			if err != nil {
				return err
			}
			if eff.RequireApproval.Value {
				status = api.StatusAwaitingApproval
			}
		}
		run, err = scanRun(tx.QueryRow(ctx, `
			WITH inserted AS (
				INSERT INTO runs (organization_id, stack_id, type, status, ref, commit_sha, created_by, placement, command, state_access)
				SELECT st.organization_id, st.id, 'command', $9, coalesce(nullif($3, ''), st.ref), nullif($4, ''), $5, $6::jsonb, $7, $8
				FROM stacks st WHERE st.organization_id = $1 AND st.id = $2
				RETURNING *
			)
			SELECT `+runColumns+` FROM inserted r`,
			orgID, stackID, ref, commit, createdBy, placement, command, access, status))
		return err
	})
	return run, err
}

// RunStateWrites lists the state versions a run wrote, unit by unit.
func (s *Store) RunStateWrites(ctx context.Context, orgID, runID string) ([]api.StateWrite, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT u.key, max(v.number) FROM state_versions v
		JOIN state_units u ON u.id = v.unit_id
		WHERE u.organization_id = $1 AND v.run_id = $2
		GROUP BY u.key ORDER BY u.key`, orgID, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []api.StateWrite
	for rows.Next() {
		var w api.StateWrite
		if err := rows.Scan(&w.Key, &w.Version); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// ScheduleDriftRuns queues a drift run for every stack whose drift check is
// due under its effective interval and that has no run in progress (a busy
// one is retried next time). It is safe to call from several server replicas.
func (s *Store) ScheduleDriftRuns(ctx context.Context) (int64, error) {
	type candidate struct {
		id, orgID string
		scheduled *time.Time
	}
	rows, err := s.pool.Query(ctx, `
		SELECT st.id, st.organization_id, st.drift_scheduled_at FROM stacks st
		WHERE NOT EXISTS (SELECT 1 FROM runs r WHERE r.stack_id = st.id AND r.status IN `+activeStatuses+`)`)
	if err != nil {
		return 0, err
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.orgID, &c.scheduled); err != nil {
			rows.Close()
			return 0, err
		}
		candidates = append(candidates, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	var queued int64
	for _, c := range candidates {
		eff, _, err := s.effective(ctx, s.pool, c.orgID, api.Level{Kind: api.LevelStack, ID: c.id})
		if err == ErrNotFound {
			continue // deleted meanwhile
		}
		if err != nil {
			return queued, err
		}
		interval := eff.DriftIntervalMinutes.Value
		if interval == 0 || (c.scheduled != nil && time.Since(*c.scheduled) < time.Duration(interval)*time.Minute) {
			continue
		}
		placement, err := json.Marshal(eff.Placement)
		if err != nil {
			return queued, err
		}
		tag, err := s.pool.Exec(ctx, `
			WITH due AS (
				SELECT st.id, st.organization_id, st.ref FROM stacks st
				WHERE st.id = $1
				  AND coalesce(st.drift_scheduled_at, '-infinity') <= now() - make_interval(mins => $2)
				  AND NOT EXISTS (SELECT 1 FROM runs r WHERE r.stack_id = st.id AND r.status IN `+activeStatuses+`)
				FOR UPDATE SKIP LOCKED
			), marked AS (
				UPDATE stacks st SET drift_scheduled_at = now() FROM due WHERE st.id = due.id
				RETURNING st.id, st.organization_id, st.ref
			)
			INSERT INTO runs (organization_id, stack_id, type, status, ref, created_by, placement)
			SELECT organization_id, id, 'drift', 'queued', ref, $3, $4::jsonb FROM marked`, c.id, interval, api.DriftActor, placement)
		if err != nil {
			return queued, err
		}
		queued += tag.RowsAffected()
	}
	return queued, nil
}

// ListActiveRuns returns an organization's active runs: those of the person
// asking first (started by login, or a commit by email; R10.5), then within
// each group those awaiting approval and the most recent. With failedWithin > 0
// it also returns the runs that failed within that window, after the active
// ones. Empty login and email put no run first.
func (s *Store) ListActiveRuns(ctx context.Context, orgID string, failedWithin time.Duration, login, email string) ([]api.ActiveRun, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+runColumns+`, st.name,
		       coalesce((SELECT array_agg(f.name ORDER BY array_position(pf.path, f.id)) FROM folders f
		                 WHERE f.id = ANY(pf.path)), '{}')
		FROM runs r
		JOIN stacks st ON st.id = r.stack_id
		LEFT JOIN folders pf ON pf.id = st.folder_id
		WHERE r.organization_id = $1
		  AND (r.status IN `+activeStatuses+`
		       OR ($2 > 0 AND r.status = 'failed' AND r.finished_at > now() - make_interval(secs => $2)))
		ORDER BY r.status = 'failed',
		         (($3 <> '' AND r.created_by = $3) OR ($4 <> '' AND lower(coalesce(r.commit_author_email, '')) = lower($4))) DESC,
		         r.status = 'awaiting_approval' DESC, coalesce(r.finished_at, r.created_at) DESC`,
		orgID, failedWithin.Seconds(), login, email)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []api.ActiveRun{}
	for rows.Next() {
		sc := newRunScan()
		var a api.ActiveRun
		if err := rows.Scan(append(sc.dest(), &a.StackName, &a.FolderPath)...); err != nil {
			return nil, err
		}
		run, err := sc.result()
		if err != nil {
			return nil, err
		}
		a.Run = *run
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) GetRun(ctx context.Context, orgID, id string) (*api.Run, error) {
	return scanRun(s.pool.QueryRow(ctx,
		`SELECT `+runColumns+` FROM runs r WHERE r.organization_id = $1 AND r.id = $2`, orgID, id))
}

func (s *Store) ListRuns(ctx context.Context, orgID, stackID string, limit int) ([]api.Run, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+runColumns+` FROM runs r
		WHERE r.organization_id = $1 AND r.stack_id = $2
		ORDER BY r.created_at DESC LIMIT $3`, orgID, stackID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []api.Run{}
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// transition moves a run between statuses atomically, returning ErrConflict when
// the run is not in one of the expected statuses.
func (s *Store) transition(ctx context.Context, orgID, id string, from []api.RunStatus, set string, args ...any) (*api.Run, error) {
	fromStrs := make([]string, len(from))
	for i, f := range from {
		fromStrs[i] = string(f)
	}
	params := append([]any{orgID, id, fromStrs}, args...)
	r, err := scanRun(s.pool.QueryRow(ctx, `
		WITH updated AS (
			UPDATE runs SET `+set+`, updated_at = now()
			WHERE organization_id = $1 AND id = $2 AND status = ANY($3)
			RETURNING *
		)
		SELECT `+runColumns+` FROM updated r`, params...))
	if errors.Is(err, ErrNotFound) {
		if _, getErr := s.GetRun(ctx, orgID, id); getErr != nil {
			return nil, getErr
		}
		return nil, ErrConflict
	}
	return r, err
}

// MissingAckError means the stack requires confirming each deleted or
// replaced resource, and these were not confirmed.
type MissingAckError struct {
	Missing []string
}

func (e *MissingAckError) Error() string {
	return fmt.Sprintf("confirm every resource this plan deletes or replaces before approving; missing: %s",
		strings.Join(e.Missing, ", "))
}

// ApproveRun queues the apply of an awaiting plan. When the stack's effective
// settings require it, every resource the plan deletes or replaces must be in
// acknowledged; the confirmed ones are recorded on the run.
func (s *Store) ApproveRun(ctx context.Context, orgID, id, by string, acknowledged []string) (*api.Run, error) {
	run, err := s.GetRun(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	if run.Status != api.StatusAwaitingApproval {
		return nil, ErrConflict
	}
	eff, _, err := s.effective(ctx, s.pool, orgID, api.Level{Kind: api.LevelStack, ID: run.StackID})
	if err != nil {
		return nil, err
	}
	var recorded []byte
	if eff.ConfirmDestructive.Value {
		changes, err := s.GetChanges(ctx, orgID, id)
		if err != nil {
			return nil, err
		}
		acked := map[string]bool{}
		for _, a := range acknowledged {
			acked[a] = true
		}
		missing, confirmed := []string{}, []string{}
		for _, c := range changes {
			if !c.Action.Destructive() {
				continue
			}
			if acked[c.Key()] {
				confirmed = append(confirmed, c.Key())
			} else {
				missing = append(missing, c.Key())
			}
		}
		if len(missing) > 0 {
			return nil, &MissingAckError{Missing: missing}
		}
		if recorded, err = json.Marshal(confirmed); err != nil {
			return nil, err
		}
	}
	return s.transition(ctx, orgID, id, []api.RunStatus{api.StatusAwaitingApproval},
		`status = 'apply_queued', approved_by = $4, approved_at = now(), acknowledged = $5`, by, recorded)
}

// SetChanges records what the plan changes. Only the executor of the plan phase can set it.
func (s *Store) SetChanges(ctx context.Context, runID string, changes []api.ResourceChange) error {
	data, err := json.Marshal(changes)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `UPDATE runs SET changes = $2, heartbeat_at = now() WHERE id = $1 AND status = 'planning'`,
		runID, data)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrConflict
	}
	return nil
}

// SetUnitBackends records where each unit keeps its state. Only the executor
// of the plan phase can set it.
func (s *Store) SetUnitBackends(ctx context.Context, runID string, backends []api.UnitBackend) error {
	data, err := json.Marshal(backends)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `UPDATE runs SET unit_backends = $2, heartbeat_at = now() WHERE id = $1 AND status = 'planning'`,
		runID, data)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrConflict
	}
	return nil
}

// RecordStep starts a step (status running), records a skipped one, or
// finishes the running step of that name, for the phase being executed. Logs
// shipped while a step runs are tagged with it.
func (s *Store) RecordStep(ctx context.Context, runID string, phase api.Phase, req api.StepRequest) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT true FROM runs WHERE id = $1 AND status = $2 FOR UPDATE`,
			runID, statusForPhase(phase)).Scan(&ok); err != nil {
			if notFound(err) == ErrNotFound {
				return ErrConflict
			}
			return err
		}
		switch req.Status {
		case api.StepRunning, api.StepSkipped:
			var started any
			if req.Status == api.StepRunning {
				started = time.Now()
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO run_steps (run_id, seq, phase, name, kind, status, started_at)
				SELECT $1, coalesce(max(seq), 0) + 1, $2, $3, $4, $5, $6 FROM run_steps WHERE run_id = $1`,
				runID, phase, req.Name, req.Kind, req.Status, started); err != nil {
				return err
			}
			if req.Status == api.StepSkipped {
				return nil
			}
			_, err := tx.Exec(ctx, `UPDATE runs SET current_step = $2, heartbeat_at = now() WHERE id = $1`, runID, req.Name)
			return err
		}
		tag, err := tx.Exec(ctx, `
			UPDATE run_steps SET status = $4, finished_at = now()
			WHERE run_id = $1 AND phase = $2 AND name = $3 AND status = 'running'`, runID, phase, req.Name, req.Status)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrInvalid
		}
		_, err = tx.Exec(ctx, `UPDATE runs SET current_step = NULL, heartbeat_at = now() WHERE id = $1`, runID)
		return err
	})
}

// ListSteps returns a run's executor steps in order.
func (s *Store) ListSteps(ctx context.Context, orgID, runID string) ([]api.RunStep, error) {
	if _, err := s.GetRun(ctx, orgID, runID); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT phase, name, kind, status, started_at, finished_at FROM run_steps WHERE run_id = $1 ORDER BY seq`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []api.RunStep{}
	for rows.Next() {
		var st api.RunStep
		if err := rows.Scan(&st.Phase, &st.Name, &st.Kind, &st.Status, &st.StartedAt, &st.FinishedAt); err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

func (s *Store) DiscardRun(ctx context.Context, orgID, id string) (*api.Run, error) {
	return s.transition(ctx, orgID, id, []api.RunStatus{api.StatusAwaitingApproval},
		`status = 'discarded', finished_at = now()`)
}

// CancelRun cancels a queued run at once. On a run that is executing it only
// records the request: the executor learns of it on its next heartbeat and
// interrupts Terraform, which stops starting changes, saves the state and
// releases its lock. Killing it here could lose state, so the run ends
// canceled only when the executor reports back (CompletePhase). Asking again
// keeps the first request.
func (s *Store) CancelRun(ctx context.Context, orgID, id, by string) (*api.Run, error) {
	return s.transition(ctx, orgID, id,
		[]api.RunStatus{api.StatusQueued, api.StatusApplyQueued, api.StatusPlanning, api.StatusApplying}, `
		status = CASE WHEN status IN ('queued', 'apply_queued') THEN 'canceled' ELSE status END,
		finished_at = CASE WHEN status IN ('queued', 'apply_queued') THEN now() ELSE finished_at END,
		stop_requested_at = coalesce(stop_requested_at, now()),
		stop_requested_by = coalesce(stop_requested_by, $4)`, by)
}

// ClaimRun hands the oldest runnable phase to an agent. A run is runnable when
// it is apply_queued, or queued with no other active run on its stack.
// Locking the stack row serializes concurrent claims per stack.
//
// An agent that doesn't accept placement only gets runs without one, and none
// at all when it has one set on itself: better stopped than running in the
// wrong place (spec 0007, R3.2 and R5.6).
func (s *Store) ClaimRun(ctx context.Context, agent *AgentIdentity, req api.ClaimRequest) (*api.Run, api.Phase, error) {
	if err := s.reportAgent(ctx, agent, req); err != nil {
		return nil, "", err
	}
	if !req.AcceptsPlacement && !agent.Placement.IsZero() {
		return nil, "", ErrNotFound
	}
	var run *api.Run
	var phase api.Phase
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var id, stackID string
		var status api.RunStatus
		var typ api.RunType
		var access *string
		promoted := false
	pick:
		err := tx.QueryRow(ctx, `
			SELECT r.id, r.status, r.stack_id, r.type, r.state_access FROM runs r
			JOIN stacks st ON st.id = r.stack_id
			WHERE r.organization_id = $1
			  AND ($2 OR r.placement = '{}')
			  AND ($3 OR r.type <> 'command')
			  AND (r.status = 'apply_queued' OR (
			        r.status = 'queued'
			        AND NOT EXISTS (
			            SELECT 1 FROM runs o WHERE o.stack_id = r.stack_id AND o.id <> r.id
			              AND (o.status IN ('planning', 'awaiting_approval', 'apply_queued', 'applying')
			                   OR (o.status = 'queued' AND o.created_at < r.created_at)))))
			ORDER BY r.created_at
			LIMIT 1
			FOR UPDATE OF r, st SKIP LOCKED`, agent.OrganizationID, req.AcceptsPlacement, req.AcceptsCommands).Scan(&id, &status, &stackID, &typ, &access)
		if errors.Is(err, pgx.ErrNoRows) && promoted {
			return nil // keep the runs now awaiting approval
		}
		if err != nil {
			return err
		}
		next := api.StatusPlanning
		phase = api.PhasePlan
		if status == api.StatusApplyQueued {
			next, phase = api.StatusApplying, api.PhaseApply
		}
		// A command that may write the state runs in the apply phase. When the
		// stack requires approval, it waits for it now that its turn came:
		// nothing runs before (spec 0009, R9).
		if typ == api.RunCommand && deref(access) == "write" && status == api.StatusQueued {
			eff, _, err := s.effective(ctx, tx, agent.OrganizationID, api.Level{Kind: api.LevelStack, ID: stackID})
			if err != nil {
				return err
			}
			if eff.RequireApproval.Value {
				if _, err := tx.Exec(ctx, `UPDATE runs SET status = 'awaiting_approval', updated_at = now() WHERE id = $1`, id); err != nil {
					return err
				}
				promoted = true
				goto pick
			}
			next, phase = api.StatusApplying, api.PhaseApply
		}
		run, err = scanRun(tx.QueryRow(ctx, `
			WITH updated AS (
				UPDATE runs SET status = $2, agent_id = $3, claimed_at = now(), heartbeat_at = NULL,
				       started_at = coalesce(started_at, now()), updated_at = now()
				WHERE id = $1
				RETURNING *
			)
			SELECT `+runColumns+` FROM updated r`, id, next, agent.ID))
		if err != nil {
			return err
		}
		run.PlacementUsed = placementUsed(run.Placement, agent, req.InstallPlacement)
		used, err := json.Marshal(run.PlacementUsed)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE runs SET placement_used = $2 WHERE id = $1`, id, used)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && run == nil) {
		return nil, "", ErrNotFound
	}
	if err != nil {
		return nil, "", err
	}
	return run, phase, nil
}

// placementUsed is what a phase runs with: the run's fields, then the agent's,
// then the installation's (spec 0007, R1.4).
func placementUsed(run *api.EffectivePlacement, agent *AgentIdentity, install api.Placement) *api.EffectivePlacement {
	used := api.EffectivePlacement{}
	if run != nil {
		used = *run
	}
	for _, name := range api.PlacementFieldNames {
		if *used.Field(name) != nil {
			continue
		}
		if v := *agent.Placement.Field(name); len(v) > 0 {
			*used.Field(name) = &api.PlacementField{Value: v, Origin: api.Level{Kind: api.LevelAgent, Name: agent.Name}}
		} else if v := *install.Field(name); len(v) > 0 {
			*used.Field(name) = &api.PlacementField{Value: v, Origin: api.Level{Kind: api.LevelInstallation}}
		}
	}
	return &used
}

func statusForPhase(p api.Phase) api.RunStatus {
	if p == api.PhaseApply {
		return api.StatusApplying
	}
	return api.StatusPlanning
}

// variablesArtifact holds the variables a plan ran with; the apply reuses them.
const variablesArtifact = "variables"

// registriesArtifact holds the registry credentials a plan ran with; the apply reuses them.
const registriesArtifact = "registries"

// ExecSpec assembles everything an executor needs, decrypting secrets. It only
// succeeds while the run is executing the given phase. The plan phase gets
// the stack's effective variables and records them; the apply phase gets
// exactly those, whatever changed in between.
func (s *Store) ExecSpec(ctx context.Context, runID string, phase api.Phase) (*api.ExecSpec, error) {
	var spec api.ExecSpec
	var orgID string
	var commit *string
	var tokenEnc []byte
	err := s.pool.QueryRow(ctx, `
		SELECT r.id, r.organization_id, r.stack_id, r.type, r.ref, r.commit_sha, st.repository, st.path,
		       coalesce(gc.token_enc, st.git_token_enc), coalesce(r.command, ''), coalesce(r.state_access, '')
		FROM runs r JOIN stacks st ON st.id = r.stack_id
		LEFT JOIN git_connections gc ON gc.id = st.connection_id
		WHERE r.id = $1 AND r.status = $2`, runID, statusForPhase(phase)).Scan(
		&spec.RunID, &orgID, &spec.StackID, &spec.Type, &spec.Ref, &commit, &spec.Repository, &spec.Path, &tokenEnc,
		&spec.Command, &spec.StateAccess)
	if err != nil {
		return nil, notFound(err)
	}
	spec.Phase = phase
	spec.CommitSHA = deref(commit)
	if tokenEnc != nil {
		tok, err := s.keys.Decrypt(tokenEnc)
		if err != nil {
			return nil, err
		}
		spec.GitToken = string(tok)
	}

	// A command that writes the state has no plan before it: its apply phase
	// reads the settings as a plan would.
	if phase == api.PhaseApply && spec.Type != api.RunCommand {
		data, err := s.LoadArtifact(ctx, runID, phase, variablesArtifact)
		if err != nil {
			return nil, fmt.Errorf("loading the plan's variables: %w", err)
		}
		if err := json.Unmarshal(data, &spec.Variables); err != nil {
			return nil, err
		}
		data, err = s.LoadArtifact(ctx, runID, phase, registriesArtifact)
		switch {
		case errors.Is(err, ErrNotFound): // planned before registry credentials existed
		case err != nil:
			return nil, fmt.Errorf("loading the plan's registry credentials: %w", err)
		default:
			if err := json.Unmarshal(data, &spec.Registries); err != nil {
				return nil, err
			}
		}
		return &spec, nil
	}

	eff, resolved, err := s.effective(ctx, s.pool, orgID, api.Level{Kind: api.LevelStack, ID: spec.StackID})
	if err != nil {
		return nil, err
	}
	if spec.Variables, err = s.execVariables(resolved); err != nil {
		return nil, err
	}
	levels, err := chain(ctx, s.pool, orgID, api.Level{Kind: api.LevelStack, ID: spec.StackID})
	if err != nil {
		return nil, err
	}
	regs, _, err := effectiveRegistries(ctx, s.pool, orgID, levels)
	if err != nil {
		return nil, err
	}
	if spec.Registries, err = s.execRegistries(regs); err != nil {
		return nil, err
	}
	// What the run was configured with, as shown on the run (secret values are already blank).
	used, err := json.Marshal(api.RunSettings{
		RequireApproval: eff.RequireApproval, ConfirmDestructive: eff.ConfirmDestructive,
		DriftIntervalMinutes: eff.DriftIntervalMinutes, VaultRole: eff.VaultRole, Variables: eff.Variables,
	})
	if err != nil {
		return nil, err
	}
	if _, err := s.pool.Exec(ctx, `UPDATE runs SET settings_used = $2 WHERE id = $1`, runID, used); err != nil {
		return nil, err
	}
	data, err := json.Marshal(spec.Variables)
	if err != nil {
		return nil, err
	}
	if err := s.SaveArtifact(ctx, runID, phase, variablesArtifact, data); err != nil {
		return nil, err
	}
	if data, err = json.Marshal(spec.Registries); err != nil {
		return nil, err
	}
	if err := s.SaveArtifact(ctx, runID, phase, registriesArtifact, data); err != nil {
		return nil, err
	}
	return &spec, nil
}

// SetCommit records the commit resolved during the plan phase, so the apply
// phase runs exactly the code that was planned, and the stack configuration
// read at that commit.
func (s *Store) SetCommit(ctx context.Context, runID string, req api.CommitRequest) error {
	var cfg []byte
	if req.Config != nil {
		var err error
		if cfg, err = json.Marshal(req.Config); err != nil {
			return err
		}
	}
	tag, err := s.pool.Exec(ctx, `
		WITH r AS (
			UPDATE runs SET commit_sha = $2, commit_author_name = $3, commit_author_email = $4, commit_message = $5,
			       config = $6, heartbeat_at = now(), updated_at = now()
			WHERE id = $1 AND (status = 'planning' OR (status = 'applying' AND type = 'command'))
			RETURNING stack_id
		)
		UPDATE stacks st SET config = coalesce($6, st.config) FROM r WHERE st.id = r.stack_id`,
		runID, req.SHA, nullable(req.AuthorName), nullable(req.AuthorEmail), nullable(req.Message), cfg)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrConflict
	}
	return nil
}

// Heartbeat marks the executor of the given phase as alive.
// Heartbeat records that the executor is alive and tells it whether someone
// asked to stop the run.
func (s *Store) Heartbeat(ctx context.Context, runID string, phase api.Phase) (api.HeartbeatResponse, error) {
	var at *time.Time
	var by *string
	err := s.pool.QueryRow(ctx, `
		UPDATE runs SET heartbeat_at = now() WHERE id = $1 AND status = $2
		RETURNING stop_requested_at, stop_requested_by`,
		runID, statusForPhase(phase)).Scan(&at, &by)
	if errors.Is(err, pgx.ErrNoRows) {
		return api.HeartbeatResponse{}, ErrConflict
	}
	if err != nil {
		return api.HeartbeatResponse{}, err
	}
	return api.HeartbeatResponse{StopRequested: at != nil, StopRequestedBy: deref(by)}, nil
}

// CompletePhase records the outcome reported by the executor.
func (s *Store) CompletePhase(ctx context.Context, runID string, phase api.Phase, res api.CompleteRequest) (*api.Run, error) {
	var orgID, stackID string
	if err := s.pool.QueryRow(ctx, `SELECT organization_id, stack_id FROM runs WHERE id = $1`, runID).Scan(&orgID, &stackID); err != nil {
		return nil, notFound(err)
	}
	// Read when the plan finishes, so a change of the rule also affects runs already queued.
	eff, _, err := s.effective(ctx, s.pool, orgID, api.Level{Kind: api.LevelStack, ID: stackID})
	if err != nil {
		return nil, err
	}
	autoApprove := !eff.RequireApproval.Value

	var run *api.Run
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var typ api.RunType
		var status api.RunStatus
		var stopAt *time.Time
		err := tx.QueryRow(ctx, `SELECT type, status, stop_requested_at FROM runs WHERE id = $1 FOR UPDATE`, runID).
			Scan(&typ, &status, &stopAt)
		if err != nil {
			return notFound(err)
		}
		if status != statusForPhase(phase) {
			return ErrConflict
		}

		next := api.StatusFailed
		switch {
		// A command that ran every line before the stop reached it succeeded,
		// as an apply that finished is applied: what it did is done.
		case typ == api.RunCommand && res.Success:
			next = api.StatusSucceeded
		case typ == api.RunCommand && stopAt != nil:
			next = api.StatusCanceled
		case typ == api.RunCommand:
		// Someone asked to stop it. A plan that still finished goes no further;
		// an apply that finished every change before stopping is applied.
		case stopAt != nil && (phase == api.PhasePlan || !res.Success):
			next = api.StatusCanceled
		case !res.Success:
		case phase == api.PhasePlan:
			next = api.StatusAfterPlan(typ, res.HasChanges, autoApprove)
		default:
			next = api.StatusApplied
		}

		var summary []byte
		if res.PlanSummary != nil {
			if summary, err = json.Marshal(res.PlanSummary); err != nil {
				return err
			}
		}
		var hasChanges *bool
		if phase == api.PhasePlan && res.Success {
			hasChanges = &res.HasChanges
		}
		run, err = scanRun(tx.QueryRow(ctx, `
			WITH updated AS (
				UPDATE runs SET status = $2,
				       error = $3,
				       has_changes = coalesce($4, has_changes),
				       plan_summary = coalesce($5, plan_summary),
				       finished_at = CASE WHEN $6 THEN now() ELSE finished_at END,
				       exit_code = $7, failed_line = $8,
				       updated_at = now()
				WHERE id = $1
				RETURNING *
			)
			SELECT `+runColumns+` FROM updated r`,
			runID, next, nullable(res.Error), hasChanges, summary, next.IsTerminal(), res.ExitCode, res.FailedLine))
		return err
	})
	return run, err
}

// FailPhase marks a run failed if it is still executing the given phase. It is
// used when the executor died without reporting (e.g. the pod was OOM-killed).
func (s *Store) FailPhase(ctx context.Context, orgID, runID string, phase api.Phase, reason string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE runs SET status = 'failed', error = $4, finished_at = now(), updated_at = now()
		WHERE organization_id = $1 AND id = $2 AND status = $3`,
		orgID, runID, statusForPhase(phase), reason)
	return err
}

// FailStaleRuns fails executing runs whose executor stopped sending heartbeats.
func (s *Store) FailStaleRuns(ctx context.Context, timeout time.Duration) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE runs SET status = 'failed', error = 'executor stopped reporting (heartbeat timeout)',
		       finished_at = now(), updated_at = now()
		WHERE status IN ('planning', 'applying')
		  AND coalesce(heartbeat_at, claimed_at) < now() - make_interval(secs => $1)`, timeout.Seconds())
	return tag.RowsAffected(), err
}
