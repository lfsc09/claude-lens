package admin

import "time"

// defaultDashboardRange matches the dashboard template's default-selected
// <option> ("This week"), so an omitted/unrecognized ?range= falls back to
// the same window a first-time visitor sees.
const defaultDashboardRange = "this-week"

// dashboardRanges are the accepted values for the dashboard's "range" query
// param (the <select id="predefined-date-filter"> in dashboard.html) and
// the SSE stream's matching ?range= param.
var dashboardRanges = map[string]bool{
	"today":        true,
	"this-week":    true,
	"this-month":   true,
	"last-7-days":  true,
	"last-15-days": true,
	"last-30-days": true,
	"last-60-days": true,
}

// normalizeDashboardRange returns key if it's a recognized range, else the
// default — so a missing or tampered-with ?range= value falls back safely
// instead of erroring or (worse) silently querying unbounded all-time data.
func normalizeDashboardRange(key string) string {
	if dashboardRanges[key] {
		return key
	}
	return defaultDashboardRange
}

// sinceForRange resolves a dashboard range key to a Unix-seconds lower bound
// for a "timestamp >= since" totals query, computed against `now` in local
// time (matching fmtTime's local rendering elsewhere in the admin UI). Every
// range's upper bound is implicitly "now" — the picker has no way to choose
// a past end date — so a single lower bound is enough; no upper-bound param
// is needed on GetTokenTotals.
func sinceForRange(key string, now time.Time) float64 {
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	switch key {
	case "today":
		return float64(midnight.Unix())
	case "this-week":
		return float64(midnight.AddDate(0, 0, -int(midnight.Weekday())).Unix())
	case "this-month":
		return float64(time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()).Unix())
	case "last-7-days":
		return float64(midnight.AddDate(0, 0, -6).Unix())
	case "last-15-days":
		return float64(midnight.AddDate(0, 0, -14).Unix())
	case "last-30-days":
		return float64(midnight.AddDate(0, 0, -29).Unix())
	case "last-60-days":
		return float64(midnight.AddDate(0, 0, -59).Unix())
	default:
		return sinceForRange(defaultDashboardRange, now)
	}
}
