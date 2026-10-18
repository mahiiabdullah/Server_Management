// Command api is the BM Monitor backend.
//
//	api              start the server (default)
//	api healthcheck  exit 0 if the local /readyz answers 200 (Docker health check)
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
	_ "time/tzdata" // the distroless image has no zoneinfo; BILLING_TIMEZONE needs it

	"github.com/jackc/pgx/v5/pgxpool" // the distroless image has no zoneinfo; BILLING_TIMEZONE needs it

	"bm-monitor/api/internal/accessreq"
	"bm-monitor/api/internal/alerts"
	"bm-monitor/api/internal/auth"
	"bm-monitor/api/internal/config"
	"bm-monitor/api/internal/db"
	"bm-monitor/api/internal/ghclient"
	"bm-monitor/api/internal/httpapi"
	"bm-monitor/api/internal/inventory"
	"bm-monitor/api/internal/metrics"
	"bm-monitor/api/internal/targets"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck())
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	// 1. Config, validated before anything else starts.
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// 2. Database and migrations.
	pool, err := db.Open(ctx, cfg.DatabaseURL, 60*time.Second)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool, log); err != nil {
		return err
	}
	for key, val := range map[string]any{
		"billing_timezone": cfg.BillingTimezone.String(),
		"billing_currency": cfg.BillingCurrency,
	} {
		if err := db.SeedSetting(ctx, pool, key, val); err != nil {
			return fmt.Errorf("seed setting %s: %w", key, err)
		}
	}

	// 3. First admin.
	if err := auth.EnsureAdmin(ctx, pool, cfg.AdminEmail, cfg.AdminPassword, log); err != nil {
		return fmt.Errorf("create first admin: %w", err)
	}

	// 4. Background workers. Each stops when workerCtx is cancelled.
	authSvc := auth.NewService(pool, cfg.CSRFSecret, cfg.SessionIdleTimeout, cfg.SessionMaxAge)
	prom := metrics.NewClient(cfg.PrometheusURL)
	tw := targets.NewWriter(pool, cfg.TargetsFile, log.With("worker", "targets"))
	accessSvc, err := accessService(cfg, pool, log)
	if err != nil {
		return err
	}
	api := httpapi.New(httpapi.Deps{Config: cfg, Pool: pool, Auth: authSvc, Prom: prom, Targets: tw, Log: log, Access: accessSvc})
	evaluator := alerts.NewEvaluator(pool, prom, api.Inventory(), api.Costs(), log.With("worker", "alerts"))
	notifier := alerts.NewNotifier(pool, alerts.NewSenders(cfg), alerts.NewLinks(cfg.SessionSecret), cfg.PublicURL.String(), log.With("worker", "notifier"))

	workerCtx, stopWorkers := context.WithCancel(context.Background())
	var workers sync.WaitGroup
	workers.Go(func() { authSvc.RunCleanup(workerCtx, 10*time.Minute, log.With("worker", "session_cleanup")) })
	workers.Go(func() { tw.Run(workerCtx, 60*time.Second) })
	workers.Go(func() { evaluator.Run(workerCtx, cfg.AlertEvalInterval) })
	workers.Go(func() { notifier.Run(workerCtx, 10*time.Second) })
	workers.Go(func() { api.Costs().RunCloser(workerCtx) })
	workers.Go(func() { fillCapacity(workerCtx, prom, api.Inventory(), log.With("worker", "capacity")) })
	workers.Go(func() { accessSvc.Run(workerCtx, time.Minute) })

	// 5. HTTP server.
	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	serveErr := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.ListenAddr)
		serveErr <- srv.ListenAndServe()
	}()

	// 6. Graceful shutdown: stop accepting, drain for up to 20 s, then stop workers and close the DB.
	select {
	case err := <-serveErr:
		stopWorkers()
		workers.Wait()
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
	}
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("http shutdown incomplete", "err", err)
	}
	stopWorkers()
	workers.Wait()
	log.Info("stopped")
	return nil
}

// fillCapacity copies observed cores, memory and disk size into servers whose
// provisioned capacity is still empty (it never overwrites values a user entered).
func fillCapacity(ctx context.Context, prom *metrics.Client, inv *inventory.Store, log *slog.Logger) {
	t := time.NewTicker(2 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		snap, err := prom.Snapshot(ctx)
		if err != nil {
			continue
		}
		servers, err := inv.List(ctx)
		if err != nil {
			continue
		}
		for _, s := range servers {
			cur := snap.Servers[s.ID]
			if cur == nil || (s.CPUCores != nil && s.MemoryBytes != nil && s.DiskBytes != nil) {
				continue
			}
			var cores *int
			var mem, disk *int64
			if cur.Cores != nil {
				cores = ptr(int(*cur.Cores))
			}
			if cur.MemTotalBytes != nil {
				mem = ptr(int64(*cur.MemTotalBytes))
			}
			if cur.DiskTotalBytes != nil {
				disk = ptr(int64(*cur.DiskTotalBytes))
			}
			if ok, err := inv.FillCapacity(ctx, s.ID, cores, mem, disk); err != nil {
				log.Error("capacity fill failed", "server", s.Name, "err", err)
			} else if ok {
				log.Info("capacity filled from first scrape", "server", s.Name)
			}
		}
	}
}

func ptr[T any](v T) *T { return &v }

// accessService wires the SSH access workflow to GitHub, if configured.
func accessService(cfg *config.Config, pool *pgxpool.Pool, log *slog.Logger) (*accessreq.Service, error) {
	if !cfg.AccessWorkflow() {
		return nil, nil
	}
	var ts ghclient.TokenSource
	if cfg.GitHubAppID != 0 {
		key, err := os.ReadFile(cfg.GitHubAppKeyFile)
		if err != nil {
			return nil, fmt.Errorf("GITHUB_APP_PRIVATE_KEY_FILE: %w", err)
		}
		if ts, err = ghclient.AppTokenSource(cfg.GitHubAPIURL, cfg.GitHubAppID, cfg.GitHubInstallationID, key); err != nil {
			return nil, err
		}
	} else {
		ts = ghclient.StaticToken(cfg.GitHubToken)
	}
	log.Info("SSH access workflow enabled", "repo", cfg.GitHubRepo, "auth", map[bool]string{true: "github-app", false: "token"}[cfg.GitHubAppID != 0])
	return accessreq.New(pool, ghclient.New(cfg.GitHubAPIURL, cfg.GitHubRepo, ts), log.With("worker", "access")), nil
}

func healthcheck() int {
	addr := os.Getenv("LISTEN_ADDR")
	switch {
	case addr == "":
		addr = "127.0.0.1:8080"
	case addr[0] == ':':
		addr = "127.0.0.1" + addr
	}
	c := &http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get("http://" + addr + "/readyz")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "readyz:", resp.Status)
		return 1
	}
	return 0
}
