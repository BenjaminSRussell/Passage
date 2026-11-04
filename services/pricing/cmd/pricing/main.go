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
	"time"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/api"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/store"
	"github.com/BenjaminSRussell/passage/services/pricing/migrations"
)

const (
	defaultHTTPAddr  = ":8080"
	defaultAdminAddr = ":9090"
)

func main() {
	slog.SetDefault(newLogger(os.Stdout))
	if err := run(context.Background(), os.Getenv); err != nil {
		slog.Error("pricing stopped", "err", err)
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
	return slog.New(handler).With("service", "pricing")
}

func run(ctx context.Context, getenv func(string) string) error {
	databaseURL := getenv("DATABASE_URL")
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	cfg, err := config(getenv)
	if err != nil {
		return err
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
	ttl, err := cacheTTL(getenv)
	if err != nil {
		return err
	}
	router := httpx.NewRouter()
	cache := store.NewCache(store.New(pool), ttl)
	a := api.New(cache, cfg)
	a.Register(router)
	admin := http.NewServeMux()
	admin.Handle("/metrics", a.Metrics())

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go runCacheConsumer(ctx, getenv, cache)
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

// Bounds of the record snapshot lifetime (§5.26.13 cache_ttl_seconds: default 60, 10 to 600).
const (
	defaultCacheTTL = 60 * time.Second
	minCacheTTL     = 10 * time.Second
	maxCacheTTL     = 600 * time.Second
)

// cacheTTL reads PRICING_CACHE_TTL as a duration (60s) or whole seconds (60). 0 turns the
// record snapshot cache off; any other value outside 10s to 600s is refused at startup.
func cacheTTL(getenv func(string) string) (time.Duration, error) {
	raw := getenv("PRICING_CACHE_TTL")
	if raw == "" {
		return defaultCacheTTL, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		n, nerr := strconv.Atoi(raw)
		if nerr != nil {
			return 0, fmt.Errorf("PRICING_CACHE_TTL must be a duration such as 60s, got %q", raw)
		}
		d = time.Duration(n) * time.Second
	}
	if d != 0 && (d < minCacheTTL || d > maxCacheTTL) {
		return 0, fmt.Errorf("PRICING_CACHE_TTL must be 0 or from %s to %s, got %q", minCacheTTL, maxCacheTTL, raw)
	}
	return d, nil
}

// config reads PRICING_MAX_LINES and PRICING_EXPLAIN_MAX_BYTES, refusing out-of-range values at startup.
func config(getenv func(string) string) (api.Config, error) {
	cfg := api.Config{MaxLines: api.DefaultMaxLines, ExplainMaxBytes: api.DefaultExplainMaxBytes}
	for _, s := range []struct {
		name     string
		min, max int
		target   *int
	}{
		{"PRICING_MAX_LINES", 1, 2000, &cfg.MaxLines},
		{"PRICING_EXPLAIN_MAX_BYTES", 8192, 262144, &cfg.ExplainMaxBytes},
	} {
		raw := getenv(s.name)
		if raw == "" {
			continue
		}
		n, err := strconv.Atoi(raw)
		if err != nil || n < s.min || n > s.max {
			return cfg, fmt.Errorf("%s must be an integer from %d to %d, got %q", s.name, s.min, s.max, raw)
		}
		*s.target = n
	}
	return cfg, nil
}
