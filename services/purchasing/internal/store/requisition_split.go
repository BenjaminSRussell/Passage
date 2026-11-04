package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	purchasingevents "github.com/BenjaminSRussell/passage/services/purchasing/internal/events"
)

// CodeRequisitionSplit is the 422 for quantities that cannot be cut out of the line.
const CodeRequisitionSplit = "PRC-REQ-007"

// CodeRequisitionQty is the 422 for a PO line that orders another quantity or unit than the requisition line it names.
const CodeRequisitionQty = "PRC-REQ-008"

// SplitRequisitionLine cuts new OPEN lines with the given quantities out of an OPEN line of an APPROVED
// requisition (FR-17.3), so the parts can go to different POs, vendors or an RFQ. It takes the line and its
// requisition under lock, checks the caller's requisition version, refuses a line a running RFQ holds
// (PRC-REQ-006, the hold slice 20 set up), and writes the new lines at the end of the requisition (numbered
// after its last line, each keeping the original's product, category, vendor, site, warehouse, requested date,
// catalog entry and unit price, and remembering the line it was cut from). The requisition total is unchanged;
// its version moves on and requisition.split.v1 is written in the same transaction. A replayed Idempotency-Key
// returns the requisition as it is now.
func (s *Postgres) SplitRequisitionLine(ctx context.Context, lineID string, version int, quantities []decimal.Decimal, minor int32, splitBy string, idem Idem) (Requisition, error) {
	p, _ := tenant.FromContext(ctx)
	var out Requisition
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		// Lock the requisition, then the line: the order every writer of requisition lines uses (see
		// loadSourcingLines), so a split cannot deadlock with a consolidation, a direct PO or an RFQ.
		if err := lockRequisitionsOf(ctx, tx, []string{lineID}); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `select 1 from purchase_requisition_line where tenant_id = $1 and id = $2 for update`, p.TenantID, lineID); err != nil {
			return err
		}
		lines, err := loadSourcingLines(ctx, tx, []string{lineID}, false)
		if err != nil {
			return err
		}
		if len(lines) != 1 {
			return httpx.NewProblem(http.StatusNotFound, CodeRequisitionLineNotFound, "requisition line "+lineID+" not found")
		}
		src := lines[0]
		if _, replay, err := claimKey(ctx, tx, idem, ids.New()); err != nil || replay {
			if err != nil {
				return err
			}
			out, err = loadRequisition(ctx, tx, src.RequisitionID)
			return err
		}
		if src.RequisitionVersion != version {
			return httpx.NewProblem(http.StatusConflict, CodeVersionConflict,
				fmt.Sprintf("requisition is at version %d, not %d; reload and retry", src.RequisitionVersion, version))
		}
		if src.Status != "OPEN" || src.RequisitionStatus != RequisitionApproved {
			return httpx.NewProblem(http.StatusConflict, CodeRequisitionState, fmt.Sprintf(
				"requisition %s line %d is %s on a %s requisition; only an OPEN line of an APPROVED requisition can be split",
				src.RequisitionNumber, src.LineNo, src.Status, src.RequisitionStatus))
		}
		if number, held, err := liveRFQOf(ctx, tx, lineID); err != nil {
			return err
		} else if held {
			return httpx.NewProblem(http.StatusConflict, CodeRequisitionOnRFQ, fmt.Sprintf(
				"requisition %s line %d is being sourced on %s; award or cancel the RFQ first", src.RequisitionNumber, src.LineNo, number))
		}
		res, err := domain.SplitRequisitionLine(src.Qty, src.Amount, quantities, minor)
		if err != nil {
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeRequisitionSplit,
				fmt.Sprintf("requisition %s line %d cannot be split: %s", src.RequisitionNumber, src.LineNo, err.Error()))
		}
		var lastNo int
		if err := tx.QueryRow(ctx, `select max(line_no) from purchase_requisition_line where tenant_id = $1 and requisition_id = $2`,
			p.TenantID, src.RequisitionID).Scan(&lastNo); err != nil {
			return err
		}
		data := purchasingevents.RequisitionSplitData{RequisitionID: src.RequisitionID, Number: src.RequisitionNumber,
			LegalEntityID: src.LegalEntityID, Currency: src.Currency, Version: version + 1, SplitBy: splitBy,
			Original: purchasingevents.RequisitionSplitLine{LineNo: src.LineNo, LineID: src.ID, Qty: res.Remaining.Qty.String(), Amount: res.Remaining.Amount.String()}}
		for i, piece := range res.Pieces {
			newID, no := ids.New(), lastNo+1+i
			if _, err := tx.Exec(ctx, `insert into purchase_requisition_line (tenant_id, id, requisition_id, line_no, product_id, description,
				category, qty, unit, estimated_unit_price, amount, requested_date, site_id, preferred_vendor_id, status, catalog_entry_id,
				warehouse_id, split_from_line_id)
				select tenant_id, $3, requisition_id, $4, product_id, description, category, $5, unit, estimated_unit_price, $6,
				requested_date, site_id, preferred_vendor_id, 'OPEN', catalog_entry_id, warehouse_id, id
				from purchase_requisition_line where tenant_id = $1 and id = $2`,
				p.TenantID, lineID, newID, no, piece.Qty, piece.Amount); err != nil {
				return err
			}
			data.NewLines = append(data.NewLines, purchasingevents.RequisitionSplitLine{LineNo: no, LineID: newID, Qty: piece.Qty.String(), Amount: piece.Amount.String()})
		}
		if _, err := tx.Exec(ctx, `update purchase_requisition_line set qty = $3, amount = $4 where tenant_id = $1 and id = $2`,
			p.TenantID, lineID, res.Remaining.Qty, res.Remaining.Amount); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update purchase_requisition set version = version + 1, updated_at = now() where tenant_id = $1 and id = $2`,
			p.TenantID, src.RequisitionID); err != nil {
			return err
		}
		if err := enqueue(ctx, tx, "requisition", src.RequisitionID, purchasingevents.RequisitionSplit, data); err != nil {
			return err
		}
		out, err = loadRequisition(ctx, tx, src.RequisitionID)
		return err
	})
	// Every writer locks requisitions first and lines second, so a deadlock is not expected; if Postgres still
	// aborts the split (40P01), the caller reloads and retries.
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "40P01" {
		return Requisition{}, httpx.NewProblem(http.StatusConflict, CodeVersionConflict,
			"the requisition is being ordered at the same time; reload and retry")
	}
	return out, err
}
