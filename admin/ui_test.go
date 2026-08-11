package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/lfsc09/claude-lens/internal/database"
)

func doForm(t *testing.T, s *Server, method, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.engine.ServeHTTP(rec, req)
	return rec
}

func doGet(t *testing.T, s *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	s.engine.ServeHTTP(rec, req)
	return rec
}

func TestUIDashboard_Renders(t *testing.T) {
	s, db := newTestServer(t)
	ctx := context.Background()
	inTok, outTok := 100, 50
	if err := db.SaveExchange(ctx, database.Exchange{
		SessionID: "sess-1", SessionName: strPtr("My Session"), Path: "/v1/messages", RawRequest: "{}", RawResponse: "{}",
		Timestamp: float64(nowUnix()), InputTokens: &inTok, OutputTokens: &outTok,
	}); err != nil {
		t.Fatalf("SaveExchange: %v", err)
	}

	rec := doGet(t, s, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Dashboard") {
		t.Error("missing 'Dashboard' heading")
	}
	if !strings.Contains(body, "My Session") {
		t.Error("session name not rendered in per-session table")
	}
	if !strings.Contains(body, "claude-lens") {
		t.Error("missing site title in nav")
	}
}

func TestUIDashboard_RendersCacheTokensAndCost(t *testing.T) {
	s, db := newTestServer(t)
	ctx := context.Background()
	inTok, outTok, cacheCreate, cacheRead := 100, 50, 200, 40
	if err := db.SaveExchange(ctx, database.Exchange{
		SessionID: "sess-cache", SessionName: strPtr("Cache Session"), Path: "/v1/messages", RawRequest: "{}", RawResponse: "{}",
		Timestamp: float64(nowUnix()), InputTokens: &inTok, OutputTokens: &outTok,
		CacheCreationTokens: &cacheCreate, CacheReadTokens: &cacheRead,
	}); err != nil {
		t.Fatalf("SaveExchange: %v", err)
	}

	rec := doGet(t, s, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Cache tokens") {
		t.Error("missing 'Cache tokens' stat card")
	}
	// 200 + 40 cache tokens comma-grouped.
	if !strings.Contains(body, "240") {
		t.Error("cache token total not rendered")
	}
}

func TestUIExchanges_RendersRowsAndFilter(t *testing.T) {
	s, db := newTestServer(t)
	ctx := context.Background()
	for _, sess := range []string{"a", "b"} {
		if err := db.SaveExchange(ctx, database.Exchange{
			SessionID: sess, Path: "/v1/messages", Timestamp: float64(nowUnix()), RawRequest: "{}", RawResponse: "{}",
		}); err != nil {
			t.Fatalf("SaveExchange: %v", err)
		}
	}

	rec := doGet(t, s, "/ui/exchanges")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Count(body, "/v1/messages") != 2 {
		t.Errorf("expected 2 rows with /v1/messages, got body: %s", body)
	}

	rec = doGet(t, s, "/ui/exchanges?q="+url.QueryEscape(`session = "a"`))
	if rec.Code != http.StatusOK {
		t.Fatalf("filtered status = %d, want 200", rec.Code)
	}
	if strings.Count(rec.Body.String(), "/v1/messages") != 1 {
		t.Errorf("filtered view should show exactly 1 row, body: %s", rec.Body.String())
	}
}

func TestUIExchanges_MalformedQueryShowsError(t *testing.T) {
	s, db := newTestServer(t)
	ctx := context.Background()
	if err := db.SaveExchange(ctx, database.Exchange{
		SessionID: "a", Path: "/v1/messages", Timestamp: float64(nowUnix()), RawRequest: "{}", RawResponse: "{}",
	}); err != nil {
		t.Fatalf("SaveExchange: %v", err)
	}

	rec := doGet(t, s, "/ui/exchanges?q="+url.QueryEscape(`bogus_field = 1`))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (a bad query is a rendered error, not an HTTP failure)", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "bogus_field") {
		t.Errorf("expected the parse error to be shown in the page, body: %s", body)
	}
	if strings.Contains(body, "/v1/messages") {
		t.Errorf("a malformed filter must not fall back to showing unfiltered rows, body: %s", body)
	}
}

func TestUIExchanges_ClearSessionOnlyForExactSessionQuery(t *testing.T) {
	s, db := newTestServer(t)
	ctx := context.Background()
	if err := db.SaveExchange(ctx, database.Exchange{
		SessionID: "a", Path: "/v1/messages", Timestamp: float64(nowUnix()), RawRequest: "{}", RawResponse: "{}",
	}); err != nil {
		t.Fatalf("SaveExchange: %v", err)
	}

	rec := doGet(t, s, "/ui/exchanges?q="+url.QueryEscape(`session = "a"`))
	if !strings.Contains(rec.Body.String(), "Clear session") {
		t.Errorf("exact session query should offer 'Clear session', body: %s", rec.Body.String())
	}

	rec = doGet(t, s, "/ui/exchanges?q="+url.QueryEscape(`session = "a" AND cost > 0`))
	body := rec.Body.String()
	if strings.Contains(body, "Clear session") {
		t.Errorf("a multi-condition query must not offer a scoped 'Clear session' delete, body: %s", body)
	}
	if !strings.Contains(body, "Clear all") {
		t.Errorf("expected 'Clear all' to be shown instead, body: %s", body)
	}
}

func TestUIExchanges_EmptyState(t *testing.T) {
	s, _ := newTestServer(t)
	rec := doGet(t, s, "/ui/exchanges")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "No exchanges found.") {
		t.Error("missing empty-state message")
	}
}

func TestUIExchangeDetail_RendersAndNotFound(t *testing.T) {
	s, db := newTestServer(t)
	ctx := context.Background()
	msgs := `[{"role":"user","content":"hello"}]`
	if err := db.SaveExchange(ctx, database.Exchange{
		SessionID: "s1", Path: "/v1/messages", Timestamp: float64(nowUnix()),
		RawRequest: `{"a":1}`, RawResponse: `{"b":2}`, InputMessages: &msgs,
		OutputText: strPtr("hi there"),
	}); err != nil {
		t.Fatalf("SaveExchange: %v", err)
	}
	rows, _ := db.GetExchanges(ctx, "", 1, 0)

	rec := doGet(t, s, "/ui/exchanges/"+strconv.FormatInt(rows[0].ID, 10))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "hi there") {
		t.Error("output text not rendered")
	}
	// The <pre> block HTML-escapes quotes, so check for the escaped form
	// and that key order was preserved (role before content, matching the
	// original JSON — see prettyJSON's use of json.Indent over
	// Unmarshal+MarshalIndent).
	roleIdx := strings.Index(body, "role&#34;: &#34;user")
	contentIdx := strings.Index(body, "content&#34;: &#34;hello")
	if roleIdx == -1 || contentIdx == -1 {
		t.Fatalf("input messages not pretty-printed, body: %s", body)
	}
	if roleIdx > contentIdx {
		t.Errorf("prettyJSON reordered keys: expected \"role\" before \"content\", got them swapped")
	}

	rec = doGet(t, s, "/ui/exchanges/999999")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "was not found") {
		t.Error("missing not-found message")
	}
}

func TestUIReset_DeletesAndRedirects(t *testing.T) {
	s, db := newTestServer(t)
	ctx := context.Background()
	if err := db.SaveExchange(ctx, database.Exchange{SessionID: "s1", Path: "/p", Timestamp: float64(nowUnix()), RawRequest: "{}", RawResponse: "{}"}); err != nil {
		t.Fatalf("SaveExchange: %v", err)
	}

	rec := doForm(t, s, http.MethodPost, "/ui/reset", url.Values{})
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	rows, _ := db.GetExchanges(ctx, "", 100, 0)
	if len(rows) != 0 {
		t.Errorf("expected all exchanges deleted, got %d", len(rows))
	}
}

func TestUIPrices_ListAndCreateAndUpdateAndDelete(t *testing.T) {
	s, db := newTestServer(t)
	ctx := context.Background()

	rec := doGet(t, s, "/ui/prices")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "claude-sonnet-5") {
		t.Error("seeded default price not listed")
	}

	rec = doForm(t, s, http.MethodPost, "/ui/prices", url.Values{
		"model_prefix": {"my-model"}, "input_per_m": {"2.5"}, "output_per_m": {"9"},
	})
	if rec.Code != http.StatusFound {
		t.Fatalf("create status = %d, want 302: %s", rec.Code, rec.Body.String())
	}
	prices, _ := db.ListPrices(ctx)
	found := false
	for _, p := range prices {
		if p.Prefix == "my-model" {
			found = true
			if p.InputPerM != 2.5 {
				t.Errorf("InputPerM = %v, want 2.5", p.InputPerM)
			}
		}
	}
	if !found {
		t.Fatal("my-model not created")
	}

	rec = doForm(t, s, http.MethodPost, "/ui/prices/my-model", url.Values{
		"input_per_m": {"5"}, "output_per_m": {"20"},
	})
	if rec.Code != http.StatusFound {
		t.Fatalf("update status = %d, want 302", rec.Code)
	}
	prices, _ = db.ListPrices(ctx)
	for _, p := range prices {
		if p.Prefix == "my-model" && p.InputPerM != 5 {
			t.Errorf("update did not apply: %+v", p)
		}
	}

	rec = doForm(t, s, http.MethodPost, "/ui/prices/my-model/delete", url.Values{})
	if rec.Code != http.StatusFound {
		t.Fatalf("delete status = %d, want 302", rec.Code)
	}
	prices, _ = db.ListPrices(ctx)
	for _, p := range prices {
		if p.Prefix == "my-model" {
			t.Error("my-model still present after delete")
		}
	}
}

func TestUICreatePrice_MissingFieldsIs400(t *testing.T) {
	s, _ := newTestServer(t)
	rec := doForm(t, s, http.MethodPost, "/ui/prices", url.Values{"model_prefix": {"x"}})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestNoRoute_Renders404Page(t *testing.T) {
	s, _ := newTestServer(t)
	rec := doGet(t, s, "/this-route-does-not-exist")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Page not found.") {
		t.Errorf("missing generic not-found message, body: %s", rec.Body.String())
	}
}

func strPtr(s string) *string { return &s }
func nowUnix() int64          { return 1700000000 }
