package store

import (
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
		"started_at", "finished_at", "created_at", "updated_at", "acknowledged", "config", "settings_used"}
	for i, c := range cols {
		cols[i] = alias + "." + c
	}
	return strings.Join(cols, ", ")
}

var runColumns = runColumnsAs("r")

// runScan reads run columns that may all be NULL (from a LEFT JOIN).
type runScan struct {
	id, stackID, typ, status, ref, commit, authorName, authorEmail, message *string
	hasChanges                                                              *bool
	summary                                                                 []byte
	errMsg, agentID, createdBy, approvedBy                                  *string
	approvedAt, startedAt, finishedAt, createdAt, updatedAt                 *time.Time
	acknowledged, config, settingsUsed                                      []byte
}

func newRunScan() *runScan { return &runScan{} }

func (s *runScan) dest() []any {
	return []any{&s.id, &s.stackID, &s.typ, &s.status, &s.ref, &s.commit, &s.authorName, &s.authorEmail, &s.message,
		&s.hasChanges, &s.summary, &s.errMsg, &s.agentID, &s.createdBy, &s.approvedBy, &s.approvedAt,
		&s.startedAt, &s.finishedAt, &s.createdAt, &s.updatedAt, &s.acknowledged, &s.config, &s.settingsUsed}
}

func (s *runScan) result() (*api.Run, error) {
	r := &api.Run{
		ID: deref(s.id), StackID: deref(s.stackID), Type: api.RunType(deref(s.typ)), Status: api.RunStatus(deref(s.status)),
		Ref: deref(s.ref), CommitSHA: deref(s.commit), CommitAuthorName: deref(s.authorName),
		CommitAuthorEmail: deref(s.authorEmail), CommitMessage: deref(s.message), HasChanges: s.hasChanges,
		Error: deref(s.errMsg), AgentID: deref(s.agentID), CreatedBy: deref(s.createdBy), ApprovedBy: deref(s.approvedBy),
		ApprovedAt: s.approvedAt, StartedAt: s.startedAt, FinishedAt: s.finishedAt,
	}
	if s.createdAt != nil {
		r.CreatedAt = *s.createdAt
	}
	if s.updatedAt != nil {
		r.UpdatedAt = *s.updatedAt
	}
	if s.summary != nil {
		r.PlanSummary = &api.PlanSummary{}
		if err := json.Unmarshal(s.summary, r.PlanSummary); err != nil {
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
	return r, nil
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
	return scanRun(s.pool.QueryRow(ctx, `
		WITH inserted AS (
			INSERT INTO runs (organization_id, stack_id, type, status, ref, created_by)
			SELECT st.organization_id, st.id, $3, 'queued', coalesce(nullif($4, ''), st.ref), $5
			FROM stacks st WHERE st.organization_id = $1 AND st.id = $2
			RETURNING *
		)
		SELECT `+runColumns+` FROM inserted r`,
		orgID, stackID, typ, ref, createdBy))
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
			INSERT INTO runs (organization_id, stack_id, type, status, ref, created_by)
			SELECT organization_id, id, 'drift', 'queued', ref, $3 FROM marked`, c.id, interval, api.DriftActor)
		if err != nil {
			return queued, err
		}
		queued += tag.RowsAffected()
	}
	return queued, nil
}

// ListActiveRuns returns an organization's active runs: those awaiting
// approval first, then the most recent.
func (s *Store) ListActiveRuns(ctx context.Context, orgID string) ([]api.ActiveRun, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+runColumns+`, st.name,
		       coalesce((SELECT array_agg(f.name ORDER BY array_position(pf.path, f.id)) FROM folders f
		                 WHERE f.id = ANY(pf.path)), '{}')
		FROM runs r
		JOIN stacks st ON st.id = r.stack_id
		LEFT JOIN folders pf ON pf.id = st.folder_id
		WHERE r.organization_id = $1 AND r.status IN `+activeStatuses+`
		ORDER BY r.status = 'awaiting_approval' DESC, r.created_at DESC`, orgID)
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

// GetChanges returns what a run's plan changes, resource by resource (empty before planning).
func (s *Store) GetChanges(ctx context.Context, orgID, runID string) ([]api.ResourceChange, error) {
	var data []byte
	if err := s.pool.QueryRow(ctx, `SELECT changes FROM runs WHERE organization_id = $1 AND id = $2`,
		orgID, runID).Scan(&data); err != nil {
		return nil, notFound(err)
	}
	out := []api.ResourceChange{}
	if data != nil {
		if err := json.Unmarshal(data, &out); err != nil {
			return nil, err
		}
	}
	return out, nil
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

// CancelRun only cancels runs that are not executing. Interrupting a running
// apply could corrupt state, so that is deliberately not supported here.
func (s *Store) CancelRun(ctx context.Context, orgID, id string) (*api.Run, error) {
	return s.transition(ctx, orgID, id, []api.RunStatus{api.StatusQueued, api.StatusApplyQueued},
		`status = 'canceled', finished_at = now()`)
}

// ClaimRun hands the oldest runnable phase to an agent. A run is runnable when
// it is apply_queued, or queued with no other active run on its stack.
// Locking the stack row serializes concurrent claims per stack.
func (s *Store) ClaimRun(ctx context.Context, agent *AgentIdentity) (*api.Run, api.Phase, error) {
	var run *api.Run
	var phase api.Phase
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var id string
		var status api.RunStatus
		err := tx.QueryRow(ctx, `
			SELECT r.id, r.status FROM runs r
			JOIN stacks st ON st.id = r.stack_id
			WHERE r.organization_id = $1
			  AND (r.status = 'apply_queued' OR (
			        r.status = 'queued'
			        AND NOT EXISTS (
			            SELECT 1 FROM runs o WHERE o.stack_id = r.stack_id AND o.id <> r.id
			              AND (o.status IN ('planning', 'awaiting_approval', 'apply_queued', 'applying')
			                   OR (o.status = 'queued' AND o.created_at < r.created_at)))))
			ORDER BY r.created_at
			LIMIT 1
			FOR UPDATE OF r, st SKIP LOCKED`, agent.OrganizationID).Scan(&id, &status)
		if err != nil {
			return err
		}
		next := api.StatusPlanning
		phase = api.PhasePlan
		if status == api.StatusApplyQueued {
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
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", ErrNotFound
	}
	if err != nil {
		return nil, "", err
	}
	return run, phase, nil
}

func statusForPhase(p api.Phase) api.RunStatus {
	if p == api.PhaseApply {
		return api.StatusApplying
	}
	return api.StatusPlanning
}

// variablesArtifact holds the variables a plan ran with; the apply reuses them.
const variablesArtifact = "variables"

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
		       coalesce(gc.token_enc, st.git_token_enc)
		FROM runs r JOIN stacks st ON st.id = r.stack_id
		LEFT JOIN git_connections gc ON gc.id = st.connection_id
		WHERE r.id = $1 AND r.status = $2`, runID, statusForPhase(phase)).Scan(
		&spec.RunID, &orgID, &spec.StackID, &spec.Type, &spec.Ref, &commit, &spec.Repository, &spec.Path, &tokenEnc)
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

	if phase == api.PhaseApply {
		data, err := s.LoadArtifact(ctx, runID, phase, variablesArtifact)
		if err != nil {
			return nil, fmt.Errorf("loading the plan's variables: %w", err)
		}
		if err := json.Unmarshal(data, &spec.Variables); err != nil {
			return nil, err
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
	// What the run was configured with, as shown on the run (secret values are already blank).
	used, err := json.Marshal(api.RunSettings{
		RequireApproval: eff.RequireApproval, ConfirmDestructive: eff.ConfirmDestructive,
		DriftIntervalMinutes: eff.DriftIntervalMinutes, Variables: eff.Variables,
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
			WHERE id = $1 AND status = 'planning'
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
func (s *Store) Heartbeat(ctx context.Context, runID string, phase api.Phase) error {
	tag, err := s.pool.Exec(ctx, `UPDATE runs SET heartbeat_at = now() WHERE id = $1 AND status = $2`,
		runID, statusForPhase(phase))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrConflict
	}
	return nil
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
		err := tx.QueryRow(ctx, `SELECT type, status FROM runs WHERE id = $1 FOR UPDATE`, runID).Scan(&typ, &status)
		if err != nil {
			return notFound(err)
		}
		if status != statusForPhase(phase) {
			return ErrConflict
		}

		next := api.StatusFailed
		switch {
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
				       updated_at = now()
				WHERE id = $1
				RETURNING *
			)
			SELECT `+runColumns+` FROM updated r`,
			runID, next, nullable(res.Error), hasChanges, summary, next.IsTerminal()))
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
