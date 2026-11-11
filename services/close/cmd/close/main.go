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
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/close/internal/api"
	"github.com/BenjaminSRussell/passage/services/close/internal/ledger"
	"github.com/BenjaminSRussell/passage/services/close/internal/store"
	"github.com/BenjaminSRussell/passage/services/close/migrations"
)

const (
	defaultHTTPAddr  = ":8080"
	defaultAdminAddr = ":9090"
	ledgerSource     = "close"
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
	accrualSweepEvery time.Duration
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
	cfg.accrualSweepEvery = cfg.reopenSweepEvery
	if raw := getenv("CLOSE_ACCRUAL_SWEEP_INTERVAL"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			return config{}, fmt.Errorf("CLOSE_ACCRUAL_SWEEP_INTERVAL must be a positive duration such as 1m, got %q", raw)
		}
		cfg.accrualSweepEvery = d
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
	slog.Info("close configuration", "reopenDefaultDays", cfg.reopenDefaultDays, "reopenApprovers", cfg.reopenApprovers, "lockWaitMs", cfg.lockWaitMS, "reopenSweepInterval", cfg.reopenSweepEvery.String(), "accrualSweepInterval", cfg.accrualSweepEvery.String())
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
	st := store.New(pool)
	a := api.New(st).WithConfig(api.Config{
		ReopenDays: cfg.reopenDefaultDays,
		Approvers:  cfg.reopenApprovers,
		LockWait:   time.Duration(cfg.lockWaitMS) * time.Millisecond,
	}).WithSliceB(st, st, st, st, st, nil)
	if url := getenv("LEDGER_URL"); url != "" {
		signer, err := tenant.ServiceSignerFromEnv(getenv)
		if err != nil {
			return err
		}
		a.WithLedgers(ledger.New(url, nil, signer))
		postings, err := ledgerclient.New(url, ledgerSource, signer, nil)
		if err != nil {
			return err
		}
		a.WithYearEnd(st, postings)
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
	go sweepEvery(ctx, cfg.reopenSweepEvery, "reopen expiry", a.ExpireReopened)
	go sweepEvery(ctx, cfg.accrualSweepEvery, "accrual posting", a.PostDueAccruals)
	return <-errs
}

func sweepEvery(ctx context.Context, every time.Duration, name string, work func(context.Context) (int, error)) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n, err := work(ctx)
			if err != nil {
				slog.Error(name+" sweep failed", "err", err)
			}
			if n > 0 {
				slog.Info(name+" sweep finished", "count", n)
			}
		}
	}
}
