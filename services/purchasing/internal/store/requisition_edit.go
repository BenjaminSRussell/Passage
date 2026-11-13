package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	purchasingevents "github.com/BenjaminSRussell/passage/services/purchasing/internal/events"
)

// RequisitionEdit is a change to a DRAFT requisition (BR-17.1). Lines, when not nil, replace all the lines; the
// caller has priced them and summed Total. A nil Lines changes the operating unit only.
type RequisitionEdit struct {
	OperatingUnitID string
	Currency        string
	Lines           []RequisitionLine
	Total           decimal.Decimal
	ActorID         string
}

// UpdateRequisition edits a DRAFT requisition (a new one, or one reopened after a rejection) under the same row
// lock and version check as the approval actions (BR-17.1): the lines are replaced as a set and renumbered from 1,
// the total follows, the version moves on and an EDITED row joins the history. A requisition in any other status
// is refused with PRC-REQ-STATE. A replayed Idempotency-Key returns the requisition as it is now.
func (s *Postgres) UpdateRequisition(ctx context.Context, id string, version int, edit RequisitionEdit, idem Idem) (Requisition, error) {
	p, _ := tenant.FromContext(ctx)
	var out Requisition
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `select 1 from purchase_requisition where tenant_id = $1 and id = $2 for update`, p.TenantID, id); err != nil {
			return err
		}
		cur, err := loadRequisition(ctx, tx, id)
		if err != nil {
			return err
		}
		if _, replay, err := claimKey(ctx, tx, idem, id); err != nil || replay {
			out = cur
			return err
		}
		if cur.Version != version {
			return httpx.NewProblem(http.StatusConflict, CodeVersionConflict,
				fmt.Sprintf("requisition is at version %d, not %d; reload and retry", cur.Version, version))
		}
		if cur.Status != RequisitionDraft {
			return httpx.NewProblem(http.StatusConflict, CodeRequisitionState,
				"requisition is "+cur.Status+"; only a DRAFT can be edited (withdraw an IN_REVIEW one, or reopen a REJECTED one, first)")
		}
		currency, total := cur.Currency, cur.Total
		if edit.Lines != nil {
			currency, total = edit.Currency, edit.Total
			if _, err := tx.Exec(ctx, `delete from purchase_requisition_line where tenant_id = $1 and requisition_id = $2`, p.TenantID, id); err != nil {
				return err
			}
			for _, l := range edit.Lines {
				if _, err := tx.Exec(ctx, `insert into purchase_requisition_line (tenant_id, id, requisition_id, line_no, product_id, description,
					category, qty, unit, estimated_unit_price, amount, requested_date, site_id, preferred_vendor_id, status, catalog_entry_id, warehouse_id,
					gl_account, cost_center, catalog_source, supplier_part_id, needs_approval)
					values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, 'OPEN', $15, $16, $17, $18, $19, $20, $21)`,
					p.TenantID, ids.New(), id, l.LineNo, nullable(l.ProductID), l.Description, l.Category, l.Qty, l.Unit,
					l.EstimatedUnitPrice, l.Amount, l.RequestedDate.Time, nullable(l.SiteID), nullable(l.PreferredVendorID),
					nullable(l.CatalogEntryID), nullable(l.WarehouseID), nullable(l.GLAccount), nullable(l.CostCenter),
					nullable(l.CatalogSource), nullable(l.SupplierPartID), l.NeedsApproval); err != nil {
					return err
				}
			}
		}
		// A flagged punchout line marks the requisition for good (BR-35.13): an edit can set needs_approval, never clear it.
		flagged := slices.ContainsFunc(edit.Lines, func(l RequisitionLine) bool { return l.NeedsApproval })
		tag, err := tx.Exec(ctx, `update purchase_requisition set operating_unit_id = $3, currency = $4, total = $5, version = version + 1,
			needs_approval = needs_approval or $7, updated_at = now() where tenant_id = $1 and id = $2 and version = $6`,
			p.TenantID, id, nullable(edit.OperatingUnitID), currency, total, version, flagged)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return httpx.NewProblem(http.StatusConflict, CodeVersionConflict, "requisition changed since it was read; reload and retry")
		}
		cur.Version = version + 1
		if err := addRequisitionHistory(ctx, tx, cur, historyRow{Action: HistoryEdited, FromStatus: cur.Status, ActorID: edit.ActorID, Version: cur.Version}); err != nil {
			return err
		}
		out, err = loadRequisition(ctx, tx, id)
		return err
	})
	return out, err
}

// CancelRequisitionLine cancels one OPEN line of an APPROVED requisition while the rest continues (BR-17.1). It
// takes the requisition lock first and the line second, the order every writer of requisition lines uses, checks
// the caller's requisition version, refuses a line a running RFQ holds (PRC-REQ-006) and the last line that is
// not already cancelled (cancel the requisition instead). The total drops to the sum of the lines that are not
// cancelled, so it stays the amount the requisition still commits (the figure budget control pre-encumbers, and
// the one requisition.cancelled.v1 carries if the rest is cancelled later); the approved amount stays in the
// requisition.approved.v1 event and the history. The requisition stays APPROVED, or becomes CLOSED when every
// line left is already consolidated, as consolidation closes it (FR-17.1). Its version moves on, a LINE_CANCELLED
// row joins the history and requisitionline.cancelled.v1 is written in the same transaction. A replayed
// Idempotency-Key returns the requisition as it is now.
func (s *Postgres) CancelRequisitionLine(ctx context.Context, id string, version int, lineID, actorID, reason string, idem Idem) (Requisition, error) {
	p, _ := tenant.FromContext(ctx)
	var out Requisition
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `select 1 from purchase_requisition where tenant_id = $1 and id = $2 for update`, p.TenantID, id); err != nil {
			return err
		}
		cur, err := loadRequisition(ctx, tx, id)
		if err != nil {
			return err
		}
		if _, replay, err := claimKey(ctx, tx, idem, id); err != nil || replay {
			out = cur
			return err
		}
		if cur.Version != version {
			return httpx.NewProblem(http.StatusConflict, CodeVersionConflict,
				fmt.Sprintf("requisition is at version %d, not %d; reload and retry", cur.Version, version))
		}
		if cur.Status != RequisitionApproved {
			return httpx.NewProblem(http.StatusConflict, CodeRequisitionState,
				"requisition is "+cur.Status+"; a single line can be cancelled only on an APPROVED requisition")
		}
		var line *RequisitionLine
		live, open := 0, 0
		for i, l := range cur.Lines {
			if l.ID == lineID {
				line = &cur.Lines[i]
			}
			if l.Status != "CANCELLED" {
				live++
			}
			if l.Status == "OPEN" {
				open++
			}
		}
		if line == nil {
			return httpx.NewProblem(http.StatusNotFound, CodeRequisitionLineNotFound, "requisition line "+lineID+" not found on requisition "+cur.Number)
		}
		if _, err := tx.Exec(ctx, `select 1 from purchase_requisition_line where tenant_id = $1 and id = $2 for update`, p.TenantID, lineID); err != nil {
			return err
		}
		if line.Status != "OPEN" {
			return httpx.NewProblem(http.StatusConflict, CodeRequisitionState, fmt.Sprintf(
				"requisition %s line %d is %s; only an OPEN line can be cancelled", cur.Number, line.LineNo, line.Status))
		}
		if live == 1 {
			return httpx.NewProblem(http.StatusConflict, CodeRequisitionState, fmt.Sprintf(
				"requisition %s line %d is the last line that is not cancelled; cancel the requisition instead", cur.Number, line.LineNo))
		}
		if number, held, err := liveRFQOf(ctx, tx, lineID); err != nil {
			return err
		} else if held {
			return httpx.NewProblem(http.StatusConflict, CodeRequisitionOnRFQ, fmt.Sprintf(
				"requisition %s line %d is being sourced on %s; award or cancel the RFQ first", cur.Number, line.LineNo, number))
		}
		if err := enqueueBudgetRelease(ctx, tx, s.queueBudget, cur.LegalEntityID, id, []string{lineID}); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update purchase_requisition_line set status = 'CANCELLED' where tenant_id = $1 and id = $2`, p.TenantID, lineID); err != nil {
			return err
		}
		next := RequisitionApproved
		if open == 1 {
			next = RequisitionClosed // the lines left are all consolidated
		}
		var total decimal.Decimal
		err = tx.QueryRow(ctx, `update purchase_requisition set status = $4, version = version + 1, updated_at = now(),
			total = (select coalesce(sum(l.amount), 0) from purchase_requisition_line l where l.tenant_id = $1 and l.requisition_id = $2
				and l.status <> 'CANCELLED')
			where tenant_id = $1 and id = $2 and version = $3 returning total`, p.TenantID, id, version, next).Scan(&total)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusConflict, CodeVersionConflict, "requisition changed since it was read; reload and retry")
		}
		if err != nil {
			return err
		}
		from := cur.Status
		cur.Status, cur.Total, cur.Version = next, total, version+1
		if err := addRequisitionHistory(ctx, tx, cur, historyRow{Action: HistoryLineCancelled, FromStatus: from, ActorID: actorID,
			Comment: reason, LineID: lineID, Version: cur.Version}); err != nil {
			return err
		}
		if err := enqueue(ctx, tx, "requisition", id, purchasingevents.RequisitionLineCancelled, purchasingevents.RequisitionLineCancelledData{
			RequisitionID: id, Number: cur.Number, LegalEntityID: cur.LegalEntityID, RequesterID: cur.RequesterID, Currency: cur.Currency,
			LineID: lineID, LineNo: line.LineNo, ProductID: line.ProductID, Category: line.Category, Qty: line.Qty.String(),
			Amount: line.Amount.String(), Total: total.String(), RequisitionStatus: next, CancelledBy: actorID, Reason: reason,
			Version: cur.Version}); err != nil {
			return err
		}
		out, err = loadRequisition(ctx, tx, id)
		return err
	})
	return out, err
}
