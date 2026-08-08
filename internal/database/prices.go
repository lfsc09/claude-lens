package database

import (
	"context"
	"time"
)

// Price is a row of the model_prices table: USD per million tokens for a
// model prefix, matched against the full model string returned by the API.
type Price struct {
	Prefix     string
	InputPerM  float64
	OutputPerM float64
	UpdatedAt  float64
}

// defaultPrices seeds model_prices on first run — mirrors the hardcoded
// dict that used to live in the Python pricing.py.
var defaultPrices = []Price{
	{Prefix: "claude-opus-5", InputPerM: 5.00, OutputPerM: 25.00},
	{Prefix: "claude-sonnet-5", InputPerM: 3.00, OutputPerM: 15.00},
	{Prefix: "claude-haiku-4-5", InputPerM: 1.00, OutputPerM: 5.00},
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
		"SELECT model_prefix, input_per_m, output_per_m, updated_at FROM model_prices ORDER BY model_prefix",
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var prices []Price
	for rows.Next() {
		var p Price
		if err := rows.Scan(&p.Prefix, &p.InputPerM, &p.OutputPerM, &p.UpdatedAt); err != nil {
			return nil, err
		}
		prices = append(prices, p)
	}
	return prices, rows.Err()
}

// UpsertPrice inserts or replaces a model price row.
func (db *DB) UpsertPrice(ctx context.Context, p Price) error {
	_, err := db.sql.ExecContext(ctx,
		`INSERT INTO model_prices (model_prefix, input_per_m, output_per_m, updated_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(model_prefix) DO UPDATE SET
		     input_per_m = excluded.input_per_m,
		     output_per_m = excluded.output_per_m,
		     updated_at = excluded.updated_at`,
		p.Prefix, p.InputPerM, p.OutputPerM, p.UpdatedAt,
	)
	return err
}

// DeletePrice removes a model price row by prefix. It is not an error if the
// prefix does not exist.
func (db *DB) DeletePrice(ctx context.Context, prefix string) error {
	_, err := db.sql.ExecContext(ctx, "DELETE FROM model_prices WHERE model_prefix = ?", prefix)
	return err
}
