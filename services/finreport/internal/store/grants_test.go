package store

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/finreport/migrations"
)

func TestGrants_AppRoleLocksDraftsButCannotWriteReadModelOrRewriteAppendOnly(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	ctx := testkit.Ctx(testkit.TenantID(1))
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	st := New(app)
	def := storeFixtureDefinition("GRANTS")
	_, first, err := st.SaveDraft(ctx, def, nil)
	if err != nil {
		t.Fatal(err)
	}
	def.Name = "Second revision"
	if _, rev, err := st.SaveDraft(ctx, def, &first); err != nil || rev != first+1 {
		t.Fatalf("second draft locks and updates the draft as passage_app: rev %d, %v", rev, err)
	}
	ledger := ids.New()
	seedBalance(t, ctx, admin, ledger, 2026, 3, "1000", "acct", "10", 1)
	if _, err := st.Publish(ctx, "GRANTS", "a"); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`update audit_trail_entry set kind = kind`,
		`delete from audit_trail_entry`,
		`update reporting_journal_line set debit = debit`,
		`insert into reporting_hwm(tenant_id, ledger_id, posting_sequence) values ('` + testkit.TenantID(1) + `', '` + ledger + `', 1)`,
		`delete from reporting_balance`,
		`update balance_verification set status = status`,
		`delete from balance_verification`,
		`delete from report_row_set`,
		`delete from report_column_set`,
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
