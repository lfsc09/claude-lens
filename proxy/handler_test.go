package proxy

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lfsc09/claude-lens/internal/config"
	"github.com/lfsc09/claude-lens/internal/database"
	"github.com/lfsc09/claude-lens/internal/pricing"
	"github.com/lfsc09/claude-lens/internal/status"
)

func newTestHandler(t *testing.T, upstreamURL string) (*Handler, *database.DB) {
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

	cfg := config.Config{AnthropicBaseURL: upstreamURL}
	h, err := NewHandler(cfg, db, est, status.New())
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	return h, db
}

// TestStreaming_NotBatched proves the plan's §6 gotcha is actually handled:
// the client must receive each upstream chunk as it arrives, not buffered
// until the full response is ready. It streams three SSE chunks from the
// fake upstream with a delay between each, and asserts the proxy delivers
// each one to the client before the next is even sent — if FlushInterval
// were unset (or 0), all three would arrive in a single burst instead.
func TestStreaming_NotBatched(t *testing.T) {
	chunkDelay := 150 * time.Millisecond
	chunks := []string{
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":10}}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n",
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":5}}\n\n",
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		for _, c := range chunks {
			time.Sleep(chunkDelay)
			io.WriteString(w, c)
			flusher.Flush()
		}
	}))
	defer upstream.Close()

	h, db := newTestHandler(t, upstream.URL)
	server := httptest.NewServer(h)
	defer server.Close()

	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/messages", strings.NewReader(`{"model":"claude-sonnet-5","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("x-claude-code-session-id", "test-session")

	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("client request failed: %v", err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	var arrivalTimes []time.Duration
	for i := 0; i < len(chunks); i++ {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("reading chunk %d: %v (got %q so far)", i, err, line)
		}
		// Each fixture chunk's first line is "event: ...\n" — read until we
		// consume the trailing blank line so we've read one full SSE event.
		for {
			l2, err := reader.ReadString('\n')
			if err != nil {
				t.Fatalf("reading rest of chunk %d: %v", i, err)
			}
			if l2 == "\n" {
				break
			}
		}
		arrivalTimes = append(arrivalTimes, time.Since(start))
	}

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	// Each chunk should arrive roughly chunkDelay after the previous one,
	// not all at once at the very end (which would mean ~3*chunkDelay for
	// the first chunk too instead of ~1*chunkDelay).
	if arrivalTimes[0] > chunkDelay*2 {
		t.Errorf("first chunk arrived after %v, want close to %v (response appears buffered, not streamed)", arrivalTimes[0], chunkDelay)
	}
	gapLast := arrivalTimes[2] - arrivalTimes[1]
	if gapLast < chunkDelay/2 {
		t.Errorf("gap between last two chunks = %v, want ~%v (chunks arrived in a single burst)", gapLast, chunkDelay)
	}

	// After the stream completes and the client has read the whole body,
	// teeReadCloser's onClose fires SaveExchange in a goroutine — poll
	// briefly for the row to land.
	waitForExchangeCount(t, db, "test-session", 1)

	detail := mustGetExchange(t, db, "test-session")
	if detail.OutputText == nil || *detail.OutputText != "hello" {
		t.Errorf("OutputText = %v, want %q", detail.OutputText, "hello")
	}
	if detail.InputTokens == nil || *detail.InputTokens != 10 {
		t.Errorf("InputTokens = %v, want 10", detail.InputTokens)
	}
	if detail.OutputTokens == nil || *detail.OutputTokens != 5 {
		t.Errorf("OutputTokens = %v, want 5", detail.OutputTokens)
	}
	if !detail.IsStreaming {
		t.Error("IsStreaming = false, want true")
	}
}

func TestNonStreamingPOST_SavesExchangeWithCost(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"content":[{"type":"text","text":"hi there"}],"usage":{"input_tokens":1000000,"output_tokens":1000000}}`)
	}))
	defer upstream.Close()

	h, db := newTestHandler(t, upstream.URL)
	server := httptest.NewServer(h)
	defer server.Close()

	req, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/messages",
		strings.NewReader(`{"model":"claude-sonnet-5","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("x-session-id", "sess-abc")
	req.Header.Set("x-session-name", "My Session")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "hi there") {
		t.Fatalf("client did not receive upstream body: %q", body)
	}

	waitForExchangeCount(t, db, "sess-abc", 1)
	detail := mustGetExchange(t, db, "sess-abc")
	if detail.SessionName == nil || *detail.SessionName != "My Session" {
		t.Errorf("SessionName = %v, want %q", detail.SessionName, "My Session")
	}
	// claude-sonnet-5 is seeded at $3/$15 per million tokens; 1M in + 1M out.
	if detail.Cost == nil || *detail.Cost != 18.0 {
		t.Errorf("Cost = %v, want 18.0", detail.Cost)
	}
}

func TestGetRequest_PassesThroughWithoutRecording(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("upstream got method %s, want GET", r.Method)
		}
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer upstream.Close()

	h, db := newTestHandler(t, upstream.URL)
	server := httptest.NewServer(h)
	defer server.Close()

	resp, err := http.Get(server.URL + "/v1/models")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "ok") {
		t.Fatalf("unexpected body: %q", body)
	}

	rows, err := db.GetExchanges(context.Background(), "", 100, 0)
	if err != nil {
		t.Fatalf("GetExchanges: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("GET request was recorded as an exchange, want none: %+v", rows)
	}
}

func TestAuthTokenAndCustomHeaders_AreInjectedUpstream(t *testing.T) {
	var gotAuth, gotCustom string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotCustom = r.Header.Get("X-Team")
		fmt.Fprint(w, `{}`)
	}))
	defer upstream.Close()

	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("database.Open: %v", err)
	}
	defer db.Close()
	est := pricing.New(db)
	_ = est.Refresh(context.Background())

	cfg := config.Config{
		AnthropicBaseURL:       upstream.URL,
		AnthropicAuthToken:     "sk-test-token",
		AnthropicCustomHeaders: "X-Team: platform",
	}
	h, err := NewHandler(cfg, db, est, status.New())
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	server := httptest.NewServer(h)
	defer server.Close()

	resp, err := http.Get(server.URL + "/v1/models")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	resp.Body.Close()

	if gotAuth != "Bearer sk-test-token" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer sk-test-token")
	}
	if gotCustom != "platform" {
		t.Errorf("X-Team = %q, want %q", gotCustom, "platform")
	}
}

func TestUpstreamUnreachable_Returns502(t *testing.T) {
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("database.Open: %v", err)
	}
	defer db.Close()
	est := pricing.New(db)
	_ = est.Refresh(context.Background())

	st := status.New()
	// Port 1 is not something a fake upstream will ever be listening on.
	cfg := config.Config{AnthropicBaseURL: "http://127.0.0.1:1"}
	h, err := NewHandler(cfg, db, est, st)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	server := httptest.NewServer(h)
	defer server.Close()

	resp, err := http.Get(server.URL + "/v1/models")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusBadGateway)
	}
	if got := st.Get(); got != status.Degraded {
		t.Errorf("status flag = %q, want %q", got, status.Degraded)
	}
}

func waitForExchangeCount(t *testing.T, db *database.DB, sessionID string, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		rows, err := db.GetExchanges(context.Background(), sessionID, 100, 0)
		if err != nil {
			t.Fatalf("GetExchanges: %v", err)
		}
		if len(rows) >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d exchange(s) with session_id=%s", want, sessionID)
}

func mustGetExchange(t *testing.T, db *database.DB, sessionID string) *database.ExchangeDetail {
	t.Helper()
	rows, err := db.GetExchanges(context.Background(), sessionID, 1, 0)
	if err != nil || len(rows) == 0 {
		t.Fatalf("no exchange found for session_id=%s (err=%v)", sessionID, err)
	}
	detail, err := db.GetExchangeDetail(context.Background(), rows[0].ID)
	if err != nil || detail == nil {
		t.Fatalf("GetExchangeDetail(%d): %v", rows[0].ID, err)
	}
	return detail
}
