// Package admin implements the admin server: a JSON API and (in a later
// build step) an HTML dashboard over the same exchanges/model_prices data.
package admin

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
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
	logger *slog.Logger
}

// NewServer builds a Server with the JSON API and HTML UI routes
// registered. It only fails if the embedded templates don't parse — a
// build-time invariant, not a runtime condition — so a broken template is
// treated as fatal rather than something the caller can meaningfully
// recover from at startup.
func NewServer(db *database.DB, est *pricing.Estimator, st *status.Flag, version string) (*Server, error) {
	gin.SetMode(gin.ReleaseMode)

	pages, err := ParseTemplates()
	if err != nil {
		return nil, err
	}

	logger := slog.Default().With("component", "admin")
	h := &handlers{db: db, est: est, status: st, pages: pages, logger: logger, version: version}

	r := gin.New()
	r.Use(gin.Recovery(), slogMiddleware(logger))

	// Static assets (favicon, logo), embedded via static.go's staticFS.
	staticContent, err := fs.Sub(staticFS, "static")
	if err != nil {
		return nil, fmt.Errorf("sub static fs: %w", err)
	}
	r.StaticFS("/static", http.FS(staticContent))
	r.GET("/favicon.ico", func(c *gin.Context) {
		c.FileFromFS("favicon.ico", http.FS(staticContent))
	})

	// JSON API
	r.GET("/health", h.health)
	r.GET("/exchanges", h.listExchanges)
	r.GET("/exchanges/:id", h.exchangeDetail)
	r.DELETE("/exchanges", h.resetExchanges)
	r.GET("/totals", h.totals)
	r.GET("/stream", h.sseStream)
	r.GET("/session-stats", h.sessionStats)
	r.GET("/prices", h.listPrices)
	r.PUT("/prices/:prefix", h.upsertPrice)
	r.DELETE("/prices/:prefix", h.deletePrice)

	// HTML UI
	r.GET("/", h.uiDashboard)
	r.GET("/ui/exchanges", h.uiExchanges)
	r.GET("/ui/exchanges/:id", h.uiExchangeDetail)
	r.POST("/ui/reset", h.uiReset)
	r.GET("/ui/prices", h.uiPrices)
	r.POST("/ui/prices", h.uiCreatePrice)
	r.POST("/ui/prices/:prefix", h.uiUpdatePrice)
	r.POST("/ui/prices/:prefix/delete", h.uiDeletePrice)
	r.NoRoute(h.notFound)

	return &Server{engine: r, logger: logger}, nil
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
		s.logger.Info("admin server listening", "addr", addr)
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
		s.logger.Info("admin server shutting down")
		return srv.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}

func slogMiddleware(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		logger.Info("request",
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", c.Writer.Status(),
			"latency", time.Since(start),
		)
	}
}
