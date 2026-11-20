package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// BindProvider prevents IDs and watches from one platform being delivered on
// another. Telegram mode uses the legacy DiscordID fields as opaque user IDs.
func (s *Store) BindProvider(ctx context.Context, provider string) error {
	if provider != "discord" && provider != "telegram" {
		return errors.New("invalid notification provider")
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO notification_provider (singleton,provider) VALUES (1,?) ON CONFLICT DO NOTHING`, provider); err != nil {
			return err
		}
		var existing string
		if err := tx.QueryRowContext(ctx, `SELECT provider FROM notification_provider WHERE singleton=1`).Scan(&existing); err != nil {
			return err
		}
		if existing != provider {
			return fmt.Errorf("database belongs to %s; use a separate DB_PATH for %s and recreate account links and watches", existing, provider)
		}
		return nil
	})
}
