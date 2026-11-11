// Command sales runs the sales context: quotations, orders, ATP, credit and holds.
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
	"github.com/BenjaminSRussell/passage/platform/fieldcrypt"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/sales/internal/api"
	"github.com/BenjaminSRussell/passage/services/sales/internal/store"
	"github.com/BenjaminSRussell/passage/services/sales/internal/upstream"
	"github.com/BenjaminSRussell/passage/services/sales/migrations"
)

const (
	defaultHTTPAddr  = ":8080"
	defaultAdminAddr = ":9090"
)

func main() {
	slog.SetDefault(newLogger(os.Stdout))
	if err := run(context.Background(), os.Getenv); err != nil {
		slog.Error("sales stopped", "err", err)
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
	return slog.New(handler).With("service", "sales")
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
	// Migrations run as the owner role in stage and prod (Argo PreSync Job);
	// MIGRATE_ON_START is for local development only.
	if getenv("MIGRATE_ON_START") == "true" {
		if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	if err := tenant.ShareReplayCache(ctx, pool); err != nil {
		return err
	}
	signer, err := tenant.ServiceSignerFromEnv(getenv)
	if err != nil {
		return err
	}
	if signer == nil || getenv("PRODUCT_URL") == "" || getenv("PRODUCTION_URL") == "" {
		slog.Warn("PRODUCT_URL, PRODUCTION_URL or the service signing key is unset: configured order lines are refused (SLS-0048) and production cannot be planned (SLS-0050)")
	}
	if signer == nil || getenv("PARTY_URL") == "" {
		slog.Warn("PARTY_URL or the service signing key is unset: order and quotation creation is refused (SLS-0152) because the customer group cannot be read")
	}
	up := upstream.New(getenv("PRODUCT_URL"), getenv("PRODUCTION_URL"), signer).WithParty(getenv("PARTY_URL"))
	keys, err := fieldcrypt.FromEnv(getenv)
	if err != nil {
		return fmt.Errorf("field encryption keys: %w", err)
	}
	if keys == nil {
		slog.Warn("FIELDCRYPT_KEYS is unset: one-time customer orders are refused (SLS-0054)")
	}
	st := wireReturns(store.New(pool).WithFieldCrypt(keys), up, signer != nil, getenv)
	a := api.New(st).WithConfigurations(up).WithProduction(up).WithCustomerGroups(up)
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

// wireReturns connects the return-order peers that are configured: billing and fulfillment price a return, inventory restocks
// it and receivables credits it. A missing peer leaves its step answering 503 (the return order keeps its state).
func wireReturns(st *store.Postgres, up *upstream.Client, signed bool, getenv func(string) string) *store.Postgres {
	if !signed {
		slog.Warn("the service signing key is unset: return orders cannot be priced, restocked or credited (SLS-0124, SLS-0121)")
		return st
	}
	r := upstream.NewReturns(up, getenv("BILLING_URL"), getenv("FULFILLMENT_URL"), getenv("INVENTORY_URL"), getenv("RECEIVABLES_URL"), getenv("SALES_RETURN_OFFSET_ACCOUNT"))
	if getenv("BILLING_URL") != "" && getenv("FULFILLMENT_URL") != "" {
		st.WithReturnSources(r, r)
	} else {
		slog.Warn("BILLING_URL or FULFILLMENT_URL is unset: return orders cannot be created (SLS-0124)")
	}
	if getenv("INVENTORY_URL") != "" && getenv("SALES_RETURN_OFFSET_ACCOUNT") != "" {
		st.WithInventory(r)
	} else {
		slog.Warn("INVENTORY_URL or SALES_RETURN_OFFSET_ACCOUNT is unset: accepted returns cannot be restocked (SLS-0124)")
	}
	if getenv("RECEIVABLES_URL") != "" {
		st.WithAR(r)
	} else {
		slog.Warn("RECEIVABLES_URL is unset: return orders cannot be credited (SLS-0121)")
	}
	return st
}

func addr(getenv func(string) string, key, fallback string) string {
	if v := getenv(key); v != "" {
		return v
	}
	return fallback
}
