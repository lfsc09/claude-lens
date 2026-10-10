package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Proxy integration types, one seeded row each.
const (
	ProxyTypeLiteLLM = "litellm"
	ProxyTypeBifrost = "bifrost"
)

const defaultPriceSyncIntervalMinutes = 60

// ErrProxyIntegrationIncomplete is returned when activating a proxy
// integration that lacks a base URL or auth token.
var ErrProxyIntegrationIncomplete = errors.New("proxy integration needs a base URL and auth token to be activated")

// ProxyIntegration is a row of proxy_integrations: an upstream gateway the
// proxy forwards to and syncs model prices from. At most one row is Active
// at a time; with none active, requests go straight to the Anthropic API.
type ProxyIntegration struct {
	ID            int64             `json:"id"`
	Type          string            `json:"type"`
	Name          string            `json:"name"`
	BaseURL       string            `json:"base_url"`
	AuthToken     string            `json:"-"`
	CustomHeaders map[string]string `json:"custom_headers"`
	Active        bool              `json:"active"`

	// PriceSyncIntervalMinutes is how often internal/pricesync.RunLoop
	// re-syncs model_prices while this integration is active. 0 disables the
	// background loop; the manual sync is unaffected.
	PriceSyncIntervalMinutes int `json:"price_sync_interval_minutes"`
	// PriceSyncedAt is when a sync last completed successfully.
	PriceSyncedAt float64 `json:"price_synced_at"`
	// PriceSyncAttemptedAt is when a sync was last attempted, successful or
	// not; RunLoop waits a full interval after it before retrying.
	PriceSyncAttemptedAt float64 `json:"price_sync_attempted_at"`
	// PriceSyncError is the error of the most recent attempt, or "" if it
	// succeeded (or none has run).
	PriceSyncError string  `json:"price_sync_error"`
	UpdatedAt      float64 `json:"updated_at"`
}

const proxyIntegrationColumns = `id, type, name, base_url, auth_token, custom_headers, active,
	price_sync_interval_minutes, price_synced_at, price_sync_attempted_at, price_sync_error, updated_at`

// seedProxyIntegrations inserts one inactive, unconfigured row per proxy
// type on a fresh database; a no-op for rows that already exist.
func (db *DB) seedProxyIntegrations(ctx context.Context) error {
	now := float64(time.Now().Unix())
	for _, seed := range [...]struct{ typ, name string }{
		{ProxyTypeLiteLLM, "LiteLLM"},
		{ProxyTypeBifrost, "Bifrost"},
	} {
		if _, err := db.sql.ExecContext(ctx,
			`INSERT INTO proxy_integrations (type, name, price_sync_interval_minutes, updated_at)
			 VALUES (?, ?, ?, ?) ON CONFLICT (type) DO NOTHING`,
			seed.typ, seed.name, defaultPriceSyncIntervalMinutes, now,
		); err != nil {
			return err
		}
	}
	return nil
}

func scanProxyIntegration(row interface{ Scan(...any) error }) (ProxyIntegration, error) {
	var p ProxyIntegration
	var headers string
	var active int
	err := row.Scan(&p.ID, &p.Type, &p.Name, &p.BaseURL, &p.AuthToken, &headers, &active,
		&p.PriceSyncIntervalMinutes, &p.PriceSyncedAt, &p.PriceSyncAttemptedAt, &p.PriceSyncError, &p.UpdatedAt)
	if err != nil {
		return p, err
	}
	p.Active = active != 0
	if err := json.Unmarshal([]byte(headers), &p.CustomHeaders); err != nil {
		return p, fmt.Errorf("decode custom headers: %w", err)
	}
	return p, nil
}

// ListProxyIntegrations returns every proxy integration in seed order.
func (db *DB) ListProxyIntegrations(ctx context.Context) ([]ProxyIntegration, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT `+proxyIntegrationColumns+` FROM proxy_integrations ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	integrations := make([]ProxyIntegration, 0, 2)
	for rows.Next() {
		p, err := scanProxyIntegration(rows)
		if err != nil {
			return nil, err
		}
		integrations = append(integrations, p)
	}
	return integrations, rows.Err()
}

// GetProxyIntegration returns the integration with the given id, or nil if
// it doesn't exist.
func (db *DB) GetProxyIntegration(ctx context.Context, id int64) (*ProxyIntegration, error) {
	return db.queryProxyIntegration(ctx, `SELECT `+proxyIntegrationColumns+` FROM proxy_integrations WHERE id = ?`, id)
}

// GetActiveProxyIntegration returns the active integration, or nil if none
// is active.
func (db *DB) GetActiveProxyIntegration(ctx context.Context) (*ProxyIntegration, error) {
	return db.queryProxyIntegration(ctx, `SELECT `+proxyIntegrationColumns+` FROM proxy_integrations WHERE active = 1`)
}

func (db *DB) queryProxyIntegration(ctx context.Context, query string, args ...any) (*ProxyIntegration, error) {
	p, err := scanProxyIntegration(db.sql.QueryRowContext(ctx, query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// UpdateProxyIntegration replaces an integration's editable fields. Active
// state and sync bookkeeping are left untouched.
func (db *DB) UpdateProxyIntegration(ctx context.Context, p ProxyIntegration) error {
	headers, err := json.Marshal(p.CustomHeaders)
	if err != nil {
		return fmt.Errorf("encode custom headers: %w", err)
	}
	_, err = db.sql.ExecContext(ctx,
		`UPDATE proxy_integrations SET base_url = ?, auth_token = ?, custom_headers = ?,
			price_sync_interval_minutes = ?, updated_at = ? WHERE id = ?`,
		p.BaseURL, p.AuthToken, string(headers), p.PriceSyncIntervalMinutes, p.UpdatedAt, p.ID,
	)
	return err
}

// ActivateProxyIntegration makes id the single active integration,
// deactivating any other in the same transaction, and marks its price sync
// as due so RunLoop syncs right away. Returns ErrProxyIntegrationIncomplete
// if it has no base URL or auth token, and sql.ErrNoRows if id doesn't exist.
func (db *DB) ActivateProxyIntegration(ctx context.Context, id int64, at float64) error {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var baseURL, token string
	if err := tx.QueryRowContext(ctx, `SELECT base_url, auth_token FROM proxy_integrations WHERE id = ?`, id).Scan(&baseURL, &token); err != nil {
		return err
	}
	if baseURL == "" || token == "" {
		return ErrProxyIntegrationIncomplete
	}
	if _, err := tx.ExecContext(ctx, `UPDATE proxy_integrations SET active = 0, updated_at = ? WHERE active = 1 AND id != ?`, at, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE proxy_integrations SET active = 1, price_sync_attempted_at = 0, updated_at = ? WHERE id = ?`, at, id); err != nil {
		return err
	}
	return tx.Commit()
}

// DeactivateProxyIntegration turns id off; with no active integration left,
// requests go directly to the Anthropic API.
func (db *DB) DeactivateProxyIntegration(ctx context.Context, id int64, at float64) error {
	_, err := db.sql.ExecContext(ctx, `UPDATE proxy_integrations SET active = 0, updated_at = ? WHERE id = ?`, at, id)
	return err
}

// MarkPriceSynced records a successful price sync for id, clearing any
// previously recorded failure.
func (db *DB) MarkPriceSynced(ctx context.Context, id int64, at float64) error {
	_, err := db.sql.ExecContext(ctx,
		`UPDATE proxy_integrations SET price_synced_at = ?, price_sync_attempted_at = ?, price_sync_error = '' WHERE id = ?`,
		at, at, id,
	)
	return err
}

// MarkPriceSyncFailed records a failed price sync attempt for id without
// touching PriceSyncedAt, which means "last success".
func (db *DB) MarkPriceSyncFailed(ctx context.Context, id int64, errMsg string, at float64) error {
	_, err := db.sql.ExecContext(ctx,
		`UPDATE proxy_integrations SET price_sync_error = ?, price_sync_attempted_at = ? WHERE id = ?`,
		errMsg, at, id,
	)
	return err
}
