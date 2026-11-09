package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Watch levels, from least to most activity.
const (
	LevelNewPRs = "new-prs"
	LevelPRs    = "prs"
	LevelAll    = "all"
)

// Watch is an admin-configured watch: DiscordID is pinged for activity at or
// above Level, org-wide (Repo == "") or in one repo.
type Watch struct {
	DiscordID string
	Repo      string
	Level     string
	CreatedBy string
	CreatedAt time.Time
}

// Watcher is a watch joined with its owner's link, as needed to ping them.
type Watcher struct {
	DiscordID   string
	Repo        string
	Level       string
	GitHubID    int64
	GitHubLogin string
}

func validLevel(level string) bool {
	return level == LevelNewPRs || level == LevelPRs || level == LevelAll
}

// SetWatch creates a watch or changes the level of an existing one for the
// same user and repo.
func (s *Store) SetWatch(ctx context.Context, w Watch) error {
	if w.DiscordID == "" || w.CreatedBy == "" || !validLevel(w.Level) {
		return errors.New("set watch: discord id, creator and a valid level are required")
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO watchers (discord_id, repo, level, created_by, created_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (discord_id, repo) DO UPDATE SET
			level      = excluded.level,
			created_by = excluded.created_by`,
		w.DiscordID, w.Repo, w.Level, w.CreatedBy, s.timestamp())
	if err != nil {
		return fmt.Errorf("set watch: %w", err)
	}
	return nil
}

// RemoveWatch deletes one watch (repo "" is the org-wide one) and returns it.
func (s *Store) RemoveWatch(ctx context.Context, discordID, repo string) (Watch, error) {
	var removed Watch
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		w, err := scanWatch(tx.QueryRowContext(ctx,
			`SELECT discord_id, repo, level, created_by, created_at FROM watchers WHERE discord_id = ? AND repo = ?`,
			discordID, repo))
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM watchers WHERE discord_id = ? AND repo = ?`, discordID, repo); err != nil {
			return fmt.Errorf("remove watch: %w", err)
		}
		removed = w
		return nil
	})
	return removed, err
}

// ListWatches returns every watch, ordered by user then repo (org-wide first).
func (s *Store) ListWatches(ctx context.Context) ([]Watch, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT discord_id, repo, level, created_by, created_at FROM watchers ORDER BY discord_id, repo`)
	if err != nil {
		return nil, fmt.Errorf("list watches: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Watch
	for rows.Next() {
		w, err := scanWatch(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate watches: %w", err)
	}
	return out, nil
}

// WatchersFor returns the org-wide watches plus the watches for repo, for
// users who are linked (unlinked users can't be pinged).
func (s *Store) WatchersFor(ctx context.Context, repo string) ([]Watcher, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT w.discord_id, w.repo, w.level, l.github_id, l.github_login
		FROM watchers w JOIN links l ON l.discord_id = w.discord_id
		WHERE w.repo = '' OR w.repo = ?
		ORDER BY w.discord_id, w.repo`, repo)
	if err != nil {
		return nil, fmt.Errorf("query watchers: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Watcher
	for rows.Next() {
		var w Watcher
		if err := rows.Scan(&w.DiscordID, &w.Repo, &w.Level, &w.GitHubID, &w.GitHubLogin); err != nil {
			return nil, fmt.Errorf("scan watcher: %w", err)
		}
		out = append(out, w)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate watchers: %w", err)
	}
	return out, nil
}

// RecordRepo remembers a repo name seen in a webhook, for autocomplete.
func (s *Store) RecordRepo(ctx context.Context, name string) error {
	if name == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO seen_repos (name, last_seen) VALUES (?, ?)
		ON CONFLICT (name) DO UPDATE SET last_seen = excluded.last_seen`,
		name, s.timestamp())
	if err != nil {
		return fmt.Errorf("record repo: %w", err)
	}
	return nil
}

// ListRepos returns the repo names seen so far, alphabetically.
func (s *Store) ListRepos(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name FROM seen_repos ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list repos: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scan repo: %w", err)
		}
		out = append(out, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate repos: %w", err)
	}
	return out, nil
}

func scanWatch(r rowScanner) (Watch, error) {
	var (
		w       Watch
		created string
	)
	err := r.Scan(&w.DiscordID, &w.Repo, &w.Level, &w.CreatedBy, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return Watch{}, ErrNotFound
	}
	if err != nil {
		return Watch{}, fmt.Errorf("scan watch: %w", err)
	}
	if w.CreatedAt, err = parseTime(created); err != nil {
		return Watch{}, err
	}
	return w, nil
}

// deleteWatches removes all watches of a Discord user (inside a link change).
func deleteWatches(ctx context.Context, tx *sql.Tx, discordID string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM watchers WHERE discord_id = ?`, discordID); err != nil {
		return fmt.Errorf("delete watches: %w", err)
	}
	return nil
}
