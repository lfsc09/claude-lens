// Package pricing estimates the USD cost of a model API call from the
// admin-managed model_prices table.
//
// Prices live in the database (see internal/database) and are managed via
// the admin UI with no restart needed; Estimator keeps an in-memory, sorted
// cache so the hot path (every proxied response) never hits SQLite.
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
// prefix, then picks among that prefix's rule rows to compute USD costs
// from token counts.
type Estimator struct {
	db *database.DB

	mu                   sync.RWMutex
	prefixesLongestFirst []string
	rulesByPrefix        map[string][]database.Price
}

// New creates an Estimator backed by db. Call Refresh before first use to
// populate the cache.
func New(db *database.DB) *Estimator {
	return &Estimator{db: db}
}

// Refresh reloads the price cache from the database. Call it after any
// admin CRUD on model_prices (CreatePrice/UpdatePrice/DeletePrice) — the
// table is tiny and prices change rarely, so a synchronous reload is
// simpler than any pub/sub or TTL scheme.
func (e *Estimator) Refresh(ctx context.Context) error {
	prices, err := e.db.ListPrices(ctx)
	if err != nil {
		return err
	}

	rulesByPrefix := make(map[string][]database.Price)
	for _, p := range prices {
		rulesByPrefix[p.Prefix] = append(rulesByPrefix[p.Prefix], p)
	}

	// Longest prefix first, so the first HasPrefix match found while
	// scanning is always the most specific one — this also naturally
	// prefers an exact match (prefix == model) over any shorter prefix,
	// without needing a separate exact-match pass. Sorting by length
	// descending resolves prefix ambiguity deliberately (e.g.
	// "claude-sonnet-4" and "claude-sonnet" both prefix
	// "claude-sonnet-4-5") instead of leaving it to accident of insertion
	// order.
	prefixes := make([]string, 0, len(rulesByPrefix))
	for prefix := range rulesByPrefix {
		prefixes = append(prefixes, prefix)
	}
	sort.Slice(prefixes, func(i, j int) bool {
		return len(prefixes[i]) > len(prefixes[j])
	})

	e.mu.Lock()
	e.prefixesLongestFirst = prefixes
	e.rulesByPrefix = rulesByPrefix
	e.mu.Unlock()
	return nil
}

// Costs is the USD breakdown of a single model call, split by token type —
// plain input/output plus Anthropic's prompt-caching tokens (cache creation
// is priced above input, cache read well below it).
type Costs struct {
	InputCost         float64
	OutputCost        float64
	CacheCreationCost float64
	CacheReadCost     float64
}

// EstimateCosts returns the USD cost breakdown for a call's token counts
// against the given model, or ok == false if no price rule matches.
func (e *Estimator) EstimateCosts(model string, inputTokens, outputTokens, cacheCreationTokens, cacheReadTokens int) (Costs, bool) {
	costs, _, ok := e.EstimateCostsAndRule(model, inputTokens, outputTokens, cacheCreationTokens, cacheReadTokens)
	return costs, ok
}

// EstimateCostsAndRule returns both the USD cost breakdown and the price
// rule that produced it, or ok == false if no price rule matches.
//
// A prefix can own several rule rows, each gated by "over N" (exclusive) or
// "under N" (inclusive) against the call's prompt size — input +
// cache-creation + cache-read tokens, not a cross-call session sum, since
// the client resends the full conversation every turn and a single
// request's prompt size already reflects how far the session has grown.
// When more than one rule matches, the one whose RuleTokens is numerically
// closest to the prompt size wins: the same "most specific match wins" idea
// already used for prefix matching (longest prefix wins there; closest
// threshold wins here). An exact distance tie — only possible with a
// literal duplicate rule — falls back to whichever was created most
// recently.
//
// The admin UI's shadow-indicator sweep (prices.js) mirrors this picker so
// the "which rule applies where" visualization stays accurate; keep the two
// in sync if this resolution logic changes.
func (e *Estimator) EstimateCostsAndRule(model string, inputTokens, outputTokens, cacheCreationTokens, cacheReadTokens int) (Costs, database.Price, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	promptTokens := inputTokens + cacheCreationTokens + cacheReadTokens
	best, ok := e.matchRuleLocked(model, promptTokens)
	if !ok {
		return Costs{}, database.Price{}, false
	}

	return Costs{
		InputCost:         round6(float64(inputTokens) * best.InputPerM / 1_000_000),
		OutputCost:        round6(float64(outputTokens) * best.OutputPerM / 1_000_000),
		CacheCreationCost: round6(float64(cacheCreationTokens) * best.CacheWritePerM / 1_000_000),
		CacheReadCost:     round6(float64(cacheReadTokens) * best.CacheReadPerM / 1_000_000),
	}, best, true
}

// matchRuleLocked implements the picker described on EstimateCosts; callers
// must hold e.mu.
func (e *Estimator) matchRuleLocked(model string, promptTokens int) (database.Price, bool) {
	for _, prefix := range e.prefixesLongestFirst {
		if !strings.HasPrefix(model, prefix) {
			continue
		}

		rules := e.rulesByPrefix[prefix]
		var best *database.Price
		var bestDist int64
		for i, p := range rules {
			if !ruleMatches(p.Rule, p.RuleTokens, promptTokens) {
				continue
			}
			dist := abs64(p.RuleTokens - int64(promptTokens))
			if best == nil || dist < bestDist || (dist == bestDist && p.CreatedAt > best.CreatedAt) {
				best = &rules[i]
				bestDist = dist
			}
		}
		if best == nil {
			return database.Price{}, false
		}
		return *best, true
	}
	return database.Price{}, false
}

// ruleMatches reports whether promptTokens falls under a rule: "under N" is
// inclusive of N, "over N" is exclusive of N.
func ruleMatches(rule string, ruleTokens int64, promptTokens int) bool {
	if rule == "under" {
		return int64(promptTokens) <= ruleTokens
	}
	return int64(promptTokens) > ruleTokens
}

func abs64(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}

func round6(f float64) float64 {
	const mult = 1e6
	return math.Round(f*mult) / mult
}
