// Command claude-lens runs the proxy and admin servers.
package main

import (
	"log/slog"
	"os"

	"github.com/lfsc09/claude-lens/internal/config"
	"github.com/lfsc09/claude-lens/internal/logging"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	logging.Setup(cfg)

	slog.Info("starting", "proxy_addr", cfg.ProxyAddr, "admin_addr", cfg.AdminAddr)
}
