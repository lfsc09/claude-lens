package database

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
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
	for _, v := range alignedRefreshCombos[unit] {
		if v == value {
			return true
		}
	}
	return false
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

// ListLimiters returns every limiter, grouped by session (global first)
// and then by active-period start hour.
func (db *DB) ListLimiters(ctx context.Context) ([]Limiter, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT `+limiterColumns+` FROM limiters ORDER BY session_id, active_start_hour`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var limiters []Limiter
	for rows.Next() {
		l, err := scanLimiter(rows)
		if err != nil {
			return nil, err
		}
		limiters = append(limiters, l)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	return limiters, db.attachNotifications(ctx, limiters)
}

// attachNotifications fills each limiter's Notifications from
// limiter_notifications in a single query.
func (db *DB) attachNotifications(ctx context.Context, limiters []Limiter) error {
	if len(limiters) == 0 {
		return nil
	}
	rows, err := db.sql.QueryContext(ctx,
		`SELECT limiter_id, notification_id, threshold_pct, request_cost_usd, alert_sent FROM limiter_notifications ORDER BY notification_id`)
	if err != nil {
		return err
	}
	defer rows.Close()

	byID := make(map[int64]*Limiter, len(limiters))
	for i := range limiters {
		byID[limiters[i].ID] = &limiters[i]
	}
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
	if err == sql.ErrNoRows {
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
	defer rows.Close()

	mask := hourMask(startHour, endHour)
	for rows.Next() {
		l, err := scanLimiter(rows)
		if err != nil {
			return nil, err
		}
		if mask&hourMask(l.ActiveStartHour, l.ActiveEndHour) != 0 {
			return &l, nil
		}
	}
	return nil, rows.Err()
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
	if l.ActiveStartHour == nil || l.ActiveEndHour == nil {
		return true
	}
	hour := now.Hour()
	start, end := *l.ActiveStartHour, *l.ActiveEndHour
	if start <= end {
		return hour >= start && hour <= end
	}
	return hour >= start || hour <= end
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

// refreshIfDue zeroes CurrentCost and recomputes NextRefreshAt when now has
// reached it, returning whether it fired. The caller is responsible for
// persisting the change (see applyDueRefresh).
func refreshIfDue(l *Limiter, now time.Time) bool {
	if float64(now.Unix()) < l.NextRefreshAt {
		return false
	}
	l.CurrentCost = 0
	l.NextRefreshAt = float64(ComputeNextRefresh(now, l.RefreshUnit, l.RefreshValue, l.RefreshAligned).Unix())
	return true
}

// applyDueRefresh runs refreshIfDue on l and, when it fired, persists the
// new window and re-arms the limiter's one-shot threshold alerts in one
// transaction.
func (db *DB) applyDueRefresh(ctx context.Context, l *Limiter, now time.Time) error {
	if !refreshIfDue(l, now) {
		return nil
	}
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("refresh limiter %d: %w", l.ID, err)
	}
	defer tx.Rollback()

	if err := persistLimiterProgress(ctx, tx, *l, float64(now.Unix())); err != nil {
		return fmt.Errorf("refresh limiter %d: %w", l.ID, err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE limiter_notifications SET alert_sent = 0 WHERE limiter_id = ?`, l.ID); err != nil {
		return fmt.Errorf("re-arm limiter %d alerts: %w", l.ID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("refresh limiter %d: %w", l.ID, err)
	}
	return nil
}

// persistLimiterProgress writes back a limiter's current_cost and
// next_refresh_at after refreshIfDue and/or an accrual.
func persistLimiterProgress(ctx context.Context, e interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}, l Limiter, updatedAt float64) error {
	_, err := e.ExecContext(ctx,
		`UPDATE limiters SET current_cost = ?, next_refresh_at = ?, updated_at = ? WHERE id = ?`,
		l.CurrentCost, l.NextRefreshAt, updatedAt, l.ID,
	)
	return err
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
	defer rows.Close()

	var limiters []Limiter
	for rows.Next() {
		var l Limiter
		if err := scanLimiterInto(rows, &l, &l.hasLinks); err != nil {
			return nil, err
		}
		limiters = append(limiters, l)
	}
	return limiters, rows.Err()
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
	var due []Limiter
	for rows.Next() {
		l, err := scanLimiter(rows)
		if err != nil {
			rows.Close()
			return 0, err
		}
		due = append(due, l)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	rows.Close()

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
// Locked on alertMu for its whole body: two exchanges completing close
// together for the same limiter must not both observe AlertSent == false
// and send a duplicate budget alert.
func (db *DB) accrueLimiterCost(ctx context.Context, sessionID string, cost float64, model *string) error {
	db.alertMu.Lock()
	defer db.alertMu.Unlock()

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

		previousCost := l.CurrentCost
		l.CurrentCost += cost
		if err := persistLimiterProgress(ctx, db.sql, *l, float64(now.Unix())); err != nil {
			return fmt.Errorf("accrue limiter %d: %w", l.ID, err)
		}
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
					if _, err := db.sql.ExecContext(ctx,
						`UPDATE limiter_notifications SET alert_sent = 1 WHERE limiter_id = ? AND notification_id = ?`,
						l.ID, t.NotificationID); err != nil {
						return fmt.Errorf("mark limiter %d alert sent: %w", l.ID, err)
					}
					db.sendAlert(t.WebhookURL, budgetAlertText(*l, *t.ThresholdPct), *l)
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
