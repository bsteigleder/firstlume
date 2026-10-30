package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/bsteigleder/firstlume/internal/api"
)

const runColumns = `r.id, r.environment_id, r.type, r.status, r.ref, r.commit_sha, r.has_changes, r.plan_summary,
	r.error, r.agent_id, r.created_by, r.approved_by, r.approved_at, r.started_at, r.finished_at, r.created_at, r.updated_at`

func scanRun(row pgx.Row) (*api.Run, error) {
	var r api.Run
	var commit, errMsg, agentID, approvedBy *string
	var summary []byte
	err := row.Scan(&r.ID, &r.EnvironmentID, &r.Type, &r.Status, &r.Ref, &commit, &r.HasChanges, &summary,
		&errMsg, &agentID, &r.CreatedBy, &approvedBy, &r.ApprovedAt, &r.StartedAt, &r.FinishedAt, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return nil, notFound(err)
	}
	r.CommitSHA, r.Error, r.AgentID, r.ApprovedBy = deref(commit), deref(errMsg), deref(agentID), deref(approvedBy)
	if summary != nil {
		r.PlanSummary = &api.PlanSummary{}
		if err := json.Unmarshal(summary, r.PlanSummary); err != nil {
			return nil, err
		}
	}
	return &r, nil
}

// CreateRun queues a run. The ref defaults to the environment's ref, then the template's.
func (s *Store) CreateRun(ctx context.Context, orgID, envID string, typ api.RunType, ref, createdBy string) (*api.Run, error) {
	return scanRun(s.pool.QueryRow(ctx, `
		WITH inserted AS (
			INSERT INTO runs (organization_id, environment_id, type, status, ref, created_by)
			SELECT e.organization_id, e.id, $3, 'queued', coalesce(nullif($4, ''), e.ref, t.default_ref), $5
			FROM environments e JOIN templates t ON t.id = e.template_id
			WHERE e.organization_id = $1 AND e.id = $2
			RETURNING *
		)
		SELECT `+runColumns+` FROM inserted r`,
		orgID, envID, typ, ref, createdBy))
}

// ScheduleDriftRuns queues a drift run for every environment whose drift check
// is due and that has no run in progress (a busy one is retried next time).
// It is safe to call from several server replicas at once.
func (s *Store) ScheduleDriftRuns(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		WITH due AS (
			SELECT e.id FROM environments e
			WHERE e.drift_interval_minutes IS NOT NULL
			  AND coalesce(e.drift_scheduled_at, '-infinity') <= now() - make_interval(mins => e.drift_interval_minutes)
			  AND NOT EXISTS (
			      SELECT 1 FROM runs r WHERE r.environment_id = e.id
			        AND r.status IN ('queued', 'planning', 'awaiting_approval', 'apply_queued', 'applying'))
			FOR UPDATE OF e SKIP LOCKED
		), marked AS (
			UPDATE environments e SET drift_scheduled_at = now()
			FROM due WHERE e.id = due.id
			RETURNING e.id, e.organization_id, e.ref, e.template_id
		)
		INSERT INTO runs (organization_id, environment_id, type, status, ref, created_by)
		SELECT m.organization_id, m.id, 'drift', 'queued', coalesce(m.ref, t.default_ref), $1
		FROM marked m JOIN templates t ON t.id = m.template_id`, api.DriftActor)
	return tag.RowsAffected(), err
}

func (s *Store) GetRun(ctx context.Context, orgID, id string) (*api.Run, error) {
	return scanRun(s.pool.QueryRow(ctx,
		`SELECT `+runColumns+` FROM runs r WHERE r.organization_id = $1 AND r.id = $2`, orgID, id))
}

func (s *Store) ListRuns(ctx context.Context, orgID, envID string, limit int) ([]api.Run, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+runColumns+` FROM runs r
		WHERE r.organization_id = $1 AND r.environment_id = $2
		ORDER BY r.created_at DESC LIMIT $3`, orgID, envID, limit)
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

func (s *Store) ApproveRun(ctx context.Context, orgID, id, by string) (*api.Run, error) {
	return s.transition(ctx, orgID, id, []api.RunStatus{api.StatusAwaitingApproval},
		`status = 'apply_queued', approved_by = $4, approved_at = now()`, by)
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
// it is apply_queued, or queued with no other active run on its environment.
// Locking the environment row serializes concurrent claims per environment.
func (s *Store) ClaimRun(ctx context.Context, agent *AgentIdentity) (*api.Run, api.Phase, error) {
	var run *api.Run
	var phase api.Phase
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var id string
		var status api.RunStatus
		err := tx.QueryRow(ctx, `
			SELECT r.id, r.status FROM runs r
			JOIN environments e ON e.id = r.environment_id
			WHERE r.organization_id = $1
			  AND (r.status = 'apply_queued' OR (
			        r.status = 'queued'
			        AND NOT EXISTS (
			            SELECT 1 FROM runs o WHERE o.environment_id = r.environment_id AND o.id <> r.id
			              AND (o.status IN ('planning', 'awaiting_approval', 'apply_queued', 'applying')
			                   OR (o.status = 'queued' AND o.created_at < r.created_at)))))
			ORDER BY r.created_at
			LIMIT 1
			FOR UPDATE OF r, e SKIP LOCKED`, agent.OrganizationID).Scan(&id, &status)
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

// ExecSpec assembles everything an executor needs, decrypting secrets. It only
// succeeds while the run is executing the given phase.
func (s *Store) ExecSpec(ctx context.Context, runID string, phase api.Phase) (*api.ExecSpec, error) {
	var spec api.ExecSpec
	var commit, tgVersion *string
	var tokenEnc []byte
	err := s.pool.QueryRow(ctx, `
		SELECT r.id, r.environment_id, r.type, r.ref, r.commit_sha,
		       t.repository, t.path, t.engine, t.engine_version, t.terragrunt_version, t.git_token_enc
		FROM runs r
		JOIN environments e ON e.id = r.environment_id
		JOIN templates t ON t.id = e.template_id
		WHERE r.id = $1 AND r.status = $2`, runID, statusForPhase(phase)).Scan(
		&spec.RunID, &spec.EnvironmentID, &spec.Type, &spec.Ref, &commit,
		&spec.Repository, &spec.Path, &spec.Engine, &spec.EngineVersion, &tgVersion, &tokenEnc)
	if err != nil {
		return nil, notFound(err)
	}
	spec.Phase = phase
	spec.CommitSHA, spec.TerragruntVersion = deref(commit), deref(tgVersion)
	if tokenEnc != nil {
		tok, err := s.keys.Decrypt(tokenEnc)
		if err != nil {
			return nil, err
		}
		spec.GitToken = string(tok)
	}

	rows, err := s.pool.Query(ctx, `
		SELECT key, category, sensitive, value, value_enc FROM environment_variables
		WHERE environment_id = $1 ORDER BY category, key`, spec.EnvironmentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	spec.Variables = []api.ExecVariable{}
	for rows.Next() {
		var v api.ExecVariable
		var plain *string
		var enc []byte
		if err := rows.Scan(&v.Key, &v.Category, &v.Sensitive, &plain, &enc); err != nil {
			return nil, err
		}
		if v.Sensitive {
			dec, err := s.keys.Decrypt(enc)
			if err != nil {
				return nil, err
			}
			v.Value = string(dec)
		} else {
			v.Value = deref(plain)
		}
		spec.Variables = append(spec.Variables, v)
	}
	return &spec, rows.Err()
}

// SetCommit records the commit resolved during the plan phase, so the apply
// phase runs exactly the code that was planned.
func (s *Store) SetCommit(ctx context.Context, runID, sha string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE runs SET commit_sha = $2, heartbeat_at = now(), updated_at = now()
		WHERE id = $1 AND status = 'planning'`, runID, sha)
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
	var run *api.Run
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var typ api.RunType
		var status api.RunStatus
		var autoApprove bool
		err := tx.QueryRow(ctx, `
			SELECT r.type, r.status, e.auto_approve FROM runs r
			JOIN environments e ON e.id = r.environment_id
			WHERE r.id = $1 FOR UPDATE OF r`, runID).Scan(&typ, &status, &autoApprove)
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
