package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/lfsc09/claude-lens/internal/parsing"
)

// Exchange is a row to be written after a proxied request/response completes.
type Exchange struct {
	SessionID           string
	Path                string
	CreatedAt           float64
	IsStreaming         bool
	RawRequest          string
	RawResponse         string
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
	SessionName         string   `json:"session_name"`
	Path                string   `json:"path"`
	CreatedAt           float64  `json:"created_at"`
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
	SessionName         string   `json:"session_name"`
	Path                string   `json:"path"`
	CreatedAt           float64  `json:"created_at"`
	IsStreaming         bool     `json:"is_streaming"`
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
	RawRequest   string          `json:"raw_request"`
	RawResponse  string          `json:"raw_response"`
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
	SessionName              string   `json:"session_name"`
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

// SaveExchange inserts one exchange row, its exchange_data payload and the
// owning sessions row (created on a session's first exchange) in a single
// transaction. On failure it logs the error and returns it; the caller
// decides whether that should affect a response already sent to the client.
func (db *DB) SaveExchange(ctx context.Context, e Exchange) error {
	var cost *float64
	if sum, any := sumCosts(e.InputCost, e.OutputCost, e.CacheCreationCost, e.CacheReadCost); any {
		c := math.Round(sum*1e6) / 1e6
		cost = &c
	}

	if err := db.insertExchange(ctx, e, cost); err != nil {
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

// insertExchange writes the sessions, exchanges and exchange_data rows for e
// atomically.
func (db *DB) insertExchange(ctx context.Context, e Exchange, cost *float64) error {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO sessions (session_id, name, updated_at) VALUES (?, ?, ?)`,
		e.SessionID, e.SessionID, e.CreatedAt,
	); err != nil {
		return fmt.Errorf("insert session: %w", err)
	}

	res, err := tx.ExecContext(ctx,
		`INSERT INTO exchanges
			(session_id, path, created_at, is_streaming, model,
			 input_tokens, output_tokens, cache_creation_tokens, cache_read_tokens,
			 cost, input_cost, output_cost, cache_creation_cost, cache_read_cost, matched_price)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.SessionID, e.Path, e.CreatedAt, boolToInt(e.IsStreaming), e.Model,
		e.InputTokens, e.OutputTokens, e.CacheCreationTokens, e.CacheReadTokens,
		cost, e.InputCost, e.OutputCost, e.CacheCreationCost, e.CacheReadCost, e.MatchedPrice,
	)
	if err != nil {
		return fmt.Errorf("insert exchange: %w", err)
	}
	exchangeID, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("exchange id: %w", err)
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO exchange_data (exchange_id, raw_request, raw_response) VALUES (?, ?, ?)`,
		exchangeID, e.RawRequest, e.RawResponse,
	); err != nil {
		return fmt.Errorf("insert exchange data: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// sumCosts adds together whichever of the given cost components are
// non-nil, returning ok == false only when none of them are set (so a row
// with no priced components at all stores a NULL cost instead of a
// misleading 0).
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

	query := `SELECT exchanges.id, exchanges.session_id, sessions.name, exchanges.path, exchanges.created_at, exchanges.is_streaming,
	                  exchanges.input_tokens, exchanges.output_tokens, exchanges.cache_creation_tokens, exchanges.cache_read_tokens,
	                  exchanges.model, exchanges.cost, exchanges.input_cost, exchanges.output_cost, exchanges.cache_creation_cost, exchanges.cache_read_cost,
	                  ` + totalTokensExpr + ` AS total_tokens
	           FROM exchanges
	           JOIN sessions ON sessions.session_id = exchanges.session_id
	           WHERE exchanges.deleted_at IS NULL `
	args := []any{}
	if where != "" {
		query += "AND (" + where + ") "
		args = append(args, whereArgs...)
	}
	query += "ORDER BY exchanges.id DESC LIMIT ? OFFSET ?"
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
		if err := rows.Scan(&e.ID, &e.SessionID, &e.SessionName, &e.Path, &e.CreatedAt,
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

	query := "SELECT COUNT(*) FROM exchanges JOIN sessions ON sessions.session_id = exchanges.session_id WHERE exchanges.deleted_at IS NULL"
	if where != "" {
		query += " AND (" + where + ")"
	}

	var total int
	if err := db.sql.QueryRowContext(ctx, query, whereArgs...).Scan(&total); err != nil {
		return 0, err
	}
	return total, nil
}

// GetExchangeDetail returns a single exchange with its request/response
// bodies and the output text assembled from the response, or nil if id
// doesn't exist or its data was deleted.
func (db *DB) GetExchangeDetail(ctx context.Context, id int64) (*ExchangeDetail, error) {
	var e ExchangeDetail
	var isStreaming int
	var matchedPrice sql.NullString
	err := db.sql.QueryRowContext(ctx,
		`SELECT exchanges.id, exchanges.session_id, sessions.name, exchanges.path, exchanges.created_at, exchanges.is_streaming,
		        input_tokens, output_tokens, cache_creation_tokens, cache_read_tokens,
		        model, cost, input_cost, output_cost, cache_creation_cost, cache_read_cost, matched_price,
		        raw_request, raw_response
		 FROM exchanges
		 JOIN sessions ON sessions.session_id = exchanges.session_id
		 JOIN exchange_data ON exchange_data.exchange_id = exchanges.id
		 WHERE exchanges.id = ? AND exchanges.deleted_at IS NULL`, id,
	).Scan(&e.ID, &e.SessionID, &e.SessionName, &e.Path, &e.CreatedAt, &isStreaming,
		&e.InputTokens, &e.OutputTokens, &e.CacheCreationTokens, &e.CacheReadTokens,
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
	e.OutputText, _ = parsing.ExtractResponseFields([]byte(e.RawResponse), e.IsStreaming)
	return &e, nil
}

// GetTokenTotals returns aggregate token/cost counts, optionally scoped to a
// session (sessionID == "" for no filter) and/or a start time. Includes
// soft-deleted exchanges so totals stay accurate after DeleteExchanges.
func (db *DB) GetTokenTotals(ctx context.Context, sessionID string, since *float64) (Totals, error) {
	query := `SELECT COUNT(*),
	                 SUM(input_tokens), SUM(output_tokens),
	                 SUM(cache_creation_tokens), SUM(cache_read_tokens),
	                 SUM(cost), SUM(input_cost), SUM(output_cost),
	                 SUM(cache_creation_cost), SUM(cache_read_cost)
	          FROM exchanges`
	var conditions []string
	var args []any
	if sessionID != "" {
		conditions = append(conditions, "session_id = ?")
		args = append(args, sessionID)
	}
	if since != nil {
		conditions = append(conditions, "created_at >= ?")
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
const sessionStatsColumns = `e.session_id, sessions.name,
		        COUNT(*),
		        COALESCE(SUM(e.input_tokens), 0), COALESCE(SUM(e.output_tokens), 0),
		        COALESCE(SUM(e.cache_creation_tokens), 0), COALESCE(SUM(e.cache_read_tokens), 0),
		        COALESCE(SUM(e.cost), 0), SUM(e.input_cost), SUM(e.output_cost),
		        SUM(e.cache_creation_cost), SUM(e.cache_read_cost),
		        MAX(le.input_tokens), MAX(le.output_tokens),
		        MAX(le.cache_creation_tokens), MAX(le.cache_read_tokens),
		        MAX(e.created_at), MAX(tm.model)`

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
		        WHERE model IS NOT NULL AND deleted_at IS NULL
		        GROUP BY session_id, model
		    )
		    WHERE rn = 1
		), last_exchange AS (
		    SELECT session_id, input_tokens, output_tokens, cache_creation_tokens, cache_read_tokens
		    FROM (
		        SELECT session_id, input_tokens, output_tokens, cache_creation_tokens, cache_read_tokens,
		               ROW_NUMBER() OVER (PARTITION BY session_id ORDER BY id DESC) AS rn
		        FROM exchanges
		        WHERE deleted_at IS NULL
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
		 JOIN sessions ON sessions.session_id = e.session_id
		 WHERE e.deleted_at IS NULL
		 GROUP BY e.session_id
		 ORDER BY MAX(e.created_at) DESC
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
	err := db.sql.QueryRowContext(ctx, "SELECT COUNT(DISTINCT session_id) FROM exchanges WHERE deleted_at IS NULL").Scan(&total)
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
		 JOIN sessions ON sessions.session_id = e.session_id
		 WHERE e.deleted_at IS NULL
		   AND e.session_id IN (SELECT DISTINCT session_id FROM exchanges WHERE id > ?)
		 GROUP BY e.session_id
		 ORDER BY MAX(e.created_at) DESC`,
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
// (local-time bucketed), oldest first. Includes soft-deleted exchanges so the
// heatmap stays accurate after DeleteExchanges.
func (db *DB) GetDailyCosts(ctx context.Context, days int) ([]DailyCost, error) {
	cutoff := float64(time.Now().AddDate(0, 0, -days).Unix())
	rows, err := db.sql.QueryContext(ctx,
		`SELECT date(created_at, 'unixepoch', 'localtime') as day,
		        SUM(COALESCE(cost, 0))
		 FROM exchanges
		 WHERE created_at >= ?
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

// DeleteExchanges soft-deletes exchanges, hard-deletes their exchange_data,
// and reports which sessions had exchanges deleted. A non-empty sessionIDs
// scopes the delete to those sessions; an empty sessionIDs deletes every
// exchange. The returned session IDs let callers also clean up Claude Code
// session files and sessions rows for exactly the sessions actually affected.
func (db *DB) DeleteExchanges(ctx context.Context, sessionIDs []string) (deletedRows int64, affectedSessions []string, err error) {
	scope, args := "", []any(nil)
	if len(sessionIDs) > 0 {
		placeholders, inArgs := sqlInClause(sessionIDs)
		scope, args = " AND session_id IN ("+placeholders+")", inArgs
	}

	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return 0, nil, err
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx,
		"SELECT DISTINCT session_id FROM exchanges WHERE deleted_at IS NULL"+scope+" ORDER BY session_id", args...)
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

	if _, err := tx.ExecContext(ctx,
		"DELETE FROM exchange_data WHERE exchange_id IN (SELECT id FROM exchanges WHERE deleted_at IS NULL"+scope+")", args...); err != nil {
		slog.Error("delete exchange data failed", "error", err, "session_ids", sessionIDs)
		return 0, nil, err
	}
	res, err := tx.ExecContext(ctx,
		"UPDATE exchanges SET deleted_at = ? WHERE deleted_at IS NULL"+scope,
		append([]any{float64(time.Now().UnixNano()) / 1e9}, args...)...)
	if err != nil {
		slog.Error("delete exchanges failed", "error", err, "session_ids", sessionIDs)
		return 0, nil, err
	}
	if deletedRows, err = res.RowsAffected(); err != nil {
		return 0, nil, err
	}
	if err := tx.Commit(); err != nil {
		return 0, nil, err
	}
	return deletedRows, affectedSessions, nil
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
