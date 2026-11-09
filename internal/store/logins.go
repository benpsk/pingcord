package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// CacheLogin records that login currently belongs to GitHub user id.
func (s *Store) CacheLogin(ctx context.Context, login string, id int64) error {
	return cacheLogin(ctx, s.db, login, id, s.timestamp())
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func cacheLogin(ctx context.Context, db execer, login string, id int64, now string) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO login_cache (github_login, github_id, fetched_at) VALUES (?, ?, ?)
		ON CONFLICT (github_login) DO UPDATE SET
			github_id  = excluded.github_id,
			fetched_at = excluded.fetched_at`,
		login, id, now)
	if err != nil {
		return fmt.Errorf("cache login: %w", err)
	}
	return nil
}

// LookupLogin returns the cached GitHub ID for login (case-insensitive) and
// when it was fetched. found is false when the login is not cached.
func (s *Store) LookupLogin(ctx context.Context, login string) (id int64, fetchedAt time.Time, found bool, err error) {
	var fetched string
	err = s.db.QueryRowContext(ctx,
		`SELECT github_id, fetched_at FROM login_cache WHERE github_login = ?`, login).Scan(&id, &fetched)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, time.Time{}, false, nil
	}
	if err != nil {
		return 0, time.Time{}, false, fmt.Errorf("lookup login: %w", err)
	}
	fetchedAt, err = parseTime(fetched)
	if err != nil {
		return 0, time.Time{}, false, err
	}
	return id, fetchedAt, true, nil
}

// RefreshLogins applies the current id → login pairs seen in a webhook
// payload: renamed users get their stored login updated and stale cache
// entries for their old login are dropped.
func (s *Store) RefreshLogins(ctx context.Context, logins map[int64]string) error {
	now := s.timestamp()
	return s.inTx(ctx, func(tx *sql.Tx) error {
		for id, login := range logins {
			if id <= 0 || login == "" {
				continue
			}
			stmts := []struct {
				query string
				args  []any
			}{
				{`UPDATE links SET github_login = ? WHERE github_id = ? AND github_login <> ?`, []any{login, id, login}},
				{`UPDATE ignored SET github_login = ? WHERE github_id = ? AND github_login <> ?`, []any{login, id, login}},
				{`DELETE FROM login_cache WHERE github_id = ? AND github_login <> ?`, []any{id, login}},
			}
			for _, st := range stmts {
				if _, err := tx.ExecContext(ctx, st.query, st.args...); err != nil {
					return fmt.Errorf("refresh login %d: %w", id, err)
				}
			}
			if err := cacheLogin(ctx, tx, login, id, now); err != nil {
				return err
			}
		}
		return nil
	})
}
