package admin

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/lfsc09/claude-lens/internal/database"
)

// sseInterval is how often /stream pushes a new payload. It's a package
// var (not a const) purely so tests can shrink it instead of waiting on
// the real 3-second cadence.
var sseInterval = 3 * time.Second

type ssePayload struct {
	ProxyStatus      string          `json:"proxy_status"`
	Totals           database.Totals `json:"totals"`
	TotalsToday      database.Totals `json:"totals_today"`
	LatestExchangeID int64           `json:"latest_exchange_id"`
}

// sseStream is a Server-Sent Events endpoint pushing live proxy status and
// token totals every sseInterval, until the client disconnects.
//
// The old Python version needed a gevent worker specifically because a
// synchronous WSGI worker blocking on time.sleep(3) inside this generator
// would tie up that whole worker process, starving every other request.
// net/http gives every connection its own goroutine, so that constraint
// doesn't exist here — this is a plain per-connection loop, no special
// worker mode or cooperative-scheduling library needed.
func (h *handlers) sseStream(c *gin.Context) {
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Writer.WriteHeader(200)

	ticker := time.NewTicker(sseInterval)
	defer ticker.Stop()

	for {
		payload, err := h.buildSSEPayload(c.Request.Context())
		if err != nil {
			slog.Error("failed to build SSE payload", "error", err)
		} else {
			b, err := json.Marshal(payload)
			if err != nil {
				slog.Error("failed to marshal SSE payload", "error", err)
			} else {
				c.Writer.WriteString("data: ")
				c.Writer.Write(b)
				c.Writer.WriteString("\n\n")
				c.Writer.Flush()
			}
		}

		select {
		case <-c.Request.Context().Done():
			return
		case <-ticker.C:
		}
	}
}

func (h *handlers) buildSSEPayload(ctx context.Context) (ssePayload, error) {
	totals, err := h.db.GetTokenTotals(ctx, "", nil)
	if err != nil {
		return ssePayload{}, err
	}
	since := startOfToday()
	totalsToday, err := h.db.GetTokenTotals(ctx, "", &since)
	if err != nil {
		return ssePayload{}, err
	}
	latestID, err := h.latestExchangeID(ctx)
	if err != nil {
		return ssePayload{}, err
	}
	return ssePayload{
		ProxyStatus:      string(h.status.Get()),
		Totals:           totals,
		TotalsToday:      totalsToday,
		LatestExchangeID: latestID,
	}, nil
}

func (h *handlers) latestExchangeID(ctx context.Context) (int64, error) {
	rows, err := h.db.GetExchanges(ctx, "", 1, 0)
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	return rows[0].ID, nil
}
