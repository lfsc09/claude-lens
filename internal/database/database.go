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

	// notifier drives Slack alert delivery from accrueLimiterCost. Unset
	// (nil) until SetNotifications is called, in which case alert dispatch
	// is silently skipped.
	notifier *notify.Client

	// alertMu serializes accrueLimiterCost's read-check-write of each
	// limiter's alert state, so two exchanges completing close together for
	// the same limiter can't both observe AlertSent == false and send a
	// duplicate budget-threshold alert.
	alertMu sync.Mutex
}

// SetNotifications wires up Slack delivery for limiter alerts (both the
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
    slack_webhook_url      TEXT    NOT NULL DEFAULT '',
    alert_threshold_pct    INTEGER,
    alert_sent             INTEGER NOT NULL DEFAULT 0,
    alert_request_cost_usd REAL,
    CHECK ((active_start_hour IS NULL) = (active_end_hour IS NULL)),
    CHECK (alert_threshold_pct IS NULL OR (alert_threshold_pct BETWEEN 1 AND 100)),
    CHECK (alert_request_cost_usd IS NULL OR alert_request_cost_usd > 0)
);
CREATE INDEX IF NOT EXISTS idx_limiters_session_id ON limiters (session_id);

CREATE TABLE IF NOT EXISTS settings (
    id                            INTEGER PRIMARY KEY CHECK (id = 1),
    litellm_sync_interval_minutes INTEGER NOT NULL DEFAULT 60,
    litellm_last_synced_at        REAL    NOT NULL DEFAULT 0,
    litellm_last_sync_error       TEXT    NOT NULL DEFAULT '',
    litellm_last_attempt_at       REAL    NOT NULL DEFAULT 0,
    updated_at                    REAL    NOT NULL
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

	return db, nil
}

// Close closes the underlying connection pool.
func (db *DB) Close() error {
	return db.sql.Close()
}
