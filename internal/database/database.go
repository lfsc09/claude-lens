// Package database wraps the SQLite-backed persistence layer shared by the
// proxy and admin servers.
package database

import (
	"context"
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

// DB wraps a *sql.DB configured for claude-lens' schema and concurrency model.
type DB struct {
	sql *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS exchanges (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id     TEXT    NOT NULL,
    session_name   TEXT,
    path           TEXT    NOT NULL,
    timestamp      REAL    NOT NULL,
    is_streaming   INTEGER NOT NULL DEFAULT 0,
    input_messages TEXT,
    output_text    TEXT,
    input_tokens   INTEGER,
    output_tokens  INTEGER,
    model          TEXT,
    cost           REAL,
    input_cost     REAL,
    output_cost    REAL,
    raw_request    TEXT,
    raw_response   TEXT
);
CREATE INDEX IF NOT EXISTS idx_exchanges_session_id ON exchanges (session_id);
CREATE INDEX IF NOT EXISTS idx_exchanges_timestamp  ON exchanges (timestamp);

CREATE TABLE IF NOT EXISTS model_prices (
    model_prefix TEXT PRIMARY KEY,
    input_per_m  REAL NOT NULL,
    output_per_m REAL NOT NULL,
    updated_at   REAL NOT NULL
);
`

// Open opens (creating if necessary) the SQLite database at path, applies the
// schema, and seeds default model prices on first run.
//
// The connection pool is deliberately capped at a single connection: SQLite
// allows only one writer at a time, and both the proxy (writing on every
// intercepted POST) and the admin server (upserting/deleting prices) share
// this *DB from the same process. Serializing at the pool level avoids
// SQLITE_BUSY races without needing retry logic throughout the query code;
// busy_timeout gives any queued writer up to 5s to acquire the lock before
// failing outright.
func Open(ctx context.Context, path string) (*DB, error) {
	dsn := fmt.Sprintf("file:%s?_journal_mode=WAL&_busy_timeout=5000", path)

	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	sqlDB.SetMaxOpenConns(1)

	if _, err := sqlDB.ExecContext(ctx, schema); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}

	db := &DB{sql: sqlDB}

	if err := db.seedDefaultPrices(ctx); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("seed default prices: %w", err)
	}

	return db, nil
}

// Close closes the underlying connection pool.
func (db *DB) Close() error {
	return db.sql.Close()
}
