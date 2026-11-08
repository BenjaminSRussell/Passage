package store

import (
	"errors"
	"net/http"
	"slices"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
)

func findingCodes(fs []domain.EndFinding) []string {
	out := []string{}
	for _, f := range fs {
		out = append(out, f.Code)
	}
	return out
}

// endCheck reads the End check of the stepper's order for a caller with or without the
// zero-output duty.
func endCheck(t *testing.T, st *stepper, duty bool) domain.EndReport {
	t.Helper()
	rep, err := st.s.EndCheck(st.ctx, st.orderID, duty)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

// refusedAsReported runs `:end` on the same order for the same caller and fails unless the
// command refuses with the check's first blocker: the check and the command agree.
func refusedAsReported(t *testing.T, st *stepper, rep domain.EndReport, duty bool) {
	t.Helper()
	if rep.CanEnd || len(rep.Blockers) == 0 {
		t.Fatalf("the check reports no blocker: %+v", rep)
	}
	_, err := st.step(domain.CmdEnd, func(tr *Transition) { tr.ZeroOutputDuty = duty })
	var pr *httpx.Problem
	if !errors.As(err, &pr) || pr.Code != rep.Blockers[0].Code || pr.Detail != rep.Blockers[0].Message {
		t.Fatalf(":end answered %v, the check reported %+v", err, rep.Blockers)
	}
}

// TestEndCheck follows an order from Started to Ended: at each step the check names what
// `:end` refuses (and `:end` refuses exactly that) or warns about, and a check that says the
// order can end is followed by an End that goes through.
func TestEndCheck(t *testing.T) {
	_, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(9701))
	le, motor, housing := ids.New(), ids.New(), ids.New()
	bom := []domain.BOMComponent{{LineNo: "10", ItemID: motor, QtyPer: d("1"), UnitCost: d("100")},
		{LineNo: "20", ItemID: housing, QtyPer: d("1"), UnitCost: d("10")}}
	in := orderOf(t, "ec-order", le, ids.New(), "2", bom, "1")
	ops, err := domain.PlanOperations([]domain.Operation{
		{OperNumber: 10, ResourceID: ids.New(), RunHPer: d("1"), LaborRate: d("30")},
		{OperNumber: 20, ResourceID: ids.New(), Subcontract: true, SubcontractPrice: d("2")},
	}, in.Quantity)
	if err != nil {
		t.Fatal(err)
	}
	in.Operations = ops
	st := startedOrder(t, s, ctx, in)

	// Started with no output: ending needs the zero-output duty, judged for the caller.
	rep := endCheck(t, st, false)
	if !slices.Equal(findingCodes(rep.Blockers), []string{domain.CodeZeroOutput}) || len(rep.Warnings) != 0 {
		t.Fatalf("started, no duty: %+v", rep)
	}
	refusedAsReported(t, st, rep, false)
	if rep := endCheck(t, st, true); !rep.CanEnd || len(rep.Blockers) != 0 {
		t.Fatalf("started, duty: %+v", rep)
	}
	// PROD-0014 is not reported: this service cannot tell an invoiced subcontract operation.
	// Consume both motors and both housings, assign one motor serial to a finished serial and
	// both housings as a batch, and leave a draft journal open.
	st.mustPost(st.mustJournal(NewJournal{Type: domain.JournalPickingList, Issues: []IssueInput{{LineNo: "10", Qty: d("2")}, {LineNo: "20", Qty: d("2")}}}).ID)
	if _, _, err := s.AssignAsBuilt(ctx, NewAsBuilt{OrderID: st.orderID, Actor: "operator-1", IdempotencyKey: "ec-ab-1", RequestHash: "ec-ab-1",
		AsBuilt: domain.AsBuilt{FinishedSerial: "S-1", ComponentItemID: motor, Tracking: domain.TrackSerial, ComponentSerial: "M-1", Qty: d("1")}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AssignAsBuilt(ctx, NewAsBuilt{OrderID: st.orderID, Actor: "operator-1", IdempotencyKey: "ec-ab-2", RequestHash: "ec-ab-2",
		AsBuilt: domain.AsBuilt{FinishedSerial: "S-1", ComponentItemID: housing, Tracking: domain.TrackBatch, ComponentSerial: "H-B1", Qty: d("1")}}); err != nil {
		t.Fatal(err)
	}
	draft := st.mustJournal(NewJournal{Type: domain.JournalReportFinished, QtyGood: d("2")})
	rep = endCheck(t, st, true)
	if !slices.Equal(findingCodes(rep.Blockers), []string{domain.CodeUnpostedJournals}) || rep.Blockers[0].Count != 1 {
		t.Fatalf("with a draft: %+v", rep)
	}
	// One motor serial is unassigned; the batch-tracked housing is not counted as serials.
	if !slices.Equal(findingCodes(rep.Warnings), []string{domain.CodeAsBuiltUnresolved}) || rep.Warnings[0].Count != 1 ||
		rep.Warnings[0].Message != "1 component serials are not assigned to a finished serial" {
		t.Fatalf("warnings with one motor unassigned: %+v", rep.Warnings)
	}
	refusedAsReported(t, st, rep, true)

	st.mustPost(draft.ID)
	st.refresh()
	if rep := endCheck(t, st, false); !rep.CanEnd || len(rep.Blockers) != 0 || len(rep.Warnings) != 1 {
		t.Fatalf("reported as finished: %+v", rep)
	}
	_, err = s.EndCheck(ctx, ids.New(), false)
	wantProblem(t, err, http.StatusNotFound, CodeOrderNotFound)
	// Another tenant cannot see the order.
	_, err = s.EndCheck(tenantCtx(testkit.TenantID(9702)), st.orderID, true)
	wantProblem(t, err, http.StatusNotFound, CodeOrderNotFound)

	st.must(domain.CmdEnd, nil)
	if rep := endCheck(t, st, true); rep.CanEnd || !slices.Equal(findingCodes(rep.Blockers), []string{domain.CodeIllegalTransition}) || len(rep.Warnings) != 0 {
		t.Fatalf("ended: %+v", rep)
	}
}

// The check reads the site's "As-built required" (PROD-0021) like `:end` does.
func TestEndCheck_AsBuiltRequired(t *testing.T) {
	_, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(9703))
	le, motor := ids.New(), ids.New()
	bom := []domain.BOMComponent{{LineNo: "10", ItemID: motor, QtyPer: d("1"), UnitCost: d("100")}}
	in := orderOf(t, "ec-ab", le, ids.New(), "1", bom, "1")
	saveParams(t, s, ctx, le, func(p *domain.Parameters) { p.AsBuiltRequired = true }, in.SiteID, 0)
	st := startedOrder(t, s, ctx, in)
	st.mustPost(st.mustJournal(NewJournal{Type: domain.JournalPickingList, Issues: []IssueInput{{LineNo: "10", Qty: d("1")}}}).ID)
	st.must(domain.CmdReportFinished, func(tr *Transition) { tr.Good = d("1") })

	rep := endCheck(t, st, false)
	if !slices.Equal(findingCodes(rep.Blockers), []string{domain.CodeAsBuiltRequired}) {
		t.Fatalf("no as-built record: %+v", rep)
	}
	refusedAsReported(t, st, rep, false)
	if _, _, err := s.AssignAsBuilt(ctx, NewAsBuilt{OrderID: st.orderID, Actor: "operator-1", IdempotencyKey: "ec-ab-r", RequestHash: "ec-ab-r",
		AsBuilt: domain.AsBuilt{FinishedSerial: "S-1", ComponentItemID: motor, Tracking: domain.TrackSerial, ComponentSerial: "M-1", Qty: d("1")}}); err != nil {
		t.Fatal(err)
	}
	st.refresh()
	if rep := endCheck(t, st, false); !rep.CanEnd || len(rep.Warnings) != 0 {
		t.Fatalf("record attached: %+v", rep)
	}
	st.must(domain.CmdEnd, nil)
}
