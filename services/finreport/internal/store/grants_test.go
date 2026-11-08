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
		`delete from report_tree`,
		`update report_library_sweep set cutoff = cutoff`,
		`delete from report_library_sweep`,
		`update report_run_sweep set cutoff = cutoff`,
		`delete from report_run_sweep`,
		`delete from statement_cell`,
		`update statement_run set started_at = started_at`,
		`update statement_run set legal_entity_id = legal_entity_id`,
		`update statement_run set legal_entity_ids = legal_entity_ids`,
		`delete from report_legal_hold`,
		`delete from report_share_link`,
		`update report_share_link set expires_at = expires_at`,
		`update report_share_link set library_id = library_id`,
		`update report_share_link set token_hash = token_hash`,
		`update report_share_link set created_by = created_by`,
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
