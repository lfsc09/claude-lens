package database

import (
	"context"
	"testing"
	"time"
)

// saveExchangeWithCost is a small SaveExchange fixture for limiter accrual
// tests, where the only thing that matters is that InputCost sums to cost.
func saveExchangeWithCost(t *testing.T, db *DB, sessionID string, cost float64) {
	t.Helper()
	if err := db.SaveExchange(context.Background(), Exchange{
		SessionID:   sessionID,
		Path:        "/v1/messages",
		Timestamp:   float64(time.Now().Unix()),
		RawRequest:  "{}",
		RawResponse: "{}",
		InputCost:   floatPtr(cost),
	}); err != nil {
		t.Fatalf("SaveExchange: %v", err)
	}
}

func TestCheckLimiters_AccrualAndBlocking(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := time.Now()

	limiter := Limiter{
		SessionID:     "sess_a",
		LimitAmount:   0.05,
		RefreshValue:  60,
		RefreshUnit:   "minutes",
		NextRefreshAt: float64(now.Add(time.Hour).Unix()),
		IsActive:      true,
		CreatedAt:     float64(now.Unix()),
		UpdatedAt:     float64(now.Unix()),
	}
	if _, err := db.CreateLimiter(ctx, limiter); err != nil {
		t.Fatalf("CreateLimiter: %v", err)
	}

	// Below the limit: not blocked yet.
	saveExchangeWithCost(t, db, "sess_a", 0.03)
	if blocked, _, err := db.CheckLimiters(ctx, "sess_a"); err != nil {
		t.Fatalf("CheckLimiters: %v", err)
	} else if blocked {
		t.Fatal("CheckLimiters reported blocked before the limit was reached")
	}

	// The exchange that pushes cumulative cost to/over the limit is never
	// itself blocked (cost is only known after it completes) — but every
	// request after it should be.
	saveExchangeWithCost(t, db, "sess_a", 0.03)
	blocked, by, err := db.CheckLimiters(ctx, "sess_a")
	if err != nil {
		t.Fatalf("CheckLimiters: %v", err)
	}
	if !blocked {
		t.Fatal("CheckLimiters did not report blocked once cumulative cost reached the limit")
	}
	if by == nil || by.SessionID != "sess_a" {
		t.Fatalf("CheckLimiters returned wrong limiter: %+v", by)
	}

	// A different session is unaffected by a session-scoped limiter.
	if blocked, _, err := db.CheckLimiters(ctx, "sess_b"); err != nil {
		t.Fatalf("CheckLimiters: %v", err)
	} else if blocked {
		t.Fatal("session-scoped limiter incorrectly blocked an unrelated session")
	}
}

func TestCheckLimiters_GlobalAndSessionGateIndependently(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := time.Now()

	global := Limiter{
		LimitAmount:   0.10,
		RefreshValue:  1,
		RefreshUnit:   "days",
		NextRefreshAt: float64(now.Add(24 * time.Hour).Unix()),
		IsActive:      true,
		CreatedAt:     float64(now.Unix()),
		UpdatedAt:     float64(now.Unix()),
	}
	scoped := Limiter{
		SessionID:     "sess_a",
		LimitAmount:   0.01,
		RefreshValue:  1,
		RefreshUnit:   "days",
		NextRefreshAt: float64(now.Add(24 * time.Hour).Unix()),
		IsActive:      true,
		CreatedAt:     float64(now.Unix()),
		UpdatedAt:     float64(now.Unix()),
	}
	if _, err := db.CreateLimiter(ctx, global); err != nil {
		t.Fatalf("CreateLimiter(global): %v", err)
	}
	if _, err := db.CreateLimiter(ctx, scoped); err != nil {
		t.Fatalf("CreateLimiter(scoped): %v", err)
	}

	// sess_a trips its own tight scoped limiter well before the loose global one.
	saveExchangeWithCost(t, db, "sess_a", 0.02)
	if blocked, by, err := db.CheckLimiters(ctx, "sess_a"); err != nil {
		t.Fatalf("CheckLimiters(sess_a): %v", err)
	} else if !blocked || by.SessionID != "sess_a" {
		t.Fatalf("expected sess_a blocked by its own scoped limiter, got blocked=%v by=%+v", blocked, by)
	}

	// sess_b has no scoped limiter, and hasn't pushed the global past 0.10 yet.
	if blocked, _, err := db.CheckLimiters(ctx, "sess_b"); err != nil {
		t.Fatalf("CheckLimiters(sess_b): %v", err)
	} else if blocked {
		t.Fatal("sess_b blocked before the global limiter reached its threshold")
	}

	// Push the global limiter's cumulative cost (sess_a's 0.02 + this) past 0.10.
	saveExchangeWithCost(t, db, "sess_b", 0.09)
	if blocked, by, err := db.CheckLimiters(ctx, "sess_b"); err != nil {
		t.Fatalf("CheckLimiters(sess_b): %v", err)
	} else if !blocked || by.SessionID != "" {
		t.Fatalf("expected sess_b blocked by the global limiter, got blocked=%v by=%+v", blocked, by)
	}
}

func TestWithinActivePeriod(t *testing.T) {
	tests := []struct {
		name       string
		start, end *int
		hour       int
		want       bool
	}{
		{"always active, midnight", nil, nil, 0, true},
		{"always active, noon", nil, nil, 12, true},
		{"normal range, start bound", intPtr(8), intPtr(12), 8, true},
		{"normal range, middle", intPtr(8), intPtr(12), 10, true},
		{"normal range, end bound", intPtr(8), intPtr(12), 12, true},
		{"normal range, before start", intPtr(8), intPtr(12), 7, false},
		{"normal range, after end", intPtr(8), intPtr(12), 13, false},
		{"single hour, match", intPtr(8), intPtr(8), 8, true},
		{"single hour, before", intPtr(8), intPtr(8), 7, false},
		{"single hour, after", intPtr(8), intPtr(8), 9, false},
		{"wraparound, in start segment", intPtr(22), intPtr(6), 23, true},
		{"wraparound, in end segment", intPtr(22), intPtr(6), 6, true},
		{"wraparound, before start segment", intPtr(22), intPtr(6), 21, false},
		{"wraparound, after end segment", intPtr(22), intPtr(6), 7, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := Limiter{ActiveStartHour: tt.start, ActiveEndHour: tt.end}
			now := time.Date(2026, 8, 19, tt.hour, 0, 0, 0, time.UTC)
			if got := withinActivePeriod(l, now); got != tt.want {
				t.Errorf("withinActivePeriod(%v-%v, hour=%d) = %v, want %v", tt.start, tt.end, tt.hour, got, tt.want)
			}
		})
	}
}

func TestHourMaskOverlap(t *testing.T) {
	tests := []struct {
		name         string
		aStart, aEnd *int
		bStart, bEnd *int
		wantOverlap  bool
	}{
		{"adjacent ranges don't overlap", intPtr(8), intPtr(12), intPtr(13), intPtr(19), false},
		{"overlapping ranges do", intPtr(8), intPtr(12), intPtr(10), intPtr(14), true},
		{"identical single hours overlap", intPtr(8), intPtr(8), intPtr(8), intPtr(8), true},
		{"adjacent single hours don't overlap", intPtr(8), intPtr(8), intPtr(9), intPtr(9), false},
		{"always-active overlaps a narrow range", nil, nil, intPtr(5), intPtr(5), true},
		{"always-active overlaps another always-active", nil, nil, nil, nil, true},
		{"wraparound overlaps a range it wraps into", intPtr(22), intPtr(6), intPtr(5), intPtr(9), true},
		{"wraparound doesn't overlap the untouched middle", intPtr(22), intPtr(6), intPtr(7), intPtr(21), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := hourMask(tt.aStart, tt.aEnd)
			b := hourMask(tt.bStart, tt.bEnd)
			got := a&b != 0
			if got != tt.wantOverlap {
				t.Errorf("hourMask(%v-%v) & hourMask(%v-%v) overlap = %v, want %v", tt.aStart, tt.aEnd, tt.bStart, tt.bEnd, got, tt.wantOverlap)
			}
		})
	}
}

func TestComputeNextRefreshAligned(t *testing.T) {
	// 2026-08-19 is a Wednesday; 2026-08-24 is the following Monday.
	wed := time.Date(2026, 8, 19, 15, 30, 0, 0, time.UTC)

	tests := []struct {
		name  string
		now   time.Time
		unit  string
		value int
		want  time.Time
	}{
		{"minutes=60 rounds to next top of hour", wed, "minutes", 60, time.Date(2026, 8, 19, 16, 0, 0, 0, time.UTC)},
		{"hours=1 rounds to next top of hour", wed, "hours", 1, time.Date(2026, 8, 19, 16, 0, 0, 0, time.UTC)},
		{"hours=1 exactly on the hour still advances", time.Date(2026, 8, 19, 16, 0, 0, 0, time.UTC), "hours", 1, time.Date(2026, 8, 19, 17, 0, 0, 0, time.UTC)},
		{"hours=24 rounds to next midnight", wed, "hours", 24, time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)},
		{"days=1 rounds to next midnight", wed, "days", 1, time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)},
		{"days=1 exactly at midnight still advances a full day", time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC), "days", 1, time.Date(2026, 8, 21, 0, 0, 0, 0, time.UTC)},
		{"days=7 rounds to next Monday", wed, "days", 7, time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)},
		{"days=7 on a Monday still rounds to the following Monday", time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC), "days", 7, time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)},
		{"months=1 rounds to the 1st of next month", wed, "months", 1, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)},
		{"months=1 crosses a year boundary", time.Date(2026, 12, 15, 10, 0, 0, 0, time.UTC), "months", 1, time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ComputeNextRefresh(tt.now, tt.unit, tt.value, true)
			if !got.Equal(tt.want) {
				t.Errorf("ComputeNextRefresh(%v, %q, %d, aligned=true) = %v, want %v", tt.now, tt.unit, tt.value, got, tt.want)
			}
		})
	}
}

func TestComputeNextRefreshUnaligned(t *testing.T) {
	now := time.Date(2026, 8, 19, 15, 30, 0, 0, time.UTC)

	tests := []struct {
		name  string
		unit  string
		value int
		want  time.Time
	}{
		{"minutes adds a fixed duration", "minutes", 45, now.Add(45 * time.Minute)},
		{"hours adds a fixed duration", "hours", 3, now.Add(3 * time.Hour)},
		{"days adds a fixed duration", "days", 10, now.Add(10 * 24 * time.Hour)},
		{"months uses calendar arithmetic", "months", 3, now.AddDate(0, 3, 0)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ComputeNextRefresh(now, tt.unit, tt.value, false)
			if !got.Equal(tt.want) {
				t.Errorf("ComputeNextRefresh(%v, %q, %d, aligned=false) = %v, want %v", now, tt.unit, tt.value, got, tt.want)
			}
		})
	}
}

// TestComputeNextRefreshMonthEndOverflow documents Go's standard calendar-month
// normalization (AddDate) rather than clamping to the last day of the target
// month: Jan 31 + 1 month overflows Feb 31 into Mar 3, since 2026 isn't a leap
// year.
func TestComputeNextRefreshMonthEndOverflow(t *testing.T) {
	now := time.Date(2026, 1, 31, 10, 0, 0, 0, time.UTC)
	want := time.Date(2026, 3, 3, 10, 0, 0, 0, time.UTC)
	got := ComputeNextRefresh(now, "months", 1, false)
	if !got.Equal(want) {
		t.Errorf("ComputeNextRefresh(%v, months, 1, aligned=false) = %v, want %v", now, got, want)
	}
}

// TestRefreshDueLimiters confirms the background-loop entry point resets
// only limiters whose next_refresh_at has already passed, leaving limiters
// not yet due untouched.
func TestRefreshDueLimiters(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := time.Now()

	due := Limiter{
		SessionID:     "sess_due",
		LimitAmount:   1.0,
		CurrentCost:   0.75,
		RefreshValue:  60,
		RefreshUnit:   "minutes",
		NextRefreshAt: float64(now.Add(-time.Minute).Unix()),
		IsActive:      true,
		CreatedAt:     float64(now.Unix()),
		UpdatedAt:     float64(now.Unix()),
	}
	dueID, err := db.CreateLimiter(ctx, due)
	if err != nil {
		t.Fatalf("CreateLimiter(due): %v", err)
	}

	notDue := Limiter{
		SessionID:     "sess_not_due",
		LimitAmount:   1.0,
		CurrentCost:   0.5,
		RefreshValue:  60,
		RefreshUnit:   "minutes",
		NextRefreshAt: float64(now.Add(time.Hour).Unix()),
		IsActive:      true,
		CreatedAt:     float64(now.Unix()),
		UpdatedAt:     float64(now.Unix()),
	}
	notDueID, err := db.CreateLimiter(ctx, notDue)
	if err != nil {
		t.Fatalf("CreateLimiter(notDue): %v", err)
	}

	n, err := db.RefreshDueLimiters(ctx)
	if err != nil {
		t.Fatalf("RefreshDueLimiters: %v", err)
	}
	if n != 1 {
		t.Fatalf("RefreshDueLimiters returned %d, want 1", n)
	}

	got, err := db.GetLimiter(ctx, dueID)
	if err != nil {
		t.Fatalf("GetLimiter(due): %v", err)
	}
	if got.CurrentCost != 0 {
		t.Errorf("due limiter CurrentCost = %v, want 0", got.CurrentCost)
	}
	if got.NextRefreshAt <= float64(now.Unix()) {
		t.Errorf("due limiter NextRefreshAt = %v, want in the future", got.NextRefreshAt)
	}

	stillNotDue, err := db.GetLimiter(ctx, notDueID)
	if err != nil {
		t.Fatalf("GetLimiter(notDue): %v", err)
	}
	if stillNotDue.CurrentCost != 0.5 {
		t.Errorf("not-due limiter CurrentCost = %v, want unchanged 0.5", stillNotDue.CurrentCost)
	}
	if stillNotDue.NextRefreshAt != notDue.NextRefreshAt {
		t.Errorf("not-due limiter NextRefreshAt = %v, want unchanged %v", stillNotDue.NextRefreshAt, notDue.NextRefreshAt)
	}
}

// TestTimeUntilNextMinute confirms the pure helper driving the background
// loop's re-alignment always lands strictly after now, never returning zero
// even when now sits exactly on a minute boundary.
func TestTimeUntilNextMinute(t *testing.T) {
	tests := []struct {
		name string
		now  time.Time
		want time.Duration
	}{
		{"exactly on the boundary", time.Date(2026, 8, 19, 10, 5, 0, 0, time.UTC), time.Minute},
		{"mid-minute", time.Date(2026, 8, 19, 10, 5, 30, 500_000_000, time.UTC), 29*time.Second + 500_000_000},
		{"one nanosecond after the boundary", time.Date(2026, 8, 19, 10, 5, 0, 1, time.UTC), time.Minute - 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := timeUntilNextMinute(tt.now); got != tt.want {
				t.Errorf("timeUntilNextMinute(%v) = %v, want %v", tt.now, got, tt.want)
			}
		})
	}
}
