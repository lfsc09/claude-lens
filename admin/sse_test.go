package admin

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lfsc09/claude-lens/internal/database"
	"github.com/lfsc09/claude-lens/internal/status"
)

func TestSSEStream_PushesPeriodicPayloads(t *testing.T) {
	old := sseInterval
	sseInterval = 30 * time.Millisecond
	defer func() { sseInterval = old }()

	s, db := newTestServer(t)
	if err := db.SaveExchange(context.Background(), database.Exchange{
		SessionID: "s1", Path: "/p", Timestamp: float64(time.Now().Unix()), RawRequest: "{}", RawResponse: "{}",
	}); err != nil {
		t.Fatalf("SaveExchange: %v", err)
	}

	server := httptest.NewServer(s.handler)
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/stream", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/stream: %v", err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache", cc)
	}

	reader := bufio.NewReader(resp.Body)
	var payloads []ssePayload
	for i := 0; i < 3; i++ {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("reading event %d: %v", i, err)
		}
		if !strings.HasPrefix(line, "data: ") {
			t.Fatalf("event %d: line = %q, want data: prefix", i, line)
		}
		var p ssePayload
		if err := json.Unmarshal([]byte(strings.TrimPrefix(strings.TrimSpace(line), "data: ")), &p); err != nil {
			t.Fatalf("event %d: decode: %v", i, err)
		}
		payloads = append(payloads, p)

		blank, err := reader.ReadString('\n')
		if err != nil || blank != "\n" {
			t.Fatalf("event %d: expected trailing blank line, got %q (err=%v)", i, blank, err)
		}
	}

	if len(payloads) != 3 {
		t.Fatalf("got %d payloads, want 3", len(payloads))
	}
	for i, p := range payloads {
		if p.ProxyStatus != "unreachable" {
			t.Errorf("payload %d: ProxyStatus = %q, want unreachable (fresh status.Flag)", i, p.ProxyStatus)
		}
		if p.LatestExchangeID != 1 {
			t.Errorf("payload %d: LatestExchangeID = %d, want 1", i, p.LatestExchangeID)
		}
		if p.Totals.Count != 1 {
			t.Errorf("payload %d: Totals.Count = %d, want 1", i, p.Totals.Count)
		}
	}
}

func TestSSEStream_ReflectsStatusChanges(t *testing.T) {
	old := sseInterval
	sseInterval = 30 * time.Millisecond
	defer func() { sseInterval = old }()

	db, err := database.Open(context.Background(), tempDBPath(t))
	if err != nil {
		t.Fatalf("database.Open: %v", err)
	}
	defer db.Close()

	st := status.New()
	h := &handlers{db: db, status: st}
	ctx := context.Background()

	p1, err := h.buildSSEPayload(ctx, "")
	if err != nil {
		t.Fatalf("buildSSEPayload: %v", err)
	}
	if p1.ProxyStatus != "unreachable" {
		t.Errorf("ProxyStatus = %q, want unreachable", p1.ProxyStatus)
	}

	st.Set(status.OK)
	p2, err := h.buildSSEPayload(ctx, "")
	if err != nil {
		t.Fatalf("buildSSEPayload: %v", err)
	}
	if p2.ProxyStatus != "ok" {
		t.Errorf("ProxyStatus = %q, want ok after status change", p2.ProxyStatus)
	}
}

// TestBuildSSEPayload_RangeFiltersTotals proves /stream's totals respect the
// same ?range= window as the dashboard page (admin/dashboard_range.go), rather
// than always reporting an unbounded all-time count.
func TestBuildSSEPayload_RangeFiltersTotals(t *testing.T) {
	db, err := database.Open(context.Background(), tempDBPath(t))
	if err != nil {
		t.Fatalf("database.Open: %v", err)
	}
	defer db.Close()

	h := &handlers{db: db, status: status.New()}
	ctx := context.Background()

	recentTok, midOldTok := 111, 222
	now := time.Now()
	if err := db.SaveExchange(ctx, database.Exchange{
		SessionID: "recent", Path: "/p", Timestamp: float64(now.Unix()), RawRequest: "{}", RawResponse: "{}",
		InputTokens: &recentTok,
	}); err != nil {
		t.Fatalf("SaveExchange(recent): %v", err)
	}
	if err := db.SaveExchange(ctx, database.Exchange{
		SessionID: "mid-old", Path: "/p", Timestamp: float64(now.AddDate(0, 0, -50).Unix()), RawRequest: "{}", RawResponse: "{}",
		InputTokens: &midOldTok,
	}); err != nil {
		t.Fatalf("SaveExchange(mid-old): %v", err)
	}

	weekly, err := h.buildSSEPayload(ctx, "this-week")
	if err != nil {
		t.Fatalf("buildSSEPayload(this-week): %v", err)
	}
	if weekly.Totals.TotalInputTokens != int64(recentTok) {
		t.Errorf("this-week TotalInputTokens = %d, want %d (50-day-old row must be excluded)",
			weekly.Totals.TotalInputTokens, recentTok)
	}

	last60, err := h.buildSSEPayload(ctx, "last-60-days")
	if err != nil {
		t.Fatalf("buildSSEPayload(last-60-days): %v", err)
	}
	wantBoth := int64(recentTok + midOldTok)
	if last60.Totals.TotalInputTokens != wantBoth {
		t.Errorf("last-60-days TotalInputTokens = %d, want %d (both rows included)",
			last60.Totals.TotalInputTokens, wantBoth)
	}
}

func tempDBPath(t *testing.T) string {
	t.Helper()
	return t.TempDir() + "/test.db"
}
