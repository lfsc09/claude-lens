package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/lfsc09/claude-lens/internal/status"
)

// Limiter is a row of the limiters table: a cost cap that blocks proxied
// requests once CurrentCost reaches LimitAmount, until the next refresh.
// SessionID == "" scopes it to every session (global); any other value only
// gates requests carrying that sanitized session id. ActiveStartHour and
// ActiveEndHour are either both nil (always active) or both set (0-23,
// inclusive, server-local), wrapping past midnight when start > end.
type Limiter struct {
	ID              int64   `json:"id"`
	SessionID       string  `json:"session_id"`
	LimitAmount     float64 `json:"limit_amount"`
	CurrentCost     float64 `json:"current_cost"`
	RefreshValue    int     `json:"refresh_value"`
	RefreshUnit     string  `json:"refresh_unit"` // "minutes" | "hours" | "days" | "months"
	RefreshAligned  bool    `json:"refresh_aligned"`
	NextRefreshAt   float64 `json:"next_refresh_at"`
	ActiveStartHour *int    `json:"active_start_hour"`
	ActiveEndHour   *int    `json:"active_end_hour"`
	IsActive        bool    `json:"is_active"`
	CreatedAt       float64 `json:"created_at"`
	UpdatedAt       float64 `json:"updated_at"`

	// Notifications are the alert destinations this limiter reports to,
	// each with its own alert rules. Empty means the limiter never alerts.
	Notifications []LimiterNotification `json:"notifications"`

	// WithinActivePeriod reports whether ActiveStartHour/ActiveEndHour
	// currently covers the server's local time. Not persisted — callers that
	// return a Limiter to the client set it via WithinActivePeriodNow so the
	// UI can render "active now" using server time instead of the browser's.
	WithinActivePeriod bool `json:"within_active_period"`

	// hasLinks reports whether the limiter has any notification links; set
	// only by applicableLimiters.
	hasLinks bool
}

// LimiterNotification links a limiter to a notification integration with the
// alert rules that apply to that destination.
type LimiterNotification struct {
	NotificationID int64 `json:"notification_id"`
	// ThresholdPct, if set, fires a one-shot alert (see AlertSent) the first
	// time the limiter's CurrentCost reaches this percentage of LimitAmount
	// within the current refresh window. Nil disables the alert.
	ThresholdPct *int `json:"threshold_pct"`
	// RequestCostUSD, if set, fires an alert every time a single exchange
	// governed by the limiter costs at least this much. Unlike ThresholdPct
	// it has no "already sent" gating. Nil disables it.
	RequestCostUSD *float64 `json:"request_cost_usd"`
	// AlertSent marks that the ThresholdPct alert already fired for the
	// current refresh window. Reset whenever the window refreshes.
	AlertSent bool `json:"alert_sent"`
}

const limiterColumns = `id, session_id, limit_amount, current_cost, refresh_value, refresh_unit, refresh_aligned,
	next_refresh_at, active_start_hour, active_end_hour, is_active, created_at, updated_at`

// alignedRefreshCombos are the (refresh_unit, refresh_value) pairs with an
// unambiguous calendar boundary — the only ones RefreshAligned may be set
// for. See ComputeNextRefresh for what each boundary resolves to.
var alignedRefreshCombos = map[string][]int{
	"minutes": {60},
	"hours":   {1, 24},
	"days":    {1, 7},
	"months":  {1},
}

// SupportsAlignedRefresh reports whether unit/value is one of the six
// combos with an unambiguous calendar boundary.
func SupportsAlignedRefresh(unit string, value int) bool {
	return slices.Contains(alignedRefreshCombos[unit], value)
}

func scanLimiter(row interface{ Scan(dest ...any) error }) (Limiter, error) {
	var l Limiter
	err := scanLimiterInto(row, &l)
	return l, err
}

// scanLimiterInto scans a limiterColumns row into l, followed by any extra
// destinations for columns the query appends.
func scanLimiterInto(row interface{ Scan(dest ...any) error }, l *Limiter, extra ...any) error {
	var aligned, active int
	dest := append([]any{&l.ID, &l.SessionID, &l.LimitAmount, &l.CurrentCost, &l.RefreshValue, &l.RefreshUnit, &aligned,
		&l.NextRefreshAt, &l.ActiveStartHour, &l.ActiveEndHour, &active, &l.CreatedAt, &l.UpdatedAt}, extra...)
	err := row.Scan(dest...)
	l.RefreshAligned = aligned != 0
	l.IsActive = active != 0
	l.Notifications = []LimiterNotification{}
	return err
}

// collectLimiters scans every row into a Limiter and closes rows. extra, if
// set, returns the scan destinations for columns the query appends after
// limiterColumns.
func collectLimiters(rows *sql.Rows, extra func(*Limiter) []any) ([]Limiter, error) {
	defer rows.Close()

	var limiters []Limiter
	for rows.Next() {
		var l Limiter
		var dest []any
		if extra != nil {
			dest = extra(&l)
		}
		if err := scanLimiterInto(rows, &l, dest...); err != nil {
			return nil, err
		}
		limiters = append(limiters, l)
	}
	return limiters, rows.Err()
}

// ListLimiters returns every limiter, grouped by session (global first)
// and then by active-period start hour.
func (db *DB) ListLimiters(ctx context.Context) ([]Limiter, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT `+limiterColumns+` FROM limiters ORDER BY session_id, active_start_hour`)
	if err != nil {
		return nil, err
	}
	limiters, err := collectLimiters(rows, nil)
	if err != nil {
		return nil, err
	}
	return limiters, db.attachNotifications(ctx, limiters)
}

// attachNotifications fills each limiter's Notifications from
// limiter_notifications in a single query over the given limiters.
func (db *DB) attachNotifications(ctx context.Context, limiters []Limiter) error {
	if len(limiters) == 0 {
		return nil
	}
	byID := make(map[int64]*Limiter, len(limiters))
	ids := make([]any, 0, len(limiters))
	for i := range limiters {
		byID[limiters[i].ID] = &limiters[i]
		ids = append(ids, limiters[i].ID)
	}
	rows, err := db.sql.QueryContext(ctx,
		`SELECT limiter_id, notification_id, threshold_pct, request_cost_usd, alert_sent FROM limiter_notifications
		 WHERE limiter_id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+`) ORDER BY notification_id`, ids...)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var limiterID int64
		var n LimiterNotification
		var sent int
		if err := rows.Scan(&limiterID, &n.NotificationID, &n.ThresholdPct, &n.RequestCostUSD, &sent); err != nil {
			return err
		}
		n.AlertSent = sent != 0
		if l, ok := byID[limiterID]; ok {
			l.Notifications = append(l.Notifications, n)
		}
	}
	return rows.Err()
}

// GetLimiter returns the limiter with the given id, or nil if it doesn't
// exist.
func (db *DB) GetLimiter(ctx context.Context, id int64) (*Limiter, error) {
	row := db.sql.QueryRowContext(ctx, `SELECT `+limiterColumns+` FROM limiters WHERE id = ?`, id)
	l, err := scanLimiter(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	single := []Limiter{l}
	if err := db.attachNotifications(ctx, single); err != nil {
		return nil, err
	}
	return &single[0], nil
}

// CreateLimiter inserts a new limiter with its notification links and
// returns its id.
func (db *DB) CreateLimiter(ctx context.Context, l Limiter) (int64, error) {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx,
		`INSERT INTO limiters (session_id, limit_amount, current_cost, refresh_value, refresh_unit, refresh_aligned,
			next_refresh_at, active_start_hour, active_end_hour, is_active, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		l.SessionID, l.LimitAmount, l.CurrentCost, l.RefreshValue, l.RefreshUnit, boolToInt(l.RefreshAligned),
		l.NextRefreshAt, l.ActiveStartHour, l.ActiveEndHour, boolToInt(l.IsActive), l.CreatedAt, l.UpdatedAt,
	)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if err := replaceLimiterNotifications(ctx, tx, id, l.Notifications); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// UpdateLimiter replaces a limiter's rule fields and notification links in
// place. IsActive is left untouched — that's SetLimiterActive's job, kept
// separate so toggling never resets progress the way a full edit does.
func (db *DB) UpdateLimiter(ctx context.Context, l Limiter) error {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`UPDATE limiters SET session_id = ?, limit_amount = ?, current_cost = ?, refresh_value = ?, refresh_unit = ?,
			refresh_aligned = ?, next_refresh_at = ?, active_start_hour = ?, active_end_hour = ?, updated_at = ?
		 WHERE id = ?`,
		l.SessionID, l.LimitAmount, l.CurrentCost, l.RefreshValue, l.RefreshUnit, boolToInt(l.RefreshAligned),
		l.NextRefreshAt, l.ActiveStartHour, l.ActiveEndHour, l.UpdatedAt, l.ID,
	); err != nil {
		return err
	}
	if err := replaceLimiterNotifications(ctx, tx, l.ID, l.Notifications); err != nil {
		return err
	}
	return tx.Commit()
}

// replaceLimiterNotifications overwrites limiterID's links with links.
func replaceLimiterNotifications(ctx context.Context, tx *sql.Tx, limiterID int64, links []LimiterNotification) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM limiter_notifications WHERE limiter_id = ?`, limiterID); err != nil {
		return err
	}
	for _, n := range links {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO limiter_notifications (limiter_id, notification_id, threshold_pct, request_cost_usd, alert_sent)
			 VALUES (?, ?, ?, ?, ?)`,
			limiterID, n.NotificationID, n.ThresholdPct, n.RequestCostUSD, boolToInt(n.AlertSent),
		); err != nil {
			return err
		}
	}
	return nil
}

// SetLimiterActive toggles a limiter's master switch without touching its
// accrued progress.
func (db *DB) SetLimiterActive(ctx context.Context, id int64, active bool, updatedAt float64) error {
	_, err := db.sql.ExecContext(ctx, `UPDATE limiters SET is_active = ?, updated_at = ? WHERE id = ?`, boolToInt(active), updatedAt, id)
	return err
}

// DeleteLimiter removes a limiter by id. It is not an error if the id does
// not exist.
func (db *DB) DeleteLimiter(ctx context.Context, id int64) error {
	_, err := db.sql.ExecContext(ctx, "DELETE FROM limiters WHERE id = ?", id)
	return err
}

// FindOverlappingLimiter returns a limiter sharing sessionID whose active
// period overlaps [startHour, endHour], or nil if none does. excludeID
// skips a row being updated (pass 0 when creating). Global (session_id ==
// "") and per-session limiters are separate groups — this only ever
// compares within the group named by sessionID.
func (db *DB) FindOverlappingLimiter(ctx context.Context, sessionID string, excludeID int64, startHour, endHour *int) (*Limiter, error) {
	rows, err := db.sql.QueryContext(ctx,
		`SELECT `+limiterColumns+` FROM limiters WHERE session_id = ? AND id != ?`,
		sessionID, excludeID,
	)
	if err != nil {
		return nil, err
	}
	peers, err := collectLimiters(rows, nil)
	if err != nil {
		return nil, err
	}

	mask := hourMask(startHour, endHour)
	for i := range peers {
		if mask&hourMask(peers[i].ActiveStartHour, peers[i].ActiveEndHour) != 0 {
			return &peers[i], nil
		}
	}
	return nil, nil
}

// hourMask returns the bitmask of hours (bit i set means hour i is
// covered) for a start/end pair. Both nil means every hour. Handles the
// overnight wraparound case (start > end) and the single-hour case
// (start == end).
func hourMask(startHour, endHour *int) uint32 {
	if startHour == nil || endHour == nil {
		return 0xFFFFFF
	}
	start, end := *startHour, *endHour
	var mask uint32
	if start <= end {
		for h := start; h <= end; h++ {
			mask |= 1 << uint(h)
		}
	} else {
		for h := start; h <= 23; h++ {
			mask |= 1 << uint(h)
		}
		for h := 0; h <= end; h++ {
			mask |= 1 << uint(h)
		}
	}
	return mask
}

// withinActivePeriod reports whether now falls inside l's active-period
// hour range (server-local time). Both bounds nil means always active;
// otherwise the range is inclusive on both ends and wraps past midnight
// when start > end.
func withinActivePeriod(l Limiter, now time.Time) bool {
	return hourMask(l.ActiveStartHour, l.ActiveEndHour)&(1<<uint(now.Hour())) != 0
}

// WithinActivePeriodNow reports whether l's active-period hour range covers
// the current server-local time. Exported for API responses (see
// Limiter.WithinActivePeriod) so the client renders "active now" using
// server time rather than replicating this check in the browser's own
// timezone.
func WithinActivePeriodNow(l Limiter) bool {
	return withinActivePeriod(l, time.Now())
}

// ComputeNextRefresh returns the next refresh boundary for a limiter's
// refresh settings, strictly after now. Aligned mode is only meaningful for
// the six combos in alignedRefreshCombos (validated at the API layer); any
// other (unit, value, aligned=true) combination falls back to a fixed
// interval here rather than guessing at a boundary.
func ComputeNextRefresh(now time.Time, unit string, value int, aligned bool) time.Time {
	if aligned {
		switch {
		case unit == "minutes" && value == 60, unit == "hours" && value == 1:
			return now.Truncate(time.Hour).Add(time.Hour)
		case unit == "hours" && value == 24, unit == "days" && value == 1:
			y, m, d := now.Date()
			return time.Date(y, m, d, 0, 0, 0, 0, now.Location()).AddDate(0, 0, 1)
		case unit == "days" && value == 7:
			y, m, d := now.Date()
			midnight := time.Date(y, m, d, 0, 0, 0, 0, now.Location())
			daysUntilMonday := (int(time.Monday-midnight.Weekday()) + 7) % 7
			if daysUntilMonday == 0 {
				daysUntilMonday = 7
			}
			return midnight.AddDate(0, 0, daysUntilMonday)
		case unit == "months" && value == 1:
			y, m, _ := now.Date()
			return time.Date(y, m+1, 1, 0, 0, 0, 0, now.Location())
		}
	}
	switch unit {
	case "minutes":
		return now.Add(time.Duration(value) * time.Minute)
	case "hours":
		return now.Add(time.Duration(value) * time.Hour)
	case "days":
		return now.Add(time.Duration(value) * 24 * time.Hour)
	case "months":
		return now.AddDate(0, value, 0)
	default:
		return now.Add(time.Duration(value) * time.Minute)
	}
}

// applyDueRefresh zeroes l's cost, advances its refresh boundary and re-arms
// its one-shot threshold alerts in one transaction, when now has reached
// l.NextRefreshAt. The update only applies while the stored boundary still
// equals l.NextRefreshAt; otherwise another refresh already ran and l is
// reloaded from the database, so a stale refresh never wipes newer accruals.
func (db *DB) applyDueRefresh(ctx context.Context, l *Limiter, now time.Time) error {
	if float64(now.Unix()) < l.NextRefreshAt {
		return nil
	}
	next := float64(ComputeNextRefresh(now, l.RefreshUnit, l.RefreshValue, l.RefreshAligned).Unix())

	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("refresh limiter %d: %w", l.ID, err)
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx,
		`UPDATE limiters SET current_cost = 0, next_refresh_at = ?, updated_at = ? WHERE id = ? AND next_refresh_at = ?`,
		next, float64(now.Unix()), l.ID, l.NextRefreshAt)
	if err != nil {
		return fmt.Errorf("refresh limiter %d: %w", l.ID, err)
	}
	refreshed, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("refresh limiter %d: %w", l.ID, err)
	}
	if refreshed == 0 {
		err := tx.QueryRowContext(ctx, `SELECT current_cost, next_refresh_at FROM limiters WHERE id = ?`, l.ID).
			Scan(&l.CurrentCost, &l.NextRefreshAt)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("reload limiter %d: %w", l.ID, err)
		}
		return nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE limiter_notifications SET alert_sent = 0 WHERE limiter_id = ?`, l.ID); err != nil {
		return fmt.Errorf("re-arm limiter %d alerts: %w", l.ID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("refresh limiter %d: %w", l.ID, err)
	}
	l.CurrentCost = 0
	l.NextRefreshAt = next
	return nil
}

// applicableLimiters returns every limiter that governs sessionID: the
// global limiter(s) (session_id == "") plus any scoped to sessionID itself.
func (db *DB) applicableLimiters(ctx context.Context, sessionID string) ([]Limiter, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT `+limiterColumns+`,
		EXISTS (SELECT 1 FROM limiter_notifications WHERE limiter_id = limiters.id)
		FROM limiters WHERE session_id = ? OR session_id = ''`, sessionID)
	if err != nil {
		return nil, err
	}
	return collectLimiters(rows, func(l *Limiter) []any { return []any{&l.hasLinks} })
}

// RefreshDueLimiters resets every limiter whose next refresh boundary has
// already passed, independent of any proxied request reaching it. Mirrors
// the lazy refresh applied in CheckLimiters/accrueLimiterCost — including
// running regardless of IsActive, so a paused limiter still gets a clean
// window whenever it's re-enabled — but is driven by RunLimiterRefreshLoop
// instead of request traffic, so an idle limiter recovers on schedule even
// with nothing hitting the proxy. Returns how many limiters were refreshed.
func (db *DB) RefreshDueLimiters(ctx context.Context) (int, error) {
	now := time.Now()
	rows, err := db.sql.QueryContext(ctx, `SELECT `+limiterColumns+` FROM limiters WHERE next_refresh_at <= ?`, float64(now.Unix()))
	if err != nil {
		return 0, err
	}
	due, err := collectLimiters(rows, nil)
	if err != nil {
		return 0, err
	}

	for i := range due {
		if err := db.applyDueRefresh(ctx, &due[i], now); err != nil {
			return i, err
		}
	}
	return len(due), nil
}

// timeUntilNextMinute returns the duration until the next minute boundary
// strictly after now.
func timeUntilNextMinute(now time.Time) time.Duration {
	return now.Truncate(time.Minute).Add(time.Minute).Sub(now)
}

// RunLimiterRefreshLoop refreshes due limiters at the start of every minute
// until ctx is done, so a limiter recovers on schedule even when no proxied
// request arrives to trigger the lazy refresh in CheckLimiters/
// accrueLimiterCost. fresh is bumped whenever a limiter actually changes, so
// the admin SSE stream can push clients a refetch signal even though no
// exchange was saved. Meant to be run in its own goroutine for the lifetime
// of the process.
func (db *DB) RunLimiterRefreshLoop(ctx context.Context, fresh *status.Fresh) {
	logger := slog.Default().With("component", "database")

	timer := time.NewTimer(timeUntilNextMinute(time.Now()))
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if n, err := db.RefreshDueLimiters(ctx); err != nil {
				logger.Error("refresh due limiters", "error", err)
			} else if n > 0 {
				logger.Info("refreshed due limiters", "count", n)
				fresh.Bump()
			}
			timer.Reset(timeUntilNextMinute(time.Now()))
		}
	}
}

// refreshApplicableLimiters returns every limiter governing sessionID (see
// applicableLimiters), lazily applying and persisting any refresh boundary
// already due. Shared by CheckLimiters and accrueLimiterCost so a due
// refresh is applied identically regardless of which one observes it first.
func (db *DB) refreshApplicableLimiters(ctx context.Context, sessionID string) ([]Limiter, error) {
	limiters, err := db.applicableLimiters(ctx, sessionID)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	for i := range limiters {
		if err := db.applyDueRefresh(ctx, &limiters[i], now); err != nil {
			return nil, err
		}
	}
	return limiters, nil
}

// CheckLimiters reports whether a request for sessionID should be blocked:
// true if any applicable, active, currently-in-window limiter has already
// reached its LimitAmount. Refresh boundaries due by now are applied (and
// persisted) as a side effect, so a limiter that was due to reset gets a
// fresh window before being judged.
func (db *DB) CheckLimiters(ctx context.Context, sessionID string) (bool, *Limiter, error) {
	limiters, err := db.refreshApplicableLimiters(ctx, sessionID)
	if err != nil {
		return false, nil, err
	}

	now := time.Now()
	for i := range limiters {
		l := &limiters[i]
		if l.IsActive && withinActivePeriod(*l, now) && l.CurrentCost >= l.LimitAmount {
			blocked := *l
			return true, &blocked, nil
		}
	}
	return false, nil, nil
}

// alertTarget is one active notification link of a limiter, joined with its
// destination.
type alertTarget struct {
	LimiterNotification
	WebhookURL string
}

// alertTargets returns limiterID's links whose notification integration is
// active.
func (db *DB) alertTargets(ctx context.Context, limiterID int64) ([]alertTarget, error) {
	rows, err := db.sql.QueryContext(ctx,
		`SELECT ln.notification_id, ln.threshold_pct, ln.request_cost_usd, ln.alert_sent, n.webhook_url
		 FROM limiter_notifications ln
		 JOIN notification_integrations n ON n.id = ln.notification_id
		 WHERE ln.limiter_id = ? AND n.active = 1`, limiterID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var targets []alertTarget
	for rows.Next() {
		var t alertTarget
		var sent int
		if err := rows.Scan(&t.NotificationID, &t.ThresholdPct, &t.RequestCostUSD, &sent, &t.WebhookURL); err != nil {
			return nil, err
		}
		t.AlertSent = sent != 0
		targets = append(targets, t)
	}
	return targets, rows.Err()
}

// accrueLimiterCost adds cost to every applicable, active, currently-in-
// window limiter's running total, and fires whichever alerts (best-effort,
// asynchronous — see sendAlert) this accrual newly triggers for each of the
// limiter's active notification links: a budget-threshold alert the moment
// its ThresholdPct is crossed, and a request-spike alert whenever this
// single exchange's cost meets its RequestCostUSD. A limiter that is
// inactive or outside its active period right now simply doesn't track this
// request, or alert on it, at all.
//
// Each limiter's total is incremented in a single UPDATE and a threshold
// alert is claimed by flipping alert_sent from 0 to 1, so concurrent accruals
// neither lose cost nor send a budget alert twice.
func (db *DB) accrueLimiterCost(ctx context.Context, sessionID string, cost float64, model *string) error {
	limiters, err := db.refreshApplicableLimiters(ctx, sessionID)
	if err != nil {
		return err
	}

	now := time.Now()
	for i := range limiters {
		l := &limiters[i]
		if !l.IsActive || !withinActivePeriod(*l, now) {
			continue
		}

		err := db.sql.QueryRowContext(ctx,
			`UPDATE limiters SET current_cost = current_cost + ?, updated_at = ? WHERE id = ? RETURNING current_cost`,
			cost, float64(now.Unix()), l.ID).Scan(&l.CurrentCost)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return fmt.Errorf("accrue limiter %d: %w", l.ID, err)
		}
		previousCost := l.CurrentCost - cost
		if db.notifier == nil || !l.hasLinks {
			continue
		}

		targets, err := db.alertTargets(ctx, l.ID)
		if err != nil {
			return fmt.Errorf("load limiter %d alert targets: %w", l.ID, err)
		}
		for _, t := range targets {
			if t.ThresholdPct != nil && !t.AlertSent && l.LimitAmount > 0 {
				threshold := l.LimitAmount * float64(*t.ThresholdPct) / 100
				if l.CurrentCost >= threshold && previousCost < threshold {
					res, err := db.sql.ExecContext(ctx,
						`UPDATE limiter_notifications SET alert_sent = 1 WHERE limiter_id = ? AND notification_id = ? AND alert_sent = 0`,
						l.ID, t.NotificationID)
					if err != nil {
						return fmt.Errorf("mark limiter %d alert sent: %w", l.ID, err)
					}
					if claimed, _ := res.RowsAffected(); claimed == 1 {
						db.sendAlert(t.WebhookURL, budgetAlertText(*l, *t.ThresholdPct), *l)
					}
				}
			}
			if t.RequestCostUSD != nil && cost >= *t.RequestCostUSD {
				db.sendAlert(t.WebhookURL, requestSpikeAlertText(*l, cost, *t.RequestCostUSD, model), *l)
			}
		}
	}
	return nil
}

// sendAlert posts text to webhookURL in its own goroutine with a background
// context: delivery failures are logged, never propagated, so a slow or
// unreachable endpoint can't delay the proxied request or the exchange save
// that triggered this accrual.
func (db *DB) sendAlert(webhookURL, text string, l Limiter) {
	go func() {
		if err := db.notifier.Send(context.Background(), webhookURL, text); err != nil {
			slog.Error("alert delivery failed", "error", err, "limiter_id", l.ID, "session_id", l.SessionID)
		}
	}()
}

// budgetAlertText formats the message body for a limiter crossing its alert
// threshold.
func budgetAlertText(l Limiter, pct int) string {
	return fmt.Sprintf(":warning: claude-lens: %s limiter has spent $%.2f of its $%.2f budget (%d%% threshold reached)",
		limiterScope(l), l.CurrentCost, l.LimitAmount, pct)
}

// requestSpikeAlertText formats the message body for a single exchange
// costing at least threshold.
func requestSpikeAlertText(l Limiter, cost, threshold float64, model *string) string {
	m := "unknown model"
	if model != nil {
		m = *model
	}
	return fmt.Sprintf(":rotating_light: claude-lens: a single request cost $%.2f (%s, %s) — over the $%.2f alert threshold",
		cost, m, limiterScope(l), threshold)
}

// limiterScope renders l's session_id as "global" or "session <id>" for
// alert text.
func limiterScope(l Limiter) string {
	if l.SessionID == "" {
		return "global"
	}
	return "session " + l.SessionID
}
