package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/bsteigleder/firstlume/internal/api"
	"github.com/bsteigleder/firstlume/internal/s3"
)

// State storage per organization (spec 0005): an organization may keep its
// state versions in a folder of its own S3 bucket, named after its folders,
// stacks and units, encrypted or in the clear. Locks and metadata stay in
// Postgres (R1.4).

// DefaultStateFolder is the folder Firstlume uses in a bucket when none is given.
const DefaultStateFolder = "firstlume"

// ownerMarker is the file that says which organization a folder belongs to (R1.6).
const ownerMarker = ".firstlume"

// maxObjectKey is S3's limit for an object key, in bytes.
const maxObjectKey = 1024

// StateStorageError means an organization's own storage failed. The state
// protocol answers 503 with it, and nothing is recorded (R3.3).
type StateStorageError struct{ Err error }

func (e *StateStorageError) Error() string {
	return "the organization's state storage did not answer: " + e.Err.Error()
}

func (e *StateStorageError) Unwrap() error { return e.Err }

// StateStorageCheckError is a storage that can't be used, said in terms an
// admin can act on, without secrets (R3.1).
type StateStorageCheckError struct{ Reason string }

func (e *StateStorageCheckError) Error() string { return e.Reason }

// orgStorage is an organization's storage, ready to use.
type orgStorage struct {
	id      string
	blobs   *s3.Client
	encrypt bool
	updated time.Time
}

// orgStorages caches clients by storage id, rebuilt when the row changes.
type orgStorages struct {
	mu   sync.Mutex
	byID map[string]*orgStorage
}

// orgStorage returns the storage with this id, from the cache when its row
// hasn't changed.
func (s *Store) orgStorage(ctx context.Context, q querier, id string) (*orgStorage, error) {
	var cfg s3.Config
	var ca *string
	var secret []byte
	var encrypt bool
	var updated time.Time
	err := q.QueryRow(ctx, `
		SELECT endpoint, bucket, region, folder, path_style, ca_cert, access_key_id, secret_access_key_enc, encrypt, updated_at
		FROM state_storages WHERE id = $1`, id).
		Scan(&cfg.Endpoint, &cfg.Bucket, &cfg.Region, &cfg.Prefix, &cfg.PathStyle, &ca, &cfg.AccessKeyID, &secret, &encrypt, &updated)
	if err != nil {
		return nil, notFound(err)
	}
	s.storages.mu.Lock()
	defer s.storages.mu.Unlock()
	if c := s.storages.byID[id]; c != nil && c.updated.Equal(updated) {
		return c, nil
	}
	plain, err := s.keys.Decrypt(secret)
	if err != nil {
		return nil, fmt.Errorf("state storage %s: %w", id, err)
	}
	cfg.SecretAccessKey = string(plain)
	cfg.CACert = deref(ca)
	client, err := s3.New(cfg)
	if err != nil {
		return nil, &StateStorageError{err}
	}
	c := &orgStorage{id: id, blobs: client, encrypt: encrypt, updated: updated}
	if s.storages.byID == nil {
		s.storages.byID = map[string]*orgStorage{}
	}
	s.storages.byID[id] = c
	return c, nil
}

// currentOrgStorage is where the organization's new versions go: nil for the
// installation's storage.
func (s *Store) currentOrgStorage(ctx context.Context, q querier, orgID string) (*orgStorage, error) {
	var id *string
	if err := q.QueryRow(ctx, `SELECT state_storage_id FROM organizations WHERE id = $1`, orgID).Scan(&id); err != nil {
		return nil, notFound(err)
	}
	if id == nil {
		return nil, nil
	}
	return s.orgStorage(ctx, q, *id)
}

// blobsAt returns the storage a version was written to (R4.1).
func (s *Store) blobsAt(ctx context.Context, storageID *string) (Blobs, error) {
	if storageID == nil {
		return s.blobs, nil
	}
	st, err := s.orgStorage(ctx, s.pool, *storageID)
	if err != nil {
		return nil, err
	}
	return storageBlobs{st.blobs}, nil
}

// storageBlobs reports an organization storage's failures as StateStorageError.
type storageBlobs struct{ c *s3.Client }

func (b storageBlobs) Put(ctx context.Context, key string, data []byte) error {
	return wrapStorage(b.c.Put(ctx, key, data))
}

func (b storageBlobs) Get(ctx context.Context, key string) ([]byte, error) {
	data, err := b.c.Get(ctx, key)
	return data, wrapStorage(err)
}

func (b storageBlobs) Delete(ctx context.Context, key string) error {
	return wrapStorage(b.c.Delete(ctx, key))
}

// List returns only what Firstlume writes in its folder, so the cleanup never
// touches anything else an organization keeps there (R4.5).
func (b storageBlobs) List(ctx context.Context, before time.Time) ([]string, error) {
	keys, err := b.c.List(ctx, before)
	if err != nil {
		return nil, wrapStorage(err)
	}
	out := keys[:0]
	for _, k := range keys {
		if strings.HasSuffix(k, ".tfstate.enc") || strings.HasSuffix(k, ".tfstate") || strings.HasPrefix(k, ".firstlume-check/") {
			out = append(out, k)
		}
	}
	return out, nil
}

func wrapStorage(err error) error {
	if err == nil {
		return nil
	}
	return &StateStorageError{err}
}

// stateObjectPath is where a version goes in an organization's folder:
// <folders>/<stack>/<unit>/v<number>-<version id>.tfstate[.enc] (R1.7). The
// version id keeps two paths that read alike, or a new stack named like a
// deleted one, from ever overwriting a file.
func stateObjectPath(ctx context.Context, tx pgx.Tx, stackID, unitKey, versionID string, number int, encrypted bool, folder string) (string, error) {
	var names []string
	var stack string
	err := tx.QueryRow(ctx, `
		SELECT coalesce((SELECT array_agg(f.name ORDER BY p.ord)
		                 FROM folders cur, unnest(cur.path) WITH ORDINALITY p(id, ord) JOIN folders f ON f.id = p.id
		                 WHERE cur.id = st.folder_id), '{}'), st.name
		FROM stacks st WHERE st.id = $1`, stackID).Scan(&names, &stack)
	if err != nil {
		return "", notFound(err)
	}
	parts := make([]string, 0, len(names)+3)
	for _, n := range append(names, stack) {
		parts = append(parts, pathSegment(n))
	}
	if unitKey != "" {
		parts = append(parts, unitKey)
	}
	ext := ".tfstate"
	if encrypted {
		ext = ".tfstate.enc"
	}
	parts = append(parts, fmt.Sprintf("v%06d-%s%s", number, versionID, ext))
	key := strings.Join(parts, "/")
	if len(folder)+1+len(key) > maxObjectKey {
		return "", fmt.Errorf("%w: the path of unit %q in the state storage is longer than %d bytes", ErrInvalid, unitKey, maxObjectKey)
	}
	return key, nil
}

// pathSegment makes a folder or stack name one path segment.
func pathSegment(name string) string {
	name = strings.ReplaceAll(strings.TrimSpace(name), "/", "_")
	if name == "" || name == "." || name == ".." {
		return "_" + name
	}
	return name
}

// ResolveStateStorage completes a request with what is stored: an empty
// secret keeps the one of the organization's storage with the same access key.
func (s *Store) ResolveStateStorage(ctx context.Context, orgID string, in api.PutStateStorageRequest) (s3.Config, error) {
	cfg := s3.Config{
		Endpoint: in.Endpoint, Bucket: in.Bucket, Region: in.Region, Prefix: in.Folder,
		PathStyle: in.PathStyle == nil || *in.PathStyle, CACert: in.CACert,
		AccessKeyID: in.AccessKeyID, SecretAccessKey: in.SecretAccessKey,
	}
	if cfg.SecretAccessKey != "" {
		return cfg, nil
	}
	var enc []byte
	err := s.pool.QueryRow(ctx, `
		SELECT secret_access_key_enc FROM state_storages
		WHERE organization_id = $1 AND access_key_id = $2
		ORDER BY (id = (SELECT state_storage_id FROM organizations WHERE id = $1)) DESC, updated_at DESC
		LIMIT 1`, orgID, cfg.AccessKeyID).Scan(&enc)
	if errors.Is(err, pgx.ErrNoRows) {
		return cfg, fmt.Errorf("%w: secret_access_key is required", ErrInvalid)
	}
	if err != nil {
		return cfg, err
	}
	plain, err := s.keys.Decrypt(enc)
	if err != nil {
		return cfg, err
	}
	cfg.SecretAccessKey = string(plain)
	return cfg, nil
}

// CheckStateStorage writes, reads and deletes a probe in the folder and checks
// the folder isn't another organization's (R3.1, R1.6). With claim, a folder
// without an owner becomes this organization's.
func (s *Store) CheckStateStorage(ctx context.Context, orgID string, cfg s3.Config, claim bool) error {
	client, err := s3.New(cfg)
	if err != nil {
		return &StateStorageCheckError{err.Error()}
	}
	if err := client.Check(ctx); err != nil {
		return &StateStorageCheckError{err.Error()}
	}
	var other string
	err = s.pool.QueryRow(ctx, `
		SELECT o.slug FROM state_storages st JOIN organizations o ON o.id = st.organization_id
		WHERE st.endpoint = $1 AND st.bucket = $2 AND st.folder = $3 AND st.organization_id <> $4`,
		cfg.Endpoint, cfg.Bucket, cfg.Prefix, orgID).Scan(&other)
	if err == nil {
		return &StateStorageCheckError{"this folder already holds the states of another organization"}
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	type owner struct {
		OrganizationID string    `json:"organization_id"`
		Since          time.Time `json:"since"`
	}
	raw, err := client.Get(ctx, ownerMarker)
	switch {
	case errors.Is(err, s3.ErrNotFound):
		if !claim {
			return nil
		}
		marker, _ := json.Marshal(owner{OrganizationID: orgID, Since: time.Now().UTC()})
		if err := client.Put(ctx, ownerMarker, marker); err != nil {
			return &StateStorageCheckError{fmt.Sprintf("S3 storage check: can't write %s/%s: %v", cfg.Prefix, ownerMarker, err)}
		}
		return nil
	case err != nil:
		return &StateStorageCheckError{fmt.Sprintf("S3 storage check: can't read %s/%s: %v", cfg.Prefix, ownerMarker, err)}
	}
	var o owner
	if json.Unmarshal(raw, &o) != nil || o.OrganizationID != orgID {
		return &StateStorageCheckError{"this folder already holds the states of another organization (" + cfg.Prefix + "/" + ownerMarker + ")"}
	}
	return nil
}

// PutStateStorage makes cfg the organization's storage, or the installation's
// when cfg is nil. The same endpoint, bucket and folder update the storage in
// place (a new key, say); another location becomes a new storage, and the old
// one stays readable while it holds versions (R4.2, R4.3).
func (s *Store) PutStateStorage(ctx context.Context, orgID string, cfg *s3.Config, encrypt bool, actor string) error {
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var before *string
		if err := tx.QueryRow(ctx, `SELECT state_storage_id FROM organizations WHERE id = $1 FOR UPDATE`, orgID).Scan(&before); err != nil {
			return notFound(err)
		}
		var current *string
		if cfg != nil {
			secret, err := s.keys.Encrypt([]byte(cfg.SecretAccessKey))
			if err != nil {
				return err
			}
			var id string
			err = tx.QueryRow(ctx, `
				INSERT INTO state_storages (organization_id, endpoint, bucket, region, folder, path_style, ca_cert, access_key_id,
				                            secret_access_key_enc, encrypt)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
				ON CONFLICT (endpoint, bucket, folder) DO UPDATE SET region = EXCLUDED.region, path_style = EXCLUDED.path_style,
				       ca_cert = EXCLUDED.ca_cert, access_key_id = EXCLUDED.access_key_id,
				       secret_access_key_enc = EXCLUDED.secret_access_key_enc, encrypt = EXCLUDED.encrypt, updated_at = now()
				WHERE state_storages.organization_id = EXCLUDED.organization_id
				RETURNING id`,
				orgID, cfg.Endpoint, cfg.Bucket, cfg.Region, cfg.Prefix, cfg.PathStyle, nullable(cfg.CACert), cfg.AccessKeyID,
				secret, encrypt).Scan(&id)
			if errors.Is(err, pgx.ErrNoRows) { // the location is another organization's
				return &StateStorageCheckError{"this folder already holds the states of another organization"}
			}
			if err != nil {
				return classify(err)
			}
			current = &id
		}
		if _, err := tx.Exec(ctx, `UPDATE organizations SET state_storage_id = $2 WHERE id = $1`, orgID, current); err != nil {
			return err
		}
		// A storage that is neither current nor holds versions is gone.
		if _, err := tx.Exec(ctx, `
			DELETE FROM state_storages st WHERE st.organization_id = $1 AND st.id IS DISTINCT FROM $2
			  AND NOT EXISTS (SELECT 1 FROM state_versions v WHERE v.storage_id = st.id)`, orgID, current); err != nil {
			return err
		}
		detail := map[string]any{"from": storageLabel(before), "to": storageLabel(current), "encrypt": cfg == nil || encrypt}
		if cfg != nil {
			detail["location"] = cfg.Endpoint + "/" + cfg.Bucket + "/" + cfg.Prefix
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO state_events (organization_id, kind, actor, detail) VALUES ($1, 'storage_changed', $2, $3)`,
			orgID, actor, detail)
		return err
	})
	return err
}

func storageLabel(id *string) string {
	if id == nil {
		return api.StateStorageInstallation
	}
	return *id
}

// GetStateStorage returns the organization's storage and the earlier ones
// that still hold versions, with counts (R4.3, R5.2).
func (s *Store) GetStateStorage(ctx context.Context, orgID string) (*api.StateStorageSettings, error) {
	var current *string
	if err := s.pool.QueryRow(ctx, `SELECT state_storage_id FROM organizations WHERE id = $1`, orgID).Scan(&current); err != nil {
		return nil, notFound(err)
	}
	type count struct{ all, plain int }
	counts := map[string]count{}
	rows, err := s.pool.Query(ctx, `
		SELECT coalesce(v.storage_id::text, ''), count(*), count(*) FILTER (WHERE NOT v.encrypted)
		FROM state_versions v JOIN state_units u ON u.id = v.unit_id
		WHERE u.organization_id = $1 GROUP BY 1`, orgID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		var c count
		if err := rows.Scan(&id, &c.all, &c.plain); err != nil {
			rows.Close()
			return nil, err
		}
		counts[id] = c
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := &api.StateStorageSettings{Previous: []api.StateStorage{}}
	installation := api.StateStorage{Kind: api.StateStorageInstallation, Encrypt: true, Versions: counts[""].all}
	if current == nil {
		out.Current = installation
	} else if installation.Versions > 0 {
		out.Previous = append(out.Previous, installation)
	}
	rows, err = s.pool.Query(ctx, `
		SELECT id, endpoint, bucket, region, folder, path_style, coalesce(ca_cert, ''), access_key_id, encrypt
		FROM state_storages WHERE organization_id = $1 ORDER BY created_at`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		st := api.StateStorage{Kind: api.StateStorageS3, HasSecretAccessKey: true}
		if err := rows.Scan(&st.ID, &st.Endpoint, &st.Bucket, &st.Region, &st.Folder, &st.PathStyle, &st.CACert, &st.AccessKeyID,
			&st.Encrypt); err != nil {
			return nil, err
		}
		st.Versions, st.PlaintextVersions = counts[st.ID].all, counts[st.ID].plain
		if current != nil && *current == st.ID {
			out.Current = st
		} else {
			out.Previous = append(out.Previous, st)
		}
	}
	return out, rows.Err()
}

// CurrentStateStorageConfig returns the organization's own storage as stored,
// secret included, for checking it again; nil means the installation's.
func (s *Store) CurrentStateStorageConfig(ctx context.Context, orgID string) (*s3.Config, error) {
	st, err := s.currentOrgStorage(ctx, s.pool, orgID)
	if err != nil || st == nil {
		var se *StateStorageError
		if errors.As(err, &se) {
			return nil, &StateStorageCheckError{se.Err.Error()}
		}
		return nil, err
	}
	cfg := st.blobs.Config()
	return &cfg, nil
}
