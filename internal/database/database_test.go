package database

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func openTestDB(t *testing.T) *DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func floatPtr(f float64) *float64 { return &f }
func intPtr(i int) *int           { return &i }
func strPtr(s string) *string     { return &s }

func TestOpen_SeedsDefaultPrices(t *testing.T) {
	db := openTestDB(t)
	prices, err := db.ListPrices(context.Background())
	if err != nil {
		t.Fatalf("ListPrices: %v", err)
	}
	if len(prices) != len(defaultPrices) {
		t.Fatalf("got %d seeded prices, want %d", len(prices), len(defaultPrices))
	}

	// Re-opening must not duplicate or reset seeded/edited rows.
	if err := db.UpsertPrice(context.Background(), Price{Prefix: "claude-opus-5", InputPerM: 99, OutputPerM: 99, UpdatedAt: 1}); err != nil {
		t.Fatalf("UpsertPrice: %v", err)
	}
	if err := db.seedDefaultPrices(context.Background()); err != nil {
		t.Fatalf("seedDefaultPrices (idempotency check): %v", err)
	}
	prices, err = db.ListPrices(context.Background())
	if err != nil {
		t.Fatalf("ListPrices: %v", err)
	}
	for _, p := range prices {
		if p.Prefix == "claude-opus-5" && p.InputPerM != 99 {
			t.Fatalf("seedDefaultPrices overwrote an existing row: %+v", p)
		}
	}
}

func TestPricesCRUD(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	if err := db.UpsertPrice(ctx, Price{Prefix: "custom-model", InputPerM: 2.5, OutputPerM: 10, UpdatedAt: 123}); err != nil {
		t.Fatalf("UpsertPrice insert: %v", err)
	}
	if err := db.UpsertPrice(ctx, Price{Prefix: "custom-model", InputPerM: 3.5, OutputPerM: 11, UpdatedAt: 124}); err != nil {
		t.Fatalf("UpsertPrice update: %v", err)
	}

	prices, err := db.ListPrices(ctx)
	if err != nil {
		t.Fatalf("ListPrices: %v", err)
	}
	found := false
	for _, p := range prices {
		if p.Prefix == "custom-model" {
			found = true
			if p.InputPerM != 3.5 || p.OutputPerM != 11 {
				t.Errorf("upsert did not update in place: %+v", p)
			}
		}
	}
	if !found {
		t.Fatal("custom-model not found after upsert")
	}

	if err := db.DeletePrice(ctx, "custom-model"); err != nil {
		t.Fatalf("DeletePrice: %v", err)
	}
	if err := db.DeletePrice(ctx, "does-not-exist"); err != nil {
		t.Fatalf("DeletePrice on missing prefix should not error: %v", err)
	}
	prices, err = db.ListPrices(ctx)
	if err != nil {
		t.Fatalf("ListPrices: %v", err)
	}
	for _, p := range prices {
		if p.Prefix == "custom-model" {
			t.Fatal("custom-model still present after delete")
		}
	}
}

func TestSaveAndGetExchange(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := float64(time.Now().Unix())

	e := Exchange{
		SessionID:     "sess-1",
		SessionName:   strPtr("my session"),
		Path:          "/v1/messages",
		Timestamp:     now,
		IsStreaming:   true,
		InputMessages: strPtr(`[{"role":"user","content":"hi"}]`),
		RawRequest:    `{"model":"claude-sonnet-5"}`,
		RawResponse:   `{"type":"message"}`,
		OutputText:    strPtr("hello there"),
		InputTokens:   intPtr(100),
		OutputTokens:  intPtr(50),
		Model:         strPtr("claude-sonnet-5"),
		InputCost:     floatPtr(0.0003),
		OutputCost:    floatPtr(0.00075),
	}
	if err := db.SaveExchange(ctx, e); err != nil {
		t.Fatalf("SaveExchange: %v", err)
	}

	list, err := db.GetExchanges(ctx, "", 10, 0)
	if err != nil {
		t.Fatalf("GetExchanges: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("got %d exchanges, want 1", len(list))
	}
	got := list[0]
	if got.SessionID != "sess-1" || got.Model == nil || *got.Model != "claude-sonnet-5" {
		t.Errorf("unexpected summary row: %+v", got)
	}
	wantCost := 0.00105
	if got.Cost == nil || round4(*got.Cost) != round4(wantCost) {
		t.Errorf("cost = %v, want ~%v", got.Cost, wantCost)
	}

	detail, err := db.GetExchangeDetail(ctx, got.ID)
	if err != nil {
		t.Fatalf("GetExchangeDetail: %v", err)
	}
	if detail == nil {
		t.Fatal("GetExchangeDetail returned nil for existing id")
	}
	if detail.RawRequest == nil || *detail.RawRequest != e.RawRequest {
		t.Errorf("RawRequest mismatch: %+v", detail.RawRequest)
	}
	if detail.OutputText == nil || *detail.OutputText != "hello there" {
		t.Errorf("OutputText mismatch: %+v", detail.OutputText)
	}

	missing, err := db.GetExchangeDetail(ctx, got.ID+999)
	if err != nil {
		t.Fatalf("GetExchangeDetail(missing): %v", err)
	}
	if missing != nil {
		t.Fatalf("GetExchangeDetail(missing) = %+v, want nil", missing)
	}
}

func TestGetExchanges_SessionFilterAndPagination(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	base := float64(time.Now().Unix())

	for i, sess := range []string{"a", "a", "b", "a"} {
		e := Exchange{
			SessionID:   sess,
			Path:        "/v1/messages",
			Timestamp:   base + float64(i),
			RawRequest:  "{}",
			RawResponse: "{}",
		}
		if err := db.SaveExchange(ctx, e); err != nil {
			t.Fatalf("SaveExchange[%d]: %v", i, err)
		}
	}

	all, err := db.GetExchanges(ctx, "", 100, 0)
	if err != nil {
		t.Fatalf("GetExchanges(all): %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("got %d, want 4", len(all))
	}
	// newest first
	if all[0].Timestamp < all[len(all)-1].Timestamp {
		t.Errorf("expected newest-first ordering, got %+v", all)
	}

	sessA, err := db.GetExchanges(ctx, "a", 100, 0)
	if err != nil {
		t.Fatalf("GetExchanges(a): %v", err)
	}
	if len(sessA) != 3 {
		t.Fatalf("got %d for session a, want 3", len(sessA))
	}

	page1, err := db.GetExchanges(ctx, "", 2, 0)
	if err != nil {
		t.Fatalf("GetExchanges(page1): %v", err)
	}
	page2, err := db.GetExchanges(ctx, "", 2, 2)
	if err != nil {
		t.Fatalf("GetExchanges(page2): %v", err)
	}
	if len(page1) != 2 || len(page2) != 2 {
		t.Fatalf("pagination sizes = %d, %d, want 2, 2", len(page1), len(page2))
	}
	if page1[0].ID == page2[0].ID {
		t.Errorf("page1 and page2 overlap: %+v vs %+v", page1, page2)
	}
}

func TestGetTokenTotals(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := float64(time.Now().Unix())

	rows := []Exchange{
		{SessionID: "a", Path: "/p", Timestamp: now, RawRequest: "{}", RawResponse: "{}",
			InputTokens: intPtr(10), OutputTokens: intPtr(20), InputCost: floatPtr(0.01), OutputCost: floatPtr(0.02)},
		{SessionID: "a", Path: "/p", Timestamp: now + 1, RawRequest: "{}", RawResponse: "{}",
			InputTokens: intPtr(5), OutputTokens: intPtr(15), InputCost: floatPtr(0.005), OutputCost: floatPtr(0.015)},
		{SessionID: "b", Path: "/p", Timestamp: now + 2, RawRequest: "{}", RawResponse: "{}",
			InputTokens: intPtr(1), OutputTokens: intPtr(1)},
	}
	for _, r := range rows {
		if err := db.SaveExchange(ctx, r); err != nil {
			t.Fatalf("SaveExchange: %v", err)
		}
	}

	totals, err := db.GetTokenTotals(ctx, "", nil)
	if err != nil {
		t.Fatalf("GetTokenTotals(all): %v", err)
	}
	if totals.Count != 3 || totals.TotalInputTokens != 16 || totals.TotalOutputTokens != 36 {
		t.Errorf("unexpected totals: %+v", totals)
	}
	if totals.TotalCost == nil || round4(*totals.TotalCost) != round4(0.05) {
		t.Errorf("TotalCost = %v, want ~0.05 (row without cost contributes NULL, not 0)", derefFloat(totals.TotalCost))
	}

	sessA, err := db.GetTokenTotals(ctx, "a", nil)
	if err != nil {
		t.Fatalf("GetTokenTotals(a): %v", err)
	}
	if sessA.Count != 2 || sessA.TotalInputTokens != 15 {
		t.Errorf("unexpected session-scoped totals: %+v", sessA)
	}

	since := now + 1
	recent, err := db.GetTokenTotals(ctx, "", &since)
	if err != nil {
		t.Fatalf("GetTokenTotals(since): %v", err)
	}
	if recent.Count != 2 {
		t.Errorf("since-filtered count = %d, want 2", recent.Count)
	}
}

func TestGetSessionStats(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := float64(time.Now().Unix())

	_ = db.SaveExchange(ctx, Exchange{SessionID: "old", SessionName: strPtr("Old"), Path: "/p", Timestamp: now, RawRequest: "{}", RawResponse: "{}"})
	_ = db.SaveExchange(ctx, Exchange{SessionID: "new", SessionName: strPtr("New"), Path: "/p", Timestamp: now + 10, RawRequest: "{}", RawResponse: "{}"})

	stats, err := db.GetSessionStats(ctx)
	if err != nil {
		t.Fatalf("GetSessionStats: %v", err)
	}
	if len(stats) != 2 {
		t.Fatalf("got %d session stats, want 2", len(stats))
	}
	if stats[0].SessionID != "new" {
		t.Errorf("expected most-recently-active session first, got %+v", stats[0])
	}
}

func TestGetDailyCosts(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := float64(time.Now().Unix())

	if err := db.SaveExchange(ctx, Exchange{
		SessionID: "a", Path: "/p", Timestamp: now, RawRequest: "{}", RawResponse: "{}",
		InputCost: floatPtr(0.01), OutputCost: floatPtr(0.02),
	}); err != nil {
		t.Fatalf("SaveExchange: %v", err)
	}

	daily, err := db.GetDailyCosts(ctx, 60)
	if err != nil {
		t.Fatalf("GetDailyCosts: %v", err)
	}
	if len(daily) != 1 {
		t.Fatalf("got %d daily buckets, want 1: %+v", len(daily), daily)
	}
	if round4(daily[0].DailyCost) != round4(0.03) {
		t.Errorf("DailyCost = %v, want ~0.03", daily[0].DailyCost)
	}

	// A tight window that excludes the row entirely.
	oldRow := now - float64(120*24*3600)
	if err := db.SaveExchange(ctx, Exchange{
		SessionID: "old", Path: "/p", Timestamp: oldRow, RawRequest: "{}", RawResponse: "{}",
		InputCost: floatPtr(1), OutputCost: floatPtr(1),
	}); err != nil {
		t.Fatalf("SaveExchange(old): %v", err)
	}
	daily, err = db.GetDailyCosts(ctx, 60)
	if err != nil {
		t.Fatalf("GetDailyCosts: %v", err)
	}
	if len(daily) != 1 {
		t.Fatalf("120-day-old row leaked into a 60-day window: %+v", daily)
	}
}

func TestDeleteExchanges(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := float64(time.Now().Unix())

	for _, sess := range []string{"a", "a", "b"} {
		if err := db.SaveExchange(ctx, Exchange{SessionID: sess, Path: "/p", Timestamp: now, RawRequest: "{}", RawResponse: "{}"}); err != nil {
			t.Fatalf("SaveExchange: %v", err)
		}
	}

	n, err := db.DeleteExchanges(ctx, "a")
	if err != nil {
		t.Fatalf("DeleteExchanges(a): %v", err)
	}
	if n != 2 {
		t.Fatalf("deleted %d rows, want 2", n)
	}
	remaining, err := db.GetExchanges(ctx, "", 100, 0)
	if err != nil {
		t.Fatalf("GetExchanges: %v", err)
	}
	if len(remaining) != 1 || remaining[0].SessionID != "b" {
		t.Fatalf("unexpected remaining rows: %+v", remaining)
	}

	n, err = db.DeleteExchanges(ctx, "")
	if err != nil {
		t.Fatalf("DeleteExchanges(all): %v", err)
	}
	if n != 1 {
		t.Fatalf("deleted %d rows, want 1", n)
	}
}

// TestConcurrentWrites proves the §4 gotcha is actually handled: many
// goroutines writing at once through the shared connection pool must not
// produce SQLITE_BUSY, since SetMaxOpenConns(1) serializes them.
func TestConcurrentWrites(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := float64(time.Now().Unix())

	const n = 50
	var wg sync.WaitGroup
	errCh := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			err := db.SaveExchange(ctx, Exchange{
				SessionID: "concurrent", Path: "/p", Timestamp: now + float64(i),
				RawRequest: "{}", RawResponse: "{}",
			})
			errCh <- err
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Errorf("concurrent SaveExchange failed: %v", err)
		}
	}

	rows, err := db.GetExchanges(ctx, "concurrent", 100, 0)
	if err != nil {
		t.Fatalf("GetExchanges: %v", err)
	}
	if len(rows) != n {
		t.Fatalf("got %d rows after concurrent writes, want %d", len(rows), n)
	}
}

func round4(f float64) float64 {
	return float64(int(f*10000+0.5)) / 10000
}
