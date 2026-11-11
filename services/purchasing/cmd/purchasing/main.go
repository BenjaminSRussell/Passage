// Command purchasing runs the purchasing API: requisitions, purchase orders,
// trade agreements and price lookup (PSG-17, wave 17 slice 1).
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
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/api"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/budget"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
	"github.com/BenjaminSRussell/passage/services/purchasing/migrations"
)

const (
	defaultHTTPAddr  = ":8080"
	defaultAdminAddr = ":9090"
)

func main() {
	slog.SetDefault(newLogger(os.Stdout))
	run := run
	if len(os.Args) > 1 && os.Args[1] == "consume" {
		run = runConsume
	} else if len(os.Args) > 1 {
		slog.Error("purchasing stopped", "err", fmt.Errorf("unknown command %q (use no argument to serve or \"consume\" to follow fulfillment and planning events)", os.Args[1]))
		os.Exit(1)
	}
	if err := run(context.Background(), os.Getenv); err != nil {
		slog.Error("purchasing stopped", "err", err)
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
	return slog.New(handler).With("service", "purchasing")
}

func run(ctx context.Context, getenv func(string) string) error {
	if err := tenant.ConfigureServiceVerifier(getenv); err != nil {
		return err
	}
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
	if err := tenant.ShareReplayCache(ctx, pool); err != nil {
		return err
	}
	st := store.New(pool)
	// Budget control converts a requisition line's pre-encumbrance when a PO line is created from it (FR-38.5). Unset, orders
	// are still created and the pre-encumbrance stays until budgeting is told some other way.
	if url := getenv("BUDGETING_URL"); url != "" {
		signer, err := tenant.ServiceSignerFromEnv(getenv)
		if err != nil {
			return err
		}
		if signer == nil {
			return errors.New("BUDGETING_URL needs a service signing key (PASSAGE_SERVICE_SIGNING_KEY_FILE)")
		}
		st.WithOrderHook(budget.Hook(budget.New(url, nil, signer)))
	} else {
		slog.Warn("BUDGETING_URL is empty: requisition pre-encumbrances are not converted when a purchase order line is created")
	}
	a := api.New(st).WithVendorPrincipal(api.PortalVendorPrincipal{})
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
