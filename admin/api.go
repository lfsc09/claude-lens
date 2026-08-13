package admin

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/lfsc09/claude-lens/internal/database"
	"github.com/lfsc09/claude-lens/internal/pricing"
	"github.com/lfsc09/claude-lens/internal/status"
)

// handlers holds the dependencies shared by every admin route.
type handlers struct {
	db      *database.DB
	est     *pricing.Estimator
	status  *status.Flag
	logger  *slog.Logger
	version string
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
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
}

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
	writeJSON(w, http.StatusOK, exchangesResponse{Rows: rows, Total: total, SessionID: sessionID})
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

func (h *handlers) resetExchanges(w http.ResponseWriter, r *http.Request) {
	sessionID := r.URL.Query().Get("session_id")
	n, err := h.db.DeleteExchanges(r.Context(), sessionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.logger.Info("reset exchanges", "deleted", n, "session_id", sessionID)
	writeJSON(w, http.StatusOK, map[string]int64{"deleted": n})
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

type upsertPriceRequest struct {
	InputPerM      *float64 `json:"input_per_m"`
	OutputPerM     *float64 `json:"output_per_m"`
	CacheWritePerM *float64 `json:"cache_write_per_m"`
	CacheReadPerM  *float64 `json:"cache_read_per_m"`
}

// upsertPrice creates or updates a price row (a "create" is just a PUT on a
// new prefix — there's no separate create endpoint). Cache rates are
// optional: an omitted field keeps whatever the row already has (0 for a
// brand-new prefix), so a client that doesn't know about cache pricing
// can't accidentally zero it out.
func (h *handlers) upsertPrice(w http.ResponseWriter, r *http.Request) {
	prefix := r.PathValue("prefix")

	var req upsertPriceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.InputPerM == nil || req.OutputPerM == nil {
		writeError(w, http.StatusBadRequest, "input_per_m and output_per_m are required numbers")
		return
	}

	var cacheWritePerM, cacheReadPerM float64
	if existing, err := h.db.GetPrice(r.Context(), prefix); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	} else if existing != nil {
		cacheWritePerM, cacheReadPerM = existing.CacheWritePerM, existing.CacheReadPerM
	}
	if req.CacheWritePerM != nil {
		cacheWritePerM = *req.CacheWritePerM
	}
	if req.CacheReadPerM != nil {
		cacheReadPerM = *req.CacheReadPerM
	}

	p := database.Price{
		Prefix:         prefix,
		InputPerM:      *req.InputPerM,
		OutputPerM:     *req.OutputPerM,
		CacheWritePerM: cacheWritePerM,
		CacheReadPerM:  cacheReadPerM,
		UpdatedAt:      float64(time.Now().Unix()),
	}
	if err := h.db.UpsertPrice(r.Context(), p); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := h.est.Refresh(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (h *handlers) deletePrice(w http.ResponseWriter, r *http.Request) {
	prefix := r.PathValue("prefix")
	if err := h.db.DeletePrice(r.Context(), prefix); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := h.est.Refresh(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"deleted": prefix})
}
