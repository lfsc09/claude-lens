package admin

import (
	"html/template"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/lfsc09/claude-lens/internal/database"
	"github.com/lfsc09/claude-lens/internal/pricing"
	"github.com/lfsc09/claude-lens/internal/status"
)

// handlers holds the dependencies shared by every admin route.
type handlers struct {
	db     *database.DB
	est    *pricing.Estimator
	status *status.Flag
	pages  map[string]*template.Template
}

// render executes the named page template (see ParseTemplates — each page
// is its own clone of base.html, so "content"/"title"/"scripts" from one
// page can never leak into another).
func (h *handlers) render(c *gin.Context, status int, page string, data any) {
	tmpl, ok := h.pages[page]
	if !ok {
		slog.Error("no such page template", "page", page)
		c.Status(http.StatusInternalServerError)
		return
	}
	c.Status(status)
	c.Header("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(c.Writer, page, data); err != nil {
		slog.Error("template render failed", "page", page, "error", err)
	}
}

func (h *handlers) health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok", "proxy": string(h.status.Get())})
}

func (h *handlers) listExchanges(c *gin.Context) {
	sessionID := c.Query("session_id")

	limit := 100
	offset := 0
	if v := c.Query("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "limit and offset must be integers"})
			return
		}
		limit = n
	}
	if v := c.Query("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "limit and offset must be integers"})
			return
		}
		offset = n
	}
	if limit > 1000 {
		limit = 1000
	}

	rows, err := h.db.GetExchanges(c.Request.Context(), sessionID, limit, offset)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if rows == nil {
		rows = []database.ExchangeSummary{}
	}
	c.JSON(http.StatusOK, rows)
}

func (h *handlers) exchangeDetail(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}

	row, err := h.db.GetExchangeDetail(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if row == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	c.JSON(http.StatusOK, row)
}

func (h *handlers) totals(c *gin.Context) {
	sessionID := c.Query("session_id")
	t, err := h.db.GetTokenTotals(c.Request.Context(), sessionID, nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, t)
}

func (h *handlers) sessionStats(c *gin.Context) {
	rows, err := h.db.GetSessionStats(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if rows == nil {
		rows = []database.SessionStat{}
	}
	c.JSON(http.StatusOK, rows)
}

func (h *handlers) resetExchanges(c *gin.Context) {
	sessionID := c.Query("session_id")
	n, err := h.db.DeleteExchanges(c.Request.Context(), sessionID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	slog.Info("reset exchanges", "deleted", n, "session_id", sessionID)
	c.JSON(http.StatusOK, gin.H{"deleted": n})
}

func (h *handlers) listPrices(c *gin.Context) {
	rows, err := h.db.ListPrices(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if rows == nil {
		rows = []database.Price{}
	}
	c.JSON(http.StatusOK, rows)
}

type upsertPriceRequest struct {
	InputPerM  *float64 `json:"input_per_m" binding:"required"`
	OutputPerM *float64 `json:"output_per_m" binding:"required"`
}

func (h *handlers) upsertPrice(c *gin.Context) {
	prefix := c.Param("prefix")

	var req upsertPriceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "input_per_m and output_per_m are required numbers"})
		return
	}

	p := database.Price{
		Prefix:     prefix,
		InputPerM:  *req.InputPerM,
		OutputPerM: *req.OutputPerM,
		UpdatedAt:  float64(time.Now().Unix()),
	}
	if err := h.db.UpsertPrice(c.Request.Context(), p); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if err := h.est.Refresh(c.Request.Context()); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, p)
}

func (h *handlers) deletePrice(c *gin.Context) {
	prefix := c.Param("prefix")
	if err := h.db.DeletePrice(c.Request.Context(), prefix); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if err := h.est.Refresh(c.Request.Context()); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": prefix})
}
