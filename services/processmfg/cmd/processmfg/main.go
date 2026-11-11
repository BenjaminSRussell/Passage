// Command processmfg runs the Passage process-manufacturing service.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/features"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/processmfg/internal/api"
	"github.com/BenjaminSRussell/passage/services/processmfg/internal/shopfloor"
	"github.com/BenjaminSRussell/passage/services/processmfg/internal/store"
	"github.com/BenjaminSRussell/passage/services/processmfg/migrations"
)

const defaultHTTPAddr = ":8080"

func main() {
	slog.SetDefault(newLogger(os.Stdout))
	if err := run(context.Background(), os.Getenv); err != nil {
		slog.Error("processmfg stopped", "err", err)
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
	return slog.New(handler).With("service", "processmfg")
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
	router := httpx.NewRouter()
	h := api.New(store.New(pool))
	// Batch record signing fails closed (503) until a step-up verifier exists;
	// only a dev deployment binds the stand-in, whose signatures say unverified.
	if getenv("PASSAGE_ENV") == "dev" {
		h.WithReauth(api.DevReauth{})
	}
	identityURL, shopfloorURL := getenv("IDENTITY_URL"), getenv("SHOPFLOOR_URL")
	if identityURL != "" || shopfloorURL != "" {
		signer, err := tenant.ServiceSignerFromEnv(getenv)
		if err != nil {
			return err
		}
		if signer == nil {
			return errors.New("IDENTITY_URL and SHOPFLOOR_URL need a service signing key (PASSAGE_SERVICE_NAME and PASSAGE_SERVICE_SIGNING_KEY or PASSAGE_SERVICE_SIGNING_KEY_FILE)")
		}
		if identityURL != "" {
			source := features.NewCache(features.NewHTTPSource(identityURL, nil, signer), features.DefaultTTL)
			h.WithFeatureGate(features.Require(source, "process-manufacturing"))
		}
		if shopfloorURL != "" {
			// Releasing a batch order generates its control recipe in the shop floor service (#25).
			client, err := shopfloor.New(shopfloorURL, signer, nil)
			if err != nil {
				return err
			}
			h.WithShopfloor(client)
		}
	}
	h.Register(router)
	addr := getenv("HTTP_ADDR")
	if addr == "" {
		addr = defaultHTTPAddr
	}
	return httpx.Serve(ctx, addr, router)
}
