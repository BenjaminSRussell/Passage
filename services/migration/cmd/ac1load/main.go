package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/migration/internal/ac1load"
	"github.com/BenjaminSRussell/passage/services/migration/internal/store"
	"github.com/BenjaminSRussell/passage/services/migration/internal/testdb"
	"github.com/BenjaminSRussell/passage/services/migration/migrations"
)

const throwawayDatabase = "passage_ac1"

func main() {
	report := flag.String("report", "internal/ac1load/testdata/ac1-reconciliation.json", "JSON reconciliation report path")
	markdown := flag.String("markdown", "internal/ac1load/testdata/ac1-reconciliation.md", "Markdown reconciliation report path")
	commit := flag.String("commit", "", "commit the measured tree is at")
	flag.Parse()
	if err := run(context.Background(), *report, *markdown, *commit); err != nil {
		fmt.Fprintf(os.Stderr, "ac1load: %v\n", err)
		os.Exit(1)
	}
}

func refuseUnlessThrowaway(ctx context.Context, owner *pgxpool.Pool) error {
	var name string
	var objects int
	if err := owner.QueryRow(ctx, `select current_database(), (select count(*) from pg_tables where schemaname not in ('pg_catalog', 'information_schema'))`).Scan(&name, &objects); err != nil {
		return err
	}
	if name != throwawayDatabase && objects != 0 {
		return fmt.Errorf("refusing to drop schemas in database %q: it is not %q and not empty", name, throwawayDatabase)
	}
	return nil
}

func run(ctx context.Context, reportPath, markdownPath, commit string) error {
	ownerURL, appURL := os.Getenv("PASSAGE_TEST_DATABASE_URL"), os.Getenv("PASSAGE_TEST_APP_DATABASE_URL")
	if ownerURL == "" || appURL == "" {
		return fmt.Errorf("set PASSAGE_TEST_DATABASE_URL (schema owner) and PASSAGE_TEST_APP_DATABASE_URL (the role the load runs as), or run make ac1-load")
	}
	ctx, cancel := context.WithTimeout(ctx, ac1load.Budget+5*time.Minute)
	defer cancel()
	owner, err := dbx.Connect(ctx, ownerURL)
	if err != nil {
		return err
	}
	defer owner.Close()
	if err := refuseUnlessThrowaway(ctx, owner); err != nil {
		return err
	}
	release, err := testdb.Acquire(ctx, owner)
	if err != nil {
		return err
	}
	defer release()
	app, err := dbx.Connect(ctx, appURL)
	if err != nil {
		return err
	}
	defer app.Close()
	env := ac1load.Env{Commit: commit}
	env.Host, _ = os.Hostname()
	var privileged bool
	if err := app.QueryRow(ctx, `select current_user, r.rolsuper or r.rolbypassrls, version() from pg_roles r where r.rolname = current_user`).Scan(&env.DBRole, &privileged, &env.DBVersion); err != nil {
		return err
	}
	if privileged {
		return fmt.Errorf("role %s bypasses row-level security; the measured load must run as the app role", env.DBRole)
	}
	for _, stmt := range []string{`drop schema if exists migration cascade`, `drop table if exists schema_migrations`} {
		if _, err := owner.Exec(ctx, stmt); err != nil {
			return err
		}
	}
	if err := dbx.Migrate(ctx, owner, migrations.FS); err != nil {
		return err
	}
	if err := ac1load.SetupTarget(ctx, owner); err != nil {
		return err
	}
	principal := tenant.Principal{
		TenantID: "00000000-0000-0000-0000-000000000001",
		Duties:   map[string]struct{}{tenant.DutyAllScope: {}},
	}
	got, err := ac1load.Run(tenant.With(ctx, principal), store.New(app).Xref(), ac1load.NewPGTarget(app), ac1load.FullSize)
	if err != nil {
		return err
	}
	doc := ac1load.Document(got, env)
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
