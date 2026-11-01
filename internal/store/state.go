package store

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bsteigleder/firstlume/internal/api"
)

// Firstlume's Terraform state backend (spec 0002). Each unit of a stack has
// one state, a lock and immutable versions. Versions are encrypted and their
// bytes go to the configured Blobs; their metadata and the locks stay here.

var (
	// ErrInvalidState means a body that is not a Terraform state.
	ErrInvalidState = errors.New("not a Terraform state")
	// ErrStateNotEmpty means the stack's state still manages resources.
	ErrStateNotEmpty = errors.New("state still has resources")
)

// StateLockedError means the unit is locked by someone else. Info is the
// LockInfo Terraform shows for the holder.
type StateLockedError struct{ Info []byte }

func (e *StateLockedError) Error() string { return "state is locked" }

// StateConflictError means a write that would lose or mix up state (R3.5).
type StateConflictError struct{ Reason string }

func (e *StateConflictError) Error() string { return e.Reason }

// Blobs stores the encrypted bytes of state versions under opaque keys.
type Blobs interface {
	Put(ctx context.Context, key string, data []byte) error
	Get(ctx context.Context, key string) ([]byte, error)
	Delete(ctx context.Context, key string) error
	// List returns the keys of objects created before t.
	List(ctx context.Context, before time.Time) ([]string, error)
}

// SetStateBlobs replaces where state versions are stored (Postgres by default).
func (s *Store) SetStateBlobs(b Blobs) { s.blobs = b }

// SetStateRetention keeps at most n versions per unit; 0 keeps them all (R4.6).
func (s *Store) SetStateRetention(n int) { s.keepVersions = n }

// pgBlobs keeps state versions in the state_objects table.
type pgBlobs struct{ pool *pgxpool.Pool }

// PostgresBlobs returns the default storage, the state_objects table.
func PostgresBlobs(pool *pgxpool.Pool) Blobs { return pgBlobs{pool} }

func (b pgBlobs) Put(ctx context.Context, key string, data []byte) error {
	_, err := b.pool.Exec(ctx, `INSERT INTO state_objects (key, data) VALUES ($1, $2)`, key, data)
	return err
}

func (b pgBlobs) Get(ctx context.Context, key string) ([]byte, error) {
	var data []byte
	err := b.pool.QueryRow(ctx, `SELECT data FROM state_objects WHERE key = $1`, key).Scan(&data)
	return data, notFound(err)
}

func (b pgBlobs) Delete(ctx context.Context, key string) error {
	_, err := b.pool.Exec(ctx, `DELETE FROM state_objects WHERE key = $1`, key)
	return err
}

func (b pgBlobs) List(ctx context.Context, before time.Time) ([]string, error) {
	rows, err := b.pool.Query(ctx, `SELECT key FROM state_objects WHERE created_at < $1`, before)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// StateActor is who uses the state protocol: a run phase or a state token.
type StateActor struct {
	OrganizationID string
	StackID        string
	RunID          string
	Phase          api.Phase
	TokenID        string
	// Name describes the actor in locks and versions.
	Name string
	// CanWrite allows writing state, and CanLock locking it; reading is always allowed.
	CanWrite bool
	CanLock  bool
}

// StateRunActor returns the actor for a run phase's state token, as long as
// the run is still executing that phase (R5.1).
func (s *Store) StateRunActor(ctx context.Context, runID string, phase api.Phase) (*StateActor, error) {
	a := StateActor{RunID: runID, Phase: phase, CanWrite: phase == api.PhaseApply, CanLock: true}
	err := s.pool.QueryRow(ctx, `SELECT organization_id, stack_id FROM runs WHERE id = $1 AND status = $2`,
		runID, statusForPhase(phase)).Scan(&a.OrganizationID, &a.StackID)
	if err != nil {
		return nil, notFound(err)
	}
	a.Name = fmt.Sprintf("run %s (%s)", short(runID), phase)
	return &a, nil
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// stateMeta is what Firstlume reads from a state: enough to order versions,
// tell infrastructures apart and count what they manage.
type stateMeta struct {
	Version   int    `json:"version"`
	Serial    *int64 `json:"serial"`
	Lineage   string `json:"lineage"`
	Resources []struct {
		Mode string `json:"mode"`
	} `json:"resources"`
}

func parseState(data []byte) (*stateMeta, int, error) {
	var m stateMeta
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, 0, fmt.Errorf("%w: %v", ErrInvalidState, err)
	}
	if m.Version < 3 || m.Serial == nil || *m.Serial < 0 || m.Lineage == "" || m.Resources == nil {
		return nil, 0, fmt.Errorf("%w: version, serial, lineage and resources are required", ErrInvalidState)
	}
	managed := 0
	for _, r := range m.Resources {
		if r.Mode == "managed" {
			managed++
		}
	}
	return &m, managed, nil
}

type stateUnitRow struct {
	id       string
	current  int
	lockID   *string
	lockInfo []byte
}

// lockUnit returns the unit's row locked for this transaction, creating it if needed.
func lockUnit(ctx context.Context, tx pgx.Tx, a *StateActor, key string) (*stateUnitRow, error) {
	if _, err := tx.Exec(ctx, `
		INSERT INTO state_units (organization_id, stack_id, key) VALUES ($1, $2, $3)
		ON CONFLICT (stack_id, key) WHERE deleted_at IS NULL DO NOTHING`, a.OrganizationID, a.StackID, key); err != nil {
		return nil, err
	}
	var u stateUnitRow
	err := tx.QueryRow(ctx, `
		SELECT id, current_version, lock_id, lock_info FROM state_units
		WHERE stack_id = $1 AND key = $2 AND deleted_at IS NULL FOR UPDATE`, a.StackID, key).
		Scan(&u.id, &u.current, &u.lockID, &u.lockInfo)
	return &u, err
}

// ReadState returns the unit's current state, or nil when it has none yet.
func (s *Store) ReadState(ctx context.Context, a *StateActor, key string) ([]byte, error) {
	return s.stateBytes(ctx, a.StackID, key)
}

func (s *Store) stateBytes(ctx context.Context, stackID, key string) ([]byte, error) {
	var objectKey string
	var storageID *string
	var encrypted bool
	err := s.pool.QueryRow(ctx, `
		SELECT v.object_key, v.storage_id, v.encrypted FROM state_units u
		JOIN state_versions v ON v.unit_id = u.id AND v.number = u.current_version
		WHERE u.stack_id = $1 AND u.key = $2 AND u.deleted_at IS NULL`, stackID, key).Scan(&objectKey, &storageID, &encrypted)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.readVersion(ctx, storageID, objectKey, encrypted)
}

// readVersion reads a version from the storage it was written to, decrypting
// it if it was encrypted (spec 0005, R4.1 and R5.2).
func (s *Store) readVersion(ctx context.Context, storageID *string, objectKey string, encrypted bool) ([]byte, error) {
	blobs, err := s.blobsAt(ctx, storageID)
	if err != nil {
		return nil, err
	}
	stored, err := blobs.Get(ctx, objectKey)
	if err != nil {
		var se *StateStorageError
		if errors.As(err, &se) {
			return nil, err
		}
		return nil, fmt.Errorf("reading state object: %w", err)
	}
	if !encrypted {
		return stored, nil
	}
	return s.keys.State().Decrypt(stored)
}

// storedObject is a version's object and where it is.
type storedObject struct {
	key       string
	storageID *string
}

func collectObjects(rows pgx.Rows) ([]storedObject, error) {
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (storedObject, error) {
		var o storedObject
		err := row.Scan(&o.key, &o.storageID)
		return o, err
	})
}

// deleteObject removes a version's object from its storage.
func (s *Store) deleteObject(ctx context.Context, o storedObject) error {
	blobs, err := s.blobsAt(ctx, o.storageID)
	if err != nil {
		return err
	}
	return blobs.Delete(ctx, o.key)
}

// WriteState stores a new version of the unit's state. lockID is the ID
// Terraform sends while it holds the lock. Writing the same serial and content
// again is accepted without a new version.
func (s *Store) WriteState(ctx context.Context, a *StateActor, key, lockID string, data []byte) error {
	meta, _, err := parseState(data)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	var unitID string
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		u, err := lockUnit(ctx, tx, a, key)
		if err != nil {
			return err
		}
		unitID = u.id
		if u.lockID != nil && *u.lockID != lockID {
			return &StateLockedError{Info: u.lockInfo}
		}
		if u.current > 0 {
			var serial int64
			var lineage string
			var prev []byte
			if err := tx.QueryRow(ctx, `SELECT serial, lineage, sha256 FROM state_versions WHERE unit_id = $1 AND number = $2`,
				u.id, u.current).Scan(&serial, &lineage, &prev); err != nil {
				return err
			}
			switch {
			case meta.Lineage != lineage:
				return &StateConflictError{fmt.Sprintf("this state belongs to another infrastructure: lineage %s, stored %s", meta.Lineage, lineage)}
			case *meta.Serial < serial:
				return &StateConflictError{fmt.Sprintf("stale state: serial %d is older than the stored %d", *meta.Serial, serial)}
			case *meta.Serial == serial && bytes.Equal(prev, sum[:]):
				return nil
			case *meta.Serial == serial:
				return &StateConflictError{fmt.Sprintf("serial %d is already stored with different content", serial)}
			}
		}

		_, err = s.insertVersion(ctx, tx, a, u.id, u.current+1, data)
		return err
	})
	if err != nil {
		return err
	}
	s.pruneVersions(ctx, unitID)
	return nil
}

// pruneVersions drops the unit's versions beyond the retention, never the
// current one. It runs after the write is committed and can't fail it: an
// object left behind is an orphan PurgeState removes later.
func (s *Store) pruneVersions(ctx context.Context, unitID string) {
	if s.keepVersions <= 0 {
		return
	}
	rows, err := s.pool.Query(ctx, `
		DELETE FROM state_versions v USING state_units u
		WHERE v.unit_id = u.id AND u.id = $1 AND v.number <= u.current_version - $2
		RETURNING v.object_key, v.storage_id`, unitID, s.keepVersions)
	if err != nil {
		return
	}
	objects, err := collectObjects(rows)
	if err != nil {
		return
	}
	for _, o := range objects {
		_ = s.deleteObject(ctx, o)
	}
}

// LockState takes the unit's lock for the LockInfo Terraform sent. Locking
// again with the same ID succeeds.
func (s *Store) LockState(ctx context.Context, a *StateActor, key string, lockInfo []byte) error {
	info, id, err := parseLockInfo(lockInfo)
	if err != nil {
		return err
	}
	// Terraform shows Who to whoever finds the unit locked: name the run, not the pod.
	info["Who"] = a.Name
	stored, err := json.Marshal(info)
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		u, err := lockUnit(ctx, tx, a, key)
		if err != nil {
			return err
		}
		if u.lockID != nil {
			if *u.lockID == id {
				return nil
			}
			return &StateLockedError{Info: u.lockInfo}
		}
		_, err = tx.Exec(ctx, `
			UPDATE state_units SET lock_id = $2, lock_info = $3, lock_run_id = $4, lock_holder = $5, locked_at = now(), updated_at = now()
			WHERE id = $1`, u.id, id, stored, nullable(a.RunID), a.Name)
		return err
	})
}

// UnlockState releases the unit's lock if lockInfo carries its ID. Releasing a
// lock someone else holds (Terraform's force-unlock) is not allowed here: it
// is done on purpose, by an admin, from the stack's State tab (R8.4).
func (s *Store) UnlockState(ctx context.Context, a *StateActor, key string, lockInfo []byte) error {
	id := ""
	if len(bytes.TrimSpace(lockInfo)) > 0 {
		_, lid, err := parseLockInfo(lockInfo)
		if err != nil {
			return err
		}
		id = lid
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var unitID string
		var lockID *string
		var info []byte
		err := tx.QueryRow(ctx, `
			SELECT id, lock_id, lock_info FROM state_units
			WHERE stack_id = $1 AND key = $2 AND deleted_at IS NULL FOR UPDATE`, a.StackID, key).Scan(&unitID, &lockID, &info)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if lockID == nil {
			return nil
		}
		if *lockID != id {
			return &StateLockedError{Info: info}
		}
		_, err = tx.Exec(ctx, `
			UPDATE state_units SET lock_id = NULL, lock_info = NULL, lock_run_id = NULL, lock_holder = NULL, locked_at = NULL,
			       updated_at = now()
			WHERE id = $1`, unitID)
		return err
	})
}

func parseLockInfo(raw []byte) (map[string]any, string, error) {
	var info map[string]any
	if err := json.Unmarshal(raw, &info); err != nil {
		return nil, "", fmt.Errorf("%w: lock info is not JSON: %v", ErrInvalid, err)
	}
	id, _ := info["ID"].(string)
	if id == "" || len(id) > 256 {
		return nil, "", fmt.Errorf("%w: lock info needs an ID of up to 256 characters", ErrInvalid)
	}
	return info, id, nil
}

// ListStateUnits returns the stack's units that have a state or a lock.
func (s *Store) ListStateUnits(ctx context.Context, orgID, stackID string) ([]api.StateUnit, error) {
	if err := s.stackExists(ctx, orgID, stackID); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT u.key, u.current_version, coalesce(v.serial, 0), coalesce(v.resources, 0), coalesce(v.size, 0),
		       v.created_at, coalesce(v.written_by, ''), v.run_id,
		       u.lock_id, coalesce(u.lock_holder, ''), coalesce(u.lock_info->>'Operation', ''), u.lock_run_id, u.locked_at
		FROM state_units u
		LEFT JOIN state_versions v ON v.unit_id = u.id AND v.number = u.current_version
		WHERE u.organization_id = $1 AND u.stack_id = $2 AND u.deleted_at IS NULL
		  AND (u.current_version > 0 OR u.lock_id IS NOT NULL)
		ORDER BY u.key`, orgID, stackID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []api.StateUnit{}
	for rows.Next() {
		var u api.StateUnit
		var runID, lockID, lockRunID *string
		var holder, operation string
		var lockedAt *time.Time
		if err := rows.Scan(&u.Key, &u.Version, &u.Serial, &u.Resources, &u.Size, &u.UpdatedAt, &u.WrittenBy, &runID,
			&lockID, &holder, &operation, &lockRunID, &lockedAt); err != nil {
			return nil, err
		}
		u.RunID = deref(runID)
		if lockID != nil {
			u.Lock = &api.StateLock{ID: *lockID, Holder: holder, Operation: operation, RunID: deref(lockRunID), Since: *lockedAt}
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) stackExists(ctx context.Context, orgID, stackID string) error {
	var ok bool
	err := s.pool.QueryRow(ctx, `SELECT true FROM stacks WHERE organization_id = $1 AND id = $2`, orgID, stackID).Scan(&ok)
	return notFound(err)
}

// hasManagedState reports whether the stack's state still manages resources.
func hasManagedState(ctx context.Context, tx pgx.Tx, stackID string) (bool, error) {
	var has bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM state_units u JOIN state_versions v ON v.unit_id = u.id AND v.number = u.current_version
			WHERE u.stack_id = $1 AND u.deleted_at IS NULL AND v.resources > 0)`, stackID).Scan(&has)
	return has, err
}

// retireState marks the stack's state deleted; PurgeState removes it after
// stateRetention (R6.2).
func retireState(ctx context.Context, tx pgx.Tx, orgID, stackID, actor, reason string) error {
	_, err := tx.Exec(ctx, `
		WITH retired AS (
			UPDATE state_units SET deleted_at = now(), lock_id = NULL, lock_info = NULL, lock_run_id = NULL,
			       lock_holder = NULL, locked_at = NULL, updated_at = now()
			WHERE stack_id = $2 AND deleted_at IS NULL
			RETURNING key
		)
		INSERT INTO state_events (organization_id, stack_id, unit_key, kind, actor, detail)
		SELECT $1, $2, key, 'state_deleted', $3, jsonb_build_object('reason', $4::text) FROM retired`,
		orgID, stackID, actor, reason)
	return err
}

// DeleteState deletes the stack's state (R6.2). It refuses while a run is
// active or a unit is locked, since someone may be about to write it.
func (s *Store) DeleteState(ctx context.Context, orgID, stackID, actor string) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var active, locked bool
		err := tx.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM runs WHERE stack_id = st.id AND status IN `+activeStatuses+`),
			       EXISTS (SELECT 1 FROM state_units WHERE stack_id = st.id AND deleted_at IS NULL AND lock_id IS NOT NULL)
			FROM stacks st WHERE st.organization_id = $1 AND st.id = $2 FOR UPDATE`, orgID, stackID).Scan(&active, &locked)
		if err != nil {
			return notFound(err)
		}
		if active || locked {
			return ErrConflict
		}
		return retireState(ctx, tx, orgID, stackID, actor, "state deleted")
	})
}

// stateRetention is how long a deleted state is kept (R6.2).
const stateRetention = 30 * 24 * time.Hour

// OrphanAge is how old an object without a version must be to be removed: a
// write in progress has stored its object but not committed its version yet (R4.3).
const OrphanAge = 24 * time.Hour

// PurgeState removes deleted states past their retention and objects older
// than orphanAge that never got a version, in every storage that holds
// versions or is an organization's current one. It returns how many objects
// it deleted. An organization storage that fails is skipped, so one bucket
// that is down doesn't stop the cleanup of the others.
func (s *Store) PurgeState(ctx context.Context, orphanAge time.Duration) (int, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT v.object_key, v.storage_id FROM state_units u JOIN state_versions v ON v.unit_id = u.id
		WHERE u.deleted_at < now() - make_interval(secs => $1)`, stateRetention.Seconds())
	if err != nil {
		return 0, err
	}
	expired, err := collectObjects(rows)
	if err != nil {
		return 0, err
	}
	n := 0
	var failed error
	for _, o := range expired {
		if err := s.deleteObject(ctx, o); err != nil {
			if o.storageID == nil {
				return n, err
			}
			failed = err // the version's metadata goes anyway; its object becomes an orphan to retry
			continue
		}
		n++
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM state_units WHERE deleted_at < now() - make_interval(secs => $1)`,
		stateRetention.Seconds()); err != nil {
		return n, err
	}

	rows, err = s.pool.Query(ctx, `
		SELECT id FROM state_storages st
		WHERE EXISTS (SELECT 1 FROM organizations o WHERE o.state_storage_id = st.id)
		   OR EXISTS (SELECT 1 FROM state_versions v WHERE v.storage_id = st.id)`)
	if err != nil {
		return n, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return n, err
	}
	storages := []*string{nil}
	for i := range ids {
		storages = append(storages, &ids[i])
	}
	for _, id := range storages {
		deleted, err := s.purgeOrphans(ctx, id, orphanAge)
		n += deleted
		switch {
		case err == nil:
		case id == nil:
			return n, err
		default:
			failed = err
		}
	}
	return n, failed
}

// purgeOrphans deletes the storage's objects older than orphanAge that no
// version of that storage points to.
func (s *Store) purgeOrphans(ctx context.Context, storageID *string, orphanAge time.Duration) (int, error) {
	blobs, err := s.blobsAt(ctx, storageID)
	if err != nil {
		return 0, err
	}
	old, err := blobs.List(ctx, time.Now().Add(-orphanAge))
	if err != nil || len(old) == 0 {
		return 0, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT k FROM unnest($1::text[]) k
		WHERE NOT EXISTS (SELECT 1 FROM state_versions WHERE object_key = k AND storage_id IS NOT DISTINCT FROM $2)`, old, storageID)
	if err != nil {
		return 0, err
	}
	orphans, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return 0, err
	}
	n := 0
	for _, key := range orphans {
		if err := blobs.Delete(ctx, key); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func newUUID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
