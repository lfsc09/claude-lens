package database

import (
	"context"
	"time"
)

// seedDefaultSettings inserts the single global settings row on a fresh
// database. A no-op when the row already exists (see the ON CONFLICT
// clause).
func (db *DB) seedDefaultSettings(ctx context.Context) error {
	_, err := db.sql.ExecContext(ctx,
		`INSERT INTO settings (id, updated_at) VALUES (1, ?) ON CONFLICT (id) DO NOTHING`,
		float64(time.Now().Unix()),
	)
	return err
}
