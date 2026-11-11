package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

// FR-38.5: the release run is a path that creates purchase orders, so it tells budget control like the others, and every
// path writes the owed conversion calls in the order's own transaction.
func TestReleaseRun_WritesAndMakesTheBudgetConversionCalls(t *testing.T) {
	pool := migrated(t)
	var told []store.PurchaseOrder
	s := store.New(pool).WithBudgetCalls(true).WithOrderHook(func(_ context.Context, orders []store.PurchaseOrder) { told = append(told, orders...) })
	ctx := principalContext(31)
	va := ids.New()
	req := approvedFor(t, s, va, va)
	a1, a2 := req.Lines[0], req.Lines[1]
	run, err := s.ReleaseRequisitions(ctx, store.ReleaseInput{LegalEntityID: legalEntity, WindowDays: 7, MaxLines: 200, Considered: 2, RequestedBy: ids.New(),
		Groups: []store.ReleaseGroup{{VendorID: va, Lines: sourcingOf(t, s, a1.ID, a2.ID), Orders: []store.PurchaseOrder{orderFor(t, va, a1, a2)}}}},
		store.Idem{Key: "rel-budget", Hash: "h"})
	if err != nil || len(run.PurchaseOrders) != 1 {
		t.Fatalf("run = %+v, %v", run, err)
	}
	if len(told) != 1 || told[0].ID != run.PurchaseOrders[0].PurchaseOrderID || len(told[0].Lines) != 2 {
		t.Fatalf("hook told %+v; want the one order the run created, with its two lines", told)
	}
	calls, err := s.BudgetCallsFor(ctx, told[0].ID, "")
	if err != nil || len(calls) != 2 {
		t.Fatalf("calls = %+v, %v; want one conversion per requisition-linked PO line", calls, err)
	}
	for _, c := range calls {
		if c.Kind != store.BudgetCallConvert || c.Status != store.BudgetCallPending || c.PurchaseOrderLineID == "" || (c.RequisitionLineID != a1.ID && c.RequisitionLineID != a2.ID) {
			t.Fatalf("call = %+v", c)
		}
	}
	// A replay of the run creates nothing new and tells nobody again.
	if _, err := s.ReleaseRequisitions(ctx, store.ReleaseInput{LegalEntityID: legalEntity, Considered: 2}, store.Idem{Key: "rel-budget", Hash: "h"}); err != nil || len(told) != 1 {
		t.Fatalf("replay told %d times, %v", len(told), err)
	}

	if err := s.RecordBudgetCall(ctx, calls[0].ID, errors.New("budgeting is down")); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordBudgetCall(ctx, calls[1].ID, nil); err != nil {
		t.Fatal(err)
	}
	pending, err := s.ListBudgetCalls(ctx, store.BudgetCallPending, 50)
	if err != nil || len(pending) != 1 || pending[0].ID != calls[0].ID || pending[0].Attempts != 1 || pending[0].LastError != "budgeting is down" {
		t.Fatalf("pending = %+v, %v; want the failed one with its attempt and message", pending, err)
	}
	done, err := s.ListBudgetCalls(ctx, store.BudgetCallDone, 50)
	if err != nil || len(done) != 1 || done[0].DoneAt == nil || done[0].Attempts != 1 {
		t.Fatalf("done = %+v, %v", done, err)
	}
	// Another tenant sees none of them.
	if other, err := s.ListBudgetCalls(principalContext(32), "", 50); err != nil || len(other) != 0 {
		t.Fatalf("other tenant sees %+v, %v", other, err)
	}
}

func TestCancelApprovedRequisition_QueuesAReleasePerLineAndOnlyWhenBudgetCallsAreOn(t *testing.T) {
	pool := migrated(t)
	ctx := principalContext(31)
	on := store.New(pool).WithBudgetCalls(true)
	req := approvedFor(t, on, ids.New(), ids.New())
	cancelled, err := on.ActOnRequisition(ctx, req.ID, req.Version, store.RequisitionAct{Action: "cancel", ActorID: req.RequesterID}, store.Idem{})
	if err != nil || cancelled.Status != store.RequisitionCancelled {
		t.Fatalf("cancel = %+v, %v", cancelled, err)
	}
	calls, err := on.BudgetCallsFor(ctx, "", req.ID)
	if err != nil || len(calls) != 2 || calls[0].Kind != store.BudgetCallRelease || calls[0].RequisitionLineID == "" {
		t.Fatalf("calls = %+v, %v; want a pending release for each of the two lines", calls, err)
	}
	off := store.New(pool)
	req2 := approvedFor(t, off, ids.New())
	if _, err := off.ActOnRequisition(ctx, req2.ID, req2.Version, store.RequisitionAct{Action: "cancel", ActorID: req2.RequesterID}, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	if calls, _ := off.BudgetCallsFor(ctx, "", req2.ID); len(calls) != 0 {
		t.Fatalf("without budget calls turned on nothing is queued: %+v", calls)
	}
}

func TestRequisitionLine_KeepsItsBudgetDimensions(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(31)
	in := requisition(t, ids.New(), "10", "20")
	in.Lines[0].GLAccount, in.Lines[0].CostCenter = "6100", "CC-20"
	r, err := s.CreateRequisition(ctx, in, store.Idem{})
	if err != nil || r.Lines[0].GLAccount != "6100" || r.Lines[0].CostCenter != "CC-20" || r.Lines[1].GLAccount != "" {
		t.Fatalf("created = %+v, %v", r.Lines, err)
	}
}
