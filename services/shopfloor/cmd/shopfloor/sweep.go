package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/api"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/store"
)

// sweepConfig reads the sweeper's environment. SWEEP_TENANT_IDS names the tenants (comma-separated
// UUIDs); the intervals are Go durations, "0" switching a job off.
func sweepConfig(getenv func(string) string) (api.SweepConfig, error) {
	tenants, err := api.ParseSweepTenants(getenv("SWEEP_TENANT_IDS"))
	if err != nil {
		return api.SweepConfig{}, fmt.Errorf("SWEEP_TENANT_IDS: %w", err)
	}
	cfg := api.DefaultSweepConfig(tenants)
	for key, dst := range map[string]*time.Duration{
		"SWEEP_ANDON_INTERVAL": &cfg.AndonEvery, "SWEEP_ROLLUP_INTERVAL": &cfg.RollupEvery,
		"SWEEP_AUTO_CLOCK_INTERVAL": &cfg.AutoClockEvery, "SWEEP_RETRY_INTERVAL": &cfg.RetryEvery,
	} {
		v := getenv(key)
		if v == "" {
			continue
		}
		if v == "0" {
			*dst = 0
			continue
		}
		d, err := time.ParseDuration(v)
		if err != nil {
			return api.SweepConfig{}, fmt.Errorf("%s must be a duration such as 1m, or 0 to switch the job off, got %q", key, v)
		}
		*dst = d
	}
	if v := getenv("SWEEP_AUTO_CLOCK_AFTER_HOURS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return api.SweepConfig{}, fmt.Errorf("SWEEP_AUTO_CLOCK_AFTER_HOURS must be a whole number of hours, got %q", v)
		}
		cfg.AutoClockAfterHour = n
	}
	if err := cfg.Validate(); err != nil {
		return api.SweepConfig{}, err
	}
	return cfg, nil
}

// runSweeper runs the scheduled jobs (andon, OEE rollup, auto clock-out, process message retry) for
// the configured tenants and serves the sweeper's admin port.
func runSweeper(ctx context.Context, getenv func(string) string, s *store.Postgres) error {
	cfg, err := sweepConfig(getenv)
	if err != nil {
		return err
	}
	a := api.New(s)
	sw, err := a.NewSweeper(cfg)
	if err != nil {
		return err
	}
	admin := http.NewServeMux()
	admin.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	admin.Handle("/metrics", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.Metrics().ServeHTTP(w, r)
		sw.WriteMetrics(w)
	}))
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errs := make(chan error, 2)
	go func() { errs <- httpx.Serve(ctx, addr(getenv, "ADMIN_ADDR", defaultAdminAddr), admin) }()
	go func() {
		slog.Info("shopfloor sweeper started", "tenants", len(cfg.TenantIDs))
		sw.Run(ctx)
		errs <- errors.New("sweeper stopped")
	}()
	return <-errs
}
