package store

import (
	"context"
	"errors"

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

// EnsureOrganization returns the id of the organization with this slug (or
// a former one), creating it (named after the slug) if needed. It
// bootstraps installations.
func (s *Store) EnsureOrganization(ctx context.Context, slug string) (string, error) {
	if o, err := s.GetOrganizationBySlug(ctx, slug); !errors.Is(err, ErrNotFound) {
		if err != nil {
			return "", err
		}
		return o.ID, nil
	}
	var id string
	err := s.pool.QueryRow(ctx, `
		INSERT INTO organizations (slug, name) VALUES ($1, $1)
		ON CONFLICT (slug) DO UPDATE SET slug = EXCLUDED.slug
		RETURNING id`, slug).Scan(&id)
	return id, err
}

// CreateOrganization refuses a slug that is another organization's former one.
func (s *Store) CreateOrganization(ctx context.Context, req api.CreateOrganizationRequest) (*api.Organization, error) {
	o, err := scanOrganization(s.pool.QueryRow(ctx, `
		INSERT INTO organizations (slug, name)
		SELECT $1, $2 WHERE NOT EXISTS (SELECT 1 FROM organization_former_slugs WHERE slug = $1)
		RETURNING `+organizationColumns, req.Slug, req.Name))
	if errors.Is(err, ErrNotFound) {
		return nil, ErrDuplicate
	}
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

// GetOrganizationBySlug also finds an organization by a former slug; the
// result carries the current one.
func (s *Store) GetOrganizationBySlug(ctx context.Context, slug string) (*api.Organization, error) {
	return scanOrganization(s.pool.QueryRow(ctx, `
		SELECT `+organizationColumns+` FROM organizations
		WHERE slug = $1 OR id = (SELECT organization_id FROM organization_former_slugs WHERE slug = $1)
		ORDER BY slug = $1 DESC LIMIT 1`, slug))
}

// UpdateOrganization renames an organization and, when slug isn't empty,
// changes its address. The old address becomes a former slug that keeps
// resolving to it; a slug in use or held by another organization is a
// duplicate.
func (s *Store) UpdateOrganization(ctx context.Context, id, name, slug string) (*api.Organization, error) {
	var o *api.Organization
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var old string
		if err := tx.QueryRow(ctx, `SELECT slug FROM organizations WHERE id = $1 FOR UPDATE`, id).Scan(&old); err != nil {
			return notFound(err)
		}
		if slug != "" && slug != old {
			var owner string
			err := tx.QueryRow(ctx, `SELECT organization_id FROM organization_former_slugs WHERE slug = $1`, slug).Scan(&owner)
			switch {
			case err == nil && owner != id:
				return ErrDuplicate
			case err != nil && !errors.Is(err, pgx.ErrNoRows):
				return err
			}
			if _, err := tx.Exec(ctx, `DELETE FROM organization_former_slugs WHERE slug = $1`, slug); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO organization_former_slugs (slug, organization_id) VALUES ($1, $2)
				ON CONFLICT (slug) DO NOTHING`, old, id); err != nil {
				return err
			}
		} else {
			slug = old
		}
		var err error
		o, err = scanOrganization(tx.QueryRow(ctx,
			`UPDATE organizations SET name = $2, slug = $3 WHERE id = $1 RETURNING `+organizationColumns, id, name, slug))
		return err
	})
	return o, classify(err)
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
