// Package database wraps the SQLite-backed persistence layer shared by the
// proxy and admin servers.
package database

import (
	"context"
	"database/sql"
	"fmt"
	"sync"

	"github.com/lfsc09/claude-lens/internal/notify"
	_ "modernc.org/sqlite"
)

// DB wraps a *sql.DB configured for claude-lens' schema and concurrency model.
type DB struct {
	sql *sql.DB

	// notifier drives alert delivery from accrueLimiterCost. Unset
	// (nil) until SetNotifications is called, in which case alert dispatch
	// is silently skipped.
	notifier *notify.Client

	// alertMu serializes accrueLimiterCost's read-check-write of each
	// limiter's alert state, so two exchanges completing close together for
	// the same limiter can't both observe AlertSent == false and send a
	// duplicate budget-threshold alert.
	alertMu sync.Mutex
}

// SetNotifications wires up delivery for limiter alerts (both the
// budget-threshold and per-request-cost kinds). Safe to skip calling
// entirely — accrueLimiterCost just won't send alerts.
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
    id         INTEGER PRIMARY KEY CHECK (id = 1),
    updated_at REAL NOT NULL
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

// Open opens (creating if necessary) the SQLite database at path, applies the
// schema, and seeds default model prices on first run.
//
// The connection pool is deliberately capped at a single connection: SQLite
// allows only one writer at a time, and both the proxy (writing on every
// intercepted POST) and the admin server (upserting/deleting prices) share
// this *DB from the same process. Serializing at the pool level avoids
// SQLITE_BUSY races without needing retry logic throughout the query code;
// busy_timeout gives any queued writer up to 5s to acquire the lock before
// failing outright. foreign_keys(1) turns on SQLite's foreign-key
// enforcement (off by default per connection) so exchange_data's
// ON DELETE CASCADE actually fires.
//
// auto_vacuum(2) (INCREMENTAL) only takes effect when set before the first
// table is created, so it is listed first and runs before journal_mode(WAL)
// writes the database header.
func Open(ctx context.Context, path string) (*DB, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=auto_vacuum(2)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)", path)

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
	if err := db.seedDefaultSettings(ctx); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("seed default settings: %w", err)
	}
	if err := db.seedProxyIntegrations(ctx); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("seed proxy integrations: %w", err)
	}

	return db, nil
}

// Close closes the underlying connection pool.
func (db *DB) Close() error {
	return db.sql.Close()
}
