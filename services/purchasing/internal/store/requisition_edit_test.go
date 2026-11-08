package store_test

import (
	"sync"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	purchasingevents "github.com/BenjaminSRussell/passage/services/purchasing/internal/events"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

func lineOf(t *testing.T, no int, amount string) store.RequisitionLine {
	return store.RequisitionLine{LineNo: no, Description: "Edited", Category: "IT", Qty: dec("2"), Unit: "EA",
		EstimatedUnitPrice: dec(amount), Amount: dec(amount).Mul(dec("2")), RequestedDate: day(t, "2026-11-09")}
}

// BR-17.1: the requester edits a DRAFT - lines replaced as a set and renumbered, total follows, version moves on,
// EDITED joins the history; a reopened (rejected) requisition is a DRAFT again and can be edited, an IN_REVIEW or
// APPROVED one cannot; stale versions and replays behave as for the other actions.
func TestUpdateRequisition_EditsADraftOnly(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(101)
	requester := ids.New()
	r, err := s.CreateRequisition(ctx, requisition(t, requester, "10", "20"), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	edit := store.RequisitionEdit{Currency: "USD", Lines: []store.RequisitionLine{lineOf(t, 1, "7")}, Total: dec("14"), ActorID: requester}
	if _, err := s.UpdateRequisition(ctx, r.ID, r.Version+2, edit, store.Idem{}); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("stale version = %v", err)
	}
	got, err := s.UpdateRequisition(ctx, r.ID, r.Version, edit, store.Idem{Key: "e1", Hash: "h"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != r.Version+1 || len(got.Lines) != 1 || got.Lines[0].LineNo != 1 || got.Lines[0].Category != "IT" || !got.Total.Equal(dec("14")) ||
		got.Lines[0].Status != "OPEN" {
		t.Fatalf("after edit: v%d total %s lines %+v", got.Version, got.Total, got.Lines)
	}
	if n := len(got.History); n != 1 || got.History[0].Action != store.HistoryEdited || got.History[0].ActorID != requester ||
		got.History[0].FromStatus != store.RequisitionDraft || got.History[0].RequisitionVersion != got.Version {
		t.Fatalf("history = %+v", got.History)
	}
	replay, err := s.UpdateRequisition(ctx, r.ID, r.Version, edit, store.Idem{Key: "e1", Hash: "h"})
	if err != nil || replay.Version != got.Version || len(replay.History) != 1 {
		t.Fatalf("replay = v%d, %d history rows, %v; want the requisition as it is", replay.Version, len(replay.History), err)
	}
	// The operating unit alone leaves the lines untouched.
	ou := ids.New()
	headerOnly, err := s.UpdateRequisition(ctx, r.ID, got.Version, store.RequisitionEdit{OperatingUnitID: ou, ActorID: requester}, store.Idem{})
	if err != nil || headerOnly.OperatingUnitID != ou || len(headerOnly.Lines) != 1 || !headerOnly.Total.Equal(dec("14")) {
		t.Fatalf("header only = %+v, %v", headerOnly, err)
	}

	// Reject, reopen, edit again.
	sub, err := s.SubmitRequisition(ctx, r.ID, headerOnly.Version, []string{domain.RoleManager}, "builtin-1", store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateRequisition(ctx, r.ID, sub.Version, edit, store.Idem{}); problemCode(err) != store.CodeRequisitionState {
		t.Fatalf("edit while IN_REVIEW = %v", err)
	}
	rej, err := s.ActOnRequisition(ctx, r.ID, sub.Version, act("reject", ids.New(), "too dear"), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateRequisition(ctx, r.ID, rej.Version, edit, store.Idem{}); problemCode(err) != store.CodeRequisitionState {
		t.Fatalf("edit while REJECTED = %v", err)
	}
	draft, err := s.ActOnRequisition(ctx, r.ID, rej.Version, act("reopen", requester, ""), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	two := store.RequisitionEdit{Currency: "USD", Lines: []store.RequisitionLine{lineOf(t, 1, "3"), lineOf(t, 2, "4")}, Total: dec("14"), ActorID: requester}
	final, err := s.UpdateRequisition(ctx, r.ID, draft.Version, two, store.Idem{})
	if err != nil || len(final.Lines) != 2 || final.Status != store.RequisitionDraft {
		t.Fatalf("edit after reopen = %s %+v, %v", final.Status, final.Lines, err)
	}
	if last := final.History[len(final.History)-1]; last.Action != store.HistoryEdited {
		t.Fatalf("history = %+v", final.History)
	}
	if _, err := s.UpdateRequisition(ctx, ids.New(), 1, edit, store.Idem{}); problemCode(err) != store.CodeRequisitionNotFound {
		t.Fatalf("unknown requisition = %v", err)
	}
}

// BR-17.1: withdrawing takes an IN_REVIEW requisition back to DRAFT with the chain cleared and writes
// requisition.withdrawn.v1, but not once a level has approved; it can be submitted again and routes afresh.
func TestActOnRequisition_WithdrawBeforeAnyApproval(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(102)
	requester, manager := ids.New(), ids.New()

	r := inReview(t, s, ctx, requester, domain.RoleManager, domain.RoleCFO)
	out, err := s.ActOnRequisition(ctx, r.ID, r.Version, act("withdraw", requester, "wrong cost centre"), store.Idem{Key: "w1", Hash: "h"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != store.RequisitionDraft || out.ApprovedSteps != 0 || len(out.ApproverChain) != 0 || out.NextApprover != "" || out.Version != r.Version+1 {
		t.Fatalf("withdraw = %s steps %d chain %v v%d", out.Status, out.ApprovedSteps, out.ApproverChain, out.Version)
	}
	last := out.History[len(out.History)-1]
	if last.Action != store.HistoryWithdrawn || last.Comment != "wrong cost centre" || last.FromStatus != store.RequisitionInReview || last.ToStatus != store.RequisitionDraft {
		t.Fatalf("history = %+v", out.History)
	}
	if replay, err := s.ActOnRequisition(ctx, r.ID, r.Version, act("withdraw", requester, "wrong cost centre"), store.Idem{Key: "w1", Hash: "h"}); err != nil || replay.Version != out.Version {
		t.Fatalf("replay = v%d, %v", replay.Version, err)
	}
	events := 0
	for _, row := range outbox(t, ctx, pool) {
		if row.Type == purchasingevents.RequisitionWithdrawn && row.Data["requisition_id"] == r.ID && row.Data["withdrawn_by"] == requester && row.Data["reason"] == "wrong cost centre" {
			events++
		}
	}
	if events != 1 {
		t.Fatalf("requisition.withdrawn.v1 written %d times, want once", events)
	}
	if _, err := s.ActOnRequisition(ctx, r.ID, out.Version, act("withdraw", requester, ""), store.Idem{}); problemCode(err) != store.CodeRequisitionState {
		t.Fatalf("withdrawing a DRAFT = %v", err)
	}
	// Submitted again, the first level approves, and the requester can no longer withdraw.
	again, err := s.SubmitRequisition(ctx, r.ID, out.Version, []string{domain.RoleManager, domain.RoleCFO}, "builtin-1", store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	level1, err := s.ActOnRequisition(ctx, r.ID, again.Version, act("approve", manager, ""), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ActOnRequisition(ctx, r.ID, level1.Version, act("withdraw", requester, ""), store.Idem{}); problemCode(err) != store.CodeRequisitionState {
		t.Fatalf("withdraw after a level approved = %v", err)
	}
	// The round is still told by the last SUBMITTED row after a withdrawal: the manager cannot approve level 2 too.
	if _, err := s.ActOnRequisition(ctx, r.ID, level1.Version, act("approve", manager, ""), store.Idem{}); problemCode(err) != store.CodeRequisitionRepeatApprover {
		t.Fatalf("the level 1 approver at level 2 after withdraw and resubmit = %v", err)
	}
}

// BR-17.1: cancelling one OPEN line of an APPROVED requisition leaves the rest approved; a running RFQ holds the
// line, the last live line cannot go, a cancelled or unknown line is refused, the total drops to the lines left,
// and the event carries the line's amount and the new total.
func TestCancelRequisitionLine_TheRestContinues(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(103)
	in := requisition(t, ids.New(), "100", "5", "7")
	r, err := s.CreateRequisition(ctx, in, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CancelRequisitionLine(ctx, r.ID, r.Version, r.Lines[0].ID, "u", "", store.Idem{}); problemCode(err) != store.CodeRequisitionState {
		t.Fatalf("cancel a line of a DRAFT = %v", err)
	}
	if r, err = s.SubmitRequisition(ctx, r.ID, r.Version, nil, "builtin-1", store.Idem{}); err != nil || r.Status != store.RequisitionApproved {
		t.Fatalf("approve = %s, %v", r.Status, err)
	}
	actor := ids.New()
	if _, err := s.CancelRequisitionLine(ctx, r.ID, r.Version+4, r.Lines[0].ID, actor, "", store.Idem{}); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("stale version = %v", err)
	}
	if _, err := s.CancelRequisitionLine(ctx, r.ID, r.Version, ids.New(), actor, "", store.Idem{}); problemCode(err) != store.CodeRequisitionLineNotFound {
		t.Fatalf("unknown line = %v", err)
	}
	held, err := s.CreateRFQ(ctx, rfqFor(r.Lines[0]), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CancelRequisitionLine(ctx, r.ID, r.Version, r.Lines[0].ID, actor, "", store.Idem{}); problemCode(err) != store.CodeRequisitionOnRFQ {
		t.Fatalf("line on a running RFQ = %v", err)
	}
	if _, err := s.ActOnRFQ(ctx, held.ID, held.Version, domain.RFQActionCancel, store.Idem{}); err != nil {
		t.Fatal(err)
	}

	out, err := s.CancelRequisitionLine(ctx, r.ID, r.Version, r.Lines[0].ID, actor, "not needed", store.Idem{Key: "cl1", Hash: "h"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != store.RequisitionApproved || out.Version != r.Version+1 || !out.Total.Equal(dec("12")) || out.Lines[0].Status != "CANCELLED" ||
		out.Lines[1].Status != "OPEN" || out.Lines[2].Status != "OPEN" {
		t.Fatalf("after cancel: %s v%d total %s lines %+v", out.Status, out.Version, out.Total, out.Lines)
	}
	if h := out.History[len(out.History)-1]; h.Action != store.HistoryLineCancelled || h.LineID != r.Lines[0].ID || h.ActorID != actor || h.Comment != "not needed" {
		t.Fatalf("history = %+v", out.History)
	}
	if replay, err := s.CancelRequisitionLine(ctx, r.ID, r.Version, r.Lines[0].ID, actor, "not needed", store.Idem{Key: "cl1", Hash: "h"}); err != nil || replay.Version != out.Version {
		t.Fatalf("replay = v%d, %v", replay.Version, err)
	}
	if _, err := s.CancelRequisitionLine(ctx, r.ID, out.Version, r.Lines[0].ID, actor, "", store.Idem{}); problemCode(err) != store.CodeRequisitionState {
		t.Fatalf("cancelling a cancelled line = %v", err)
	}
	events := 0
	for _, row := range outbox(t, ctx, pool) {
		if row.Type == purchasingevents.RequisitionLineCancelled && row.Data["requisition_id"] == r.ID {
			events++
			if row.Data["line_id"] != r.Lines[0].ID || row.Data["amount"] != "100" || row.Data["line_no"] != float64(1) || row.Data["cancelled_by"] != actor ||
				row.Data["total"] != "12" || row.Data["requisition_status"] != store.RequisitionApproved {
				t.Fatalf("event = %+v", row.Data)
			}
		}
	}
	if events != 1 {
		t.Fatalf("requisitionline.cancelled.v1 written %d times, want once", events)
	}

	// The second goes too; the last live line stays: cancel the requisition instead.
	next, err := s.CancelRequisitionLine(ctx, r.ID, out.Version, r.Lines[1].ID, actor, "", store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CancelRequisitionLine(ctx, r.ID, next.Version, r.Lines[2].ID, actor, "", store.Idem{}); problemCode(err) != store.CodeRequisitionState {
		t.Fatalf("cancelling the last live line = %v", err)
	}
	if !next.Total.Equal(dec("7")) {
		t.Fatalf("total after two lines cancelled = %s, want 7", next.Total)
	}
	whole, err := s.ActOnRequisition(ctx, r.ID, next.Version, act("cancel", actor, ""), store.Idem{})
	if err != nil || whole.Status != store.RequisitionCancelled {
		t.Fatalf("cancel the requisition = %s, %v", whole.Status, err)
	}
	// requisition.cancelled.v1 carries what was still committed, so budget control does not release line 1 twice.
	for _, row := range outbox(t, ctx, pool) {
		if row.Type == purchasingevents.RequisitionCancelled && row.Data["requisition_id"] == r.ID && row.Data["total"] != "7" {
			t.Fatalf("requisition.cancelled.v1 total = %v, want 7", row.Data["total"])
		}
	}
	for _, l := range whole.Lines {
		if l.Status != "CANCELLED" {
			t.Fatalf("line %d is %s", l.LineNo, l.Status)
		}
	}
}

// BR-17.1, FR-17.1: when the line cancelled is the last OPEN one and the others are already consolidated, the
// requisition is CLOSED as consolidation would have closed it; a consolidated line itself cannot be cancelled.
func TestCancelRequisitionLine_ClosesWhenTheRestIsOrdered(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(31)
	r := approved(t, s, ids.New(), "10", "20")
	if _, err := s.CreatePurchaseOrder(ctx, orderFor(t, ids.New(), r.Lines[0]), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	cur, err := s.GetRequisition(ctx, r.ID)
	if err != nil || cur.Lines[0].Status != "CONSOLIDATED" || cur.Status != store.RequisitionApproved {
		t.Fatalf("after the PO: %s %+v, %v", cur.Status, cur.Lines, err)
	}
	if _, err := s.CancelRequisitionLine(ctx, r.ID, cur.Version, r.Lines[0].ID, ids.New(), "", store.Idem{}); problemCode(err) != store.CodeRequisitionState {
		t.Fatalf("cancelling an ordered line = %v", err)
	}
	out, err := s.CancelRequisitionLine(ctx, r.ID, cur.Version, r.Lines[1].ID, ids.New(), "", store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != store.RequisitionClosed || !out.Total.Equal(dec("10")) || out.Lines[1].Status != "CANCELLED" {
		t.Fatalf("after cancelling the last open line: %s total %s lines %+v", out.Status, out.Total, out.Lines)
	}
	if h := out.History[len(out.History)-1]; h.FromStatus != store.RequisitionApproved || h.ToStatus != store.RequisitionClosed {
		t.Fatalf("history = %+v", h)
	}
	found := false
	for _, row := range outbox(t, ctx, pool) {
		if row.Type == purchasingevents.RequisitionLineCancelled && row.Data["requisition_id"] == r.ID {
			found = row.Data["requisition_status"] == store.RequisitionClosed && row.Data["total"] == "10"
		}
	}
	if !found {
		t.Fatal("requisitionline.cancelled.v1 does not say the requisition closed with total 10")
	}
}

// Cancelling a line races an RFQ for it, a direct PO for it and a release run over it: each takes the requisition
// lock before the line, so exactly one of them gets the line, the losers are refused with a problem (or, for the
// release run, skip the group), and nothing deadlocks.
func TestCancelRequisitionLine_RacesTheOtherLineWriters(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(31)
	for round := 0; round < 8; round++ {
		vendor := ids.New()
		r := approvedFor(t, s, vendor, vendor, vendor)
		target := r.Lines[0]
		var wg sync.WaitGroup
		start := make(chan struct{})
		var cancelErr, rfqErr, poErr, releaseErr error
		var run store.ReleaseRun
		group := store.ReleaseInput{LegalEntityID: legalEntity, WindowDays: 7, MaxLines: 200, Considered: 1, RequestedBy: ids.New(),
			Groups: []store.ReleaseGroup{{VendorID: vendor, Lines: sourcingOf(t, s, target.ID), Orders: []store.PurchaseOrder{orderFor(t, vendor, target)}}}}
		for _, f := range []func(){
			func() {
				_, cancelErr = s.CancelRequisitionLine(ctx, r.ID, r.Version, target.ID, r.RequesterID, "", store.Idem{})
			},
			func() { _, rfqErr = s.CreateRFQ(ctx, rfqFor(target), store.Idem{}) },
			func() { _, poErr = s.CreatePurchaseOrder(ctx, orderFor(t, vendor, target), store.Idem{}) },
			func() { run, releaseErr = s.ReleaseRequisitions(ctx, group, store.Idem{}) },
		} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				f()
			}()
		}
		close(start)
		wg.Wait()
		if releaseErr != nil {
			t.Fatalf("round %d: release run = %v", round, releaseErr)
		}
		released := run.LinesReleased == 1
		winners := 0
		for _, won := range []bool{cancelErr == nil, rfqErr == nil, poErr == nil, released} {
			if won {
				winners++
			}
		}
		if winners != 1 {
			t.Fatalf("round %d: %d writers got the line (cancel %v, rfq %v, po %v, released %v); want exactly one", round, winners,
				cancelErr, rfqErr, poErr, released)
		}
		for name, err := range map[string]error{"cancel": cancelErr, "rfq": rfqErr, "po": poErr} {
			if err != nil && problemCode(err) == "" {
				t.Fatalf("round %d: %s failed with %v, want a problem", round, name, err)
			}
		}
		status := lineStatus(t, s, target.ID)
		if (cancelErr == nil) != (status == "CANCELLED") || ((poErr == nil || released) && status != "CONSOLIDATED") || (rfqErr == nil && status != "OPEN") {
			t.Fatalf("round %d: line is %s (cancel %v, rfq %v, po %v, released %v)", round, status, cancelErr, rfqErr, poErr, released)
		}
	}
}
