package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/quality/internal/api"
	"github.com/BenjaminSRussell/passage/services/quality/internal/inventory"
	"github.com/BenjaminSRussell/passage/services/quality/internal/store"
	"github.com/BenjaminSRussell/passage/services/quality/migrations"
)

const (
	defaultHTTPAddr  = ":8080"
	defaultAdminAddr = ":9090"

	defaultHoldSweep = time.Minute
	holdStaleAfter   = 5 * time.Minute
)

func main() {
	slog.SetDefault(newLogger(os.Stdout))
	if err := run(context.Background(), os.Getenv); err != nil {
		slog.Error("quality stopped", "err", err)
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
	return slog.New(handler).With("service", "quality")
}

func run(ctx context.Context, getenv func(string) string) error {
	databaseURL := getenv("DATABASE_URL")
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required")
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
	st, err := newStore(pool, getenv)
	if err != nil {
		return err
	}
	a := api.New(st)
	router := httpx.NewRouter()
	a.Register(router)
	admin := http.NewServeMux()
	admin.Handle("/metrics", a.Metrics())

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	interval, err := holdSweepInterval(getenv)
	if err != nil {
		return err
	}
	go st.RunHoldSweeper(ctx, splitList(getenv("QUALITY_HOLD_TENANTS")), interval, holdStaleAfter)
	errs := make(chan error, 2)
	go func() { errs <- httpx.Serve(ctx, addr(getenv, "HTTP_ADDR", defaultHTTPAddr), router) }()
	go func() { errs <- httpx.Serve(ctx, addr(getenv, "ADMIN_ADDR", defaultAdminAddr), admin) }()
	return <-errs
}

func newStore(pool *pgxpool.Pool, getenv func(string) string) (*store.Postgres, error) {
	st := store.New(pool)
	url := getenv("INVENTORY_URL")
	if url == "" {
		slog.Warn("INVENTORY_URL is empty: failed inspections and nonconformances do not hold stock in inventory")
		return st, nil
	}
	if len(splitList(getenv("QUALITY_HOLD_TENANTS"))) == 0 {
		return nil, errors.New("INVENTORY_URL needs QUALITY_HOLD_TENANTS, the comma-separated tenant UUIDs whose queued hold calls the sweeper sends (tenants cannot be listed under row-level security)")
	}
	signer, err := tenant.ServiceSignerFromEnv(getenv)
	if err != nil {
		return nil, fmt.Errorf("service signer: %w", err)
	}
	if signer == nil {
		return nil, errors.New("INVENTORY_URL needs PASSAGE_SERVICE_SIGNING_KEY or PASSAGE_SERVICE_SIGNING_KEY_FILE: inventory accepts hold calls only with a signed service token")
	}
	return st.WithHolds(inventory.New(url, signer, nil)), nil
}

func holdSweepInterval(getenv func(string) string) (time.Duration, error) {
	raw := getenv("QUALITY_HOLD_SWEEP_INTERVAL")
	if raw == "" {
		return defaultHoldSweep, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < 5*time.Second {
		return 0, fmt.Errorf("QUALITY_HOLD_SWEEP_INTERVAL must be a duration of at least 5s, got %q", raw)
	}
	return d, nil
}

func splitList(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func addr(getenv func(string) string, key, fallback string) string {
	if v := getenv(key); v != "" {
		return v
	}
	return fallback
}
