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
	"github.com/BenjaminSRussell/passage/platform/fieldcrypt"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/party/internal/api"
	"github.com/BenjaminSRussell/passage/services/party/internal/domain"
	"github.com/BenjaminSRussell/passage/services/party/internal/numbering"
	"github.com/BenjaminSRussell/passage/services/party/internal/store"
	"github.com/BenjaminSRussell/passage/services/party/internal/vies"
	"github.com/BenjaminSRussell/passage/services/party/migrations"
)

const (
	defaultHTTPAddr  = ":8080"
	defaultAdminAddr = ":9090"

	defaultVIESTimeout = 5 * time.Second
)

func main() {
	slog.SetDefault(newLogger(os.Stdout))
	if err := run(context.Background(), os.Getenv); err != nil {
		slog.Error("party stopped", "err", err)
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
	return slog.New(handler).With("service", "party")
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
		slog.Error("database connect failed", "err", err)
		return fmt.Errorf("connect database: %w", err)
	}
	defer pool.Close()
	slog.Info("database connected")
	if getenv("MIGRATE_ON_START") == "true" {
		slog.Info("migrations starting")
		if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
			slog.Error("migrations failed", "err", err)
			return fmt.Errorf("migrate: %w", err)
		}
		slog.Info("migrations applied")
	}
	if err := tenant.ShareReplayCache(ctx, pool); err != nil {
		return err
	}
	var sequencer api.Sequencer = numbering.Unavailable{}
	if url := getenv("NUMBERING_URL"); url != "" {
		signer, err := tenant.ServiceSignerFromEnv(getenv)
		if err != nil {
			return err
		}
		sequencer = numbering.New(url).WithSigner(signer)
	} else {
		slog.Warn("NUMBERING_URL is empty, party creation returns 503 until it is set")
	}
	keys, err := fieldcrypt.FromEnv(getenv)
	if err != nil {
		return fmt.Errorf("field encryption keys: %w", err)
	}
	if keys == nil {
		slog.Warn("FIELDCRYPT_KEYS is empty, vendor bank accounts cannot be stored or read until it is set")
	}
	parties := store.New(pool).WithKeys(keys)
	a := api.New(taxChecker(getenv), parties, parties, sequencer).WithCredit(parties).WithVendors(parties).WithCustomers(parties).WithMerge(parties).WithSearch(parties).WithSuppliers(parties)
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

func addr(getenv func(string) string, key, fallback string) string {
	if v := getenv(key); v != "" {
		return v
	}
	return fallback
}

func taxChecker(getenv func(string) string) domain.Checker {
	url := getenv("PARTY_VIES_URL")
	if url == "" {
		slog.Warn("PARTY_VIES_URL is empty, VAT numbers stay unverified")
		return vies.Unavailable{}
	}
	timeout := defaultVIESTimeout
	if ms, err := strconv.Atoi(getenv("PARTY_VIES_TIMEOUT_MS")); err == nil && ms > 0 {
		timeout = time.Duration(ms) * time.Millisecond
	}
	return vies.New(url, timeout)
}
