package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/billing/internal/api"
	"github.com/BenjaminSRussell/passage/services/billing/internal/revrec"
	"github.com/BenjaminSRussell/passage/services/billing/internal/store"
	"github.com/BenjaminSRussell/passage/services/billing/migrations"
)

const (
	defaultHTTPAddr  = ":8080"
	defaultAdminAddr = ":9090"
	defaultMaxLines  = 2000
)

func main() {
	slog.SetDefault(newLogger(os.Stdout))
	if err := run(context.Background(), os.Getenv); err != nil {
		slog.Error("billing stopped", "err", err)
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
	return slog.New(handler).With("service", "billing")
}

// run serves the billing API on HTTP_ADDR and the metrics on ADMIN_ADDR. The
// tax engine is the rate table in BILLING_TAX_RATES when set, otherwise the
// fail-closed UnavailableTax until the wave-13 tax client is wired, so no
// invoice posts with silent zero tax.
func run(ctx context.Context, getenv func(string) string) error {
	if err := tenant.ConfigureServiceVerifier(getenv); err != nil {
		return err
	}
	databaseURL := getenv("DATABASE_URL")
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	maxLines, err := api.ParseMaxLines(getenv("BILLING_MAX_LINES"), defaultMaxLines)
	if err != nil {
		return err
	}
	tax, err := api.ParseTaxRates(getenv("BILLING_TAX_RATES"))
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
	st := store.New(pool)
	a := api.New(st, tax, maxLines).WithSubscriptions(st)
	var glProfiles map[string]store.GLProfile
	ledger, err := ledgerclient.FromEnv(getenv, "billing")
	if err != nil {
		return fmt.Errorf("ledger client: %w", err)
	}
	if ledger != nil {
		profiles, err := api.ParseGLProfiles(getenv("BILLING_GL_PROFILES"))
		if err != nil {
			return err
		}
		a.WithLedger(ledger, st, profiles)
	}
	allocations, err := revrec.FromEnv(getenv)
	if err != nil {
		return fmt.Errorf("revrec client: %w", err)
	}
	if ledger != nil && allocations == nil {
		// Every profile names deferred revenue accounts and every order line books its revrec allocation: without revrec the
		// invoice journal would hold every order invoice (BIL-0207), so the service refuses to start half configured.
		return fmt.Errorf("REVREC_URL is required when LEDGER_URL is set: order invoices book the revrec allocation")
	}
	if allocations != nil {
		a.WithAllocations(allocations)
	}
	autobook, err := newAutoBook(getenv, glProfiles, a)
	if err != nil {
		return err
	}
	router := httpx.NewRouter()
	a.Register(router)
	admin := http.NewServeMux()
	admin.Handle("/metrics", a.Metrics())

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if autobook != nil {
		go autobook.loop(ctx)
	}
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
