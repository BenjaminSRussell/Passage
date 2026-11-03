package api

import (
	"context"
	"encoding/csv"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/migrations"
)

// S11-a: a receivable and a payable with the same legacy id (AX CustTransOpen and VendTransOpen RecIds)
// are two open items: both load, each under its side's source table, and a re-run holds both.
func TestOpenItems_ReceivableAndPayableWithOneLegacyIDAreTwoItems(t *testing.T) {
	e := newLoadEnv(t, 3, nil)
	ap := `{"legacyId":"5001","party":"V001","account":"2100","currency":"USD","amount":"-300.00","ledger":"AP","documentDate":"2026-01-01","dueDate":"2026-02-01"}`
	run := e.start(t, loadBody([]string{loadRecord("5001", "100.00", ""), ap}, ""))
	if run.Status != "RECONCILED" || run.RecordsLoaded != 2 || run.RecordsQuarantined != 0 {
		t.Fatalf("run %+v", run.runDTO)
	}
	if n := e.scalar(t, `select count(*) from legacy_xref where run_id = $1 and source_id = '5001'
		and source_table in ('open_items.receivable', 'open_items.payable')`, run.RunID); n != 2 {
		t.Fatalf("xref rows for 5001 = %d, want one per side", n)
	}
	if n := e.scalar(t, `select count(*) from legacy_xref where source_table = 'open_items'`); n != 0 {
		t.Fatalf("%d rows under the bare open_items", n)
	}
	if n := e.scalar(t, `select count(*) from target_row where run_id = $1 and legacy_id = '5001'`, run.RunID); n != 2 {
		t.Fatalf("staged rows for 5001 = %d, want 2", n)
	}
	// Only the receivable goes to receivables; the confirmation lands on the receivable's xref row.
	if ld := e.load(t, run.RunID); ld.Created != 1 || ld.NotAvailable != 1 {
		t.Fatalf("load %+v", ld)
	}
	if n := e.scalar(t, `select count(*) from legacy_xref where run_id = $1 and source_table = 'open_items.receivable' and target_confirmed`, run.RunID); n != 1 {
		t.Fatalf("confirmed receivable xref rows = %d", n)
	}
	if n := e.scalar(t, `select count(*) from legacy_xref where run_id = $1 and source_table = 'open_items.payable' and not target_confirmed`, run.RunID); n != 1 {
		t.Fatalf("payable xref row must stay the factory's own")
	}
	again := e.start(t, loadBody([]string{loadRecord("5001", "100.00", ""), ap}, ""))
	if again.RecordsLoaded != 0 || again.AlreadyPresent != 2 || again.Status != "RECONCILED" || again.InputHash != run.InputHash {
		t.Fatalf("re-run %+v", again.runDTO)
	}
	// A payable twice on the payable side is still the duplicate.
	dup := e.start(t, loadBody(append(nRecords(200), ap, ap), ""))
	if dup.RecordsQuarantined != 1 || !strings.Contains(dup.Quarantined[0].Reason, "payable side") || dup.Quarantined[0].Ledger != "AP" {
		t.Fatalf("duplicate payable %+v", dup.Quarantined)
	}
	// The lookup names the side; the bare table name finds both.
	side := decode[struct{ Items []xrefDTO }](t, e.do(t, "GET", "/api/v1/migration/xref?system=AX2012&table=open_items.payable&id=5001", ""))
	if len(side.Items) != 1 || side.Items[0].Table != "open_items.payable" {
		t.Fatalf("payable lookup %+v", side.Items)
	}
	both := decode[struct{ Items []xrefDTO }](t, e.do(t, "GET", "/api/v1/migration/xref?system=AX2012&table=open_items&id=5001", ""))
	if len(both.Items) != 2 {
		t.Fatalf("bare lookup %+v", both.Items)
	}
}

// 0016 rekeys the rows held under the bare open_items by side from what the run staged, backfills the load
// status on the run, and leaves nothing under the old name.
func TestMigration0016_RekeysOpenItemsBySide(t *testing.T) {
	e := newLoadEnv(t, 3, nil)
	ap := `{"legacyId":"AP-9","party":"V001","account":"2100","currency":"USD","amount":"-300.00","ledger":"AP","documentDate":"2026-01-01","dueDate":"2026-02-01"}`
	run := e.start(t, loadBody([]string{loadRecord("AR-9", "100.00", ""), ap}, ""))
	e.load(t, run.RunID)
	ctx := context.Background()
	// Put the rows back the way they were stored before the sides were split. The trigger allows the move only
	// from the side tables to... nothing: it only allows the forward move, so the test goes around it.
	for _, sql := range []string{
		`alter table legacy_xref disable trigger legacy_xref_confirm_target`,
		`update legacy_xref set source_table = 'open_items' where run_id = '` + run.RunID + `'`,
		`alter table legacy_xref enable trigger legacy_xref_confirm_target`,
		`update pack_run set receivables_load = null where run_id = '` + run.RunID + `'`,
	} {
		if _, err := e.admin.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	data, err := fs.ReadFile(migrations.FS, "0016_open_items_sides_data.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err := pgx.BeginFunc(ctx, e.admin, func(tx pgx.Tx) error { _, err := tx.Exec(ctx, string(data)); return err }); err != nil {
		t.Fatal(err)
	}
	for id, table := range map[string]string{"AR-9": "open_items.receivable", "AP-9": "open_items.payable"} {
		if n := e.scalar(t, `select count(*) from legacy_xref where source_id = $1 and source_table = $2`, id, table); n != 1 {
			t.Fatalf("%s is not under %s", id, table)
		}
	}
	if n := e.scalar(t, `select count(*) from pack_run where run_id = $1 and receivables_load = 'RECONCILED'`, run.RunID); n != 1 {
		t.Fatalf("receivables_load not backfilled")
	}
	// 0016 puts the 0006 trigger back: no move of the source table is allowed afterwards, not even the
	// rekey's own, and nothing else but the target confirmation.
	if _, err := e.admin.Exec(ctx, `update legacy_xref set source_table = 'open_items.payable' where source_id = 'AR-9'`); err == nil {
		t.Fatal("moving a receivable to the payable side must be refused")
	}
	if _, err := e.admin.Exec(ctx, `update legacy_xref set amount = 1 where source_id = 'AR-9'`); err == nil {
		t.Fatal("an update of the amount must be refused")
	}
	for _, sql := range []string{
		`alter table legacy_xref disable trigger legacy_xref_confirm_target`,
		`update legacy_xref set source_table = 'open_items' where source_id = 'AR-9'`,
		`alter table legacy_xref enable trigger legacy_xref_confirm_target`,
	} {
		if _, err := e.admin.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.admin.Exec(ctx, `update legacy_xref set source_table = 'open_items.receivable' where source_id = 'AR-9'`); err == nil {
		t.Fatal("after 0016 the trigger must refuse the rekey move too: legacy_xref rows are write-once")
	}
	if err := pgx.BeginFunc(ctx, e.admin, func(tx pgx.Tx) error { _, err := tx.Exec(ctx, string(data)); return err }); err != nil {
		t.Fatal(err)
	}
	// A re-run of the same extract finds both rows and loads nothing.
	again := e.start(t, loadBody([]string{loadRecord("AR-9", "100.00", ""), ap}, ""))
	if again.RecordsLoaded != 0 || again.AlreadyPresent != 2 {
		t.Fatalf("re-run after the rekey %+v", again.runDTO)
	}
}

// 0016 does not need the row-level security bypass: a migration role that only owns the tables (as any
// role that ran 0015's ALTERs does) rekeys every tenant's rows, on an empty database and on a loaded one,
// and leaves FORCE row-level security on the tables. The role lives in a transaction that is rolled back.
func TestMigration0016_RunsAsTableOwnerWithoutBypass(t *testing.T) {
	e := newLoadEnv(t, 3, nil)
	ctx := context.Background()
	data, err := fs.ReadFile(migrations.FS, "0016_open_items_sides_data.sql")
	if err != nil {
		t.Fatal(err)
	}
	asOwner := func(t *testing.T, check func(tx pgx.Tx)) {
		t.Helper()
		tx, err := e.admin.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		var schema string
		if err := tx.QueryRow(ctx, `select current_schema()`).Scan(&schema); err != nil {
			t.Fatal(err)
		}
		role := "mf_owner_" + strings.ReplaceAll(schema, "t_", "")
		for _, sql := range []string{
			`create role ` + role + ` nologin nosuperuser nobypassrls`,
			`grant usage, create on schema ` + schema + ` to ` + role,
			`alter table legacy_xref owner to ` + role,
			`alter table target_row owner to ` + role,
			`alter table pack_run owner to ` + role,
			`alter table target_load owner to ` + role,
			`alter function legacy_xref_confirm_target() owner to ` + role,
			`set local role ` + role,
		} {
			if _, err := tx.Exec(ctx, sql); err != nil {
				t.Fatalf("%s: %v", sql, err)
			}
		}
		if _, err := tx.Exec(ctx, string(data)); err != nil {
			t.Fatalf("0016 as a table owner without bypass: %v", err)
		}
		if _, err := tx.Exec(ctx, `reset role`); err != nil {
			t.Fatal(err)
		}
		var unforced int
		if err := tx.QueryRow(ctx, `select count(*) from pg_class where relnamespace = $1::regnamespace
			and relname in ('legacy_xref', 'target_row', 'pack_run', 'target_load') and not (relrowsecurity and relforcerowsecurity)`, schema).Scan(&unforced); err != nil {
			t.Fatal(err)
		}
		if unforced != 0 {
			t.Fatalf("%d tables left without FORCE row-level security", unforced)
		}
		check(tx)
	}
	// An empty database: nothing to rekey and no bypass needed.
	asOwner(t, func(pgx.Tx) {})
	// A loaded one, rows of two tenants held under the bare open_items.
	ap := `{"legacyId":"AP-7","party":"V001","account":"2100","currency":"USD","amount":"-300.00","ledger":"AP","documentDate":"2026-01-01","dueDate":"2026-02-01"}`
	run := e.start(t, loadBody([]string{loadRecord("AR-7", "100.00", ""), ap}, ""))
	e.load(t, run.RunID)
	otherRun := insertPackRun(t, e.admin, tenantB, "open_items", "USMF", "RECONCILED", time.Now())
	for _, sql := range []string{
		`alter table legacy_xref disable trigger legacy_xref_confirm_target`,
		`update legacy_xref set source_table = 'open_items' where run_id = '` + run.RunID + `'`,
		`insert into legacy_xref (tenant_id, xref_id, source_system, source_company, source_table, source_id, passage_entity, passage_id, run_id, amount, currency)
			select '` + tenantB + `', gen_random_uuid(), source_system, source_company, 'open_items', source_id, passage_entity, gen_random_uuid(), '` + otherRun + `', amount, currency
			from legacy_xref where run_id = '` + run.RunID + `' and source_id = 'AR-7'`,
		`alter table legacy_xref enable trigger legacy_xref_confirm_target`,
		`update pack_run set receivables_load = null where run_id = '` + run.RunID + `'`,
	} {
		if _, err := e.admin.Exec(ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	asOwner(t, func(tx pgx.Tx) {
		var bare, recv, pay, loads int
		if err := tx.QueryRow(ctx, `select count(*) filter (where source_table = 'open_items'),
			count(*) filter (where source_table = 'open_items.receivable' and source_id = 'AR-7'),
			count(*) filter (where source_table = 'open_items.payable' and source_id = 'AP-7') from legacy_xref`).Scan(&bare, &recv, &pay); err != nil {
			t.Fatal(err)
		}
		if err := tx.QueryRow(ctx, `select count(*) from pack_run where run_id = $1 and receivables_load = 'RECONCILED'`, run.RunID).Scan(&loads); err != nil {
			t.Fatal(err)
		}
		if bare != 0 || recv != 2 || pay != 1 || loads != 1 {
			t.Fatalf("after 0016 as owner: bare %d, receivables %d (want both tenants), payables %d, loads backfilled %d", bare, recv, pay, loads)
		}
	})
}

// S11-b: the go/no-go needs an open_items run's receivables load to have run; a waiver does not excuse it.
func TestGoNoGo_ReceivablesLoadThatDidNotRunKeepsPacksReconciledOpen(t *testing.T) {
	h, admin := dbServer(t)
	plan, t0 := readyPlan(t, h, admin)
	// Replace the plan's open_items run by one with a staged load.
	later := insertPackRun(t, admin, tenantA, "open_items", "USMF", "RECONCILED", t0.Add(3*time.Hour))
	call := decisionCall{t: t, h: h, base: "/api/v1/cutover/plans/" + plan.PlanID + "/go-no-go"}
	item := func() (string, string) {
		for _, it := range call.get(t).Items {
			if it.Code == "packs_reconciled" {
				return it.Status, it.Detail
			}
		}
		t.Fatal("no packs_reconciled item")
		return "", ""
	}
	// The gl_balances pack of readyPlan is staged (open): only the open_items part is under test.
	if _, d := item(); strings.Contains(d, "receivables load") {
		t.Fatalf("no load requested: %s", d)
	}
	for _, status := range []string{"STAGED", "LOADING", "PARTIAL", "FAILED", "UNRECONCILED", "MISMATCH"} {
		if _, err := admin.Exec(context.Background(), `update pack_run set receivables_load = $2 where run_id = $1`, later, status); err != nil {
			t.Fatal(err)
		}
		if st, d := item(); st != "OPEN" || !strings.Contains(d, "its receivables load is "+status) {
			t.Fatalf("load %s: %s %s", status, st, d)
		}
	}
	// Payables staged and waived does not excuse the load either.
	if _, err := admin.Exec(context.Background(), `update pack_run set payables_staged = 2 where run_id = $1`, later); err != nil {
		t.Fatal(err)
	}
	w := `{"decision":"GO","attestations":` + attestations() + `,"waivers":[{"pack":"open_items","reason":"AP opened by hand in payables"}]}`
	if r := call.post("alice", w, "k-waive-failed-load"); r.Code != http.StatusUnprocessableEntity || !strings.Contains(r.Body.String(), "packs_reconciled") {
		t.Fatalf("waived GO with a failed load: %d %s", r.Code, r.Body)
	}
	if st, d := item(); st != "OPEN" || !strings.Contains(d, "no waiver excuses") {
		t.Fatalf("waived: %s %s", st, d)
	}
	if _, err := admin.Exec(context.Background(), `update pack_run set receivables_load = 'RECONCILED', payables_staged = 0 where run_id = $1`, later); err != nil {
		t.Fatal(err)
	}
	if _, d := item(); strings.Contains(d, "receivables load") {
		t.Fatalf("reconciled load: %s", d)
	}
}

// 9.49.15: the runbook, the rollback plan, the go/no-go checklist, the decisions and the reconciliation of a
// plan are served as CSV to a cutover manager, from the factory's own data.
func TestCutoverExports_ServeCSVFiles(t *testing.T) {
	h, admin := dbServer(t)
	plan, _ := readyPlan(t, h, admin)
	get := func(file, duties string) *httptest.ResponseRecorder {
		return do(t, h, request{method: "GET", path: "/api/v1/cutover/plans/" + plan.PlanID + "/export/" + file, tenant: tenantA, duties: duties})
	}
	read := func(file string) [][]string {
		r := get(file, "CutoverManager,MigrationView")
		if r.Code != http.StatusOK || !strings.HasPrefix(r.Header().Get("Content-Type"), "text/csv") ||
			!strings.Contains(r.Header().Get("Content-Disposition"), "cutover-"+plan.PlanID+"-"+file) {
			t.Fatalf("%s: %d %v %s", file, r.Code, r.Header(), r.Body)
		}
		rows, err := csv.NewReader(r.Body).ReadAll()
		if err != nil {
			t.Fatal(err)
		}
		return rows
	}
	if rows := read("runbook.csv"); len(rows) != len(plan.Steps)+1 || rows[0][3] != "step" || rows[1][0] != "1" {
		t.Fatalf("runbook rows %d for %d steps", len(rows), len(plan.Steps))
	}
	if rows := read("rollback-plan.csv"); len(rows) != 7 {
		t.Fatalf("rollback plan rows %d", len(rows))
	}
	checklist := read("go-no-go.csv")
	if len(checklist) != 15 || checklist[1][0] != "packs_reconciled" || checklist[2][0] != "parallel_run_signed_off" || checklist[2][3] != "MET" {
		t.Fatalf("checklist rows %v", checklist)
	}
	if rows := read("decisions.csv"); len(rows) != 1 {
		t.Fatalf("no decision yet, got %v", rows)
	}
	recon := read("reconciliation.csv")
	seen := map[string]bool{}
	for _, r := range recon[1:] {
		seen[r[0]] = true
		if r[11] != "NO_RUN" && r[11] != "" {
			t.Fatalf("hand-inserted runs carry no totals: %v", r)
		}
	}
	if !seen["open_items"] || !seen["gl_balances"] {
		t.Fatalf("reconciliation lists %v", seen)
	}
	// A recorded NO_GO appears in the decision history with its reason, formula-safe.
	c := decisionCall{t: t, h: h, base: "/api/v1/cutover/plans/" + plan.PlanID + "/go-no-go"}
	if r := c.post("alice", `{"decision":"NO_GO","reason":"=HYPERLINK(\"x\") bank smoke test failed"}`, "k-nogo"); r.Code != http.StatusCreated {
		t.Fatalf("no-go: %d %s", r.Code, r.Body)
	}
	if rows := read("decisions.csv"); len(rows) != 2 || rows[1][1] != "NO_GO" || rows[1][2] != "alice" || !strings.HasPrefix(rows[1][5], "'=HYPERLINK") {
		t.Fatalf("decision rows %v", rows)
	}
	// The duty, the file name and the plan are checked; another tenant's plan is not found.
	if r := get("runbook.csv", "MigrationView"); r.Code != http.StatusForbidden {
		t.Fatalf("without the cutover duty: %d", r.Code)
	}
	// The runbook files need the cutover duty only; the control totals of the reconciliation need MigrationView too.
	if r := get("runbook.csv", "CutoverManager"); r.Code != http.StatusOK {
		t.Fatalf("runbook as a cutover manager: %d", r.Code)
	}
	if r := get("reconciliation.csv", "CutoverManager"); r.Code != http.StatusForbidden || !strings.Contains(r.Body.String(), "MigrationView") {
		t.Fatalf("reconciliation without MigrationView: %d %s", r.Code, r.Body)
	}
	if r := get("secrets.csv", "CutoverManager"); r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown file: %d", r.Code)
	}
	if r := do(t, h, request{method: "GET", path: "/api/v1/cutover/plans/" + plan.PlanID + "/export/runbook.csv", tenant: tenantB, duties: "CutoverManager"}); r.Code != http.StatusNotFound {
		t.Fatalf("other tenant: %d", r.Code)
	}
}
