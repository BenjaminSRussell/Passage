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
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/ops/internal/api"
	"github.com/BenjaminSRussell/passage/services/ops/internal/domain"
	"github.com/BenjaminSRussell/passage/services/ops/internal/store"
	"github.com/BenjaminSRussell/passage/services/ops/migrations"
)

const (
	defaultHTTPAddr  = ":8080"
	defaultAdminAddr = ":9090"
)

func main() {
	slog.SetDefault(newLogger(os.Stdout))
	if err := run(context.Background(), os.Getenv); err != nil {
		slog.Error("ops stopped", "err", err)
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
	return slog.New(handler).With("service", "ops")
}

func run(ctx context.Context, getenv func(string) string) error {
	// The ops service must answer in a fenced region: it runs the rejoin and serves /healthz/region.
	httpx.DisableFence()
	if err := tenant.ConfigureServiceVerifier(getenv); err != nil {
		return err
	}
	cfg := api.Config{Region: getenv("REGION_NAME")}
	operator, ok := ids.Canonical(getenv("OPS_OPERATOR_TENANT_ID"))
	if !ok || operator == ids.Nil {
		return errors.New("OPS_OPERATOR_TENANT_ID must be the platform operator's tenant id (a UUID): the ops routes serve that tenant only")
	}
	cfg.OperatorTenantID = operator
	if v := getenv("FAILOVER_MAX_LAG_S"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return fmt.Errorf("FAILOVER_MAX_LAG_S must be a positive number of seconds, got %q", v)
		}
		cfg.MaxLagSeconds = n
	}
	seed, err := domain.ParseRegions(getenv("OPS_REGIONS"))
	if err != nil {
		return fmt.Errorf("OPS_REGIONS: %w", err)
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
	st := store.New(pool)
	if len(seed) > 0 {
		if err := st.Seed(ctx, seed); err != nil {
			return fmt.Errorf("seed regions: %w", err)
		}
	}
	a := api.New(st, cfg)
	router := httpx.NewRouterWithoutTenant()
	a.Register(router)
	admin := http.NewServeMux()
	admin.Handle("/metrics", httpx.WithMetrics(a.Metrics()))

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
