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
	"github.com/BenjaminSRussell/passage/services/budgeting/internal/api"
	"github.com/BenjaminSRussell/passage/services/budgeting/internal/sources"
	"github.com/BenjaminSRussell/passage/services/budgeting/internal/store"
	"github.com/BenjaminSRussell/passage/services/budgeting/migrations"
)

const (
	defaultHTTPAddr  = ":8080"
	defaultAdminAddr = ":9090"
)

func main() {
	slog.SetDefault(newLogger(os.Stdout))
	if err := run(context.Background(), os.Getenv); err != nil {
		slog.Error("budgeting stopped", "err", err)
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
	return slog.New(handler).With("service", "budgeting")
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
	router := httpx.NewRouter()
	pg := store.New(pool)
	a := api.New(pg)
	gl, err := ledgerclient.FromEnv(getenv, "budgeting")
	if err != nil {
		return err
	}
	// A nil *ledgerclient.Client stored in the PriorActuals interface would be non-nil there, so the unset client is passed as a bare nil.
	if gl != nil {
		a.WithPlanning(pg, gl)
	} else {
		a.WithPlanning(pg, nil)
	}
	// ASSETS_URL, HR_URL, PROJECTS_URL and PLANNING_URL (with the service signing key) let a plan be generated from the fixed asset
	// schedules, HR positions, project forecasts and the demand forecast; a source whose URL is unset answers 503 BUD-0063.
	rd, err := sources.FromEnv(getenv)
	if err != nil {
		return err
	}
	if rd != nil {
		a.WithPlanSources(rd)
	}
	a.Register(router)
	admin := http.NewServeMux()
	admin.Handle("/metrics", httpx.WithMetrics(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = fmt.Fprint(w, "# HELP budgeting_up Process is serving.\n# TYPE budgeting_up gauge\nbudgeting_up 1\n")
	})))

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
