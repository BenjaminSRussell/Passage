package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/production/internal/api"
	"github.com/BenjaminSRussell/passage/services/production/internal/store"
	"github.com/BenjaminSRussell/passage/services/production/migrations"
)

const (
	defaultHTTPAddr  = ":8080"
	defaultAdminAddr = ":9090"
)

func main() {
	slog.SetDefault(newLogger(os.Stdout))
	if err := run(context.Background(), os.Getenv); err != nil {
		slog.Error("production stopped", "err", err)
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
	return slog.New(handler).With("service", "production")
}

// config reads the service parameters (PRD 4.21.10, 4.21.13):
// PRODUCTION_RAF_TOLERANCE_PCT, the over-delivery allowed on report as
// finished, 0 to 100, default 0; PRODUCTION_OVERCONSUMPTION_PCT, the picking
// beyond the requirement allowed without the over-consumption duty, 0 to
// 1000, default 10; PRODUCTION_AUTOSTART_ON_FEEDBACK, whether a route card
// starts a Released order, default true.
func config(getenv func(string) string) (api.Config, error) {
	cfg := api.Config{RAFTolerancePct: decimal.Zero, OverConsumptionPct: decimal.NewFromInt(10), AutoStartOnFeedback: true}
	pct := func(name string, max int64, into *decimal.Decimal) error {
		raw := getenv(name)
		if raw == "" {
			return nil
		}
		v, err := decimal.NewFromString(raw)
		if err != nil || v.IsNegative() || v.GreaterThan(decimal.NewFromInt(max)) {
			return fmt.Errorf("%s must be a number from 0 to %d, got %q", name, max, raw)
		}
		*into = v
		return nil
	}
	if err := pct("PRODUCTION_RAF_TOLERANCE_PCT", 100, &cfg.RAFTolerancePct); err != nil {
		return cfg, err
	}
	if err := pct("PRODUCTION_OVERCONSUMPTION_PCT", 1000, &cfg.OverConsumptionPct); err != nil {
		return cfg, err
	}
	switch raw := getenv("PRODUCTION_AUTOSTART_ON_FEEDBACK"); raw {
	case "", "true":
	case "false":
		cfg.AutoStartOnFeedback = false
	default:
		return cfg, fmt.Errorf("PRODUCTION_AUTOSTART_ON_FEEDBACK must be true or false, got %q", raw)
	}
	return cfg, nil
}

func run(ctx context.Context, getenv func(string) string) error {
	databaseURL := getenv("DATABASE_URL")
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	cfg, err := config(getenv)
	if err != nil {
		return err
	}
	pool, err := dbx.Connect(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("connect database: %w", err)
	}
	defer pool.Close()
	if getenv("MIGRATE_ON_START") == "true" {
		if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	a := api.New(store.New(pool), cfg)
	router := httpx.NewRouter()
	a.Register(router)
	admin := http.NewServeMux()
	admin.Handle("/metrics", a.Metrics())

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errs := make(chan error, 2)
	go func() { errs <- httpx.Serve(ctx, addr(getenv, "HTTP_ADDR", defaultHTTPAddr), router) }()
	go func() { errs <- httpx.Serve(ctx, addr(getenv, "ADMIN_ADDR", defaultAdminAddr), admin) }()
	return <-errs
}

func addr(getenv func(string) string, key, fallback string) string {
	if v := getenv(key); v != "" {
		return v
	}
	return fallback
}
