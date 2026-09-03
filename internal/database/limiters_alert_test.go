package database

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lfsc09/claude-lens/internal/notify"
)

// newAlertServer spins up a test HTTP server standing in for a Slack
// incoming webhook, counting how many requests it receives.
func newAlertServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var count atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	return server, &count
}

// waitForCount polls got until it reaches want, or fails the test after 2s
// — accrueLimiterCost dispatches alerts from a background goroutine.
func waitForCount(t *testing.T, got *atomic.Int32, want int32) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got.Load() == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for alert count = %d, got %d", want, got.Load())
}

// assertNoMoreAlerts waits briefly and confirms got hasn't grown past want,
// for cases where there's nothing to poll for arriving.
func assertNoMoreAlerts(t *testing.T, got *atomic.Int32, want int32) {
	t.Helper()
	time.Sleep(200 * time.Millisecond)
	if n := got.Load(); n != want {
		t.Fatalf("alert count = %d, want %d (no further alerts)", n, want)
	}
}

func TestAccrueLimiterCost_BudgetAlertFiresOnceOnThresholdCross(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := time.Now()
	server, count := newAlertServer(t)
	db.SetNotifications(notify.NewClient(), server.URL)

	id, err := db.CreateLimiter(ctx, Limiter{
		SessionID:         "sess_alert",
		LimitAmount:       1.0,
		RefreshValue:      60,
		RefreshUnit:       "minutes",
		NextRefreshAt:     float64(now.Add(time.Hour).Unix()),
		IsActive:          true,
		CreatedAt:         float64(now.Unix()),
		UpdatedAt:         float64(now.Unix()),
		AlertThresholdPct: intPtr(80),
	})
	if err != nil {
		t.Fatalf("CreateLimiter: %v", err)
	}

	// Below the 80% threshold: no alert yet.
	saveExchangeWithCost(t, db, "sess_alert", 0.79)
	assertNoMoreAlerts(t, count, 0)

	// Crosses 80% of the $1.00 budget: exactly one alert fires.
	saveExchangeWithCost(t, db, "sess_alert", 0.05)
	waitForCount(t, count, 1)

	got, err := db.GetLimiter(ctx, id)
	if err != nil {
		t.Fatalf("GetLimiter: %v", err)
	}
	if !got.AlertSent {
		t.Error("AlertSent = false after crossing threshold, want true")
	}

	// Further accrual within the same window doesn't resend it.
	saveExchangeWithCost(t, db, "sess_alert", 0.05)
	assertNoMoreAlerts(t, count, 1)
}

// TestAccrueLimiterCost_BudgetAlertResetsOnRefresh confirms AlertSent is
// cleared by refreshIfDue, so a limiter that crosses its threshold again in
// a later window gets a fresh alert instead of staying silenced forever.
func TestAccrueLimiterCost_BudgetAlertResetsOnRefresh(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := time.Now()
	server, count := newAlertServer(t)
	db.SetNotifications(notify.NewClient(), server.URL)

	id, err := db.CreateLimiter(ctx, Limiter{
		SessionID:         "sess_reset",
		LimitAmount:       1.0,
		RefreshValue:      60,
		RefreshUnit:       "minutes",
		NextRefreshAt:     float64(now.Add(time.Hour).Unix()),
		IsActive:          true,
		CreatedAt:         float64(now.Unix()),
		UpdatedAt:         float64(now.Unix()),
		AlertThresholdPct: intPtr(50),
	})
	if err != nil {
		t.Fatalf("CreateLimiter: %v", err)
	}

	// First window: crosses 50% and alerts once.
	saveExchangeWithCost(t, db, "sess_reset", 0.60)
	waitForCount(t, count, 1)

	got, err := db.GetLimiter(ctx, id)
	if err != nil {
		t.Fatalf("GetLimiter: %v", err)
	}
	if !got.AlertSent {
		t.Fatal("AlertSent = false after crossing threshold, want true")
	}

	// Force the window due again (as if its refresh boundary had passed)
	// without touching AlertSent — that's refreshIfDue's job, not ours.
	got.NextRefreshAt = float64(time.Now().Add(-time.Minute).Unix())
	if err := db.UpdateLimiter(ctx, *got); err != nil {
		t.Fatalf("UpdateLimiter: %v", err)
	}

	// Re-crossing the threshold in the new window fires a second,
	// independent alert.
	saveExchangeWithCost(t, db, "sess_reset", 0.60)
	waitForCount(t, count, 2)

	final, err := db.GetLimiter(ctx, id)
	if err != nil {
		t.Fatalf("GetLimiter: %v", err)
	}
	if !final.AlertSent {
		t.Error("AlertSent = false after re-crossing threshold in the new window, want true")
	}
	if final.CurrentCost != 0.60 {
		t.Errorf("CurrentCost = %v, want 0.60 (reset by refresh, then accrued fresh)", final.CurrentCost)
	}
}

func TestAccrueLimiterCost_BudgetAlertPrefersLimiterWebhookOverDefault(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := time.Now()

	defaultServer, defaultCount := newAlertServer(t)
	limiterServer, limiterCount := newAlertServer(t)
	db.SetNotifications(notify.NewClient(), defaultServer.URL)

	_, err := db.CreateLimiter(ctx, Limiter{
		SessionID:         "sess_own_webhook",
		LimitAmount:       1.0,
		RefreshValue:      60,
		RefreshUnit:       "minutes",
		NextRefreshAt:     float64(now.Add(time.Hour).Unix()),
		IsActive:          true,
		CreatedAt:         float64(now.Unix()),
		UpdatedAt:         float64(now.Unix()),
		AlertThresholdPct: intPtr(50),
		SlackWebhookURL:   limiterServer.URL,
	})
	if err != nil {
		t.Fatalf("CreateLimiter: %v", err)
	}

	saveExchangeWithCost(t, db, "sess_own_webhook", 0.60)
	waitForCount(t, limiterCount, 1)
	assertNoMoreAlerts(t, defaultCount, 0)
}

func TestAccrueLimiterCost_NoAlertWhenThresholdUnset(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := time.Now()
	server, count := newAlertServer(t)
	db.SetNotifications(notify.NewClient(), server.URL)

	if _, err := db.CreateLimiter(ctx, Limiter{
		SessionID:     "sess_no_alert",
		LimitAmount:   1.0,
		RefreshValue:  60,
		RefreshUnit:   "minutes",
		NextRefreshAt: float64(now.Add(time.Hour).Unix()),
		IsActive:      true,
		CreatedAt:     float64(now.Unix()),
		UpdatedAt:     float64(now.Unix()),
		// AlertThresholdPct left nil: alerting disabled for this limiter.
	}); err != nil {
		t.Fatalf("CreateLimiter: %v", err)
	}

	saveExchangeWithCost(t, db, "sess_no_alert", 1.0)
	assertNoMoreAlerts(t, count, 0)
}
