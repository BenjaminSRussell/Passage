package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/localization/internal/api"
	"github.com/BenjaminSRussell/passage/services/localization/internal/domain"
)

const (
	defaultHTTPAddr  = ":8080"
	defaultAdminAddr = ":9090"
	defaultPacksDir  = "countrypacks"
)

func main() {
	slog.SetDefault(newLogger(os.Stdout))
	if err := run(context.Background(), os.Getenv); err != nil {
		slog.Error("localization stopped", "err", err)
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
	return slog.New(handler).With("service", "localization")
}

// run loads and validates every country pack before serving; an invalid pack stops the service.
func run(ctx context.Context, getenv func(string) string) error {
	dir := getenv("COUNTRY_PACKS_DIR")
	if dir == "" {
		dir = defaultPacksDir
	}
	packs, err := domain.LoadPacks(os.DirFS(dir))
	if err != nil {
		return fmt.Errorf("load country packs from %s: %w", dir, err)
	}
	a := api.New(packs)
	router := httpx.NewRouter()
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
