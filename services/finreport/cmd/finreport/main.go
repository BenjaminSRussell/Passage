// Command finreport serves the financial reporting API (wave 15).
//
// Configuration is read from the environment (12-factor):
//
//	DATABASE_URL       required, Postgres URL for the passage_reporting database
//	MIGRATE_ON_START   "true" applies migrations at startup (dev only)
//	HTTP_ADDR          listen address, default :8080
//	REPORTING_*        tunables, validated at startup (see internal/config)
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/api"
	"github.com/BenjaminSRussell/passage/services/finreport/internal/config"
	"github.com/BenjaminSRussell/passage/services/finreport/internal/store"
	"github.com/BenjaminSRussell/passage/services/finreport/migrations"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "finreport"))
	if err := run(ctx, os.Getenv); err != nil {
		slog.Error("finreport stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, getenv func(string) string) error {
	// Reporting tunables are validated before any connection is made, so an
	// out-of-range REPORTING_* value stops the pod at startup (PRD §2.15.13).
	cfg, err := config.FromEnv(getenv)
	if err != nil {
		return err
	}
	slog.Info("reporting configuration", "config", cfg)
	url := getenv("DATABASE_URL")
	if url == "" {
		return errors.New("DATABASE_URL is required")
	}
	pool, err := dbx.Connect(ctx, url)
	if err != nil {
		return err
	}
	defer pool.Close()
	if getenv("MIGRATE_ON_START") == "true" {
		if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
			return err
		}
	}
	addr := getenv("HTTP_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	return httpx.Serve(ctx, addr, newHandler(store.New(pool), cfg))
}

// newHandler mounts the reporting API on the platform router, which already
// installs the tenant middleware and the health endpoints.
func newHandler(st api.Store, cfg config.Reporting) http.Handler {
	router := httpx.NewRouter()
	api.New(st).WithConfig(cfg).Register(router)
	return router
}
