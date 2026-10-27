package store

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/costacct/internal/domain"
	"github.com/BenjaminSRussell/passage/services/costacct/migrations"
)

type ledgerFixture struct {
	ctx       context.Context
	st        *Postgres
	le        string
	primary   string
	secondary string
}

func newLedger(t *testing.T, tenant int) ledgerFixture {
	t.Helper()
	pool := testkit.DB(t)
	ctx := testCtx(testkit.TenantID(tenant))
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	f := ledgerFixture{ctx: ctx, st: New(pool), le: ids.New()}
	_, raw, err := f.st.CreateCostElement(ctx, ids.New(), "h", CostElement{LegalEntityID: f.le, Kind: "Primary", Code: "FREIGHT", Name: "Freight",
		MainAccountFrom: "610000", MainAccountTo: "619999"})
	if err != nil {
		t.Fatal(err)
	}
	f.primary = idOf(t, raw)
	_, raw, err = f.st.CreateCostElement(ctx, ids.New(), "h", CostElement{LegalEntityID: f.le, Kind: "Secondary", Code: "SETTLE", Name: "Settlement"})
	if err != nil {
		t.Fatal(err)
	}
	f.secondary = idOf(t, raw)
	return f
}

func (f ledgerFixture) object(t *testing.T, kind, code string) string {
	t.Helper()
	status, raw, err := f.st.CreateCostObject(f.ctx, ids.New(), "h", CostObject{LegalEntityID: f.le, Kind: kind, Code: code, Name: code})
	if err != nil || status != http.StatusCreated {
		t.Fatalf("create %s: %v", code, err)
	}
	return idOf(t, raw)
}

func (f ledgerFixture) book(t *testing.T, objectID, amount string) {
	t.Helper()
	if _, _, err := f.st.MirrorJournalLines(f.ctx, ids.New(), "h", MirrorRequest{LegalEntityID: f.le, Lines: []MirrorLine{
		{JournalLineID: ids.New(), MainAccount: "610100", CostObjectID: objectID, Period: "2026-09", Currency: "USD", Amount: dec(amount)}}}); err != nil {
		t.Fatal(err)
	}
}

// AC-38.11 through the store: freight of 12,000.00 booked on the entity object is
// distributed 7,200 / 3,600 / 1,200 on the freight element, the source is
// emptied, the run nets to zero and the GL reconciliation is unchanged.
func TestRunTopDown_WorkedExampleNetsToZero(t *testing.T) {
	f := newLedger(t, 3)
	source := f.object(t, "CostCenter", "ENTITY")
	segs := []string{f.object(t, "Product", "NORTH"), f.object(t, "Product", "SOUTH"), f.object(t, "Product", "WEST")}
	idle := f.object(t, "Product", "IDLE")
	f.book(t, source, "12000.00")
	req := TopDownRequest{LegalEntityID: f.le, Period: "2026-09", ReferencePeriod: "2026-08", Currency: "USD", SourceCostObjectID: source,
		CostElementID: f.primary, Amount: dec("12000.00"), Segments: []TopDownSegment{
			{CostObjectID: segs[0], Basis: dec("300000")}, {CostObjectID: segs[1], Basis: dec("150000")},
			{CostObjectID: segs[2], Basis: dec("50000")}, {CostObjectID: idle, Basis: dec("0")}}}
	_, raw, err := f.st.RunTopDown(f.ctx, ids.New(), "h", req)
	if err != nil {
		t.Fatal(err)
	}
	var run TopDownRun
	if err := json.Unmarshal(raw, &run); err != nil {
		t.Fatal(err)
	}
	if len(run.Excluded) != 1 || run.Excluded[0] != idle {
		t.Fatalf("excluded = %v, want the zero-basis segment", run.Excluded)
	}
	for i, want := range []string{"7200", "3600", "1200"} {
		if got := sumEntries(t, f.ctx, f.st.pool, "cost_object_id = $1 and top_down_run_id = $2", segs[i], run.ID); !got.Equal(dec(want)) {
			t.Errorf("segment %d received %s, want %s", i, got, want)
		}
	}
	if got := sumEntries(t, f.ctx, f.st.pool, "top_down_run_id = $1", run.ID); !got.IsZero() {
		t.Fatalf("run entries net to %s, want 0", got)
	}
	if got := sumEntries(t, f.ctx, f.st.pool, "cost_object_id = $1", source); !got.IsZero() {
		t.Fatalf("source keeps %s, want 0", got)
	}
	recon, err := f.st.Reconcile(f.ctx, ReconcileRequest{LegalEntityID: f.le, Period: "2026-09", Currency: "USD",
		Journal: []domain.JournalBalance{{MainAccount: "610100", Amount: dec("12000.00")}}})
	if err != nil || !recon.Reconciled {
		t.Fatalf("GL reconciliation after top-down: %+v (%v)", recon, err)
	}
	got, err := f.st.GetTopDownRun(f.ctx, run.ID)
	if err != nil || got.ReferencePeriod != "2026-08" || len(got.Shares) != 3 {
		t.Fatalf("stored run %+v (%v)", got, err)
	}
	if runs, err := f.st.ListTopDownRuns(f.ctx, f.le); err != nil || len(runs) != 1 {
		t.Fatalf("list = %d (%v)", len(runs), err)
	}
	// A second run for the same source, element and period is refused.
	if _, _, err := f.st.RunTopDown(f.ctx, ids.New(), "h", req); !hasCode(err, CodePeriodAllocated) {
		t.Fatalf("second run: %v, want %s", err, CodePeriodAllocated)
	}
}

// FR-38.19: only what is booked on the source can be distributed.
func TestRunTopDown_RefusesMoreThanBooked(t *testing.T) {
	f := newLedger(t, 4)
	source := f.object(t, "CostCenter", "ENTITY")
	seg := f.object(t, "Product", "NORTH")
	f.book(t, source, "100.00")
	_, _, err := f.st.RunTopDown(f.ctx, ids.New(), "h", TopDownRequest{LegalEntityID: f.le, Period: "2026-09", Currency: "USD",
		SourceCostObjectID: source, CostElementID: f.primary, Amount: dec("100.01"), Segments: []TopDownSegment{{CostObjectID: seg, Basis: dec("1")}}})
	if !hasCode(err, CodeExceedsBooked) {
		t.Fatalf("err = %v, want %s", err, CodeExceedsBooked)
	}
	if got := sumEntries(t, f.ctx, f.st.pool, "cost_object_id = $1", seg); !got.IsZero() {
		t.Fatalf("refused run posted %s", got)
	}
}

// BR-38.9: settlements draw down collected cost and never exceed it, even when
// two settlements of the same order race.
func TestSettleInternalOrder_CapAndConcurrency(t *testing.T) {
	f := newLedger(t, 5)
	order := f.object(t, "InternalOrder", "IO-TRADESHOW")
	receiver := f.object(t, "CostCenter", "CC-MARKETING")
	f.book(t, order, "500.00")

	settle := func(amount string) (Settlement, error) {
		_, raw, err := f.st.SettleInternalOrder(f.ctx, ids.New(), "h", SettlementRequest{OrderID: order, Period: "2026-09", Currency: "USD",
			ReceiverID: receiver, SecondaryElementID: f.secondary, Amount: dec(amount)})
		var s Settlement
		if err == nil {
			err = json.Unmarshal(raw, &s)
		}
		return s, err
	}
	first, err := settle("200.00")
	if err != nil || !first.Remaining.Equal(dec("300")) || first.LegalEntityID != f.le {
		t.Fatalf("first settlement %+v (%v)", first, err)
	}
	if _, err := settle("300.01"); !hasCode(err, CodeOverSettlement) {
		t.Fatalf("over-settlement: %v, want CO-0618", err)
	}

	// Two concurrent settlements of 200 against 300 collected: exactly one wins.
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = settle("200.00")
		}(i)
	}
	wg.Wait()
	if (errs[0] == nil) == (errs[1] == nil) {
		t.Fatalf("concurrent settlements: %v / %v, want exactly one to succeed", errs[0], errs[1])
	}
	if got := sumEntries(t, f.ctx, f.st.pool, "cost_object_id = $1", order); !got.Equal(dec("100")) {
		t.Fatalf("order keeps %s, want 100", got)
	}
	if got := sumEntries(t, f.ctx, f.st.pool, "settlement_id is not null"); !got.IsZero() {
		t.Fatalf("settlement entries net to %s, want 0", got)
	}
	list, err := f.st.ListSettlements(f.ctx, order, "2026-09")
	if err != nil || len(list) != 2 {
		t.Fatalf("settlements = %d (%v)", len(list), err)
	}

	// Only internal orders settle, onto a secondary element.
	if _, _, err := f.st.SettleInternalOrder(f.ctx, ids.New(), "h", SettlementRequest{OrderID: receiver, Period: "2026-09", Currency: "USD",
		ReceiverID: order, SecondaryElementID: f.secondary, Amount: dec("1")}); !hasCode(err, CodeNotInternalOrder) {
		t.Fatalf("settling a cost center: %v, want %s", err, CodeNotInternalOrder)
	}
	if _, _, err := f.st.SettleInternalOrder(f.ctx, ids.New(), "h", SettlementRequest{OrderID: order, Period: "2026-09", Currency: "USD",
		ReceiverID: receiver, SecondaryElementID: f.primary, Amount: dec("1")}); !hasCode(err, CodeSecondaryNotFound) {
		t.Fatalf("primary settlement element: %v, want %s", err, CodeSecondaryNotFound)
	}
}
