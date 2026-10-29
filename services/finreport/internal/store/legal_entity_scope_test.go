package store

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/finreport/internal/domain"
	"github.com/BenjaminSRussell/passage/services/finreport/migrations"
)

func scopedCtx(tenantID, scope string) context.Context {
	duties := map[string]struct{}{}
	if scope == "" {
		duties["org.switcher.all_scope"] = struct{}{}
	}
	return tenant.With(context.Background(), tenant.Principal{TenantID: tenantID, LegalEntityID: scope, UserID: "u", Duties: duties})
}

func countAs(t *testing.T, app *pgxpool.Pool, ctx context.Context, query string, args ...any) (n int) {
	t.Helper()
	err := dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, query, args...).Scan(&n)
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func requireRLSViolation(t *testing.T, err error, msg string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
		t.Errorf("%s: %v, want insufficient_privilege 42501", msg, err)
	}
}

func TestLegalEntityScope_RunsCellsAndReadModel(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	all := scopedCtx(testkit.TenantID(1), "")
	if err := dbx.Migrate(all, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	testkit.AssertTenantRLS(t, admin)
	st := New(app)
	entityA, entityB := ids.New(), ids.New()
	ctxA, ctxB := scopedCtx(testkit.TenantID(1), entityA), scopedCtx(testkit.TenantID(1), entityB)
	otherTenant := scopedCtx(testkit.TenantID(2), "")

	if _, _, err := st.SaveDraft(all, storeFixtureDefinition("LE"), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Publish(all, "LE", "tester"); err != nil {
		t.Fatal(err)
	}
	ledger := ids.New()
	seedBalance(t, all, admin, ledger, 2026, 3, "1000", "acct", "10", 7)
	for _, table := range []string{"reporting_balance", "reporting_hwm"} {
		if _, err := admin.Exec(all, `update `+table+` set legal_entity_id = $1`, entityA); err != nil {
			t.Fatal(err)
		}
	}

	spec := RunSpec{Code: "LE", LedgerID: ledger, CurrencyType: "acct", PeriodYear: 2026, PeriodNo: 3, Calendar: storeFixtureCalendar(), RequestedBy: "tester", RequestHash: "h"}
	run, err := st.CreateRun(ctxA, spec)
	if err != nil {
		t.Fatalf("entity A run: %v", err)
	}
	if run.CellCount != 2 {
		t.Fatalf("entity A run has %d cells, want 2", run.CellCount)
	}

	t.Run("run defaults to the caller's entity and is hidden from others", func(t *testing.T) {
		if n := countAs(t, app, ctxA, `select count(*) from statement_run where id = $1 and legal_entity_id = $2`, run.ID, entityA); n != 1 {
			t.Errorf("entity A sees %d of its runs, want 1", n)
		}
		if n := countAs(t, app, ctxB, `select count(*) from statement_run where id = $1`, run.ID); n != 0 {
			t.Errorf("entity B reads %d of entity A's runs", n)
		}
		if _, err := st.GetRun(ctxB, run.ID); err == nil {
			t.Error("entity B fetched entity A's run")
		}
		if cells, err := st.ListCells(ctxB, run.ID); err != nil || len(cells) != 0 {
			t.Errorf("entity B lists %d cells of entity A's run (%v)", len(cells), err)
		}
		if n := countAs(t, app, all, `select count(*) from statement_run where id = $1`, run.ID); n != 1 {
			t.Errorf("all-entity caller sees %d runs, want 1", n)
		}
		if n := countAs(t, app, otherTenant, `select count(*) from statement_run`); n != 0 {
			t.Errorf("another tenant reads %d runs", n)
		}
	})

	t.Run("cells follow their run", func(t *testing.T) {
		count := func(ctx context.Context) int {
			return countAs(t, app, ctx, `select count(*) from statement_cell where run_id = $1`, run.ID)
		}
		if count(ctxA) != 2 || count(all) != 2 || count(ctxB) != 0 || count(otherTenant) != 0 {
			t.Errorf("cell visibility A=%d all=%d B=%d other tenant=%d, want 2 2 0 0", count(ctxA), count(all), count(ctxB), count(otherTenant))
		}
		insert := `insert into statement_cell(tenant_id, run_id, row_code, column_code) values($1, $2, 'RX', 'CX')`
		err := dbx.WithTenantTx(ctxB, app, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctxB, insert, testkit.TenantID(1), run.ID)
			return err
		})
		requireRLSViolation(t, err, "entity B cell insert under entity A's run")
		err = dbx.WithTenantTx(ctxA, app, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctxA, insert, testkit.TenantID(1), run.ID)
			return err
		})
		if err != nil {
			t.Errorf("entity A cell insert under its own run: %v", err)
		}
	})

	t.Run("a scoped caller cannot create a run for another entity", func(t *testing.T) {
		other := spec
		other.LegalEntityID = entityB
		other.RequestHash = "h-other"
		_, err := st.CreateRun(ctxA, other)
		var p *httpx.Problem
		if !errors.As(err, &p) || p.Status != http.StatusForbidden {
			t.Fatalf("err = %v, want 403 problem", err)
		}
		if n := countAs(t, app, all, `select count(*) from statement_run`); n != 1 {
			t.Errorf("rejected run left %d rows, want 1", n)
		}
	})

	t.Run("read model rows are scoped", func(t *testing.T) {
		for _, table := range []string{"reporting_balance", "reporting_hwm"} {
			q := `select count(*) from ` + table
			if countAs(t, app, ctxA, q) == 0 || countAs(t, app, all, q) == 0 {
				t.Errorf("%s: entity A and the all-entity caller must see the ledger", table)
			}
			if n := countAs(t, app, ctxB, q); n != 0 {
				t.Errorf("%s: entity B reads %d of entity A's rows", table, n)
			}
			if n := countAs(t, app, otherTenant, q); n != 0 {
				t.Errorf("%s: another tenant reads %d rows", table, n)
			}
		}
		if _, err := admin.Exec(all, `insert into reporting_hwm(tenant_id, ledger_id, posting_sequence) values($1, $2, 1)`, testkit.TenantID(1), ids.New()); err != nil {
			t.Fatal(err)
		}
		if n := countAs(t, app, ctxA, `select count(*) from reporting_hwm`); n != 1 {
			t.Errorf("entity A sees %d hwm rows, want 1: an unassigned row is invisible to scoped callers", n)
		}
		if n := countAs(t, app, all, `select count(*) from reporting_hwm`); n != 2 {
			t.Errorf("all-entity caller sees %d hwm rows, want 2", n)
		}
	})

	t.Run("journal lines and ledger audit entries are scoped", func(t *testing.T) {
		lineA := line(ledger, 1, "2026-03-01", 3, "1000", "5", "0")
		if err := st.PutJournalLines(ctxA, []domain.JournalLine{lineA}); err != nil {
			t.Fatalf("entity A line: %v", err)
		}
		lineB := line(ledger, 2, "2026-03-01", 3, "1000", "5", "0")
		lineB.LegalEntityID = entityA
		err := st.PutJournalLines(ctxB, []domain.JournalLine{lineB})
		var p *httpx.Problem
		if !errors.As(err, &p) || p.Status != http.StatusForbidden {
			t.Fatalf("entity B line for entity A: %v, want 403 problem", err)
		}
		q := `select count(*) from reporting_journal_line`
		if countAs(t, app, ctxA, q) != 1 || countAs(t, app, all, q) != 1 || countAs(t, app, ctxB, q) != 0 {
			t.Error("journal line visibility must be A=1 all=1 B=0")
		}
		audit := `select count(*) from audit_trail_entry where ledger_id is not null`
		if countAs(t, app, ctxA, audit) == 0 || countAs(t, app, ctxB, audit) != 0 {
			t.Error("the run's ledger audit entry must be visible to entity A and hidden from entity B")
		}
		if n := countAs(t, app, ctxB, `select count(*) from audit_trail_entry where ledger_id is null`); n == 0 {
			t.Error("tenant-level reporting audit entries must stay visible to every entity")
		}
	})
}
