package pricesync

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lfsc09/claude-lens/internal/database"
	"github.com/lfsc09/claude-lens/internal/litellm"
	"github.com/lfsc09/claude-lens/internal/pricing"
	"github.com/lfsc09/claude-lens/internal/upstream"
)

var testFetchers = Fetchers{database.ProxyTypeLiteLLM: litellm.NewClient()}

// activateLiteLLM points the seeded LiteLLM integration at baseURL, activates
// it, and returns the stored integration.
func activateLiteLLM(t *testing.T, db *database.DB, baseURL string) database.ProxyIntegration {
	t.Helper()
	ctx := context.Background()
	list, err := db.ListProxyIntegrations(ctx)
	if err != nil {
		t.Fatalf("ListProxyIntegrations: %v", err)
	}
	p := list[0]
	p.BaseURL = baseURL
	p.AuthToken = "token"
	if err := db.UpdateProxyIntegration(ctx, p); err != nil {
		t.Fatalf("UpdateProxyIntegration: %v", err)
	}
	if err := db.ActivateProxyIntegration(ctx, p.ID, 1); err != nil {
		t.Fatalf("ActivateProxyIntegration: %v", err)
	}
	got, err := db.GetProxyIntegration(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetProxyIntegration: %v", err)
	}
	return *got
}

func openTestDB(t *testing.T) *database.DB {
	t.Helper()
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("database.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestSync_UpsertsAndRefreshesEstimator(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"model_name":"brand-new-model","model_info":{"input_cost_per_token":0.000001,"output_cost_per_token":0.000002}}]}`))
	}))
	defer srv.Close()

	db := openTestDB(t)
	est := pricing.New(db)
	if err := est.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	result, err := Sync(context.Background(), db, est, testFetchers, activateLiteLLM(t, db, srv.URL))
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if result.Created != 1 || len(result.Models) != 1 {
		t.Errorf("got %+v, want 1 created model", result)
	}

	// The estimator must already reflect the new price without a separate
	// Refresh call — Sync is responsible for that.
	if _, ok := est.EstimateCosts("brand-new-model", 1_000_000, 0, 0, 0); !ok {
		t.Error("estimator wasn't refreshed after Sync")
	}

	got, err := db.GetActiveProxyIntegration(context.Background())
	if err != nil || got == nil {
		t.Fatalf("GetActiveProxyIntegration: %v, %v", got, err)
	}
	if got.PriceSyncedAt == 0 {
		t.Error("Sync didn't mark PriceSyncedAt")
	}
}

func TestSync_NonLiteLLMUpstreamReturnsErrorWithoutSideEffects(t *testing.T) {
	notLiteLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer notLiteLLM.Close()

	db := openTestDB(t)
	est := pricing.New(db)
	if err := est.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	before, err := db.ListPrices(context.Background())
	if err != nil {
		t.Fatalf("ListPrices: %v", err)
	}

	_, err = Sync(context.Background(), db, est, testFetchers, activateLiteLLM(t, db, notLiteLLM.URL))
	if err == nil {
		t.Fatal("expected an error for a non-LiteLLM upstream")
	}
	if !errors.Is(err, ErrUpstreamUnavailable) {
		t.Errorf("got %v, want an error wrapping ErrUpstreamUnavailable (the admin API and UI key off this)", err)
	}

	after, err := db.ListPrices(context.Background())
	if err != nil {
		t.Fatalf("ListPrices: %v", err)
	}
	if len(after) != len(before) {
		t.Errorf("prices changed on a failed sync: before=%d after=%d", len(before), len(after))
	}

	got, err := db.GetActiveProxyIntegration(context.Background())
	if err != nil || got == nil {
		t.Fatalf("GetActiveProxyIntegration: %v, %v", got, err)
	}
	if got.PriceSyncError == "" {
		t.Error("Sync's failure wasn't recorded in PriceSyncError")
	}
}

func TestSync_SuccessClearsAPriorFailure(t *testing.T) {
	notLiteLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer notLiteLLM.Close()
	litellmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[]}`))
	}))
	defer litellmSrv.Close()

	db := openTestDB(t)
	est := pricing.New(db)
	if err := est.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	if _, err := Sync(context.Background(), db, est, testFetchers, activateLiteLLM(t, db, notLiteLLM.URL)); err == nil {
		t.Fatal("expected the first sync (against a non-LiteLLM upstream) to fail")
	}
	if _, err := Sync(context.Background(), db, est, testFetchers, activateLiteLLM(t, db, litellmSrv.URL)); err != nil {
		t.Fatalf("expected the second sync (against a real LiteLLM upstream) to succeed: %v", err)
	}

	got, err := db.GetActiveProxyIntegration(context.Background())
	if err != nil || got == nil {
		t.Fatalf("GetActiveProxyIntegration: %v, %v", got, err)
	}
	if got.PriceSyncError != "" {
		t.Errorf("PriceSyncError = %q, want cleared after the subsequent successful sync", got.PriceSyncError)
	}
}

func TestSync_NonFetchFailureIsNotErrUpstreamUnavailable(t *testing.T) {
	litellmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"model_name":"m","model_info":{"input_cost_per_token":0.000001,"output_cost_per_token":0.000002}}]}`))
	}))
	defer litellmSrv.Close()

	db := openTestDB(t)
	est := pricing.New(db)
	if err := est.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	integration := activateLiteLLM(t, db, litellmSrv.URL)
	db.Close() // fetch succeeds, but the subsequent upsert now fails — a DB error, not a reachability one.

	_, err := Sync(context.Background(), db, est, testFetchers, integration)
	if err == nil {
		t.Fatal("expected an error once the DB is closed")
	}
	if errors.Is(err, ErrUpstreamUnavailable) {
		t.Errorf("got %v wrapping ErrUpstreamUnavailable, want it reserved for fetch failures only — "+
			"the admin API maps ErrUpstreamUnavailable to 502 specifically so the UI greys out its sync "+
			"button, which a mere DB error shouldn't trigger", err)
	}
}

func TestSyncDue(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name        string
		integration database.ProxyIntegration
		want        bool
	}{
		{
			name:        "fresh database is immediately due",
			integration: database.ProxyIntegration{PriceSyncIntervalMinutes: 60},
			want:        true,
		},
		{
			name: "recent success is not due",
			integration: database.ProxyIntegration{
				PriceSyncIntervalMinutes: 60,
				PriceSyncedAt:            float64(now.Add(-30 * time.Minute).Unix()),
			},
			want: false,
		},
		{
			name: "success past the interval is due again",
			integration: database.ProxyIntegration{
				PriceSyncIntervalMinutes: 60,
				PriceSyncedAt:            float64(now.Add(-61 * time.Minute).Unix()),
			},
			want: true,
		},
		{
			name: "recent failure is not due",
			integration: database.ProxyIntegration{
				PriceSyncIntervalMinutes: 60,
				PriceSyncAttemptedAt:     float64(now.Add(-1 * time.Minute).Unix()),
			},
			want: false,
		},
		{
			name: "failure past the interval is due again",
			integration: database.ProxyIntegration{
				PriceSyncIntervalMinutes: 60,
				PriceSyncAttemptedAt:     float64(now.Add(-61 * time.Minute).Unix()),
			},
			want: true,
		},
		{
			name: "a more recent failure than the last success still blocks retry",
			integration: database.ProxyIntegration{
				PriceSyncIntervalMinutes: 60,
				PriceSyncedAt:            float64(now.Add(-120 * time.Minute).Unix()),
				PriceSyncAttemptedAt:     float64(now.Add(-1 * time.Minute).Unix()),
			},
			want: false,
		},
		{
			name: "zero interval is never due",
			integration: database.ProxyIntegration{
				PriceSyncIntervalMinutes: 0,
				PriceSyncAttemptedAt:     float64(now.Add(-24 * time.Hour).Unix()),
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := syncDue(tt.integration, now); got != tt.want {
				t.Errorf("syncDue(%+v) = %v, want %v", tt.integration, got, tt.want)
			}
		})
	}
}

func TestRunLoop_SyncsImmediatelyWhenDueThenStopsOnCancel(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	db := openTestDB(t)
	est := pricing.New(db)
	if err := est.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	activateLiteLLM(t, db, srv.URL)
	registry, err := upstream.NewRegistry(context.Background(), db)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		RunLoop(ctx, db, est, testFetchers, registry)
		close(done)
	}()

	// A freshly activated integration has PriceSyncedAt == 0, so RunLoop's immediate
	// check-and-sync (before it ever waits on pollInterval) must fire
	// right away, without needing to wait out a real interval. hits is
	// written from the httptest server's own goroutine while this loop
	// polls it from the test goroutine, hence the atomic rather than a
	// plain int.
	deadline := time.After(2 * time.Second)
	for hits.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("RunLoop never synced within 2s of starting (a fresh DB should always be immediately due)")
		case <-time.After(5 * time.Millisecond):
		}
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunLoop didn't return after ctx cancel")
	}

	got, err := db.GetActiveProxyIntegration(context.Background())
	if err != nil || got == nil {
		t.Fatalf("GetActiveProxyIntegration: %v, %v", got, err)
	}
	if got.PriceSyncedAt == 0 {
		t.Error("RunLoop's sync didn't mark PriceSyncedAt")
	}
}

func TestRunLoop_ZeroIntervalNeverSyncs(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	db := openTestDB(t)
	est := pricing.New(db)
	if err := est.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	integration := activateLiteLLM(t, db, srv.URL)
	integration.PriceSyncIntervalMinutes = 0
	if err := db.UpdateProxyIntegration(context.Background(), integration); err != nil {
		t.Fatalf("UpdateProxyIntegration: %v", err)
	}
	registry, err := upstream.NewRegistry(context.Background(), db)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		RunLoop(ctx, db, est, testFetchers, registry)
		close(done)
	}()

	// Give the immediate check a moment to run (and, correctly, do nothing).
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunLoop didn't return after ctx cancel")
	}

	if hits != 0 {
		t.Errorf("got %d requests to the LiteLLM server, want 0 (sync interval disabled)", hits)
	}
}

func TestRunLoop_NoActiveIntegrationNeverSyncs(t *testing.T) {
	db := openTestDB(t)
	est := pricing.New(db)
	if err := est.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	registry, err := upstream.NewRegistry(context.Background(), db)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		RunLoop(ctx, db, est, testFetchers, registry)
		close(done)
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunLoop didn't return after ctx cancel")
	}
}
