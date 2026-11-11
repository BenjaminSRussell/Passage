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
	_ "time/tzdata" // SLA time zones resolve without the host zoneinfo

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/api"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/objectstore"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/optimizer"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/store"
	"github.com/BenjaminSRussell/passage/services/fieldservice/migrations"
)

const (
	defaultHTTPAddr  = ":8080"
	defaultAdminAddr = ":9090"
	readyTimeout     = 500 * time.Millisecond
)

func main() {
	slog.SetDefault(newLogger(os.Stdout))
	if err := run(context.Background(), os.Getenv); err != nil {
		slog.Error("fieldservice stopped", "err", err)
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
	return slog.New(handler).With("service", "fieldservice")
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
	if getenv("MIGRATE_ON_START") == "true" {
		if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	signer, err := tenant.ServiceSignerFromEnv(getenv)
	if err != nil {
		return err
	}
	if signer == nil || getenv("DISPATCH_URL") == "" {
		slog.Warn("DISPATCH_URL or the service signing key is unset: dispatch optimizations are refused (SVC-DSP-017)")
	}
	objects, err := objectstore.FromEnv(getenv)
	if err != nil {
		return err
	}
	s := store.New(pool)
	if objects != nil {
		s.WithObjects(objects)
	}
	a := api.New(s).WithOptimizer(optimizer.New(getenv("DISPATCH_URL"), signer))
	router := httpx.NewRouter()
	a.Register(router)

	// Readiness pings Postgres; liveness stays dependency-free.
	public := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/readyz" {
			ctxReady, cancel := context.WithTimeout(r.Context(), readyTimeout)
			defer cancel()
			if err := s.Ping(ctxReady); err != nil {
				httpx.JSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
				return
			}
		}
		router.ServeHTTP(w, r)
	})
	admin := http.NewServeMux()
	admin.Handle("/metrics", httpx.WithMetrics(a.Metrics()))

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	captures, err := newCaptureJob(getenv, s)
	if err != nil {
		return err
	}
	if captures != nil {
		go captures.loop(ctx)
	}
	errs := make(chan error, 2)
	go func() { errs <- httpx.Serve(ctx, addr(getenv, "HTTP_ADDR", defaultHTTPAddr), public) }()
	go func() { errs <- httpx.Serve(ctx, addr(getenv, "ADMIN_ADDR", defaultAdminAddr), admin) }()
	return <-errs
}

func addr(getenv func(string) string, key, fallback string) string {
	if v := getenv(key); v != "" {
		return v
	}
	return fallback
}
