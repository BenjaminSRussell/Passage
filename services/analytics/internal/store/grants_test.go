package store

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/analytics/migrations"
)

func appPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	return app
}

func TestGrants_AppRoleCannotRewriteOutbox(t *testing.T) {
	app := appPool(t)
	ctx := ctxFor(1, "")
	for _, stmt := range []string{`update outbox set type = type`, `delete from outbox`, `select count(*) from outbox`} {
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

func TestGrants_ReconciliationUpsertWorksAsAppRole(t *testing.T) {
	app := appPool(t)
	st := New(app)
	ctx := ctxFor(1, "")
	if _, _, err := st.RecordReconciliation(ctx, recon(leUSMF, "revenue", "2026-10-01", "1", "1")); err != nil {
		t.Fatal(err)
	}
	got, changed, err := st.RecordReconciliation(ctx, recon(leUSMF, "revenue", "2026-10-01", "1", "2"))
	if err != nil || !changed || got.Status != "FAILED" {
		t.Fatalf("upsert as passage_app = %+v changed=%v, %v", got, changed, err)
	}
}
