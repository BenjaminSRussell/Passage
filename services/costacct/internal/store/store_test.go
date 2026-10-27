package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/costacct/internal/domain"
	ccevents "github.com/BenjaminSRussell/passage/services/costacct/internal/events"
	"github.com/BenjaminSRussell/passage/services/costacct/migrations"
)

func testCtx(tenantID string) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: tenantID, Duties: map[string]struct{}{}})
}

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func idOf(t *testing.T, raw []byte) string {
	t.Helper()
	var v struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &v); err != nil || v.ID == "" {
		t.Fatalf("no id in %s (%v)", raw, err)
	}
	return v.ID
}

// sumEntries sums cost entries matching where inside a tenant transaction.
func sumEntries(t *testing.T, ctx context.Context, pool *pgxpool.Pool, where string, args ...any) decimal.Decimal {
	t.Helper()
	var out decimal.Decimal
	err := dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
		var s string
		if err := tx.QueryRow(ctx, "select coalesce(sum(amount), 0)::text from cost_entry where "+where, args...).Scan(&s); err != nil {
			return err
		}
		out = dec(s)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestRunAllocation_ReconcilesToJournal mirrors journal lines, runs a reciprocal
// step and checks that total cost before and after the step is identical.
func TestRunAllocation_ReconcilesToJournal(t *testing.T) {
	pool := testkit.DB(t)
	ctx := testCtx(testkit.TenantID(2))
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	st := New(pool)
	le := ids.New()
	objects := make([]string, 3)
	for i, code := range []string{"CC-A", "CC-B", "CC-C"} {
		status, raw, err := st.CreateCostObject(ctx, "obj-"+code, "h-"+code, CostObject{LegalEntityID: le, Kind: "CostCenter", Code: code, Name: code})
		if err != nil || status != http.StatusCreated {
			t.Fatalf("create %s: %v", code, err)
		}
		objects[i] = idOf(t, raw)
	}
	if _, _, err := st.CreateCostElement(ctx, "el-pr", "h-pr", CostElement{LegalEntityID: le, Kind: "Primary", Code: "PR-600", Name: "Payroll",
		MainAccountFrom: "600000", MainAccountTo: "609999"}); err != nil {
		t.Fatal(err)
	}
	_, secRaw, err := st.CreateCostElement(ctx, "el-se", "h-se", CostElement{LegalEntityID: le, Kind: "Secondary", Code: "SE-ALLOC", Name: "Allocated"})
	if err != nil {
		t.Fatal(err)
	}
	secID := idOf(t, secRaw)

	lines := []MirrorLine{
		{JournalLineID: ids.New(), MainAccount: "600100", CostObjectID: objects[0], Period: "2026-09", Currency: "USD", Amount: dec("100.00")},
		{JournalLineID: ids.New(), MainAccount: "600200", CostObjectID: objects[0], Period: "2026-09", Currency: "USD", Amount: dec("50.25")},
		{JournalLineID: ids.New(), MainAccount: "600300", CostObjectID: objects[1], Period: "2026-09", Currency: "USD", Amount: dec("200.00")},
	}
	if _, _, err := st.MirrorJournalLines(ctx, "m-1", "h-m1", MirrorRequest{LegalEntityID: le, Lines: lines}); err != nil {
		t.Fatal(err)
	}
	// Replaying the same lines under a new key mirrors nothing new.
	_, raw, err := st.MirrorJournalLines(ctx, "m-2", "h-m2", MirrorRequest{LegalEntityID: le, Lines: lines})
	if err != nil {
		t.Fatal(err)
	}
	var counts struct{ Mirrored, Skipped int }
	if err := json.Unmarshal(raw, &counts); err != nil || counts.Mirrored != 0 || counts.Skipped != 3 {
		t.Fatalf("replay counts %+v (%v)", counts, err)
	}
	if _, _, err := st.MirrorJournalLines(ctx, "m-3", "h-m3", MirrorRequest{LegalEntityID: le, Lines: []MirrorLine{
		{JournalLineID: ids.New(), MainAccount: "700000", CostObjectID: objects[0], Period: "2026-09", Currency: "USD", Amount: dec("1")}}}); !hasCode(err, CodeNoPrimaryElement) {
		t.Fatalf("unmapped account: err = %v", err)
	}

	// AC-38.6: the month's primary entries reconcile to the journal with 0.00 difference.
	recon, err := st.Reconcile(ctx, ReconcileRequest{LegalEntityID: le, Period: "2026-09", Currency: "USD", Journal: []domain.JournalBalance{
		{MainAccount: "600100", Amount: dec("100.00")}, {MainAccount: "600200", Amount: dec("50.25")}, {MainAccount: "600300", Amount: dec("200.00")},
		{MainAccount: "400000", Amount: dec("-500.00")}}})
	if err != nil || !recon.Reconciled || len(recon.Lines) != 1 || !recon.Lines[0].Difference.IsZero() || len(recon.UnmappedAccounts) != 1 {
		t.Fatalf("reconciliation %+v (%v)", recon, err)
	}
	recon, err = st.Reconcile(ctx, ReconcileRequest{LegalEntityID: le, Period: "2026-09", Currency: "USD", Journal: []domain.JournalBalance{
		{MainAccount: "600100", Amount: dec("100.00")}}})
	if err != nil || recon.Reconciled || !recon.Lines[0].Difference.Equal(dec("-250.25")) {
		t.Fatalf("an unmirrored difference must show: %+v (%v)", recon, err)
	}
	// A cost object of another legal entity cannot receive this entity's journal lines.
	other := ids.New()
	_, otherRaw, err := st.CreateCostObject(ctx, "obj-other", "h-other", CostObject{LegalEntityID: other, Kind: "CostCenter", Code: "CC-X", Name: "Other"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.MirrorJournalLines(ctx, "m-x", "h-x", MirrorRequest{LegalEntityID: le, Lines: []MirrorLine{
		{JournalLineID: ids.New(), MainAccount: "600100", CostObjectID: idOf(t, otherRaw), Period: "2026-09", Currency: "USD", Amount: dec("1")}}}); !hasCode(err, CodeObjectNotFound) {
		t.Fatalf("foreign cost object: err = %v", err)
	}
	if _, _, err := st.CreateCostObject(ctx, "obj-dup", "h-dup", CostObject{LegalEntityID: le, Kind: "CostCenter", Code: "CC-A", Name: "Dup"}); !hasCode(err, CodeDuplicateCode) {
		t.Fatalf("duplicate code: err = %v", err)
	}
	if objs, err := st.ListCostObjects(ctx, le, "CostCenter"); err != nil || len(objs) != 3 {
		t.Fatalf("cost objects %d (%v)", len(objs), err)
	}

	journal := sumEntries(t, ctx, pool, "origin = 'Journal' and period_key = '2026-09'")
	if !journal.Equal(dec("350.25")) {
		t.Fatalf("journal total %s, want 350.25", journal)
	}

	basis := [][]decimal.Decimal{{dec("0"), dec("1"), dec("0")}, {dec("1"), dec("0"), dec("1")}, {dec("0"), dec("0"), dec("0")}}
	req := RunRequest{LegalEntityID: le, Period: "2026-09", Currency: "USD", SecondaryElementID: secID, CostObjectIDs: objects, Basis: basis}
	// A EUR line on the same objects must not enter a USD run's cost base.
	if _, _, err := st.MirrorJournalLines(ctx, "m-eur", "h-eur", MirrorRequest{LegalEntityID: le, Lines: []MirrorLine{
		{JournalLineID: ids.New(), MainAccount: "600400", CostObjectID: objects[0], Period: "2026-09", Currency: "EUR", Amount: dec("999.00")}}}); err != nil {
		t.Fatal(err)
	}
	_, runRaw, err := st.RunAllocation(ctx, "r-1", "h-r1", req)
	if err != nil {
		t.Fatal(err)
	}
	var run RunResponse
	if err := json.Unmarshal(runRaw, &run); err != nil {
		t.Fatal(err)
	}
	// A and B bring 150.25 + 200.00 in; the sink C takes all of it out.
	if !run.Result.TotalIn.Equal(dec("350.25")) || !run.Result.TotalOut.Equal(dec("350.25")) {
		t.Fatalf("run totals in=%s out=%s, want 350.25", run.Result.TotalIn, run.Result.TotalOut)
	}
	for i, want := range []string{"0", "0", "350.25"} {
		got := sumEntries(t, ctx, pool, "cost_object_id = $1 and currency = 'USD' and period_key = '2026-09'", objects[i])
		if !got.Equal(dec(want)) {
			t.Errorf("object %d holds %s after the run, want %s", i, got, want)
		}
	}
	stored, err := st.GetRun(ctx, run.RunID)
	if err != nil || !stored.TotalOut.Equal(dec("350.25")) || len(stored.Postings) != 3 || len(stored.CostObjectIDs) != 3 {
		t.Fatalf("stored run %+v (%v)", stored, err)
	}
	if runs, err := st.ListRuns(ctx, le); err != nil || len(runs) != 1 {
		t.Fatalf("runs %d (%v)", len(runs), err)
	}
	allocated := sumEntries(t, ctx, pool, "origin = 'Allocation' and period_key = '2026-09'")
	if !allocated.IsZero() {
		t.Fatalf("allocation postings sum to %s, want 0", allocated)
	}
	after := sumEntries(t, ctx, pool, "period_key = '2026-09' and currency = 'USD'")
	if !after.Equal(journal) {
		t.Fatalf("cost after allocation %s, journal %s", after, journal)
	}
	if len(run.Warnings) != 0 || len(stored.Warnings) != 0 || stored.Status != "Completed" {
		t.Fatalf("first run warnings %+v / %+v status %s, want none and Completed", run.Warnings, stored.Warnings, stored.Status)
	}

	// BR-38.7 / CO-0615: journal cost posted after the run flags it for a rerun.
	if _, _, err := st.MirrorJournalLines(ctx, "m-late", "h-late", MirrorRequest{LegalEntityID: le, Lines: []MirrorLine{
		{JournalLineID: ids.New(), MainAccount: "600100", CostObjectID: objects[1], Period: "2026-09", Currency: "USD", Amount: dec("10.00")},
		{JournalLineID: ids.New(), MainAccount: "600100", CostObjectID: objects[2], Period: "2026-09", Currency: "USD", Amount: dec("1.00")}}}); err != nil {
		t.Fatal(err)
	}
	stale, err := st.GetRun(ctx, run.RunID)
	if err != nil || !hasWarning(stale.Warnings, "CO-0615") {
		t.Fatalf("stale run warnings %+v (%v), want CO-0615", stale.Warnings, err)
	}

	// A rerun reverses the first run and posts a new one from current journal cost.
	_, rerunRaw, err := st.RunAllocation(ctx, "r-2", "h-r2", req)
	if err != nil {
		t.Fatalf("rerun: %v", err)
	}
	var rerun RunResponse
	if err := json.Unmarshal(rerunRaw, &rerun); err != nil || rerun.ReversedRunID != run.RunID || rerun.RunID == run.RunID {
		t.Fatalf("rerun %+v (%v), want reversedRunId %s", rerun, err, run.RunID)
	}
	// A and B now bring 150.25 + 210.00; C keeps its own 1.00 (zero basis, CO-0613, BR-38.8).
	if !rerun.Result.TotalIn.Equal(dec("360.25")) || !hasWarning(rerun.Warnings, "CO-0613") || hasWarning(rerun.Warnings, "CO-0615") {
		t.Fatalf("rerun totals %s warnings %+v", rerun.Result.TotalIn, rerun.Warnings)
	}
	for i, want := range []string{"0", "0", "361.25"} {
		got := sumEntries(t, ctx, pool, "cost_object_id = $1 and currency = 'USD' and period_key = '2026-09'", objects[i])
		if !got.Equal(dec(want)) {
			t.Errorf("object %d holds %s after the rerun, want %s", i, got, want)
		}
	}
	// Each run nets to zero on its own, and the ledger still reconciles to the journal.
	for _, id := range []string{run.RunID, rerun.RunID} {
		if got := sumEntries(t, ctx, pool, "allocation_run_id = $1", id); !got.IsZero() {
			t.Errorf("run %s entries sum to %s, want 0", id, got)
		}
	}
	if got := sumEntries(t, ctx, pool, "allocation_run_id = $1 and cost_object_id = $2", run.RunID, objects[2]); !got.IsZero() {
		t.Errorf("reversed run still moves %s onto C", got)
	}
	journal = sumEntries(t, ctx, pool, "origin = 'Journal' and period_key = '2026-09' and currency = 'USD'")
	if after := sumEntries(t, ctx, pool, "period_key = '2026-09' and currency = 'USD'"); !after.Equal(journal) || !journal.Equal(dec("361.25")) {
		t.Fatalf("cost after rerun %s, journal %s", after, journal)
	}
	old, err := st.GetRun(ctx, run.RunID)
	if err != nil || old.Status != "Reversed" || old.ReversedByRunID != rerun.RunID || len(old.Warnings) != 0 {
		t.Fatalf("reversed run %+v (%v)", old, err)
	}
	current, err := st.GetRun(ctx, rerun.RunID)
	if err != nil || current.Status != "Completed" || current.ReversesRunID != run.RunID || !hasWarning(current.Warnings, "CO-0613") || hasWarning(current.Warnings, "CO-0615") {
		t.Fatalf("current run %+v (%v)", current, err)
	}
	if runs, err := st.ListRuns(ctx, le); err != nil || len(runs) != 2 || runs[0].ID != rerun.RunID {
		t.Fatalf("runs after rerun %+v (%v)", runs, err)
	}
	var events int
	if err := pool.QueryRow(ctx, `select count(*) from outbox where type = $1 and payload->'data'->>'reversed_run_id' = $2`,
		ccevents.AllocationCompleted, run.RunID).Scan(&events); err != nil || events != 1 {
		t.Fatalf("allocation.completed with reversed_run_id: %d (%v)", events, err)
	}
	// Replaying the rerun's key returns its answer and reverses nothing again.
	if _, again, err := st.RunAllocation(ctx, "r-2", "h-r2", req); err != nil || idOfRun(t, again) != rerun.RunID {
		t.Fatalf("replay: %s (%v)", again, err)
	}
	if got := sumEntries(t, ctx, pool, "allocation_run_id = $1", run.RunID); !got.IsZero() {
		t.Fatalf("replay moved the reversed run: %s", got)
	}
}

// Two concurrent reruns of one period: one reverses the completed run, the
// other is refused (CST-0002); exactly one Completed run remains.
func TestRunAllocation_ConcurrentRerun(t *testing.T) {
	pool := testkit.DB(t)
	ctx := testCtx(testkit.TenantID(4))
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	st := New(pool)
	le := ids.New()
	objects := make([]string, 2)
	for i, code := range []string{"CC-S", "CC-R"} {
		_, raw, err := st.CreateCostObject(ctx, "obj-"+code, "h", CostObject{LegalEntityID: le, Kind: "CostCenter", Code: code, Name: code})
		if err != nil {
			t.Fatal(err)
		}
		objects[i] = idOf(t, raw)
	}
	if _, _, err := st.CreateCostElement(ctx, "el-pr", "h", CostElement{LegalEntityID: le, Kind: "Primary", Code: "PR-600", Name: "Payroll",
		MainAccountFrom: "600000", MainAccountTo: "609999"}); err != nil {
		t.Fatal(err)
	}
	_, secRaw, err := st.CreateCostElement(ctx, "el-se", "h", CostElement{LegalEntityID: le, Kind: "Secondary", Code: "SE-ALLOC", Name: "Allocated"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.MirrorJournalLines(ctx, "m", "h", MirrorRequest{LegalEntityID: le, Lines: []MirrorLine{
		{JournalLineID: ids.New(), MainAccount: "600100", CostObjectID: objects[0], Period: "2026-09", Currency: "USD", Amount: dec("90.00")}}}); err != nil {
		t.Fatal(err)
	}
	req := RunRequest{LegalEntityID: le, Period: "2026-09", Currency: "USD", SecondaryElementID: idOf(t, secRaw), CostObjectIDs: objects,
		Basis: [][]decimal.Decimal{{dec("0"), dec("1")}, {dec("0"), dec("0")}}}
	if _, _, err := st.RunAllocation(ctx, "first", "h", req); err != nil {
		t.Fatal(err)
	}
	errs := make(chan error, 2)
	for _, k := range []string{"a", "b"} {
		go func(k string) {
			_, _, err := st.RunAllocation(ctx, "rerun-"+k, "h", req)
			errs <- err
		}(k)
	}
	ok, refused := 0, 0
	for range 2 {
		switch err := <-errs; {
		case err == nil:
			ok++
		case hasCode(err, CodePeriodAllocated):
			refused++
		default:
			t.Fatalf("rerun: %v", err)
		}
	}
	runs, err := st.ListRuns(ctx, le)
	if err != nil {
		t.Fatal(err)
	}
	completed := 0
	for _, r := range runs {
		if r.Status == "Completed" {
			completed++
		}
	}
	if ok < 1 || ok+refused != 2 || completed != 1 || len(runs) != 1+ok {
		t.Fatalf("ok %d refused %d completed %d runs %d", ok, refused, completed, len(runs))
	}
	if got := sumEntries(t, ctx, pool, "cost_object_id = $1", objects[1]); !got.Equal(dec("90.00")) {
		t.Fatalf("receiver holds %s, want 90.00 once", got)
	}
}

// FR-38.16: a kind-specific rule beats the element default, the element
// default applies before the rule's date, and entries mirrored before a rule
// existed keep their class.
func TestBehaviorRule_ClassifiesMirror(t *testing.T) {
	pool := testkit.DB(t)
	ctx := testCtx(testkit.TenantID(3))
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	st := New(pool)
	le := ids.New()
	_, objRaw, err := st.CreateCostObject(ctx, "obj-b", "h-b", CostObject{LegalEntityID: le, Kind: "CostCenter", Code: "CC-B", Name: "B"})
	if err != nil {
		t.Fatal(err)
	}
	obj := idOf(t, objRaw)
	_, prodRaw, err := st.CreateCostObject(ctx, "obj-p", "h-p", CostObject{LegalEntityID: le, Kind: "Product", Code: "PR-1", Name: "P"})
	if err != nil {
		t.Fatal(err)
	}
	prod := idOf(t, prodRaw)
	_, elRaw, err := st.CreateCostElement(ctx, "el-b", "h-eb", CostElement{LegalEntityID: le, Kind: "Primary", Code: "PR-700", Name: "Rent",
		MainAccountFrom: "700000", MainAccountTo: "709999", DefaultBehavior: "Variable"})
	if err != nil {
		t.Fatal(err)
	}
	el := idOf(t, elRaw)
	line := func(object, period string) MirrorLine {
		return MirrorLine{JournalLineID: ids.New(), MainAccount: "700100", CostObjectID: object, Period: period, Currency: "USD", Amount: dec("10")}
	}
	// Before any rule: the element default.
	if _, _, err := st.MirrorJournalLines(ctx, "bh-0", "h", MirrorRequest{LegalEntityID: le, Lines: []MirrorLine{line(obj, "2026-08")}}); err != nil {
		t.Fatal(err)
	}
	if status, _, err := st.CreateBehaviorRule(ctx, "rule-1", "h-rule1", BehaviorRule{LegalEntityID: le, ElementID: el,
		ObjectKind: "CostCenter", Behavior: "Fixed", EffectiveFrom: "2026-09-01"}); err != nil || status != http.StatusCreated {
		t.Fatalf("rule: status %d err %v", status, err)
	}
	if _, _, err := st.CreateBehaviorRule(ctx, "rule-2", "h-rule2", BehaviorRule{LegalEntityID: le, ElementID: el,
		ObjectKind: "CostCenter", Behavior: "Variable", EffectiveFrom: "2026-09-01"}); !hasCode(err, CodeBehaviorRuleExists) {
		t.Fatalf("duplicate rule: err = %v, want %s", err, CodeBehaviorRuleExists)
	}
	if _, _, err := st.CreateBehaviorRule(ctx, "rule-3", "h-rule3", BehaviorRule{LegalEntityID: ids.New(), ElementID: el,
		Behavior: "Fixed", EffectiveFrom: "2026-09-01"}); !hasCode(err, CodeSecondaryNotFound) {
		t.Fatalf("element of another legal entity: err = %v", err)
	}
	if _, _, err := st.MirrorJournalLines(ctx, "bh-1", "h", MirrorRequest{LegalEntityID: le, Lines: []MirrorLine{
		line(obj, "2026-09"), line(obj, "2026-07"), line(prod, "2026-09")}}); err != nil {
		t.Fatal(err)
	}
	classes := map[string]string{}
	err = dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select cost_object_id::text || '|' || period_key, behavior from cost_entry where origin = 'Journal'`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var k, b string
			if err := rows.Scan(&k, &b); err != nil {
				return err
			}
			classes[k] = b
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		obj + "|2026-08":  "Variable", // mirrored before the rule existed
		obj + "|2026-09":  "Fixed",    // the CostCenter rule from September
		obj + "|2026-07":  "Variable", // before the rule's date: element default
		prod + "|2026-09": "Variable", // the rule names another kind
	}
	for k, w := range want {
		if classes[k] != w {
			t.Errorf("%s: behavior %q, want %q", k, classes[k], w)
		}
	}
	rules, err := st.ListBehaviorRules(ctx, le, el)
	if err != nil || len(rules) != 1 || rules[0].ObjectKind != "CostCenter" || rules[0].EffectiveFrom != "2026-09-01" {
		t.Fatalf("rules %+v (%v)", rules, err)
	}
	elems, err := st.ListCostElements(ctx, le)
	if err != nil || len(elems) != 1 || elems[0].DefaultBehavior != "Variable" {
		t.Fatalf("elements %+v (%v)", elems, err)
	}
}

func hasWarning(ws []Warning, code string) bool {
	for _, w := range ws {
		if w.Code == code {
			return true
		}
	}
	return false
}

func idOfRun(t *testing.T, raw []byte) string {
	t.Helper()
	var v RunResponse
	if err := json.Unmarshal(raw, &v); err != nil || v.RunID == "" {
		t.Fatalf("no runId in %s (%v)", raw, err)
	}
	return v.RunID
}

func hasCode(err error, code string) bool {
	var p *httpx.Problem
	return errors.As(err, &p) && p.Code == code
}
