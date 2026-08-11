package admin

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/lfsc09/claude-lens/internal/database"
)

const uiPageSize = 50

func startOfToday() float64 {
	now := time.Now()
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	return float64(midnight.Unix())
}

func (h *handlers) uiDashboard(c *gin.Context) {
	ctx := c.Request.Context()

	totals, err := h.db.GetTokenTotals(ctx, "", nil)
	if err != nil {
		c.String(http.StatusInternalServerError, "internal error")
		return
	}
	since := startOfToday()
	totalsToday, err := h.db.GetTokenTotals(ctx, "", &since)
	if err != nil {
		c.String(http.StatusInternalServerError, "internal error")
		return
	}
	stats, err := h.db.GetSessionStats(ctx)
	if err != nil {
		c.String(http.StatusInternalServerError, "internal error")
		return
	}
	dailyCosts, err := h.db.GetDailyCosts(ctx, 60)
	if err != nil {
		c.String(http.StatusInternalServerError, "internal error")
		return
	}

	h.render(c, http.StatusOK, "dashboard.html", gin.H{
		"Totals":       totals,
		"TotalsToday":  totalsToday,
		"SessionStats": stats,
		"DailyCosts":   dailyCosts,
	})
}

func (h *handlers) uiExchanges(c *gin.Context) {
	q := c.Query("q")
	page := 1
	if v := c.Query("page"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 1 {
			page = n
		}
	}
	offset := (page - 1) * uiPageSize

	rows, err := h.db.GetExchanges(c.Request.Context(), q, uiPageSize+1, offset)
	var filterErr string
	if err != nil {
		// A malformed query is a user input error, not a server failure —
		// render the page with the parse error and no rows rather than a
		// 500 or (worse) silently falling back to an unfiltered list.
		filterErr = err.Error()
		rows = nil
	}
	hasNext := len(rows) > uiPageSize
	if hasNext {
		rows = rows[:uiPageSize]
	}

	// Only an exact `session = "..."` query (no other conditions) scopes the
	// "Clear session" delete action — anything more complex only offers
	// "Clear all", so a multi-condition filter can never turn into an
	// implicit "delete everything matching this" button.
	sessionID, _ := database.ExtractExactSession(q)

	h.render(c, http.StatusOK, "exchanges.html", gin.H{
		"Rows":        rows,
		"Query":       q,
		"FilterError": filterErr,
		"SessionID":   sessionID,
		"Page":        page,
		"HasNext":     hasNext,
	})
}

func (h *handlers) uiExchangeDetail(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		h.render(c, http.StatusNotFound, "404.html", gin.H{"ID": int64(0)})
		return
	}

	row, err := h.db.GetExchangeDetail(c.Request.Context(), id)
	if err != nil {
		c.String(http.StatusInternalServerError, "internal error")
		return
	}
	if row == nil {
		h.render(c, http.StatusNotFound, "404.html", gin.H{"ID": id})
		return
	}
	h.render(c, http.StatusOK, "exchange_detail.html", row)
}

func (h *handlers) uiReset(c *gin.Context) {
	sessionID := c.PostForm("session_id")
	n, err := h.db.DeleteExchanges(c.Request.Context(), sessionID)
	if err != nil {
		c.String(http.StatusInternalServerError, "internal error")
		return
	}
	h.logger.Info("reset exchanges", "deleted", n, "session_id", sessionID)

	q := ""
	if sessionID != "" {
		q = fmt.Sprintf("session = %q", sessionID)
	}
	redirectURL, err := urlFor("ui_exchanges", "q", q)
	if err != nil {
		redirectURL = "/ui/exchanges"
	}
	c.Redirect(http.StatusFound, redirectURL)
}

func (h *handlers) uiPrices(c *gin.Context) {
	prices, err := h.db.ListPrices(c.Request.Context())
	if err != nil {
		c.String(http.StatusInternalServerError, "internal error")
		return
	}
	h.render(c, http.StatusOK, "prices.html", gin.H{"Prices": prices})
}

func (h *handlers) uiCreatePrice(c *gin.Context) {
	prefix := strings.TrimSpace(c.PostForm("model_prefix"))
	inputPerM, errIn := strconv.ParseFloat(c.PostForm("input_per_m"), 64)
	outputPerM, errOut := strconv.ParseFloat(c.PostForm("output_per_m"), 64)
	if prefix == "" || errIn != nil || errOut != nil {
		c.String(http.StatusBadRequest, "model_prefix, input_per_m, and output_per_m are required")
		return
	}
	cacheWritePerM, errCW := parseOptionalFloat(c.PostForm("cache_write_per_m"))
	cacheReadPerM, errCR := parseOptionalFloat(c.PostForm("cache_read_per_m"))
	if errCW != nil || errCR != nil {
		c.String(http.StatusBadRequest, "cache_write_per_m and cache_read_per_m must be numbers")
		return
	}

	if err := h.upsertPriceAndRefresh(c, prefix, inputPerM, outputPerM, cacheWritePerM, cacheReadPerM); err != nil {
		c.String(http.StatusInternalServerError, "internal error")
		return
	}
	c.Redirect(http.StatusFound, "/ui/prices")
}

func (h *handlers) uiUpdatePrice(c *gin.Context) {
	prefix := c.Param("prefix")
	inputPerM, errIn := strconv.ParseFloat(c.PostForm("input_per_m"), 64)
	outputPerM, errOut := strconv.ParseFloat(c.PostForm("output_per_m"), 64)
	if errIn != nil || errOut != nil {
		c.String(http.StatusBadRequest, "input_per_m and output_per_m must be numbers")
		return
	}
	cacheWritePerM, errCW := parseOptionalFloat(c.PostForm("cache_write_per_m"))
	cacheReadPerM, errCR := parseOptionalFloat(c.PostForm("cache_read_per_m"))
	if errCW != nil || errCR != nil {
		c.String(http.StatusBadRequest, "cache_write_per_m and cache_read_per_m must be numbers")
		return
	}

	if err := h.upsertPriceAndRefresh(c, prefix, inputPerM, outputPerM, cacheWritePerM, cacheReadPerM); err != nil {
		c.String(http.StatusInternalServerError, "internal error")
		return
	}
	c.Redirect(http.StatusFound, "/ui/prices")
}

// parseOptionalFloat parses a form field that may be blank (meaning "leave
// unchanged" to the caller), returning nil without error in that case.
func parseOptionalFloat(raw string) (*float64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

func (h *handlers) uiDeletePrice(c *gin.Context) {
	prefix := c.Param("prefix")
	if err := h.db.DeletePrice(c.Request.Context(), prefix); err != nil {
		c.String(http.StatusInternalServerError, "internal error")
		return
	}
	if err := h.est.Refresh(c.Request.Context()); err != nil {
		c.String(http.StatusInternalServerError, "internal error")
		return
	}
	c.Redirect(http.StatusFound, "/ui/prices")
}

// upsertPriceAndRefresh writes a price row. cacheWritePerM/cacheReadPerM may
// be nil (form field left blank), in which case the existing row's value is
// kept (0 for a brand-new prefix) instead of silently zeroing it out.
func (h *handlers) upsertPriceAndRefresh(c *gin.Context, prefix string, inputPerM, outputPerM float64, cacheWritePerM, cacheReadPerM *float64) error {
	ctx := c.Request.Context()

	var cwPerM, crPerM float64
	if existing, err := h.db.GetPrice(ctx, prefix); err != nil {
		return err
	} else if existing != nil {
		cwPerM, crPerM = existing.CacheWritePerM, existing.CacheReadPerM
	}
	if cacheWritePerM != nil {
		cwPerM = *cacheWritePerM
	}
	if cacheReadPerM != nil {
		crPerM = *cacheReadPerM
	}

	p := database.Price{
		Prefix:         prefix,
		InputPerM:      inputPerM,
		OutputPerM:     outputPerM,
		CacheWritePerM: cwPerM,
		CacheReadPerM:  crPerM,
		UpdatedAt:      float64(time.Now().Unix()),
	}
	if err := h.db.UpsertPrice(ctx, p); err != nil {
		return err
	}
	return h.est.Refresh(ctx)
}

func (h *handlers) notFound(c *gin.Context) {
	h.render(c, http.StatusNotFound, "404.html", gin.H{"ID": int64(0)})
}
