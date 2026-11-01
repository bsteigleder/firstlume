// Package store is the Postgres persistence layer.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"

	"github.com/bsteigleder/firstlume/internal/secrets"
	"github.com/bsteigleder/firstlume/internal/vault"
	"github.com/bsteigleder/firstlume/migrations"
)

var (
	ErrNotFound = errors.New("not found")
	// ErrConflict means the resource exists but is not in a state that allows the operation.
	ErrConflict = errors.New("conflict")
	// ErrDuplicate means a unique constraint was violated.
	ErrDuplicate = errors.New("already exists")
	// ErrInvalid means the input references something that does not exist or is malformed.
	ErrInvalid = errors.New("invalid reference")
	// ErrTooDeep means a folder would nest deeper than api.MaxFolderDepth.
	ErrTooDeep = errors.New("folder too deep")
	// ErrMissingSecret means a new sensitive variable was sent without a value.
	ErrMissingSecret = errors.New("sensitive variable without a value")
)

type Store struct {
	pool  *pgxpool.Pool
	keys  *secrets.Keys
	blobs Blobs
	// keepVersions caps the state versions kept per unit; 0 keeps all.
	keepVersions int
	// signingKey caches the key run JWTs are signed with.
	signingMu  sync.Mutex
	signingKey *vault.SigningKey
}

func New(pool *pgxpool.Pool, keys *secrets.Keys) *Store {
	return &Store{pool: pool, keys: keys, blobs: pgBlobs{pool}}
}

// Connect opens a connection pool and verifies connectivity.
func Connect(ctx context.Context, url string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connecting to database: %w", err)
	}
	return pool, nil
}

// Migrate applies pending migrations. A Postgres advisory lock makes it safe to
// call from several replicas starting at the same time.
func Migrate(ctx context.Context, pool *pgxpool.Pool) ([]*goose.MigrationResult, error) {
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	return migrate(ctx, db, migrations.FS)
}

func migrate(ctx context.Context, db *sql.DB, fsys fs.FS) ([]*goose.MigrationResult, error) {
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return nil, err
	}
	p, err := goose.NewProvider(goose.DialectPostgres, db, fsys, goose.WithSessionLocker(locker))
	if err != nil {
		return nil, err
	}
	return p.Up(ctx)
}

func (s *Store) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// classify maps Postgres constraint errors to store errors.
func classify(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505": // unique_violation
			return ErrDuplicate
		case "23503": // foreign_key_violation
			return ErrInvalid
		}
	}
	return notFound(err)
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
