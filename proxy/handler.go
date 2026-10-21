package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"strings"
	"sync"
	"time"

	"github.com/lfsc09/claude-lens/internal/database"
	"github.com/lfsc09/claude-lens/internal/parsing"
	"github.com/lfsc09/claude-lens/internal/pricing"
	"github.com/lfsc09/claude-lens/internal/status"
	"github.com/lfsc09/claude-lens/internal/upstream"
)

// exchangeMeta carries per-request state from the director to modifyResponse
// through the outbound request's context.
type exchangeMeta struct {
	intercept   bool
	sessionID   string
	path        string
	createdAt   float64
	rawRequest  string
	isStreaming bool
	model       *string
	statusCode  int
}

// metaKey is the request context key under which the exchange metadata is
// stored.
type metaKey struct{}

// Handler is an http.Handler that reverse-proxies to the current upstream
// target (see upstream.Registry), intercepting POST requests to record an exchange.
type Handler struct {
	upstream      *upstream.Registry
	db            *database.DB
	estimator     *pricing.Estimator
	status        *status.Flag
	fresh         *status.Fresh
	limitersFresh *status.Fresh
	rp            *httputil.ReverseProxy
	logger        *slog.Logger

	// saveWG tracks saveExchange goroutines still writing to the database;
	// Server.Run drains it on shutdown.
	saveWG sync.WaitGroup
}

// NewHandler builds a Handler that forwards to whatever target reg currently
// resolves to.
func NewHandler(reg *upstream.Registry, db *database.DB, estimator *pricing.Estimator, st *status.Flag, fr *status.Fresh, limitersFresh *status.Fresh) *Handler {
	h := &Handler{
		upstream:      reg,
		db:            db,
		estimator:     estimator,
		status:        st,
		fresh:         fr,
		limitersFresh: limitersFresh,
		logger:        slog.Default().With("component", "proxy"),
	}

	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout: 10 * time.Second,
		}).DialContext,
		ResponseHeaderTimeout: 300 * time.Second,
	}

	h.rp = &httputil.ReverseProxy{
		Director:       h.director,
		ModifyResponse: h.modifyResponse,
		ErrorHandler:   h.errorHandler,
		Transport:      transport,
		// Flushes after every write so streamed responses are not buffered.
		FlushInterval: -1,
	}

	return h
}

// ServeHTTP rejects a POST that a limiter blocks and forwards every other
// request to the current upstream.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		sessionID := sessionIDFromRequest(r)
		if blocked, by, err := h.db.CheckLimiters(r.Context(), sessionID); err != nil {
			h.logger.Error("check limiters failed", "error", err, "session_id", sessionID)
		} else if blocked {
			writeLimiterBlocked(w, by)
			return
		}
	}
	h.rp.ServeHTTP(w, r)
}

// sessionIDFromRequest extracts and sanitizes the session id Claude Code
// sends on a request, falling back to "default_session" when neither
// header is present.
func sessionIDFromRequest(r *http.Request) string {
	return SanitizeSessionID(firstNonEmpty(
		r.Header.Get("x-claude-code-session-id"),
		r.Header.Get("x-session-id"),
		"default_session",
	))
}

// writeLimiterBlocked writes an Anthropic-shaped 429 error body.
func writeLimiterBlocked(w http.ResponseWriter, by *database.Limiter) {
	scope := "global"
	if by.SessionID != "" {
		scope = "session " + by.SessionID
	}
	message := fmt.Sprintf("Cost limit reached: %s limiter has spent $%.2f of its $%.2f budget", scope, by.CurrentCost, by.LimitAmount)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusTooManyRequests)
	if err := json.NewEncoder(w).Encode(map[string]any{
		"type": "error",
		"error": map[string]string{
			"type":    "rate_limit_error",
			"message": message,
		},
	}); err != nil {
		slog.Default().Warn("writeLimiterBlocked: write response", "error", err)
	}
}

// director points r at the current upstream target, injects its credentials
// and headers, and attaches the exchange metadata to the request context.
func (h *Handler) director(r *http.Request) {
	originalPath := r.URL.Path

	target := h.upstream.Current()
	r.URL.Scheme = target.BaseURL.Scheme
	r.URL.Host = target.BaseURL.Host
	r.URL.Path = target.BaseURL.Path + originalPath
	r.Host = target.BaseURL.Host

	// Dropping Accept-Encoding lets http.Transport negotiate gzip itself and
	// decompress it, so the recorded body is always plain text.
	r.Header.Del("Accept-Encoding")

	if target.AuthToken != "" {
		r.Header.Set("Authorization", FormatAuthHeader(target.AuthToken))
	}
	for name, value := range target.Headers {
		r.Header.Set(name, value)
	}

	meta := &exchangeMeta{path: originalPath}

	if r.Method == http.MethodPost {
		meta.intercept = true
		meta.sessionID = sessionIDFromRequest(r)
		meta.createdAt = float64(time.Now().UnixNano()) / 1e9

		body, err := io.ReadAll(r.Body)
		r.Body.Close()
		if err != nil {
			h.logger.Error("failed to read request body, forwarding without interception", "error", err, "path", originalPath)
			meta.intercept = false
			r.Body = http.NoBody
		} else {
			r.Body = io.NopCloser(bytes.NewReader(body))
			meta.rawRequest = string(body)
			meta.isStreaming, meta.model = parsing.ExtractRequestFields(body)
		}
	}

	*r = *r.WithContext(context.WithValue(r.Context(), metaKey{}, meta))
}

// modifyResponse marks the proxy healthy and wraps the response body so the
// exchange is saved once it has been fully read.
func (h *Handler) modifyResponse(resp *http.Response) error {
	h.status.Set(status.OK)

	meta, _ := resp.Request.Context().Value(metaKey{}).(*exchangeMeta)
	if meta == nil || !meta.intercept {
		return nil
	}

	meta.statusCode = resp.StatusCode
	h.saveWG.Add(1)
	resp.Body = newTeeReadCloser(resp.Body, func(raw []byte) {
		go func() {
			defer h.saveWG.Done()
			h.saveExchange(meta, raw)
		}()
	})
	return nil
}

// waitForSaves blocks until every in-flight saveExchange goroutine has
// finished or timeout elapses.
func (h *Handler) waitForSaves(timeout time.Duration) {
	done := make(chan struct{})
	go func() {
		h.saveWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		h.logger.Warn("timed out waiting for in-flight exchange saves during shutdown")
	}
}

// saveExchange extracts usage from rawResponse, costs it and stores the
// exchange.
func (h *Handler) saveExchange(meta *exchangeMeta, rawResponse []byte) {
	ctx := context.Background()

	usage := parsing.ExtractUsage(rawResponse, meta.isStreaming)
	if usage.Empty() && meta.statusCode/100 == 2 {
		h.logger.Warn("no usable token usage in upstream response; cost left unknown", "session_id", meta.sessionID, "path", meta.path)
	}

	var inputCost, outputCost, cacheCreationCost, cacheReadCost *float64
	var matchedPrice *string
	if meta.model != nil && usage.InputTokens != nil && usage.OutputTokens != nil {
		cacheCreationTokens, cacheReadTokens := 0, 0
		if usage.CacheCreationTokens != nil {
			cacheCreationTokens = *usage.CacheCreationTokens
		}
		if usage.CacheReadTokens != nil {
			cacheReadTokens = *usage.CacheReadTokens
		}
		costs, price, ok := h.estimator.EstimateCostsAndRule(*meta.model, *usage.InputTokens, *usage.OutputTokens, cacheCreationTokens, cacheReadTokens)
		if ok {
			inputCost, outputCost = &costs.InputCost, &costs.OutputCost
			if usage.CacheCreationTokens != nil {
				cacheCreationCost = &costs.CacheCreationCost
			}
			if usage.CacheReadTokens != nil {
				cacheReadCost = &costs.CacheReadCost
			}
			// Captured once here rather than recomputed on read, so the
			// exchange detail page keeps showing the rule that was actually
			// charged even after model_prices changes later.
			if raw, err := json.Marshal(price); err != nil {
				h.logger.Error("failed to marshal matched price rule", "error", err, "session_id", meta.sessionID, "path", meta.path)
			} else {
				s := string(raw)
				matchedPrice = &s
			}
		}
	}

	err := h.db.SaveExchange(ctx, database.Exchange{
		SessionID:           meta.sessionID,
		Path:                meta.path,
		CreatedAt:           meta.createdAt,
		IsStreaming:         meta.isStreaming,
		RawRequest:          meta.rawRequest,
		RawResponse:         strings.ToValidUTF8(string(rawResponse), "�"),
		InputTokens:         usage.InputTokens,
		OutputTokens:        usage.OutputTokens,
		CacheCreationTokens: usage.CacheCreationTokens,
		CacheReadTokens:     usage.CacheReadTokens,
		Model:               meta.model,
		InputCost:           inputCost,
		OutputCost:          outputCost,
		CacheCreationCost:   cacheCreationCost,
		CacheReadCost:       cacheReadCost,
		MatchedPrice:        matchedPrice,
	})
	if err != nil {
		h.logger.Error("failed to save exchange", "error", err, "session_id", meta.sessionID, "path", meta.path)
		return
	}
	h.fresh.Bump()
	h.limitersFresh.Bump()
}

// errorHandler marks the proxy degraded and answers a failed forward with
// 502, 504 or 500.
func (h *Handler) errorHandler(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, context.Canceled) {
		return // client disconnected; nothing to write back
	}

	h.status.Set(status.Degraded)
	h.logger.Error("forward to upstream failed", "error", err, "url", r.URL.String())

	var netErr net.Error
	switch {
	case errors.As(err, &netErr) && netErr.Timeout():
		http.Error(w, "Gateway Timeout: upstream did not respond in time", http.StatusGatewayTimeout)
	case errors.As(err, &netErr):
		http.Error(w, "Bad Gateway: upstream connection failed", http.StatusBadGateway)
	default:
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

// firstNonEmpty returns the first non-empty string in values, or "" when all
// are empty.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
