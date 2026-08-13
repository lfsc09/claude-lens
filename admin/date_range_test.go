package admin

import (
	"testing"
	"time"
)

func TestNormalizeDashboardRange(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"today", "today"},
		{"this-week", "this-week"},
		{"this-month", "this-month"},
		{"last-7-days", "last-7-days"},
		{"last-15-days", "last-15-days"},
		{"last-30-days", "last-30-days"},
		{"last-60-days", "last-60-days"},
		{"", defaultDashboardRange},
		{"bogus", defaultDashboardRange},
		{"all-time", defaultDashboardRange},
	}
	for _, tt := range tests {
		if got := normalizeDashboardRange(tt.in); got != tt.want {
			t.Errorf("normalizeDashboardRange(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestSinceForRange fixes "now" at a known Wednesday (2026-08-12) so each
// range's expected lower bound can be hand-verified rather than re-deriving
// the same arithmetic under test.
func TestSinceForRange(t *testing.T) {
	now := time.Date(2026, 8, 12, 15, 30, 0, 0, time.Local) // Wednesday
	midnight := func(y int, m time.Month, d int) float64 {
		return float64(time.Date(y, m, d, 0, 0, 0, 0, time.Local).Unix())
	}

	tests := []struct {
		key  string
		want float64
	}{
		{"today", midnight(2026, 8, 12)},
		{"this-week", midnight(2026, 8, 9)},   // most recent Sunday
		{"this-month", midnight(2026, 8, 1)},  // 1st of the month
		{"last-7-days", midnight(2026, 8, 6)}, // today - 6, inclusive of today = 7 days
		{"last-15-days", midnight(2026, 7, 29)},
		{"last-30-days", midnight(2026, 7, 14)},
		{"last-60-days", midnight(2026, 6, 14)},
		{"unknown-key", midnight(2026, 8, 9)}, // falls back to default ("this-week")
	}
	for _, tt := range tests {
		if got := sinceForRange(tt.key, now); got != tt.want {
			t.Errorf("sinceForRange(%q, %v) = %v, want %v", tt.key, now, time.Unix(int64(got), 0), time.Unix(int64(tt.want), 0))
		}
	}
}

func TestSinceForRange_TodayIsSunday(t *testing.T) {
	// 2026-08-09 is itself a Sunday — "this-week" should resolve to that
	// same day's midnight, not the previous Sunday.
	now := time.Date(2026, 8, 9, 10, 0, 0, 0, time.Local)
	want := float64(time.Date(2026, 8, 9, 0, 0, 0, 0, time.Local).Unix())
	if got := sinceForRange("this-week", now); got != want {
		t.Errorf("sinceForRange(this-week) on a Sunday = %v, want %v", time.Unix(int64(got), 0), time.Unix(int64(want), 0))
	}
}
