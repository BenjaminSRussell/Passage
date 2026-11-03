// Command ac1load runs the Wave 10 AC1 seeded load against Postgres and writes a
// reconciliation report. See services/migration/scripts/ac1-load.sh.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/migration/internal/ac1load"
	"github.com/BenjaminSRussell/passage/services/migration/internal/store"
	"github.com/BenjaminSRussell/passage/services/migration/migrations"
)

func main() {
	report := flag.String("report", "internal/ac1load/testdata/ac1-reconciliation.json", "JSON reconciliation report path")
	markdown := flag.String("markdown", "internal/ac1load/testdata/ac1-reconciliation.md", "Markdown reconciliation report path")
	flag.Parse()
	if err := run(context.Background(), *report, *markdown); err != nil {
		fmt.Fprintf(os.Stderr, "ac1load: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, reportPath, markdownPath string) error {
	url := os.Getenv("PASSAGE_TEST_DATABASE_URL")
	if url == "" {
		url = os.Getenv("DATABASE_URL")
	}
	if url == "" {
		return fmt.Errorf("set PASSAGE_TEST_DATABASE_URL or DATABASE_URL (or run make ac1-load)")
	}
	ctx, cancel := context.WithTimeout(ctx, ac1load.Budget+time.Minute)
	defer cancel()
	pool, err := dbx.Connect(ctx, url)
	if err != nil {
		return err
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `drop schema if exists migration cascade`); err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, `drop table if exists schema_migrations`); err != nil {
		return err
	}
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		return err
	}
	principal := tenant.Principal{
		TenantID: "00000000-0000-0000-0000-000000000001",
		Duties:   map[string]struct{}{tenant.DutyAllScope: {}},
	}
	got, err := ac1load.Run(tenant.With(ctx, principal), store.New(pool).Xref(), ac1load.FullSize)
	if err != nil {
		return err
	}
	doc := ac1load.Document(got)
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(reportPath, append(raw, '\n'), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(markdownPath, []byte(ac1load.Markdown(doc)), 0o644); err != nil {
		return err
	}
	fmt.Print(ac1load.Markdown(doc))
	if !doc.OK {
		return fmt.Errorf("AC1 missed the %s budget or failed reconciliation (elapsed %s)", ac1load.Budget, got.Elapsed)
	}
	return nil
}
