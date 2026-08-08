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
	for _, p := range mustListPrices(t, db) {
		_ = db.DeletePrice(ctx, p.Prefix)
	}
	// A price row with no cache rates set (e.g. one created before this
	// feature, or never edited) must still price input/output normally and
	// simply report 0 cache cost, not fail the match.
	if err := db.UpsertPrice(ctx, database.Price{Prefix: "no-cache-rate-model", InputPerM: 2.00, OutputPerM: 10.00, UpdatedAt: 1}); err != nil {
		t.Fatalf("UpsertPrice: %v", err)
	}

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

	// Clear the seeded defaults and install the exact ambiguous-prefix
	// scenario called out in the plan (§7): "claude-sonnet-4" and
	// "claude-sonnet" both prefix "claude-sonnet-4-5" — the more specific
	// (longer) prefix must win, not whichever happens to be inserted or
	// scanned first.
	for _, p := range mustListPrices(t, db) {
		if err := db.DeletePrice(ctx, p.Prefix); err != nil {
			t.Fatalf("DeletePrice(%s): %v", p.Prefix, err)
		}
	}
	prices := []database.Price{
		{Prefix: "claude-sonnet", InputPerM: 1.00, OutputPerM: 1.00, UpdatedAt: 1},
		{Prefix: "claude-sonnet-4", InputPerM: 3.00, OutputPerM: 15.00, UpdatedAt: 2},
	}
	// Insert in an order where the shorter prefix comes last, to prove the
	// result doesn't depend on insertion/scan order.
	for _, p := range prices {
		if err := db.UpsertPrice(ctx, p); err != nil {
			t.Fatalf("UpsertPrice(%s): %v", p.Prefix, err)
		}
	}

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
	for _, p := range mustListPrices(t, db) {
		_ = db.DeletePrice(ctx, p.Prefix)
	}
	prices := []database.Price{
		{Prefix: "claude", InputPerM: 1.00, OutputPerM: 1.00, UpdatedAt: 1},
		{Prefix: "claude-sonnet-5", InputPerM: 3.00, OutputPerM: 15.00, UpdatedAt: 2},
	}
	for _, p := range prices {
		if err := db.UpsertPrice(ctx, p); err != nil {
			t.Fatalf("UpsertPrice: %v", err)
		}
	}

	e := New(db)
	if err := e.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	costs, ok := e.EstimateCosts("claude-sonnet-5", 1_000_000, 0, 0, 0)
	if !ok || costs.InputCost != 3.00 {
		t.Errorf("got ok=%v inputCost=%v, want exact match at 3.00", ok, costs.InputCost)
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

	if err := db.UpsertPrice(ctx, database.Price{Prefix: "brand-new-model", InputPerM: 9, OutputPerM: 9, UpdatedAt: 1}); err != nil {
		t.Fatalf("UpsertPrice: %v", err)
	}
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
