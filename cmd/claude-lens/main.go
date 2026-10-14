// Command claude-lens runs the proxy and admin servers as goroutines in a
// single process, sharing one *database.DB, one status.Flag, and one
// status.Fresh.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/lfsc09/claude-lens/admin"
	"github.com/lfsc09/claude-lens/internal/bifrost"
	"github.com/lfsc09/claude-lens/internal/config"
	"github.com/lfsc09/claude-lens/internal/database"
	"github.com/lfsc09/claude-lens/internal/litellm"
	"github.com/lfsc09/claude-lens/internal/logging"
	"github.com/lfsc09/claude-lens/internal/notify"
	"github.com/lfsc09/claude-lens/internal/pricesync"
	"github.com/lfsc09/claude-lens/internal/pricing"
	"github.com/lfsc09/claude-lens/internal/status"
	"github.com/lfsc09/claude-lens/internal/upstream"
	"github.com/lfsc09/claude-lens/proxy"
)

func main() {
	os.Exit(run())
}

// run executes the command selected by the flags and returns the process exit
// code, so deferred cleanup runs before the process exits.
func run() int {
	feed := flag.Bool("feed", false, "seed a single row into a table via the running admin API, instead of starting the service")
	table := flag.String("table", "", "table to seed with --feed (limiters, model_prices, notification_integrations, proxy_integrations)")
	row := flag.String("row", "", "row to insert or update with --feed, as a JSON object")
	match := flag.String("match", "", "match an existing row by column:value pairs (JSON object) for --feed; updates it instead of inserting")
	dummy := flag.Bool("dummy", false, "create dummy exchanges via the running admin API, instead of starting the service")
	count := flag.Int("count", 100, "number of exchanges to create with --dummy")
	sessions := flag.Int("sessions", 5, "number of distinct sessions to spread --dummy exchanges over")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "error", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *feed && *dummy {
		fmt.Fprintln(os.Stderr, "error: --feed and --dummy cannot be combined")
		return 1
	}

	if *dummy {
		if err := runDummy(ctx, cfg, *count, *sessions); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
		return 0
	}

	if *feed {
		if err := runFeed(ctx, cfg, *table, *row, *match); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
		return 0
	}

	logging.Setup(cfg)

	db, err := database.Open(ctx, cfg.DBPath)
	if err != nil {
		slog.Error("failed to open database", "error", err)
		return 1
	}
	defer db.Close()

	db.SetNotifications(notify.NewClient())

	est := pricing.New(db)
	if err := est.Refresh(ctx); err != nil {
		slog.Error("failed to load model prices", "error", err)
		return 1
	}

	st := status.New()
	fresh := status.NewFresh()
	limitersFresh := status.NewFresh()

	registry, err := upstream.NewRegistry(ctx, db)
	if err != nil {
		slog.Error("failed to resolve upstream", "error", err)
		return 1
	}
	fetchers := pricesync.Fetchers{
		database.ProxyTypeLiteLLM: litellm.NewClient(),
		database.ProxyTypeBifrost: bifrost.NewClient(),
	}

	proxySrv := proxy.NewServer(registry, db, est, st, fresh, limitersFresh)

	adminSrv, err := admin.NewServer(db, est, registry, fetchers, st, fresh, limitersFresh, Version, cfg.DBPath, cfg.LogDir, !config.IsLoopbackAddr(cfg.AdminAddr))
	if err != nil {
		slog.Error("failed to build admin server", "error", err)
		return 1
	}

	slog.Info("starting", "version", Version, "proxy_addr", cfg.ProxyAddr, "admin_addr", cfg.AdminAddr)

	var proxyErr, adminErr error
	var wg sync.WaitGroup
	wg.Add(4)
	go func() {
		defer wg.Done()
		// stop cancels ctx, shutting the other server down when this one exits.
		defer stop()
		proxyErr = proxySrv.Run(ctx, cfg.ProxyAddr)
	}()
	go func() {
		defer wg.Done()
		defer stop()
		adminErr = adminSrv.Run(ctx, cfg.AdminAddr)
	}()
	go func() {
		defer wg.Done()
		db.RunLimiterRefreshLoop(ctx, limitersFresh)
	}()
	go func() {
		defer wg.Done()
		pricesync.RunLoop(ctx, db, est, fetchers, registry)
	}()

	<-ctx.Done()
	slog.Info("shutdown signal received, shutting down")
	wg.Wait()

	exitCode := 0
	if proxyErr != nil {
		slog.Error("proxy server exited with error", "error", proxyErr)
		exitCode = 1
	}
	if adminErr != nil {
		slog.Error("admin server exited with error", "error", adminErr)
		exitCode = 1
	}
	return exitCode
}
