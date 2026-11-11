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

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/services/productcost/internal/api"
	"github.com/BenjaminSRussell/passage/services/productcost/internal/domain"
	"github.com/BenjaminSRussell/passage/services/productcost/internal/engineering"
	"github.com/BenjaminSRussell/passage/services/productcost/internal/inventory"
	"github.com/BenjaminSRussell/passage/services/productcost/internal/store"
	"github.com/BenjaminSRussell/passage/services/productcost/migrations"
)

const (
	defaultHTTPAddr  = ":8080"
	defaultAdminAddr = ":9090"
)

func main() {
	slog.SetDefault(newLogger(os.Stdout))
	if err := run(context.Background(), os.Getenv); err != nil {
		slog.Error("productcost stopped", "err", err)
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
	return slog.New(handler).With("service", "productcost")
}

// config reads the costing environment settings and rejects out-of-range values.
func config(getenv func(string) string) (api.Config, error) {
	cfg := api.Config{MaxLevels: domain.DefaultMaxLevels, Workers: domain.DefaultWorkers}
	for _, setting := range []struct {
		name     string
		min, max int
		target   *int
	}{
		{"COSTING_MAX_LEVELS", 1, 40, &cfg.MaxLevels},
		{"COSTING_ROLL_WORKERS", 1, 32, &cfg.Workers},
	} {
		raw := getenv(setting.name)
		if raw == "" {
			continue
		}
		n, err := strconv.Atoi(raw)
		if err != nil || n < setting.min || n > setting.max {
			return cfg, fmt.Errorf("%s must be an integer from %d to %d, got %q", setting.name, setting.min, setting.max, raw)
		}
		*setting.target = n
	}
	return cfg, nil
}

func ledgerIdempotencyTTLDays(getenv func(string) string) (int, error) {
	raw := getenv("PRODUCTCOST_LEDGER_IDEMPOTENCY_TTL_DAYS")
	if raw == "" {
		return domain.MinLedgerIdempotencyTTLDays, nil
	}
	days, err := strconv.Atoi(raw)
	if err != nil || days < domain.MinLedgerIdempotencyTTLDays || days > domain.MaxLedgerIdempotencyTTLDays {
		return 0, fmt.Errorf("PRODUCTCOST_LEDGER_IDEMPOTENCY_TTL_DAYS must be an integer from %d to %d, got %q", domain.MinLedgerIdempotencyTTLDays, domain.MaxLedgerIdempotencyTTLDays, raw)
	}
	return days, nil
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
	ttlDays, err := ledgerIdempotencyTTLDays(getenv)
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
	a := api.New(pool, cfg).WithStore(store.New(pool).WithLedgerIdempotencyTTLDays(ttlDays))
	onHand, err := inventory.FromEnv(getenv)
	if err != nil {
		return fmt.Errorf("inventory client: %w", err)
	}
	if onHand != nil {
		a.WithInventory(onHand)
	}
	design, err := engineering.FromEnv(getenv)
	if err != nil {
		return fmt.Errorf("engineering client: %w", err)
	}
	if design != nil {
		a.WithEngineering(design)
	}
	ledger, err := ledgerclient.FromEnv(getenv, "productcost")
	if err != nil {
		return fmt.Errorf("ledger client: %w", err)
	}
	if ledger != nil {
		a.WithLedger(ledger)
	}
	router := httpx.NewRouter()
	a.Register(router)
	admin := http.NewServeMux()
	admin.Handle("/metrics", httpx.WithMetrics(a.Metrics()))

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
