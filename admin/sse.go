package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/lfsc09/claude-lens/internal/database"
	"github.com/lfsc09/claude-lens/internal/status"
)

// sseInterval is how often /stream pushes a new payload. It's a package
// var (not a const) purely so tests can shrink it instead of waiting on
// the real 3-second cadence.
var sseInterval = 3 * time.Second

type ssePayload struct {
	ProxyStatus      string          `json:"proxy_status"`
	Totals           database.Totals `json:"totals"`
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
func (h *handlers) sseStream(w http.ResponseWriter, r *http.Request) {
	rangeKey := normalizeDashboardRange(r.URL.Query().Get("range"))

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	ticker := time.NewTicker(sseInterval)
	defer ticker.Stop()

	// lastVersion/lastStatus/first gate pushes on this connection actually
	// having something new to report — h.fresh.Version() and h.status.Get()
	// are cheap in-process reads, so checking them costs nothing on a quiet
	// tick, unlike buildSSEPayload which hits the database. The very first
	// iteration always pushes so a newly connected client isn't left with
	// nothing until the next change.
	var lastVersion uint64
	var lastStatus status.Value
	first := true

	for {
		version := h.fresh.Version()
		current := h.status.Get()

		if first || version != lastVersion || current != lastStatus {
			payload, err := h.buildSSEPayload(r.Context(), rangeKey)
			if err != nil {
				h.logger.Error("failed to build SSE payload", "error", err)
			} else {
				b, err := json.Marshal(payload)
				if err != nil {
					h.logger.Error("failed to marshal SSE payload", "error", err)
				} else {
					w.Write([]byte("data: "))
					w.Write(b)
					w.Write([]byte("\n\n"))
					flusher.Flush()
				}
			}
			lastVersion, lastStatus, first = version, current, false
		}

		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}

// buildSSEPayload resolves rangeKey to a since-timestamp fresh on every
// call (rather than once at connection-open time), so a long-lived stream
// self-corrects across day/week/month rollovers without needing the client
// to reconnect.
func (h *handlers) buildSSEPayload(ctx context.Context, rangeKey string) (ssePayload, error) {
	since := sinceForRange(normalizeDashboardRange(rangeKey), time.Now())
	totals, err := h.db.GetTokenTotals(ctx, "", &since)
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
