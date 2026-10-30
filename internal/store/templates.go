package store

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/bsteigleder/firstlume/internal/api"
)

const templateColumns = `id, name, repository, path, default_ref, engine, engine_version,
	terragrunt_version, git_token_enc IS NOT NULL, created_at, updated_at`

func scanTemplate(row pgx.Row) (*api.Template, error) {
	var t api.Template
	var tgVersion *string
	err := row.Scan(&t.ID, &t.Name, &t.Repository, &t.Path, &t.DefaultRef, &t.Engine, &t.EngineVersion,
		&tgVersion, &t.HasGitToken, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return nil, notFound(err)
	}
	t.TerragruntVersion = deref(tgVersion)
	return &t, nil
}

func (s *Store) CreateTemplate(ctx context.Context, orgID string, req api.CreateTemplateRequest) (*api.Template, error) {
	var tokenEnc []byte
	if req.GitToken != "" {
		var err error
		if tokenEnc, err = s.keys.Encrypt([]byte(req.GitToken)); err != nil {
			return nil, err
		}
	}
	t, err := scanTemplate(s.pool.QueryRow(ctx, `
		INSERT INTO templates (organization_id, name, repository, path, default_ref, engine, engine_version, terragrunt_version, git_token_enc)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING `+templateColumns,
		orgID, req.Name, req.Repository, req.Path, req.DefaultRef, req.Engine, req.EngineVersion,
		nullable(req.TerragruntVersion), tokenEnc))
	return t, classify(err)
}

func (s *Store) GetTemplate(ctx context.Context, orgID, id string) (*api.Template, error) {
	return scanTemplate(s.pool.QueryRow(ctx,
		`SELECT `+templateColumns+` FROM templates WHERE organization_id = $1 AND id = $2`, orgID, id))
}

func (s *Store) ListTemplates(ctx context.Context, orgID string) ([]api.Template, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+templateColumns+` FROM templates WHERE organization_id = $1 ORDER BY name`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []api.Template{}
	for rows.Next() {
		t, err := scanTemplate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// DeleteTemplate fails with ErrConflict while environments still use the template.
func (s *Store) DeleteTemplate(ctx context.Context, orgID, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM templates WHERE organization_id = $1 AND id = $2`, orgID, id)
	if err != nil {
		if classify(err) == ErrInvalid {
			return ErrConflict
		}
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
