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

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/api"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
	"github.com/BenjaminSRussell/passage/services/inventory/migrations"
)

const (
	defaultHTTPAddr  = ":8080"
	defaultAdminAddr = ":9090"
)

func main() {
	slog.SetDefault(newLogger(os.Stdout))
	start := run
	switch {
	case len(os.Args) > 1 && os.Args[1] == "consume":
		start = consume
	case len(os.Args) > 1:
		slog.Error("inventory stopped", "err", fmt.Errorf("unknown command %q (use no argument to serve or \"consume\" to run the postings consumer)", os.Args[1]))
		os.Exit(2)
	}
	if err := start(context.Background(), os.Getenv); err != nil {
		slog.Error("inventory stopped", "err", err)
		os.Exit(1)
	}
}

// consume runs the postings consumer (#480): it books receiving and sales
// postings through the store and has no HTTP API of its own.
func consume(ctx context.Context, getenv func(string) string) error {
	cfg, err := loadConfig(getenv)
	if err != nil {
		return err
	}
	pool, err := dbx.Connect(ctx, cfg.databaseURL)
	if err != nil {
		return fmt.Errorf("connect database: %w", err)
	}
	defer pool.Close()
	return runPostingsConsumer(ctx, getenv, store.New(pool).WithRules(cfg.rules), pool, cfg.negative, cfg.allowNegativeTruck)
}

func newLogger(w io.Writer) *slog.Logger {
	renamed := map[string]string{slog.TimeKey: "timestamp", slog.MessageKey: "message"}
	handler := slog.NewJSONHandler(w, &slog.HandlerOptions{ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
		if name, ok := renamed[a.Key]; ok {
			a.Key = name
		}
		return a
	}})
	return slog.New(handler).With("service", "inventory")
}

// config is the validated runtime configuration.
type config struct {
	databaseURL string
	rules       domain.JournalRules
	negative    domain.NegativePolicy
	// allowNegativeTruck is the service.allow-negative-truck policy (BR-45.7): truck stock may go negative.
	allowNegativeTruck bool
	post               store.PostParams
}

// loadConfig reads and validates the environment. Invalid values stop startup.
func loadConfig(getenv func(string) string) (config, error) {
	var c config
	c.databaseURL = getenv("DATABASE_URL")
	if c.databaseURL == "" {
		return c, errors.New("DATABASE_URL is required")
	}
	c.rules = domain.DefaultJournalRules
	if raw := getenv("INV_JOURNAL_MAX_LINES"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1000 || n > 50000 {
			return c, fmt.Errorf("INV_JOURNAL_MAX_LINES must be an integer from 1000 to 50000, got %q", raw)
		}
		c.rules.MaxLines = n
	}
	switch raw := getenv("INV_NEGATIVE_DEFAULT"); raw {
	case "", "forbid":
		c.negative = domain.NegativePolicy{}
	case "allow":
		c.negative = domain.NegativePolicy{PhysicalAllowed: true, FinancialAllowed: true}
	default:
		return c, fmt.Errorf("INV_NEGATIVE_DEFAULT must be forbid or allow, got %q", raw)
	}
	switch raw := getenv("INV_ALLOW_NEGATIVE_TRUCK"); raw {
	case "", "false":
	case "true":
		c.allowNegativeTruck = true
	default:
		return c, fmt.Errorf("INV_ALLOW_NEGATIVE_TRUCK must be true or false, got %q", raw)
	}
	for name, target := range map[string]*string{
		"INV_INVENTORY_ACCOUNT":        &c.post.InventoryAccount,
		"INV_ADJUSTMENT_ACCOUNT":       &c.post.AdjustmentAccount,
		"INV_OPENING_CLEARING_ACCOUNT": &c.post.OpeningAccount,
	} {
		*target = getenv(name)
		if *target == "" {
			return c, fmt.Errorf("%s is required", name)
		}
	}
	// Optional: only OWNERSHIP_CHANGE journals post to the consignment accrual
	// account (FR-16.17); without it they are refused with INV-JNL-005.
	c.post.ConsignmentAccount = getenv("INV_CONSIGNMENT_ACCRUAL_ACCOUNT")
	c.post.Negative = c.negative
	return c, nil
}

func run(ctx context.Context, getenv func(string) string) error {
	if err := tenant.ConfigureServiceVerifier(getenv); err != nil {
		return err
	}
	cfg, err := loadConfig(getenv)
	if err != nil {
		return err
	}
	pool, err := dbx.Connect(ctx, cfg.databaseURL)
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
	s := store.New(pool).WithRules(cfg.rules)
	a := api.New(s, cfg.post).WithTruckStock(s)
	// Optional: without LEDGER_URL the GL sweep answers 503 INV-GL-005; with it, a signing key is required.
	ledger, err := ledgerclient.FromEnv(getenv, "inventory")
	if err != nil {
		return fmt.Errorf("ledger client: %w", err)
	}
	if ledger != nil {
		a.WithGL(s, ledger)
	}
	router := httpx.NewRouter()
	a.Register(router)
	admin := http.NewServeMux()
	admin.Handle("/metrics", a.Metrics())
	// The kernel router's /readyz is static; this one checks Postgres. The chart
	// probes it on the admin port.
	admin.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := s.Ping(r.Context()); err != nil {
			httpx.JSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "ready"})
	})

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
