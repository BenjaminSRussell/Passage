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
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/analytics/internal/api"
	"github.com/BenjaminSRussell/passage/services/analytics/internal/cube"
	"github.com/BenjaminSRussell/passage/services/analytics/internal/store"
	"github.com/BenjaminSRussell/passage/services/analytics/migrations"
)

const defaultHTTPAddr = ":8080"

func main() {
	slog.SetDefault(newLogger(os.Stdout))
	if err := run(context.Background(), os.Getenv); err != nil {
		slog.Error("analytics stopped", "err", err)
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
	return slog.New(handler).With("service", "analytics")
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
	st := store.New(pool)
	job, err := newReconJob(getenv, st)
	if err != nil {
		return err
	}
	if job != nil {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		go job.Loop(ctx)
	}
	router := httpx.NewRouter()
	router.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	router.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := st.Ping(r.Context()); err != nil {
			httpx.JSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "ready"})
	})
	a := api.New(st)
	if url, secret := getenv("SEMANTIC_LAYER_URL"), getenv("SEMANTIC_LAYER_SECRET"); url != "" && secret != "" {
		a.WithExecutor(cube.New(url, secret, nil))
	} else {
		slog.Info("semantic layer not configured; query:execute and dashboard tile values answer ANA-0120")
	}
	a.Register(router)
	return httpx.ServeWithMetrics(ctx, addr(getenv, "HTTP_ADDR", defaultHTTPAddr), tenant.Middleware(router))
}

func addr(getenv func(string) string, key, fallback string) string {
	if v := getenv(key); v != "" {
		return v
	}
	return fallback
}
