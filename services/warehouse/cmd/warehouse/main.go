package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/issueclaim"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/warehouse/internal/api"
	"github.com/BenjaminSRussell/passage/services/warehouse/internal/store"
	"github.com/BenjaminSRussell/passage/services/warehouse/migrations"
)

const (
	defaultHTTPAddr       = ":8080"
	defaultAdminAddr      = ":9090"
	defaultLockTimeoutMin = 30
)

func main() {
	slog.SetDefault(newLogger(os.Stdout))
	if err := run(context.Background(), os.Getenv); err != nil {
		slog.Error("warehouse stopped", "err", err)
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
	return slog.New(handler).With("service", "warehouse")
}

// run serves the API and the admin port. With DATABASE_URL set, locations,
// directives and work are persisted (migrations run first when
// MIGRATE_ON_START=true) and the setup and work routes are mounted. Without it
// the service is stateless and serves the directive simulation alone.
func run(ctx context.Context, getenv func(string) string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	if err := tenant.ConfigureServiceVerifier(getenv); err != nil {
		return err
	}
	a := api.New()
	if url := getenv("DATABASE_URL"); url != "" {
		pool, err := dbx.Connect(ctx, url)
		if err != nil {
			return fmt.Errorf("connect: %w", err)
		}
		defer pool.Close()
		if getenv("MIGRATE_ON_START") == "true" {
			if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
				return fmt.Errorf("migrate: %w", err)
			}
		}
		lock, err := lockTimeout(getenv("WMS_LOCK_TIMEOUT_MIN"))
		if err != nil {
			return err
		}
		st := store.New(pool)
		holds, err := issueclaim.FromEnv(getenv)
		if err != nil {
			return fmt.Errorf("inventory client: %w", err)
		}
		a = api.NewWithStore(st.WithHolds(holds), lock)
	}
	router := httpx.NewRouter()
	a.Register(router)
	admin := http.NewServeMux()
	admin.Handle("/metrics", a.Metrics())

	errs := make(chan error, 2)
	go func() { errs <- httpx.Serve(ctx, addr(getenv, "HTTP_ADDR", defaultHTTPAddr), router) }()
	go func() { errs <- httpx.Serve(ctx, addr(getenv, "ADMIN_ADDR", defaultAdminAddr), admin) }()
	return <-errs
}

// lockTimeout reads WMS_LOCK_TIMEOUT_MIN (5–240, default 30) as a duration.
func lockTimeout(v string) (time.Duration, error) {
	if v == "" {
		return defaultLockTimeoutMin * time.Minute, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 5 || n > 240 {
		return 0, fmt.Errorf("WMS_LOCK_TIMEOUT_MIN must be between 5 and 240, got %q", v)
	}
	return time.Duration(n) * time.Minute, nil
}

func addr(getenv func(string) string, key, fallback string) string {
	if v := getenv(key); v != "" {
		return v
	}
	return fallback
}
