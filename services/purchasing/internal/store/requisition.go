package store

import (
	"context"
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

// Requisition statuses (FR-17.1).
const (
	RequisitionDraft    = "DRAFT"
	RequisitionInReview = "IN_REVIEW"
	RequisitionApproved = "APPROVED"
)

// Requisition is a purchase requisition (FR-17.1) with its lines.
type Requisition struct {
	ID              string            `json:"id"`
	Number          string            `json:"number"`
	LegalEntityID   string            `json:"legalEntityId"`
	OperatingUnitID string            `json:"operatingUnitId,omitempty"`
	RequesterID     string            `json:"requesterId"`
	Status          string            `json:"status"`
	Currency        string            `json:"currency"`
	Total           decimal.Decimal   `json:"total"`
	ApproverChain   []string          `json:"approverChain"`
	PolicyVersion   string            `json:"policyVersion,omitempty"`
	Version         int               `json:"version"`
	Lines           []RequisitionLine `json:"lines"`
	CreatedAt       time.Time         `json:"createdAt"`
	UpdatedAt       time.Time         `json:"updatedAt"`
	// Warnings are computed on create (PRC-REQ-003) and not stored.
	Warnings []Warning `json:"warnings,omitempty"`
}

// Warning is a non-blocking message tied to a line (PRD 3.17.18 severity warning).
type Warning struct {
	Code   string `json:"code"`
	LineNo int    `json:"lineNo"`
	Detail string `json:"detail"`
}

// RequisitionLine is one requested item. ProductID is empty for a non-catalog line.
type RequisitionLine struct {
	ID                 string          `json:"id"`
	LineNo             int             `json:"lineNo"`
	ProductID          string          `json:"productId,omitempty"`
	Description        string          `json:"description,omitempty"`
	Category           string          `json:"category,omitempty"`
	Qty                decimal.Decimal `json:"qty"`
	Unit               string          `json:"unit"`
	EstimatedUnitPrice decimal.Decimal `json:"estimatedUnitPrice"`
	Amount             decimal.Decimal `json:"amount"`
	RequestedDate      domain.Day      `json:"requestedDate"`
	SiteID             string          `json:"siteId,omitempty"`
	PreferredVendorID  string          `json:"preferredVendorId,omitempty"`
	Status             string          `json:"status"`
	// PurchaseOrderID and PurchaseOrderLineNo name the PO line that ordered a
	// CONSOLIDATED line (FR-17.3 commitment tracking).
	PurchaseOrderID     string `json:"purchaseOrderId,omitempty"`
	PurchaseOrderLineNo int    `json:"purchaseOrderLineNo,omitempty"`
}

// RequisitionSummary is a list row: the header without lines.
type RequisitionSummary struct {
	ID            string          `json:"id"`
	Number        string          `json:"number"`
	LegalEntityID string          `json:"legalEntityId"`
	RequesterID   string          `json:"requesterId"`
	Status        string          `json:"status"`
	Currency      string          `json:"currency"`
	Total         decimal.Decimal `json:"total"`
	LineCount     int             `json:"lineCount"`
	Version       int             `json:"version"`
	CreatedAt     time.Time       `json:"createdAt"`
}

// RequisitionFilter narrows ListRequisitions; empty fields do not filter.
type RequisitionFilter struct {
	Status      string
	RequesterID string
}

// CreateRequisition stores a DRAFT requisition with a fresh PR- number. Totals
// and line amounts are computed by the caller. A replayed Idempotency-Key
// returns the requisition created by the first request.
func (s *Postgres) CreateRequisition(ctx context.Context, in Requisition, idem Idem) (Requisition, error) {
	p, _ := tenant.FromContext(ctx)
	in.ID = ids.New()
	var out Requisition
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		existing, replay, err := claimKey(ctx, tx, idem, in.ID)
		if err != nil {
			return err
		}
		if replay {
			out, err = loadRequisition(ctx, tx, existing)
			return err
		}
		var seq int64
		if err := tx.QueryRow(ctx, `select nextval('purchase_requisition_number_seq')`).Scan(&seq); err != nil {
			return err
		}
		in.Number = fmt.Sprintf("PR-%08d", seq)
		in.Status, in.Version = RequisitionDraft, 1
		if _, err := tx.Exec(ctx, `insert into purchase_requisition (tenant_id, id, legal_entity_id, operating_unit_id, number, requester_id,
			status, currency, total, version)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
			p.TenantID, in.ID, in.LegalEntityID, nullable(in.OperatingUnitID), in.Number, in.RequesterID, in.Status, in.Currency,
			in.Total, in.Version); err != nil {
			return err
		}
		for _, l := range in.Lines {
			if _, err := tx.Exec(ctx, `insert into purchase_requisition_line (tenant_id, id, requisition_id, line_no, product_id, description,
				category, qty, unit, estimated_unit_price, amount, requested_date, site_id, preferred_vendor_id, status)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, 'OPEN')`,
				p.TenantID, ids.New(), in.ID, l.LineNo, nullable(l.ProductID), l.Description, l.Category, l.Qty, l.Unit,
				l.EstimatedUnitPrice, l.Amount, l.RequestedDate.Time, nullable(l.SiteID), nullable(l.PreferredVendorID)); err != nil {
				return err
			}
		}
		out, err = loadRequisition(ctx, tx, in.ID)
		out.Warnings = in.Warnings
		return err
	})
	return out, err
}

// GetRequisition returns one requisition with its lines.
func (s *Postgres) GetRequisition(ctx context.Context, id string) (Requisition, error) {
	var out Requisition
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = loadRequisition(ctx, tx, id)
		return err
	})
	return out, err
}

// ListRequisitions returns requisition headers newest first, after the cursor id.
func (s *Postgres) ListRequisitions(ctx context.Context, f RequisitionFilter, after string, limit int) (Page[RequisitionSummary], error) {
	p, _ := tenant.FromContext(ctx)
	limit = clampLimit(limit)
	var items []RequisitionSummary
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select r.id, r.number, r.legal_entity_id, r.requester_id, r.status, r.currency, r.total,
			(select count(*) from purchase_requisition_line l where l.tenant_id = r.tenant_id and l.requisition_id = r.id),
			r.version, r.created_at
			from purchase_requisition r
			where r.tenant_id = $1 and ($2 = '' or r.status = $2) and ($3 = '' or r.requester_id::text = $3)
			and ($4::uuid is null or r.id < $4::uuid)
			order by r.id desc limit $5`, p.TenantID, f.Status, f.RequesterID, nullable(after), limit+1)
		if err != nil {
			return err
		}
		items, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (RequisitionSummary, error) {
			var r RequisitionSummary
			err := row.Scan(&r.ID, &r.Number, &r.LegalEntityID, &r.RequesterID, &r.Status, &r.Currency, &r.Total, &r.LineCount,
				&r.Version, &r.CreatedAt)
			return r, err
		})
		return err
	})
	if err != nil {
		return Page[RequisitionSummary]{}, err
	}
	if items == nil {
		items = []RequisitionSummary{}
	}
	return pageOf(items, limit, func(r RequisitionSummary) string { return r.ID }), nil
}

// SubmitRequisition moves a DRAFT to IN_REVIEW with the approver chain, or to
// APPROVED when the chain is empty (auto-approval band). It checks the caller's
// version (optimistic concurrency) and writes requisition.submitted (and
// requisition.approved on auto-approval) in the same transaction. A replayed
// Idempotency-Key returns the requisition as it is now instead of a conflict.
func (s *Postgres) SubmitRequisition(ctx context.Context, id string, version int, chain []string, policyVersion string, idem Idem) (Requisition, error) {
	p, _ := tenant.FromContext(ctx)
	var out Requisition
	err := s.withTx(ctx, func(tx pgx.Tx) error {
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
			return httpx.NewProblem(http.StatusConflict, CodeRequisitionState, "requisition is "+cur.Status+"; only DRAFT can be submitted")
		}
		if chain == nil {
			chain = []string{}
		}
		cur.Status, cur.ApproverChain, cur.PolicyVersion = RequisitionInReview, chain, policyVersion
		if len(chain) == 0 {
			cur.Status = RequisitionApproved
		}
		tag, err := tx.Exec(ctx, `update purchase_requisition set status = $3, approver_chain = $4, policy_version = $5,
			version = version + 1, updated_at = now() where tenant_id = $1 and id = $2 and version = $6`,
			p.TenantID, id, cur.Status, cur.ApproverChain, cur.PolicyVersion, version)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return httpx.NewProblem(http.StatusConflict, CodeVersionConflict, "requisition changed since it was read; reload and retry")
		}
		data := requisitionData(cur)
		if err := enqueue(ctx, tx, "requisition", id, purchasingevents.RequisitionSubmitted, data); err != nil {
			return err
		}
		if cur.Status == RequisitionApproved {
			if err := enqueue(ctx, tx, "requisition", id, purchasingevents.RequisitionApproved, data); err != nil {
				return err
			}
		}
		out, err = loadRequisition(ctx, tx, id)
		return err
	})
	return out, err
}

func requisitionData(r Requisition) purchasingevents.RequisitionData {
	lines := make([]purchasingevents.RequisitionLine, 0, len(r.Lines))
	for _, l := range r.Lines {
		lines = append(lines, purchasingevents.RequisitionLine{LineNo: l.LineNo, LineID: l.ID, ProductID: l.ProductID, Category: l.Category,
			Qty: l.Qty.String(), Amount: l.Amount.String()})
	}
	return purchasingevents.RequisitionData{RequisitionID: r.ID, Number: r.Number, LegalEntityID: r.LegalEntityID, RequesterID: r.RequesterID,
		Currency: r.Currency, Lines: lines, Total: r.Total.String(), ApproverChain: r.ApproverChain, PolicyVersion: r.PolicyVersion,
		Status: r.Status}
}

func loadRequisition(ctx context.Context, tx pgx.Tx, id string) (Requisition, error) {
	p, _ := tenant.FromContext(ctx)
	var r Requisition
	err := tx.QueryRow(ctx, `select id, legal_entity_id, coalesce(operating_unit_id::text, ''), number, requester_id, status, currency,
		total, approver_chain, coalesce(policy_version, ''), version, created_at, updated_at
		from purchase_requisition where tenant_id = $1 and id = $2`, p.TenantID, id).
		Scan(&r.ID, &r.LegalEntityID, &r.OperatingUnitID, &r.Number, &r.RequesterID, &r.Status, &r.Currency, &r.Total, &r.ApproverChain,
			&r.PolicyVersion, &r.Version, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return Requisition{}, notFound(err, CodeRequisitionNotFound, "requisition not found")
	}
	rows, err := tx.Query(ctx, `select id, line_no, coalesce(product_id::text, ''), description, category, qty, unit,
		estimated_unit_price, amount, requested_date, coalesce(site_id::text, ''), coalesce(preferred_vendor_id::text, ''), status,
		coalesce(purchase_order_id::text, ''), coalesce(purchase_order_line_no, 0)
		from purchase_requisition_line where tenant_id = $1 and requisition_id = $2 order by line_no`, p.TenantID, id)
	if err != nil {
		return Requisition{}, err
	}
	defer rows.Close()
	r.Lines = []RequisitionLine{}
	for rows.Next() {
		var l RequisitionLine
		var requested time.Time
		if err := rows.Scan(&l.ID, &l.LineNo, &l.ProductID, &l.Description, &l.Category, &l.Qty, &l.Unit, &l.EstimatedUnitPrice,
			&l.Amount, &requested, &l.SiteID, &l.PreferredVendorID, &l.Status, &l.PurchaseOrderID, &l.PurchaseOrderLineNo); err != nil {
			return Requisition{}, err
		}
		l.RequestedDate = day(requested)
		r.Lines = append(r.Lines, l)
	}
	return r, rows.Err()
}
