package database

import (
	"context"
	"time"
)

// Settings is the single global settings row.
type Settings struct {
	SaveRawPayloads bool `json:"save_raw_payloads"`
}

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

// loadSettings primes the in-memory copies of the settings read on the
// proxy's hot path.
func (db *DB) loadSettings(ctx context.Context) error {
	s, err := db.GetSettings(ctx)
	if err != nil {
		return err
	}
	db.saveRawPayloads.Store(s.SaveRawPayloads)
	return nil
}

// GetSettings returns the current global settings.
func (db *DB) GetSettings(ctx context.Context) (Settings, error) {
	var s Settings
	var saveRaw int
	err := db.sql.QueryRowContext(ctx,
		`SELECT save_raw_payloads FROM settings WHERE id = 1`,
	).Scan(&saveRaw)
	s.SaveRawPayloads = saveRaw != 0
	return s, err
}

// SetSaveRawPayloads persists whether new exchanges keep their raw
// request/response payloads, and applies it to subsequent SaveExchange calls
// immediately.
func (db *DB) SetSaveRawPayloads(ctx context.Context, enabled bool) error {
	if _, err := db.sql.ExecContext(ctx,
		`UPDATE settings SET save_raw_payloads = ?, updated_at = ? WHERE id = 1`,
		boolToInt(enabled), float64(time.Now().Unix()),
	); err != nil {
		return err
	}
	db.saveRawPayloads.Store(enabled)
	return nil
}
