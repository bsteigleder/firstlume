package store

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"

	"github.com/bsteigleder/firstlume/internal/api"
)

// endedEarly is the error of a resource the apply was still working on when it ended.
const endedEarly = "The apply ended before this resource finished."

// RecordApplyProgress stores what the apply did to the resources it reports
// (spec 0004, R3.1) and marks the run as reporting progress. Keys that are not
// in the run's plan are skipped and returned.
func (s *Store) RecordApplyProgress(ctx context.Context, runID string, req api.ApplyProgressRequest) ([]string, error) {
	var skipped []string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var data []byte
		if err := tx.QueryRow(ctx, `SELECT changes FROM runs WHERE id = $1 AND status = 'applying' FOR UPDATE`,
			runID).Scan(&data); err != nil {
			if notFound(err) == ErrNotFound {
				return ErrConflict
			}
			return err
		}
		var changes []api.ResourceChange
		if data != nil {
			if err := json.Unmarshal(data, &changes); err != nil {
				return err
			}
		}
		known := make(map[string]bool, len(changes))
		for _, c := range changes {
			known[c.Key()] = true
		}
		if _, err := tx.Exec(ctx, `UPDATE runs SET apply_tracked = true, heartbeat_at = now() WHERE id = $1`, runID); err != nil {
			return err
		}
		for _, p := range req.Resources {
			if !known[p.Key] {
				skipped = append(skipped, p.Key)
				continue
			}
			steps, err := json.Marshal(nonNil(p.Steps))
			if err != nil {
				return err
			}
			errs, err := json.Marshal(nonNil(p.Errors))
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO run_change_progress (run_id, key, steps, errors) VALUES ($1, $2, $3, $4)
				ON CONFLICT (run_id, key) DO UPDATE SET steps = $3, errors = $4, updated_at = now()`,
				runID, p.Key, steps, errs); err != nil {
				return err
			}
		}
		return nil
	})
	return skipped, err
}

func nonNil[T any](xs []T) []T {
	if xs == nil {
		return []T{}
	}
	return xs
}

// GetChanges returns what a run's plan changes, resource by resource (empty
// before planning), with where each resource is in the apply when the apply
// reports progress.
func (s *Store) GetChanges(ctx context.Context, orgID, runID string) ([]api.ResourceChange, error) {
	var data []byte
	var status api.RunStatus
	var tracked bool
	if err := s.pool.QueryRow(ctx, `SELECT changes, status, apply_tracked FROM runs WHERE organization_id = $1 AND id = $2`,
		orgID, runID).Scan(&data, &status, &tracked); err != nil {
		return nil, notFound(err)
	}
	out := []api.ResourceChange{}
	if data != nil {
		if err := json.Unmarshal(data, &out); err != nil {
			return nil, err
		}
	}
	if !tracked {
		return out, nil
	}
	progress, err := s.applyProgress(ctx, runID)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Apply = deriveApply(out[i].Action, progress[out[i].Key()], status.IsTerminal())
	}
	return out, nil
}

// ApplyTotals counts the outcome of a finished apply that reported progress;
// nil otherwise.
func (s *Store) ApplyTotals(ctx context.Context, orgID, runID string) (*api.ApplyTotals, error) {
	var status api.RunStatus
	var tracked bool
	if err := s.pool.QueryRow(ctx, `SELECT status, apply_tracked FROM runs WHERE organization_id = $1 AND id = $2`,
		orgID, runID).Scan(&status, &tracked); err != nil {
		return nil, notFound(err)
	}
	if !tracked || !status.IsTerminal() {
		return nil, nil
	}
	changes, err := s.GetChanges(ctx, orgID, runID)
	if err != nil {
		return nil, err
	}
	t := &api.ApplyTotals{}
	for _, c := range changes {
		switch c.Apply.State {
		case api.ApplyDone:
			t.Done++
		case api.ApplyFailed:
			t.Failed++
		case api.ApplyNotApplied:
			t.NotApplied++
		}
	}
	return t, nil
}

// applyRecord is what the executor reported about one resource.
type applyRecord struct {
	Steps  []api.ApplyStep
	Errors []api.ApplyError
}

func (s *Store) applyProgress(ctx context.Context, runID string) (map[string]*applyRecord, error) {
	rows, err := s.pool.Query(ctx, `SELECT key, steps, errors FROM run_change_progress WHERE run_id = $1`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*applyRecord{}
	for rows.Next() {
		var key string
		var steps, errs []byte
		if err := rows.Scan(&key, &steps, &errs); err != nil {
			return nil, err
		}
		rec := &applyRecord{}
		if err := json.Unmarshal(steps, &rec.Steps); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(errs, &rec.Errors); err != nil {
			return nil, err
		}
		out[key] = rec
	}
	return out, rows.Err()
}

// deriveApply works out a resource's state (spec 0004, R2.1 and R2.4) from
// what the executor reported, the plan's action (a replace takes two steps)
// and whether the run has finished.
func deriveApply(action api.ChangeAction, rec *applyRecord, finished bool) *api.ResourceApply {
	if rec == nil {
		if finished {
			return &api.ResourceApply{State: api.ApplyNotApplied}
		}
		return &api.ResourceApply{State: api.ApplyPending}
	}
	a := &api.ResourceApply{Steps: rec.Steps, Errors: rec.Errors}
	want := 1
	if action == api.ActionReplace {
		want = 2
	}
	completed, failed := 0, len(rec.Errors) > 0
	for _, st := range rec.Steps {
		if a.StartedAt == nil && st.StartedAt != nil {
			a.StartedAt = st.StartedAt
		}
		if st.FinishedAt != nil && (a.FinishedAt == nil || st.FinishedAt.After(*a.FinishedAt)) {
			a.FinishedAt = st.FinishedAt
		}
		switch {
		case st.Failed:
			failed = true
		case st.FinishedAt != nil:
			completed++
		}
	}
	switch {
	case failed:
		a.State = api.ApplyFailed
	case completed >= want:
		a.State = api.ApplyDone
	case finished:
		a.State = api.ApplyFailed
		a.Errors = append(a.Errors, api.ApplyError{Summary: endedEarly})
	default:
		a.State = api.ApplyApplying
		a.FinishedAt = nil
	}
	if a.StartedAt != nil && a.FinishedAt != nil && a.State != api.ApplyApplying {
		a.DurationMS = a.FinishedAt.Sub(*a.StartedAt).Milliseconds()
	}
	return a
}
