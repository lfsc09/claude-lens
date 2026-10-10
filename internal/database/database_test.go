package database

import (
	"context"
	"path/filepath"
	"reflect"
	"sort"
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
	var opusID int64
	for _, p := range prices {
		if p.Prefix == "claude-opus-5" {
			opusID = p.ID
		}
	}
	if err := db.UpdatePrice(context.Background(), opusID, Price{InputPerM: 99, OutputPerM: 99, UpdatedAt: 1}); err != nil {
		t.Fatalf("UpdatePrice: %v", err)
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

	id, err := db.CreatePrice(ctx, Price{Prefix: "custom-model", InputPerM: 2.5, OutputPerM: 10, CreatedAt: 123, UpdatedAt: 123})
	if err != nil {
		t.Fatalf("CreatePrice: %v", err)
	}
	if err := db.UpdatePrice(ctx, id, Price{InputPerM: 3.5, OutputPerM: 11, UpdatedAt: 124}); err != nil {
		t.Fatalf("UpdatePrice: %v", err)
	}

	prices, err := db.ListPrices(ctx)
	if err != nil {
		t.Fatalf("ListPrices: %v", err)
	}
	found := false
	for _, p := range prices {
		if p.ID == id {
			found = true
			if p.InputPerM != 3.5 || p.OutputPerM != 11 {
				t.Errorf("update did not apply in place: %+v", p)
			}
		}
	}
	if !found {
		t.Fatal("custom-model not found after create")
	}

	if err := db.DeletePrice(ctx, id); err != nil {
		t.Fatalf("DeletePrice: %v", err)
	}
	if err := db.DeletePrice(ctx, 9999999); err != nil {
		t.Fatalf("DeletePrice on missing id should not error: %v", err)
	}
	prices, err = db.ListPrices(ctx)
	if err != nil {
		t.Fatalf("ListPrices: %v", err)
	}
	for _, p := range prices {
		if p.ID == id {
			t.Fatal("custom-model still present after delete")
		}
	}
}

func TestSaveAndGetExchange(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := float64(time.Now().Unix())

	matchedPrice := `{"id":1,"model_prefix":"claude-sonnet-5","rule":"over","rule_tokens":0,"input_per_m":3,"output_per_m":15}`
	e := Exchange{
		SessionID:    "sess-1",
		Path:         "/v1/messages",
		CreatedAt:    now,
		IsStreaming:  false,
		RawRequest:   `{"model":"claude-sonnet-5"}`,
		RawResponse:  `{"type":"message","content":[{"type":"text","text":"hello there"}]}`,
		InputTokens:  intPtr(100),
		OutputTokens: intPtr(50),
		Model:        strPtr("claude-sonnet-5"),
		InputCost:    floatPtr(0.0003),
		OutputCost:   floatPtr(0.00075),
		MatchedPrice: &matchedPrice,
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
	if detail.RawRequest != e.RawRequest || detail.RawResponse != e.RawResponse {
		t.Errorf("raw payload mismatch: %+v", detail)
	}
	if detail.SessionName != "sess-1" {
		t.Errorf("SessionName = %q, want it to default to the session id", detail.SessionName)
	}
	if detail.OutputText == nil || *detail.OutputText != "hello there" {
		t.Errorf("OutputText mismatch: %+v", detail.OutputText)
	}
	if string(detail.MatchedPrice) != matchedPrice {
		t.Errorf("MatchedPrice = %s, want %s", detail.MatchedPrice, matchedPrice)
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
			CreatedAt:   base + float64(i),
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
	if all[0].CreatedAt < all[len(all)-1].CreatedAt {
		t.Errorf("expected newest-first ordering, got %+v", all)
	}

	sessA, err := db.GetExchanges(ctx, `session = "a"`, 100, 0)
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

// TestGetExchanges_FilterQuery exercises the search-box query language (see
// query_filter.go) end-to-end against a real DB: text =/like, numeric/bool
// comparisons, computed fields (cache_tokens), AND/OR precedence, and
// parens overriding that precedence.
func TestGetExchanges_FilterQuery(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	base := float64(time.Now().Unix())

	seed := []Exchange{
		{ // row 1
			SessionID: "sess-a", Path: "/v1/messages", CreatedAt: base,
			RawRequest: "{}", RawResponse: "{}",
			Model:               strPtr("claude-sonnet-4-5"),
			InputTokens:         intPtr(100),
			OutputTokens:        intPtr(50),
			CacheCreationTokens: intPtr(10),
			CacheReadTokens:     intPtr(5),
			InputCost:           floatPtr(0.03),
			IsStreaming:         false,
		},
		{ // row 2
			SessionID: "sess-b", Path: "/v1/messages", CreatedAt: base + 100,
			RawRequest: "{}", RawResponse: "{}",
			Model:        strPtr("claude-haiku-4-5"),
			InputTokens:  intPtr(200),
			OutputTokens: intPtr(80),
			InputCost:    floatPtr(0.20),
			IsStreaming:  true,
		},
		{ // row 3: no usage captured at all (e.g. a parse failure)
			SessionID: "sess-a", Path: "/v1/complete", CreatedAt: base + 200,
			RawRequest: "{}", RawResponse: "{}",
			Model:       strPtr("claude-opus-4-5"),
			IsStreaming: false,
		},
	}
	for i, e := range seed {
		if err := db.SaveExchange(ctx, e); err != nil {
			t.Fatalf("SaveExchange[%d]: %v", i, err)
		}
	}

	tests := []struct {
		name      string
		query     string
		wantPaths []string // by .Path, since it's distinct per row above
	}{
		{"text like", `model like sonnet`, []string{"/v1/messages"}},
		{"text equals exact model", `model = "claude-haiku-4-5"`, []string{"/v1/messages"}},
		{"session matches two rows", `session = "sess-a"`, []string{"/v1/complete", "/v1/messages"}},
		{"path like across sessions", `path like messages`, []string{"/v1/messages", "/v1/messages"}},
		{"bool stream", `stream = true`, []string{"/v1/messages"}},
		{"number gt", `input_tokens > 150`, []string{"/v1/messages"}},
		{"cost gt", `cost > 0.1`, []string{"/v1/messages"}},
		{"and excludes non-matching cost", `model like sonnet AND cost > 0.1`, nil},
		{"or includes both", `model like sonnet OR cost > 0.1`, []string{"/v1/messages", "/v1/messages"}},
		{
			"parens override precedence",
			`(model like sonnet OR model like haiku) AND session = "sess-a"`,
			[]string{"/v1/messages"},
		},
		{"cache_tokens computed field", `cache_tokens > 0`, []string{"/v1/messages"}},
		{"total_tokens excludes no-usage row", `total_tokens > 0`, []string{"/v1/messages", "/v1/messages"}},
		{"no match", `model = "does-not-exist"`, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows, err := db.GetExchanges(ctx, tt.query, 100, 0)
			if err != nil {
				t.Fatalf("GetExchanges(%q): %v", tt.query, err)
			}
			var gotPaths []string
			for _, r := range rows {
				gotPaths = append(gotPaths, r.Path)
			}
			sort.Strings(gotPaths)
			want := append([]string(nil), tt.wantPaths...)
			sort.Strings(want)
			if !reflect.DeepEqual(gotPaths, want) {
				t.Errorf("GetExchanges(%q) paths = %v, want %v", tt.query, gotPaths, want)
			}
		})
	}

	if _, err := db.GetExchanges(ctx, `nope = 1`, 100, 0); err == nil {
		t.Error("GetExchanges with an unknown filter field: expected error, got nil")
	}
}

func TestGetTokenTotals(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := float64(time.Now().Unix())

	rows := []Exchange{
		{SessionID: "a", Path: "/p", CreatedAt: now, RawRequest: "{}", RawResponse: "{}",
			InputTokens: intPtr(10), OutputTokens: intPtr(20), InputCost: floatPtr(0.01), OutputCost: floatPtr(0.02)},
		{SessionID: "a", Path: "/p", CreatedAt: now + 1, RawRequest: "{}", RawResponse: "{}",
			InputTokens: intPtr(5), OutputTokens: intPtr(15), InputCost: floatPtr(0.005), OutputCost: floatPtr(0.015)},
		{SessionID: "b", Path: "/p", CreatedAt: now + 2, RawRequest: "{}", RawResponse: "{}",
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
		t.Errorf("TotalCost = %v, want ~0.05 (row without cost contributes NULL, not 0)", deref(totals.TotalCost))
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

	_ = db.SaveExchange(ctx, Exchange{SessionID: "old", Path: "/p", CreatedAt: now, RawRequest: "{}", RawResponse: "{}"})
	_ = db.SaveExchange(ctx, Exchange{SessionID: "new", Path: "/p", CreatedAt: now + 10, RawRequest: "{}", RawResponse: "{}"})
	if err := db.SetSessionName(ctx, "old", "Old"); err != nil {
		t.Fatalf("SetSessionName: %v", err)
	}

	stats, err := db.GetSessionStats(ctx, 50, 0)
	if err != nil {
		t.Fatalf("GetSessionStats: %v", err)
	}
	if len(stats) != 2 {
		t.Fatalf("got %d session stats, want 2", len(stats))
	}
	if stats[0].SessionID != "new" {
		t.Errorf("expected most-recently-active session first, got %+v", stats[0])
	}
	if stats[1].SessionID != "old" || stats[1].SessionName != "Old" {
		t.Errorf("expected sessions join to populate SessionName, got %+v", stats[1])
	}

	page, err := db.GetSessionStats(ctx, 1, 0)
	if err != nil {
		t.Fatalf("GetSessionStats(limit=1): %v", err)
	}
	if len(page) != 1 || page[0].SessionID != "new" {
		t.Fatalf("got %+v, want single most-recent session", page)
	}
	page, err = db.GetSessionStats(ctx, 1, 1)
	if err != nil {
		t.Fatalf("GetSessionStats(offset=1): %v", err)
	}
	if len(page) != 1 || page[0].SessionID != "old" {
		t.Fatalf("got %+v, want single second-most-recent session", page)
	}

	total, err := db.CountSessionStats(ctx)
	if err != nil {
		t.Fatalf("CountSessionStats: %v", err)
	}
	if total != 2 {
		t.Errorf("CountSessionStats = %d, want 2", total)
	}
}

// TestGetSessionStats_ModelAndCacheCosts asserts the most-used-model ranking
// (by exchange count, ties broken alphabetically) and that cache creation/
// read costs are now summed alongside input/output costs.
func TestGetSessionStats_ModelAndCacheCosts(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := float64(time.Now().Unix())

	rows := []Exchange{
		{SessionID: "s", Path: "/p", CreatedAt: now, RawRequest: "{}", RawResponse: "{}",
			Model: strPtr("claude-opus"), CacheCreationCost: floatPtr(0.01), CacheReadCost: floatPtr(0.02)},
		{SessionID: "s", Path: "/p", CreatedAt: now + 1, RawRequest: "{}", RawResponse: "{}",
			Model: strPtr("claude-sonnet"), CacheCreationCost: floatPtr(0.03)},
		{SessionID: "s", Path: "/p", CreatedAt: now + 2, RawRequest: "{}", RawResponse: "{}",
			Model: strPtr("claude-sonnet"), CacheReadCost: floatPtr(0.04)},
	}
	for _, r := range rows {
		if err := db.SaveExchange(ctx, r); err != nil {
			t.Fatalf("SaveExchange: %v", err)
		}
	}

	stats, err := db.GetSessionStats(ctx, 50, 0)
	if err != nil {
		t.Fatalf("GetSessionStats: %v", err)
	}
	if len(stats) != 1 {
		t.Fatalf("got %d session stats, want 1", len(stats))
	}
	s := stats[0]
	if s.Model == nil || *s.Model != "claude-sonnet" {
		t.Errorf("Model = %v, want claude-sonnet (used in 2 of 3 exchanges)", deref(s.Model))
	}
	if s.TotalCacheCreationCost == nil || round4(*s.TotalCacheCreationCost) != round4(0.04) {
		t.Errorf("TotalCacheCreationCost = %v, want ~0.04", s.TotalCacheCreationCost)
	}
	if s.TotalCacheReadCost == nil || round4(*s.TotalCacheReadCost) != round4(0.06) {
		t.Errorf("TotalCacheReadCost = %v, want ~0.06", s.TotalCacheReadCost)
	}
}

func TestGetSessionStatsSince(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := float64(time.Now().Unix())

	_ = db.SaveExchange(ctx, Exchange{SessionID: "a", Path: "/p", CreatedAt: now, RawRequest: "{}", RawResponse: "{}"})
	firstPage, err := db.GetExchanges(ctx, "", 1, 0)
	if err != nil || len(firstPage) != 1 {
		t.Fatalf("GetExchanges: rows=%+v err=%v", firstPage, err)
	}
	sinceID := firstPage[0].ID

	// Two different sessions each write a new exchange after sinceID,
	// simulating two Claude Code terminals active within the same poll
	// window — both must appear in the delta, not just the one owning the
	// highest exchange id.
	_ = db.SaveExchange(ctx, Exchange{SessionID: "b", Path: "/p", CreatedAt: now + 1, RawRequest: "{}", RawResponse: "{}"})
	_ = db.SaveExchange(ctx, Exchange{SessionID: "c", Path: "/p", CreatedAt: now + 2, RawRequest: "{}", RawResponse: "{}"})

	delta, err := db.GetSessionStatsSince(ctx, sinceID)
	if err != nil {
		t.Fatalf("GetSessionStatsSince: %v", err)
	}
	if len(delta) != 2 {
		t.Fatalf("got %d delta rows, want 2 (b and c, not a): %+v", len(delta), delta)
	}
	seen := map[string]bool{delta[0].SessionID: true, delta[1].SessionID: true}
	if !seen["b"] || !seen["c"] {
		t.Errorf("expected sessions b and c in delta, got %+v", delta)
	}
}

func TestGetDailyCosts(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := float64(time.Now().Unix())

	if err := db.SaveExchange(ctx, Exchange{
		SessionID: "a", Path: "/p", CreatedAt: now, RawRequest: "{}", RawResponse: "{}",
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
		SessionID: "old", Path: "/p", CreatedAt: oldRow, RawRequest: "{}", RawResponse: "{}",
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

	for _, sess := range []string{"a", "a", "b", "c"} {
		if err := db.SaveExchange(ctx, Exchange{SessionID: sess, Path: "/p", CreatedAt: now, RawRequest: "{}", RawResponse: "{}"}); err != nil {
			t.Fatalf("SaveExchange: %v", err)
		}
	}

	n, affected, err := db.DeleteExchanges(ctx, []string{"a", "b"})
	if err != nil {
		t.Fatalf("DeleteExchanges([a,b]): %v", err)
	}
	if n != 3 {
		t.Fatalf("deleted %d rows, want 3", n)
	}
	if !reflect.DeepEqual(affected, []string{"a", "b"}) {
		t.Fatalf("affectedSessions = %v, want [a b]", affected)
	}
	remaining, err := db.GetExchanges(ctx, "", 100, 0)
	if err != nil {
		t.Fatalf("GetExchanges: %v", err)
	}
	if len(remaining) != 1 || remaining[0].SessionID != "c" {
		t.Fatalf("unexpected remaining rows: %+v", remaining)
	}

	n, affected, err = db.DeleteExchanges(ctx, nil)
	if err != nil {
		t.Fatalf("DeleteExchanges(nil): %v", err)
	}
	if n != 1 || !reflect.DeepEqual(affected, []string{"c"}) {
		t.Fatalf("DeleteExchanges(nil): n=%d affected=%v, want n=1 affected=[c]", n, affected)
	}
	remaining, err = db.GetExchanges(ctx, "", 100, 0)
	if err != nil {
		t.Fatalf("GetExchanges: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("empty sessionIDs should delete every row: got %d remaining, want 0", len(remaining))
	}

	if n, affected, err := db.DeleteExchanges(ctx, nil); err != nil || n != 0 || affected != nil {
		t.Fatalf("DeleteExchanges(nil) on empty table: n=%d affected=%v err=%v, want 0 nil nil", n, affected, err)
	}
}

// TestDeleteSessions verifies that DeleteSessions removes only the requested
// sessions' rows and no-ops on an empty ID slice.
func TestDeleteSessions(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	for _, sess := range []string{"a", "b", "c"} {
		if err := db.SetSessionName(ctx, sess, "name-"+sess); err != nil {
			t.Fatalf("SetSessionName(%s): %v", sess, err)
		}
	}

	if err := db.DeleteSessions(ctx, nil); err != nil {
		t.Fatalf("DeleteSessions(nil): %v", err)
	}
	if err := db.DeleteSessions(ctx, []string{"a", "c"}); err != nil {
		t.Fatalf("DeleteSessions([a,c]): %v", err)
	}

	var remaining []string
	rows, err := db.sql.QueryContext(ctx, "SELECT session_id FROM sessions ORDER BY session_id")
	if err != nil {
		t.Fatalf("query sessions: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		remaining = append(remaining, id)
	}
	if !reflect.DeepEqual(remaining, []string{"b"}) {
		t.Fatalf("remaining sessions = %v, want [b]", remaining)
	}
}

// TestSetSessionName_BlankResetsToSessionID verifies a blank name falls back
// to the session id instead of removing the row.
func TestSetSessionName_BlankResetsToSessionID(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	if err := db.SetSessionName(ctx, "s1", "Custom"); err != nil {
		t.Fatalf("SetSessionName: %v", err)
	}
	if err := db.SetSessionName(ctx, "s1", "   "); err != nil {
		t.Fatalf("SetSessionName(blank): %v", err)
	}
	name, ok, err := db.SessionName(ctx, "s1")
	if err != nil || !ok || name != "s1" {
		t.Fatalf("SessionName = %q ok=%v err=%v, want %q true nil", name, ok, err, "s1")
	}
}

// TestDeleteExchanges_SoftDeletes verifies that DeleteExchanges hides the
// exchanges and drops their exchange_data, while the exchanges rows stay
// behind so global history (GetTokenTotals, GetDailyCosts) keeps counting a
// purged session.
func TestDeleteExchanges_SoftDeletes(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := float64(time.Now().Unix())

	purged := Exchange{
		SessionID: "purge-me", Path: "/p", CreatedAt: now, RawRequest: "{}", RawResponse: "{}",
		InputTokens: intPtr(10), OutputTokens: intPtr(5),
		InputCost: floatPtr(0.01), OutputCost: floatPtr(0.02),
	}
	if err := db.SaveExchange(ctx, purged); err != nil {
		t.Fatalf("SaveExchange: %v", err)
	}
	list, err := db.GetExchanges(ctx, "", 10, 0)
	if err != nil || len(list) != 1 {
		t.Fatalf("GetExchanges before delete: %v, %+v", err, list)
	}
	exchangeID := list[0].ID

	beforeTotals, err := db.GetTokenTotals(ctx, "", nil)
	if err != nil {
		t.Fatalf("GetTokenTotals before delete: %v", err)
	}

	if n, affected, err := db.DeleteExchanges(ctx, []string{"purge-me"}); err != nil || n != 1 || !reflect.DeepEqual(affected, []string{"purge-me"}) {
		t.Fatalf("DeleteExchanges: n=%d affected=%v err=%v", n, affected, err)
	}

	remaining, err := db.GetExchanges(ctx, "", 10, 0)
	if err != nil {
		t.Fatalf("GetExchanges after delete: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("deleted exchange still listed: %+v", remaining)
	}
	if total, err := db.CountExchanges(ctx, ""); err != nil || total != 0 {
		t.Fatalf("CountExchanges after delete = %d, %v, want 0", total, err)
	}
	if detail, err := db.GetExchangeDetail(ctx, exchangeID); err != nil || detail != nil {
		t.Fatalf("GetExchangeDetail after delete = %+v, %v, want nil", detail, err)
	}
	if stats, err := db.GetSessionStats(ctx, 10, 0); err != nil || len(stats) != 0 {
		t.Fatalf("GetSessionStats after delete = %+v, %v, want empty", stats, err)
	}
	if total, err := db.CountSessionStats(ctx); err != nil || total != 0 {
		t.Fatalf("CountSessionStats after delete = %d, %v, want 0", total, err)
	}

	var rowCount, dataCount int
	if err := db.sql.QueryRowContext(ctx, "SELECT COUNT(*) FROM exchanges WHERE deleted_at IS NOT NULL").Scan(&rowCount); err != nil {
		t.Fatalf("count exchanges: %v", err)
	}
	if err := db.sql.QueryRowContext(ctx, "SELECT COUNT(*) FROM exchange_data").Scan(&dataCount); err != nil {
		t.Fatalf("count exchange_data: %v", err)
	}
	if rowCount != 1 || dataCount != 0 {
		t.Fatalf("soft-deleted exchanges = %d, exchange_data rows = %d, want 1 and 0", rowCount, dataCount)
	}

	afterTotals, err := db.GetTokenTotals(ctx, "", nil)
	if err != nil {
		t.Fatalf("GetTokenTotals after delete: %v", err)
	}
	if !reflect.DeepEqual(beforeTotals, afterTotals) {
		t.Errorf("totals changed after deleting exchanges: before=%+v after=%+v", beforeTotals, afterTotals)
	}
	if afterTotals.TotalCost == nil || round4(*afterTotals.TotalCost) != round4(0.03) {
		t.Errorf("TotalCost after delete = %v, want ~0.03", afterTotals.TotalCost)
	}

	daily, err := db.GetDailyCosts(ctx, 60)
	if err != nil {
		t.Fatalf("GetDailyCosts after delete: %v", err)
	}
	if len(daily) != 1 || round4(daily[0].DailyCost) != round4(0.03) {
		t.Fatalf("GetDailyCosts after delete = %+v, want one bucket of ~0.03", daily)
	}
}

// TestDeleteExchanges_SessionResurrects verifies a new exchange for a
// previously deleted session makes it visible again without bringing back the
// deleted exchanges.
func TestDeleteExchanges_SessionResurrects(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := float64(time.Now().Unix())

	for i := 0; i < 2; i++ {
		if err := db.SaveExchange(ctx, Exchange{SessionID: "s", Path: "/p", CreatedAt: now + float64(i), RawRequest: "{}", RawResponse: "{}"}); err != nil {
			t.Fatalf("SaveExchange: %v", err)
		}
	}
	if _, _, err := db.DeleteExchanges(ctx, []string{"s"}); err != nil {
		t.Fatalf("DeleteExchanges: %v", err)
	}
	if err := db.SaveExchange(ctx, Exchange{SessionID: "s", Path: "/p", CreatedAt: now + 10, RawRequest: "{}", RawResponse: "{}"}); err != nil {
		t.Fatalf("SaveExchange after delete: %v", err)
	}

	stats, err := db.GetSessionStats(ctx, 10, 0)
	if err != nil || len(stats) != 1 || stats[0].ExchangeCount != 1 {
		t.Fatalf("GetSessionStats = %+v, %v, want one session with one visible exchange", stats, err)
	}
}

// TestOpen_UsesIncrementalAutoVacuum verifies the pragma was applied before
// the first table was created.
func TestOpen_UsesIncrementalAutoVacuum(t *testing.T) {
	db := openTestDB(t)

	var mode int
	if err := db.sql.QueryRow("PRAGMA auto_vacuum").Scan(&mode); err != nil {
		t.Fatalf("PRAGMA auto_vacuum: %v", err)
	}
	if mode != 2 {
		t.Fatalf("auto_vacuum = %d, want 2 (INCREMENTAL)", mode)
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
				SessionID: "concurrent", Path: "/p", CreatedAt: now + float64(i),
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

	rows, err := db.GetExchanges(ctx, `session = "concurrent"`, 100, 0)
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
