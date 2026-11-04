package store

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

// Sourcing filters of the open-lines queue (FR-17.3, FR-17.14).
const (
	SourcingNeedsSourcing = "NEEDS_SOURCING" // no preferred vendor: start an RFQ or pick a vendor
	SourcingHasVendor     = "HAS_VENDOR"     // ready to consolidate with the preferred vendor
)

// SourcingLine is an approved requisition line with the header facts that
// consolidation needs.
type SourcingLine struct {
	RequisitionLine
	RequisitionID     string `json:"requisitionId"`
	RequisitionNumber string `json:"requisitionNumber"`
	RequisitionStatus string `json:"requisitionStatus"`
	LegalEntityID     string `json:"legalEntityId"`
	Currency          string `json:"currency"`
	// RequisitionVersion is the requisition's row version, the If-Match value of a split.
	RequisitionVersion int `json:"requisitionVersion"`
}

// SourcingFilter narrows ListOpenRequisitionLines; empty fields do not filter.
type SourcingFilter struct {
	Sourcing string
	VendorID string
}

const sourcingColumns = `l.id, l.line_no, coalesce(l.product_id::text, ''), l.description, l.category, l.qty, l.unit,
	l.estimated_unit_price, l.amount, l.requested_date, coalesce(l.site_id::text, ''), coalesce(l.preferred_vendor_id::text, ''),
	l.status, r.id, r.number, r.status, r.legal_entity_id, r.currency, coalesce(l.purchase_order_id::text, ''),
	coalesce(l.purchase_order_line_no, 0), coalesce(l.warehouse_id::text, ''), r.version, coalesce(l.split_from_line_id::text, '')`

func scanSourcingLine(row pgx.CollectableRow) (SourcingLine, error) {
	var s SourcingLine
	var requested time.Time
	err := row.Scan(&s.ID, &s.LineNo, &s.ProductID, &s.Description, &s.Category, &s.Qty, &s.Unit, &s.EstimatedUnitPrice, &s.Amount,
		&requested, &s.SiteID, &s.PreferredVendorID, &s.Status, &s.RequisitionID, &s.RequisitionNumber, &s.RequisitionStatus,
		&s.LegalEntityID, &s.Currency, &s.PurchaseOrderID, &s.PurchaseOrderLineNo, &s.WarehouseID, &s.RequisitionVersion, &s.SplitFromLineID)
	s.RequestedDate = day(requested)
	return s, err
}

// ListOpenRequisitionLines is the consolidation queue: OPEN lines of APPROVED
// requisitions that no running RFQ is sourcing (DRAFT, SENT, REPLIES_OPEN or CLOSED), newest first by line id. NEEDS_SOURCING keeps lines without a
// preferred vendor, HAS_VENDOR those with one; VendorID keeps one vendor's lines.
func (s *Postgres) ListOpenRequisitionLines(ctx context.Context, f SourcingFilter, after string, limit int) (Page[SourcingLine], error) {
	p, _ := tenant.FromContext(ctx)
	limit = clampLimit(limit)
	var items []SourcingLine
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+sourcingColumns+`
			from purchase_requisition_line l
			join purchase_requisition r on r.tenant_id = l.tenant_id and r.id = l.requisition_id
			where l.tenant_id = $1 and l.status = 'OPEN' and r.status = 'APPROVED'
			and ($2 = '' or ($2 = 'NEEDS_SOURCING') = (l.preferred_vendor_id is null))
			and ($3 = '' or l.preferred_vendor_id::text = $3)
			and not exists (select 1 from purchase_rfq_line rl join purchase_rfq q on q.tenant_id = rl.tenant_id and q.id = rl.rfq_id
				where rl.tenant_id = l.tenant_id and rl.requisition_line_id = l.id and q.status in ('DRAFT', 'SENT', 'REPLIES_OPEN', 'CLOSED'))
			and ($4::uuid is null or l.id < $4::uuid)
			order by l.id desc limit $5`, p.TenantID, f.Sourcing, f.VendorID, nullable(after), limit+1)
		if err != nil {
			return err
		}
		items, err = pgx.CollectRows(rows, scanSourcingLine)
		return err
	})
	if err != nil {
		return Page[SourcingLine]{}, err
	}
	if items == nil {
		items = []SourcingLine{}
	}
	return pageOf(items, limit, func(l SourcingLine) string { return l.ID }), nil
}

// RequisitionLinesByID loads requisition lines with their headers, in any
// status; the caller reports ids that are missing.
func (s *Postgres) RequisitionLinesByID(ctx context.Context, lineIDs []string) ([]SourcingLine, error) {
	var out []SourcingLine
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = loadSourcingLines(ctx, tx, lineIDs, false)
		return err
	})
	return out, err
}

func loadSourcingLines(ctx context.Context, tx pgx.Tx, lineIDs []string, lock bool) ([]SourcingLine, error) {
	p, _ := tenant.FromContext(ctx)
	q := `select ` + sourcingColumns + `
		from purchase_requisition_line l join purchase_requisition r on r.tenant_id = l.tenant_id and r.id = l.requisition_id
		where l.tenant_id = $1 and l.id = any($2::uuid[]) order by l.id`
	if lock {
		q += ` for update of l for share of r`
	}
	rows, err := tx.Query(ctx, q, p.TenantID, lineIDs)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanSourcingLine)
}

// ConsolidateRequisitionLines writes the POs built from approved requisition
// lines in one transaction (FR-17.3). insertPurchaseOrder claims each line
// (OPEN on an APPROVED requisition, same legal entity), links it to its PO line,
// draws any agreement line, and closes requisitions with no open lines left. A
// replayed Idempotency-Key returns the POs the lines were consolidated into.
func (s *Postgres) ConsolidateRequisitionLines(ctx context.Context, orders []PurchaseOrder, lineIDs []string, idem Idem) ([]PurchaseOrder, error) {
	var out []PurchaseOrder
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		_, replay, err := claimKey(ctx, tx, idem, ids.New())
		if err != nil {
			return err
		}
		if replay {
			out, err = loadLinkedOrders(ctx, tx, lineIDs)
			return err
		}
		locked, err := lockRequisitionLines(ctx, tx, orders)
		if err != nil {
			return err
		}
		if err := sameQuantities(orders, locked); err != nil {
			return err
		}
		for _, o := range orders {
			o.ID = ids.New()
			po, err := insertPurchaseOrder(ctx, tx, o)
			if err != nil {
				return err
			}
			out = append(out, po)
		}
		return nil
	})
	return out, err
}

// lockRequisitionLines locks every requisition line the orders reference in one statement, in line id order,
// before any order claims its own. A write that creates several POs (consolidation, an RFQ award) would
// otherwise lock them PO by PO, out of id order, and could deadlock with a consolidation or a new RFQ locking
// the same lines in id order.
func lockRequisitionLines(ctx context.Context, tx pgx.Tx, orders []PurchaseOrder) ([]SourcingLine, error) {
	var lineIDs []string
	for _, o := range orders {
		for _, l := range o.Lines {
			if l.RequisitionLineID != "" {
				lineIDs = append(lineIDs, l.RequisitionLineID)
			}
		}
	}
	if len(lineIDs) == 0 {
		return nil, nil
	}
	return loadSourcingLines(ctx, tx, lineIDs, true)
}

// sameQuantities refuses a consolidation built from lines read before a split (FR-17.3) changed them: each PO line
// orders its requisition line's quantity as the API read it, unlocked, so a split committed in between would have
// the PO order the whole old quantity while the parts cut from it stay OPEN to be ordered again.
func sameQuantities(orders []PurchaseOrder, locked []SourcingLine) error {
	byID := make(map[string]SourcingLine, len(locked))
	for _, s := range locked {
		byID[s.ID] = s
	}
	for _, o := range orders {
		for _, l := range o.Lines {
			if s, ok := byID[l.RequisitionLineID]; ok && !s.Qty.Equal(l.Qty) {
				return requisitionLineChanged(s)
			}
		}
	}
	return nil
}

// requisitionLineChanged is the 409 for a requisition line whose quantity moved (a split) since the caller read it.
func requisitionLineChanged(s SourcingLine) error {
	return httpx.NewProblem(http.StatusConflict, CodeRequisitionState, fmt.Sprintf(
		"requisition %s line %d now has %s %s (it was split since it was read); reload and retry",
		s.RequisitionNumber, s.LineNo, s.Qty, s.Unit))
}

// claimRequisitionLines locks the requisition lines a PO references and checks
// that each is OPEN on an APPROVED requisition of the PO's legal entity. It
// returns the requisition ids touched.
func claimRequisitionLines(ctx context.Context, tx pgx.Tx, po PurchaseOrder) ([]string, error) {
	var lineIDs []string
	for _, l := range po.Lines {
		if l.RequisitionLineID != "" {
			lineIDs = append(lineIDs, l.RequisitionLineID)
		}
	}
	if len(lineIDs) == 0 {
		return nil, nil
	}
	src, err := loadSourcingLines(ctx, tx, lineIDs, true)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]SourcingLine, len(src))
	for _, s := range src {
		byID[s.ID] = s
	}
	seen := map[string]bool{}
	var reqIDs []string
	for _, id := range lineIDs {
		s, ok := byID[id]
		if !ok {
			return nil, httpx.NewProblem(http.StatusNotFound, CodeRequisitionLineNotFound, "requisition line "+id+" not found")
		}
		if s.Status != "OPEN" || s.RequisitionStatus != RequisitionApproved {
			return nil, httpx.NewProblem(http.StatusConflict, CodeRequisitionState, fmt.Sprintf(
				"requisition %s line %d is %s on a %s requisition; only OPEN lines of APPROVED requisitions can be ordered",
				s.RequisitionNumber, s.LineNo, s.Status, s.RequisitionStatus))
		}
		if s.LegalEntityID != po.LegalEntityID {
			return nil, httpx.NewProblem(http.StatusUnprocessableEntity, CodeRequisitionLegalEntity, fmt.Sprintf(
				"requisition %s belongs to another legal entity than the purchase order", s.RequisitionNumber))
		}
		if number, held, err := liveRFQOf(ctx, tx, id); err != nil {
			return nil, err
		} else if held {
			return nil, httpx.NewProblem(http.StatusConflict, CodeRequisitionOnRFQ, fmt.Sprintf(
				"requisition %s line %d is being sourced on %s; award or cancel the RFQ first", s.RequisitionNumber, s.LineNo, number))
		}
		if !seen[s.RequisitionID] {
			seen[s.RequisitionID] = true
			reqIDs = append(reqIDs, s.RequisitionID)
		}
	}
	return reqIDs, nil
}

// linkRequisitionLines marks the PO's requisition lines CONSOLIDATED with their
// PO line, and closes each requisition whose lines are all consolidated or
// cancelled (FR-17.1 CLOSED).
func linkRequisitionLines(ctx context.Context, tx pgx.Tx, po PurchaseOrder, reqIDs []string) error {
	p, _ := tenant.FromContext(ctx)
	for _, l := range po.Lines {
		if l.RequisitionLineID == "" {
			continue
		}
		tag, err := tx.Exec(ctx, `update purchase_requisition_line set status = 'CONSOLIDATED', purchase_order_id = $3,
			purchase_order_line_no = $4 where tenant_id = $1 and id = $2 and status = 'OPEN'`,
			p.TenantID, l.RequisitionLineID, po.ID, l.LineNo)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return httpx.NewProblem(http.StatusConflict, CodeRequisitionState,
				"requisition line "+l.RequisitionLineID+" is ordered by more than one purchase order line")
		}
	}
	for _, id := range reqIDs {
		if _, err := tx.Exec(ctx, `update purchase_requisition set status = 'CLOSED', version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2 and status = 'APPROVED'
			and not exists (select 1 from purchase_requisition_line l where l.tenant_id = $1 and l.requisition_id = $2 and l.status = 'OPEN')`,
			p.TenantID, id); err != nil {
			return err
		}
	}
	return nil
}

// loadLinkedOrders returns the distinct POs the requisition lines were consolidated into.
func loadLinkedOrders(ctx context.Context, tx pgx.Tx, lineIDs []string) ([]PurchaseOrder, error) {
	p, _ := tenant.FromContext(ctx)
	rows, err := tx.Query(ctx, `select distinct purchase_order_id::text from purchase_requisition_line
		where tenant_id = $1 and id = any($2::uuid[]) and purchase_order_id is not null order by 1`, p.TenantID, lineIDs)
	if err != nil {
		return nil, err
	}
	poIDs, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	out := make([]PurchaseOrder, 0, len(poIDs))
	for _, id := range poIDs {
		po, err := loadPurchaseOrder(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, po)
	}
	return out, nil
}
