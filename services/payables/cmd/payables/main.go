package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"time"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/fieldcrypt"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/payables"
	"github.com/BenjaminSRussell/passage/services/payables/internal/api"
	"github.com/BenjaminSRussell/passage/services/payables/internal/domain"
	"github.com/BenjaminSRussell/passage/services/payables/internal/fx"
	"github.com/BenjaminSRussell/passage/services/payables/internal/ledger"
	"github.com/BenjaminSRussell/passage/services/payables/internal/party"
	"github.com/BenjaminSRussell/passage/services/payables/internal/store"
	"github.com/BenjaminSRussell/passage/services/payables/internal/tax"
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
	if err := tenant.ShareReplayCache(ctx, pool); err != nil {
		return err
	}
	validator, err := domain.NewXSDValidator(payables.Pain001XSD)
	if err != nil {
		return err
	}
	defer validator.Close()
	keys, err := fieldcrypt.FromEnv(getenv)
	if err != nil {
		return err
	}
	profiles, err := api.LoadPostingProfiles(getenv)
	if err != nil {
		return err
	}
	items := store.New(pool).WithFieldCrypt(keys)
	balances, err := ledgerBalances(getenv)
	if err != nil {
		return err
	}
	poster, err := ledgerPoster(getenv)
	if err != nil {
		slog.Warn("payment runs are disabled", "reason", err)
		poster = nil
	}
	rates, err := rateSource(getenv)
	if err != nil {
		return err
	}
	banks, err := vendorBanks(getenv)
	if err != nil {
		slog.Warn("payment run file generation is disabled", "reason", err)
		banks = nil
	}
	withholding, err := withholdingTax(getenv)
	if err != nil {
		slog.Warn("withholding is disabled", "reason", err)
		withholding = nil
	}
	groupDim, err := vendorGroupDimension(getenv)
	if err != nil {
		return err
	}
	a := api.New(pool, validator, time.Now).WithAging(items, balances, getenv("AP_CONTROL_ACCOUNT")).WithProposals(items, rates).WithPaymentRuns(items, poster, profiles).
		WithVendorBanks(banks, keys).WithWithholding(withholding, items, profiles).WithVendorGroupDimension(groupDim)
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

var dimensionAttrRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,19}$`)

// vendorGroupDimension is the ledger dimension attribute for the vendor group code: AP_VENDOR_GROUP_DIMENSION,
// VENDGRP when unset, none when "-". A ledger that does not define it drops it from the run journal (optional
// dimension) and makes the per-group reconcile lines uncomparable.
func vendorGroupDimension(getenv func(string) string) (string, error) {
	switch v := getenv("AP_VENDOR_GROUP_DIMENSION"); v {
	case "":
		return api.DefaultVendorGroupDimension, nil
	case "-":
		return "", nil
	default:
		if !dimensionAttrRe.MatchString(v) {
			return "", errors.New("AP_VENDOR_GROUP_DIMENSION must be a ledger dimension attribute (A-Z, digits, underscore) or -")
		}
		return v, nil
	}
}

func ledgerClient(getenv func(string) string) (*ledger.Client, error) {
	url := getenv("LEDGER_URL")
	if url == "" {
		return nil, nil
	}
	signer, err := tenant.ServiceSignerFromEnv(getenv)
	if err != nil {
		return nil, err
	}
	timeout := ledger.Timeout
	if v, err := time.ParseDuration(getenv("LEDGER_TIMEOUT")); err == nil && v > 0 {
		timeout = v
	}
	return ledger.New(url, &http.Client{Timeout: timeout}).WithSigner(signer), nil
}

func ledgerBalances(getenv func(string) string) (api.LedgerBalances, error) {
	c, err := ledgerClient(getenv)
	if c == nil || err != nil {
		return nil, err
	}
	return c, nil
}

func ledgerPoster(getenv func(string) string) (api.LedgerPoster, error) {
	c, err := ledgerClient(getenv)
	if c == nil || err != nil {
		return nil, err
	}
	if !c.Signed() {
		return nil, errors.New("LEDGER_URL is set but payment runs need PASSAGE_SERVICE_SIGNING_KEY or PASSAGE_SERVICE_SIGNING_KEY_FILE: journals are never posted with forwarded user headers")
	}
	return c, nil
}

func serviceClientTimeout(getenv func(string) string, key string, fallback time.Duration) time.Duration {
	if v, err := time.ParseDuration(getenv(key)); err == nil && v > 0 {
		return v
	}
	return fallback
}

func vendorBanks(getenv func(string) string) (api.VendorBanks, error) {
	url := getenv("PARTY_URL")
	if url == "" {
		return nil, nil
	}
	signer, err := tenant.ServiceSignerFromEnv(getenv)
	if err != nil {
		return nil, err
	}
	c := party.New(url, &http.Client{Timeout: serviceClientTimeout(getenv, "PARTY_TIMEOUT", party.Timeout)}).WithSigner(signer)
	if !c.Signed() {
		return nil, errors.New("PARTY_URL is set but vendor bank details need PASSAGE_SERVICE_SIGNING_KEY or PASSAGE_SERVICE_SIGNING_KEY_FILE: they are never revealed with forwarded user headers")
	}
	return c, nil
}

func withholdingTax(getenv func(string) string) (api.WithholdingCalculator, error) {
	url := getenv("TAX_URL")
	if url == "" {
		return nil, nil
	}
	signer, err := tenant.ServiceSignerFromEnv(getenv)
	if err != nil {
		return nil, err
	}
	c := tax.New(url, &http.Client{Timeout: serviceClientTimeout(getenv, "TAX_TIMEOUT", tax.Timeout)}).WithSigner(signer)
	if !c.Signed() {
		return nil, errors.New("TAX_URL is set but withholding needs PASSAGE_SERVICE_SIGNING_KEY or PASSAGE_SERVICE_SIGNING_KEY_FILE")
	}
	return c, nil
}

func rateSource(getenv func(string) string) (api.RateSource, error) {
	url := getenv("FX_URL")
	if url == "" {
		return nil, nil
	}
	signer, err := tenant.ServiceSignerFromEnv(getenv)
	if err != nil {
		return nil, err
	}
	timeout := fx.Timeout
	if v, err := time.ParseDuration(getenv("FX_TIMEOUT")); err == nil && v > 0 {
		timeout = v
	}
	return fx.New(url, getenv("FX_RATE_TYPE"), &http.Client{Timeout: timeout}).WithSigner(signer), nil
}

func addr(getenv func(string) string, key, fallback string) string {
	if v := getenv(key); v != "" {
		return v
	}
	return fallback
}
