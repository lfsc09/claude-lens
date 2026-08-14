// Package proxy implements the reverse-proxy server: it forwards requests to
// the configured Anthropic base URL and, for POSTs, records an exchange row
// once the (possibly streamed) response has been fully delivered to the
// client.
package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/lfsc09/claude-lens/internal/config"
	"github.com/lfsc09/claude-lens/internal/database"
	"github.com/lfsc09/claude-lens/internal/pricing"
	"github.com/lfsc09/claude-lens/internal/status"
)

// Server runs the proxy's HTTP listener.
type Server struct {
	handler *Handler
	status  *status.Flag
	logger  *slog.Logger
}

// NewServer builds a Server. See NewHandler for the possible config errors.
func NewServer(cfg config.Config, db *database.DB, estimator *pricing.Estimator, st *status.Flag, fr *status.Fresh) (*Server, error) {
	h, err := NewHandler(cfg, db, estimator, st, fr)
	if err != nil {
		return nil, err
	}
	return &Server{handler: h, status: st, logger: slog.Default().With("component", "proxy")}, nil
}

// Run serves on addr until ctx is done, then gracefully shuts down.
func (s *Server) Run(ctx context.Context, addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.health)
	mux.Handle("/", s.handler)

	srv := &http.Server{
		Addr:    addr,
		Handler: mux,
		// Protects against slow-loris on the way in. Deliberately no
		// WriteTimeout — this server streams SSE responses of unbounded
		// duration; a write deadline would cut them off mid-stream.
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		s.logger.Info("proxy server listening", "addr", addr)
		s.status.Set(status.OK)
		err := srv.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		close(errCh)
	}()

	select {
	case <-ctx.Done():
		s.status.Set(status.Unreachable)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		s.logger.Info("proxy server shutting down")
		err := srv.Shutdown(shutdownCtx)
		// Shutdown only waits for in-flight HTTP handlers to return, not for
		// the detached saveExchange goroutines they kicked off — drain those
		// too so the last exchange before shutdown isn't silently dropped.
		s.handler.waitForSaves(5 * time.Second)
		return err
	case err := <-errCh:
		s.status.Set(status.Unreachable)
		return err
	}
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
