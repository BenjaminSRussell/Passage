// Command migration-factory serves the migration factory API: open-items pack runs with
// control totals, the legacy cross-reference and the cutover runbook (PSG-49).
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/api"
	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/store"
	"github.com/BenjaminSRussell/passage/tools/migration-factory/migrations"
)

func main() {
	slog.SetDefault(newLogger(os.Stdout))
	if err := run(context.Background(), os.Getenv); err != nil {
		slog.Error("migration-factory stopped", "err", err)
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
	return slog.New(handler).With("service", "migration-factory")
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
	addr := getenv("HTTP_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	return httpx.Serve(ctx, addr, api.New(store.New(pool)).Routes())
}
