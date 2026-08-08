package database

import (
	"context"
	"database/sql"
	"time"
)

// Price is a row of the model_prices table: USD per million tokens for a
// model prefix, matched against the full model string returned by the API.
//
// CacheWritePerM/CacheReadPerM price Anthropic's prompt-caching tokens
// (cache_creation_input_tokens / cache_read_input_tokens), which are billed
// at different rates than plain input tokens — a write is pricier, a read
// much cheaper.
type Price struct {
	Prefix         string  `json:"model_prefix"`
	InputPerM      float64 `json:"input_per_m"`
	OutputPerM     float64 `json:"output_per_m"`
	CacheWritePerM float64 `json:"cache_write_per_m"`
	CacheReadPerM  float64 `json:"cache_read_per_m"`
	UpdatedAt      float64 `json:"updated_at"`
}

// defaultPrices seeds model_prices on first run — mirrors the hardcoded
// dict that used to live in the Python pricing.py. Cache write/read rates
// follow Anthropic's standard multipliers (1.25x input for a 5-minute-TTL
// cache write, 0.1x input for a cache read).
var defaultPrices = []Price{
	{Prefix: "claude-opus-5", InputPerM: 5.00, OutputPerM: 25.00, CacheWritePerM: 6.25, CacheReadPerM: 0.50},
	{Prefix: "claude-sonnet-5", InputPerM: 3.00, OutputPerM: 15.00, CacheWritePerM: 3.75, CacheReadPerM: 0.30},
	{Prefix: "claude-haiku-4-5", InputPerM: 1.00, OutputPerM: 5.00, CacheWritePerM: 1.25, CacheReadPerM: 0.10},
}

func (db *DB) seedDefaultPrices(ctx context.Context) error {
	var count int
	if err := db.sql.QueryRowContext(ctx, "SELECT COUNT(*) FROM model_prices").Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	now := float64(time.Now().Unix())
	for _, p := range defaultPrices {
		p.UpdatedAt = now
		if err := db.UpsertPrice(ctx, p); err != nil {
			return err
		}
	}
	return nil
}

// ListPrices returns all model price rows, ordered by prefix.
func (db *DB) ListPrices(ctx context.Context) ([]Price, error) {
	rows, err := db.sql.QueryContext(ctx,
		`SELECT model_prefix, input_per_m, output_per_m, cache_write_per_m, cache_read_per_m, updated_at
		 FROM model_prices ORDER BY model_prefix`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var prices []Price
	for rows.Next() {
		var p Price
		if err := rows.Scan(&p.Prefix, &p.InputPerM, &p.OutputPerM, &p.CacheWritePerM, &p.CacheReadPerM, &p.UpdatedAt); err != nil {
			return nil, err
		}
		prices = append(prices, p)
	}
	return prices, rows.Err()
}

// GetPrice returns the price row for an exact prefix, or nil if it doesn't
// exist yet.
func (db *DB) GetPrice(ctx context.Context, prefix string) (*Price, error) {
	var p Price
	err := db.sql.QueryRowContext(ctx,
		`SELECT model_prefix, input_per_m, output_per_m, cache_write_per_m, cache_read_per_m, updated_at
		 FROM model_prices WHERE model_prefix = ?`,
		prefix,
	).Scan(&p.Prefix, &p.InputPerM, &p.OutputPerM, &p.CacheWritePerM, &p.CacheReadPerM, &p.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// UpsertPrice inserts or replaces a model price row.
func (db *DB) UpsertPrice(ctx context.Context, p Price) error {
	_, err := db.sql.ExecContext(ctx,
		`INSERT INTO model_prices (model_prefix, input_per_m, output_per_m, cache_write_per_m, cache_read_per_m, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(model_prefix) DO UPDATE SET
		     input_per_m = excluded.input_per_m,
		     output_per_m = excluded.output_per_m,
		     cache_write_per_m = excluded.cache_write_per_m,
		     cache_read_per_m = excluded.cache_read_per_m,
		     updated_at = excluded.updated_at`,
		p.Prefix, p.InputPerM, p.OutputPerM, p.CacheWritePerM, p.CacheReadPerM, p.UpdatedAt,
	)
	return err
}

// DeletePrice removes a model price row by prefix. It is not an error if the
// prefix does not exist.
func (db *DB) DeletePrice(ctx context.Context, prefix string) error {
	_, err := db.sql.ExecContext(ctx, "DELETE FROM model_prices WHERE model_prefix = ?", prefix)
	return err
}
