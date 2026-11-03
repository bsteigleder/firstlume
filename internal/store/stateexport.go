package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// StateExportError means a unit's state could not be read, so nothing was
// exported (R8.9).
type StateExportError struct {
	Key string
	Err error
}

func (e *StateExportError) Error() string {
	name := e.Key
	if name == "" {
		name = "(root)"
	}
	return fmt.Sprintf("could not read the state of %s from storage", name)
}

func (e *StateExportError) Unwrap() error { return e.Err }

// ExportState calls add with the current state of each of the stack's units,
// or only of unit when it isn't nil, then records who exported what (R8.9). It
// returns ErrNotFound when there is no state to export.
func (s *Store) ExportState(ctx context.Context, orgID, stackID string, unit *string, actor string, add func(key string, data []byte) error) error {
	type version struct {
		key       string
		number    int32
		objectKey string
		storageID *string
		encrypted bool
	}
	rows, err := s.pool.Query(ctx, `
		SELECT u.key, v.number, v.object_key, v.storage_id, v.encrypted FROM state_units u
		JOIN state_versions v ON v.unit_id = u.id AND v.number = u.current_version
		WHERE u.organization_id = $1 AND u.stack_id = $2 AND u.deleted_at IS NULL AND ($3::text IS NULL OR u.key = $3)
		ORDER BY u.key`, orgID, stackID, unit)
	if err != nil {
		return err
	}
	versions, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (version, error) {
		var v version
		err := row.Scan(&v.key, &v.number, &v.objectKey, &v.storageID, &v.encrypted)
		return v, err
	})
	if err != nil {
		return err
	}
	if len(versions) == 0 {
		return ErrNotFound
	}
	keys := make([]string, len(versions))
	numbers := make([]int32, len(versions))
	for i, v := range versions {
		data, err := s.readVersion(ctx, v.storageID, v.objectKey, v.encrypted)
		if err != nil {
			return &StateExportError{Key: v.key, Err: err}
		}
		if err := add(v.key, data); err != nil {
			return err
		}
		keys[i], numbers[i] = v.key, v.number
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO state_events (organization_id, stack_id, unit_key, kind, actor, detail)
		SELECT $1, $2, k, 'state_exported', $3, jsonb_build_object('version', n)
		FROM unnest($4::text[], $5::int[]) AS t(k, n)`, orgID, stackID, actor, keys, numbers)
	return err
}
