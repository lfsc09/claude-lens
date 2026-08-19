package pricing

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/lfsc09/claude-lens/internal/database"
)

func openTestDB(t *testing.T) *database.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := database.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("database.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// clearPrices deletes every seeded/default rule so a test can install its
// own exact scenario.
func clearPrices(t *testing.T, db *database.DB) {
	t.Helper()
	for _, p := range mustListPrices(t, db) {
		if err := db.DeletePrice(context.Background(), p.ID); err != nil {
			t.Fatalf("DeletePrice(%d): %v", p.ID, err)
		}
	}
}

// createPrice is a small test helper around CreatePrice that fills in
// Rule/RuleTokens defaults ("over", 0 — i.e. an unconditional catch-all)
// when the caller doesn't care about tiering.
func createPrice(t *testing.T, db *database.DB, p database.Price) int64 {
	t.Helper()
	if p.Rule == "" {
		p.Rule = "over"
	}
	id, err := db.CreatePrice(context.Background(), p)
	if err != nil {
		t.Fatalf("CreatePrice(%s): %v", p.Prefix, err)
	}
	return id
}

func TestEstimateCosts_UsesDefaultSeededPrices(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	e := New(db)
	if err := e.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	costs, ok := e.EstimateCosts("claude-sonnet-5-20260101", 1_000_000, 1_000_000, 0, 0)
	if !ok {
		t.Fatal("expected a match against the seeded claude-sonnet-5 price")
	}
	if costs.InputCost != 3.00 || costs.OutputCost != 15.00 {
		t.Errorf("got (%v, %v), want (3.00, 15.00)", costs.InputCost, costs.OutputCost)
	}
}

func TestEstimateCosts_ComputesCacheCosts(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	e := New(db)
	if err := e.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	// claude-sonnet-5 is seeded with CacheWritePerM=3.75, CacheReadPerM=0.30.
	costs, ok := e.EstimateCosts("claude-sonnet-5-20260101", 0, 0, 1_000_000, 1_000_000)
	if !ok {
		t.Fatal("expected a match against the seeded claude-sonnet-5 price")
	}
	if costs.CacheCreationCost != 3.75 || costs.CacheReadCost != 0.30 {
		t.Errorf("got (cacheCreation=%v, cacheRead=%v), want (3.75, 0.30)", costs.CacheCreationCost, costs.CacheReadCost)
	}
}

func TestEstimateCosts_ZeroCacheRateDoesNotBreakInputOutput(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	clearPrices(t, db)
	// A price row with no cache rates set (e.g. one created before this
	// feature, or never edited) must still price input/output normally and
	// simply report 0 cache cost, not fail the match.
	createPrice(t, db, database.Price{Prefix: "no-cache-rate-model", RuleTokens: 0, InputPerM: 2.00, OutputPerM: 10.00, CreatedAt: 1, UpdatedAt: 1})

	e := New(db)
	if err := e.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	costs, ok := e.EstimateCosts("no-cache-rate-model", 1_000_000, 1_000_000, 1_000_000, 1_000_000)
	if !ok {
		t.Fatal("expected a match")
	}
	if costs.InputCost != 2.00 || costs.OutputCost != 10.00 {
		t.Errorf("got (input=%v, output=%v), want (2.00, 10.00)", costs.InputCost, costs.OutputCost)
	}
	if costs.CacheCreationCost != 0 || costs.CacheReadCost != 0 {
		t.Errorf("got (cacheCreation=%v, cacheRead=%v), want (0, 0) for an unpriced cache rate", costs.CacheCreationCost, costs.CacheReadCost)
	}
}

func TestEstimateCosts_UnknownModel(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	e := New(db)
	if err := e.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	_, ok := e.EstimateCosts("some-unrelated-model", 100, 100, 0, 0)
	if ok {
		t.Fatal("expected no match for an unrelated model name")
	}
}

func TestEstimateCosts_LongestPrefixWins(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	// "claude-sonnet-4" and "claude-sonnet" both prefix
	// "claude-sonnet-4-5" — the more specific (longer) prefix must win, not
	// whichever happens to be inserted or scanned first.
	clearPrices(t, db)
	// Insert in an order where the shorter prefix comes last, to prove the
	// result doesn't depend on insertion/scan order.
	createPrice(t, db, database.Price{Prefix: "claude-sonnet", RuleTokens: 0, InputPerM: 1.00, OutputPerM: 1.00, CreatedAt: 1, UpdatedAt: 1})
	createPrice(t, db, database.Price{Prefix: "claude-sonnet-4", RuleTokens: 0, InputPerM: 3.00, OutputPerM: 15.00, CreatedAt: 2, UpdatedAt: 2})

	e := New(db)
	if err := e.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	costs, ok := e.EstimateCosts("claude-sonnet-4-5", 1_000_000, 1_000_000, 0, 0)
	if !ok {
		t.Fatal("expected a match")
	}
	if costs.InputCost != 3.00 || costs.OutputCost != 15.00 {
		t.Errorf("got (%v, %v), want the claude-sonnet-4 price (3.00, 15.00), not the shorter claude-sonnet prefix", costs.InputCost, costs.OutputCost)
	}
}

func TestEstimateCosts_ExactMatchWinsOverShorterPrefix(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	clearPrices(t, db)
	createPrice(t, db, database.Price{Prefix: "claude", RuleTokens: 0, InputPerM: 1.00, OutputPerM: 1.00, CreatedAt: 1, UpdatedAt: 1})
	createPrice(t, db, database.Price{Prefix: "claude-sonnet-5", RuleTokens: 0, InputPerM: 3.00, OutputPerM: 15.00, CreatedAt: 2, UpdatedAt: 2})

	e := New(db)
	if err := e.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	costs, ok := e.EstimateCosts("claude-sonnet-5", 1_000_000, 0, 0, 0)
	if !ok || costs.InputCost != 3.00 {
		t.Errorf("got ok=%v inputCost=%v, want exact match at 3.00", ok, costs.InputCost)
	}
}

func TestEstimateCosts_ClosestRuleTokensWins(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	clearPrices(t, db)
	// "over 200" and "under 1000" overlap on (200, 1000]. The rule whose
	// threshold is numerically closest to promptTokens should win — not
	// whichever was created most recently.
	createPrice(t, db, database.Price{Prefix: "tiered-model", Rule: "over", RuleTokens: 200, InputPerM: 1.00, OutputPerM: 1.00, CreatedAt: 1, UpdatedAt: 1})
	createPrice(t, db, database.Price{Prefix: "tiered-model", Rule: "under", RuleTokens: 1000, InputPerM: 9.00, OutputPerM: 9.00, CreatedAt: 2, UpdatedAt: 2})

	e := New(db)
	if err := e.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	// promptTokens=201: distance to 200 is 1, to 1000 is 799 — "over 200" wins.
	if costs, ok := e.EstimateCosts("tiered-model", 201, 0, 0, 0); !ok || costs.InputCost != 0.000201 {
		t.Errorf("promptTokens=201: got ok=%v inputCost=%v, want the over-200 rate", ok, costs.InputCost)
	}
	// promptTokens=999: distance to 200 is 799, to 1000 is 1 — "under 1000" wins.
	if costs, ok := e.EstimateCosts("tiered-model", 999, 0, 0, 0); !ok || costs.InputCost != 0.008991 {
		t.Errorf("promptTokens=999: got ok=%v inputCost=%v, want the under-1000 rate", ok, costs.InputCost)
	}
}

func TestEstimateCostsAndRule_ReturnsTheMatchedRule(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	clearPrices(t, db)
	createPrice(t, db, database.Price{Prefix: "tiered-model", Rule: "over", RuleTokens: 200, InputPerM: 1.00, OutputPerM: 1.00, CreatedAt: 1, UpdatedAt: 1})
	createPrice(t, db, database.Price{Prefix: "tiered-model", Rule: "under", RuleTokens: 1000, InputPerM: 9.00, OutputPerM: 9.00, CreatedAt: 2, UpdatedAt: 2})

	e := New(db)
	if err := e.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	costs, rule, ok := e.EstimateCostsAndRule("tiered-model", 201, 0, 0, 0)
	if !ok || costs.InputCost != 0.000201 {
		t.Errorf("got ok=%v inputCost=%v, want the over-200 rate", ok, costs.InputCost)
	}
	if rule.Rule != "over" || rule.RuleTokens != 200 {
		t.Errorf("rule = %+v, want the over-200 rule", rule)
	}

	if _, _, ok := e.EstimateCostsAndRule("unknown-model", 100, 0, 0, 0); ok {
		t.Fatal("expected no match for an unknown model")
	}
}

func TestEstimateCosts_ExactDistanceTieBreaksByRecency(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	clearPrices(t, db)
	// A literal duplicate rule (same rule + rule_tokens): distance is
	// identical for both, so the more recently created one must win.
	createPrice(t, db, database.Price{Prefix: "dup-model", Rule: "over", RuleTokens: 0, InputPerM: 1.00, OutputPerM: 1.00, CreatedAt: 1, UpdatedAt: 1})
	createPrice(t, db, database.Price{Prefix: "dup-model", Rule: "over", RuleTokens: 0, InputPerM: 5.00, OutputPerM: 5.00, CreatedAt: 2, UpdatedAt: 2})

	e := New(db)
	if err := e.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	costs, ok := e.EstimateCosts("dup-model", 1_000_000, 0, 0, 0)
	if !ok || costs.InputCost != 5.00 {
		t.Errorf("got ok=%v inputCost=%v, want the more recently created duplicate (5.00)", ok, costs.InputCost)
	}
}

func TestEstimateCosts_NoRuleMatchesPromptSize(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	clearPrices(t, db)
	// Only a narrow "under 1000" tier exists for this prefix — a call far
	// outside that range must report no match, not silently fall back to a
	// shorter prefix or an unrelated rule.
	createPrice(t, db, database.Price{Prefix: "narrow-model", Rule: "under", RuleTokens: 1000, InputPerM: 1.00, OutputPerM: 1.00, CreatedAt: 1, UpdatedAt: 1})

	e := New(db)
	if err := e.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	if _, ok := e.EstimateCosts("narrow-model", 5000, 0, 0, 0); ok {
		t.Fatal("expected no match: promptTokens=5000 exceeds the only rule's under-1000 range")
	}
}

func TestRefresh_PicksUpChangesWithoutRestart(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	e := New(db)
	if err := e.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	if _, ok := e.EstimateCosts("brand-new-model", 100, 100, 0, 0); ok {
		t.Fatal("expected no match before the price exists")
	}

	createPrice(t, db, database.Price{Prefix: "brand-new-model", RuleTokens: 0, InputPerM: 9, OutputPerM: 9, CreatedAt: 1, UpdatedAt: 1})
	if err := e.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	costs, ok := e.EstimateCosts("brand-new-model", 1_000_000, 0, 0, 0)
	if !ok || costs.InputCost != 9.00 {
		t.Errorf("got ok=%v inputCost=%v, want ok=true inputCost=9.00 after Refresh", ok, costs.InputCost)
	}
}

func mustListPrices(t *testing.T, db *database.DB) []database.Price {
	t.Helper()
	prices, err := db.ListPrices(context.Background())
	if err != nil {
		t.Fatalf("ListPrices: %v", err)
	}
	return prices
}
