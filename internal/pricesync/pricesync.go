// Package pricesync keeps the model_prices table in sync with the active
// proxy integration's per-model rates, on demand and on the integration's
// own schedule.
package pricesync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/lfsc09/claude-lens/internal/database"
	"github.com/lfsc09/claude-lens/internal/priceprovider"
	"github.com/lfsc09/claude-lens/internal/pricing"
	"github.com/lfsc09/claude-lens/internal/upstream"
)

// Result reports what a Sync call did, in a shape the admin API can return
// to the client as-is.
type Result struct {
	Created int      `json:"created"`
	Updated int      `json:"updated"`
	Models  []string `json:"models"`
}

// ErrUpstreamUnavailable wraps a Sync failure caused by the price fetch
// itself, as opposed to a later failure such as a database write.
var ErrUpstreamUnavailable = errors.New("price sync upstream unavailable")

// ErrNoPrices wraps a Sync failure where the gateway answered but reported no
// usable price for any model.
var ErrNoPrices = errors.New("gateway reported no model prices")

// Fetchers maps a proxy integration type to the client that reads model
// prices from that kind of gateway.
type Fetchers map[string]priceprovider.Fetcher

// Sync pulls per-model rates from integration's gateway via fetchers, upserts
// them with database.UpsertPricesFromSync, refreshes est and records the
// completion time on the integration. A failed fetch, or one that yields no
// price (ErrNoPrices), changes no price, is recorded on the integration and
// is returned wrapped in ErrUpstreamUnavailable.
func Sync(ctx context.Context, db *database.DB, est *pricing.Estimator, fetchers Fetchers, integration database.ProxyIntegration) (Result, error) {
	now := float64(time.Now().Unix())

	fetcher, ok := fetchers[integration.Type]
	if !ok {
		return Result{}, fmt.Errorf("no price fetcher for integration type %q", integration.Type)
	}

	prices, fetchErr := fetcher.FetchModelPrices(ctx, integration.BaseURL, integration.AuthToken)
	if fetchErr == nil && len(prices) == 0 {
		fetchErr = ErrNoPrices
	}
	if fetchErr != nil {
		if err := db.MarkPriceSyncFailed(ctx, integration.ID, fetchErr.Error(), now); err != nil {
			slog.Warn("record price sync failure", "integration", integration.Type, "error", err)
		}
		return Result{}, fmt.Errorf("%w: %w", ErrUpstreamUnavailable, fetchErr)
	}

	created, updated, err := db.UpsertPricesFromSync(ctx, prices, now)
	if err != nil {
		return Result{}, fmt.Errorf("upsert prices: %w", err)
	}
	result := Result{Created: created, Updated: updated, Models: make([]string, 0, len(prices))}
	for _, p := range prices {
		result.Models = append(result.Models, p.ModelName)
	}

	if err := est.Refresh(ctx); err != nil {
		return Result{}, fmt.Errorf("refresh estimator: %w", err)
	}
	if err := db.MarkPriceSynced(ctx, integration.ID, now); err != nil {
		return Result{}, fmt.Errorf("mark synced: %w", err)
	}
	return result, nil
}

// pollInterval is how often RunLoop checks whether a sync is due.
const pollInterval = time.Minute

// syncDue reports whether integration's PriceSyncIntervalMinutes has elapsed
// since the later of its last success and last attempt, as of now. Always
// false when the interval is 0 (auto-sync disabled).
func syncDue(integration database.ProxyIntegration, now time.Time) bool {
	if integration.PriceSyncIntervalMinutes <= 0 {
		return false
	}
	lastAttempt := max(integration.PriceSyncedAt, integration.PriceSyncAttemptedAt)
	due := time.Unix(int64(lastAttempt), 0).
		Add(time.Duration(integration.PriceSyncIntervalMinutes) * time.Minute)
	return !now.Before(due)
}

// RunLoop syncs the active proxy integration's prices whenever its
// PriceSyncIntervalMinutes has elapsed since the later of its last success
// and last attempt. It checks at start, every pollInterval and whenever the
// registry reports an integration change, until ctx is done. Failures are
// logged.
func RunLoop(ctx context.Context, db *database.DB, est *pricing.Estimator, fetchers Fetchers, registry *upstream.Registry) {
	logger := slog.Default().With("component", "pricesync")

	checkAndSyncIfDue := func() {
		integration, err := db.GetActiveProxyIntegration(ctx)
		if err != nil {
			logger.Warn("read active proxy integration", "error", err)
			return
		}
		if integration == nil || !syncDue(*integration, time.Now()) {
			return
		}

		result, err := Sync(ctx, db, est, fetchers, *integration)
		if err != nil {
			logger.Warn("price sync failed", "integration", integration.Type, "error", err)
			return
		}
		logger.Info("price sync complete", "integration", integration.Type, "created", result.Created, "updated", result.Updated, "models", len(result.Models))
	}

	checkAndSyncIfDue()

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			checkAndSyncIfDue()
		case <-registry.Changed():
			checkAndSyncIfDue()
		}
	}
}
