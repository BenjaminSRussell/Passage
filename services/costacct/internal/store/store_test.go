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
	if _, _, err := st.RunAllocation(ctx, "r-2", "h-r2", req); !hasCode(err, CodePeriodAllocated) {
		t.Fatalf("second run: err = %v, want %s", err, CodePeriodAllocated)
	}
}

func hasCode(err error, code string) bool {
	var p *httpx.Problem
	return errors.As(err, &p) && p.Code == code
}
