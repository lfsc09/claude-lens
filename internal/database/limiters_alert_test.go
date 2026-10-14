package database

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
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

// addNotification registers an active Slack integration posting to url and
// returns its id.
func addNotification(t *testing.T, db *DB, url string) int64 {
	t.Helper()
	now := float64(time.Now().Unix())
	id, err := db.CreateNotificationIntegration(context.Background(), NotificationIntegration{
		Type: NotificationTypeSlack, Name: url, WebhookURL: url, Active: true, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("CreateNotificationIntegration: %v", err)
	}
	return id
}

// alertLimiter builds an active limiter for sessionID whose window ends in an
// hour, linked to the given notification links.
func alertLimiter(sessionID string, limit float64, links ...LimiterNotification) Limiter {
	now := time.Now()
	return Limiter{
		SessionID:     sessionID,
		LimitAmount:   limit,
		RefreshValue:  60,
		RefreshUnit:   "minutes",
		NextRefreshAt: float64(now.Add(time.Hour).Unix()),
		IsActive:      true,
		CreatedAt:     float64(now.Unix()),
		UpdatedAt:     float64(now.Unix()),
		Notifications: links,
	}
}

func firstLinkAlertSent(t *testing.T, db *DB, id int64) bool {
	t.Helper()
	got, err := db.GetLimiter(context.Background(), id)
	if err != nil {
		t.Fatalf("GetLimiter: %v", err)
	}
	if len(got.Notifications) != 1 {
		t.Fatalf("got %d notification links, want 1", len(got.Notifications))
	}
	return got.Notifications[0].AlertSent
}

func TestAccrueLimiterCost_BudgetAlertFiresOnceOnThresholdCross(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	server, count := newAlertServer(t)
	db.SetNotifications(notify.NewClient())

	id, err := db.CreateLimiter(ctx, alertLimiter("sess_alert", 1.0,
		LimiterNotification{NotificationID: addNotification(t, db, server.URL), ThresholdPct: intPtr(80)}))
	if err != nil {
		t.Fatalf("CreateLimiter: %v", err)
	}

	// Below the 80% threshold: no alert yet.
	saveExchangeWithCost(t, db, "sess_alert", 0.79)
	assertNoMoreAlerts(t, count, 0)

	// Crosses 80% of the $1.00 budget: exactly one alert fires.
	saveExchangeWithCost(t, db, "sess_alert", 0.05)
	waitForCount(t, count, 1)
	if !firstLinkAlertSent(t, db, id) {
		t.Error("AlertSent = false after crossing threshold, want true")
	}

	// Further accrual within the same window doesn't resend it.
	saveExchangeWithCost(t, db, "sess_alert", 0.05)
	assertNoMoreAlerts(t, count, 1)
}

// TestAccrueLimiterCost_BudgetAlertResetsOnRefresh confirms AlertSent is
// cleared when the window refreshes, so a limiter that crosses its threshold
// again in a later window gets a fresh alert instead of staying silenced
// forever.
func TestAccrueLimiterCost_BudgetAlertResetsOnRefresh(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	server, count := newAlertServer(t)
	db.SetNotifications(notify.NewClient())

	id, err := db.CreateLimiter(ctx, alertLimiter("sess_reset", 1.0,
		LimiterNotification{NotificationID: addNotification(t, db, server.URL), ThresholdPct: intPtr(50)}))
	if err != nil {
		t.Fatalf("CreateLimiter: %v", err)
	}

	// First window: crosses 50% and alerts once.
	saveExchangeWithCost(t, db, "sess_reset", 0.60)
	waitForCount(t, count, 1)
	if !firstLinkAlertSent(t, db, id) {
		t.Fatal("AlertSent = false after crossing threshold, want true")
	}

	// Force the window due again (as if its refresh boundary had passed).
	got, err := db.GetLimiter(ctx, id)
	if err != nil {
		t.Fatalf("GetLimiter: %v", err)
	}
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
	if !final.Notifications[0].AlertSent {
		t.Error("AlertSent = false after re-crossing threshold in the new window, want true")
	}
	if final.CurrentCost != 0.60 {
		t.Errorf("CurrentCost = %v, want 0.60 (reset by refresh, then accrued fresh)", final.CurrentCost)
	}
}

func TestAccrueLimiterCost_NoAlertWithoutLinks(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	_, count := newAlertServer(t)
	db.SetNotifications(notify.NewClient())

	if _, err := db.CreateLimiter(ctx, alertLimiter("sess_no_alert", 1.0)); err != nil {
		t.Fatalf("CreateLimiter: %v", err)
	}

	saveExchangeWithCost(t, db, "sess_no_alert", 1.0)
	assertNoMoreAlerts(t, count, 0)
}

// TestAccrueLimiterCost_RequestSpikeAlertFiresOverThreshold confirms a
// single exchange costing at least RequestCostUSD fires an alert, with no
// "already sent" gating — unlike the budget alert, this is expected to fire
// on every qualifying request.
func TestAccrueLimiterCost_RequestSpikeAlertFiresOverThreshold(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	server, count := newAlertServer(t)
	db.SetNotifications(notify.NewClient())

	if _, err := db.CreateLimiter(ctx, alertLimiter("sess_spike", 100.0,
		LimiterNotification{NotificationID: addNotification(t, db, server.URL), RequestCostUSD: floatPtr(1.0)})); err != nil {
		t.Fatalf("CreateLimiter: %v", err)
	}

	// Below the per-request threshold: no alert.
	saveExchangeWithCost(t, db, "sess_spike", 0.50)
	assertNoMoreAlerts(t, count, 0)

	// Meets the threshold: fires.
	saveExchangeWithCost(t, db, "sess_spike", 1.0)
	waitForCount(t, count, 1)

	// Fires again on a second qualifying request in the same window.
	saveExchangeWithCost(t, db, "sess_spike", 2.0)
	waitForCount(t, count, 2)
}

// TestAccrueLimiterCost_NoAlertWhenLimiterInactiveOrIntegrationInactive
// confirms alerts respect both the limiter's is-active gate and the
// notification integration's own active flag.
func TestAccrueLimiterCost_NoAlertWhenLimiterInactiveOrIntegrationInactive(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	server, count := newAlertServer(t)
	db.SetNotifications(notify.NewClient())

	inactive := alertLimiter("sess_spike_inactive", 100.0,
		LimiterNotification{NotificationID: addNotification(t, db, server.URL+"/a"), RequestCostUSD: floatPtr(1.0)})
	inactive.IsActive = false
	if _, err := db.CreateLimiter(ctx, inactive); err != nil {
		t.Fatalf("CreateLimiter: %v", err)
	}
	saveExchangeWithCost(t, db, "sess_spike_inactive", 50.0)
	assertNoMoreAlerts(t, count, 0)

	muted := addNotification(t, db, server.URL+"/b")
	if err := db.SetNotificationIntegrationActive(ctx, muted, false, float64(time.Now().Unix())); err != nil {
		t.Fatalf("SetNotificationIntegrationActive: %v", err)
	}
	if _, err := db.CreateLimiter(ctx, alertLimiter("sess_muted", 100.0,
		LimiterNotification{NotificationID: muted, RequestCostUSD: floatPtr(1.0)})); err != nil {
		t.Fatalf("CreateLimiter: %v", err)
	}
	saveExchangeWithCost(t, db, "sess_muted", 50.0)
	assertNoMoreAlerts(t, count, 0)
}

// TestAccrueLimiterCost_EachLinkAlertsIndependently confirms two links on
// one limiter each fire on their own threshold.
func TestAccrueLimiterCost_EachLinkAlertsIndependently(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	serverA, countA := newAlertServer(t)
	serverB, countB := newAlertServer(t)
	db.SetNotifications(notify.NewClient())

	if _, err := db.CreateLimiter(ctx, alertLimiter("sess_links", 1.0,
		LimiterNotification{NotificationID: addNotification(t, db, serverA.URL), ThresholdPct: intPtr(50)},
		LimiterNotification{NotificationID: addNotification(t, db, serverB.URL), ThresholdPct: intPtr(90)})); err != nil {
		t.Fatalf("CreateLimiter: %v", err)
	}

	saveExchangeWithCost(t, db, "sess_links", 0.60)
	waitForCount(t, countA, 1)
	assertNoMoreAlerts(t, countB, 0)

	saveExchangeWithCost(t, db, "sess_links", 0.35)
	waitForCount(t, countB, 1)
	assertNoMoreAlerts(t, countA, 1)
}

func TestDeleteNotificationIntegration_CascadesToLimiterLinks(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	nid := addNotification(t, db, "https://hooks.example.test/x")

	id, err := db.CreateLimiter(ctx, alertLimiter("sess_cascade", 1.0,
		LimiterNotification{NotificationID: nid, ThresholdPct: intPtr(50)}))
	if err != nil {
		t.Fatalf("CreateLimiter: %v", err)
	}
	if err := db.DeleteNotificationIntegration(ctx, nid); err != nil {
		t.Fatalf("DeleteNotificationIntegration: %v", err)
	}

	got, err := db.GetLimiter(ctx, id)
	if err != nil {
		t.Fatalf("GetLimiter: %v", err)
	}
	if len(got.Notifications) != 0 {
		t.Errorf("got %d links after deleting the integration, want 0", len(got.Notifications))
	}
}

func TestUpdateLimiter_ReplacesNotificationLinks(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	a := addNotification(t, db, "https://hooks.example.test/a")
	b := addNotification(t, db, "https://hooks.example.test/b")

	id, err := db.CreateLimiter(ctx, alertLimiter("sess_replace", 1.0,
		LimiterNotification{NotificationID: a, ThresholdPct: intPtr(50)}))
	if err != nil {
		t.Fatalf("CreateLimiter: %v", err)
	}
	l, err := db.GetLimiter(ctx, id)
	if err != nil {
		t.Fatalf("GetLimiter: %v", err)
	}
	l.Notifications = []LimiterNotification{{NotificationID: b, RequestCostUSD: floatPtr(2)}}
	if err := db.UpdateLimiter(ctx, *l); err != nil {
		t.Fatalf("UpdateLimiter: %v", err)
	}

	got, err := db.GetLimiter(ctx, id)
	if err != nil {
		t.Fatalf("GetLimiter: %v", err)
	}
	if len(got.Notifications) != 1 || got.Notifications[0].NotificationID != b || got.Notifications[0].ThresholdPct != nil {
		t.Errorf("got %+v, want only the link to integration %d", got.Notifications, b)
	}
}

// TestAccrueLimiterCost_ConcurrentCrossingsSendExactlyOneBudgetAlert drives
// accrueLimiterCost from many goroutines whose combined cost crosses the
// budget threshold, and asserts exactly one alert is sent — guarding against
// the read-check-write race on AlertSent. Run with -race.
func TestAccrueLimiterCost_ConcurrentCrossingsSendExactlyOneBudgetAlert(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	server, count := newAlertServer(t)
	db.SetNotifications(notify.NewClient())

	if _, err := db.CreateLimiter(ctx, alertLimiter("sess_concurrent", 1.0,
		LimiterNotification{NotificationID: addNotification(t, db, server.URL), ThresholdPct: intPtr(50)})); err != nil {
		t.Fatalf("CreateLimiter: %v", err)
	}

	const goroutines = 20
	var wg sync.WaitGroup
	var failures atomic.Int32
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			// Each accrual alone crosses the 50% threshold of the $1.00
			// budget, so every one of them independently observes the
			// crossing if the read-check-write isn't serialized.
			if err := db.SaveExchange(ctx, Exchange{
				SessionID:   "sess_concurrent",
				Path:        "/v1/messages",
				CreatedAt:   float64(time.Now().Unix()),
				RawRequest:  "{}",
				RawResponse: "{}",
				InputCost:   floatPtr(0.60),
			}); err != nil {
				failures.Add(1)
			}
		}()
	}
	wg.Wait()
	if n := failures.Load(); n != 0 {
		t.Fatalf("SaveExchange failed %d/%d times", n, goroutines)
	}

	waitForCount(t, count, 1)
	assertNoMoreAlerts(t, count, 1)
}

func TestApplicableLimiters_FlagsLimitersWithLinks(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	server, _ := newAlertServer(t)
	webhookID := addNotification(t, db, server.URL)

	pct := 50
	if _, err := db.CreateLimiter(ctx, alertLimiter("sess_linked", 1.0, LimiterNotification{NotificationID: webhookID, ThresholdPct: &pct})); err != nil {
		t.Fatalf("CreateLimiter linked: %v", err)
	}
	if _, err := db.CreateLimiter(ctx, alertLimiter("", 1.0)); err != nil {
		t.Fatalf("CreateLimiter global: %v", err)
	}

	limiters, err := db.applicableLimiters(ctx, "sess_linked")
	if err != nil {
		t.Fatalf("applicableLimiters: %v", err)
	}
	if len(limiters) != 2 {
		t.Fatalf("got %d limiters, want 2", len(limiters))
	}
	for _, l := range limiters {
		if want := l.SessionID == "sess_linked"; l.hasLinks != want {
			t.Errorf("limiter %q hasLinks = %v, want %v", l.SessionID, l.hasLinks, want)
		}
	}
}
