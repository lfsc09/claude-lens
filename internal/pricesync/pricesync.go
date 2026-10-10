// Package pricesync keeps claude-lens's model_prices table in sync with the
// active proxy integration's authoritative per-model rates, both on demand
// (the admin UI's manual sync button) and on the integration's own
// background schedule stored in the database, so changing it never needs a
// restart.
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

// ErrUpstreamUnavailable wraps a Sync failure that specifically means "this
// upstream can't serve prices" (the fetch itself failed) — as opposed to
// some other failure further down Sync's pipeline (e.g. a DB write error),
// which says nothing about whether the upstream is reachable. Callers use
// errors.Is against this to decide whether the failure is a genuine
// capability signal: the admin API maps it to 502 (see
// admin.syncProxyPrices), which the Prices page uses to grey out its manual
// sync button.
var ErrUpstreamUnavailable = errors.New("price sync upstream unavailable")

// Fetchers maps a proxy integration type to the client that reads model
// prices from that kind of gateway.
type Fetchers map[string]priceprovider.Fetcher

// Sync pulls per-model rates from integration's gateway via fetchers and
// upserts each model's price row into db — see database.UpsertPriceFromSync
// for why a manually configured above-200k override survives a sync that
// doesn't report that tier — then refreshes est so the new rates apply
// immediately and marks the sync's completion time on the integration, so
// RunLoop's staleness check doesn't immediately fire again right after.
// Returns an error, without changing any price, if the fetch itself fails —
// wrapped in ErrUpstreamUnavailable — and records that failure on the
// integration regardless of whether this call came from the admin UI's
// button or RunLoop's own schedule, so the admin UI can grey out the button
// the moment *any* attempt establishes the gateway can't serve prices.
func Sync(ctx context.Context, db *database.DB, est *pricing.Estimator, fetchers Fetchers, integration database.ProxyIntegration) (Result, error) {
	now := float64(time.Now().Unix())

	fetcher, ok := fetchers[integration.Type]
	if !ok {
		return Result{}, fmt.Errorf("no price fetcher for integration type %q", integration.Type)
	}

	prices, fetchErr := fetcher.FetchModelPrices(ctx, integration.BaseURL, integration.AuthToken)
	if fetchErr != nil {
		_ = db.MarkPriceSyncFailed(ctx, integration.ID, fetchErr.Error(), now)
		return Result{}, fmt.Errorf("%w: %v", ErrUpstreamUnavailable, fetchErr)
	}

	result := Result{Models: make([]string, 0, len(prices))}
	for _, p := range prices {
		created, err := db.UpsertPriceFromSync(ctx, p.ModelName, p.InputPerM, p.OutputPerM, p.CacheWritePerM, p.CacheReadPerM,
			p.InputPerMAbove200k, p.OutputPerMAbove200k, p.CacheWritePerMAbove200k, p.CacheReadPerMAbove200k, now)
		if err != nil {
			return Result{}, fmt.Errorf("upsert %s: %w", p.ModelName, err)
		}
		if created {
			result.Created++
		} else {
			result.Updated++
		}
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

// pollInterval is how often RunLoop checks whether a sync is due. It is not
// itself the sync schedule — see database.ProxyIntegration.PriceSyncIntervalMinutes
// for that — just the granularity at which a change to that setting takes
// effect when no integration change wakes the loop sooner.
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
// and last attempt — including immediately when it was just activated, or on
// a restart after the interval already lapsed. Using the last attempt rather
// than just the last success means a run of failures still retries once per
// interval, not on every poll. The check runs every pollInterval and
// whenever the registry reports an integration change, so activating an
// integration or editing its interval takes effect promptly, no restart
// needed. With no active integration there is nothing to sync. The admin
// UI's manual sync button is wired separately and unaffected either way.
//
// Failures are logged, not propagated: a gateway that can't serve prices
// fails every due check by design and that must never take the process down.
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
