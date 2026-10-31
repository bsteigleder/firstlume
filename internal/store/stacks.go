package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/bsteigleder/firstlume/internal/api"
)

const stackColumns = `st.id, st.folder_id, st.name, st.repository, st.ref, st.path, st.git_token_enc IS NOT NULL,
	st.config, st.drift_status, st.drift_run_id, st.drift_checked_at, st.created_at, st.updated_at, st.connection_id`

func scanStack(row pgx.Row, extra ...any) (*api.Stack, error) {
	var st api.Stack
	var folder, driftStatus, driftRun, connection *string
	var driftChecked *time.Time
	var cfg []byte
	dest := []any{&st.ID, &folder, &st.Name, &st.Repository, &st.Ref, &st.Path, &st.HasGitToken,
		&cfg, &driftStatus, &driftRun, &driftChecked, &st.CreatedAt, &st.UpdatedAt, &connection}
	if err := row.Scan(append(dest, extra...)...); err != nil {
		return nil, notFound(err)
	}
	st.FolderID, st.ConnectionID = deref(folder), deref(connection)
	if cfg != nil {
		st.Config = &api.StackConfig{}
		if err := json.Unmarshal(cfg, st.Config); err != nil {
			return nil, err
		}
	}
	if driftStatus != nil && driftChecked != nil {
		st.Drift = &api.Drift{Status: api.DriftStatus(*driftStatus), RunID: deref(driftRun), CheckedAt: *driftChecked}
	}
	return &st, nil
}

// SourceInUseError means another stack already manages the same repository,
// ref and path: two stacks would manage the same infrastructure with
// separate states.
type SourceInUseError struct {
	StackID, StackName string
}

func (e *SourceInUseError) Error() string {
	return fmt.Sprintf("stack %q already manages this repository, ref and path", e.StackName)
}

func (s *Store) checkSourceFree(ctx context.Context, q querier, orgID, exceptID, repo, ref, path string) error {
	var e SourceInUseError
	err := q.QueryRow(ctx, `
		SELECT id, name FROM stacks WHERE organization_id = $1 AND repository = $2 AND ref = $3 AND path = $4
		  AND id IS DISTINCT FROM $5::uuid`, orgID, repo, ref, path, nullable(exceptID)).Scan(&e.StackID, &e.StackName)
	if err == pgx.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	return &e
}

func (s *Store) encryptToken(token string) ([]byte, error) {
	if token == "" {
		return nil, nil
	}
	return s.keys.Encrypt([]byte(token))
}

// CreateStack creates a stack at the organization's top level (FolderID "")
// or in one of its folders.
func (s *Store) CreateStack(ctx context.Context, orgID string, req api.CreateStackRequest) (*api.Stack, error) {
	tokenEnc, err := s.encryptToken(req.GitToken)
	if err != nil {
		return nil, err
	}
	var st *api.Stack
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.checkSourceFree(ctx, tx, orgID, "", req.Repository, req.Ref, req.Path); err != nil {
			return err
		}
		if req.FolderID != "" {
			if _, err := scanFolder(tx.QueryRow(ctx, `SELECT `+folderColumns+` FROM folders WHERE organization_id = $1 AND id = $2`,
				orgID, req.FolderID)); err != nil {
				if err == ErrNotFound {
					return ErrInvalid
				}
				return err
			}
		}
		st, err = scanStack(tx.QueryRow(ctx, `
			WITH st AS (
				INSERT INTO stacks (organization_id, folder_id, name, repository, ref, path, git_token_enc, connection_id)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING *
			)
			SELECT `+stackColumns+` FROM st`,
			orgID, nullable(req.FolderID), req.Name, req.Repository, req.Ref, req.Path, tokenEnc, nullable(req.ConnectionID)))
		return classify(err)
	})
	return st, err
}

func (s *Store) GetStack(ctx context.Context, orgID, id string) (*api.Stack, error) {
	return scanStack(s.pool.QueryRow(ctx,
		`SELECT `+stackColumns+` FROM stacks st WHERE st.organization_id = $1 AND st.id = $2`, orgID, id))
}

// UpdateStack changes the fields set in req.
func (s *Store) UpdateStack(ctx context.Context, orgID, id string, req api.UpdateStackRequest) (*api.Stack, error) {
	var st *api.Stack
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := scanStack(tx.QueryRow(ctx,
			`SELECT `+stackColumns+` FROM stacks st WHERE st.organization_id = $1 AND st.id = $2 FOR UPDATE`, orgID, id))
		if err != nil {
			return err
		}
		repo, ref, path := cur.Repository, cur.Ref, cur.Path
		if req.Repository != nil {
			repo = *req.Repository
		}
		if req.Ref != nil {
			ref = *req.Ref
		}
		if req.Path != nil {
			path = *req.Path
		}
		if err := s.checkSourceFree(ctx, tx, orgID, id, repo, ref, path); err != nil {
			return err
		}
		setToken := req.GitToken != nil
		var tokenEnc []byte
		if setToken {
			if tokenEnc, err = s.encryptToken(*req.GitToken); err != nil {
				return err
			}
		}
		st, err = scanStack(tx.QueryRow(ctx, `
			WITH st AS (
				UPDATE stacks SET name = coalesce($3, name), repository = $4, ref = $5, path = $6,
				       git_token_enc = CASE WHEN $7 THEN $8 ELSE git_token_enc END, updated_at = now()
				WHERE organization_id = $1 AND id = $2 RETURNING *
			)
			SELECT `+stackColumns+` FROM st`, orgID, id, req.Name, repo, ref, path, setToken, tokenEnc))
		return classify(err)
	})
	return st, err
}

// DeleteStack refuses to delete while a run is in progress, so an apply is
// never orphaned. It does not destroy infrastructure: run a destroy first.
func (s *Store) DeleteStack(ctx context.Context, orgID, id, actor string) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var active bool
		err := tx.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM runs WHERE stack_id = st.id AND status IN `+activeStatuses+`)
			FROM stacks st WHERE st.organization_id = $1 AND st.id = $2 FOR UPDATE`, orgID, id).Scan(&active)
		if err != nil {
			return notFound(err)
		}
		if active {
			return ErrConflict
		}
		// Deleting the stack would orphan infrastructure its state still manages (R6.1).
		managed, err := hasManagedState(ctx, tx, id)
		if err != nil {
			return err
		}
		if managed {
			return ErrStateNotEmpty
		}
		if err := retireState(ctx, tx, orgID, id, actor, "stack deleted"); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `DELETE FROM stacks WHERE id = $1`, id)
		return err
	})
}

// listStackSummaries returns every stack of an organization with its latest run.
func (s *Store) listStackSummaries(ctx context.Context, orgID string) ([]api.StackSummary, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+stackColumns+`, lr.id IS NOT NULL, `+runColumnsAs("lr")+`
		FROM stacks st
		LEFT JOIN LATERAL (
			SELECT * FROM runs WHERE stack_id = st.id ORDER BY created_at DESC LIMIT 1
		) lr ON true
		WHERE st.organization_id = $1
		ORDER BY lower(st.name)`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []api.StackSummary{}
	for rows.Next() {
		var hasRun bool
		run := newRunScan()
		st, err := scanStack(rows, append([]any{&hasRun}, run.dest()...)...)
		if err != nil {
			return nil, err
		}
		sum := api.StackSummary{Stack: *st}
		if hasRun {
			if sum.LastRun, err = run.result(); err != nil {
				return nil, err
			}
		}
		out = append(out, sum)
	}
	return out, rows.Err()
}
