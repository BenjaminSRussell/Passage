package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strconv"
	"time"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/planning/internal/api"
	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
	"github.com/BenjaminSRussell/passage/services/planning/internal/forecastclient"
	"github.com/BenjaminSRussell/passage/services/planning/internal/store"
	"github.com/BenjaminSRussell/passage/services/planning/migrations"
)

const (
	defaultHTTPAddr  = ":8080"
	defaultAdminAddr = ":9090"
	// defaultHorizonDays is the default of PLANNING_HORIZON_DAYS (PRD 4.22.13).
	defaultHorizonDays = 365
	// maxRunTimeoutMinutes bounds PLANNING_RUN_TIMEOUT_MINUTES (default 60).
	maxRunTimeoutMinutes = 720
	// runStopWait is how long shutdown waits for interrupted runs to record it.
	runStopWait = 10 * time.Second
)

func main() {
	slog.SetDefault(newLogger(os.Stdout))
	if err := run(context.Background(), os.Getenv); err != nil {
		slog.Error("planning stopped", "err", err)
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
	return slog.New(handler).With("service", "planning")
}

func run(ctx context.Context, getenv func(string) string) error {
	databaseURL := getenv("DATABASE_URL")
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	maxVersions, err := intEnv(getenv, "PLANNING_MAX_VERSIONS", store.DefaultMaxVersions, store.MinMaxVersions, store.MaxMaxVersions)
	if err != nil {
		return err
	}
	horizon, err := intEnv(getenv, "PLANNING_HORIZON_DAYS", defaultHorizonDays, domain.MinHorizonDays, domain.MaxHorizonDays)
	if err != nil {
		return err
	}
	concurrentRuns, err := intEnv(getenv, "PLANNING_MAX_CONCURRENT_RUNS", api.DefaultMaxConcurrentRuns, 1, api.MaxMaxConcurrentRuns)
	if err != nil {
		return err
	}
	runTimeout, err := intEnv(getenv, "PLANNING_RUN_TIMEOUT_MINUTES", int(api.DefaultRunTimeout/time.Minute), 1, maxRunTimeoutMinutes)
	if err != nil {
		return err
	}
	flags, err := featureFlags(getenv("PLANNING_FEATURE_FLAGS"))
	if err != nil {
		return err
	}
	slog.Info("feature flags", "enabled", flags)
	// The flags are per deployment (the platform has no tenant flag service
	// yet), so a missing or malformed FORECAST_URL with the flag on is a
	// configuration error of the whole process, refused before connecting.
	var forecaster *forecastclient.Client
	if slices.Contains(flags, flagStatisticalForecast) {
		if getenv("FORECAST_URL") == "" {
			return errors.New("FORECAST_URL is required while planning.statistical-forecast is enabled")
		}
		if forecaster, err = forecastclient.New(getenv("FORECAST_URL")); err != nil {
			return fmt.Errorf("FORECAST_URL: %w", err)
		}
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
	a := api.New(store.New(pool).WithMaxVersions(maxVersions)).WithHorizon(horizon).WithRunLimits(concurrentRuns, time.Duration(runTimeout)*time.Minute)
	if forecaster != nil {
		a = a.WithStatisticalForecast(forecaster)
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
	err = <-errs
	// Asynchronous plan runs still executing record that they were
	// interrupted while the pool is open.
	a.StopRuns(runStopWait)
	return err
}

func addr(getenv func(string) string, key, fallback string) string {
	if v := getenv(key); v != "" {
		return v
	}
	return fallback
}

// intEnv reads an integer setting in [lo, hi], or fallback when it is unset.
func intEnv(getenv func(string) string, key string, fallback, lo, hi int) (int, error) {
	raw := getenv(key)
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < lo || n > hi {
		return 0, fmt.Errorf("%s must be an integer from %d to %d, got %q", key, lo, hi, raw)
	}
	return n, nil
}
