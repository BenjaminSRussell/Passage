package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/intercompany/internal/api"
)

const (
	defaultHTTPAddr  = ":8080"
	defaultAdminAddr = ":9090"
)

func main() {
	slog.SetDefault(newLogger(os.Stdout))
	if err := run(context.Background(), os.Getenv); err != nil {
		slog.Error("intercompany stopped", "err", err)
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
	return slog.New(handler).With("service", "intercompany")
}

// run serves the stateless slice-1 API on HTTP_ADDR and /metrics on
// ADMIN_ADDR. The database, migrations and outbox are wired in the store
// slice; until then no DATABASE_URL is read.
func run(ctx context.Context, getenv func(string) string) error {
	a := api.New()
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

func addr(getenv func(string) string, name, fallback string) string {
	if v := getenv(name); v != "" {
		return v
	}
	return fallback
}
