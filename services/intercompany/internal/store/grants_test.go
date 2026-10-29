package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/intercompany/internal/domain"
	"github.com/BenjaminSRussell/passage/services/intercompany/internal/store"
	"github.com/BenjaminSRussell/passage/services/intercompany/migrations"
)

func appSetup(t *testing.T) (*store.Postgres, *pgxpool.Pool, context.Context) {
	t.Helper()
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return store.New(app), app, testkit.Ctx(testkit.TenantID(1))
}

func TestGrants_AppRoleCannotRewriteOutboxOrDeletePartners(t *testing.T) {
	_, app, ctx := appSetup(t)
	for _, stmt := range []string{
		`select count(*) from outbox`,
		`update outbox set type = type`,
		`delete from intercompany_partner`,
		`delete from idempotency_keys`,
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

func TestGrants_LockedUpdateWorksAsAppRole(t *testing.T) {
	s, _, ctx := appSetup(t)
	rec, _, err := s.CreatePartner(ctx, pairing("P", "S"), "grants")
	if err != nil {
		t.Fatal(err)
	}
	off := false
	if got, err := s.UpdatePartner(ctx, rec.ID, 1, domain.PartnerPatch{Active: &off}); err != nil || got.Active {
		t.Fatalf("update as app role: %+v, %v", got, err)
	}
}
