package store_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/processmfg/internal/domain"
	pmfevents "github.com/BenjaminSRussell/passage/services/processmfg/internal/events"
	"github.com/BenjaminSRussell/passage/services/processmfg/internal/store"
)

// coFormula is a 1,000 kg formula with a co-product (line 4) and a by-product (line 5).
func coFormula(code string) store.Formula {
	return store.Formula{Code: code, Name: "Dough", ItemID: ids.New(), Size: dec("1000"), Unit: "kg", Lines: []store.FormulaLine{
		{LineNumber: 1, ItemID: ids.New(), Type: domain.LineIngredient, Qty: dec("900"), Scalable: true},
		{LineNumber: 4, ItemID: ids.New(), Type: domain.LineCoProduct, Qty: dec("100"), Scalable: true},
		{LineNumber: 5, ItemID: ids.New(), Type: domain.LineByProduct, Qty: dec("50"), Scalable: true},
	}}
}

func newOrder(t *testing.T, s *store.Postgres) store.BatchOrder {
	t.Helper()
	ctx := ctxFor(1, "")
	f, _, err := s.CreateFormula(ctx, coFormula("CO-1"), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	o, _, err := s.CreateBatchOrder(ctx, store.NewBatchOrder{LegalEntityID: entityA, FormulaID: f.ID, BatchSize: dec("2000")}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func advance(t *testing.T, s *store.Postgres, o store.BatchOrder, cmd string) store.BatchOrder {
	t.Helper()
	out, changed, err := s.AdvanceBatchOrder(ctxFor(1, ""), o.ID, cmd, o.Version, store.Idem{Key: cmd + o.ID, Fingerprint: cmd})
	if err != nil || !changed {
		t.Fatalf("%s: changed=%v err=%v", cmd, changed, err)
	}
	return out
}

// The whole lifecycle: release, start, report as finished, end with the cost
// split. The split adds up to the batch cost, is stored once and read back.
func TestBatchOrderLifecycle_EndAllocatesCostExactly(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1, "")
	o := newOrder(t, s)
	o = advance(t, s, o, domain.CmdRelease)
	o = advance(t, s, o, domain.CmdStart)
	if o.Status != "started" || o.Version != 3 || o.StartedAt == nil || o.ReleasedAt == nil {
		t.Fatalf("after start %+v", o)
	}
	if _, _, err := s.RescaleBatchOrder(ctx, o.ID, dec("3000"), o.Version, store.Idem{}); problemCode(err) != store.CodeOrderNotRescalable {
		t.Fatalf("started order rescaled: %v", err)
	}
	co, by := o.Lines[1].ItemID, o.Lines[2].ItemID
	rep, changed, err := s.ReportBatchOrder(ctx, o.ID, store.ReportInput{MainQty: dec("1800"), Outputs: map[int]decimal.Decimal{4: dec("200")}}, o.Version, store.Idem{Key: "rep", Fingerprint: "r"})
	if err != nil || !changed || rep.Status != "reported" || !rep.ActualQty.Equal(dec("1800")) {
		t.Fatalf("report changed=%v %+v err=%v", changed, rep, err)
	}
	// Line 5 was left out: reported at its planned 100 kg (2000/1000 x 50).
	if !rep.Lines[1].RafQty.Equal(dec("200")) || !rep.Lines[2].RafQty.Equal(dec("100")) || rep.Lines[0].RafQty != nil {
		t.Fatalf("reported lines %+v", rep.Lines)
	}
	end := store.EndInput{Method: domain.MethodTCA, Currency: "USD", BatchCost: dec("10000"), MainBasis: domain.CostBasis{StandardCost: dec("40")},
		CoBasis: map[int]domain.CostBasis{4: {StandardCost: dec("20")}}, ByRates: map[int]domain.ByRate{5: {CostType: "per-quantity", Rate: dec("1.5")}}}
	if _, _, err := s.EndBatchOrder(ctx, o.ID, end, rep.Version+5, store.Idem{}); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("stale version: %v", err)
	}
	ended, changed, err := s.EndBatchOrder(ctx, o.ID, end, rep.Version, store.Idem{Key: "end", Fingerprint: "e"})
	if err != nil || !changed || ended.Status != "ended" || ended.EndedAt == nil || ended.Allocation == nil {
		t.Fatalf("end changed=%v %+v err=%v", changed, ended, err)
	}
	// burden 100 x 1.5 = 150 comes off 10000 first; the rest splits 72000 : 4000.
	sh := ended.Allocation.Shares
	if len(sh) != 3 || sh[0].Role != "main" || sh[1].ItemID != co || sh[2].ItemID != by || !sh[2].Amount.Equal(dec("150")) {
		t.Fatalf("shares %+v", sh)
	}
	if !sh[0].Amount.Equal(dec("9331.5789")) || !sh[1].Amount.Equal(dec("518.4211")) {
		t.Fatalf("main %s co %s", sh[0].Amount, sh[1].Amount)
	}
	var sum string
	if err := admin.QueryRow(context.Background(), `select sum(amount)::text from batch_order_allocation where order_id = $1`, o.ID).Scan(&sum); err != nil || !dec(sum).Equal(dec("10000")) {
		t.Fatalf("stored shares sum %s %v", sum, err)
	}
	got, err := s.GetBatchOrder(ctx, o.ID)
	if err != nil || got.Allocation == nil || len(got.Allocation.Shares) != 3 || got.Allocation.Currency != "USD" || !got.Allocation.BatchCost.Equal(dec("10000")) {
		t.Fatalf("read back %+v %v", got.Allocation, err)
	}
	again, changed, err := s.EndBatchOrder(ctx, o.ID, end, rep.Version, store.Idem{Key: "end", Fingerprint: "e"})
	if err != nil || changed || again.Version != ended.Version {
		t.Fatalf("replay changed=%v %v", changed, err)
	}
	if _, _, err := s.AdvanceBatchOrder(ctx, o.ID, domain.CmdStart, ended.Version, store.Idem{}); problemCode(err) != store.CodeOrderState {
		t.Fatalf("start after end: %v", err)
	}

	states := outboxEvents(t, admin, pmfevents.BatchOrderStateChanged)
	if len(states) != 4 {
		t.Fatalf("state events = %d, want 4 (the replay writes none)", len(states))
	}
	var last pmfevents.BatchOrderStateChangedData
	if err := json.Unmarshal(states[3].Data, &last); err != nil || last.From != "reported" || last.To != "ended" || last.Currency != "USD" || last.Version != ended.Version {
		t.Fatalf("last state event %+v %v", last, err)
	}
	var rd pmfevents.BatchOrderStateChangedData
	if err := json.Unmarshal(states[2].Data, &rd); err != nil || rd.ActualQty != "1800" || len(rd.Outputs) != 2 {
		t.Fatalf("report event %+v %v", rd, err)
	}
	allocated := outboxEvents(t, admin, pmfevents.CoProductAllocated)
	var data pmfevents.CoProductAllocatedData
	if len(allocated) != 1 || json.Unmarshal(allocated[0].Data, &data) != nil || len(data.Shares) != 3 || data.BatchCost != "10000" {
		t.Fatalf("allocated events %+v", allocated)
	}
}

func TestBatchOrderLifecycle_RefusalsWriteNothing(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1, "")
	o := newOrder(t, s)
	if _, _, err := s.AdvanceBatchOrder(ctx, o.ID, domain.CmdStart, o.Version, store.Idem{}); problemCode(err) != store.CodeOrderState {
		t.Fatalf("start of a planned order: %v", err)
	}
	if _, _, err := s.ReportBatchOrder(ctx, o.ID, store.ReportInput{MainQty: dec("1")}, o.Version, store.Idem{}); problemCode(err) != store.CodeOrderState {
		t.Fatalf("report of a planned order: %v", err)
	}
	if _, _, err := s.AdvanceBatchOrder(ctx, o.ID, domain.CmdRelease, 9, store.Idem{}); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("stale version: %v", err)
	}
	if _, _, err := s.AdvanceBatchOrder(ctx, ids.New(), domain.CmdRelease, 1, store.Idem{}); problemCode(err) != store.CodeOrderNotFound {
		t.Fatalf("missing order: %v", err)
	}
	o = advance(t, s, o, domain.CmdRelease)
	o = advance(t, s, o, domain.CmdStart)
	if _, _, err := s.ReportBatchOrder(ctx, o.ID, store.ReportInput{MainQty: dec("1"), Outputs: map[int]decimal.Decimal{1: dec("5")}}, o.Version, store.Idem{}); problemCode(err) != store.CodeReportInvalid {
		t.Fatalf("report of an ingredient line: %v", err)
	}
	if _, _, err := s.ReportBatchOrder(ctx, o.ID, store.ReportInput{MainQty: dec("0")}, o.Version, store.Idem{}); problemCode(err) != store.CodeReportInvalid {
		t.Fatalf("zero main quantity: %v", err)
	}
	o2, _, err := s.ReportBatchOrder(ctx, o.ID, store.ReportInput{MainQty: dec("1800")}, o.Version, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	// A burden above the cost is refused by the rule and the order stays reported.
	bad := store.EndInput{Method: domain.MethodQuantity, Currency: "USD", BatchCost: dec("10"),
		ByRates: map[int]domain.ByRate{5: {CostType: "per-quantity", Rate: dec("500")}}}
	if _, _, err := s.EndBatchOrder(ctx, o.ID, bad, o2.Version, store.Idem{}); problemCode(err) != domain.CodeAllocation {
		t.Fatalf("burden above cost: %v", err)
	}
	got, _ := s.GetBatchOrder(ctx, o.ID)
	var n int
	if err := admin.QueryRow(context.Background(), `select count(*) from batch_order_allocation`).Scan(&n); err != nil || n != 0 || got.Status != "reported" || got.Allocation != nil {
		t.Fatalf("refused end left %d allocation rows, status %s: %v", n, got.Status, err)
	}
	// The other tenant and a caller scoped to another legal entity cannot move the order.
	if _, _, err := s.AdvanceBatchOrder(ctxFor(2, ""), o.ID, domain.CmdRelease, 1, store.Idem{}); problemCode(err) != store.CodeOrderNotFound {
		t.Fatalf("other tenant: %v", err)
	}
	if _, _, err := s.EndBatchOrder(ctxFor(1, entityB), o.ID, bad, o2.Version, store.Idem{}); problemCode(err) != store.CodeOrderNotFound {
		t.Fatalf("other legal entity: %v", err)
	}
}
