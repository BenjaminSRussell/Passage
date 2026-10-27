package store_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	purchasingevents "github.com/BenjaminSRussell/passage/services/purchasing/internal/events"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

// lifecyclePO is a one-line PO of qty EA at 10.00 delivering on date.
func lifecyclePO(t *testing.T, vendor, qty, date string) store.PurchaseOrder {
	amt := dec(qty).Mul(dec("10"))
	return store.PurchaseOrder{LegalEntityID: legalEntity, VendorID: vendor, Currency: "USD", ChangeManagement: true,
		Subtotal: amt, DiscountTotal: decimal.Zero, ChargesTotal: decimal.Zero, TaxTotal: decimal.Zero, Total: amt,
		Lines: []store.PurchaseOrderLine{{LineNo: 1, Description: "bolt", Qty: dec(qty), Unit: "EA", UnitPrice: dec("10"),
			PriceUnit: dec("1"), DiscountPct: decimal.Zero, GrossAmount: amt, DiscountAmount: decimal.Zero, LineAmount: amt,
			TaxAmount: decimal.Zero, DeliveryDate: day(t, date), PriceSource: "MANUAL"}}}
}

// edited is next as the API hands it to EditPurchaseOrder: the stored PO's
// parties with new lines.
func edited(cur, next store.PurchaseOrder) store.PurchaseOrder {
	next.LegalEntityID, next.VendorID, next.Currency = cur.LegalEntityID, cur.VendorID, cur.Currency
	return next
}

// walk runs custom methods in order and returns the PO after the last.
func walk(t *testing.T, ctx context.Context, s *store.Postgres, po store.PurchaseOrder, actions ...string) store.PurchaseOrder {
	t.Helper()
	for _, a := range actions {
		next, err := s.ActOnPurchaseOrder(ctx, po.ID, po.RowVersion, a, store.Idem{})
		if err != nil {
			t.Fatalf("%s from %s: %v", a, po.ApprovalStatus, err)
		}
		po = next
	}
	return po
}

func confirmedPO(t *testing.T, ctx context.Context, s *store.Postgres, in store.PurchaseOrder) store.PurchaseOrder {
	t.Helper()
	po, err := s.CreatePurchaseOrder(ctx, in, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	po = walk(t, ctx, s, po, domain.POActionSubmit, domain.POActionApprove, domain.POActionConfirm)
	if po.ApprovalStatus != domain.POStatusConfirmed || po.VersionNo != 1 {
		t.Fatalf("after confirm: %s v%d", po.ApprovalStatus, po.VersionNo)
	}
	return po
}

func hasPatchPath(ops []domain.PatchOp, path string) bool {
	for _, op := range ops {
		if op.Path == path {
			return true
		}
	}
	return false
}

func eventTypes(rows []outboxRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Type)
	}
	return out
}

func setLineQuantities(t *testing.T, ctx context.Context, pool *pgxpool.Pool, poID, received, invoiced string) {
	t.Helper()
	err := dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `update purchase_order_line set received_qty = $2, invoiced_qty = $3 where purchase_order_id = $1`, poID, received, invoiced)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// AC-17.2 and AC-17.3 against Postgres: a qty change past 5 % is version 2
// pending re-approval and tells the vendor nothing until it is confirmed again;
// a stale If-Match is PRC-PO-009; a one-day date move confirms version 3 at once;
// every confirmed version writes po.confirmed and, above 1, poversion.created.
func TestPurchaseOrderLifecycle_VersionsAndReapproval(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(41)
	cur := confirmedPO(t, ctx, s, lifecyclePO(t, ids.New(), "100", "2027-01-05"))
	if got := eventTypes(outbox(t, ctx, pool)); len(got) != 1 || got[0] != purchasingevents.POConfirmed {
		t.Fatalf("after first confirm outbox = %v", got)
	}

	pending, err := s.EditPurchaseOrder(ctx, cur.ID, cur.RowVersion, edited(cur, lifecyclePO(t, "", "110", "2027-01-05")))
	if err != nil {
		t.Fatal(err)
	}
	if pending.ApprovalStatus != domain.POStatusInReview || pending.VersionNo != 1 || !pending.Lines[0].Qty.Equal(dec("110")) {
		t.Fatalf("after qty change: %s v%d qty %s", pending.ApprovalStatus, pending.VersionNo, pending.Lines[0].Qty)
	}
	v2, err := s.GetPurchaseOrderVersion(ctx, cur.ID, 2)
	if err != nil || v2.ConfirmedAt != nil || !v2.ReapprovalRequired || !hasPatchPath(v2.Patch, "/lines/0/qty") {
		t.Fatalf("pending version 2 = %+v, %v", v2, err)
	}
	if got := outbox(t, ctx, pool); len(got) != 1 {
		t.Fatalf("a change waiting for re-approval wrote %v", eventTypes(got))
	}

	if _, err := s.EditPurchaseOrder(ctx, cur.ID, cur.RowVersion, edited(cur, lifecyclePO(t, "", "120", "2027-01-05"))); problemCode(err) != store.CodeStaleVersion {
		t.Fatalf("stale If-Match = %v, want %s", err, store.CodeStaleVersion)
	}
	if _, err := s.ActOnPurchaseOrder(ctx, cur.ID, pending.RowVersion, domain.POActionConfirm, store.Idem{}); problemCode(err) != domain.CodeConfirmPrecondition {
		t.Fatalf("confirm while IN_REVIEW = %v", err)
	}

	approved := walk(t, ctx, s, pending, domain.POActionApprove)
	key := store.Idem{Key: "confirm-v2", Hash: "h"}
	reconfirmed, err := s.ActOnPurchaseOrder(ctx, cur.ID, approved.RowVersion, domain.POActionConfirm, key)
	if err != nil || reconfirmed.VersionNo != 2 || reconfirmed.ApprovalStatus != domain.POStatusConfirmed {
		t.Fatalf("re-confirm = %s v%d, %v", reconfirmed.ApprovalStatus, reconfirmed.VersionNo, err)
	}
	replay, err := s.ActOnPurchaseOrder(ctx, cur.ID, approved.RowVersion, domain.POActionConfirm, key)
	if err != nil || replay.RowVersion != reconfirmed.RowVersion {
		t.Fatalf("replayed confirm = %+v, %v; want the PO as it is", replay, err)
	}
	rows := outbox(t, ctx, pool)
	if got := eventTypes(rows); len(got) != 3 || got[1] != purchasingevents.POConfirmed || got[2] != purchasingevents.POVersionCreated {
		t.Fatalf("after re-confirm outbox = %v", got)
	}
	if rows[2].Data["version_no"] != float64(2) || rows[2].Data["reapproval_required"] != true {
		t.Fatalf("poversion.created = %+v", rows[2].Data)
	}

	// AC-17.3: the default rules exclude dates, so a one-day move confirms at once.
	moved, err := s.EditPurchaseOrder(ctx, cur.ID, reconfirmed.RowVersion, edited(cur, lifecyclePO(t, "", "110", "2027-01-06")))
	if err != nil || moved.ApprovalStatus != domain.POStatusConfirmed || moved.VersionNo != 3 {
		t.Fatalf("date move = %s v%d, %v", moved.ApprovalStatus, moved.VersionNo, err)
	}
	rows = outbox(t, ctx, pool)
	if got := eventTypes(rows); len(got) != 5 || got[4] != purchasingevents.POVersionCreated || rows[4].Data["reapproval_required"] != false {
		t.Fatalf("after date move outbox = %v", got)
	}
	versions, err := s.ListPurchaseOrderVersions(ctx, cur.ID)
	if err != nil || len(versions) != 3 || versions[2].ConfirmedAt == nil || !hasPatchPath(versions[2].Patch, "/lines/0/deliveryDate") {
		t.Fatalf("versions = %+v, %v", versions, err)
	}

	// Nothing changed: no version, no event.
	same, err := s.EditPurchaseOrder(ctx, cur.ID, moved.RowVersion, edited(cur, lifecyclePO(t, "", "110", "2027-01-06")))
	if err != nil || same.VersionNo != 3 || len(outbox(t, ctx, pool)) != 5 {
		t.Fatalf("no-op edit = v%d, %v", same.VersionNo, err)
	}
	if _, err := s.ListPurchaseOrderVersions(principalContext(42), cur.ID); problemCode(err) != store.CodePurchaseOrderNotFound {
		t.Fatalf("other tenant versions = %v", err)
	}
}

// A rejected change edited back to the confirmed content drops the pending
// version and returns the PO to CONFIRMED at its old version; an edit before
// the first confirmation sends the PO back to DRAFT.
func TestPurchaseOrderLifecycle_RejectedChangeEditedBack(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(43)

	draft, err := s.CreatePurchaseOrder(ctx, lifecyclePO(t, ids.New(), "100", "2027-01-05"), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	inReview := walk(t, ctx, s, draft, domain.POActionSubmit)
	back, err := s.EditPurchaseOrder(ctx, draft.ID, inReview.RowVersion, edited(draft, lifecyclePO(t, "", "90", "2027-01-05")))
	if err != nil || back.ApprovalStatus != domain.POStatusDraft || back.VersionNo != 0 || !back.Total.Equal(dec("900")) {
		t.Fatalf("edit in review = %s v%d total %s, %v", back.ApprovalStatus, back.VersionNo, back.Total, err)
	}

	cur := walk(t, ctx, s, back, domain.POActionSubmit, domain.POActionApprove, domain.POActionConfirm)
	pending, err := s.EditPurchaseOrder(ctx, cur.ID, cur.RowVersion, edited(cur, lifecyclePO(t, "", "200", "2027-01-05")))
	if err != nil || pending.ApprovalStatus != domain.POStatusInReview {
		t.Fatalf("change = %s, %v", pending.ApprovalStatus, err)
	}
	rejected := walk(t, ctx, s, pending, domain.POActionReject)
	if rejected.ApprovalStatus != domain.POStatusRejected {
		t.Fatalf("reject = %s", rejected.ApprovalStatus)
	}
	restored, err := s.EditPurchaseOrder(ctx, cur.ID, rejected.RowVersion, edited(cur, lifecyclePO(t, "", "90", "2027-01-05")))
	if err != nil || restored.ApprovalStatus != domain.POStatusConfirmed || restored.VersionNo != 1 || !restored.Total.Equal(dec("900")) {
		t.Fatalf("edited back = %s v%d total %s, %v", restored.ApprovalStatus, restored.VersionNo, restored.Total, err)
	}
	if _, err := s.GetPurchaseOrderVersion(ctx, cur.ID, 2); problemCode(err) != store.CodePOVersionNotFound {
		t.Fatalf("dropped version 2 = %v", err)
	}
}

// BR-17.7 / PRC-PO-007 and BR-17.2: quantities cannot drop below received or
// invoiced, a received PO cannot be cancelled, and a cancelled PO is closed to
// edits and actions; cancellation cancels every line and writes po.cancelled.
func TestPurchaseOrderLifecycle_ReceivedFloorAndCancel(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(44)
	cur := confirmedPO(t, ctx, s, lifecyclePO(t, ids.New(), "100", "2027-01-05"))

	setLineQuantities(t, ctx, pool, cur.ID, "50", "0")
	if _, err := s.EditPurchaseOrder(ctx, cur.ID, cur.RowVersion, edited(cur, lifecyclePO(t, "", "40", "2027-01-05"))); problemCode(err) != domain.CodeReceivedFloor {
		t.Fatalf("below received = %v", err)
	}
	if _, err := s.EditPurchaseOrder(ctx, cur.ID, cur.RowVersion, edited(cur, store.PurchaseOrder{Lines: []store.PurchaseOrderLine{}})); problemCode(err) != domain.CodeReceivedFloor {
		t.Fatalf("removing a received line = %v", err)
	}
	if _, err := s.ActOnPurchaseOrder(ctx, cur.ID, cur.RowVersion, domain.POActionCancel, store.Idem{}); problemCode(err) != domain.CodePOState {
		t.Fatalf("cancel with receipts = %v", err)
	}

	setLineQuantities(t, ctx, pool, cur.ID, "0", "0")
	cancelled := walk(t, ctx, s, cur, domain.POActionCancel)
	if cancelled.ApprovalStatus != domain.POStatusCancelled || cancelled.Lines[0].Status != domain.POLineCancelled {
		t.Fatalf("cancel = %s, line %s", cancelled.ApprovalStatus, cancelled.Lines[0].Status)
	}
	rows := outbox(t, ctx, pool)
	last := rows[len(rows)-1]
	if last.Type != purchasingevents.POCancelled || last.Data["prior_status"] != domain.POStatusConfirmed {
		t.Fatalf("last event = %+v", last)
	}
	if lines, _ := last.Data["lines"].([]any); len(lines) != 1 || lines[0].(map[string]any)["remaining_qty"] != "100" {
		t.Fatalf("po.cancelled lines = %+v", last.Data["lines"])
	}
	if _, err := s.EditPurchaseOrder(ctx, cur.ID, cancelled.RowVersion, edited(cur, lifecyclePO(t, "", "100", "2027-01-05"))); problemCode(err) != domain.CodePOState {
		t.Fatalf("edit after cancel = %v", err)
	}
	if _, err := s.ActOnPurchaseOrder(ctx, cur.ID, cancelled.RowVersion, domain.POActionCancel, store.Idem{}); problemCode(err) != domain.CodePOState {
		t.Fatalf("cancel twice = %v", err)
	}
}

// Cancelling returns what the PO held: the agreement line's release (FR-17.5)
// and the requisition line, which is OPEN again on a re-opened APPROVED
// requisition (FR-17.3). A line drawn on an agreement keeps its quantity and
// may move its delivery date only within the agreement validity (BR-17.10).
func TestPurchaseOrderLifecycle_CancelReturnsAgreementAndRequisition(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(31)
	vendor, product := ids.New(), ids.New()
	agr, err := s.CreateAgreement(ctx, blanket(t, vendor, product), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	qtyLine := agr.Agreement.Lines[0].ID
	po := confirmedPO(t, ctx, s, release(t, vendor, product, "100", qtyLine, "2027-03-01"))

	outside := release(t, vendor, product, "100", qtyLine, "2028-01-15")
	if _, err := s.EditPurchaseOrder(ctx, po.ID, po.RowVersion, edited(po, outside)); problemCode(err) != domain.CodeAgreementWindow {
		t.Fatalf("delivery outside validity = %v", err)
	}
	more := release(t, vendor, product, "120", qtyLine, "2027-03-01")
	if _, err := s.EditPurchaseOrder(ctx, po.ID, po.RowVersion, edited(po, more)); problemCode(err) != store.CodePOLineLink {
		t.Fatalf("agreement line qty change = %v", err)
	}
	inside, err := s.EditPurchaseOrder(ctx, po.ID, po.RowVersion, edited(po, release(t, vendor, product, "100", qtyLine, "2027-04-01")))
	if err != nil || inside.VersionNo != 2 {
		t.Fatalf("delivery within validity = v%d, %v", inside.VersionNo, err)
	}

	req := approved(t, s, ids.New(), "10")
	reqPO := lifecyclePO(t, vendor, "1", "2027-01-05")
	reqPO.Lines[0].RequisitionLineID = req.Lines[0].ID
	ordered, err := s.CreatePurchaseOrder(ctx, reqPO, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetRequisition(ctx, req.ID); got.Lines[0].Status != "CONSOLIDATED" {
		t.Fatalf("requisition line after PO = %s", got.Lines[0].Status)
	}

	walk(t, ctx, s, inside, domain.POActionCancel)
	walk(t, ctx, s, ordered, domain.POActionCancel)
	after, err := s.GetAgreement(ctx, agr.Agreement.ID)
	if err != nil || !after.Agreement.Lines[0].ReleasedQty.IsZero() || !after.Agreement.Lines[0].ReleasedAmount.IsZero() {
		t.Fatalf("agreement line after cancel = %+v, %v", after.Agreement.Lines[0], err)
	}
	got, err := s.GetRequisition(ctx, req.ID)
	if err != nil || got.Status != store.RequisitionApproved || got.Lines[0].Status != "OPEN" || got.Lines[0].PurchaseOrderID != "" {
		t.Fatalf("requisition after cancel = %s, line %+v, %v", got.Status, got.Lines[0], err)
	}
}
