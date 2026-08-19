package admin

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/lfsc09/claude-lens/internal/database"
	"github.com/lfsc09/claude-lens/internal/files"
	"github.com/lfsc09/claude-lens/internal/pricing"
	"github.com/lfsc09/claude-lens/internal/status"
)

// handlers holds the dependencies shared by every admin route.
type handlers struct {
	db      *database.DB
	est     *pricing.Estimator
	status  *status.Flag
	fresh   *status.Fresh
	logger  *slog.Logger
	version string
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Default().Warn("writeJSON: write response", "error", err)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func (h *handlers) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "ok",
		"proxy":   string(h.status.Get()),
		"version": h.version,
	})
}

// exchangesResponse wraps a page of exchanges with the total count matching
// the current filter (for pagination) and, when the filter is exactly one
// `session = "..."` condition, the extracted session id — the query
// grammar (query_filter.go) is business logic that belongs server-side, so
// the frontend doesn't need its own copy just to decide whether a delete
// action is session-scoped or global.
type exchangesResponse struct {
	Rows      []database.ExchangeSummary `json:"rows"`
	Total     int                        `json:"total"`
	SessionID string                     `json:"session_id,omitempty"`
	// SessionActive is only meaningful alongside SessionID: it's true when
	// that session had a proxied request within claudeSessionActiveWindow,
	// which the UI uses to warn before deleting its on-disk Claude Code
	// files out from under a still-open terminal.
	SessionActive bool `json:"session_active,omitempty"`
}

// claudeSessionActiveWindow is how recent a session's last proxied request
// must be for the delete dialog to warn that it might still be open in a
// terminal.
const claudeSessionActiveWindow = 30 * time.Minute

func (h *handlers) listExchanges(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")

	limit := 100
	offset := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "limit and offset must be integers")
			return
		}
		limit = n
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "limit and offset must be integers")
			return
		}
		offset = n
	}
	if limit > 1000 {
		limit = 1000
	}

	rows, err := h.db.GetExchanges(r.Context(), q, limit, offset)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if rows == nil {
		rows = []database.ExchangeSummary{}
	}
	total, err := h.db.CountExchanges(r.Context(), q)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	sessionID, _ := database.ExtractExactSession(q)
	var sessionActive bool
	if sessionID != "" {
		sessionActive, err = h.db.SessionActiveWithin(r.Context(), sessionID, claudeSessionActiveWindow, time.Now())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, exchangesResponse{Rows: rows, Total: total, SessionID: sessionID, SessionActive: sessionActive})
}

func (h *handlers) exchangeDetail(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}

	row, err := h.db.GetExchangeDetail(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if row == nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	writeJSON(w, http.StatusOK, row)
}

func (h *handlers) deleteExchanges(w http.ResponseWriter, r *http.Request) {
	sessionID := r.URL.Query().Get("session_id")
	alsoDeleteClaudeSession := r.URL.Query().Get("also_delete_claude_session") == "true"
	n, err := h.db.DeleteExchanges(r.Context(), sessionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var deletedQtd int
	// Try to delete the Claude session if requested, but don't fail the request if it fails.
	if alsoDeleteClaudeSession && sessionID != "" {
		if deletedQtd, err = files.TryDeleteClaudeSession(sessionID); err != nil {
			h.logger.Error("failed to delete Claude session", "error", err, "session_id", sessionID, "deleted", deletedQtd)
		}
	}
	h.logger.Info("delete exchanges", "deletedRows", n, "session_id", sessionID, "also_delete_claude_session", alsoDeleteClaudeSession, "deleted_files", deletedQtd)
	writeJSON(w, http.StatusOK, map[string]int64{"deletedRows": n, "deletedFiles": int64(deletedQtd)})
}

// totals accepts either ?session_id= or ?range= (never both in practice —
// range powers the dashboard, session_id powers nothing today but is kept
// for parity with the old JSON API). An absent/unrecognized range param
// leaves `since` nil, matching GetTokenTotals' all-time default.
func (h *handlers) totals(w http.ResponseWriter, r *http.Request) {
	sessionID := r.URL.Query().Get("session_id")
	var since *float64
	if rangeKey := r.URL.Query().Get("range"); rangeKey != "" {
		s := sinceForRange(normalizeDashboardRange(rangeKey), time.Now())
		since = &s
	}
	t, err := h.db.GetTokenTotals(r.Context(), sessionID, since)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (h *handlers) sessionStats(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.GetSessionStats(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if rows == nil {
		rows = []database.SessionStat{}
	}
	writeJSON(w, http.StatusOK, rows)
}

// dailyCosts powers the dashboard's spending heatmap. ?days= defaults to 60
// (the heatmap's fixed window) and falls back to that default for anything
// absent, non-numeric, or non-positive.
func (h *handlers) dailyCosts(w http.ResponseWriter, r *http.Request) {
	days := 60
	if v := r.URL.Query().Get("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			days = n
		}
	}
	rows, err := h.db.GetDailyCosts(r.Context(), days)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if rows == nil {
		rows = []database.DailyCost{}
	}
	writeJSON(w, http.StatusOK, rows)
}

func (h *handlers) listPrices(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.ListPrices(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if rows == nil {
		rows = []database.Price{}
	}
	writeJSON(w, http.StatusOK, rows)
}

type createPriceRequest struct {
	Prefix         string   `json:"model_prefix"`
	Rule           string   `json:"rule"`
	RuleTokens     *int64   `json:"rule_tokens"`
	InputPerM      *float64 `json:"input_per_m"`
	OutputPerM     *float64 `json:"output_per_m"`
	CacheWritePerM *float64 `json:"cache_write_per_m"`
	CacheReadPerM  *float64 `json:"cache_read_per_m"`
}

// createPrice adds a new rule row for a prefix. Always inserts —
// duplicate/overlapping prefixes are expected, since a prefix can own
// several tiered rules (see internal/pricing for how overlaps are resolved).
func (h *handlers) createPrice(w http.ResponseWriter, r *http.Request) {
	var req createPriceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil ||
		req.Prefix == "" || (req.Rule != "over" && req.Rule != "under") ||
		req.RuleTokens == nil || *req.RuleTokens < 0 ||
		req.InputPerM == nil || req.OutputPerM == nil {
		writeError(w, http.StatusBadRequest, "model_prefix, rule (over|under), rule_tokens, input_per_m and output_per_m are required")
		return
	}

	var cacheWritePerM, cacheReadPerM float64
	if req.CacheWritePerM != nil {
		cacheWritePerM = *req.CacheWritePerM
	}
	if req.CacheReadPerM != nil {
		cacheReadPerM = *req.CacheReadPerM
	}

	now := float64(time.Now().Unix())
	p := database.Price{
		Prefix:         req.Prefix,
		Rule:           req.Rule,
		RuleTokens:     *req.RuleTokens,
		InputPerM:      *req.InputPerM,
		OutputPerM:     *req.OutputPerM,
		CacheWritePerM: cacheWritePerM,
		CacheReadPerM:  cacheReadPerM,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	id, err := h.db.CreatePrice(r.Context(), p)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	p.ID = id
	if err := h.est.Refresh(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, p)
}

type updatePriceRequest struct {
	InputPerM      *float64 `json:"input_per_m"`
	OutputPerM     *float64 `json:"output_per_m"`
	CacheWritePerM *float64 `json:"cache_write_per_m"`
	CacheReadPerM  *float64 `json:"cache_read_per_m"`
}

// updatePrice patches an existing rule row's rates. Cache rates are
// optional: an omitted field keeps whatever the row already has, so a
// client that doesn't know about cache pricing can't accidentally zero it
// out. Prefix/rule/rule_tokens are immutable once created.
func (h *handlers) updatePrice(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}

	var req updatePriceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.InputPerM == nil || req.OutputPerM == nil {
		writeError(w, http.StatusBadRequest, "input_per_m and output_per_m are required numbers")
		return
	}

	existing, err := h.db.GetPrice(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if existing == nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}

	cacheWritePerM, cacheReadPerM := existing.CacheWritePerM, existing.CacheReadPerM
	if req.CacheWritePerM != nil {
		cacheWritePerM = *req.CacheWritePerM
	}
	if req.CacheReadPerM != nil {
		cacheReadPerM = *req.CacheReadPerM
	}

	updatedAt := float64(time.Now().Unix())
	if err := h.db.UpdatePrice(r.Context(), id, *req.InputPerM, *req.OutputPerM, cacheWritePerM, cacheReadPerM, updatedAt); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := h.est.Refresh(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	existing.InputPerM, existing.OutputPerM = *req.InputPerM, *req.OutputPerM
	existing.CacheWritePerM, existing.CacheReadPerM = cacheWritePerM, cacheReadPerM
	existing.UpdatedAt = updatedAt
	writeJSON(w, http.StatusOK, existing)
}

func (h *handlers) deletePrice(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if err := h.db.DeletePrice(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := h.est.Refresh(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"deleted": id})
}
