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
	"github.com/BenjaminSRussell/passage/services/close/internal/api"
	"github.com/BenjaminSRussell/passage/services/close/internal/ledger"
	"github.com/BenjaminSRussell/passage/services/close/internal/store"
	"github.com/BenjaminSRussell/passage/services/close/migrations"
)

const (
	defaultHTTPAddr  = ":8080"
	defaultAdminAddr = ":9090"
)

func main() {
	slog.SetDefault(newLogger(os.Stdout))
	if err := run(context.Background(), os.Getenv); err != nil {
		slog.Error("close stopped", "err", err)
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
	return slog.New(handler).With("service", "close")
}

func addr(getenv func(string) string, key, fallback string) string {
	if v := getenv(key); v != "" {
		return v
	}
	return fallback
}

type config struct {
	reopenDefaultDays int
	reopenApprovers   int
	lockWaitMS        int
	reopenSweepEvery  time.Duration
}

func intSetting(getenv func(string) string, key string, fallback, min, max int) (int, error) {
	raw := getenv(key)
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < min || n > max {
		return 0, fmt.Errorf("%s must be an integer from %d to %d, got %q", key, min, max, raw)
	}
	return n, nil
}

func loadConfig(getenv func(string) string) (config, error) {
	var cfg config
	var err error
	if cfg.reopenDefaultDays, err = intSetting(getenv, "CLOSE_REOPEN_DEFAULT_DAYS", 3, 1, 30); err != nil {
		return config{}, err
	}
	if cfg.reopenApprovers, err = intSetting(getenv, "CLOSE_REOPEN_APPROVERS", 2, 2, 3); err != nil {
		return config{}, err
	}
	if cfg.lockWaitMS, err = intSetting(getenv, "CLOSE_LOCK_WAIT_MS", 5000, 1000, 30000); err != nil {
		return config{}, err
	}
	cfg.reopenSweepEvery = time.Minute
	if raw := getenv("CLOSE_REOPEN_SWEEP_INTERVAL"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			return config{}, fmt.Errorf("CLOSE_REOPEN_SWEEP_INTERVAL must be a positive duration such as 1m, got %q", raw)
		}
		cfg.reopenSweepEvery = d
	}
	return cfg, nil
}

func run(ctx context.Context, getenv func(string) string) error {
	if err := tenant.ConfigureServiceVerifier(getenv); err != nil {
		return err
	}
	cfg, err := loadConfig(getenv)
	if err != nil {
		return err
	}
	slog.Info("close configuration", "reopenDefaultDays", cfg.reopenDefaultDays, "reopenApprovers", cfg.reopenApprovers, "lockWaitMs", cfg.lockWaitMS, "reopenSweepInterval", cfg.reopenSweepEvery.String())
	databaseURL := getenv("DATABASE_URL")
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	pool, err := dbx.Connect(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("connect database: %w", err)
	}
	defer pool.Close()
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}
	if err := tenant.ShareReplayCache(ctx, pool); err != nil {
		return err
	}
	a := api.New(store.New(pool))
	if url := getenv("LEDGER_URL"); url != "" {
		signer, err := tenant.ServiceSignerFromEnv(getenv)
		if err != nil {
			return err
		}
		a.WithLedgers(ledger.New(url, nil, signer))
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
