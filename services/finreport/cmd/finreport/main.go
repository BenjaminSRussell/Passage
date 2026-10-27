// Command finreport serves the financial reporting API (wave 15).
//
// Configuration is read from the environment (12-factor):
//
//	DATABASE_URL       required, Postgres URL for the passage_reporting database
//	MIGRATE_ON_START   "true" applies migrations at startup (dev only)
//	HTTP_ADDR          listen address, default :8080
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
	return httpx.Serve(ctx, addr, newHandler(store.New(pool)))
}

// newHandler mounts the reporting API on the platform router, which already
// installs the tenant middleware and the health endpoints.
func newHandler(st api.Store) http.Handler {
	router := httpx.NewRouter()
	api.New(st).Register(router)
	return router
}
