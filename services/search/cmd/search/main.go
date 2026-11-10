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
	"github.com/BenjaminSRussell/passage/services/search/internal/api"
	"github.com/BenjaminSRussell/passage/services/search/internal/store"
	"github.com/BenjaminSRussell/passage/services/search/migrations"
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
		slog.Error("search stopped", "err", fmt.Errorf("unknown command %q (use no argument to serve or \"consume\" to run the index consumer)", os.Args[1]))
		os.Exit(2)
	}
	if err := start(context.Background(), os.Getenv); err != nil {
		slog.Error("search stopped", "err", err)
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
	return slog.New(handler).With("service", "search")
}

func connect(ctx context.Context, getenv func(string) string) (*store.Store, func(), error) {
	url := getenv("DATABASE_URL")
	if url == "" {
		return nil, nil, errors.New("DATABASE_URL is required")
	}
	pool, err := dbx.Connect(ctx, url)
	if err != nil {
		return nil, nil, fmt.Errorf("connect: %w", err)
	}
	if getenv("MIGRATE_ON_START") == "true" {
		if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
			pool.Close()
			return nil, nil, fmt.Errorf("migrate: %w", err)
		}
	}
	return store.New(pool), pool.Close, nil
}

func run(ctx context.Context, getenv func(string) string) error {
	st, closeDB, err := connect(ctx, getenv)
	if err != nil {
		return err
	}
	defer closeDB()
	a := api.New(st)
	router := httpx.NewRouter()
	a.Register(router)
	admin := http.NewServeMux()
	admin.Handle("/metrics", a.Metrics())
	admin.HandleFunc("/readyz", ready(st.Ping))

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errs := make(chan error, 2)
	go func() { errs <- httpx.Serve(ctx, addr(getenv, "HTTP_ADDR", defaultHTTPAddr), router) }()
	go func() { errs <- httpx.Serve(ctx, addr(getenv, "ADMIN_ADDR", defaultAdminAddr), admin) }()
	return <-errs
}

func ready(ping func(context.Context) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := ping(r.Context()); err != nil {
			slog.ErrorContext(r.Context(), "dependency not reachable", "err", err)
			httpx.JSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "ready"})
	}
}

func addr(getenv func(string) string, key, fallback string) string {
	if v := getenv(key); v != "" {
		return v
	}
	return fallback
}
