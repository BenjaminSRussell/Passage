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
	"github.com/BenjaminSRussell/passage/platform/issueclaim"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/tenant"
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
	runner := run
	if len(os.Args) > 1 {
		if os.Args[1] != "consume" {
			slog.Error("production stopped", "err", fmt.Errorf("unknown command %q (use no argument to serve or \"consume\" to run the planned order consumer)", os.Args[1]))
			os.Exit(1)
		}
		runner = consume
	}
	if err := runner(context.Background(), os.Getenv); err != nil {
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
// starts a Released order, default true; PRODUCTION_ESTIMATE_ON_CREATE, whether
// a new order is estimated in its create transaction, default false.
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
	switch raw := getenv("PRODUCTION_ESTIMATE_ON_CREATE"); raw {
	case "", "false":
	case "true":
		cfg.EstimateOnCreate = true
	default:
		return cfg, fmt.Errorf("PRODUCTION_ESTIMATE_ON_CREATE must be true or false, got %q", raw)
	}
	return cfg, nil
}

func run(ctx context.Context, getenv func(string) string) error {
	if err := tenant.ConfigureServiceVerifier(getenv); err != nil {
		return err
	}
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
	if err := tenant.ShareReplayCache(ctx, pool); err != nil {
		return err
	}
	holds, err := issueclaim.FromEnv(getenv)
	if err != nil {
		return fmt.Errorf("inventory client: %w", err)
	}
	st := store.New(pool).WithHolds(holds)
	a := api.New(st, cfg)
	ledger, err := ledgerclient.FromEnv(getenv, "production")
	if err != nil {
		return fmt.Errorf("ledger client: %w", err)
	}
	if ledger != nil {
		a.WithGL(st, ledger)
	}
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

// consume runs the planned order consumer (FR-22.10): it follows planning's plannedorder.firmed.v1 into
// the production intake. It does not migrate and serves no API; its admin port has health and metrics.
func consume(ctx context.Context, getenv func(string) string) error {
	databaseURL := getenv("DATABASE_URL")
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	pool, err := dbx.Connect(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("connect database: %w", err)
	}
	defer pool.Close()
	return runConsumer(ctx, getenv, store.New(pool), pool)
}
