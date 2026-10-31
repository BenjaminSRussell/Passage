package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/fieldcrypt"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/hr/internal/api"
	"github.com/BenjaminSRussell/passage/services/hr/internal/domain"
	"github.com/BenjaminSRussell/passage/services/hr/internal/store"
	"github.com/BenjaminSRussell/passage/services/hr/migrations"
)

const (
	defaultHTTPAddr  = ":8080"
	defaultAdminAddr = ":9090"
	defaultMaxDepth  = 10
)

func main() {
	slog.SetDefault(newLogger(os.Stdout))
	if err := run(context.Background(), os.Getenv); err != nil {
		slog.Error("hr stopped", "err", err)
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
	return slog.New(handler).With("service", "hr")
}

// sealerFromEnv reads the fieldcrypt keys (FIELDCRYPT_KEYS_FILE or FIELDCRYPT_KEYS) and
// FIELDCRYPT_CURRENT_KEY_ID. HR_PII_KEY (base64 of 32 bytes) is optional and only opens values
// written before fieldcrypt. The service refuses to start without fieldcrypt keys: P3 values
// must never be stored unsealed.
func sealerFromEnv(getenv func(string) string) (*domain.Sealer, error) {
	keys, err := fieldcrypt.FromEnv(getenv)
	if err != nil {
		return nil, err
	}
	if keys == nil {
		return nil, errors.New("FIELDCRYPT_KEYS or FIELDCRYPT_KEYS_FILE is required")
	}
	var legacy []byte
	if raw := getenv("HR_PII_KEY"); raw != "" {
		if legacy, err = base64.StdEncoding.DecodeString(raw); err != nil {
			return nil, fmt.Errorf("HR_PII_KEY is not base64: %w", err)
		}
	}
	return domain.NewSealer(keys, legacy)
}

func maxDepthFromEnv(getenv func(string) string) (int, error) {
	raw := getenv("HR_MANAGER_MAX_DEPTH")
	if raw == "" {
		return defaultMaxDepth, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > 20 {
		return 0, fmt.Errorf("HR_MANAGER_MAX_DEPTH must be an integer from 1 to 20, got %q", raw)
	}
	return n, nil
}

func run(ctx context.Context, getenv func(string) string) error {
	databaseURL := getenv("DATABASE_URL")
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	sealer, err := sealerFromEnv(getenv)
	if err != nil {
		return err
	}
	depth, err := maxDepthFromEnv(getenv)
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
	a := api.New(store.New(pool, sealer, depth))
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

func addr(getenv func(string) string, key, fallback string) string {
	if v := getenv(key); v != "" {
		return v
	}
	return fallback
}
