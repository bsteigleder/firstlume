package store

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/bsteigleder/firstlume/internal/api"
)

const organizationColumns = `id, slug, name, created_at`

func scanOrganization(row pgx.Row) (*api.Organization, error) {
	var o api.Organization
	if err := row.Scan(&o.ID, &o.Slug, &o.Name, &o.CreatedAt); err != nil {
		return nil, notFound(err)
	}
	return &o, nil
}

// EnsureOrganization returns the id of the organization with this slug,
// creating it (named after the slug) if needed. It bootstraps installations.
func (s *Store) EnsureOrganization(ctx context.Context, slug string) (string, error) {
	var id string
	err := s.pool.QueryRow(ctx, `
		INSERT INTO organizations (slug, name) VALUES ($1, $1)
		ON CONFLICT (slug) DO UPDATE SET slug = EXCLUDED.slug
		RETURNING id`, slug).Scan(&id)
	return id, err
}

func (s *Store) CreateOrganization(ctx context.Context, req api.CreateOrganizationRequest) (*api.Organization, error) {
	o, err := scanOrganization(s.pool.QueryRow(ctx,
		`INSERT INTO organizations (slug, name) VALUES ($1, $2) RETURNING `+organizationColumns, req.Slug, req.Name))
	return o, classify(err)
}

func (s *Store) ListOrganizations(ctx context.Context) ([]api.Organization, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+organizationColumns+` FROM organizations ORDER BY lower(name), slug`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []api.Organization{}
	for rows.Next() {
		o, err := scanOrganization(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *o)
	}
	return out, rows.Err()
}

func (s *Store) GetOrganizationBySlug(ctx context.Context, slug string) (*api.Organization, error) {
	return scanOrganization(s.pool.QueryRow(ctx, `SELECT `+organizationColumns+` FROM organizations WHERE slug = $1`, slug))
}

func (s *Store) RenameOrganization(ctx context.Context, id, name string) (*api.Organization, error) {
	return scanOrganization(s.pool.QueryRow(ctx,
		`UPDATE organizations SET name = $2 WHERE id = $1 RETURNING `+organizationColumns, id, name))
}

// DeleteOrganization only deletes an organization without folders or stacks.
func (s *Store) DeleteOrganization(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM organizations o WHERE id = $1
		  AND NOT EXISTS (SELECT 1 FROM folders WHERE organization_id = o.id)
		  AND NOT EXISTS (SELECT 1 FROM stacks WHERE organization_id = o.id)`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		if _, err := s.GetOrganizationByID(ctx, id); err != nil {
			return err
		}
		return ErrConflict
	}
	return nil
}

func (s *Store) GetOrganizationByID(ctx context.Context, id string) (*api.Organization, error) {
	return scanOrganization(s.pool.QueryRow(ctx, `SELECT `+organizationColumns+` FROM organizations WHERE id = $1`, id))
}
