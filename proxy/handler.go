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
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/lfsc09/claude-lens/internal/config"
	"github.com/lfsc09/claude-lens/internal/database"
	"github.com/lfsc09/claude-lens/internal/parsing"
	"github.com/lfsc09/claude-lens/internal/pricing"
	"github.com/lfsc09/claude-lens/internal/status"
)

// exchangeMeta carries per-request state from Director to ModifyResponse —
// httputil.ReverseProxy gives no other hook for passing data between the
// two, so it rides the outbound request's context (resp.Request is the same
// *http.Request Director mutated, per net/http.Transport's contract of
// setting Response.Request to the request it sent).
type exchangeMeta struct {
	intercept     bool
	sessionID     string
	sessionName   *string
	path          string
	timestamp     float64
	rawRequest    string
	inputMessages *string
	isStreaming   bool
	model         *string
}

type metaKey struct{}

// Handler is an http.Handler that reverse-proxies to the configured
// Anthropic base URL, intercepting POST requests to record an exchange.
type Handler struct {
	cfg           config.Config
	db            *database.DB
	estimator     *pricing.Estimator
	status        *status.Flag
	fresh         *status.Fresh
	customHeaders map[string]string
	target        *url.URL
	rp            *httputil.ReverseProxy
	logger        *slog.Logger

	// saveWG tracks saveExchange goroutines still writing to the database,
	// so Server.Run can drain them before the process exits instead of
	// dropping the last exchange of a request that completed right as
	// shutdown began.
	saveWG sync.WaitGroup
}

// NewHandler builds a Handler. It fails only if
// CLENS_PROXY_CUSTOM_HEADERS or CLENS_PROXY_BASE_URL is malformed.
func NewHandler(cfg config.Config, db *database.DB, estimator *pricing.Estimator, st *status.Flag, fr *status.Fresh) (*Handler, error) {
	customHeaders, err := ParseCustomHeaders(cfg.AnthropicCustomHeaders)
	if err != nil {
		return nil, err
	}

	target, err := url.Parse(cfg.AnthropicBaseURL)
	if err != nil {
		return nil, err
	}

	h := &Handler{
		cfg:           cfg,
		db:            db,
		estimator:     estimator,
		status:        st,
		fresh:         fr,
		customHeaders: customHeaders,
		target:        target,
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
		// Flush after every write, unconditionally — Anthropic's streaming
		// responses are SSE, but don't rely on ReverseProxy's default
		// content-type sniff to catch that; a buffered stream defeats the
		// entire point of this proxy.
		FlushInterval: -1,
	}

	return h, nil
}

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

// writeLimiterBlocked writes an Anthropic-shaped 429 error body so Claude
// Code's existing error display renders it, instead of a generic
// connection failure.
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

func (h *Handler) director(r *http.Request) {
	originalPath := r.URL.Path

	r.URL.Scheme = h.target.Scheme
	r.URL.Host = h.target.Host
	r.URL.Path = h.target.Path + originalPath
	r.Host = h.target.Host

	// The client's Accept-Encoding (Claude Code asks for gzip/br) would
	// otherwise ride straight through to upstream. http.Transport only
	// auto-decompresses gzip when it picks the encoding itself — if this
	// header survives, Anthropic replies compressed (often brotli, which
	// Go never decodes) and every downstream read, including the
	// tee'd copy used for parsing tokens/output text, gets raw compressed
	// bytes instead of text. Stripping it lets Transport negotiate and
	// transparently undo gzip on its own.
	r.Header.Del("Accept-Encoding")

	if h.cfg.AnthropicAuthToken != "" {
		SetHeader(r.Header, "Authorization", FormatAuthHeader(h.cfg.AnthropicAuthToken))
	}
	for name, value := range h.customHeaders {
		SetHeader(r.Header, name, value)
	}

	meta := &exchangeMeta{path: originalPath}

	if r.Method == http.MethodPost {
		meta.intercept = true
		meta.sessionID = sessionIDFromRequest(r)
		if name := r.Header.Get("x-session-name"); name != "" {
			meta.sessionName = &name
		}
		meta.timestamp = float64(time.Now().UnixNano()) / 1e9

		body, err := io.ReadAll(r.Body)
		r.Body.Close()
		if err != nil {
			h.logger.Error("failed to read request body, forwarding without interception", "error", err, "path", originalPath)
			meta.intercept = false
			r.Body = http.NoBody
		} else {
			r.Body = io.NopCloser(bytes.NewReader(body))
			meta.rawRequest = string(body)
			meta.inputMessages, meta.isStreaming, meta.model = parsing.ExtractRequestFields(body)
		}
	}

	*r = *r.WithContext(context.WithValue(r.Context(), metaKey{}, meta))
}

func (h *Handler) modifyResponse(resp *http.Response) error {
	h.status.Set(status.OK)

	meta, _ := resp.Request.Context().Value(metaKey{}).(*exchangeMeta)
	if meta == nil || !meta.intercept {
		return nil
	}

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
// finished, or timeout elapses first — bounded so a stuck database write
// (past SQLite's own busy_timeout) can't hang shutdown forever.
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

func (h *Handler) saveExchange(meta *exchangeMeta, rawResponse []byte) {
	ctx := context.Background()

	outputText, usage := parsing.ExtractResponseFields(rawResponse, meta.isStreaming)

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
		SessionName:         meta.sessionName,
		Path:                meta.path,
		Timestamp:           meta.timestamp,
		IsStreaming:         meta.isStreaming,
		InputMessages:       meta.inputMessages,
		RawRequest:          meta.rawRequest,
		RawResponse:         strings.ToValidUTF8(string(rawResponse), "�"),
		OutputText:          outputText,
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
}

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

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
