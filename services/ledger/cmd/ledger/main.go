// Command ledger runs the Passage ledger service: universal journal, chart of
// accounts, balance projection and journal posting API (wave 11).
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/api"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/closeperiods"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/fxrates"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/store"
	"github.com/BenjaminSRussell/passage/services/ledger/migrations"
)

const (
	defaultHTTPAddr  = ":8080"
	defaultAdminAddr = ":9090"
)

func main() {
	slog.SetDefault(newLogger(os.Stdout))
	if err := run(context.Background(), os.Getenv); err != nil {
		slog.Error("ledger stopped", "err", err)
		os.Exit(1)
	}
}

// newLogger writes JSON lines with the required timestamp, level, message and
// service fields (platform logging rule).
func newLogger(w io.Writer) *slog.Logger {
	renamed := map[string]string{slog.TimeKey: "timestamp", slog.MessageKey: "message"}
	handler := slog.NewJSONHandler(w, &slog.HandlerOptions{ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
		if name, ok := renamed[a.Key]; ok {
			a.Key = name
		}
		return a
	}})
	return slog.New(handler).With("service", "ledger")
}

func run(ctx context.Context, getenv func(string) string) error {
	if err := tenant.ConfigureServiceVerifier(getenv); err != nil {
		return err
	}
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
	if err := tenant.ShareReplayCache(ctx, pool); err != nil {
		return err
	}
	a := api.New(store.New(pool))
	gate, err := periodGate(getenv)
	if err != nil {
		return err
	}
	if gate != nil {
		a.WithPeriodGate(gate)
	}
	rates, err := rateSource(getenv)
	if err != nil {
		return err
	}
	if rates != nil {
		a.WithRateSource(rates)
	}
	router := httpx.NewRouter()
	a.Register(router)
	admin := a.Admin()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errs := make(chan error, 2)
	go func() { errs <- httpx.Serve(ctx, addr(getenv, "HTTP_ADDR", defaultHTTPAddr), router) }()
	go func() { errs <- httpx.Serve(ctx, addr(getenv, "ADMIN_ADDR", defaultAdminAddr), admin) }()
	return <-errs
}

func periodGate(getenv func(string) string) (store.PeriodGate, error) {
	url := getenv("CLOSE_URL")
	switch {
	case getenv("LEDGER_PERIOD_CHECK") == "disabled":
		slog.Warn("period control disabled; postings are not checked against the close service", "closeUrlSet", url != "")
		return nil, nil
	case url == "":
		return nil, errors.New("CLOSE_URL is required, or set LEDGER_PERIOD_CHECK=disabled to run without period control")
	}
	signer, err := tenant.ServiceSignerFromEnv(getenv)
	if err != nil {
		return nil, err
	}
	if signer == nil {
		return nil, errors.New("CLOSE_URL is set but there is no service signing key (PASSAGE_SERVICE_SIGNING_KEY or PASSAGE_SERVICE_SIGNING_KEY_FILE)")
	}
	return closeperiods.New(url, nil, signer), nil
}

func rateSource(getenv func(string) string) (store.RateSource, error) {
	url := getenv("FX_URL")
	if url == "" {
		slog.Warn("FX_URL is not set; ledger sets that span currencies cannot post")
		return nil, nil
	}
	signer, err := tenant.ServiceSignerFromEnv(getenv)
	if err != nil {
		return nil, err
	}
	if signer == nil {
		return nil, errors.New("FX_URL is set but there is no service signing key (PASSAGE_SERVICE_SIGNING_KEY or PASSAGE_SERVICE_SIGNING_KEY_FILE)")
	}
	return fxrates.New(url, nil, signer), nil
}

func addr(getenv func(string) string, key, fallback string) string {
	if v := getenv(key); v != "" {
		return v
	}
	return fallback
}
