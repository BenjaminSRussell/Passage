package store

import (
	"context"
	"errors"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
)

func (e *rmaEnv) toDispositioned(t *testing.T, qty int64) ReturnOrder {
	t.Helper()
	insp := e.toInspected(t, e.create(t, e.newOrder(domain.DispositionReturnToStock, qty)), qty, qty, domain.DispositionReturnToStock)
	disp, err := e.s.DispositionReturnOrder(e.ctx, insp.ID, insp.Version, "returns", e.now)
	if err != nil {
		t.Fatalf("disposition: %v", err)
	}
	return disp
}

// #557: a credit receivables never confirmed is stored; once the operator released it there with :cancel and the
// invoice changed, a plain repeat sends the stored credit again, while a recompute sends the corrected one.
func TestRMA_RecomputeSendsTheCorrectedCreditUnderTheSameKey(t *testing.T) {
	e := newRMAEnv(t)
	disp := e.toDispositioned(t, 4)
	e.ar.failNext = true
	if _, err := e.s.CreditReturnOrder(e.ctx, disp.ID, disp.Version, "ar", e.now); stateCode(t, err) != CodeRMAUpstream {
		t.Fatalf("receivables down: %v", err)
	}
	stuck, err := e.s.GetReturnOrder(e.ctx, disp.ID)
	if err != nil || stuck.Status != domain.RMACredited || len(stuck.Lines) == 0 {
		t.Fatalf("a failed post leaves the credit stored: %+v %v", stuck, err)
	}
	// Billing now holds the line with tax, which the first computation did not know.
	e.src.invoice.Lines[0].TaxCode, e.src.invoice.Lines[0].Tax = "VAT", decimal.RequireFromString("190.00")

	if _, err := e.s.RecomputeReturnCredit(e.ctx, stuck.ID, stuck.Version+7, "ar", e.now); problemStatus(err) != 412 {
		t.Fatalf("stale version: %v", err)
	}
	re, err := e.s.RecomputeReturnCredit(e.ctx, stuck.ID, stuck.Version, "ar", e.now)
	if err != nil || re.Version != stuck.Version+1 || re.Status != domain.RMACredited {
		t.Fatalf("recompute: %+v %v", re, err)
	}
	if !re.Tax.Equal(decimal.RequireFromString("76")) {
		t.Fatalf("tax of 4 of 10 units at 190: %s", re.Tax)
	}
	done, err := e.s.CreditReturnOrder(e.ctx, re.ID, re.Version, "ar", e.now)
	if err != nil || done.Status != domain.RMAClosed || done.CreditNoteID == nil {
		t.Fatalf("credit after recompute: %+v %v", done, err)
	}
	if n := len(e.ar.credits); n != 1 || e.ar.credits[0].IdempotencyKey != "rma-credit-"+disp.ID ||
		len(e.ar.credits[0].TaxLines) != 1 || !e.ar.credits[0].TaxLines[0].Amount.Equal(decimal.RequireFromString("76")) {
		t.Fatalf("credit sent: %+v", e.ar.credits)
	}
	if _, err := e.s.RecomputeReturnCredit(e.ctx, done.ID, done.Version, "ar", e.now); stateCode(t, err) != CodeRMAState {
		t.Fatalf("a confirmed credit is not recomputed: %v", err)
	}
}

// The recompute refuses a return that has not reached the credit.
func TestRMA_RecomputeNeedsACreditedReturn(t *testing.T) {
	e := newRMAEnv(t)
	disp := e.toDispositioned(t, 2)
	if _, err := e.s.RecomputeReturnCredit(e.ctx, disp.ID, disp.Version, "ar", e.now); stateCode(t, err) != CodeRMAState {
		t.Fatalf("dispositioned: %v", err)
	}
}

type backfillAR struct {
	recordingAR
	calls []string
	held  map[string]bool
	fail  map[string]bool
}

func (b *backfillAR) BackfillRMAReservation(_ context.Context, le, rmaID, invoiceID string, lines []ARInvoiceLine) error {
	b.calls = append(b.calls, rmaID+"|"+invoiceID)
	switch {
	case b.held[rmaID]:
		return ErrReservationHeld
	case b.fail[rmaID]:
		return errors.New("billing down")
	}
	return nil
}

// #557: the sales-side driver gives the credits posted before reservations existed the reservation they never took,
// works out the lines of a credit stored before migration 0048, and takes each order only once.
func TestRMA_BackfillDriverReservesEarlierCreditsOnce(t *testing.T) {
	e := newRMAEnv(t)
	ar := &backfillAR{held: map[string]bool{}, fail: map[string]bool{}}
	e.s.WithAR(ar)
	credit := func() ReturnOrder {
		disp := e.toDispositioned(t, 2)
		// Each credit uses its own invoice in the fake billing, so reset the credited quantity between RMAs.
		done, err := e.s.CreditReturnOrder(e.ctx, disp.ID, disp.Version, "ar", e.now)
		if err != nil || done.CreditNoteID == nil {
			t.Fatalf("credit: %+v %v", done, err)
		}
		return done
	}
	legacy, held, failing := credit(), credit(), credit()
	// The first was credited before 0048: no stored invoice lines.
	if _, err := e.pool.Exec(e.ctx, `update return_order set credit_invoice_lines = null where id = $1`, legacy.ID); err != nil {
		t.Fatal(err)
	}
	ar.held[held.ID], ar.fail[failing.ID] = true, true

	res, err := e.s.BackfillReservations(e.ctx, e.le, 0)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if res.Backfilled != 1 || res.AlreadyHeld != 1 || len(res.Failed) != 1 || res.Failed[0].RMANumber != failing.RMANumber || res.More {
		t.Fatalf("result %+v calls %v", res, ar.calls)
	}
	if len(ar.calls) != 3 || ar.calls[0] != legacy.ID+"|"+e.src.invoice.ID {
		t.Fatalf("calls %v", ar.calls)
	}
	// A repeat takes up only what failed.
	delete(ar.fail, failing.ID)
	ar.calls = nil
	res, err = e.s.BackfillReservations(e.ctx, e.le, 0)
	if err != nil || res.Backfilled != 1 || len(ar.calls) != 1 || ar.calls[0] != failing.ID+"|"+e.src.invoice.ID {
		t.Fatalf("repeat %+v %v calls %v", res, err, ar.calls)
	}
	res, err = e.s.BackfillReservations(e.ctx, e.le, 0)
	if err != nil || res.Backfilled != 0 || len(ar.calls) != 1 {
		t.Fatalf("nothing left %+v %v calls %v", res, err, ar.calls)
	}
}

func TestRMA_BackfillWithoutAReservationBackfillerIsUnavailable(t *testing.T) {
	e := newRMAEnv(t)
	if _, err := e.s.BackfillReservations(e.ctx, e.le, 0); problemStatus(err) != 503 {
		t.Fatalf("recording AR has no backfill: %v", err)
	}
}
