package admin

import (
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/lfsc09/claude-lens/internal/config"
)

// middleware wraps an http.Handler with cross-cutting behavior.
type middleware func(http.Handler) http.Handler

// chain applies mws around h in order, so mws[0] is outermost (runs first
// on the way in, last on the way out).
func chain(h http.Handler, mws ...middleware) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

// recoverMiddleware turns a panicking handler into a 500 instead of
// crashing the server.
func recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Default().With("component", "admin").Error("panic recovered", "error", rec, "path", r.URL.Path)
				w.WriteHeader(http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// statusRecorder captures the status code written by the wrapped handler —
// plain http.ResponseWriter doesn't expose it after the fact.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

// WriteHeader records status and forwards it to the wrapped ResponseWriter.
func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

// Flush lets statusRecorder satisfy http.Flusher when the wrapped
// ResponseWriter does — embedding the http.ResponseWriter interface only
// promotes methods declared on that interface, not Flush, so without this
// the /api/stream SSE handler's `w.(http.Flusher)` assertion would fail
// once wrapped by loggingMiddleware.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// loggingMiddleware logs one line per request: method, path, status, and
// latency.
func loggingMiddleware(logger *slog.Logger) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)
			logger.Info("request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status,
				"latency", time.Since(start),
			)
		})
	}
}

// guardMiddleware rejects requests a web page could send to the local admin
// server on a visitor's behalf. Unless allowAnyHost is set, the Host header
// must name a loopback address, which blocks DNS rebinding. State-changing
// requests must be same-origin and, when they carry a body, declare it as
// JSON, which blocks cross-site form and text/plain posts.
func guardMiddleware(allowAnyHost bool) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !allowAnyHost && !config.IsLoopbackHost(hostWithoutPort(r.Host)) {
				writeError(w, http.StatusForbidden, "forbidden host")
				return
			}
			if isStateChanging(r.Method) {
				if !isSameOrigin(r) {
					writeError(w, http.StatusForbidden, "cross-origin request rejected")
					return
				}
				if r.ContentLength != 0 && !hasJSONContentType(r) {
					writeError(w, http.StatusUnsupportedMediaType, "content type must be application/json")
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// hostWithoutPort strips an optional port and IPv6 brackets from a Host
// header value.
func hostWithoutPort(hostport string) string {
	if host, _, err := net.SplitHostPort(hostport); err == nil {
		return host
	}
	return strings.Trim(hostport, "[]")
}

// isStateChanging reports whether method can modify server state, i.e. is not
// GET, HEAD or OPTIONS.
func isStateChanging(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	return true
}

// isSameOrigin reports whether r carries no cross-site browser signals: its
// Sec-Fetch-Site is absent, same-origin or none, and its Origin, if any,
// names the request's own host.
func isSameOrigin(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "", "same-origin", "none":
	default:
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	return err == nil && u.Host == r.Host
}

// hasJSONContentType reports whether r declares an application/json body.
func hasJSONContentType(r *http.Request) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && mediaType == "application/json"
}
