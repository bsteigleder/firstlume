package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/bsteigleder/firstlume/internal/api"
)

// StateImport is a state to import into the unit with Key (spec 0002, R9).
type StateImport struct {
	Key  string
	Data []byte
}

// CheckState reports whether data is a Terraform state Firstlume accepts.
func CheckState(data []byte) error {
	_, _, err := parseState(data)
	return err
}

// ImportState imports states into the stack's units. Units without state get
// one; a unit that has one changes only when its key is in replace, and then
// gets a new version, keeping its history (R9.4). With dryRun nothing is
// written. Otherwise every unit is written or none is (R9.6), and a locked
// unit that would change fails the import with a StateConflictError.
func (s *Store) ImportState(ctx context.Context, orgID, stackID string, items []StateImport, replace map[string]bool, dryRun bool, actor string) ([]api.StateImportUnit, error) {
	if err := s.stackExists(ctx, orgID, stackID); err != nil {
		return nil, err
	}
	// Lock the units always in the same order.
	items = append([]StateImport(nil), items...)
	sort.Slice(items, func(i, j int) bool { return items[i].Key < items[j].Key })

	if dryRun {
		out := make([]api.StateImportUnit, 0, len(items))
		for _, it := range items {
			cur, err := s.currentState(ctx, s.pool, stackID, it.Key, false)
			if err != nil {
				return nil, err
			}
			u, _, err := planImport(it, cur, replace[it.Key])
			if err != nil {
				return nil, err
			}
			out = append(out, u)
		}
		return out, nil
	}

	var out []api.StateImportUnit
	var written []string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		out = make([]api.StateImportUnit, 0, len(items))
		var locked []string
		a := &StateActor{OrganizationID: orgID, StackID: stackID, Name: "import (" + actor + ")"}
		for _, it := range items {
			if _, err := lockUnit(ctx, tx, a, it.Key); err != nil {
				return err
			}
			cur, err := s.currentState(ctx, tx, stackID, it.Key, true)
			if err != nil {
				return err
			}
			u, data, err := planImport(it, cur, replace[it.Key])
			if err != nil {
				return err
			}
			if u.Action != "skip" && u.LockedBy != "" {
				locked = append(locked, fmt.Sprintf("%s (by %s)", unitName(u.Key), u.LockedBy))
			}
			if u.Action == "skip" || len(locked) > 0 {
				out = append(out, u)
				continue
			}
			if u.Version, err = s.insertVersion(ctx, tx, a, cur.unitID, cur.version+1, data); err != nil {
				return err
			}
			key := u.Key
			if err := stateEvent(ctx, tx, orgID, stackID, &key, "state_imported", actor,
				map[string]any{"version": u.Version, "serial": u.Serial, "replaced": u.Action == "replace"}); err != nil {
				return err
			}
			written = append(written, cur.unitID)
			out = append(out, u)
		}
		if len(locked) > 0 {
			return &StateConflictError{"locked units can't be imported into: " + strings.Join(locked, ", ")}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, id := range written {
		s.pruneVersions(ctx, id)
	}
	return out, nil
}

// StateLineage is a unit whose current state has a lineage.
type StateLineage struct {
	Lineage string
	api.StateLocation
}

// StateLineages finds which of these lineages the organization's stacks have.
func (s *Store) StateLineages(ctx context.Context, orgID string, lineages []string) ([]StateLineage, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT v.lineage, st.id, st.name, u.key FROM state_units u
		JOIN state_versions v ON v.unit_id = u.id AND v.number = u.current_version
		JOIN stacks st ON st.id = u.stack_id
		WHERE u.organization_id = $1 AND u.deleted_at IS NULL AND v.lineage = ANY($2)
		ORDER BY st.name, u.key`, orgID, lineages)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StateLineage
	for rows.Next() {
		var l StateLineage
		if err := rows.Scan(&l.Lineage, &l.StackID, &l.Stack, &l.Unit); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func unitName(key string) string {
	if key == "" {
		return "the root unit"
	}
	return key
}

// currentUnitState is a unit's current version, if it has one.
type currentUnitState struct {
	unitID   string
	version  int
	serial   int64
	lineage  string
	sha256   []byte
	lockedBy string
}

func (s *Store) currentState(ctx context.Context, q querier, stackID, key string, forUpdate bool) (*currentUnitState, error) {
	var c currentUnitState
	var serial *int64
	var lineage *string
	sql := `
		SELECT u.id, u.current_version, v.serial, v.lineage, v.sha256, coalesce(u.lock_holder, '') FROM state_units u
		LEFT JOIN state_versions v ON v.unit_id = u.id AND v.number = u.current_version
		WHERE u.stack_id = $1 AND u.key = $2 AND u.deleted_at IS NULL`
	if forUpdate {
		sql += ` FOR UPDATE OF u`
	}
	err := q.QueryRow(ctx, sql, stackID, key).Scan(&c.unitID, &c.version, &serial, &lineage, &c.sha256, &c.lockedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return &c, nil
	}
	if err != nil {
		return nil, err
	}
	if serial != nil {
		c.serial, c.lineage = *serial, *lineage
	}
	return &c, nil
}

// planImport decides what importing it does to a unit in state cur, and
// returns the bytes to store. Replacing a state of the same lineage with a
// serial not above the current one raises its serial, so that Terraform sees
// a newer state: a plan saved before the import is then stale (R5.5).
func planImport(it StateImport, cur *currentUnitState, replace bool) (api.StateImportUnit, []byte, error) {
	meta, managed, err := parseState(it.Data)
	if err != nil {
		return api.StateImportUnit{}, nil, err
	}
	u := api.StateImportUnit{Key: it.Key, Serial: *meta.Serial, Lineage: meta.Lineage, Resources: managed,
		CurrentVersion: cur.version, CurrentSerial: cur.serial, LockedBy: cur.lockedBy}
	sum := sha256.Sum256(it.Data)
	switch {
	case cur.version == 0:
		u.Status, u.Action = "new", "create"
		return u, it.Data, nil
	case bytes.Equal(sum[:], cur.sha256):
		u.Status, u.Action = "same", "skip"
		return u, nil, nil
	case meta.Lineage != cur.lineage:
		u.Status = "conflict"
	case *meta.Serial > cur.serial:
		u.Status = "newer"
	default:
		u.Status = "older"
	}
	if !replace {
		u.Action = "skip"
		return u, nil, nil
	}
	u.Action = "replace"
	data := it.Data
	if meta.Lineage == cur.lineage && *meta.Serial <= cur.serial {
		u.Serial = cur.serial + 1
		if data, err = withSerial(it.Data, u.Serial); err != nil {
			return u, nil, err
		}
	}
	return u, data, nil
}

// withSerial returns the state with its serial set to serial.
func withSerial(data []byte, serial int64) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidState, err)
	}
	m["serial"] = json.RawMessage(strconv.FormatInt(serial, 10))
	return json.MarshalIndent(m, "", "  ")
}

// insertVersion stores data as the unit's version number, written by a, and
// makes it current. The object goes first: a crash before the commit leaves
// only an orphan object, which PurgeState removes; readers never see it (R4.3).
func (s *Store) insertVersion(ctx context.Context, tx pgx.Tx, a *StateActor, unitID string, number int, data []byte) (int, error) {
	meta, managed, err := parseState(data)
	if err != nil {
		return 0, err
	}
	sum := sha256.Sum256(data)
	versionID, err := newUUID()
	if err != nil {
		return 0, err
	}
	// The organization's own storage, if it has one, gets the version under
	// its names, encrypted or not as it chose (spec 0005); the installation's,
	// under ids and always encrypted.
	org, err := s.currentOrgStorage(ctx, tx, a.OrganizationID)
	if err != nil {
		return 0, err
	}
	var blobs Blobs = s.blobs
	var storageID *string
	encrypted := true
	objectKey := a.OrganizationID + "/" + a.StackID + "/" + versionID
	if org != nil {
		blobs, storageID, encrypted = storageBlobs{org.blobs}, &org.id, org.encrypt
		var unitKey string
		if err := tx.QueryRow(ctx, `SELECT key FROM state_units WHERE id = $1`, unitID).Scan(&unitKey); err != nil {
			return 0, err
		}
		if objectKey, err = stateObjectPath(ctx, tx, a.StackID, unitKey, versionID, number, encrypted, org.blobs.Config().Prefix); err != nil {
			return 0, err
		}
	}
	stored := data
	if encrypted {
		if stored, err = s.keys.State().Encrypt(data); err != nil {
			return 0, err
		}
	}
	if err := blobs.Put(ctx, objectKey, stored); err != nil {
		var se *StateStorageError
		if errors.As(err, &se) {
			return 0, err
		}
		return 0, fmt.Errorf("storing state object: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO state_versions (id, unit_id, number, serial, lineage, sha256, size, resources, object_key, run_id, phase, token_id, written_by,
		                            storage_id, encrypted)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`,
		versionID, unitID, number, *meta.Serial, meta.Lineage, sum[:], len(data), managed, objectKey,
		nullable(a.RunID), nullable(string(a.Phase)), nullable(a.TokenID), a.Name, storageID, encrypted); err != nil {
		return 0, err
	}
	_, err = tx.Exec(ctx, `UPDATE state_units SET current_version = $2, updated_at = now() WHERE id = $1`, unitID, number)
	return number, err
}
