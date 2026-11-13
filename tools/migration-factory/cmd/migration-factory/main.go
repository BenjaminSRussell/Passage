// Command migration-factory serves the migration factory API: open-items pack runs with
// control totals, the legacy cross-reference and the cutover runbook (PSG-49).
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/tenant"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/api"
	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/store"
	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/target"
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

// targetConfig reads the base address in envURL and the service signing key the factory's calls are signed with. The
// key must belong to the service named "migration-factory" (PASSAGE_SERVICE_NAME), the caller the service grants are
// written for. With neither set the service starts and the load step answers 503; an address without a key, or a key
// for another service, stops it, because every call would be refused.
func targetConfig(getenv func(string) string, envURL string) (target.Config, error) {
	base := getenv(envURL)
	signer, err := tenant.ServiceSignerFromEnv(getenv)
	if err != nil {
		return target.Config{}, err
	}
	if base != "" {
		if u, err := url.Parse(base); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return target.Config{}, fmt.Errorf("%s must be an http or https base address such as http://receivables:8080", envURL)
		}
	}
	switch {
	case base != "" && signer == nil:
		return target.Config{}, fmt.Errorf("%s is set but no service signing key is (PASSAGE_SERVICE_SIGNING_KEY or PASSAGE_SERVICE_SIGNING_KEY_FILE)", envURL)
	case signer != nil && getenv(tenant.EnvServiceName) != target.ServiceName:
		return target.Config{}, fmt.Errorf("%s must be %q for the factory's service grants", tenant.EnvServiceName, target.ServiceName)
	case base == "" || signer == nil:
		return target.Config{}, nil
	}
	return target.Config{BaseURL: base, Signer: signer}, nil
}

// receivablesTarget reads RECEIVABLES_URL (see targetConfig); without it the load step answers 503 MIG-1520.
func receivablesTarget(getenv func(string) string) (*target.Receivables, error) {
	cfg, err := targetConfig(getenv, "RECEIVABLES_URL")
	if err != nil {
		return nil, err
	}
	return target.NewReceivables(cfg), nil
}

// payablesTarget reads PAYABLES_URL (see targetConfig); without it the AP load answers 503 MIG-1550.
func payablesTarget(getenv func(string) string) (*target.Payables, error) {
	cfg, err := targetConfig(getenv, "PAYABLES_URL")
	if err != nil {
		return nil, err
	}
	return target.NewPayables(cfg), nil
}

// inventoryTarget reads INVENTORY_URL (see targetConfig); without it the onhand load answers 503 MIG-1560.
func inventoryTarget(getenv func(string) string) (*target.Inventory, error) {
	cfg, err := targetConfig(getenv, "INVENTORY_URL")
	if err != nil {
		return nil, err
	}
	return target.NewInventory(cfg), nil
}

// treasuryTarget reads TREASURY_URL (see targetConfig); without it the bank_balances load answers 503 MIG-1560.
func treasuryTarget(getenv func(string) string) (*target.Treasury, error) {
	cfg, err := targetConfig(getenv, "TREASURY_URL")
	if err != nil {
		return nil, err
	}
	return target.NewTreasury(cfg), nil
}

// xrefTarget reads MIGRATION_URL (see targetConfig): the migration service whose cross-reference resolves the legacy customers of
// an open-items run (H-S8-6). Without it a customer must be on the record or in load.customerIds.
func xrefTarget(getenv func(string) string) (*target.Xref, error) {
	cfg, err := targetConfig(getenv, "MIGRATION_URL")
	if err != nil {
		return nil, err
	}
	return target.NewXref(cfg), nil
}

func run(ctx context.Context, getenv func(string) string) error {
	databaseURL := getenv("DATABASE_URL")
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	receivables, err := receivablesTarget(getenv)
	if err != nil {
		return err
	}
	payables, err := payablesTarget(getenv)
	if err != nil {
		return err
	}
	inventory, err := inventoryTarget(getenv)
	if err != nil {
		return err
	}
	treasury, err := treasuryTarget(getenv)
	if err != nil {
		return err
	}
	xref, err := xrefTarget(getenv)
	if err != nil {
		return err
	}
	// LEDGER_URL is optional: without it the parallel-run replay and the rollback compensations
	// answer 503 MIG-1530; with it the service signing key is required.
	ledger, err := ledgerclient.FromEnv(getenv, target.ServiceName)
	if err != nil {
		return fmt.Errorf("ledger client: %w", err)
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
	srv := api.New(store.New(pool)).WithTarget(receivables).WithPayables(payables).WithInventory(inventory).WithTreasury(treasury).WithXref(xref)
	if ledger != nil {
		srv = srv.WithLedger(ledger)
	}
	return httpx.Serve(ctx, addr, srv.Routes())
}
