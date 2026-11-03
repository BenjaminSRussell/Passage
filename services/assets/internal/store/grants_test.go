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
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
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

func TestGrants_LeaseBookingAndProfileWorkAsAppRole(t *testing.T) {
	s, ctx := store.New(appPool(t)), scoped(1, "")
	le, ledgerID, fake := ids.New(), ids.New(), ledgerclient.NewFake()
	lease := workedLease(t, s, ctx, le, "L-GRANT")
	if _, _, err := s.SaveLeasePostingProfile(ctx, store.LeasePostingProfile{LegalEntityID: le, ROUAsset: "1750", ROUAccumulated: "1795", Liability: "2450", InterestExpense: "7250", DepreciationExpense: "6850", PaymentClearing: "2150", RemeasurementGain: "7150"}, 0, "grants"); err != nil {
		t.Fatalf("profile insert: %v", err)
	}
	res, err := s.PostLeasesToGL(ctx, fake, store.LeaseGLPostRequest{LegalEntityID: le, LedgerID: ledgerID, PostThrough: day("2026-12-31")})
	if err != nil || len(res.Booked) != 2 || len(res.Failed) != 0 {
		t.Fatalf("booking stores the pending row, links the journal on the lease and its line, and writes the event: %+v %v", res, err)
	}
	if _, err := s.ModifyLease(ctx, lease.ID, store.LeaseModificationInput{Version: lease.Version, EffectivePeriod: 1, PaymentAmount: d("1")}, "grant-mod"); problemCode(err) != store.CodeLeasePeriodPosted {
		t.Fatalf("modify checks the pending bookings and posted lines: %v", err)
	}
}
