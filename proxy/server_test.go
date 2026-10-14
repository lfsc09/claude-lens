package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lfsc09/claude-lens/internal/pricing"
	"github.com/lfsc09/claude-lens/internal/status"
)

func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}

func TestServer_HealthAndGracefulShutdown(t *testing.T) {
	db := openTestDB(t)
	est := pricing.New(db)
	_ = est.Refresh(context.Background())

	st := status.New()
	if st.Get() != status.Unreachable {
		t.Errorf("initial status = %q, want %q", st.Get(), status.Unreachable)
	}

	reg := newTestRegistry(t, db, "http://127.0.0.1:1", "token", nil)
	server := NewServer(reg, db, est, st, status.NewFresh(), status.NewFresh())

	addr := freePort(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Run(ctx, addr) }()

	waitForListening(t, addr)

	if st.Get() != status.OK {
		t.Errorf("status after startup = %q, want %q", st.Get(), status.OK)
	}

	resp, err := http.Get(fmt.Sprintf("http://%s/health", addr))
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode /health body: %v", err)
	}
	resp.Body.Close()
	if body["status"] != "ok" {
		t.Errorf("/health body = %v, want status=ok", body)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned error after graceful shutdown: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return within 2s of context cancellation")
	}

	if st.Get() != status.Unreachable {
		t.Errorf("status after shutdown = %q, want %q", st.Get(), status.Unreachable)
	}
}

func waitForListening(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.Dial("tcp", addr)
		if err == nil {
			conn.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("server never started listening on %s", addr)
}

func TestRejectBrowserRequests(t *testing.T) {
	handler := rejectBrowserRequests(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	cases := []struct {
		name    string
		headers map[string]string
		want    int
	}{
		{"api client", nil, http.StatusNoContent},
		{"with origin", map[string]string{"Origin": "http://evil.test"}, http.StatusForbidden},
		{"with fetch metadata", map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusForbidden},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		for name, value := range tc.headers {
			req.Header.Set(name, value)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Errorf("%s: status = %d, want %d", tc.name, rec.Code, tc.want)
		}
	}
}
