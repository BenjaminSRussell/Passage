package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/domain"
	"github.com/BenjaminSRussell/passage/services/finreport/internal/events"
	"github.com/BenjaminSRussell/passage/services/finreport/migrations"
)

func storeFixtureCalendar() []domain.Period {
	return []domain.Period{
		{Year: 2026, No: 1, Quarter: 1},
		{Year: 2026, No: 2, Quarter: 1},
		{Year: 2026, No: 3, Quarter: 1},
	}
}

func storeFixtureDefinition(code string) domain.Definition {
	return domain.Definition{
		Code: code, Name: "Store fixture", Kind: domain.KindTrialBalance,
		Rows: []domain.Row{
			{Code: "R001", Kind: domain.RowAccountRange, Label: "Cash", AccountFrom: "1000", AccountTo: "1000", NormalBalance: "debit"},
			{Code: "R002", Kind: domain.RowAccountRange, Label: "Receivables", AccountFrom: "1001", AccountTo: "1001", NormalBalance: "debit"},
		},
		Columns: []domain.Column{{Code: "C01", Label: "Period", PeriodExpr: "P", CurrencyType: "acct"}},
	}
}

// TestCreateRun_DrillSumsAndReplay seeds the read model, publishes a definition,
// runs it, and checks that every cell's balance keys sum to the cell value and
// that a repeated Idempotency-Key returns the stored run (skipped without a DB).
func TestCreateRun_DrillSumsAndReplay(t *testing.T) {
	pool := testkit.DB(t)
	ctx := testkit.Ctx(testkit.TenantID(1), "reporting.definition.maintain", "reporting.statement.run")
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	st := New(pool)
	if _, _, err := st.SaveDraft(ctx, storeFixtureDefinition("STOREFIX"), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Publish(ctx, "STOREFIX", "tester"); err != nil {
		t.Fatal(err)
	}
	ledger := ids.New()
	err := dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
		seed := []struct {
			year, no int
			account  string
			net      string
		}{
			{2026, 3, "1000", "10.500000"}, {2026, 2, "1000", "5.250000"}, {2026, 3, "1001", "3.000000"},
		}
		for _, s := range seed {
			if _, err := tx.Exec(ctx, `insert into reporting_balance(tenant_id, ledger_id, period_year, period_no, quarter, adjustment,
				account, combination, dims, currency_type, net, closing_net)
				values($1,$2,$3,$4,1,false,$5,'-','{}','acct',$6::numeric,0)`,
				testkit.TenantID(1), ledger, s.year, s.no, s.account, s.net); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `insert into reporting_hwm(tenant_id, ledger_id, posting_sequence) values($1,$2,42)`, testkit.TenantID(1), ledger)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	spec := RunSpec{Code: "STOREFIX", LedgerID: ledger, CurrencyType: "acct", PeriodYear: 2026, PeriodNo: 3,
		Calendar: storeFixtureCalendar(), RequestedBy: "tester", IdempotencyKey: "run-1", RequestHash: "h1"}
	run, err := st.CreateRun(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	if run.HWMByLedger[ledger] != 42 || run.CellCount != 2 || len(run.ResultHash) != 64 {
		t.Fatalf("unexpected run: %+v", run)
	}
	cells, err := st.ListCells(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cells {
		if err := c.CheckDrill(); err != nil {
			t.Fatalf("drill %s/%s: %v", c.Row, c.Column, err)
		}
	}

	again, err := st.CreateRun(ctx, spec)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if again.ID != run.ID {
		t.Fatalf("replay returned run %s, want %s", again.ID, run.ID)
	}
	spec.RequestHash = "different"
	if _, err := st.CreateRun(ctx, spec); err == nil {
		t.Fatal("reusing an Idempotency-Key for a different request must fail")
	}
}

func migrated(t *testing.T) (*Postgres, context.Context) {
	t.Helper()
	pool := testkit.DB(t)
	ctx := testkit.Ctx(testkit.TenantID(1))
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	return New(pool), ctx
}

func problemCode(err error) string {
	var p *httpx.Problem
	if errors.As(err, &p) {
		return p.Code
	}
	return ""
}

func outboxTypes(t *testing.T, ctx context.Context, pool *pgxpool.Pool) []string {
	t.Helper()
	var out []string
	err := dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select type from outbox order by created_at, type`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				return err
			}
			out = append(out, s)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func seedBalance(t *testing.T, ctx context.Context, pool *pgxpool.Pool, ledger string, year, no int, account, currency, net string, hwm int64) {
	t.Helper()
	err := dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
		p, _ := tenant.FromContext(ctx)
		if _, err := tx.Exec(ctx, `insert into reporting_balance(tenant_id, ledger_id, period_year, period_no, quarter, account, combination, currency_type, net)
			values($1,$2,$3,$4,1,$5,'-',$6,$7::numeric)`, p.TenantID, ledger, year, no, account, currency, net); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `insert into reporting_hwm(tenant_id, ledger_id, posting_sequence) values($1,$2,$3)
			on conflict (tenant_id, ledger_id) do update set posting_sequence = excluded.posting_sequence`, p.TenantID, ledger, hwm)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// A run over a definition with a non-zero calculation row and a CALC column is
// stored (computed cells carry no keys), and the run and the publish each write
// their event to the outbox in the same transaction.
func TestCreateRun_ComputedCellsAndEvents(t *testing.T) {
	st, ctx := migrated(t)
	def := domain.Definition{Code: "PL-CALC", Name: "Margin", Kind: domain.KindIncomeStatement,
		Rows: []domain.Row{
			{Code: "REV", Kind: domain.RowAccountRange, Label: "Revenue", AccountFrom: "4000", AccountTo: "4999", NormalBalance: "credit"},
			{Code: "COGS", Kind: domain.RowAccountRange, Label: "Cost of sales", AccountFrom: "5000", AccountTo: "5999", NormalBalance: "debit"},
			{Code: "GM", Kind: domain.RowCalculation, Label: "Gross margin", Expression: "REV-COGS", NormalBalance: "credit"},
		},
		Columns: []domain.Column{
			{Code: "C1", Label: "Period", PeriodExpr: "P"},
			{Code: "TXN", Label: "Transaction currency", PeriodExpr: "P", CurrencyType: "txn"},
			{Code: "VAR", Label: "Difference", PeriodExpr: "CALC", Expression: "C1-TXN"},
		},
	}
	if _, _, err := st.SaveDraft(ctx, def, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Publish(ctx, "PL-CALC", "tester"); err != nil {
		t.Fatal(err)
	}
	ledger := ids.New()
	seedBalance(t, ctx, st.pool, ledger, 2026, 3, "4000", "acct", "-1000", 7)
	seedBalance(t, ctx, st.pool, ledger, 2026, 3, "5000", "acct", "600", 7)
	seedBalance(t, ctx, st.pool, ledger, 2026, 3, "4000", "txn", "-900", 9)
	run, err := st.CreateRun(ctx, RunSpec{Code: "PL-CALC", LedgerID: ledger, CurrencyType: "acct", PeriodYear: 2026, PeriodNo: 3,
		Calendar: storeFixtureCalendar(), IdempotencyKey: "calc-1", RequestHash: "h"})
	if err != nil {
		t.Fatal(err)
	}
	if run.HWMByLedger[ledger] != 9 || run.DefinitionVersion != 1 {
		t.Fatalf("run = %+v", run)
	}
	gm, err := st.GetCell(ctx, run.ID, "GM", "C1")
	if err != nil {
		t.Fatal(err)
	}
	if !gm.Computed || gm.Value.String() != "400" || len(gm.Keys) != 0 {
		t.Fatalf("gross margin cell = %+v", gm)
	}
	revTxn, err := st.GetCell(ctx, run.ID, "REV", "TXN")
	if err != nil {
		t.Fatal(err)
	}
	if revTxn.Value.String() != "900" || revTxn.Computed || len(revTxn.Keys) != 1 || revTxn.Keys[0].CurrencyType != "txn" {
		t.Fatalf("revenue in txn currency = %+v", revTxn)
	}
	if variance, _ := st.GetCell(ctx, run.ID, "REV", "VAR"); variance.Value.String() != "100" || !variance.Computed {
		t.Fatalf("variance = %+v", variance)
	}
	if _, err := st.GetCell(ctx, run.ID, "NOPE", "C1"); problemCode(err) != CodeCellNotFound {
		t.Fatalf("missing cell error = %v", err)
	}
	if got := outboxTypes(t, ctx, st.pool); len(got) != 2 || got[0] != events.DefinitionPublished || got[1] != events.StatementGenerated {
		t.Fatalf("outbox = %v", got)
	}
	runs, err := st.ListRuns(ctx, "PL-CALC", 10)
	if err != nil || len(runs) != 1 || runs[0].ID != run.ID || runs[0].ResultHash != run.ResultHash {
		t.Fatalf("ListRuns = %+v, %v", runs, err)
	}
	if runs, _ := st.ListRuns(ctx, "OTHER", 10); len(runs) != 0 {
		t.Fatalf("filtered ListRuns = %+v", runs)
	}
}

func TestCreateRun_RefusesUnpublishedAndUnknownPeriod(t *testing.T) {
	st, ctx := migrated(t)
	if _, _, err := st.SaveDraft(ctx, storeFixtureDefinition("DRAFTONLY"), nil); err != nil {
		t.Fatal(err)
	}
	spec := RunSpec{Code: "DRAFTONLY", LedgerID: ids.New(), CurrencyType: "acct", PeriodYear: 2026, PeriodNo: 3, Calendar: storeFixtureCalendar(), RequestHash: "h"}
	if _, err := st.CreateRun(ctx, spec); problemCode(err) != CodeNotPublished {
		t.Fatalf("draft-only run error = %v, want %s", err, CodeNotPublished)
	}
	if _, err := st.Publish(ctx, "DRAFTONLY", "tester"); err != nil {
		t.Fatal(err)
	}
	spec.PeriodNo = 9
	if _, err := st.CreateRun(ctx, spec); problemCode(err) != domain.CodePeriodNotFound {
		t.Fatalf("unknown period error = %v, want %s", err, domain.CodePeriodNotFound)
	}
	if _, err := st.GetRun(ctx, ids.New()); problemCode(err) != CodeRunNotFound {
		t.Fatalf("missing run error = %v", err)
	}
}

// BR-15.8 and the optimistic draft revision: a second save needs If-Match, a
// stale one is refused, publishing creates immutable versions 1..n.
func TestDrafts_RevisionsAndVersions(t *testing.T) {
	st, ctx := migrated(t)
	def := storeFixtureDefinition("REV-TEST")
	if _, rev, err := st.SaveDraft(ctx, def, nil); err != nil || rev != 1 {
		t.Fatalf("first save rev %d, %v", rev, err)
	}
	if _, _, err := st.SaveDraft(ctx, def, nil); problemCode(err) != "precondition_required" {
		t.Fatalf("save without If-Match = %v", err)
	}
	stale := 5
	if _, _, err := st.SaveDraft(ctx, def, &stale); problemCode(err) != CodeRevisionConflict {
		t.Fatalf("stale save = %v", err)
	}
	one := 1
	def.Name = "Renamed"
	if _, rev, err := st.SaveDraft(ctx, def, &one); err != nil || rev != 2 {
		t.Fatalf("save with If-Match rev %d, %v", rev, err)
	}
	if v1, err := st.Publish(ctx, "REV-TEST", "a"); err != nil || v1.Version != 1 || v1.Name != "Renamed" {
		t.Fatalf("publish v1 = %+v, %v", v1, err)
	}
	if _, err := st.Publish(ctx, "REV-TEST", "a"); problemCode(err) != CodeDefinitionNotFound {
		t.Fatalf("publish without a draft = %v", err)
	}
	def.Name = "Second"
	if _, _, err := st.SaveDraft(ctx, def, nil); err != nil {
		t.Fatal(err)
	}
	if v2, err := st.Publish(ctx, "REV-TEST", "a"); err != nil || v2.Version != 2 {
		t.Fatalf("publish v2 = %+v, %v", v2, err)
	}
	if v1, _, err := st.GetDefinition(ctx, "REV-TEST", 1); err != nil || v1.Name != "Renamed" {
		t.Fatalf("version 1 must be unchanged, got %+v, %v", v1, err)
	}
	if latest, _, err := st.GetDefinition(ctx, "REV-TEST", Latest); err != nil || latest.Version != 2 {
		t.Fatalf("latest = %+v, %v", latest, err)
	}
	list, err := st.ListDefinitions(ctx)
	if err != nil || len(list) != 1 || list[0].LatestVersion != 2 || list[0].HasDraft || list[0].Name != "Second" {
		t.Fatalf("ListDefinitions = %+v, %v", list, err)
	}
}

// FR-15.11: an import never silently replaces an existing code, and its
// Idempotency-Key replays the first result.
func TestImportDefinition_DuplicateOverwriteAndReplay(t *testing.T) {
	st, ctx := migrated(t)
	def := storeFixtureDefinition("IMP-TEST")
	if _, rev, replayed, err := st.ImportDefinition(ctx, def, false, "imp-1", "h1"); err != nil || rev != 1 || replayed {
		t.Fatalf("first import rev %d replayed %v: %v", rev, replayed, err)
	}
	if _, _, replayed, err := st.ImportDefinition(ctx, def, false, "imp-1", "h1"); err != nil || !replayed {
		t.Fatalf("replay replayed %v: %v", replayed, err)
	}
	if _, _, _, err := st.ImportDefinition(ctx, def, false, "imp-1", "other"); problemCode(err) != CodeIdempotencyReuse {
		t.Fatalf("key reuse = %v", err)
	}
	if _, _, _, err := st.ImportDefinition(ctx, def, false, "imp-2", "h2"); problemCode(err) != CodeImportDuplicate {
		t.Fatalf("import over an open draft = %v, want %s", err, CodeImportDuplicate)
	}
	def.Name = "Overwritten"
	if saved, rev, _, err := st.ImportDefinition(ctx, def, true, "", ""); err != nil || rev != 2 || saved.Name != "Overwritten" {
		t.Fatalf("overwrite rev %d: %+v, %v", rev, saved, err)
	}
}

// Row-level security: tenant 2 sees none of tenant 1's definitions, runs,
// balances or outbox rows through an application (non-owner) role.
func TestTenantIsolation(t *testing.T) {
	if os.Getenv(testkit.EnvAppURL) == "" {
		t.Skipf("%s not set; skipping RLS test", testkit.EnvAppURL)
	}
	admin, app := testkit.DBWithApp(t)
	ctx1 := testkit.Ctx(testkit.TenantID(1))
	ctx2 := testkit.Ctx(testkit.TenantID(2))
	if err := dbx.Migrate(ctx1, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	var schema, appUser string
	if err := admin.QueryRow(ctx1, `select current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	if err := app.QueryRow(ctx1, `select current_user`).Scan(&appUser); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx1, fmt.Sprintf(`grant usage on schema %s to %s; grant select, insert, update, delete on all tables in schema %[1]s to %[2]s`, schema, appUser)); err != nil {
		t.Fatal(err)
	}
	st := New(app)
	if _, _, err := st.SaveDraft(ctx1, storeFixtureDefinition("TENANT-ONE"), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Publish(ctx1, "TENANT-ONE", "a"); err != nil {
		t.Fatal(err)
	}
	ledger := ids.New()
	seedBalance(t, ctx1, app, ledger, 2026, 3, "1000", "acct", "10", 1)
	run, err := st.CreateRun(ctx1, RunSpec{Code: "TENANT-ONE", LedgerID: ledger, CurrencyType: "acct", PeriodYear: 2026, PeriodNo: 3,
		Calendar: storeFixtureCalendar(), IdempotencyKey: "shared-key", RequestHash: "h"})
	if err != nil {
		t.Fatal(err)
	}
	if list, err := st.ListDefinitions(ctx2); err != nil || len(list) != 0 {
		t.Fatalf("tenant 2 definitions = %+v, %v", list, err)
	}
	if _, _, err := st.SaveHierarchy(ctx1, accountTree(true), nil, "a"); err != nil {
		t.Fatal(err)
	}
	if list, err := st.ListHierarchies(ctx2); err != nil || len(list) != 0 {
		t.Fatalf("tenant 2 hierarchies = %+v, %v", list, err)
	}
	if _, _, err := st.GetHierarchy(ctx2, "HIER-BS", Draft); problemCode(err) != CodeHierarchyNotFound {
		t.Fatalf("tenant 2 reading tenant 1's hierarchy = %v", err)
	}
	if _, err := st.GetRun(ctx2, run.ID); problemCode(err) != CodeRunNotFound {
		t.Fatalf("tenant 2 reading tenant 1's run = %v", err)
	}
	if runs, _ := st.ListRuns(ctx2, "", 10); len(runs) != 0 {
		t.Fatalf("tenant 2 runs = %+v", runs)
	}
	if lag, _ := st.Lag(ctx2); len(lag) != 0 {
		t.Fatalf("tenant 2 lag = %+v", lag)
	}
	if got := outboxTypes(t, ctx2, app); len(got) != 0 {
		t.Fatalf("tenant 2 outbox = %v", got)
	}
	// The same code and Idempotency-Key are independent per tenant.
	if _, _, err := st.SaveDraft(ctx2, storeFixtureDefinition("TENANT-ONE"), nil); err != nil {
		t.Fatalf("tenant 2 reusing a code: %v", err)
	}
	if _, err := st.CreateRun(ctx2, RunSpec{Code: "TENANT-ONE", LedgerID: ledger, CurrencyType: "acct", PeriodYear: 2026, PeriodNo: 3,
		Calendar: storeFixtureCalendar(), IdempotencyKey: "shared-key", RequestHash: "h"}); problemCode(err) != CodeNotPublished {
		t.Fatalf("tenant 2 run must not replay tenant 1's run, got %v", err)
	}
	err = dbx.WithTenantTx(ctx2, app, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx2, `insert into reporting_hwm(tenant_id, ledger_id, posting_sequence) values($1,$2,1)`, testkit.TenantID(1), ids.New())
		return err
	})
	if err == nil {
		t.Fatal("writing a row for another tenant must be refused by the policy")
	}
}

// AC-15.5 and AC-15.7: a published definition exported and re-imported under a
// new code produces identical cells, and two runs at the same HWM have the same
// result hash while a later posting changes it.
func TestExportImportRoundTripAndReproducibleHash(t *testing.T) {
	st, ctx := migrated(t)
	def := storeFixtureDefinition("RT-SOURCE")
	def.Rows = append(def.Rows,
		domain.Row{Code: "TOT", Kind: domain.RowTotal, Label: "Total", References: []string{"R001", "R002"}, NormalBalance: "debit"},
		domain.Row{Code: "SHARE", Kind: domain.RowCalculation, Label: "Cash share", Expression: "R001/TOT*100", NormalBalance: "debit"})
	def.Columns = append(def.Columns, domain.Column{Code: "C02", Label: "YTD", PeriodExpr: "YTD"})
	if _, _, err := st.SaveDraft(ctx, def, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Publish(ctx, "RT-SOURCE", "a"); err != nil {
		t.Fatal(err)
	}
	exported, _, err := st.GetDefinition(ctx, "RT-SOURCE", Latest)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := exported.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	imported, err := domain.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	imported.Code = "RT-COPY"
	if _, _, _, err := st.ImportDefinition(ctx, imported, false, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Publish(ctx, "RT-COPY", "a"); err != nil {
		t.Fatal(err)
	}
	ledger := ids.New()
	seedBalance(t, ctx, st.pool, ledger, 2026, 2, "1000", "acct", "30.25", 10)
	seedBalance(t, ctx, st.pool, ledger, 2026, 3, "1000", "acct", "10", 10)
	seedBalance(t, ctx, st.pool, ledger, 2026, 3, "1001", "acct", "40", 10)
	runOf := func(code, key string) Run {
		t.Helper()
		run, err := st.CreateRun(ctx, RunSpec{Code: code, LedgerID: ledger, CurrencyType: "acct", PeriodYear: 2026, PeriodNo: 3,
			Calendar: storeFixtureCalendar(), IdempotencyKey: key, RequestHash: key})
		if err != nil {
			t.Fatal(err)
		}
		return run
	}
	values := func(run Run) []string {
		t.Helper()
		cells, err := st.ListCells(ctx, run.ID)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, c := range cells {
			out = append(out, fmt.Sprintf("%s/%s=%s/%t", c.Row, c.Column, c.Value.StringFixed(6), c.Blank))
		}
		return out
	}
	source, copied := runOf("RT-SOURCE", "k-src"), runOf("RT-COPY", "k-copy")
	if a, b := values(source), values(copied); fmt.Sprint(a) != fmt.Sprint(b) || len(a) != 8 {
		t.Fatalf("source cells %v\ncopy cells   %v", a, b)
	}
	if source.ResultHash != copied.ResultHash {
		t.Fatal("identical definitions over the same HWM must hash identically")
	}
	if again := runOf("RT-SOURCE", "k-src-2"); again.ResultHash != source.ResultHash || again.ID == source.ID {
		t.Fatalf("re-run at the same HWM: %+v vs %+v", again, source)
	}
	seedBalance(t, ctx, st.pool, ledger, 2026, 3, "1000", "txn", "1", 11) // other currency type: cells unchanged, HWM moves
	seedBalance(t, ctx, st.pool, ledger, 2026, 1, "1001", "acct", "5", 12)
	if later := runOf("RT-SOURCE", "k-src-3"); later.ResultHash == source.ResultHash || later.HWMByLedger[ledger] != 12 {
		t.Fatalf("a later posting must change the hash and the HWM: %+v", later)
	}
}
