package admin

import (
	"log/slog"
	"net/http"
	"time"
)

// middleware wraps an http.Handler with cross-cutting behavior.
type middleware func(http.Handler) http.Handler

// chain applies mws around h in order, so mws[0] is outermost (runs first
// on the way in, last on the way out) — matches the order they'd be passed
// to gin's r.Use(...).
func chain(h http.Handler, mws ...middleware) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

// recoverMiddleware turns a panicking handler into a 500 instead of
// crashing the server — mirrors gin.Recovery().
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
// plain http.ResponseWriter doesn't expose it after the fact, unlike gin's
// c.Writer.Status().
type statusRecorder struct {
	http.ResponseWriter
	status int
}

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

// loggingMiddleware logs one line per request — mirrors the old
// slogMiddleware built on gin.HandlerFunc.
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
