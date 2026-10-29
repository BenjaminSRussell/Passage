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
	"github.com/BenjaminSRussell/passage/services/compliance/internal/api"
	"github.com/BenjaminSRussell/passage/services/compliance/internal/store"
	"github.com/BenjaminSRussell/passage/services/compliance/migrations"
)

const (
	defaultHTTPAddr  = ":8080"
	defaultAdminAddr = ":9090"
)

func main() {
	slog.SetDefault(newLogger(os.Stdout))
	if err := run(context.Background(), os.Getenv); err != nil {
		slog.Error("compliance stopped", "err", err)
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
	return slog.New(handler).With("service", "compliance")
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
	cfg, err := config(getenv)
	if err != nil {
		return err
	}
	a := api.New(store.New(pool), cfg)
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

// config reads the tenant policies sod.block_high (default true) and
// sod.max_mitigation_days (default 365, 1..730) from the environment.
func config(getenv func(string) string) (api.Config, error) {
	cfg := api.Config{BlockHigh: true, MaxMitigationDays: 365}
	switch v := getenv("SOD_BLOCK_HIGH"); v {
	case "", "true":
	case "false":
		cfg.BlockHigh = false
	default:
		return cfg, fmt.Errorf("SOD_BLOCK_HIGH must be true or false, got %q", v)
	}
	if v := getenv("SOD_MAX_MITIGATION_DAYS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 730 {
			return cfg, fmt.Errorf("SOD_MAX_MITIGATION_DAYS must be 1..730, got %q", v)
		}
		cfg.MaxMitigationDays = n
	}
	return cfg, nil
}

func addr(getenv func(string) string, key, fallback string) string {
	if v := getenv(key); v != "" {
		return v
	}
	return fallback
}
