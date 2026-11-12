// Command fulfillment serves the fulfillment API: release, picking, packing and
// packing-slip posting. Startup validates configuration and exits non-zero on error.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strconv"
	"time"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/issueclaim"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/api"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/carrier"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/salesreport"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/store"
	"github.com/BenjaminSRussell/passage/services/fulfillment/migrations"
)

var gs1Prefix = regexp.MustCompile(`^[0-9]{6,12}$`)

func main() {
	if err := run(); err != nil {
		slog.Error("fulfillment stopped", "err", err)
		os.Exit(1)
	}
}

func run() error {
	ctx := context.Background()
	consume := len(os.Args) > 1 && os.Args[1] == "consume"
	if len(os.Args) > 1 && !consume {
		return fmt.Errorf("unknown command %q (use no argument to serve or \"consume\" to run the sales order consumer)", os.Args[1])
	}
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return errors.New("DATABASE_URL is required")
	}
	if consume {
		// The consumer only stores sales' order types: it has no HTTP API, no SSCC or release
		// settings and no service token verifier.
		pool, err := dbx.Connect(ctx, dsn)
		if err != nil {
			return fmt.Errorf("connect: %w", err)
		}
		defer pool.Close()
		return runConsumer(ctx, os.Getenv, store.New(pool, "", 0), pool)
	}
	if err := tenant.ConfigureServiceVerifier(os.Getenv); err != nil {
		return err
	}
	prefix := os.Getenv("GS1_COMPANY_PREFIX")
	if !gs1Prefix.MatchString(prefix) {
		return errors.New("GS1_COMPANY_PREFIX must be 6 to 12 digits")
	}
	ext, err := intEnv("SSCC_EXTENSION_DIGIT", 0, 0, 9)
	if err != nil {
		return err
	}
	horizon, err := intEnv("RELEASE_HORIZON_DAYS", 2, 0, 30)
	if err != nil {
		return err
	}
	fullATP := os.Getenv("RELEASE_REQUIRES_FULL_ATP") != "false"

	pool, err := dbx.Connect(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer pool.Close()
	if os.Getenv("MIGRATE_ON_START") == "true" {
		if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}

	holds, err := issueclaim.FromEnv(os.Getenv)
	if err != nil {
		return fmt.Errorf("inventory client: %w", err)
	}
	carriers, err := carrier.FromEnv(os.Getenv)
	if err != nil {
		return fmt.Errorf("carrier-gateway client: %w", err)
	}
	sales, err := salesreport.FromEnv(os.Getenv)
	if err != nil {
		return fmt.Errorf("sales client: %w", err)
	}
	interval := salesreport.DefaultInterval
	if raw := os.Getenv("SALES_REPORT_INTERVAL"); raw != "" {
		if interval, err = time.ParseDuration(raw); err != nil || interval < time.Second {
			return errors.New("SALES_REPORT_INTERVAL must be a duration of at least 1s")
		}
	}
	s := store.New(pool, prefix, ext).WithReleaseDefaults(horizon, fullATP).WithHolds(holds)
	if sales != nil {
		// Every replica runs the reporter: reports are leased, so each is sent by one at a time (#496).
		reporter := &salesreport.Reporter{Store: s, Sales: sales}
		go salesreport.Run(ctx, interval, reporter.RunOnce)
		slog.Info("sales delivery reporter running", "interval", interval)
	} else {
		slog.Warn("SALES_URL is unset: posted packing slips are queued for sales but not reported")
	}
	if err := tenant.ShareReplayCache(ctx, pool); err != nil {
		return err
	}
	r := httpx.NewRouter()
	api.New(s, api.Options{ReleaseHorizonDays: horizon, RequireFullATP: fullATP, Carrier: carriers}).Routes(r)

	addr := os.Getenv("HTTP_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	slog.Info("fulfillment listening", "addr", addr)
	return httpx.ServeWithMetrics(ctx, addr, r)
}

func intEnv(name string, def, lo, hi int) (int, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < lo || n > hi {
		return 0, fmt.Errorf("%s must be an integer from %d to %d", name, lo, hi)
	}
	return n, nil
}
