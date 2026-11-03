// Command receivables runs the Passage receivables service: cash application,
// open items and aging (wave 30 slice 1).
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/api"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/store"
	"github.com/BenjaminSRussell/passage/services/receivables/migrations"
)

func main() {
	if err := run(); err != nil {
		slog.Error("receivables exited", "err", err)
		os.Exit(1)
	}
}

func run() error {
	ctx := context.Background()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		return errors.New("DATABASE_URL is required")
	}
	addr := os.Getenv("HTTP_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	adminAddr := os.Getenv("ADMIN_ADDR")
	if adminAddr == "" {
		adminAddr = ":9090"
	}
	pool, err := dbx.Connect(ctx, url)
	if err != nil {
		return err
	}
	defer pool.Close()
	if os.Getenv("MIGRATE_ON_START") == "true" {
		if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
			return err
		}
	}
	s := store.New(pool)
	if gl, err := ledgerclient.FromEnv(os.Getenv, "receivables"); err != nil {
		return err
	} else if gl != nil {
		s.WithLedger(gl)
	}
	h := api.New(s)
	r := httpx.NewRouter()
	h.Register(r)
	admin := http.NewServeMux()
	admin.Handle("/metrics", h.Metrics())

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errs := make(chan error, 2)
	go func() { errs <- httpx.Serve(ctx, addr, r) }()
	go func() { errs <- httpx.Serve(ctx, adminAddr, admin) }()
	return <-errs
}
