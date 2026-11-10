package store_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	purchasingevents "github.com/BenjaminSRussell/passage/services/purchasing/internal/events"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

func accept(version int) domain.AckInput {
	return domain.AckInput{VersionNo: version, Outcome: domain.AckAccepted}
}

func proposeDate(t *testing.T, version int, date string) domain.AckInput {
	d := day(t, date)
	return domain.AckInput{VersionNo: version, Outcome: domain.AckChangesProposed, Reason: "mill delay", Changes: []domain.LineChange{{LineNo: 1, DeliveryDate: &d}}}
}

func countTypes(rows []outboxRow, typ string) int {
	n := 0
	for _, r := range rows {
		if r.Type == typ {
			n++
		}
	}
	return n
}

func TestPOAck_VendorScopeAndLegalEntityIsolation(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(61)
	vendorA, vendorB := ids.New(), ids.New()
	po := confirmedPO(t, ctx, s, lifecyclePO(t, vendorA, "100", "2027-01-05"))

	if _, err := s.AcknowledgePurchaseOrder(ctx, vendorB, po.ID, accept(1), store.Idem{}); problemCode(err) != store.CodePurchaseOrderNotFound {
		t.Fatalf("vendor B acknowledging vendor A's order = %v", err)
	}
	if _, err := s.GetVendorPurchaseOrder(ctx, vendorB, po.ID); problemCode(err) != store.CodePurchaseOrderNotFound {
		t.Fatalf("vendor B reading vendor A's order = %v", err)
	}
	pageB, err := s.ListVendorPurchaseOrders(ctx, vendorB, "", 50)
	if err != nil || len(pageB.Items) != 0 {
		t.Fatalf("vendor B list = %+v, %v", pageB, err)
	}
	pageA, err := s.ListVendorPurchaseOrders(ctx, vendorA, "", 50)
	if err != nil || len(pageA.Items) != 1 || pageA.Items[0].AckState != "NONE" {
		t.Fatalf("vendor A list = %+v, %v", pageA, err)
	}
	if got, err := s.GetVendorPurchaseOrder(ctx, vendorA, po.ID); err != nil || got.ID != po.ID {
		t.Fatalf("vendor A read = %+v, %v", got, err)
	}

	draft, err := s.CreatePurchaseOrder(ctx, lifecyclePO(t, vendorA, "5", "2027-01-05"), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetVendorPurchaseOrder(ctx, vendorA, draft.ID); problemCode(err) != store.CodePurchaseOrderNotFound {
		t.Fatalf("vendor reading a draft = %v", err)
	}
	if _, err := s.AcknowledgePurchaseOrder(ctx, vendorA, draft.ID, accept(1), store.Idem{}); problemCode(err) != store.CodePurchaseOrderNotFound {
		t.Fatalf("vendor acknowledging a draft = %v", err)
	}

	other := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(61), LegalEntityID: ids.New(), UserID: ids.New()})
	if _, err := s.GetVendorPurchaseOrder(other, vendorA, po.ID); problemCode(err) != store.CodePurchaseOrderNotFound {
		t.Fatalf("another legal entity reading the order = %v", err)
	}
	if _, err := s.AcknowledgePurchaseOrder(other, vendorA, po.ID, accept(1), store.Idem{}); problemCode(err) != store.CodePurchaseOrderNotFound {
		t.Fatalf("another legal entity acknowledging the order = %v", err)
	}
	if page, err := s.ListVendorPurchaseOrders(other, vendorA, "", 50); err != nil || len(page.Items) != 0 {
		t.Fatalf("another legal entity list = %+v, %v", page, err)
	}

	ack, err := s.AcknowledgePurchaseOrder(ctx, vendorA, po.ID, accept(1), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetAcknowledgement(other, ack.ID); problemCode(err) != store.CodeAckNotFound {
		t.Fatalf("another legal entity reading the answer = %v", err)
	}
	if page, err := s.ListAcknowledgements(other, store.AckFilter{}, "", 50); err != nil || len(page.Items) != 0 {
		t.Fatalf("another legal entity answers = %+v, %v", page, err)
	}
	if page, err := s.ListAwaitingAcknowledgement(other, "", 50); err != nil || len(page.Items) != 0 {
		t.Fatalf("another legal entity awaiting = %+v, %v", page, err)
	}
}

func TestPOAck_AcceptRejectStaleReplayAndEvents(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(62)
	vendor := ids.New()
	po := confirmedPO(t, ctx, s, lifecyclePO(t, vendor, "100", "2027-01-05"))

	awaiting, err := s.ListAwaitingAcknowledgement(ctx, "", 50)
	if err != nil || len(awaiting.Items) != 1 || awaiting.Items[0].ID != po.ID {
		t.Fatalf("awaiting = %+v, %v", awaiting, err)
	}
	if _, err := s.AcknowledgePurchaseOrder(ctx, vendor, po.ID, accept(2), store.Idem{}); problemCode(err) != store.CodeAckStale {
		t.Fatalf("answer to a version that is not the latest = %v", err)
	}

	key := store.Idem{Key: "k1", Hash: "h1"}
	first, err := s.AcknowledgePurchaseOrder(ctx, vendor, po.ID, accept(1), key)
	if err != nil || first.Outcome != domain.AckAccepted || first.VendorID != vendor || first.VersionNo != 1 || first.PONumber == "" {
		t.Fatalf("accept = %+v, %v", first, err)
	}
	replay, err := s.AcknowledgePurchaseOrder(ctx, vendor, po.ID, accept(1), key)
	if err != nil || replay.ID != first.ID {
		t.Fatalf("replay = %+v, %v; want the same answer", replay, err)
	}
	if _, err := s.AcknowledgePurchaseOrder(ctx, vendor, po.ID, accept(1), store.Idem{Key: "k1", Hash: "other"}); err == nil {
		t.Fatal("a key reused with a different body was accepted")
	}
	if _, err := s.AcknowledgePurchaseOrder(ctx, vendor, po.ID, domain.AckInput{VersionNo: 1, Outcome: domain.AckRejected, Reason: "no"}, store.Idem{Key: "k2", Hash: "h2"}); problemCode(err) != store.CodeAckAnswered {
		t.Fatalf("second answer to the same version = %v", err)
	}

	rows := outbox(t, ctx, pool)
	if countTypes(rows, purchasingevents.POAckAccepted) != 1 {
		t.Fatalf("outbox = %v, want exactly one accepted event", eventTypes(rows))
	}
	last := rows[len(rows)-1]
	if last.Type != purchasingevents.POAckAccepted || last.Data["ack_id"] != first.ID || last.Data["vendor_id"] != vendor || last.Data["version_no"] != float64(1) {
		t.Fatalf("accepted event = %+v", last)
	}

	left, err := s.ListAwaitingAcknowledgement(ctx, "", 50)
	if err != nil || len(left.Items) != 0 {
		t.Fatalf("awaiting after the answer = %+v, %v", left, err)
	}

	second := confirmedPO(t, ctx, s, lifecyclePO(t, vendor, "10", "2027-01-05"))
	rejected, err := s.AcknowledgePurchaseOrder(ctx, vendor, second.ID, domain.AckInput{VersionNo: 1, Outcome: domain.AckRejected, Reason: "cannot supply"}, store.Idem{})
	if err != nil || rejected.Outcome != domain.AckRejected || rejected.Reason != "cannot supply" {
		t.Fatalf("reject = %+v, %v", rejected, err)
	}
	if countTypes(outbox(t, ctx, pool), purchasingevents.POAckRejected) != 1 {
		t.Fatal("no rejected event")
	}
	got, err := s.ListAcknowledgements(ctx, store.AckFilter{Outcome: domain.AckRejected}, "", 50)
	if err != nil || len(got.Items) != 1 || got.Items[0].ID != rejected.ID {
		t.Fatalf("list by outcome = %+v, %v", got, err)
	}
}

func TestPOAck_ProposalApproveDeclineAndOneOpen(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(63)
	vendor := ids.New()
	po := confirmedPO(t, ctx, s, lifecyclePO(t, vendor, "100", "2027-01-05"))

	if _, err := s.AcknowledgePurchaseOrder(ctx, vendor, po.ID, proposeDate(t, 1, "2027-01-05"), store.Idem{}); problemCode(err) != domain.CodeValidation {
		t.Fatalf("a proposal equal to the order = %v", err)
	}
	prop, err := s.AcknowledgePurchaseOrder(ctx, vendor, po.ID, proposeDate(t, 1, "2027-01-06"), store.Idem{})
	if err != nil || prop.Outcome != domain.AckChangesProposed || len(prop.Changes) != 1 || prop.Decision != "" {
		t.Fatalf("proposal = %+v, %v", prop, err)
	}
	if _, err := s.AcknowledgePurchaseOrder(ctx, vendor, po.ID, proposeDate(t, 1, "2027-01-07"), store.Idem{}); problemCode(err) != store.CodeAckOpenProposal {
		t.Fatalf("second open proposal = %v", err)
	}
	if _, err := s.AcknowledgePurchaseOrder(ctx, vendor, po.ID, accept(1), store.Idem{}); problemCode(err) != store.CodeAckOpenProposal {
		t.Fatalf("accept while a proposal is open = %v", err)
	}
	pending, err := s.ListAcknowledgements(ctx, store.AckFilter{OpenProposals: true}, "", 50)
	if err != nil || len(pending.Items) != 1 || pending.Items[0].ID != prop.ID {
		t.Fatalf("pending = %+v, %v", pending, err)
	}

	declined, err := s.DecideAcknowledgement(ctx, prop.ID, false, "no", store.Idem{})
	if err != nil || declined.Decision != domain.AckDeclined || declined.DecisionNote != "no" || declined.ResultingVersionNo != 0 {
		t.Fatalf("decline = %+v, %v", declined, err)
	}
	unchanged, err := s.GetPurchaseOrder(ctx, po.ID)
	if err != nil || unchanged.VersionNo != 1 || unchanged.Lines[0].DeliveryDate.String() != "2027-01-05" {
		t.Fatalf("order after decline = v%d %v, %v", unchanged.VersionNo, unchanged.Lines[0].DeliveryDate, err)
	}
	if _, err := s.DecideAcknowledgement(ctx, prop.ID, true, "", store.Idem{}); problemCode(err) != store.CodeAckDecided {
		t.Fatalf("deciding twice = %v", err)
	}
	if _, err := s.DecideAcknowledgement(ctx, ids.New(), true, "", store.Idem{}); problemCode(err) != store.CodeAckNotFound {
		t.Fatalf("unknown proposal = %v", err)
	}

	again, err := s.AcknowledgePurchaseOrder(ctx, vendor, po.ID, proposeDate(t, 1, "2027-01-06"), store.Idem{})
	if err != nil {
		t.Fatalf("proposal after a decline = %v", err)
	}
	key := store.Idem{Key: "approve-1", Hash: "h"}
	approved, err := s.DecideAcknowledgement(ctx, again.ID, true, "ok", key)
	if err != nil || approved.Decision != domain.AckApproved || approved.ResultingVersionNo != 2 {
		t.Fatalf("approve = %+v, %v", approved, err)
	}
	replay, err := s.DecideAcknowledgement(ctx, again.ID, true, "ok", key)
	if err != nil || replay.ID != approved.ID || replay.ResultingVersionNo != 2 {
		t.Fatalf("approve replay = %+v, %v", replay, err)
	}
	next, err := s.GetPurchaseOrder(ctx, po.ID)
	if err != nil || next.VersionNo != 2 || next.ApprovalStatus != domain.POStatusConfirmed || next.Lines[0].DeliveryDate.String() != "2027-01-06" {
		t.Fatalf("order after approve = v%d %s %v, %v", next.VersionNo, next.ApprovalStatus, next.Lines[0].DeliveryDate, err)
	}

	st, err := s.ListAwaitingAcknowledgement(ctx, "", 50)
	if err != nil || len(st.Items) != 1 || st.Items[0].VersionNo != 2 {
		t.Fatalf("version 2 should await an answer = %+v, %v", st, err)
	}
	if _, err := s.AcknowledgePurchaseOrder(ctx, vendor, po.ID, accept(1), store.Idem{}); problemCode(err) != store.CodeAckStale {
		t.Fatalf("answer to version 1 after version 2 = %v", err)
	}

	rows := outbox(t, ctx, pool)
	if countTypes(rows, purchasingevents.POAckProposed) != 2 || countTypes(rows, purchasingevents.POAckDecided) != 2 || countTypes(rows, purchasingevents.POVersionCreated) != 1 {
		t.Fatalf("outbox = %v", eventTypes(rows))
	}
}

func TestPOAck_ApprovedQuantityChangeWaitsForReapproval(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(64)
	vendor := ids.New()
	po := confirmedPO(t, ctx, s, lifecyclePO(t, vendor, "100", "2027-01-05"))
	qty, price := dec("80"), dec("12.50")
	prop, err := s.AcknowledgePurchaseOrder(ctx, vendor, po.ID, domain.AckInput{VersionNo: 1, Outcome: domain.AckChangesProposed,
		Changes: []domain.LineChange{{LineNo: 1, Qty: &qty, UnitPrice: &price}}}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	approved, err := s.DecideAcknowledgement(ctx, prop.ID, true, "", store.Idem{})
	if err != nil || approved.ResultingVersionNo != 2 {
		t.Fatalf("approve = %+v, %v", approved, err)
	}
	got, err := s.GetPurchaseOrder(ctx, po.ID)
	if err != nil || got.ApprovalStatus != domain.POStatusInReview || !got.Lines[0].Qty.Equal(qty) || !got.Lines[0].UnitPrice.Equal(price) || !got.Total.Equal(dec("1000")) {
		t.Fatalf("order after a quantity and price change = %s qty %s price %s total %s, %v", got.ApprovalStatus, got.Lines[0].Qty, got.Lines[0].UnitPrice, got.Total, err)
	}
	if _, err := s.AcknowledgePurchaseOrder(ctx, vendor, po.ID, accept(1), store.Idem{}); problemCode(err) != store.CodeAckState {
		t.Fatalf("answer while the buyer's change is in review = %v", err)
	}
}

func TestPOAck_TableRowLevelSecurityIsForced(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(65)
	vendor := ids.New()
	po := confirmedPO(t, ctx, s, lifecyclePO(t, vendor, "100", "2027-01-05"))
	if _, err := s.AcknowledgePurchaseOrder(ctx, vendor, po.ID, accept(1), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	otherTenant := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(66), UserID: ids.New(), Duties: map[string]struct{}{tenant.DutyAllScope: {}}})
	var seen int
	err := dbx.WithTenantTx(otherTenant, pool, func(tx pgx.Tx) error {
		return tx.QueryRow(otherTenant, `select count(*) from purchase_order_ack`).Scan(&seen)
	})
	if err != nil || seen != 0 {
		t.Fatalf("another tenant sees %d answers, %v", seen, err)
	}
	var forced bool
	err = dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select relrowsecurity and relforcerowsecurity from pg_class where relname = 'purchase_order_ack'`).Scan(&forced)
	})
	if err != nil || !forced {
		t.Fatalf("purchase_order_ack row security forced = %v, %v", forced, err)
	}
}
