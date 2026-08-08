// Package database wraps the SQLite-backed persistence layer shared by the
// proxy and admin servers.
package database

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	_ "modernc.org/sqlite"
)

// DB wraps a *sql.DB configured for claude-lens' schema and concurrency model.
type DB struct {
	sql *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS exchanges (
    id                    INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id            TEXT    NOT NULL,
    session_name          TEXT,
    path                  TEXT    NOT NULL,
    timestamp             REAL    NOT NULL,
    is_streaming          INTEGER NOT NULL DEFAULT 0,
    input_messages        TEXT,
    output_text           TEXT,
    input_tokens          INTEGER,
    output_tokens         INTEGER,
    cache_creation_tokens INTEGER,
    cache_read_tokens     INTEGER,
    model                 TEXT,
    cost                  REAL,
    input_cost            REAL,
    output_cost           REAL,
    cache_creation_cost   REAL,
    cache_read_cost       REAL,
    raw_request           TEXT,
    raw_response          TEXT
);
CREATE INDEX IF NOT EXISTS idx_exchanges_session_id ON exchanges (session_id);
CREATE INDEX IF NOT EXISTS idx_exchanges_timestamp  ON exchanges (timestamp);

CREATE TABLE IF NOT EXISTS model_prices (
    model_prefix     TEXT PRIMARY KEY,
    input_per_m      REAL NOT NULL,
    output_per_m     REAL NOT NULL,
    cache_write_per_m REAL NOT NULL DEFAULT 0,
    cache_read_per_m  REAL NOT NULL DEFAULT 0,
    updated_at       REAL NOT NULL
);
`

// newColumns lists columns added to the schema after the tables already
// shipped, keyed by table. CREATE TABLE IF NOT EXISTS above is a no-op on a
// database that already has these tables, so on an existing installation
// these columns must be added explicitly. Safe to run on every startup:
// addColumnIfMissing checks PRAGMA table_info before altering.
var newColumns = map[string][]string{
	"exchanges": {
		"cache_creation_tokens INTEGER",
		"cache_read_tokens INTEGER",
		"cache_creation_cost REAL",
		"cache_read_cost REAL",
	},
	"model_prices": {
		"cache_write_per_m REAL NOT NULL DEFAULT 0",
		"cache_read_per_m REAL NOT NULL DEFAULT 0",
	},
}

// migrateSchema adds any column listed in newColumns that isn't already
// present on its table. Idempotent — freshly created tables (which already
// have every column via the schema constant above) are left untouched.
func migrateSchema(ctx context.Context, sqlDB *sql.DB) error {
	for table, columns := range newColumns {
		existing, err := tableColumns(ctx, sqlDB, table)
		if err != nil {
			return fmt.Errorf("inspect columns of %s: %w", table, err)
		}
		for _, col := range columns {
			name := strings.Fields(col)[0]
			if existing[name] {
				continue
			}
			if _, err := sqlDB.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s", table, col)); err != nil {
				return fmt.Errorf("add column %s.%s: %w", table, name, err)
			}
		}
	}
	return nil
}

func tableColumns(ctx context.Context, sqlDB *sql.DB, table string) (map[string]bool, error) {
	rows, err := sqlDB.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cols := make(map[string]bool)
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull int
		var dfltValue any
		var pk int
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dfltValue, &pk); err != nil {
			return nil, err
		}
		cols[name] = true
	}
	return cols, rows.Err()
}

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
	if err := migrateSchema(ctx, sqlDB); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("migrate schema: %w", err)
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
