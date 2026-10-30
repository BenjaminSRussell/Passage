package api_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/receiving/internal/api"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store/memstore"
)

type markCall struct {
	legalEntityID, orderID string
	lineNo, seq            int
	ref                    string
	amount                 decimal.Decimal
}

type purchasingDouble struct {
	plans    []domain.InvoicingPlan
	calls    []markCall
	err      error
	rejectAt map[int]bool
}

func (p *purchasingDouble) List(context.Context, string) ([]domain.InvoicingPlan, error) {
	out := make([]domain.InvoicingPlan, len(p.plans))
	for i, pl := range p.plans {
		pl.Rows = append([]domain.PlanRow(nil), pl.Rows...)
		out[i] = pl
	}
	return out, nil
}

func (p *purchasingDouble) MarkInvoiced(_ context.Context, le, orderID string, lineNo, seq int, ref string, amount decimal.Decimal) error {
	p.calls = append(p.calls, markCall{le, orderID, lineNo, seq, ref, amount})
	if p.err != nil {
		return p.err
	}
	if p.rejectAt[seq] {
		return fmt.Errorf("status 409: %w", domain.ErrPlanRowMarkRejected)
	}
	for i := range p.plans {
		for j := range p.plans[i].Rows {
			if p.plans[i].LineNo == lineNo && p.plans[i].Rows[j].Seq == seq {
				p.plans[i].Rows[j].Status = domain.PlanRowInvoiced
			}
		}
	}
	return nil
}

func markingRig(p *purchasingDouble) *rig {
	return newRigPorts(memstore.New(), domain.PeriodOpen, func(ports *api.Ports) {
		ports.PurchaseOrders = domain.FakePurchaseOrders{Orders: map[string]domain.PurchaseOrder{poID: planPO()}}
		ports.InvoicingPlans, ports.PlanRowSettlements = p, p
	})
}

func twoRowPlan() []domain.InvoicingPlan {
	return []domain.InvoicingPlan{{PurchaseOrderID: poID, LineNo: 1, SettleWithoutReceipt: true, Currency: "USD", Rows: planRows(
		row(1, day(2026, 9, 1), "1000.00", "OPEN"), row(2, day(2026, 10, 1), "1000.50", "OPEN"))}}
}

func (rg *rig) run(t *testing.T) runJSON {
	return decode[runJSON](t, rg.call(http.MethodPost, planRunPath, planBody(), rg.settle()), http.StatusOK)
}

func TestPlanSettlement_TellsPurchasingEachRowItSettled(t *testing.T) {
	p := &purchasingDouble{plans: twoRowPlan()}
	rg := markingRig(p)

	first := rg.run(t)

	if len(first.Settled) != 2 || len(p.calls) != 2 {
		t.Fatalf("settled %d, purchasing told %d times", len(first.Settled), len(p.calls))
	}
	for i, c := range p.calls {
		s := first.Settled[i]
		if c.orderID != poID || c.legalEntityID != legalEntityID || c.lineNo != 1 || c.seq != i+1 || c.ref != s.ID || c.amount.String() != dec(s.Amount).String() {
			t.Fatalf("call %d = %+v for settlement %+v", i, c, s)
		}
	}
	again := rg.run(t)
	if len(again.Settled) != 0 || again.skips()[poID+"/1/1"] != "ALREADY_INVOICED" || len(p.calls) != 2 || len(rg.ledger.Entries) != 2 {
		t.Fatalf("second run: settled %d skips %v calls %d entries %d", len(again.Settled), again.skips(), len(p.calls), len(rg.ledger.Entries))
	}
}

func TestPlanSettlement_PurchasingDownStillSettlesAndTheNextRunCatchesUp(t *testing.T) {
	p := &purchasingDouble{plans: twoRowPlan(), err: errors.New("connection refused")}
	rg := markingRig(p)

	first := rg.run(t)

	if len(first.Settled) != 2 || len(rg.ledger.Entries) != 2 {
		t.Fatalf("settlement must not depend on purchasing: settled %d, entries %d", len(first.Settled), len(rg.ledger.Entries))
	}
	if len(p.calls) != 1 {
		t.Fatalf("a transport failure must stop the run's marking after one attempt, got %d", len(p.calls))
	}

	p.err = nil
	catchUp := rg.run(t)

	if len(catchUp.Settled) != 0 || len(rg.ledger.Entries) != 2 {
		t.Fatalf("catch-up run re-posted: settled %d entries %d", len(catchUp.Settled), len(rg.ledger.Entries))
	}
	if catchUp.skips()[poID+"/1/1"] != "ALREADY_SETTLED" || catchUp.skips()[poID+"/1/2"] != "ALREADY_SETTLED" {
		t.Fatalf("skips %v", catchUp.skips())
	}
	if len(p.calls) != 3 || p.plans[0].Rows[0].Status != domain.PlanRowInvoiced || p.plans[0].Rows[1].Status != domain.PlanRowInvoiced {
		t.Fatalf("after catch-up: calls %d rows %+v", len(p.calls), p.plans[0].Rows)
	}
	if p.calls[1].ref != first.Settled[0].ID || p.calls[2].ref != first.Settled[1].ID {
		t.Fatalf("catch-up used references %q, %q; want the original settlements", p.calls[1].ref, p.calls[2].ref)
	}
}

func TestPlanSettlement_RejectedMarkDoesNotStopTheOtherRows(t *testing.T) {
	p := &purchasingDouble{plans: twoRowPlan(), rejectAt: map[int]bool{1: true}}
	rg := markingRig(p)

	first := rg.run(t)

	if len(first.Settled) != 2 || len(p.calls) != 2 || p.plans[0].Rows[1].Status != domain.PlanRowInvoiced || p.plans[0].Rows[0].Status != domain.PlanRowOpen {
		t.Fatalf("settled %d calls %d rows %+v", len(first.Settled), len(p.calls), p.plans[0].Rows)
	}
}

func TestPlanSettlement_SettledRowWhoseAmountChangedIsReportedNotRemarked(t *testing.T) {
	p := &purchasingDouble{plans: twoRowPlan(), err: errors.New("down")}
	rg := markingRig(p)
	rg.run(t)

	p.err = nil
	p.plans[0].Rows[0].Amount = dec("900.00")
	next := rg.run(t)

	if next.skips()[poID+"/1/1"] != "SETTLED_AMOUNT_CHANGED" || next.skips()[poID+"/1/2"] != "ALREADY_SETTLED" {
		t.Fatalf("skips %v", next.skips())
	}
	if len(p.calls) != 2 || p.plans[0].Rows[0].Status != domain.PlanRowOpen {
		t.Fatalf("a changed row must not be marked: calls %d rows %+v", len(p.calls), p.plans[0].Rows)
	}
}
