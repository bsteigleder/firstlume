package store

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/bsteigleder/firstlume/internal/api"
)

const environmentColumns = `id, template_id, name, ref, auto_approve, created_at, updated_at`

func scanEnvironment(row pgx.Row) (*api.Environment, error) {
	var e api.Environment
	var ref *string
	if err := row.Scan(&e.ID, &e.TemplateID, &e.Name, &ref, &e.AutoApprove, &e.CreatedAt, &e.UpdatedAt); err != nil {
		return nil, notFound(err)
	}
	e.Ref = deref(ref)
	return &e, nil
}

func (s *Store) CreateEnvironment(ctx context.Context, orgID string, req api.CreateEnvironmentRequest) (*api.Environment, error) {
	// The template must belong to the same organization.
	e, err := scanEnvironment(s.pool.QueryRow(ctx, `
		INSERT INTO environments (organization_id, template_id, name, ref, auto_approve)
		SELECT $1, t.id, $3, $4, $5 FROM templates t WHERE t.organization_id = $1 AND t.id = $2
		RETURNING `+environmentColumns,
		orgID, req.TemplateID, req.Name, nullable(req.Ref), req.AutoApprove))
	if err == ErrNotFound {
		return nil, ErrInvalid
	}
	return e, classify(err)
}

func (s *Store) GetEnvironment(ctx context.Context, orgID, id string) (*api.Environment, error) {
	return scanEnvironment(s.pool.QueryRow(ctx,
		`SELECT `+environmentColumns+` FROM environments WHERE organization_id = $1 AND id = $2`, orgID, id))
}

func (s *Store) ListEnvironments(ctx context.Context, orgID string) ([]api.Environment, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+environmentColumns+` FROM environments WHERE organization_id = $1 ORDER BY name`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []api.Environment{}
	for rows.Next() {
		e, err := scanEnvironment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

// DeleteEnvironment refuses to delete while a run is in progress, so an apply
// is never orphaned. It does not destroy infrastructure: run a destroy first.
func (s *Store) DeleteEnvironment(ctx context.Context, orgID, id string) error {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM environments e
		WHERE e.organization_id = $1 AND e.id = $2
		  AND NOT EXISTS (SELECT 1 FROM runs r WHERE r.environment_id = e.id AND r.status IN ('planning', 'apply_queued', 'applying'))`,
		orgID, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		if _, err := s.GetEnvironment(ctx, orgID, id); err != nil {
			return err
		}
		return ErrConflict
	}
	return nil
}

// ListVariables returns the environment's variables with sensitive values omitted.
func (s *Store) ListVariables(ctx context.Context, orgID, envID string) ([]api.Variable, error) {
	if _, err := s.GetEnvironment(ctx, orgID, envID); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT key, category, sensitive, coalesce(value, '')
		FROM environment_variables WHERE environment_id = $1 ORDER BY category, key`, envID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []api.Variable{}
	for rows.Next() {
		var v api.Variable
		if err := rows.Scan(&v.Key, &v.Category, &v.Sensitive, &v.Value); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// PutVariables replaces the full variable set. A sensitive variable sent with an
// empty value keeps its stored value, since the API never returns it.
func (s *Store) PutVariables(ctx context.Context, orgID, envID string, vars []api.Variable) error {
	if _, err := s.GetEnvironment(ctx, orgID, envID); err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		existing := map[string][]byte{}
		rows, err := tx.Query(ctx, `
			SELECT category, key, value_enc FROM environment_variables
			WHERE environment_id = $1 AND sensitive FOR UPDATE`, envID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var cat, key string
			var enc []byte
			if err := rows.Scan(&cat, &key, &enc); err != nil {
				rows.Close()
				return err
			}
			existing[cat+"/"+key] = enc
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}

		if _, err := tx.Exec(ctx, `DELETE FROM environment_variables WHERE environment_id = $1`, envID); err != nil {
			return err
		}
		for _, v := range vars {
			var plain *string
			var enc []byte
			switch {
			case !v.Sensitive:
				plain = &v.Value
			case v.Value != "":
				if enc, err = s.keys.Encrypt([]byte(v.Value)); err != nil {
					return err
				}
			default:
				if enc = existing[string(v.Category)+"/"+v.Key]; enc == nil {
					return ErrInvalid
				}
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO environment_variables (environment_id, key, category, sensitive, value, value_enc)
				VALUES ($1, $2, $3, $4, $5, $6)`,
				envID, v.Key, v.Category, v.Sensitive, plain, enc); err != nil {
				return classify(err)
			}
		}
		_, err = tx.Exec(ctx, `UPDATE environments SET updated_at = now() WHERE id = $1`, envID)
		return err
	})
}
