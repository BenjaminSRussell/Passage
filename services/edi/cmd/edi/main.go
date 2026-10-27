// Command edi runs the EDI engine HTTP API: partner profiles, the map tester,
// inbound X12 receipt with 997 generation and the interchange list.
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strconv"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"

	"github.com/BenjaminSRussell/passage/services/edi/internal/api"
	"github.com/BenjaminSRussell/passage/services/edi/internal/store"
	"github.com/BenjaminSRussell/passage/services/edi/migrations"
)

func main() {
	if err := run(); err != nil {
		slog.Error("edi exited", "err", err)
		os.Exit(1)
	}
}

func run() error {
	ctx := context.Background()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		return errors.New("DATABASE_URL is required")
	}
	dupDays, err := envInt("EDI_DUP_WINDOW_DAYS", 30)
	if err != nil {
		return err
	}
	if dupDays < 7 || dupDays > 365 {
		// dup_window_days allowed values (PRD §6.35.13).
		return errors.New("EDI_DUP_WINDOW_DAYS must be 7 to 365")
	}
	pool, err := dbx.Connect(ctx, url)
	if err != nil {
		return err
	}
	defer pool.Close()
	if os.Getenv("MIGRATE_ON_START") == "true" {
		if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
			return err
		}
	}
	addr := os.Getenv("HTTP_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	return httpx.Serve(ctx, addr, api.New(store.New(pool, dupDays)).Routes())
}

func envInt(name string, def int) (int, error) {
	v := os.Getenv(name)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, errors.New(name + " must be an integer")
	}
	return n, nil
}
