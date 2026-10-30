package store

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/bsteigleder/firstlume/internal/api"
)

const folderColumns = `id, parent_id, name, path::text[], created_at, updated_at`

func scanFolder(row pgx.Row) (*api.Folder, error) {
	var f api.Folder
	var parent *string
	if err := row.Scan(&f.ID, &parent, &f.Name, &f.Path, &f.CreatedAt, &f.UpdatedAt); err != nil {
		return nil, notFound(err)
	}
	f.ParentID = deref(parent)
	return &f, nil
}

// CreateFolder creates a folder at the top level (parentID "") or inside
// another folder of the same organization, at most api.MaxFolderDepth deep.
func (s *Store) CreateFolder(ctx context.Context, orgID, parentID, name string) (*api.Folder, error) {
	var f *api.Folder
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		parentPath := []string{}
		if parentID != "" {
			err := tx.QueryRow(ctx, `SELECT path::text[] FROM folders WHERE organization_id = $1 AND id = $2 FOR SHARE`,
				orgID, parentID).Scan(&parentPath)
			if err != nil {
				if notFound(err) == ErrNotFound {
					return ErrInvalid
				}
				return err
			}
			if len(parentPath) >= api.MaxFolderDepth {
				return ErrTooDeep
			}
		}
		var err error
		f, err = scanFolder(tx.QueryRow(ctx, `
			INSERT INTO folders (id, organization_id, parent_id, name, path)
			SELECT g.id, $1, $2, $3, $4::uuid[] || g.id FROM (SELECT gen_random_uuid() AS id) g
			RETURNING `+folderColumns, orgID, nullable(parentID), name, parentPath))
		return classify(err)
	})
	return f, err
}

func (s *Store) GetFolder(ctx context.Context, orgID, id string) (*api.Folder, error) {
	return scanFolder(s.pool.QueryRow(ctx,
		`SELECT `+folderColumns+` FROM folders WHERE organization_id = $1 AND id = $2`, orgID, id))
}

func (s *Store) RenameFolder(ctx context.Context, orgID, id, name string) (*api.Folder, error) {
	f, err := scanFolder(s.pool.QueryRow(ctx, `
		UPDATE folders SET name = $3, updated_at = now() WHERE organization_id = $1 AND id = $2
		RETURNING `+folderColumns, orgID, id, name))
	return f, classify(err)
}

// DeleteFolder only deletes an empty folder (no subfolders or stacks). Its
// settings and variables go with it.
func (s *Store) DeleteFolder(ctx context.Context, orgID, id string) error {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM folders f WHERE organization_id = $1 AND id = $2
		  AND NOT EXISTS (SELECT 1 FROM folders c WHERE c.parent_id = f.id)
		  AND NOT EXISTS (SELECT 1 FROM stacks st WHERE st.folder_id = f.id)`, orgID, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		if _, err := s.GetFolder(ctx, orgID, id); err != nil {
			return err
		}
		return ErrConflict
	}
	return nil
}

func (s *Store) listFolders(ctx context.Context, orgID string) ([]api.Folder, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+folderColumns+` FROM folders WHERE organization_id = $1
		ORDER BY cardinality(path), lower(name)`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []api.Folder{}
	for rows.Next() {
		f, err := scanFolder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *f)
	}
	return out, rows.Err()
}

// Tree returns an organization's folders and stacks, each stack with its latest run.
func (s *Store) Tree(ctx context.Context, orgID string) (*api.Tree, error) {
	folders, err := s.listFolders(ctx, orgID)
	if err != nil {
		return nil, err
	}
	stacks, err := s.listStackSummaries(ctx, orgID)
	if err != nil {
		return nil, err
	}
	return &api.Tree{Folders: folders, Stacks: stacks}, nil
}
