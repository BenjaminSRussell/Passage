// Command demoplant seeds the demo plant (internal/demoplant) into a production database and, when LEDGER_URL and
// -ledger-id are given, books its WIP in the general ledger and prints the reconciliation. See the README, "Demo plant".
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/production/internal/demoplant"
	"github.com/BenjaminSRussell/passage/services/production/internal/store"
	"github.com/BenjaminSRussell/passage/services/production/migrations"
)

const (
	defaultTenant      = "00000000-0000-0000-0000-000000000001"
	defaultLegalEntity = "00000000-0000-0000-0000-0000000000e1"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Getenv, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "demoplant:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, getenv func(string) string, out io.Writer) error {
	fs := flag.NewFlagSet("demoplant", flag.ContinueOnError)
	tenantID := fs.String("tenant", defaultTenant, "tenant UUID")
	legalEntity := fs.String("legal-entity", defaultLegalEntity, "legal entity UUID the plant belongs to")
	currency := fs.String("currency", "USD", "order currency")
	prefix := fs.String("prefix", demoplant.DefaultPrefix, "idempotency-key prefix; a new prefix seeds a fresh set")
	postingDate := fs.String("posting-date", "", "journal posting date YYYY-MM-DD (default today)")
	ledgerID := fs.String("ledger-id", "", "general ledger to book the WIP in (needs LEDGER_URL and a production signing key)")
	migrate := fs.Bool("migrate", false, "apply the production migrations first")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !ids.Valid(*tenantID) || !ids.Valid(*legalEntity) {
		return errors.New("-tenant and -legal-entity must be UUIDs")
	}
	databaseURL := getenv("DATABASE_URL")
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	cfg := demoplant.Config{LegalEntityID: *legalEntity, Currency: *currency, Prefix: *prefix, PostingDate: *postingDate}
	if *ledgerID != "" {
		if !ids.Valid(*ledgerID) {
			return errors.New("-ledger-id must be a UUID")
		}
		ledger, err := ledgerclient.FromEnv(getenv, "production")
		if err != nil {
			return err
		}
		if ledger == nil {
			return errors.New("-ledger-id needs LEDGER_URL")
		}
		cfg.Ledger, cfg.LedgerID = ledger, *ledgerID
	}
	pool, err := dbx.Connect(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("connect database: %w", err)
	}
	defer pool.Close()
	if *migrate {
		if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	ctx = tenant.With(ctx, tenant.Principal{TenantID: *tenantID, LegalEntityID: *legalEntity, UserID: "demo-plant-seed", Duties: map[string]struct{}{}})
	res, err := demoplant.Seed(ctx, store.New(pool), cfg)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(res)
}
