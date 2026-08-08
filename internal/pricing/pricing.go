// Package pricing estimates the USD cost of a model API call from the
// admin-managed model_prices table.
//
// This is net-new relative to the old Python version, which hardcoded a
// price dict in pricing.py. Here prices live in the database (see
// internal/database) and are managed via the admin UI with no restart
// needed; Estimator keeps an in-memory, sorted cache so the hot path
// (every proxied response) never hits SQLite.
package pricing

import (
	"context"
	"math"
	"sort"
	"strings"
	"sync"

	"github.com/lfsc09/claude-lens/internal/database"
)

// Estimator matches a model name against the longest matching model_prices
// prefix and computes USD costs from token counts.
type Estimator struct {
	db *database.DB

	mu    sync.RWMutex
	cache []database.Price // sorted longest-prefix-first
}

// New creates an Estimator backed by db. Call Refresh before first use to
// populate the cache.
func New(db *database.DB) *Estimator {
	return &Estimator{db: db}
}

// Refresh reloads the price cache from the database. Call it after any
// admin CRUD on model_prices (UpsertPrice/DeletePrice) — the table is tiny
// and prices change rarely, so a synchronous reload is simpler than any
// pub/sub or TTL scheme.
func (e *Estimator) Refresh(ctx context.Context) error {
	prices, err := e.db.ListPrices(ctx)
	if err != nil {
		return err
	}

	// Longest prefix first, so the first HasPrefix match found while
	// scanning is always the most specific one — this also naturally
	// prefers an exact match (prefix == model) over any shorter prefix,
	// without needing a separate exact-match pass. Python's version
	// iterated a dict in insertion order and returned the first prefix
	// hit, which was ambiguous whenever two prefixes both matched (e.g.
	// "claude-sonnet-4" and "claude-sonnet" both prefix
	// "claude-sonnet-4-5"); sorting by length descending resolves that
	// ambiguity deliberately instead of by accident of insertion order.
	sort.Slice(prices, func(i, j int) bool {
		return len(prices[i].Prefix) > len(prices[j].Prefix)
	})

	e.mu.Lock()
	e.cache = prices
	e.mu.Unlock()
	return nil
}

// EstimateCosts returns the USD input/output cost for a token count against
// the given model, or ok == false if no price row's prefix matches.
func (e *Estimator) EstimateCosts(model string, inputTokens, outputTokens int) (inputCost, outputCost float64, ok bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	for _, p := range e.cache {
		if strings.HasPrefix(model, p.Prefix) {
			inputCost = round6(float64(inputTokens) * p.InputPerM / 1_000_000)
			outputCost = round6(float64(outputTokens) * p.OutputPerM / 1_000_000)
			return inputCost, outputCost, true
		}
	}
	return 0, 0, false
}

func round6(f float64) float64 {
	const mult = 1e6
	return math.Round(f*mult) / mult
}
