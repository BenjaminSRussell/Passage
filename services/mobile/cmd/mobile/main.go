package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/mobile/internal/api"
	"github.com/BenjaminSRussell/passage/services/mobile/internal/session"
	"github.com/BenjaminSRussell/passage/services/mobile/internal/store"
	"github.com/BenjaminSRussell/passage/services/mobile/internal/warehouse"
	"github.com/BenjaminSRussell/passage/services/mobile/migrations"
)

const (
	defaultHTTPAddr  = ":8080"
	defaultAdminAddr = ":9090"
)

func main() {
	slog.SetDefault(newLogger(os.Stdout))
	if err := run(context.Background(), os.Getenv); err != nil {
		slog.Error("mobile stopped", "err", err)
		os.Exit(1)
	}
}

func newLogger(w io.Writer) *slog.Logger {
	renamed := map[string]string{slog.TimeKey: "timestamp", slog.MessageKey: "message"}
	handler := slog.NewJSONHandler(w, &slog.HandlerOptions{ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
		if name, ok := renamed[a.Key]; ok {
			a.Key = name
		}
		return a
	}})
	return slog.New(handler).With("service", "mobile")
}

// run serves the API and the admin port. DATABASE_URL, WAREHOUSE_URL and a
// service signing key are required: the service has nothing to offer without
// its ledger and the work API it drives.
func run(ctx context.Context, getenv func(string) string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	if err := tenant.ConfigureServiceVerifier(getenv); err != nil {
		return err
	}
	idle, err := idleTimeout(getenv("MOBILE_IDLE_TIMEOUT_MIN"))
	if err != nil {
		return err
	}
	url, warehouseURL := getenv("DATABASE_URL"), getenv("WAREHOUSE_URL")
	if url == "" || warehouseURL == "" {
		return errors.New("DATABASE_URL and WAREHOUSE_URL are required")
	}
	signer, err := tenant.ServiceSignerFromEnv(getenv)
	if err != nil {
		return err
	}
	wh, err := warehouse.New(warehouseURL, signer, nil)
	if err != nil {
		return err
	}
	pool, err := dbx.Connect(ctx, url)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer pool.Close()
	if getenv("MIGRATE_ON_START") == "true" {
		if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	if err := tenant.ShareReplayCache(ctx, pool); err != nil {
		return fmt.Errorf("replay cache: %w", err)
	}
	a := api.New(store.New(pool), wh, idle)
	router := httpx.NewRouter()
	a.Register(router)
	admin := http.NewServeMux()
	admin.Handle("/metrics", a.Metrics())

	errs := make(chan error, 2)
	go func() { errs <- httpx.Serve(ctx, addr(getenv, "HTTP_ADDR", defaultHTTPAddr), router) }()
	go func() { errs <- httpx.Serve(ctx, addr(getenv, "ADMIN_ADDR", defaultAdminAddr), admin) }()
	return <-errs
}

// idleTimeout reads MOBILE_IDLE_TIMEOUT_MIN (5 to 240, default 30).
func idleTimeout(v string) (time.Duration, error) {
	if v == "" {
		return session.DefaultIdleMinutes * time.Minute, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < session.MinIdleMinutes || n > session.MaxIdleMinutes {
		return 0, fmt.Errorf("MOBILE_IDLE_TIMEOUT_MIN must be between %d and %d, got %q", session.MinIdleMinutes, session.MaxIdleMinutes, v)
	}
	return session.Idle(n), nil
}

func addr(getenv func(string) string, key, fallback string) string {
	if v := getenv(key); v != "" {
		return v
	}
	return fallback
}
