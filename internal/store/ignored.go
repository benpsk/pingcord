package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Ignored is a GitHub user that is never pinged.
type Ignored struct {
	GitHubID    int64
	GitHubLogin string
	CreatedBy   string
	CreatedAt   time.Time
}

// Ignore marks a GitHub user as never-ping. Ignoring twice is a no-op.
func (s *Store) Ignore(ctx context.Context, githubID int64, login, createdBy string) error {
	if githubID <= 0 || login == "" || createdBy == "" {
		return errors.New("ignore: github id, login and creator are required")
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO ignored (github_id, github_login, created_by, created_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (github_id) DO UPDATE SET github_login = excluded.github_login`,
		githubID, login, createdBy, s.timestamp())
	if err != nil {
		return fmt.Errorf("ignore user: %w", err)
	}
	return nil
}

// Unignore removes an ignored user by login (case-insensitive) and returns it.
func (s *Store) Unignore(ctx context.Context, login string) (Ignored, error) {
	var removed Ignored
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		ig, err := scanIgnored(tx.QueryRowContext(ctx,
			`SELECT github_id, github_login, created_by, created_at FROM ignored WHERE github_login = ?`, login))
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM ignored WHERE github_id = ?`, ig.GitHubID); err != nil {
			return fmt.Errorf("unignore user: %w", err)
		}
		removed = ig
		return nil
	})
	return removed, err
}

// IgnoredIDs returns which of ids are ignored.
func (s *Store) IgnoredIDs(ctx context.Context, ids []int64) (map[int64]bool, error) {
	out := make(map[int64]bool, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	placeholders, args := inClause(ids)
	rows, err := s.db.QueryContext(ctx,
		//nolint:gosec // G202: only "?" placeholders are concatenated; values are bound args.
		`SELECT github_id FROM ignored WHERE github_id IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("query ignored: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan ignored id: %w", err)
		}
		out[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate ignored: %w", err)
	}
	return out, nil
}

// ListIgnored returns all ignored users ordered by login.
func (s *Store) ListIgnored(ctx context.Context) ([]Ignored, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT github_id, github_login, created_by, created_at FROM ignored ORDER BY github_login`)
	if err != nil {
		return nil, fmt.Errorf("list ignored: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Ignored
	for rows.Next() {
		ig, err := scanIgnored(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ig)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate ignored: %w", err)
	}
	return out, nil
}

func scanIgnored(r rowScanner) (Ignored, error) {
	var (
		ig      Ignored
		created string
	)
	err := r.Scan(&ig.GitHubID, &ig.GitHubLogin, &ig.CreatedBy, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return Ignored{}, ErrNotFound
	}
	if err != nil {
		return Ignored{}, fmt.Errorf("scan ignored: %w", err)
	}
	if ig.CreatedAt, err = parseTime(created); err != nil {
		return Ignored{}, err
	}
	return ig, nil
}
