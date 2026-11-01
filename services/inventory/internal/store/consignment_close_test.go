package store_test

import (
	"sync"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	invevents "github.com/BenjaminSRussell/passage/services/inventory/internal/events"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
)

// A partly received replenishment order is closed: the received vendor-owned stock stays, the open
// quantity is given up, the order takes no more receipts and consignment.closed names what was given up.
func TestCloseConsignmentKeepsReceivedStockAndGivesUpTheRest(t *testing.T) {
	s, _, admin := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	o, _, err := s.CreateConsignment(ctx, ids.New(), replenishment(ctx, product, "10", "3"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CloseConsignment(ctx, o.ID, o.Version, "nothing received"); problemCode(err) != domain.CodeConsignmentUntouched {
		t.Fatalf("close of an untouched order: %v", err)
	}
	date := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	o, err = s.ReceiveConsignment(ctx, o.ID, o.Version, []domain.ConsignmentReceipt{{LineID: o.Lines[0].ID, Qty: dec("4")}}, date)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CancelConsignment(ctx, o.ID, o.Version, ""); problemCode(err) != domain.CodeConsignmentStarted {
		t.Fatalf("cancel of an order with receipts: %v", err)
	}
	if _, err := s.CloseConsignment(ctx, o.ID, o.Version+1, ""); problemCode(err) != store.CodeConflictVersion {
		t.Fatalf("stale version: %v", err)
	}
	closed, err := s.CloseConsignment(ctx, o.ID, o.Version, "vendor stopped supplying")
	if err != nil || closed.Status != domain.ConsignmentClosed || closed.Version != o.Version+1 || closed.ClosedAt == nil ||
		closed.CloseReason != "vendor stopped supplying" || closed.ClosedBy == "" {
		t.Fatalf("close: %v %+v", err, closed)
	}
	if line := closed.Lines[0]; !closed.OpenQty(line).IsZero() || !closed.GivenUpQty(line).Equal(dec("6")) || !line.ReceivedQty.Equal(dec("4")) {
		t.Fatalf("line after close: %+v", line)
	}
	if qty, _ := ownerStock(t, s, ctx, product, "V1"); !qty.Equal(dec("4")) {
		t.Fatalf("vendor-owned stock %s, want the 4 received to stay", qty)
	}
	if _, err := s.ReceiveConsignment(ctx, o.ID, closed.Version, []domain.ConsignmentReceipt{{LineID: o.Lines[0].ID, Qty: dec("1")}}, date); problemCode(err) != domain.CodeConsignmentState {
		t.Fatalf("receipt on a closed order: %v", err)
	}
	if _, err := s.CloseConsignment(ctx, o.ID, closed.Version, ""); problemCode(err) != domain.CodeConsignmentState {
		t.Fatalf("second close: %v", err)
	}
	var evt invevents.ConsignmentClosedData
	eventData(t, admin, invevents.ConsignmentClosed, "'consignmentOrderId'", o.ID, &evt)
	if evt.Disposition != "CLOSED" || evt.VendorID != "V1" || evt.Reason != "vendor stopped supplying" || len(evt.Lines) != 1 ||
		evt.Lines[0].Qty != "10" || evt.Lines[0].ReceivedQty != "4" || evt.Lines[0].GivenUpQty != "6" {
		t.Fatalf("event %+v", evt)
	}
	other := entityCtx(1, ids.New())
	if _, err := s.CloseConsignment(other, o.ID, closed.Version, ""); problemCode(err) != domain.CodeConsignmentNotFound {
		t.Fatalf("another legal entity: %v", err)
	}
}

func TestCancelConsignmentEndsAnUntouchedOrder(t *testing.T) {
	s, _, admin := setup(t)
	ctx := entityCtx(1, ids.New())
	o, _, err := s.CreateConsignment(ctx, ids.New(), replenishment(ctx, ids.New(), "10", "3"))
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := s.CancelConsignment(ctx, o.ID, o.Version, "")
	if err != nil || cancelled.Status != domain.ConsignmentCancelled || cancelled.ClosedAt == nil || cancelled.CloseReason != "" ||
		!cancelled.GivenUpQty(cancelled.Lines[0]).Equal(dec("10")) {
		t.Fatalf("cancel: %v %+v", err, cancelled)
	}
	if _, err := s.ReceiveConsignment(ctx, o.ID, cancelled.Version, []domain.ConsignmentReceipt{{LineID: o.Lines[0].ID, Qty: dec("1")}}, time.Now()); problemCode(err) != domain.CodeConsignmentState {
		t.Fatalf("receipt on a cancelled order: %v", err)
	}
	if _, err := s.CancelConsignment(ctx, o.ID, cancelled.Version, ""); problemCode(err) != domain.CodeConsignmentState {
		t.Fatalf("second cancel: %v", err)
	}
	list, err := s.ListConsignments(ctx, store.ConsignmentFilter{Status: domain.ConsignmentCancelled})
	if err != nil || len(list) != 1 || list[0].ID != o.ID {
		t.Fatalf("list by status: %v %+v", err, list)
	}
	var evt invevents.ConsignmentClosedData
	eventData(t, admin, invevents.ConsignmentClosed, "'consignmentOrderId'", o.ID, &evt)
	if evt.Disposition != "CANCELLED" || evt.Lines[0].GivenUpQty != "10" {
		t.Fatalf("event %+v", evt)
	}
}

// A receipt and a close sent at the same version of a partly received order race on the order's row
// lock: exactly one wins and the loser is refused (412 at the stale version), so no receipt is ever
// booked against a closed order and the given-up quantity matches the stock that stayed.
func TestConcurrentReceiptAndCloseNeverBookAgainstAClosedOrder(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	date := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	for round := 0; round < 10; round++ {
		product := ids.New()
		o, _, err := s.CreateConsignment(ctx, ids.New(), replenishment(ctx, product, "10", "3"))
		if err != nil {
			t.Fatal(err)
		}
		if o, err = s.ReceiveConsignment(ctx, o.ID, o.Version, []domain.ConsignmentReceipt{{LineID: o.Lines[0].ID, Qty: dec("4")}}, date); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		start := make(chan struct{})
		var receiveErr, closeErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, receiveErr = s.ReceiveConsignment(ctx, o.ID, o.Version, []domain.ConsignmentReceipt{{LineID: o.Lines[0].ID, Qty: dec("2")}}, date)
		}()
		go func() {
			defer wg.Done()
			<-start
			_, closeErr = s.CloseConsignment(ctx, o.ID, o.Version, "race")
		}()
		close(start)
		wg.Wait()
		if (receiveErr == nil) == (closeErr == nil) {
			t.Fatalf("round %d: receive %v, close %v; want exactly one to succeed", round, receiveErr, closeErr)
		}
		final, err := s.GetConsignment(ctx, o.ID)
		if err != nil {
			t.Fatal(err)
		}
		stock, _ := ownerStock(t, s, ctx, product, "V1")
		if closeErr == nil {
			if problemCode(receiveErr) != store.CodeConflictVersion || final.Status != domain.ConsignmentClosed || !stock.Equal(dec("4")) ||
				!final.GivenUpQty(final.Lines[0]).Equal(dec("6")) {
				t.Fatalf("round %d close won: receive %v status %s stock %s line %+v", round, receiveErr, final.Status, stock, final.Lines[0])
			}
		} else if problemCode(closeErr) != store.CodeConflictVersion || final.Status != domain.ConsignmentPartial || !stock.Equal(dec("6")) {
			t.Fatalf("round %d receive won: close %v status %s stock %s", round, closeErr, final.Status, stock)
		}
	}
}
