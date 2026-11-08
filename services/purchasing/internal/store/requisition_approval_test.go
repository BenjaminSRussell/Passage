package store_test

import (
	"context"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	purchasingevents "github.com/BenjaminSRussell/passage/services/purchasing/internal/events"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

func inReview(t *testing.T, s *store.Postgres, ctx context.Context, requester string, chain ...string) store.Requisition {
	t.Helper()
	r, err := s.CreateRequisition(ctx, requisition(t, requester, "4000", "3000"), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if r, err = s.SubmitRequisition(ctx, r.ID, 1, chain, "builtin-1", store.Idem{}); err != nil || r.Status != store.RequisitionInReview {
		t.Fatalf("submit = %s, %v", r.Status, err)
	}
	return r
}

func act(action, actor, comment string) store.RequisitionAct {
	return store.RequisitionAct{Action: action, ActorID: actor, Comment: comment}
}

// BR-17.1: each level of the chain approves in turn; the last one makes the requisition APPROVED and writes
// requisition.approved.v1; the requester and an earlier approver cannot take a later level; every step is in the
// history; a replay returns the requisition as it is.
func TestActOnRequisition_ApprovalWalksTheChain(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(41)
	requester, manager, owner := ids.New(), ids.New(), ids.New()
	r := inReview(t, s, ctx, requester, domain.RoleManager, domain.RoleCategoryOwner)
	if r.NextApprover != domain.RoleManager || r.ApprovedSteps != 0 || len(r.History) != 1 || r.History[0].Action != store.HistorySubmitted {
		t.Fatalf("after submit: next %q steps %d history %+v", r.NextApprover, r.ApprovedSteps, r.History)
	}

	if _, err := s.ActOnRequisition(ctx, r.ID, r.Version, act("approve", requester, ""), store.Idem{}); problemCode(err) != store.CodeRequisitionSelfApproval {
		t.Fatalf("requester approving = %v", err)
	}
	if _, err := s.ActOnRequisition(ctx, r.ID, r.Version+3, act("approve", manager, ""), store.Idem{}); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("stale version = %v", err)
	}
	first, err := s.ActOnRequisition(ctx, r.ID, r.Version, act("approve", manager, "within budget"), store.Idem{Key: "a1", Hash: "h"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != store.RequisitionInReview || first.ApprovedSteps != 1 || first.NextApprover != domain.RoleCategoryOwner || first.Version != r.Version+1 {
		t.Fatalf("after level 1: %s steps %d next %q version %d", first.Status, first.ApprovedSteps, first.NextApprover, first.Version)
	}
	replay, err := s.ActOnRequisition(ctx, r.ID, r.Version, act("approve", manager, "within budget"), store.Idem{Key: "a1", Hash: "h"})
	if err != nil || replay.Version != first.Version || replay.ApprovedSteps != 1 {
		t.Fatalf("replay = v%d steps %d, %v; want the requisition as it is (v%d)", replay.Version, replay.ApprovedSteps, err, first.Version)
	}
	if _, err := s.ActOnRequisition(ctx, r.ID, first.Version, act("approve", manager, ""), store.Idem{}); problemCode(err) != store.CodeRequisitionRepeatApprover {
		t.Fatalf("same approver twice = %v", err)
	}
	for _, status := range []string{"cancel", "reopen"} {
		if _, err := s.ActOnRequisition(ctx, r.ID, first.Version, act(status, requester, ""), store.Idem{}); problemCode(err) != store.CodeRequisitionState {
			t.Fatalf("%s while IN_REVIEW = %v", status, err)
		}
	}
	done, err := s.ActOnRequisition(ctx, r.ID, first.Version, act("approve", owner, ""), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != store.RequisitionApproved || done.ApprovedSteps != 2 || done.NextApprover != "" {
		t.Fatalf("after level 2: %s steps %d next %q", done.Status, done.ApprovedSteps, done.NextApprover)
	}
	if _, err := s.ActOnRequisition(ctx, r.ID, done.Version, act("approve", ids.New(), ""), store.Idem{}); problemCode(err) != store.CodeRequisitionState {
		t.Fatalf("approving an APPROVED requisition = %v", err)
	}
	wantTrail := []struct {
		action string
		step   int
		role   string
	}{{store.HistorySubmitted, 0, ""}, {store.HistoryApproved, 1, domain.RoleManager}, {store.HistoryApproved, 2, domain.RoleCategoryOwner}}
	if len(done.History) != len(wantTrail) {
		t.Fatalf("history = %+v", done.History)
	}
	for i, w := range wantTrail {
		h := done.History[i]
		if h.Action != w.action || h.StepNo != w.step || h.Role != w.role {
			t.Errorf("history[%d] = %+v, want %+v", i, h, w)
		}
	}
	if done.History[1].Comment != "within budget" || done.History[1].ActorID != manager || done.History[2].ToStatus != store.RequisitionApproved {
		t.Errorf("history rows = %+v", done.History)
	}

	approvedEvents := 0
	for _, row := range outbox(t, ctx, pool) {
		if row.Type == purchasingevents.RequisitionApproved && row.Data["requisition_id"] == r.ID {
			approvedEvents++
		}
	}
	if approvedEvents != 1 {
		t.Fatalf("requisition.approved.v1 written %d times, want once (on the last level)", approvedEvents)
	}
}

// BR-17.1: a rejection stops the chain with a reason and requisition.rejected.v1; the requester reopens it to DRAFT
// with a clean chain and submits again, and an approver of the first round may approve the second.
func TestActOnRequisition_RejectReopenAndSubmitAgain(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(42)
	requester, manager := ids.New(), ids.New()
	r := inReview(t, s, ctx, requester, domain.RoleManager)

	rej, err := s.ActOnRequisition(ctx, r.ID, r.Version, act("reject", manager, "Use the framework agreement"), store.Idem{})
	if err != nil || rej.Status != store.RequisitionRejected {
		t.Fatalf("reject = %s, %v", rej.Status, err)
	}
	if _, err := s.ActOnRequisition(ctx, r.ID, rej.Version, act("approve", manager, ""), store.Idem{}); problemCode(err) != store.CodeRequisitionState {
		t.Fatalf("approving a REJECTED requisition = %v", err)
	}
	draft, err := s.ActOnRequisition(ctx, r.ID, rej.Version, act("reopen", requester, ""), store.Idem{})
	if err != nil || draft.Status != store.RequisitionDraft || draft.ApprovedSteps != 0 || len(draft.ApproverChain) != 0 {
		t.Fatalf("reopen = %s steps %d chain %v, %v", draft.Status, draft.ApprovedSteps, draft.ApproverChain, err)
	}
	again, err := s.SubmitRequisition(ctx, draft.ID, draft.Version, []string{domain.RoleManager}, "builtin-1", store.Idem{})
	if err != nil || again.Status != store.RequisitionInReview {
		t.Fatalf("submit again = %s, %v", again.Status, err)
	}
	if done, err := s.ActOnRequisition(ctx, r.ID, again.Version, act("approve", manager, ""), store.Idem{}); err != nil || done.Status != store.RequisitionApproved {
		t.Fatalf("approval in the second round = %v, %v", done.Status, err)
	}

	var actions []string
	got, _ := s.GetRequisition(ctx, r.ID)
	for _, h := range got.History {
		actions = append(actions, h.Action)
	}
	if want := "SUBMITTED REJECTED REOPENED SUBMITTED APPROVED"; join(actions) != want {
		t.Fatalf("history = %s, want %s", join(actions), want)
	}
	if got.History[1].Comment != "Use the framework agreement" || got.History[1].Role != domain.RoleManager || got.History[1].StepNo != 1 {
		t.Errorf("rejection row = %+v", got.History[1])
	}
	rejected := 0
	for _, row := range outbox(t, ctx, pool) {
		if row.Type == purchasingevents.RequisitionRejected && row.Data["requisition_id"] == r.ID {
			rejected++
			if row.Data["reason"] != "Use the framework agreement" || row.Data["rejected_by"] != manager || row.Data["role"] != domain.RoleManager {
				t.Errorf("requisition.rejected.v1 = %v", row.Data)
			}
		}
	}
	if rejected != 1 {
		t.Fatalf("requisition.rejected.v1 written %d times, want once", rejected)
	}
}

// PRC-REQ-010 is per round: an approval from before a rejection does not count against the approver once the
// requisition is resubmitted, but within the new round they still cannot take two levels.
func TestActOnRequisition_RepeatApproverIsPerRound(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(48)
	requester, manager, cfo := ids.New(), ids.New(), ids.New()
	r := inReview(t, s, ctx, requester, domain.RoleManager, domain.RoleCFO)
	first, err := s.ActOnRequisition(ctx, r.ID, r.Version, act("approve", manager, ""), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	rej, err := s.ActOnRequisition(ctx, r.ID, first.Version, act("reject", cfo, "Split it by quarter"), store.Idem{})
	if err != nil || rej.Status != store.RequisitionRejected || rej.History[len(rej.History)-1].StepNo != 2 {
		t.Fatalf("reject at level 2 = %s, %v", rej.Status, err)
	}
	draft, err := s.ActOnRequisition(ctx, r.ID, rej.Version, act("reopen", requester, ""), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.SubmitRequisition(ctx, r.ID, draft.Version, []string{domain.RoleManager, domain.RoleCFO}, "builtin-1", store.Idem{})
	if err != nil || again.ApprovedSteps != 0 || again.NextApprover != domain.RoleManager {
		t.Fatalf("submit again = steps %d next %q, %v", again.ApprovedSteps, again.NextApprover, err)
	}
	level1, err := s.ActOnRequisition(ctx, r.ID, again.Version, act("approve", manager, ""), store.Idem{})
	if err != nil || level1.ApprovedSteps != 1 {
		t.Fatalf("the first round's approver at level 1 of the second round = steps %d, %v", level1.ApprovedSteps, err)
	}
	if _, err := s.ActOnRequisition(ctx, r.ID, level1.Version, act("approve", manager, ""), store.Idem{}); problemCode(err) != store.CodeRequisitionRepeatApprover {
		t.Fatalf("same approver at level 2 of the second round = %v", err)
	}
	if done, err := s.ActOnRequisition(ctx, r.ID, level1.Version, act("approve", cfo, ""), store.Idem{}); err != nil || done.Status != store.RequisitionApproved {
		t.Fatalf("another approver at level 2 = %v, %v", done.Status, err)
	}
}

func join(s []string) string {
	out := ""
	for i, v := range s {
		if i > 0 {
			out += " "
		}
		out += v
	}
	return out
}

// BR-17.1: cancelling an APPROVED requisition cancels its OPEN lines and says so in requisition.cancelled.v1; it is
// refused while a running RFQ holds a line and once a line is ordered; the queue no longer shows cancelled lines.
func TestActOnRequisition_CancelApprovedRequisition(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(31) // the tenant the shared requisition helpers use
	requester := ids.New()

	held := approved(t, s, requester, "10", "20")
	rfq, err := s.CreateRFQ(ctx, rfqFor(held.Lines[0]), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ActOnRequisition(ctx, held.ID, held.Version, act("cancel", requester, ""), store.Idem{}); problemCode(err) != store.CodeRequisitionOnRFQ {
		t.Fatalf("cancel with a line on a running RFQ = %v", err)
	}
	if _, err := s.ActOnRFQ(ctx, rfq.ID, rfq.Version, domain.RFQActionCancel, store.Idem{}); err != nil {
		t.Fatal(err)
	}

	ordered := approved(t, s, requester, "10", "20")
	if _, err := s.CreatePurchaseOrder(ctx, orderFor(t, ids.New(), ordered.Lines[0]), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	ordered, _ = s.GetRequisition(ctx, ordered.ID)
	if _, err := s.ActOnRequisition(ctx, ordered.ID, ordered.Version, act("cancel", requester, ""), store.Idem{}); problemCode(err) != store.CodeRequisitionState {
		t.Fatalf("cancel with an ordered line = %v", err)
	}

	held, _ = s.GetRequisition(ctx, held.ID)
	out, err := s.ActOnRequisition(ctx, held.ID, held.Version, act("cancel", requester, "Not needed any more"), store.Idem{Key: "c1", Hash: "h"})
	if err != nil || out.Status != store.RequisitionCancelled {
		t.Fatalf("cancel = %s, %v", out.Status, err)
	}
	for _, l := range out.Lines {
		if l.Status != "CANCELLED" {
			t.Errorf("line %d is %s, want CANCELLED", l.LineNo, l.Status)
		}
	}
	if last := out.History[len(out.History)-1]; last.Action != store.HistoryCancelled || last.FromStatus != store.RequisitionApproved || last.Comment != "Not needed any more" {
		t.Errorf("last history row = %+v", last)
	}
	queue, err := s.ListOpenRequisitionLines(ctx, store.SourcingFilter{}, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range queue.Items {
		if l.RequisitionID == held.ID {
			t.Errorf("cancelled line %d is still in the consolidation queue", l.LineNo)
		}
	}
	if _, err := s.ActOnRequisition(ctx, held.ID, out.Version, act("cancel", requester, ""), store.Idem{}); problemCode(err) != store.CodeRequisitionState {
		t.Fatalf("cancelling twice = %v", err)
	}
	cancelled := 0
	for _, row := range outbox(t, ctx, pool) {
		if row.Type == purchasingevents.RequisitionCancelled && row.Data["requisition_id"] == held.ID {
			cancelled++
			lines, _ := row.Data["cancelled_line_ids"].([]any)
			if len(lines) != 2 || row.Data["previous_status"] != "APPROVED" || row.Data["reason"] != "Not needed any more" {
				t.Errorf("requisition.cancelled.v1 = %v", row.Data)
			}
		}
	}
	if cancelled != 1 {
		t.Fatalf("requisition.cancelled.v1 written %d times, want once", cancelled)
	}
}

// Cancelling an APPROVED requisition and raising an RFQ for its lines take the requisition lock first, in the order
// every writer of requisition lines uses, so when they race exactly one wins: never a CANCELLED line on a running RFQ,
// never a deadlock.
func TestActOnRequisition_CancelRacesAnRFQ(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(31)
	requester := ids.New()
	for i := 0; i < 8; i++ {
		r := approved(t, s, requester, "10", "20")
		errs := make(chan error, 2)
		start := make(chan struct{})
		go func() {
			<-start
			_, err := s.ActOnRequisition(ctx, r.ID, r.Version, act("cancel", requester, ""), store.Idem{})
			errs <- err
		}()
		go func() {
			<-start
			_, err := s.CreateRFQ(ctx, rfqFor(r.Lines...), store.Idem{})
			errs <- err
		}()
		close(start)
		cancelErr, rfqErr := <-errs, <-errs
		if (cancelErr == nil) == (rfqErr == nil) {
			t.Fatalf("round %d: cancel and RFQ both %v / %v; want exactly one to win", i, cancelErr, rfqErr)
		}
		for _, err := range []error{cancelErr, rfqErr} {
			if err != nil && problemCode(err) == "" {
				t.Fatalf("round %d: the loser failed with %v, want a problem (state or RFQ hold)", i, err)
			}
		}
		got, _ := s.GetRequisition(ctx, r.ID)
		for _, l := range got.Lines {
			if l.Status == "CANCELLED" && got.Status != store.RequisitionCancelled {
				t.Fatalf("round %d: line %d cancelled on a %s requisition", i, l.LineNo, got.Status)
			}
		}
	}
}

// A DRAFT can be cancelled by its requester, and a REJECTED one.
func TestActOnRequisition_CancelDraftAndRejected(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(44)
	requester := ids.New()
	d, err := s.CreateRequisition(ctx, requisition(t, requester, "10"), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	out, err := s.ActOnRequisition(ctx, d.ID, d.Version, act("cancel", requester, ""), store.Idem{})
	if err != nil || out.Status != store.RequisitionCancelled || out.Lines[0].Status != "CANCELLED" {
		t.Fatalf("cancel draft = %s, %v", out.Status, err)
	}
	r := inReview(t, s, ctx, requester, domain.RoleManager)
	rej, err := s.ActOnRequisition(ctx, r.ID, r.Version, act("reject", ids.New(), "no"), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if out, err = s.ActOnRequisition(ctx, r.ID, rej.Version, act("cancel", requester, ""), store.Idem{}); err != nil || out.Status != store.RequisitionCancelled {
		t.Fatalf("cancel rejected = %s, %v", out.Status, err)
	}
}

// The approver's inbox lists IN_REVIEW requisitions with the role that decides next, filters by role and legal
// entity, hides the caller's own, pages by cursor and moves a requisition on as its levels approve.
func TestListRequisitionApprovals_InboxFollowsTheChain(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(45)
	requester, other, approver := ids.New(), ids.New(), ids.New()
	a := inReview(t, s, ctx, requester, domain.RoleManager, domain.RoleCFO)
	b := inReview(t, s, ctx, other, domain.RoleManager)
	_ = inReview(t, s, ctx, other, domain.RoleCFO)
	if _, err := s.CreateRequisition(ctx, requisition(t, other, "5"), store.Idem{}); err != nil { // DRAFT: not listed
		t.Fatal(err)
	}

	all, err := s.ListRequisitionApprovals(ctx, store.ApprovalQueueFilter{}, "", 0)
	if err != nil || len(all.Items) != 3 {
		t.Fatalf("inbox = %d, %v; want the 3 IN_REVIEW requisitions", len(all.Items), err)
	}
	if it := all.Items[2]; it.ID != a.ID || it.NextApprover != domain.RoleManager || it.ChainLength != 2 || it.ApprovedSteps != 0 || !it.Total.Equal(dec("7000")) || it.LineCount != 2 {
		t.Fatalf("item = %+v", it)
	}
	if got, _ := s.ListRequisitionApprovals(ctx, store.ApprovalQueueFilter{Role: domain.RoleManager}, "", 0); len(got.Items) != 2 {
		t.Fatalf("MANAGER inbox = %d, want 2", len(got.Items))
	}
	if got, _ := s.ListRequisitionApprovals(ctx, store.ApprovalQueueFilter{ExcludeRequesterID: other}, "", 0); len(got.Items) != 1 || got.Items[0].ID != a.ID {
		t.Fatalf("inbox without the caller's own = %+v", got.Items)
	}
	if got, _ := s.ListRequisitionApprovals(ctx, store.ApprovalQueueFilter{LegalEntityID: ids.New()}, "", 0); len(got.Items) != 0 {
		t.Fatalf("other legal entity = %d", len(got.Items))
	}
	page, err := s.ListRequisitionApprovals(ctx, store.ApprovalQueueFilter{}, "", 2)
	if err != nil || len(page.Items) != 2 || page.Next == "" {
		t.Fatalf("first page = %d next %q, %v", len(page.Items), page.Next, err)
	}
	if rest, _ := s.ListRequisitionApprovals(ctx, store.ApprovalQueueFilter{}, page.Next, 2); len(rest.Items) != 1 {
		t.Fatalf("second page = %d, want 1", len(rest.Items))
	}

	if _, err := s.ActOnRequisition(ctx, a.ID, a.Version, act("approve", approver, ""), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ActOnRequisition(ctx, b.ID, b.Version, act("approve", approver, ""), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	cfo, _ := s.ListRequisitionApprovals(ctx, store.ApprovalQueueFilter{Role: domain.RoleCFO}, "", 0)
	if len(cfo.Items) != 2 {
		t.Fatalf("CFO inbox = %d, want the one moved up and the one that started there", len(cfo.Items))
	}
	if left, _ := s.ListRequisitionApprovals(ctx, store.ApprovalQueueFilter{}, "", 0); len(left.Items) != 2 {
		t.Fatalf("inbox after b was approved = %d, want 2", len(left.Items))
	}
}

// RLS: the history and the inbox follow the tenant and the legal entity scope.
func TestRequisitionApproval_IsolatedByTenantAndLegalEntity(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctxA := principalContext(46)
	ctxB := principalContext(47)
	r := inReview(t, s, ctxA, ids.New(), domain.RoleManager)
	if _, err := s.ActOnRequisition(ctxB, r.ID, r.Version, act("approve", ids.New(), ""), store.Idem{}); problemCode(err) != store.CodeRequisitionNotFound {
		t.Fatalf("other tenant approving = %v", err)
	}
	if got, _ := s.ListRequisitionApprovals(ctxB, store.ApprovalQueueFilter{}, "", 0); len(got.Items) != 0 {
		t.Fatalf("other tenant sees %d requisitions", len(got.Items))
	}
	otherEntity := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(46), LegalEntityID: ids.New(), UserID: ids.New()})
	if got, _ := s.ListRequisitionApprovals(otherEntity, store.ApprovalQueueFilter{}, "", 0); len(got.Items) != 0 {
		t.Fatalf("another legal entity sees %d requisitions", len(got.Items))
	}
	if _, err := s.ActOnRequisition(otherEntity, r.ID, r.Version, act("approve", ids.New(), ""), store.Idem{}); problemCode(err) != store.CodeRequisitionNotFound {
		t.Fatalf("another legal entity approving = %v", err)
	}
}
