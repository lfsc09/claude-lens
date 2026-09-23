package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"strings"
	"time"
)

// Exchange is a row to be written after a proxied request/response completes.
type Exchange struct {
	SessionID           string
	Path                string
	Timestamp           float64
	IsStreaming         bool
	InputMessages       *string
	RawRequest          string
	RawResponse         string
	OutputText          *string
	InputTokens         *int
	OutputTokens        *int
	CacheCreationTokens *int
	CacheReadTokens     *int
	Model               *string
	InputCost           *float64
	OutputCost          *float64
	CacheCreationCost   *float64
	CacheReadCost       *float64
	MatchedPrice        *string
}

// ExchangeSummary is a row as listed in the exchanges table/dashboard (no
// request/response bodies). JSON field names mirror the underlying SQL
// column names.
type ExchangeSummary struct {
	ID                  int64    `json:"id"`
	SessionID           string   `json:"session_id"`
	SessionName         *string  `json:"session_name"`
	Path                string   `json:"path"`
	Timestamp           float64  `json:"timestamp"`
	IsStreaming         bool     `json:"is_streaming"`
	InputTokens         *int     `json:"input_tokens"`
	OutputTokens        *int     `json:"output_tokens"`
	CacheCreationTokens *int     `json:"cache_creation_tokens"`
	CacheReadTokens     *int     `json:"cache_read_tokens"`
	Model               *string  `json:"model"`
	Cost                *float64 `json:"cost"`
	InputCost           *float64 `json:"input_cost"`
	OutputCost          *float64 `json:"output_cost"`
	CacheCreationCost   *float64 `json:"cache_creation_cost"`
	CacheReadCost       *float64 `json:"cache_read_cost"`
	TotalTokens         *int     `json:"total_tokens"`
}

// ExchangeDetail is a full exchange row, including request/response bodies.
type ExchangeDetail struct {
	ID                  int64    `json:"id"`
	SessionID           string   `json:"session_id"`
	SessionName         *string  `json:"session_name"`
	Path                string   `json:"path"`
	Timestamp           float64  `json:"timestamp"`
	IsStreaming         bool     `json:"is_streaming"`
	InputMessages       *string  `json:"input_messages"`
	OutputText          *string  `json:"output_text"`
	InputTokens         *int     `json:"input_tokens"`
	OutputTokens        *int     `json:"output_tokens"`
	CacheCreationTokens *int     `json:"cache_creation_tokens"`
	CacheReadTokens     *int     `json:"cache_read_tokens"`
	Model               *string  `json:"model"`
	Cost                *float64 `json:"cost"`
	InputCost           *float64 `json:"input_cost"`
	OutputCost          *float64 `json:"output_cost"`
	CacheCreationCost   *float64 `json:"cache_creation_cost"`
	CacheReadCost       *float64 `json:"cache_read_cost"`
	// MatchedPrice is the Price rule that was actually used to cost this
	// exchange, captured as JSON at save time — a permanent snapshot, not a
	// live lookup, so it stays accurate even after model_prices changes.
	MatchedPrice json.RawMessage `json:"matched_price,omitempty"`
	RawRequest   *string         `json:"raw_request"`
	RawResponse  *string         `json:"raw_response"`
}

// Totals is an aggregate over a set of exchanges.
type Totals struct {
	Count                    int64    `json:"count"`
	TotalInputTokens         int64    `json:"total_input_tokens"`
	TotalOutputTokens        int64    `json:"total_output_tokens"`
	TotalCacheCreationTokens int64    `json:"total_cache_creation_tokens"`
	TotalCacheReadTokens     int64    `json:"total_cache_read_tokens"`
	TotalCost                *float64 `json:"total_cost"`
	TotalInputCost           *float64 `json:"total_input_cost"`
	TotalOutputCost          *float64 `json:"total_output_cost"`
	TotalCacheCreationCost   *float64 `json:"total_cache_creation_cost"`
	TotalCacheReadCost       *float64 `json:"total_cache_read_cost"`
}

// SessionStat is a per-session aggregate row.
type SessionStat struct {
	SessionID                string   `json:"session_id"`
	SessionName              *string  `json:"session_name"`
	ExchangeCount            int64    `json:"exchange_count"`
	TotalInputTokens         int64    `json:"total_input_tokens"`
	TotalOutputTokens        int64    `json:"total_output_tokens"`
	TotalCacheCreationTokens int64    `json:"total_cache_creation_tokens"`
	TotalCacheReadTokens     int64    `json:"total_cache_read_tokens"`
	TotalCost                float64  `json:"total_cost"`
	TotalInputCost           *float64 `json:"total_input_cost"`
	TotalOutputCost          *float64 `json:"total_output_cost"`
	TotalCacheCreationCost   *float64 `json:"total_cache_creation_cost"`
	TotalCacheReadCost       *float64 `json:"total_cache_read_cost"`
	// ContextInputTokens, ContextOutputTokens, ContextCacheCreationTokens, and
	// ContextCacheReadTokens hold the last exchange's own token counts — see
	// sessionStatsCTEs for why that differs from the Total* sums above.
	ContextInputTokens         *int64 `json:"context_input_tokens"`
	ContextOutputTokens        *int64 `json:"context_output_tokens"`
	ContextCacheCreationTokens *int64 `json:"context_cache_creation_tokens"`
	ContextCacheReadTokens     *int64 `json:"context_cache_read_tokens"`
	// Model is the model used in the most exchanges within the session
	// (ties broken alphabetically), or nil if no exchange in the session
	// has a recorded model.
	Model       *string `json:"model"`
	LastUpdated float64 `json:"last_updated"`
}

// DailyCost is a daily cost total, local-time bucketed.
type DailyCost struct {
	Day       string  `json:"day"`
	DailyCost float64 `json:"daily_cost"`
}

// SaveExchange inserts one exchange row and its mirrored exchanges_ledger
// bookkeeping row in a single transaction, so the two never drift out of
// sync. On failure it logs the error and returns it; the caller decides
// whether that should affect a response already sent to the client.
func (db *DB) SaveExchange(ctx context.Context, e Exchange) error {
	var cost *float64
	if sum, any := sumCosts(e.InputCost, e.OutputCost, e.CacheCreationCost, e.CacheReadCost); any {
		c := math.Round(sum*1e6) / 1e6
		cost = &c
	}

	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		slog.Error("save exchange failed", "error", err, "session_id", e.SessionID)
		return err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx,
		`INSERT INTO exchanges
			(session_id, path, timestamp, is_streaming,
			 input_messages, output_text, input_tokens, output_tokens,
			 cache_creation_tokens, cache_read_tokens,
			 model, cost, input_cost, output_cost, cache_creation_cost, cache_read_cost, matched_price,
			 raw_request, raw_response)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.SessionID, e.Path, e.Timestamp, boolToInt(e.IsStreaming),
		e.InputMessages, e.OutputText, e.InputTokens, e.OutputTokens,
		e.CacheCreationTokens, e.CacheReadTokens,
		e.Model, cost, e.InputCost, e.OutputCost, e.CacheCreationCost, e.CacheReadCost, e.MatchedPrice,
		e.RawRequest, e.RawResponse,
	)
	if err != nil {
		slog.Error("save exchange failed", "error", err, "session_id", e.SessionID)
		return err
	}

	exchangeID, err := res.LastInsertId()
	if err != nil {
		slog.Error("save exchange failed", "error", err, "session_id", e.SessionID)
		return err
	}
	if err := insertLedgerEntry(ctx, tx, exchangeID, e, cost); err != nil {
		slog.Error("save exchange ledger entry failed", "error", err, "session_id", e.SessionID)
		return err
	}

	if err := tx.Commit(); err != nil {
		slog.Error("save exchange failed", "error", err, "session_id", e.SessionID)
		return err
	}

	if cost != nil {
		if err := db.accrueLimiterCost(ctx, e.SessionID, *cost, e.Model); err != nil {
			slog.Error("accrue limiter cost failed", "error", err, "session_id", e.SessionID)
		}
	}

	slog.Info("saved exchange",
		"session_id", e.SessionID, "path", e.Path, "model", deref(e.Model),
		"input_tokens", deref(e.InputTokens), "output_tokens", deref(e.OutputTokens),
		"cache_creation_tokens", deref(e.CacheCreationTokens), "cache_read_tokens", deref(e.CacheReadTokens),
		"cost", deref(cost), "streaming", e.IsStreaming,
	)
	return nil
}

// sumCosts adds together whichever of the given cost components are
// non-nil, returning ok == false only when none of them are set (so a row
// with no priced components at all still stores a NULL cost, matching the
// pre-existing input+output behavior, instead of a misleading 0).
func sumCosts(costs ...*float64) (sum float64, ok bool) {
	for _, c := range costs {
		if c != nil {
			sum += *c
			ok = true
		}
	}
	return sum, ok
}

// GetExchanges returns a page of exchange summaries, newest first, optionally
// filtered by a search-box query (see query_filter.go for the grammar). Pass
// filterQuery == "" for no filter.
func (db *DB) GetExchanges(ctx context.Context, filterQuery string, limit, offset int) ([]ExchangeSummary, error) {
	where, whereArgs, err := CompileExchangeFilter(filterQuery)
	if err != nil {
		return nil, err
	}

	query := `SELECT exchanges.id, exchanges.session_id, sn.name, exchanges.path, exchanges.timestamp, exchanges.is_streaming,
	                  exchanges.input_tokens, exchanges.output_tokens, exchanges.cache_creation_tokens, exchanges.cache_read_tokens,
	                  exchanges.model, exchanges.cost, exchanges.input_cost, exchanges.output_cost, exchanges.cache_creation_cost, exchanges.cache_read_cost,
	                  ` + totalTokensExpr + ` AS total_tokens
	           FROM exchanges
	           LEFT JOIN session_names sn ON sn.session_id = exchanges.session_id `
	args := []any{}
	if where != "" {
		query += "WHERE " + where + " "
		args = append(args, whereArgs...)
	}
	query += "ORDER BY id DESC LIMIT ? OFFSET ?"
	args = append(args, limit, offset)

	rows, err := db.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]ExchangeSummary, 0, limit)
	for rows.Next() {
		var e ExchangeSummary
		var isStreaming int
		if err := rows.Scan(&e.ID, &e.SessionID, &e.SessionName, &e.Path, &e.Timestamp,
			&isStreaming, &e.InputTokens, &e.OutputTokens, &e.CacheCreationTokens, &e.CacheReadTokens,
			&e.Model, &e.Cost, &e.InputCost, &e.OutputCost, &e.CacheCreationCost, &e.CacheReadCost,
			&e.TotalTokens); err != nil {
			return nil, err
		}
		e.IsStreaming = isStreaming != 0
		out = append(out, e)
	}
	return out, rows.Err()
}

// CountExchanges returns how many exchanges match filterQuery (the same
// grammar as GetExchanges), for computing pagination totals. Pass
// filterQuery == "" for no filter.
func (db *DB) CountExchanges(ctx context.Context, filterQuery string) (int, error) {
	where, whereArgs, err := CompileExchangeFilter(filterQuery)
	if err != nil {
		return 0, err
	}

	query := "SELECT COUNT(*) FROM exchanges LEFT JOIN session_names sn ON sn.session_id = exchanges.session_id "
	if where != "" {
		query += "WHERE " + where
	}

	var total int
	if err := db.sql.QueryRowContext(ctx, query, whereArgs...).Scan(&total); err != nil {
		return 0, err
	}
	return total, nil
}

// GetExchangeDetail returns a single exchange, or nil if id doesn't exist.
func (db *DB) GetExchangeDetail(ctx context.Context, id int64) (*ExchangeDetail, error) {
	var e ExchangeDetail
	var isStreaming int
	var matchedPrice sql.NullString
	err := db.sql.QueryRowContext(ctx,
		`SELECT exchanges.id, exchanges.session_id, sn.name, exchanges.path, exchanges.timestamp, exchanges.is_streaming,
		        input_messages, output_text, input_tokens, output_tokens,
		        cache_creation_tokens, cache_read_tokens,
		        model, cost, input_cost, output_cost, cache_creation_cost, cache_read_cost, matched_price,
		        raw_request, raw_response
		 FROM exchanges
		 LEFT JOIN session_names sn ON sn.session_id = exchanges.session_id
		 WHERE exchanges.id = ?`, id,
	).Scan(&e.ID, &e.SessionID, &e.SessionName, &e.Path, &e.Timestamp, &isStreaming,
		&e.InputMessages, &e.OutputText, &e.InputTokens, &e.OutputTokens,
		&e.CacheCreationTokens, &e.CacheReadTokens,
		&e.Model, &e.Cost, &e.InputCost, &e.OutputCost, &e.CacheCreationCost, &e.CacheReadCost, &matchedPrice,
		&e.RawRequest, &e.RawResponse)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	e.IsStreaming = isStreaming != 0
	if matchedPrice.Valid {
		e.MatchedPrice = json.RawMessage(matchedPrice.String)
	}
	return &e, nil
}

// GetTokenTotals returns aggregate token/cost counts, optionally scoped to a
// session (sessionID == "" for no filter) and/or a start timestamp. Reads
// from exchanges_ledger rather than exchanges so totals stay accurate after
// DeleteExchanges reclaims space from a session's raw payloads.
func (db *DB) GetTokenTotals(ctx context.Context, sessionID string, since *float64) (Totals, error) {
	query := `SELECT COUNT(*),
	                 SUM(input_tokens), SUM(output_tokens),
	                 SUM(cache_creation_tokens), SUM(cache_read_tokens),
	                 SUM(cost), SUM(input_cost), SUM(output_cost),
	                 SUM(cache_creation_cost), SUM(cache_read_cost)
	          FROM exchanges_ledger`
	var conditions []string
	var args []any
	if sessionID != "" {
		conditions = append(conditions, "session_id = ?")
		args = append(args, sessionID)
	}
	if since != nil {
		conditions = append(conditions, "timestamp >= ?")
		args = append(args, *since)
	}
	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}

	var t Totals
	var inputTokens, outputTokens, cacheCreationTokens, cacheReadTokens sql.NullInt64
	var totalCost, totalInputCost, totalOutputCost, totalCacheCreationCost, totalCacheReadCost sql.NullFloat64
	err := db.sql.QueryRowContext(ctx, query, args...).Scan(
		&t.Count, &inputTokens, &outputTokens, &cacheCreationTokens, &cacheReadTokens,
		&totalCost, &totalInputCost, &totalOutputCost, &totalCacheCreationCost, &totalCacheReadCost,
	)
	if err != nil {
		return Totals{}, err
	}
	t.TotalInputTokens = inputTokens.Int64
	t.TotalOutputTokens = outputTokens.Int64
	t.TotalCacheCreationTokens = cacheCreationTokens.Int64
	t.TotalCacheReadTokens = cacheReadTokens.Int64
	t.TotalCost = roundedOrNil(totalCost, 4)
	t.TotalInputCost = roundedOrNil(totalInputCost, 4)
	t.TotalOutputCost = roundedOrNil(totalOutputCost, 4)
	t.TotalCacheCreationCost = roundedOrNil(totalCacheCreationCost, 4)
	t.TotalCacheReadCost = roundedOrNil(totalCacheReadCost, 4)
	return t, nil
}

// sessionStatsColumns is the column list shared by GetSessionStats and
// GetSessionStatsSince — both aggregate the same shape, just scoped
// differently. The le.* columns are wrapped in MAX() only to satisfy
// GROUP BY e.session_id; last_exchange already joins 1:1 per session, so
// it's a no-op, not an actual aggregation.
const sessionStatsColumns = `e.session_id, MAX(sn.name),
		        COUNT(*),
		        COALESCE(SUM(e.input_tokens), 0), COALESCE(SUM(e.output_tokens), 0),
		        COALESCE(SUM(e.cache_creation_tokens), 0), COALESCE(SUM(e.cache_read_tokens), 0),
		        COALESCE(SUM(e.cost), 0), SUM(e.input_cost), SUM(e.output_cost),
		        SUM(e.cache_creation_cost), SUM(e.cache_read_cost),
		        MAX(le.input_tokens), MAX(le.output_tokens),
		        MAX(le.cache_creation_tokens), MAX(le.cache_read_tokens),
		        MAX(e.timestamp), MAX(tm.model)`

// sessionStatsCTEs supplies the two per-session lookups joined onto the
// GROUP BY e.session_id aggregate below. top_models ranks each session's
// models by exchange count (ties broken alphabetically for determinism) and
// keeps only the winner, so the dominant model can be surfaced without
// changing the aggregate's shape. last_exchange keeps only the
// most-recent-by-id row per session, so its token counts reflect the
// conversation's current size instead of a sum across every call in the
// session (each call resends the whole growing conversation, so summing
// token counts across calls double-counts earlier turns).
const sessionStatsCTEs = `WITH top_models AS (
		    SELECT session_id, model
		    FROM (
		        SELECT session_id, model,
		               ROW_NUMBER() OVER (PARTITION BY session_id ORDER BY COUNT(*) DESC, model ASC) AS rn
		        FROM exchanges
		        WHERE model IS NOT NULL
		        GROUP BY session_id, model
		    )
		    WHERE rn = 1
		), last_exchange AS (
		    SELECT session_id, input_tokens, output_tokens, cache_creation_tokens, cache_read_tokens
		    FROM (
		        SELECT session_id, input_tokens, output_tokens, cache_creation_tokens, cache_read_tokens,
		               ROW_NUMBER() OVER (PARTITION BY session_id ORDER BY id DESC) AS rn
		        FROM exchanges
		    )
		    WHERE rn = 1
		)`

// GetSessionStats returns a page of per-session aggregates, most recently
// active first.
func (db *DB) GetSessionStats(ctx context.Context, limit, offset int) ([]SessionStat, error) {
	rows, err := db.sql.QueryContext(ctx,
		sessionStatsCTEs+`
		 SELECT `+sessionStatsColumns+`
		 FROM exchanges e
		 LEFT JOIN top_models tm ON tm.session_id = e.session_id
		 LEFT JOIN last_exchange le ON le.session_id = e.session_id
		 LEFT JOIN session_names sn ON sn.session_id = e.session_id
		 GROUP BY e.session_id
		 ORDER BY MAX(e.timestamp) DESC
		 LIMIT ? OFFSET ?`,
		limit, offset,
	)
	if err != nil {
		return nil, err
	}
	return scanSessionStats(rows)
}

// CountSessionStats returns how many distinct sessions have logged at least
// one exchange, for the by-session table's pagination total.
func (db *DB) CountSessionStats(ctx context.Context) (int, error) {
	var total int
	err := db.sql.QueryRowContext(ctx, "SELECT COUNT(DISTINCT session_id) FROM exchanges").Scan(&total)
	return total, err
}

// GetSessionStatsSince returns per-session aggregates for exactly the
// sessions that logged an exchange with id > sinceID, most recently active
// first.
func (db *DB) GetSessionStatsSince(ctx context.Context, sinceID int64) ([]SessionStat, error) {
	rows, err := db.sql.QueryContext(ctx,
		sessionStatsCTEs+`
		 SELECT `+sessionStatsColumns+`
		 FROM exchanges e
		 LEFT JOIN top_models tm ON tm.session_id = e.session_id
		 LEFT JOIN last_exchange le ON le.session_id = e.session_id
		 LEFT JOIN session_names sn ON sn.session_id = e.session_id
		 WHERE e.session_id IN (SELECT DISTINCT session_id FROM exchanges WHERE id > ?)
		 GROUP BY e.session_id
		 ORDER BY MAX(e.timestamp) DESC`,
		sinceID,
	)
	if err != nil {
		return nil, err
	}
	return scanSessionStats(rows)
}

// scanSessionStats scans and closes rows produced by a sessionStatsColumns
// query.
func scanSessionStats(rows *sql.Rows) ([]SessionStat, error) {
	defer rows.Close()
	out := make([]SessionStat, 0)
	for rows.Next() {
		var s SessionStat
		var totalInputCost, totalOutputCost, totalCacheCreationCost, totalCacheReadCost sql.NullFloat64
		var contextInputTokens, contextOutputTokens, contextCacheCreationTokens, contextCacheReadTokens sql.NullInt64
		var model sql.NullString
		if err := rows.Scan(&s.SessionID, &s.SessionName, &s.ExchangeCount,
			&s.TotalInputTokens, &s.TotalOutputTokens,
			&s.TotalCacheCreationTokens, &s.TotalCacheReadTokens,
			&s.TotalCost, &totalInputCost, &totalOutputCost,
			&totalCacheCreationCost, &totalCacheReadCost,
			&contextInputTokens, &contextOutputTokens,
			&contextCacheCreationTokens, &contextCacheReadTokens,
			&s.LastUpdated, &model); err != nil {
			return nil, err
		}
		if totalInputCost.Valid {
			s.TotalInputCost = &totalInputCost.Float64
		}
		if totalOutputCost.Valid {
			s.TotalOutputCost = &totalOutputCost.Float64
		}
		if totalCacheCreationCost.Valid {
			s.TotalCacheCreationCost = &totalCacheCreationCost.Float64
		}
		if totalCacheReadCost.Valid {
			s.TotalCacheReadCost = &totalCacheReadCost.Float64
		}
		if contextInputTokens.Valid {
			s.ContextInputTokens = &contextInputTokens.Int64
		}
		if contextOutputTokens.Valid {
			s.ContextOutputTokens = &contextOutputTokens.Int64
		}
		if contextCacheCreationTokens.Valid {
			s.ContextCacheCreationTokens = &contextCacheCreationTokens.Int64
		}
		if contextCacheReadTokens.Valid {
			s.ContextCacheReadTokens = &contextCacheReadTokens.Int64
		}
		if model.Valid {
			s.Model = &model.String
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// GetDailyCosts returns daily cost totals for the last `days` days
// (local-time bucketed), oldest first. Reads from exchanges_ledger rather
// than exchanges so the heatmap stays accurate after DeleteExchanges
// reclaims space from a session's raw payloads.
func (db *DB) GetDailyCosts(ctx context.Context, days int) ([]DailyCost, error) {
	cutoff := float64(time.Now().AddDate(0, 0, -days).Unix())
	rows, err := db.sql.QueryContext(ctx,
		`SELECT date(timestamp, 'unixepoch', 'localtime') as day,
		        SUM(COALESCE(cost, 0))
		 FROM exchanges_ledger
		 WHERE timestamp >= ?
		 GROUP BY day
		 ORDER BY day`,
		cutoff,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []DailyCost
	for rows.Next() {
		var d DailyCost
		if err := rows.Scan(&d.Day, &d.DailyCost); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// sqlInClause returns len(ids) "?" placeholders joined by commas, plus the
// matching args slice, for building a `WHERE col IN (...)` clause.
func sqlInClause(ids []string) (string, []any) {
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return placeholders, args
}

// DeleteExchanges deletes exchanges rows and reports which sessions had rows
// deleted. A non-empty sessionIDs scopes the delete to those sessions; an
// empty sessionIDs deletes every exchange. The returned session IDs let
// callers also clean up Claude Code session files and session_names for
// exactly the sessions actually affected.
func (db *DB) DeleteExchanges(ctx context.Context, sessionIDs []string) (deletedRows int64, affectedSessions []string, err error) {
	selectQuery, deleteQuery := "SELECT DISTINCT session_id FROM exchanges", "DELETE FROM exchanges"
	var args []any
	if len(sessionIDs) > 0 {
		placeholders, inArgs := sqlInClause(sessionIDs)
		selectQuery += " WHERE session_id IN (" + placeholders + ")"
		deleteQuery += " WHERE session_id IN (" + placeholders + ")"
		args = inArgs
	}
	selectQuery += " ORDER BY session_id"

	rows, err := db.sql.QueryContext(ctx, selectQuery, args...)
	if err != nil {
		return 0, nil, err
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, nil, err
		}
		affectedSessions = append(affectedSessions, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, nil, err
	}
	rows.Close()

	if len(affectedSessions) == 0 {
		return 0, nil, nil
	}

	res, err := db.sql.ExecContext(ctx, deleteQuery, args...)
	if err != nil {
		slog.Error("delete exchanges failed", "error", err, "session_ids", sessionIDs)
		return 0, nil, err
	}
	deletedRows, err = res.RowsAffected()
	return deletedRows, affectedSessions, err
}

// DeleteSessionNames removes session_names rows for sessionIDs. Called
// alongside DeleteExchanges only when the caller also purges those sessions'
// on-disk Claude Code files, since a name is otherwise independent of the
// exchange log the same way exchanges_ledger survives DeleteExchanges.
func (db *DB) DeleteSessionNames(ctx context.Context, sessionIDs []string) error {
	if len(sessionIDs) == 0 {
		return nil
	}
	placeholders, args := sqlInClause(sessionIDs)
	_, err := db.sql.ExecContext(ctx, "DELETE FROM session_names WHERE session_id IN ("+placeholders+")", args...)
	return err
}

// SessionName returns a session's display name and whether one is set.
func (db *DB) SessionName(ctx context.Context, sessionID string) (name string, ok bool, err error) {
	err = db.sql.QueryRowContext(ctx, "SELECT name FROM session_names WHERE session_id = ?", sessionID).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return name, err == nil, err
}

// SetSessionName sets or clears a session's display name. An empty (after
// trimming) name removes the mapping entirely, reverting the session to
// showing its raw id.
func (db *DB) SetSessionName(ctx context.Context, sessionID, name string) error {
	if sessionID == "" {
		return errors.New("session_id is required")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		_, err := db.sql.ExecContext(ctx, "DELETE FROM session_names WHERE session_id = ?", sessionID)
		return err
	}
	_, err := db.sql.ExecContext(ctx,
		`INSERT INTO session_names (session_id, name, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT(session_id) DO UPDATE SET name = excluded.name, updated_at = excluded.updated_at`,
		sessionID, name, float64(time.Now().Unix()),
	)
	return err
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func roundedOrNil(v sql.NullFloat64, decimals int) *float64 {
	if !v.Valid {
		return nil
	}
	mult := math.Pow(10, float64(decimals))
	r := math.Round(v.Float64*mult) / mult
	return &r
}

// deref returns the value p points to, or T's zero value if p is nil.
func deref[T any](p *T) T {
	if p == nil {
		var zero T
		return zero
	}
	return *p
}
