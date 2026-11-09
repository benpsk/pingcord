// Package store persists GitHub ↔ Discord links, ignored users and the
// login → ID cache in SQLite.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite" // registers the pure-Go "sqlite" driver
)

// ErrNotFound is returned when a requested record does not exist.
var ErrNotFound = errors.New("not found")

//go:embed migrations/*.sql
var migrationFS embed.FS

// Store is a SQLite-backed repository. It is safe for concurrent use.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

// Option configures a Store.
type Option func(*Store)

// WithClock overrides the time source (for tests).
func WithClock(now func() time.Time) Option {
	return func(s *Store) { s.now = now }
}

// Open opens (creating if needed) the database at path and applies pending
// migrations.
func Open(ctx context.Context, path string, opts ...Option) (*Store, error) {
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	// SQLite allows a single writer; one connection avoids SQLITE_BUSY churn.
	db.SetMaxOpenConns(1)

	s := &Store{db: db, now: time.Now}
	for _, opt := range opts {
		opt(s)
	}
	if err := s.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) timestamp() string {
	return s.now().UTC().Format(time.RFC3339Nano)
}

func parseTime(v string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse stored time %q: %w", v, err)
	}
	return t, nil
}

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx,
		`CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("create schema_version: %w", err)
	}
	var current int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&current); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}

	names, err := fs.Glob(migrationFS, "migrations/*.sql")
	if err != nil {
		return fmt.Errorf("list migrations: %w", err)
	}
	slices.Sort(names)

	for _, name := range names {
		version, err := migrationVersion(name)
		if err != nil {
			return err
		}
		if version <= current {
			continue
		}
		if err := s.applyMigration(ctx, name, version); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) applyMigration(ctx context.Context, name string, version int) error {
	body, err := migrationFS.ReadFile(name)
	if err != nil {
		return fmt.Errorf("read migration %s: %w", name, err)
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_version (version) VALUES (?)`, version); err != nil {
			return fmt.Errorf("record migration %s: %w", name, err)
		}
		return nil
	})
}

// migrationVersion parses the numeric prefix of "migrations/001_init.sql".
func migrationVersion(name string) (int, error) {
	base := strings.TrimPrefix(name, "migrations/")
	prefix, _, _ := strings.Cut(base, "_")
	v, err := strconv.Atoi(prefix)
	if err != nil {
		return 0, fmt.Errorf("migration %s: missing numeric prefix", name)
	}
	return v, nil
}

func (s *Store) inTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

// inClause returns "?,?,?" and the args for a deduplicated ID list.
func inClause(ids []int64) (string, []any) {
	uniq := slices.Compact(slices.Sorted(slices.Values(ids)))
	args := make([]any, len(uniq))
	for i, id := range uniq {
		args[i] = id
	}
	return strings.TrimSuffix(strings.Repeat("?,", len(uniq)), ","), args
}
