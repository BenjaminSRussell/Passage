package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/numbering/internal/domain"
	"github.com/BenjaminSRussell/passage/services/numbering/internal/store"
	"github.com/BenjaminSRussell/passage/services/numbering/migrations"
)

func TestGrants_AppRoleCannotRewriteAllocationsOrOutbox(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	ctx := tenant.With(context.Background(), principal(1))
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`update number_allocation set value = value`,
		`delete from number_allocation`,
		`delete from number_sequence`,
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

func TestGrants_LockedAllocationWorksAsAppRole(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	ctx := tenant.With(context.Background(), principal(1))
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(app)
	in := store.Sequence{Code: "INV", Name: "Invoices", Format: "INV-{######}", LegalEntityID: testkit.TenantID(9), Mode: domain.ModeGapFree, StartFrom: 1}
	if _, err := s.Create(ctx, in, "grants-1"); err != nil {
		t.Fatalf("create as app role: %v", err)
	}
	for want := int64(1); want <= 2; want++ {
		got, err := s.Allocate(ctx, "INV", "")
		if err != nil || got.Value != want {
			t.Fatalf("allocate = %+v, %v; want %d", got, err, want)
		}
	}
}
