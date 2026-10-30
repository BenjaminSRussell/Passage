package store

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
)

func appOf(t *testing.T, admin *pgxpool.Pool) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv(testkit.EnvAppURL)
	if url == "" {
		return admin
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = currentSchema(t, admin) + ",public"
	app, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	return app
}

func TestGrants_AppRoleCannotRewriteAppendOnlyTables(t *testing.T) {
	if os.Getenv(testkit.EnvAppURL) == "" {
		t.Skipf("%s is needed for the grants test", testkit.EnvAppURL)
	}
	_, admin, ctx := migrated(t)
	app := appOf(t, admin)
	for _, stmt := range []string{
		`update sales_order_version set version = version`,
		`delete from sales_order_version`,
		`update agreement_release set quantity = quantity`,
		`delete from sales_order`,
		`delete from sales_quotation`,
		`delete from allocation_quota`,
		`delete from allocation_consumption`,
		`delete from order_schedule_line`,
		`delete from schedule_release`,
		`delete from schedule_release_line`,
		`select count(*) from outbox`,
		`update outbox set type = type`,
	} {
		err := dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, stmt)
			return err
		})
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Errorf("%q = %v; want insufficient_privilege 42501", stmt, err)
		}
	}
}
