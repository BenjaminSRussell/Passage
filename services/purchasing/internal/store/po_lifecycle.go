package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	purchasingevents "github.com/BenjaminSRussell/passage/services/purchasing/internal/events"
)

// PO lifecycle problem codes raised by the store.
const (
	// CodeStaleVersion (PRC-PO-009) is raised when If-Match is not the PO row version (BR-17.8).
	CodeStaleVersion = "PRC-PO-009"
	// CodePOVersionNotFound is a version number the PO does not have.
	CodePOVersionNotFound = "PRC-PO-VERSION-NOT-FOUND"
	// CodePOLineLink is raised when an edit changes what a line is linked to.
	CodePOLineLink = "PRC-PO-010"
)

// PurchaseOrderVersion is one stored version of a PO (FR-17.8). Patch is the
// RFC 6902 diff from the previous version's snapshot; version 1 has an empty
// patch. ConfirmedAt is nil while the version waits for re-approval.
type PurchaseOrderVersion struct {
	VersionNo          int              `json:"versionNo"`
	ConfirmedAt        *time.Time       `json:"confirmedAt,omitempty"`
	ConfirmedBy        string           `json:"confirmedBy,omitempty"`
	ReapprovalRequired bool             `json:"reapprovalRequired"`
	CreatedAt          time.Time        `json:"createdAt"`
	Patch              []domain.PatchOp `json:"patch"`
	Snapshot           domain.Snapshot  `json:"snapshot"`
}

// EditPurchaseOrder replaces the lines and charges of a PO with next, under the
// row lock and If-Match (BR-17.8). Lines are matched by line number: a line
// missing from next is cancelled (BR-17.9), a line number not on the PO is
// added. Before the first confirmation the edit returns the PO to DRAFT, so a
// PO in review is submitted again. After it, the change is the next version
// (FR-17.8): IN_REVIEW and unconfirmed when change management is on and a
// reapproval rule fires, otherwise confirmed at once with po.confirmed and
// poversion.created. Editing a change back to the confirmed content drops the
// pending version.
func (s *Postgres) EditPurchaseOrder(ctx context.Context, id string, ifMatch int, next PurchaseOrder) (PurchaseOrder, error) {
	var out PurchaseOrder
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		if err := lockPurchaseOrder(ctx, tx, id); err != nil {
			return err
		}
		cur, err := loadPurchaseOrder(ctx, tx, id)
		if err != nil {
			return err
		}
		out, _, err = editLocked(ctx, tx, cur, ifMatch, next, false)
		return err
	})
	return out, err
}

// editResult says which version an edit of a confirmed PO wrote: its number
// (0 when the edit changed nothing or the PO was never confirmed) and whether
// it waits for re-approval.
type editResult struct {
	versionNo  int
	reapproval bool
}

// editLocked is the body of EditPurchaseOrder for cur, which the caller has
// locked and loaded inside tx. It returns the PO as stored afterwards.
//
// planned is true only for a change that comes from inbound planning (a postpone or a quantity message applied by
// the inbound consumer or its retry): that is the one writer that may move the quantity of a line ordered from a
// requisition line. A buyer's edit and a reprice may not.
func editLocked(ctx context.Context, tx pgx.Tx, cur PurchaseOrder, ifMatch int, next PurchaseOrder, planned bool) (PurchaseOrder, editResult, error) {
	id := cur.ID
	actor, _ := tenant.FromContext(ctx)
	if err := checkRowVersion(cur, ifMatch); err != nil {
		return PurchaseOrder{}, editResult{}, err
	}
	if err := domain.CheckEditable(cur.ApprovalStatus); err != nil {
		return PurchaseOrder{}, editResult{}, err
	}
	if err := checkHeaderUnchanged(cur, next); err != nil {
		return PurchaseOrder{}, editResult{}, err
	}
	pol, err := loadPolicy(ctx, tx, cur.LegalEntityID)
	if err != nil {
		return PurchaseOrder{}, editResult{}, err
	}
	removed, err := checkLineChanges(cur.Lines, next.Lines, pol.AllowInvoiceBeforeReceipt, planned)
	if err != nil {
		return PurchaseOrder{}, editResult{}, err
	}
	if err := checkAgreementDates(ctx, tx, cur.Lines, next.Lines); err != nil {
		return PurchaseOrder{}, editResult{}, err
	}
	next.ID, next.Number = id, cur.Number
	next.LegalEntityID, next.VendorID, next.Currency = cur.LegalEntityID, cur.VendorID, cur.Currency
	next.ChangeManagement = cur.ChangeManagement
	next.VersionNo = cur.VersionNo
	merged := mergeLines(cur.Lines, next.Lines, removed)
	// rewrite stores next's lines and header in status and reloads the PO.
	rewrite := func(status string, res editResult) (PurchaseOrder, editResult, error) {
		if err := applyLines(ctx, tx, next, removed); err != nil {
			return PurchaseOrder{}, editResult{}, err
		}
		if err := addAutoChargeCodes(ctx, tx, id, next.AutoChargeCodes); err != nil {
			return PurchaseOrder{}, editResult{}, err
		}
		next.ApprovalStatus = status
		if err := updateHeader(ctx, tx, next); err != nil {
			return PurchaseOrder{}, editResult{}, err
		}
		// The editor is one of the users four-eyes keeps from approving the change.
		if _, err := tx.Exec(ctx, `update purchase_order set changed_by = $3 where tenant_id = $1 and id = $2`,
			actor.TenantID, id, userOrNil(actor.UserID)); err != nil {
			return PurchaseOrder{}, editResult{}, err
		}
		out, err := loadPurchaseOrder(ctx, tx, id)
		if err != nil {
			return PurchaseOrder{}, editResult{}, err
		}
		// A change of a confirmed PO cannot take a line below its planned services: the
		// specification is frozen outside DRAFT and REJECTED, so it could not be fixed later.
		if cur.VersionNo > 0 {
			if err := checkServiceSpecsWithinLines(ctx, tx, out); err != nil {
				return PurchaseOrder{}, editResult{}, err
			}
		}
		return out, res, nil
	}

	if cur.VersionNo == 0 {
		// Not yet confirmed: a plain edit that restarts approval.
		return rewrite(domain.POStatusDraft, editResult{})
	}

	base, ok, err := loadVersion(ctx, tx, id, cur.VersionNo)
	if err != nil {
		return PurchaseOrder{}, editResult{}, err
	}
	if !ok {
		return PurchaseOrder{}, editResult{}, fmt.Errorf("purchase order %s: confirmed version %d is missing", cur.Number, cur.VersionNo)
	}
	nextSnap := snapshotOf(next, merged)
	patch, err := domain.VersionDiff(base.Snapshot, nextSnap)
	if err != nil {
		return PurchaseOrder{}, editResult{}, err
	}
	pendingNo := cur.VersionNo + 1
	if len(patch) == 0 {
		if cur.ApprovalStatus == domain.POStatusConfirmed {
			return cur, editResult{}, nil // nothing changed and nothing pending
		}
		// The change was edited back to the confirmed content: drop it.
		if err := deleteVersion(ctx, tx, id, pendingNo); err != nil {
			return PurchaseOrder{}, editResult{}, err
		}
		return rewrite(domain.POStatusConfirmed, editResult{})
	}
	reapproval := cur.ChangeManagement &&
		domain.ChangeNeedsReapproval(base.Snapshot, nextSnap, patch, domain.DefaultReapprovalRules())
	if err := writeVersion(ctx, tx, id, pendingNo, nextSnap, patch, reapproval); err != nil {
		return PurchaseOrder{}, editResult{}, err
	}
	if reapproval {
		return rewrite(domain.POStatusInReview, editResult{versionNo: pendingNo, reapproval: true})
	}
	if err := markConfirmed(ctx, tx, id, pendingNo); err != nil {
		return PurchaseOrder{}, editResult{}, err
	}
	next.VersionNo = pendingNo
	out, res, err := rewrite(domain.POStatusConfirmed, editResult{versionNo: pendingNo})
	if err != nil {
		return PurchaseOrder{}, editResult{}, err
	}
	if err := emitConfirmed(ctx, tx, out); err != nil {
		return PurchaseOrder{}, editResult{}, err
	}
	return out, res, emitVersionCreated(ctx, tx, out, patch, false)
}

// ActOnPurchaseOrder runs one custom method on a PO (submit, approve, reject,
// confirm or cancel) under the row lock and If-Match. A replayed key returns the
// PO as it is now.
func (s *Postgres) ActOnPurchaseOrder(ctx context.Context, id string, ifMatch int, action string, idem Idem) (PurchaseOrder, error) {
	var out PurchaseOrder
	actor, _ := tenant.FromContext(ctx)
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		existing, replay, err := claimKey(ctx, tx, idem, id)
		if err != nil {
			return err
		}
		if replay {
			out, err = loadPurchaseOrder(ctx, tx, existing)
			return err
		}
		if err := lockPurchaseOrder(ctx, tx, id); err != nil {
			return err
		}
		cur, err := loadPurchaseOrder(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := checkRowVersion(cur, ifMatch); err != nil {
			return err
		}
		head := cur
		if action == domain.POActionApprove || action == domain.POActionConfirm {
			pol, err := loadPolicy(ctx, tx, cur.LegalEntityID)
			if err != nil {
				return err
			}
			if err := domain.CheckFourEyes(pol, actor.UserID, cur.CreatedBy, cur.SubmittedBy, cur.ChangedBy); err != nil {
				return err
			}
		}
		switch action {
		case domain.POActionCancel:
			if err := cancelOrder(ctx, tx, cur); err != nil {
				return err
			}
			head.ApprovalStatus = domain.POStatusCancelled
		case domain.POActionConfirm:
			if _, err := domain.NextApproval(cur.ApprovalStatus, action); err != nil {
				return err
			}
			if err := confirmPending(ctx, tx, cur); err != nil {
				return err
			}
			head.ApprovalStatus, head.VersionNo = domain.POStatusConfirmed, cur.VersionNo+1
		default:
			if head.ApprovalStatus, err = domain.NextApproval(cur.ApprovalStatus, action); err != nil {
				return err
			}
			// A planned outline worth more than its line is refused at submit, while the
			// specification can still be fixed (it is frozen once the PO leaves DRAFT).
			if action == domain.POActionSubmit {
				if err := checkServiceSpecsWithinLines(ctx, tx, cur); err != nil {
					return err
				}
			}
		}
		if err := updateHeader(ctx, tx, head); err != nil {
			return err
		}
		if action == domain.POActionSubmit {
			if _, err := tx.Exec(ctx, `update purchase_order set submitted_by = $3 where tenant_id = $1 and id = $2`,
				actor.TenantID, id, userOrNil(actor.UserID)); err != nil {
				return err
			}
		}
		if out, err = loadPurchaseOrder(ctx, tx, id); err != nil {
			return err
		}
		if action != domain.POActionConfirm {
			return nil
		}
		if err := emitConfirmed(ctx, tx, out); err != nil {
			return err
		}
		if out.VersionNo == 1 {
			return nil
		}
		v, _, err := loadVersion(ctx, tx, id, out.VersionNo)
		if err != nil {
			return err
		}
		return emitVersionCreated(ctx, tx, out, v.Patch, v.ReapprovalRequired)
	})
	return out, err
}

// ListPurchaseOrderVersions returns every stored version of a PO, oldest first,
// including a pending one; 404 when the PO is not visible.
func (s *Postgres) ListPurchaseOrderVersions(ctx context.Context, poID string) ([]PurchaseOrderVersion, error) {
	var out []PurchaseOrderVersion
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		if err := checkVisible(ctx, tx, poID); err != nil {
			return err
		}
		p, _ := tenant.FromContext(ctx)
		rows, err := tx.Query(ctx, `select `+versionColumns+` from purchase_order_version
			where tenant_id = $1 and purchase_order_id = $2 order by version_no`, p.TenantID, poID)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (PurchaseOrderVersion, error) { return scanVersion(row) })
		return err
	})
	if out == nil {
		out = []PurchaseOrderVersion{}
	}
	return out, err
}

// GetPurchaseOrderVersion returns one stored version of a PO (FR-17.8); 404
// when the PO is not visible or has no such version.
func (s *Postgres) GetPurchaseOrderVersion(ctx context.Context, poID string, no int) (PurchaseOrderVersion, error) {
	var out PurchaseOrderVersion
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		if err := checkVisible(ctx, tx, poID); err != nil {
			return err
		}
		v, ok, err := loadVersion(ctx, tx, poID, no)
		if err != nil {
			return err
		}
		if !ok {
			return httpx.NewProblem(http.StatusNotFound, CodePOVersionNotFound, fmt.Sprintf("purchase order version %d not found", no))
		}
		out = v
		return nil
	})
	return out, err
}

// checkVisible is 404 unless the PO is visible to the caller. Versions carry no
// legal entity, so reading one goes through the PO row and its legal-entity
// scope policy first.
func checkVisible(ctx context.Context, tx pgx.Tx, poID string) error {
	p, _ := tenant.FromContext(ctx)
	var one int
	if err := tx.QueryRow(ctx, `select 1 from purchase_order where tenant_id = $1 and id = $2`, p.TenantID, poID).Scan(&one); err != nil {
		return notFound(err, CodePurchaseOrderNotFound, "purchase order not found")
	}
	return nil
}

// confirmPending confirms version 1 from the live PO, or the pending change
// version written by an edit that needed re-approval.
func confirmPending(ctx context.Context, tx pgx.Tx, cur PurchaseOrder) error {
	if err := checkSchedulesBalanced(ctx, tx, cur); err != nil {
		return err
	}
	if err := checkServiceSpecsWithinLines(ctx, tx, cur); err != nil {
		return err
	}
	no := cur.VersionNo + 1
	if cur.VersionNo == 0 {
		if err := writeVersion(ctx, tx, cur.ID, no, snapshotOf(cur, cur.Lines), nil, false); err != nil {
			return err
		}
		return markConfirmed(ctx, tx, cur.ID, no)
	}
	if _, ok, err := loadVersion(ctx, tx, cur.ID, no); err != nil {
		return err
	} else if !ok {
		return &domain.Error{Code: domain.CodeConfirmPrecondition, Detail: "purchase order has no pending change to confirm"}
	}
	return markConfirmed(ctx, tx, cur.ID, no)
}

// cancelOrder cancels every open line of a PO that has nothing received or
// invoiced (BR-17.2), returns agreement releases and requisition lines, and
// writes po.cancelled.
func cancelOrder(ctx context.Context, tx pgx.Tx, cur PurchaseOrder) error {
	received, invoiced := decimal.Zero, decimal.Zero
	var open []PurchaseOrderLine
	data := purchasingevents.POCancelledData{POID: cur.ID, Number: cur.Number, VersionNo: cur.VersionNo,
		PriorStatus: cur.ApprovalStatus, Lines: []purchasingevents.POCancelledLine{}}
	for _, l := range cur.Lines {
		if l.Status == domain.POLineCancelled {
			continue
		}
		open = append(open, l)
		received = received.Add(l.ReceivedQty)
		invoiced = invoiced.Add(l.InvoicedQty)
		data.Lines = append(data.Lines, purchasingevents.POCancelledLine{LineNo: l.LineNo, ProductID: l.ProductID, Unit: l.Unit,
			RemainingQty: l.Qty.Sub(l.ReceivedQty).String()})
	}
	if err := domain.CanCancel(cur.ApprovalStatus, received, invoiced); err != nil {
		return err
	}
	p, _ := tenant.FromContext(ctx)
	if _, err := tx.Exec(ctx, `update purchase_order_line set status = $3 where tenant_id = $1 and purchase_order_id = $2`,
		p.TenantID, cur.ID, domain.POLineCancelled); err != nil {
		return err
	}
	if err := releaseLines(ctx, tx, cur.ID, open); err != nil {
		return err
	}
	if err := syncInvoicingPlans(ctx, tx, cur.ID); err != nil {
		return err
	}
	if err := syncDeliverySchedules(ctx, tx, cur.ID); err != nil {
		return err
	}
	return enqueue(ctx, tx, "purchase_order", cur.ID, purchasingevents.POCancelled, data)
}

// releaseLines undoes what cancelled PO lines held: the quantity and amount
// they drew from agreement lines (FR-17.5) and the requisition lines they
// ordered, which go back to OPEN on an APPROVED requisition so they can be
// sourced again (FR-17.3).
func releaseLines(ctx context.Context, tx pgx.Tx, poID string, lines []PurchaseOrderLine) error {
	p, _ := tenant.FromContext(ctx)
	// The requisitions first, in id order, before any requisition or agreement line: the order every writer of
	// requisition lines uses (see loadSourcingLines). Reopening a line and then its CLOSED requisition would
	// deadlock with a split or a consolidation that holds the requisition and waits for the line.
	var reqLineIDs []string
	for _, l := range lines {
		if l.RequisitionLineID != "" {
			reqLineIDs = append(reqLineIDs, l.RequisitionLineID)
		}
	}
	if len(reqLineIDs) > 0 {
		if err := lockRequisitionsOf(ctx, tx, reqLineIDs); err != nil {
			return err
		}
	}
	// The requisition lines below are written after the agreement lines are locked; that is safe because every
	// writer of a requisition line holds its requisition first, and this transaction already holds them all.
	if err := lockAgreementLinesOf(ctx, tx, lines); err != nil {
		return err
	}
	for _, l := range lines {
		if l.AgreementLineID != "" {
			if _, err := tx.Exec(ctx, `update purchase_agreement_line
				set released_qty = greatest(released_qty - $3, 0), released_amount = greatest(released_amount - $4, 0)
				where tenant_id = $1 and id = $2`, p.TenantID, l.AgreementLineID, l.Qty, l.LineAmount); err != nil {
				return err
			}
		}
		if l.RequisitionLineID == "" {
			continue
		}
		var reqID string
		err := tx.QueryRow(ctx, `update purchase_requisition_line set status = 'OPEN', purchase_order_id = null, purchase_order_line_no = null
			where tenant_id = $1 and id = $2 and purchase_order_id = $3 and purchase_order_line_no = $4 and status = 'CONSOLIDATED'
			returning requisition_id`, p.TenantID, l.RequisitionLineID, poID, l.LineNo).Scan(&reqID)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update purchase_requisition set status = 'APPROVED', version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2 and status = 'CLOSED'`, p.TenantID, reqID); err != nil {
			return err
		}
	}
	return nil
}

// checkHeaderUnchanged keeps the parties of a PO fixed; site and warehouse may
// change only before the first confirmation.
func checkHeaderUnchanged(cur, next PurchaseOrder) error {
	if cur.VendorID != next.VendorID || cur.Currency != next.Currency || cur.LegalEntityID != next.LegalEntityID {
		return &domain.Error{Code: domain.CodeValidation, Detail: "vendor, currency and legal entity cannot change on an existing purchase order"}
	}
	if cur.VersionNo > 0 && (cur.SiteID != next.SiteID || cur.WarehouseID != next.WarehouseID) {
		return &domain.Error{Code: domain.CodeValidation, Detail: "site and warehouse cannot change after the purchase order is confirmed"}
	}
	return nil
}

// checkLineChanges matches next to the current lines by number. It refuses
// reopening a cancelled line, a new line with a requisition or agreement link,
// a changed link, a changed unit or quantity on a line ordered from a requisition
// line (unless planned), a changed product, unit, quantity or amount on a line drawn
// against an agreement, and quantities below received or invoiced (PRC-PO-007). It
// returns the live lines that next leaves out, which the edit cancels.
func checkLineChanges(cur, next []PurchaseOrderLine, allowInvoiceBeforeReceipt, planned bool) ([]PurchaseOrderLine, error) {
	byNo := make(map[int]PurchaseOrderLine, len(cur))
	for _, l := range cur {
		byNo[l.LineNo] = l
	}
	kept := make(map[int]bool, len(next))
	for _, n := range next {
		kept[n.LineNo] = true
		c, exists := byNo[n.LineNo]
		if !exists {
			if n.RequisitionLineID != "" || n.AgreementLineID != "" {
				return nil, linkErr("new line %d cannot reference a requisition or agreement line; create a new purchase order for it", n.LineNo)
			}
			continue
		}
		if c.Status == domain.POLineCancelled {
			return nil, &domain.Error{Code: domain.CodePOState, Detail: fmt.Sprintf("line %d is cancelled and cannot be changed", n.LineNo)}
		}
		if c.RequisitionLineID != n.RequisitionLineID || c.AgreementLineID != n.AgreementLineID {
			return nil, linkErr("line %d keeps its requisition and agreement line", n.LineNo)
		}
		if c.RequisitionLineID != "" && !planned && (!c.Qty.Equal(n.Qty) || c.Unit != n.Unit) {
			// The line consumed its whole requisition line (PRC-REQ-008 at create), so another quantity or unit
			// would leave the need and the order out of step. Cancel the line and order the rest from a split.
			return nil, httpx.NewProblem(http.StatusUnprocessableEntity, CodeRequisitionQty, fmt.Sprintf(
				"line %d orders %s %s of its requisition line; its quantity and unit cannot change (cancel the line, split the requisition line and order again)",
				n.LineNo, c.Qty, c.Unit))
		}
		if c.AgreementLineID != "" && (!c.Qty.Equal(n.Qty) || !c.LineAmount.Equal(n.LineAmount) || c.ProductID != n.ProductID || c.Unit != n.Unit) {
			return nil, linkErr("line %d draws on a purchase agreement; its product, unit, quantity and amount cannot change (cancel the line and order again)", n.LineNo)
		}
		if c.ReceivedQty.Sign() > 0 && (c.SiteID != n.SiteID || c.WarehouseID != n.WarehouseID) {
			return nil, &domain.Error{Code: domain.CodeValidation, Detail: fmt.Sprintf(
				"line %d has receipts; its site and warehouse cannot change (cancel the open quantity and order the rest again)", n.LineNo)}
		}
		if err := domain.CheckReduction(n.LineNo, c.ReceivedQty, c.InvoicedQty, n.Qty, allowInvoiceBeforeReceipt); err != nil {
			return nil, err
		}
	}
	var removed []PurchaseOrderLine
	for _, c := range cur {
		if c.Status == domain.POLineCancelled || kept[c.LineNo] {
			continue
		}
		if err := domain.CheckReduction(c.LineNo, c.ReceivedQty, c.InvoicedQty, decimal.Zero, allowInvoiceBeforeReceipt); err != nil {
			return nil, err
		}
		removed = append(removed, c)
	}
	return removed, nil
}

// checkAgreementDates keeps BR-17.10 true after an edit: a line drawn against
// a purchase agreement whose delivery date moves must still fall within the
// agreement's validity (PRC-AGR-005). The release itself is unchanged, so the
// agreement's status and limits are not checked again.
func checkAgreementDates(ctx context.Context, tx pgx.Tx, cur, next []PurchaseOrderLine) error {
	byNo := make(map[int]PurchaseOrderLine, len(cur))
	for _, l := range cur {
		byNo[l.LineNo] = l
	}
	for _, n := range next {
		c, ok := byNo[n.LineNo]
		if !ok || c.AgreementLineID == "" || c.DeliveryDate.Equal(n.DeliveryDate.Time) {
			continue
		}
		agr, err := loadAgreementLine(ctx, tx, c.AgreementLineID, false)
		if err != nil {
			return err
		}
		if !agr.Covers(n.DeliveryDate) {
			to := "open"
			if agr.ValidTo != nil {
				to = agr.ValidTo.String()
			}
			return &domain.Error{Code: domain.CodeAgreementWindow, Detail: fmt.Sprintf(
				"line %d: delivery date %s is outside agreement %s validity %s–%s", n.LineNo, n.DeliveryDate, agr.Number, agr.ValidFrom, to)}
		}
	}
	return nil
}

func linkErr(format string, args ...any) error {
	return &domain.Error{Code: CodePOLineLink, Detail: fmt.Sprintf(format, args...)}
}

// mergeLines is the full line set a version records after an edit (BR-17.9):
// next's lines, the lines the edit removes marked CANCELLED, and the lines
// cancelled earlier.
func mergeLines(cur, next, removed []PurchaseOrderLine) []PurchaseOrderLine {
	out := make([]PurchaseOrderLine, 0, len(cur)+len(next))
	for _, l := range next {
		l.Status = domain.POLineOpen
		out = append(out, l)
	}
	for _, l := range removed {
		l.Status = domain.POLineCancelled
		out = append(out, l)
	}
	for _, l := range cur {
		if l.Status == domain.POLineCancelled {
			out = append(out, l)
		}
	}
	return out
}

// snapshotOf builds the version content of po's header and charges with the
// given lines (cancelled lines included).
func snapshotOf(po PurchaseOrder, lines []PurchaseOrderLine) domain.Snapshot {
	out := make([]domain.SnapshotLine, 0, len(lines))
	for _, l := range lines {
		out = append(out, domain.SnapshotLine{LineNo: l.LineNo, ProductID: l.ProductID, Description: l.Description,
			Qty: l.Qty, Unit: l.Unit, UnitPrice: l.UnitPrice, PriceUnit: l.PriceUnit, DiscountPct: l.DiscountPct,
			LineAmount: l.LineAmount, TaxAmount: l.TaxAmount, DeliveryDate: l.DeliveryDate, Status: l.Status,
			SiteID: lineOverride(l.SiteID, po.SiteID), WarehouseID: lineOverride(l.WarehouseID, po.WarehouseID)})
	}
	charges := make([]domain.SnapshotCharge, 0, len(po.Charges))
	for _, c := range po.Charges {
		charges = append(charges, domain.SnapshotCharge{Calc: c.Calc, Value: c.Value, Amount: c.Amount, ChargeCode: c.ChargeCode, LineNo: c.LineNo})
	}
	return domain.NewSnapshot(po.Currency, po.Total, out, charges)
}

func checkRowVersion(cur PurchaseOrder, ifMatch int) error {
	if cur.RowVersion != ifMatch {
		return httpx.NewProblem(http.StatusPreconditionFailed, CodeStaleVersion,
			fmt.Sprintf("purchase order %s changed at %s and is at version %d, not %d; reload and reapply the change",
				cur.Number, cur.UpdatedAt.UTC().Format(time.RFC3339), cur.RowVersion, ifMatch))
	}
	return nil
}

func lockPurchaseOrder(ctx context.Context, tx pgx.Tx, id string) error {
	p, _ := tenant.FromContext(ctx)
	var one int
	err := tx.QueryRow(ctx, `select 1 from purchase_order where tenant_id = $1 and id = $2 for update`, p.TenantID, id).Scan(&one)
	if err != nil {
		return notFound(err, CodePurchaseOrderNotFound, "purchase order not found")
	}
	return nil
}

// applyLines writes next's lines (updating existing line numbers, inserting
// new ones), cancels the removed lines and returns what they held, replaces
// the header charges, and brings the lines' invoicing plans in step (BR-17.17).
func applyLines(ctx context.Context, tx pgx.Tx, next PurchaseOrder, removed []PurchaseOrderLine) error {
	p, _ := tenant.FromContext(ctx)
	for _, l := range removed {
		if _, err := tx.Exec(ctx, `update purchase_order_line set status = $4 where tenant_id = $1 and purchase_order_id = $2 and line_no = $3`,
			p.TenantID, next.ID, l.LineNo, domain.POLineCancelled); err != nil {
			return err
		}
	}
	if err := releaseLines(ctx, tx, next.ID, removed); err != nil {
		return err
	}
	for _, l := range next.Lines {
		tag, err := tx.Exec(ctx, `update purchase_order_line set product_id = $3, description = $4, qty = $5, unit = $6,
			unit_price = $7, price_unit = $8, discount_pct = $9, gross_amount = $10, discount_amount = $11, line_amount = $12,
			tax_amount = $13, delivery_date = $14, price_source = $15, site_id = $17, warehouse_id = $18
			where tenant_id = $1 and purchase_order_id = $2 and line_no = $16 and status <> 'CANCELLED'`,
			p.TenantID, next.ID, nullable(l.ProductID), l.Description, l.Qty, l.Unit, l.UnitPrice, l.PriceUnit, l.DiscountPct,
			l.GrossAmount, l.DiscountAmount, l.LineAmount, l.TaxAmount, l.DeliveryDate.Time, l.PriceSource, l.LineNo,
			nullable(lineOverride(l.SiteID, next.SiteID)), nullable(lineOverride(l.WarehouseID, next.WarehouseID)))
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 1 {
			continue
		}
		if _, err := tx.Exec(ctx, `insert into purchase_order_line (tenant_id, purchase_order_id, line_no, product_id, description,
			qty, unit, unit_price, price_unit, discount_pct, gross_amount, discount_amount, line_amount, tax_amount, delivery_date,
			price_source, category, site_id, warehouse_id, status)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, 'OPEN')`,
			p.TenantID, next.ID, l.LineNo, nullable(l.ProductID), l.Description, l.Qty, l.Unit, l.UnitPrice, l.PriceUnit,
			l.DiscountPct, l.GrossAmount, l.DiscountAmount, l.LineAmount, l.TaxAmount, l.DeliveryDate.Time, l.PriceSource, l.Category,
			nullable(lineOverride(l.SiteID, next.SiteID)), nullable(lineOverride(l.WarehouseID, next.WarehouseID))); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `delete from purchase_order_charge where tenant_id = $1 and purchase_order_id = $2`, p.TenantID, next.ID); err != nil {
		return err
	}
	for _, c := range next.Charges {
		if err := insertCharge(ctx, tx, next.ID, c); err != nil {
			return err
		}
	}
	if err := syncInvoicingPlans(ctx, tx, next.ID); err != nil {
		return err
	}
	return syncDeliverySchedules(ctx, tx, next.ID)
}

// updateHeader writes approval status, confirmed version number, site,
// warehouse and totals, and bumps the row version used as the ETag.
// addAutoChargeCodes adds codes to the charge codes auto charges have added to the PO (FR-17.9);
// the set only grows, so applyAutoCharges never adds a code twice.
func addAutoChargeCodes(ctx context.Context, tx pgx.Tx, id string, codes []string) error {
	if len(codes) == 0 {
		return nil
	}
	p, _ := tenant.FromContext(ctx)
	_, err := tx.Exec(ctx, `update purchase_order set auto_charge_codes = array(
		select c from unnest(auto_charge_codes || $3::text[]) with ordinality as t(c, n) group by c order by min(n))
		where tenant_id = $1 and id = $2`, p.TenantID, id, codes)
	return err
}

func updateHeader(ctx context.Context, tx pgx.Tx, po PurchaseOrder) error {
	p, _ := tenant.FromContext(ctx)
	_, err := tx.Exec(ctx, `update purchase_order set approval_status = $3, version_no = $4, site_id = $5, warehouse_id = $6,
		subtotal = $7, discount_total = $8, charges_total = $9, tax_total = $10, total = $11,
		row_version = row_version + 1, updated_at = now()
		where tenant_id = $1 and id = $2`,
		p.TenantID, po.ID, po.ApprovalStatus, po.VersionNo, nullable(po.SiteID), nullable(po.WarehouseID),
		po.Subtotal, po.DiscountTotal, po.ChargesTotal, po.TaxTotal, po.Total)
	return err
}

// writeVersion stores version no unconfirmed, replacing a pending row of the
// same number (a change edited again while it waits for re-approval).
func writeVersion(ctx context.Context, tx pgx.Tx, poID string, no int, snap domain.Snapshot, patch []domain.PatchOp, reapproval bool) error {
	p, _ := tenant.FromContext(ctx)
	if patch == nil {
		patch = []domain.PatchOp{}
	}
	snapJSON, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	patchJSON, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `insert into purchase_order_version (tenant_id, purchase_order_id, version_no, snapshot, patch, reapproval_required)
		values ($1, $2, $3, $4, $5, $6)
		on conflict (tenant_id, purchase_order_id, version_no) do update
		set snapshot = excluded.snapshot, patch = excluded.patch, reapproval_required = excluded.reapproval_required, created_at = now()
		where purchase_order_version.confirmed_at is null`,
		p.TenantID, poID, no, string(snapJSON), string(patchJSON), reapproval)
	return err
}

// markConfirmed stamps version no as confirmed now by the caller.
func markConfirmed(ctx context.Context, tx pgx.Tx, poID string, no int) error {
	p, _ := tenant.FromContext(ctx)
	_, err := tx.Exec(ctx, `update purchase_order_version set confirmed_at = now(), confirmed_by = $4
		where tenant_id = $1 and purchase_order_id = $2 and version_no = $3 and confirmed_at is null`,
		p.TenantID, poID, no, userOrNil(p.UserID))
	return err
}

func deleteVersion(ctx context.Context, tx pgx.Tx, poID string, no int) error {
	p, _ := tenant.FromContext(ctx)
	_, err := tx.Exec(ctx, `delete from purchase_order_version
		where tenant_id = $1 and purchase_order_id = $2 and version_no = $3 and confirmed_at is null`, p.TenantID, poID, no)
	return err
}

func userOrNil(id string) *string {
	if !ids.Valid(id) {
		return nil
	}
	return &id
}

const versionColumns = `version_no, snapshot::text, patch::text, reapproval_required, confirmed_at, coalesce(confirmed_by::text, ''), created_at`

func scanVersion(row pgx.Row) (PurchaseOrderVersion, error) {
	var v PurchaseOrderVersion
	var snap, patch string
	if err := row.Scan(&v.VersionNo, &snap, &patch, &v.ReapprovalRequired, &v.ConfirmedAt, &v.ConfirmedBy, &v.CreatedAt); err != nil {
		return v, err
	}
	if err := json.Unmarshal([]byte(snap), &v.Snapshot); err != nil {
		return v, err
	}
	if err := json.Unmarshal([]byte(patch), &v.Patch); err != nil {
		return v, err
	}
	return v, nil
}

// loadVersion reads one stored version; ok is false when it does not exist.
func loadVersion(ctx context.Context, tx pgx.Tx, poID string, no int) (PurchaseOrderVersion, bool, error) {
	p, _ := tenant.FromContext(ctx)
	v, err := scanVersion(tx.QueryRow(ctx, `select `+versionColumns+` from purchase_order_version
		where tenant_id = $1 and purchase_order_id = $2 and version_no = $3`, p.TenantID, poID, no))
	if errors.Is(err, pgx.ErrNoRows) {
		return v, false, nil
	}
	return v, err == nil, err
}

// emitConfirmed writes po.confirmed.v1 for po at its confirmed version.
func emitConfirmed(ctx context.Context, tx pgx.Tx, po PurchaseOrder) error {
	data := purchasingevents.POConfirmedData{POID: po.ID, Number: po.Number, VersionNo: po.VersionNo, LegalEntityID: po.LegalEntityID,
		VendorID: po.VendorID, SiteID: po.SiteID, WarehouseID: po.WarehouseID, Currency: po.Currency,
		Subtotal: po.Subtotal.String(), DiscountTotal: po.DiscountTotal.String(), ChargesTotal: po.ChargesTotal.String(),
		TaxTotal: po.TaxTotal.String(), Total: po.Total.String(), Lines: []purchasingevents.POLineData{}}
	schedules, err := scheduleEventRows(ctx, tx, po.ID)
	if err != nil {
		return err
	}
	for _, l := range po.Lines {
		data.Lines = append(data.Lines, purchasingevents.POLineData{LineNo: l.LineNo, ProductID: l.ProductID, Description: l.Description,
			Qty: l.Qty.String(), Unit: l.Unit, UnitPrice: l.UnitPrice.String(), PriceUnit: l.PriceUnit.String(),
			LineAmount: l.LineAmount.String(), DeliveryDate: l.DeliveryDate.String(), Status: l.Status, SiteID: l.SiteID, WarehouseID: l.WarehouseID, Schedule: schedules[l.LineNo]})
	}
	return enqueue(ctx, tx, "purchase_order", po.ID, purchasingevents.POConfirmed, data)
}

// emitVersionCreated writes poversion.created.v1 for a confirmed version above 1 (FR-17.8).
func emitVersionCreated(ctx context.Context, tx pgx.Tx, po PurchaseOrder, patch []domain.PatchOp, reapproval bool) error {
	if patch == nil {
		patch = []domain.PatchOp{}
	}
	data := purchasingevents.POVersionCreatedData{POID: po.ID, Number: po.Number, VersionNo: po.VersionNo, Patch: patch, ReapprovalRequired: reapproval}
	return enqueue(ctx, tx, "purchase_order", po.ID, purchasingevents.POVersionCreated, data)
}
