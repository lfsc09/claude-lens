package admin

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lfsc09/claude-lens/internal/database"
	"github.com/lfsc09/claude-lens/internal/status"
)

// sseEventReader reads "data: ...\n\n" events off an SSE response body on a
// background goroutine, so tests can assert both "an event arrives" (read
// with a timeout) and "no event arrives" (nothing shows up before a
// timeout) without blocking forever on a stream that, by design, may stay
// silent for many ticks.
type sseEventReader struct {
	lines chan string
	errs  chan error
}

func newSSEEventReader(body io.Reader) *sseEventReader {
	r := &sseEventReader{lines: make(chan string), errs: make(chan error, 1)}
	go func() {
		reader := bufio.NewReader(body)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				r.errs <- err
				return
			}
			if strings.HasPrefix(line, "data: ") {
				r.lines <- line
			}
		}
	}()
	return r
}

// next waits up to timeout for the next event, decoding its payload. It
// fails the test if no event arrives in time.
func (r *sseEventReader) next(t *testing.T, timeout time.Duration) ssePayload {
	t.Helper()
	select {
	case line := <-r.lines:
		var p ssePayload
		if err := json.Unmarshal([]byte(strings.TrimPrefix(strings.TrimSpace(line), "data: ")), &p); err != nil {
			t.Fatalf("decode event: %v", err)
		}
		return p
	case err := <-r.errs:
		t.Fatalf("reading event: %v", err)
		return ssePayload{}
	case <-time.After(timeout):
		t.Fatalf("no event received within %v", timeout)
		return ssePayload{}
	}
}

// expectQuiet fails the test if an event arrives before timeout — used to
// prove the stream doesn't push when nothing has changed.
func (r *sseEventReader) expectQuiet(t *testing.T, timeout time.Duration) {
	t.Helper()
	select {
	case line := <-r.lines:
		t.Fatalf("received unexpected event while quiet: %q", line)
	case err := <-r.errs:
		t.Fatalf("reading event: %v", err)
	case <-time.After(timeout):
	}
}

func TestSSEStream_PushesOnConnectThenOnlyOnChange(t *testing.T) {
	old := sseInterval
	sseInterval = 30 * time.Millisecond
	defer func() { sseInterval = old }()

	s, db, st, fresh, limitersFresh := newTestServerWithStatus(t)
	if err := db.SaveExchange(context.Background(), database.Exchange{
		SessionID: "s1", Path: "/p", Timestamp: float64(time.Now().Unix()), RawRequest: "{}", RawResponse: "{}",
	}); err != nil {
		t.Fatalf("SaveExchange: %v", err)
	}

	server := httptest.NewServer(s.handler)
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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

	events := newSSEEventReader(resp.Body)

	// A newly connected client gets an immediate payload regardless of
	// change tracking.
	initial := events.next(t, time.Second)
	if initial.LatestExchangeID != 1 {
		t.Errorf("initial: LatestExchangeID = %d, want 1", initial.LatestExchangeID)
	}
	if initial.Totals.Count != 1 {
		t.Errorf("initial: Totals.Count = %d, want 1", initial.Totals.Count)
	}

	// Several quiet ticks with no new exchange and no status change should
	// produce no further events.
	events.expectQuiet(t, 200*time.Millisecond)

	// The proxy saving a new exchange bumps Fresh — the next tick should
	// push a fresh payload.
	fresh.Bump()
	afterFresh := events.next(t, time.Second)
	if afterFresh.ProxyStatus != "unreachable" {
		t.Errorf("afterFresh: ProxyStatus = %q, want unreachable", afterFresh.ProxyStatus)
	}

	// Quiet again after the fresh-triggered push.
	events.expectQuiet(t, 200*time.Millisecond)

	// A proxy status change should also trigger a push, even with no new
	// exchange.
	st.Set(status.OK)
	afterStatus := events.next(t, time.Second)
	if afterStatus.ProxyStatus != "ok" {
		t.Errorf("afterStatus: ProxyStatus = %q, want ok", afterStatus.ProxyStatus)
	}

	// Quiet again after the status-triggered push.
	events.expectQuiet(t, 200*time.Millisecond)

	// A limiter refresh (background loop or request path) bumps
	// limitersFresh independently of both fresh and status — the stream
	// should push a payload carrying the new LimitersVersion even though
	// neither ProxyStatus nor LatestExchangeID changed.
	limitersFresh.Bump()
	afterLimiters := events.next(t, time.Second)
	if afterLimiters.LimitersVersion != 1 {
		t.Errorf("afterLimiters: LimitersVersion = %d, want 1", afterLimiters.LimitersVersion)
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
	h := &handlers{db: db, status: st, limitersFresh: status.NewFresh()}
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

	h := &handlers{db: db, status: status.New(), limitersFresh: status.NewFresh()}
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
