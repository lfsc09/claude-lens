// Package admin implements the admin server: a JSON API and (in a later
// build step) an HTML dashboard over the same exchanges/model_prices data.
package admin

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/lfsc09/claude-lens/internal/database"
	"github.com/lfsc09/claude-lens/internal/pricing"
	"github.com/lfsc09/claude-lens/internal/status"
)

// Server runs the admin HTTP listener.
type Server struct {
	engine *gin.Engine
}

// NewServer builds a Server with all JSON API routes registered.
func NewServer(db *database.DB, est *pricing.Estimator, st *status.Flag) *Server {
	gin.SetMode(gin.ReleaseMode)

	h := &handlers{db: db, est: est, status: st}

	r := gin.New()
	r.Use(gin.Recovery(), slogMiddleware())

	r.GET("/health", h.health)
	r.GET("/exchanges", h.listExchanges)
	r.GET("/exchanges/:id", h.exchangeDetail)
	r.DELETE("/exchanges", h.resetExchanges)
	r.GET("/totals", h.totals)
	r.GET("/session-stats", h.sessionStats)
	r.GET("/prices", h.listPrices)
	r.PUT("/prices/:prefix", h.upsertPrice)
	r.DELETE("/prices/:prefix", h.deletePrice)

	return &Server{engine: r}
}

// Run serves on addr until ctx is done, then gracefully shuts down.
func (s *Server) Run(ctx context.Context, addr string) error {
	srv := &http.Server{
		Addr:    addr,
		Handler: s.engine,
		// No WriteTimeout: the /stream SSE route (added in a later build
		// step) holds its connection open indefinitely.
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("admin server listening", "addr", addr)
		err := srv.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		close(errCh)
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		slog.Info("admin server shutting down")
		return srv.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}

func slogMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		slog.Info("request",
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", c.Writer.Status(),
			"latency", time.Since(start),
		)
	}
}
