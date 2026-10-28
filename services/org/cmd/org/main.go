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

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/org/internal/api"
	"github.com/BenjaminSRussell/passage/services/org/internal/domain"
	"github.com/BenjaminSRussell/passage/services/org/internal/seed"
	"github.com/BenjaminSRussell/passage/services/org/internal/store"
	"github.com/BenjaminSRussell/passage/services/org/migrations"
)

const (
	defaultHTTPAddr  = ":8080"
	defaultAdminAddr = ":9090"
)

func main() {
	slog.SetDefault(newLogger(os.Stdout))
	if err := run(context.Background(), os.Getenv); err != nil {
		slog.Error("org stopped", "err", err)
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
	return slog.New(handler).With("service", "org")
}

func run(ctx context.Context, getenv func(string) string) error {
	databaseURL := getenv("DATABASE_URL")
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	limits, err := treeLimits(getenv)
	if err != nil {
		return err
	}
	validator, err := addressValidator(getenv("ORG_ADDRESS_VALIDATOR"))
	if err != nil {
		return err
	}
	seedPackage, seedTenant := getenv("ORG_SEED_PACKAGE"), getenv("ORG_SEED_TENANT_ID")
	if seedPackage != "" && seedPackage != seed.DemoManufacturer {
		return fmt.Errorf("ORG_SEED_PACKAGE must be %q, got %q", seed.DemoManufacturer, seedPackage)
	}
	if seedPackage != "" && !ids.Valid(seedTenant) {
		return errors.New("ORG_SEED_TENANT_ID must be a UUID when ORG_SEED_PACKAGE is set")
	}
	pool, err := dbx.Connect(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("connect database: %w", err)
	}
	defer pool.Close()
	if getenv("MIGRATE_ON_START") == "true" {
		if err := migrateAndSeed(ctx, pool, getenv("MIGRATE_DATABASE_URL")); err != nil {
			return err
		}
	}
	st := store.New(pool).WithTreeLimits(limits)
	if seedPackage != "" {
		if err := seed.Run(ctx, st, seedTenant, seedPackage); err != nil {
			return fmt.Errorf("seed %s: %w", seedPackage, err)
		}
		slog.Info("seed package applied", "package", seedPackage, "tenantId", seedTenant)
	}
	a := api.New(st)
	a.SetAddressValidator(validator)
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

func migrateAndSeed(ctx context.Context, servePool *pgxpool.Pool, ownerURL string) error {
	pool := servePool
	if ownerURL != "" {
		var err error
		if pool, err = dbx.Connect(ctx, ownerURL); err != nil {
			return fmt.Errorf("connect migration database: %w", err)
		}
		defer pool.Close()
	}
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	if err := store.SeedReference(ctx, pool, migrations.Reference); err != nil {
		return fmt.Errorf("seed reference data: %w", err)
	}
	return nil
}

func addressValidator(name string) (domain.AddressValidator, error) {
	switch name {
	case "", "null":
		return domain.NullAddressValidator{}, nil
	case "rules":
		return domain.RuleAddressValidator{}, nil
	}
	return nil, fmt.Errorf("ORG_ADDRESS_VALIDATOR must be null or rules, got %q", name)
}

func addr(getenv func(string) string, key, fallback string) string {
	if v := getenv(key); v != "" {
		return v
	}
	return fallback
}

func treeLimits(getenv func(string) string) (domain.TreeLimits, error) {
	limits := domain.DefaultTreeLimits
	for _, setting := range []struct {
		name     string
		min, max int
		target   *int
	}{
		{"ORG_HIERARCHY_MAX_NODES", 1000, 200000, &limits.MaxNodes},
		{"ORG_HIERARCHY_MAX_DEPTH", 5, 25, &limits.MaxDepth},
	} {
		raw := getenv(setting.name)
		if raw == "" {
			continue
		}
		n, err := strconv.Atoi(raw)
		if err != nil || n < setting.min || n > setting.max {
			return limits, fmt.Errorf("%s must be an integer from %d to %d, got %q", setting.name, setting.min, setting.max, raw)
		}
		*setting.target = n
	}
	return limits, nil
}
