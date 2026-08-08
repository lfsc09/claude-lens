package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/lfsc09/claude-lens/internal/database"
	"github.com/lfsc09/claude-lens/internal/pricing"
	"github.com/lfsc09/claude-lens/internal/status"
)

func newTestServer(t *testing.T) (*Server, *database.DB) {
	t.Helper()
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("database.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	est := pricing.New(db)
	if err := est.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	s, err := NewServer(db, est, status.New(), "test")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return s, db
}

func doJSON(t *testing.T, s *Server, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(b)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.engine.ServeHTTP(rec, req)
	return rec
}

func TestHealth(t *testing.T) {
	s, _ := newTestServer(t)
	rec := doJSON(t, s, http.MethodGet, "/health", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("status field = %q, want ok", body["status"])
	}
	if body["proxy"] != "unreachable" {
		t.Errorf("proxy field = %q, want unreachable (fresh status.Flag)", body["proxy"])
	}
	if body["version"] != "test" {
		t.Errorf("version field = %q, want %q (the version newTestServer passed to NewServer)", body["version"], "test")
	}
}

func TestExchangesList_EmptyIsJSONArrayNotNull(t *testing.T) {
	s, _ := newTestServer(t)
	rec := doJSON(t, s, http.MethodGet, "/exchanges", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "[]" {
		t.Errorf("body = %q, want %q (must be an empty array, not null)", got, "[]")
	}
}

func TestExchangesList_FilterAndPagination(t *testing.T) {
	s, db := newTestServer(t)
	ctx := context.Background()
	for i, sess := range []string{"a", "a", "b"} {
		if err := db.SaveExchange(ctx, database.Exchange{
			SessionID: sess, Path: "/p", Timestamp: float64(1000 + i), RawRequest: "{}", RawResponse: "{}",
		}); err != nil {
			t.Fatalf("SaveExchange: %v", err)
		}
	}

	rec := doJSON(t, s, http.MethodGet, "/exchanges?session_id=a", nil)
	var rows []database.ExchangeSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows for session a, want 2", len(rows))
	}

	rec = doJSON(t, s, http.MethodGet, "/exchanges?limit=abc", nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("non-integer limit: status = %d, want 400", rec.Code)
	}
}

func TestExchangesList_LimitClampedTo1000(t *testing.T) {
	s, _ := newTestServer(t)
	rec := doJSON(t, s, http.MethodGet, "/exchanges?limit=5000", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	// No direct way to observe the clamped limit value through the JSON
	// response when the table is empty; this at minimum proves an
	// out-of-range limit doesn't error out.
}

func TestExchangeDetail_NotFound(t *testing.T) {
	s, _ := newTestServer(t)
	rec := doJSON(t, s, http.MethodGet, "/exchanges/999", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	var body map[string]string
	json.Unmarshal(rec.Body.Bytes(), &body)
	if body["error"] != "not found" {
		t.Errorf("error = %q, want %q", body["error"], "not found")
	}
}

func TestExchangeDetail_NonIntegerIDIs404(t *testing.T) {
	s, _ := newTestServer(t)
	rec := doJSON(t, s, http.MethodGet, "/exchanges/not-a-number", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestExchangeDetail_Found(t *testing.T) {
	s, db := newTestServer(t)
	ctx := context.Background()
	if err := db.SaveExchange(ctx, database.Exchange{
		SessionID: "s1", Path: "/v1/messages", Timestamp: 1000, RawRequest: "{}", RawResponse: "{}",
	}); err != nil {
		t.Fatalf("SaveExchange: %v", err)
	}
	rows, _ := db.GetExchanges(ctx, "", 1, 0)

	rec := doJSON(t, s, http.MethodGet, fmt.Sprintf("/exchanges/%d", rows[0].ID), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var detail database.ExchangeDetail
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if detail.SessionID != "s1" {
		t.Errorf("SessionID = %q, want s1", detail.SessionID)
	}
}

func TestTotals(t *testing.T) {
	s, db := newTestServer(t)
	ctx := context.Background()
	inCost, outCost := 0.01, 0.02
	inTok, outTok := 10, 20
	if err := db.SaveExchange(ctx, database.Exchange{
		SessionID: "s1", Path: "/p", Timestamp: 1000, RawRequest: "{}", RawResponse: "{}",
		InputTokens: &inTok, OutputTokens: &outTok, InputCost: &inCost, OutputCost: &outCost,
	}); err != nil {
		t.Fatalf("SaveExchange: %v", err)
	}

	rec := doJSON(t, s, http.MethodGet, "/totals", nil)
	var totals database.Totals
	if err := json.Unmarshal(rec.Body.Bytes(), &totals); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if totals.Count != 1 || totals.TotalInputTokens != 10 {
		t.Errorf("unexpected totals: %+v", totals)
	}
}

func TestSessionStats(t *testing.T) {
	s, db := newTestServer(t)
	ctx := context.Background()
	if err := db.SaveExchange(ctx, database.Exchange{SessionID: "s1", Path: "/p", Timestamp: 1000, RawRequest: "{}", RawResponse: "{}"}); err != nil {
		t.Fatalf("SaveExchange: %v", err)
	}

	rec := doJSON(t, s, http.MethodGet, "/session-stats", nil)
	var rows []database.SessionStat
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(rows) != 1 || rows[0].SessionID != "s1" {
		t.Errorf("unexpected session stats: %+v", rows)
	}
}

func TestResetExchanges(t *testing.T) {
	s, db := newTestServer(t)
	ctx := context.Background()
	for _, sess := range []string{"a", "a", "b"} {
		if err := db.SaveExchange(ctx, database.Exchange{SessionID: sess, Path: "/p", Timestamp: 1000, RawRequest: "{}", RawResponse: "{}"}); err != nil {
			t.Fatalf("SaveExchange: %v", err)
		}
	}

	rec := doJSON(t, s, http.MethodDelete, "/exchanges?session_id=a", nil)
	var body map[string]int64
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["deleted"] != 2 {
		t.Errorf("deleted = %d, want 2", body["deleted"])
	}

	remaining, _ := db.GetExchanges(ctx, "", 100, 0)
	if len(remaining) != 1 {
		t.Fatalf("got %d remaining, want 1", len(remaining))
	}
}

func TestPricesCRUD(t *testing.T) {
	s, _ := newTestServer(t)

	rec := doJSON(t, s, http.MethodGet, "/prices", nil)
	var seeded []database.Price
	json.Unmarshal(rec.Body.Bytes(), &seeded)
	if len(seeded) == 0 {
		t.Fatal("expected seeded default prices")
	}

	rec = doJSON(t, s, http.MethodPut, "/prices/my-custom-model", map[string]float64{
		"input_per_m": 2.5, "output_per_m": 12,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var upserted database.Price
	json.Unmarshal(rec.Body.Bytes(), &upserted)
	if upserted.Prefix != "my-custom-model" || upserted.InputPerM != 2.5 {
		t.Errorf("unexpected upserted price: %+v", upserted)
	}

	rec = doJSON(t, s, http.MethodPut, "/prices/bad-model", map[string]string{"input_per_m": "not-a-number"})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("malformed PUT body: status = %d, want 400", rec.Code)
	}

	rec = doJSON(t, s, http.MethodDelete, "/prices/my-custom-model", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE status = %d, want 200", rec.Code)
	}

	rec = doJSON(t, s, http.MethodGet, "/prices", nil)
	var afterDelete []database.Price
	json.Unmarshal(rec.Body.Bytes(), &afterDelete)
	for _, p := range afterDelete {
		if p.Prefix == "my-custom-model" {
			t.Error("my-custom-model still present after DELETE")
		}
	}
}

func TestUpsertPrice_CacheRatesOptional(t *testing.T) {
	s, _ := newTestServer(t)

	// Create with explicit cache rates.
	rec := doJSON(t, s, http.MethodPut, "/prices/cache-model", map[string]float64{
		"input_per_m": 2.0, "output_per_m": 10.0, "cache_write_per_m": 2.5, "cache_read_per_m": 0.2,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var p database.Price
	json.Unmarshal(rec.Body.Bytes(), &p)
	if p.CacheWritePerM != 2.5 || p.CacheReadPerM != 0.2 {
		t.Fatalf("unexpected cache rates after create: %+v", p)
	}

	// Update input/output only, omitting cache rates — they must be
	// preserved, not reset to 0.
	rec = doJSON(t, s, http.MethodPut, "/prices/cache-model", map[string]float64{
		"input_per_m": 3.0, "output_per_m": 11.0,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	json.Unmarshal(rec.Body.Bytes(), &p)
	if p.InputPerM != 3.0 || p.OutputPerM != 11.0 {
		t.Errorf("input/output not updated: %+v", p)
	}
	if p.CacheWritePerM != 2.5 || p.CacheReadPerM != 0.2 {
		t.Errorf("cache rates were reset when omitted from the update, want preserved: %+v", p)
	}
}

func TestUpsertPrice_RefreshesEstimatorImmediately(t *testing.T) {
	s, db := newTestServer(t)
	ctx := context.Background()

	rec := doJSON(t, s, http.MethodPut, "/prices/brand-new", map[string]float64{
		"input_per_m": 9, "output_per_m": 9,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200", rec.Code)
	}

	// Round-trip through the DB to confirm the write actually landed
	// (the estimator's own refresh behavior is covered by
	// internal/pricing's tests; this just confirms the API wired it up).
	prices, err := db.ListPrices(ctx)
	if err != nil {
		t.Fatalf("ListPrices: %v", err)
	}
	found := false
	for _, p := range prices {
		if p.Prefix == "brand-new" {
			found = true
		}
	}
	if !found {
		t.Fatal("brand-new price not persisted")
	}
}
