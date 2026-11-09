package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Link connects a GitHub account to a Discord user.
type Link struct {
	GitHubID    int64
	GitHubLogin string
	DiscordID   string
	CreatedBy   string // Discord ID of the admin who made the link
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

const linkColumns = `github_id, github_login, discord_id, created_by, created_at, updated_at`

// UpsertLink stores l, replacing any existing link for either the GitHub
// account or the Discord user so that both sides stay one-to-one.
func (s *Store) UpsertLink(ctx context.Context, l Link) error {
	if l.GitHubID <= 0 || l.GitHubLogin == "" || l.DiscordID == "" || l.CreatedBy == "" {
		return errors.New("upsert link: github id, login, discord id and creator are required")
	}
	now := s.timestamp()
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM links WHERE discord_id = ? AND github_id <> ?`, l.DiscordID, l.GitHubID); err != nil {
			return fmt.Errorf("upsert link: clear previous discord link: %w", err)
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO links (`+linkColumns+`) VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT (github_id) DO UPDATE SET
				github_login = excluded.github_login,
				discord_id   = excluded.discord_id,
				created_by   = excluded.created_by,
				updated_at   = excluded.updated_at`,
			l.GitHubID, l.GitHubLogin, l.DiscordID, l.CreatedBy, now, now)
		if err != nil {
			return fmt.Errorf("upsert link: %w", err)
		}
		return nil
	})
}

// DeleteLinkByDiscordID removes the link for a Discord user and returns it.
func (s *Store) DeleteLinkByDiscordID(ctx context.Context, discordID string) (Link, error) {
	var removed Link
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		l, err := scanLink(tx.QueryRowContext(ctx,
			`SELECT `+linkColumns+` FROM links WHERE discord_id = ?`, discordID))
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM links WHERE discord_id = ?`, discordID); err != nil {
			return fmt.Errorf("delete link: %w", err)
		}
		removed = l
		return nil
	})
	return removed, err
}

// ListLinks returns all links ordered by GitHub login.
func (s *Store) ListLinks(ctx context.Context) ([]Link, error) {
	return s.queryLinks(ctx, `SELECT `+linkColumns+` FROM links ORDER BY github_login`)
}

// LinksByGitHubIDs returns the links for the given GitHub IDs, keyed by ID.
// IDs without a link are absent from the result.
func (s *Store) LinksByGitHubIDs(ctx context.Context, ids []int64) (map[int64]Link, error) {
	out := make(map[int64]Link, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	placeholders, args := inClause(ids)
	links, err := s.queryLinks(ctx,
		`SELECT `+linkColumns+` FROM links WHERE github_id IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, err
	}
	for _, l := range links {
		out[l.GitHubID] = l
	}
	return out, nil
}

func (s *Store) queryLinks(ctx context.Context, query string, args ...any) ([]Link, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query links: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var links []Link
	for rows.Next() {
		l, err := scanLink(rows)
		if err != nil {
			return nil, err
		}
		links = append(links, l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate links: %w", err)
	}
	return links, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanLink(r rowScanner) (Link, error) {
	var (
		l                Link
		created, updated string
	)
	err := r.Scan(&l.GitHubID, &l.GitHubLogin, &l.DiscordID, &l.CreatedBy, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return Link{}, ErrNotFound
	}
	if err != nil {
		return Link{}, fmt.Errorf("scan link: %w", err)
	}
	if l.CreatedAt, err = parseTime(created); err != nil {
		return Link{}, err
	}
	if l.UpdatedAt, err = parseTime(updated); err != nil {
		return Link{}, err
	}
	return l, nil
}
