package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/payables"
	"github.com/BenjaminSRussell/passage/services/payables/internal/api"
	"github.com/BenjaminSRussell/passage/services/payables/internal/domain"
	"github.com/BenjaminSRussell/passage/services/payables/internal/fx"
	"github.com/BenjaminSRussell/passage/services/payables/internal/ledger"
	"github.com/BenjaminSRussell/passage/services/payables/internal/store"
)

const (
	defaultHTTPAddr  = ":8080"
	defaultAdminAddr = ":9090"
)

func main() {
	slog.SetDefault(newLogger(os.Stdout))
	if err := run(context.Background(), os.Getenv); err != nil {
		slog.Error("payables stopped", "err", err)
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
	return slog.New(handler).With("service", "payables")
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
	validator, err := domain.NewXSDValidator(payables.Pain001XSD)
	if err != nil {
		return err
	}
	defer validator.Close()
	items := store.New(pool)
	a := api.New(pool, validator, time.Now).WithAging(items, ledgerBalances(getenv), getenv("AP_CONTROL_ACCOUNT")).WithProposals(items, rateSource(getenv))
	router := httpx.NewRouter()
	a.Register(router)
	admin := http.NewServeMux()
	admin.Handle("/metrics", a.Metrics())

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errs := make(chan error, 2)
	go func() { errs <- httpx.Serve(ctx, addr(getenv, "HTTP_ADDR", defaultHTTPAddr), a.Instrument(router)) }()
	go func() { errs <- httpx.Serve(ctx, addr(getenv, "ADMIN_ADDR", defaultAdminAddr), admin) }()
	return <-errs
}

func ledgerBalances(getenv func(string) string) api.LedgerBalances {
	url := getenv("LEDGER_URL")
	if url == "" {
		return nil
	}
	timeout := ledger.Timeout
	if v, err := time.ParseDuration(getenv("LEDGER_TIMEOUT")); err == nil && v > 0 {
		timeout = v
	}
	return ledger.New(url, &http.Client{Timeout: timeout})
}

func rateSource(getenv func(string) string) api.RateSource {
	url := getenv("FX_URL")
	if url == "" {
		return nil
	}
	timeout := fx.Timeout
	if v, err := time.ParseDuration(getenv("FX_TIMEOUT")); err == nil && v > 0 {
		timeout = v
	}
	return fx.New(url, getenv("FX_RATE_TYPE"), &http.Client{Timeout: timeout})
}

func addr(getenv func(string) string, key, fallback string) string {
	if v := getenv(key); v != "" {
		return v
	}
	return fallback
}
