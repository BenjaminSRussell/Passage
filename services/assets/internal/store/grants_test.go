package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/assets/internal/store"
	"github.com/BenjaminSRussell/passage/services/assets/migrations"
)

func appPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	return app
}

func TestGrants_AppRoleCannotRewriteAppendOnlyTables(t *testing.T) {
	app := appPool(t)
	ctx := scoped(1, "")
	for _, stmt := range []string{
		`update asset_transaction set type = type`,
		`delete from asset_transaction`,
		`update outbox set type = type`,
		`select count(*) from outbox`,
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

func TestGrants_LockedAssetAndBookWorkAsAppRole(t *testing.T) {
	s, ctx := store.New(appPool(t)), scoped(1, "")
	le := ids.New()
	a := newAsset(t, s, ctx, le, "FA-G1")
	if _, err := s.AddBook(ctx, a.ID, book("IFRS", "1200"), "grants-book"); err != nil {
		t.Fatalf("add book locks the asset FOR UPDATE: %v", err)
	}
}
