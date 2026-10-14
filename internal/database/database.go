// Package database wraps the SQLite-backed persistence layer shared by the
// proxy and admin servers.
package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync/atomic"

	"github.com/lfsc09/claude-lens/internal/notify"
	_ "modernc.org/sqlite"
)

// DB wraps a *sql.DB configured for claude-lens' schema and concurrency model.
type DB struct {
	sql *sql.DB

	// notifier delivers limiter alerts; nil disables alerting.
	notifier *notify.Client

	// saveRawPayloads mirrors settings.save_raw_payloads so SaveExchange can
	// check it per request without a query.
	saveRawPayloads atomic.Bool
}

// SetNotifications sets the client that delivers limiter alerts.
func (db *DB) SetNotifications(client *notify.Client) {
	db.notifier = client
}

const schema = `
CREATE TABLE IF NOT EXISTS sessions (
    session_id TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    updated_at REAL NOT NULL
);

CREATE TABLE IF NOT EXISTS exchanges (
    id                    INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id            TEXT    NOT NULL,
    path                  TEXT    NOT NULL,
    created_at            REAL    NOT NULL,
    deleted_at            REAL,
    is_streaming          INTEGER NOT NULL DEFAULT 0,
    model                 TEXT,
    input_tokens          INTEGER,
    output_tokens         INTEGER,
    cache_creation_tokens INTEGER,
    cache_read_tokens     INTEGER,
    cost                  REAL,
    input_cost            REAL,
    output_cost           REAL,
    cache_creation_cost   REAL,
    cache_read_cost       REAL,
    matched_price         TEXT
);
CREATE INDEX IF NOT EXISTS idx_exchanges_session_created ON exchanges (session_id, created_at);
CREATE INDEX IF NOT EXISTS idx_exchanges_created_at      ON exchanges (created_at);

CREATE TABLE IF NOT EXISTS exchange_data (
    exchange_id  INTEGER PRIMARY KEY REFERENCES exchanges(id) ON DELETE CASCADE,
    raw_request  TEXT NOT NULL,
    raw_response TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS model_prices (
    id                           INTEGER PRIMARY KEY AUTOINCREMENT,
    model_prefix                 TEXT    NOT NULL UNIQUE,
    input_per_m                  REAL    NOT NULL,
    output_per_m                 REAL    NOT NULL,
    cache_write_per_m            REAL    NOT NULL DEFAULT 0,
    cache_read_per_m             REAL    NOT NULL DEFAULT 0,
    input_per_m_above_200k       REAL,
    output_per_m_above_200k      REAL,
    cache_write_per_m_above_200k REAL,
    cache_read_per_m_above_200k  REAL,
    created_at                   REAL    NOT NULL,
    updated_at                   REAL    NOT NULL
);

CREATE TABLE IF NOT EXISTS limiters (
    id                     INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id             TEXT    NOT NULL DEFAULT '',
    limit_amount           REAL    NOT NULL,
    current_cost           REAL    NOT NULL DEFAULT 0,
    refresh_value          INTEGER NOT NULL,
    refresh_unit           TEXT    NOT NULL CHECK (refresh_unit IN ('minutes','hours','days','months')),
    refresh_aligned        INTEGER NOT NULL DEFAULT 0,
    next_refresh_at        REAL    NOT NULL,
    active_start_hour      INTEGER,
    active_end_hour        INTEGER,
    is_active              INTEGER NOT NULL DEFAULT 1,
    created_at             REAL    NOT NULL,
    updated_at             REAL    NOT NULL,
    CHECK ((active_start_hour IS NULL) = (active_end_hour IS NULL))
);
CREATE INDEX IF NOT EXISTS idx_limiters_session_id ON limiters (session_id);

CREATE TABLE IF NOT EXISTS settings (
    id                INTEGER PRIMARY KEY CHECK (id = 1),
    save_raw_payloads INTEGER NOT NULL DEFAULT 0 CHECK (save_raw_payloads IN (0, 1)),
    updated_at        REAL    NOT NULL
);

CREATE TABLE IF NOT EXISTS proxy_integrations (
    id                          INTEGER PRIMARY KEY AUTOINCREMENT,
    type                        TEXT    NOT NULL UNIQUE CHECK (type IN ('litellm','bifrost')),
    name                        TEXT    NOT NULL,
    base_url                    TEXT    NOT NULL DEFAULT '',
    auth_token                  TEXT    NOT NULL DEFAULT '',
    custom_headers              TEXT    NOT NULL DEFAULT '{}',
    active                      INTEGER NOT NULL DEFAULT 0,
    price_sync_interval_minutes INTEGER NOT NULL DEFAULT 60 CHECK (price_sync_interval_minutes >= 0),
    price_synced_at             REAL    NOT NULL DEFAULT 0,
    price_sync_attempted_at     REAL    NOT NULL DEFAULT 0,
    price_sync_error            TEXT    NOT NULL DEFAULT '',
    updated_at                  REAL    NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_proxy_integrations_single_active ON proxy_integrations (active) WHERE active = 1;

CREATE TABLE IF NOT EXISTS notification_integrations (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    type        TEXT    NOT NULL CHECK (type IN ('slack')),
    name        TEXT    NOT NULL,
    webhook_url TEXT    NOT NULL UNIQUE,
    active      INTEGER NOT NULL DEFAULT 1,
    created_at  REAL    NOT NULL,
    updated_at  REAL    NOT NULL
);

CREATE TABLE IF NOT EXISTS limiter_notifications (
    limiter_id       INTEGER NOT NULL REFERENCES limiters(id) ON DELETE CASCADE,
    notification_id  INTEGER NOT NULL REFERENCES notification_integrations(id) ON DELETE CASCADE,
    threshold_pct    INTEGER,
    request_cost_usd REAL,
    alert_sent       INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (limiter_id, notification_id),
    CHECK (threshold_pct IS NOT NULL OR request_cost_usd IS NOT NULL),
    CHECK (threshold_pct IS NULL OR (threshold_pct BETWEEN 1 AND 100)),
    CHECK (request_cost_usd IS NULL OR request_cost_usd > 0)
);
`

// schemaVersion is stored in the database's user_version pragma and must
// match for an existing database to be opened.
const schemaVersion = 2

const databaseFileMode = 0o600

// ErrIncompatibleSchema is returned by Open when the database file was
// created by a different schema version.
var ErrIncompatibleSchema = errors.New("database was created by an incompatible claude-lens version")

// Open opens (creating if necessary) the SQLite database at path with
// owner-only permissions, applies the schema, and seeds default prices,
// settings and proxy integrations on first run. It fails with
// ErrIncompatibleSchema when path holds a database of another schema version.
//
// The pool holds a single connection, which serializes all writes;
// busy_timeout lets a queued writer wait up to 5s for the lock. auto_vacuum
// is listed first because it only takes effect before the first table exists.
func Open(ctx context.Context, path string) (*DB, error) {
	if err := restrictFile(path); err != nil {
		return nil, fmt.Errorf("secure database file: %w", err)
	}

	dsn := fmt.Sprintf("file:%s?_pragma=auto_vacuum(2)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)", path)

	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	sqlDB.SetMaxOpenConns(1)

	if err := applySchema(ctx, sqlDB, path); err != nil {
		sqlDB.Close()
		return nil, err
	}

	db := &DB{sql: sqlDB}

	if err := db.seedDefaultPrices(ctx); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("seed default prices: %w", err)
	}
	if err := db.seedDefaultSettings(ctx); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("seed default settings: %w", err)
	}
	if err := db.loadSettings(ctx); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("load settings: %w", err)
	}
	if err := db.seedProxyIntegrations(ctx); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("seed proxy integrations: %w", err)
	}

	return db, nil
}

// restrictFile creates the file at path if missing and limits it to its
// owner. SQLite gives its -wal and -shm files the same mode.
func restrictFile(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, databaseFileMode)
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Chmod(path, databaseFileMode)
}

// applySchema creates the schema on an empty database and stamps it with
// schemaVersion; a database that already has tables must carry that version.
func applySchema(ctx context.Context, sqlDB *sql.DB, path string) error {
	var version, tables int
	if err := sqlDB.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if err := sqlDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table'`).Scan(&tables); err != nil {
		return fmt.Errorf("inspect database: %w", err)
	}
	if tables > 0 && version != schemaVersion {
		return fmt.Errorf("%w: remove %s to start fresh", ErrIncompatibleSchema, path)
	}

	if _, err := sqlDB.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	if _, err := sqlDB.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, schemaVersion)); err != nil {
		return fmt.Errorf("stamp schema version: %w", err)
	}
	return nil
}

// Close closes the underlying connection pool.
func (db *DB) Close() error {
	return db.sql.Close()
}
