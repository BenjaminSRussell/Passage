package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
)

// PurchaseOrder is a PO in its pre-confirmation state (FR-17.7). Totals are
// persisted with the lines; a deferred constraint trigger enforces the header identity.
type PurchaseOrder struct {
	ID               string          `json:"id"`
	Number           string          `json:"number"`
	LegalEntityID    string          `json:"legalEntityId"`
	VendorID         string          `json:"vendorId"`
	SiteID           string          `json:"siteId,omitempty"`
	WarehouseID      string          `json:"warehouseId,omitempty"`
	Currency         string          `json:"currency"`
	ApprovalStatus   string          `json:"approvalStatus"`
	DocumentStatus   string          `json:"documentStatus"`
	VersionNo        int             `json:"versionNo"`
	ChangeManagement bool            `json:"changeManagement"`
	Subtotal         decimal.Decimal `json:"subtotal"`
	DiscountTotal    decimal.Decimal `json:"discountTotal"`
	ChargesTotal     decimal.Decimal `json:"chargesTotal"`
	TaxTotal         decimal.Decimal `json:"taxTotal"`
	Total            decimal.Decimal `json:"total"`
	RowVersion       int             `json:"rowVersion"`
	// CreatedBy, SubmittedBy and ChangedBy (the last user who edited or
	// repriced the content) are the users the four-eyes policy keeps from
	// approving or confirming the PO; empty for POs from before they were recorded.
	CreatedBy   string                `json:"createdBy,omitempty"`
	SubmittedBy string                `json:"submittedBy,omitempty"`
	ChangedBy   string                `json:"changedBy,omitempty"`
	Lines       []PurchaseOrderLine   `json:"lines"`
	Charges     []PurchaseOrderCharge `json:"charges"`
	// AutoChargeCodes are the charge codes auto charges have added to the PO (FR-17.9). An edit with
	// applyAutoCharges does not add them again, so a charge the buyer removed stays removed. On a
	// write it holds the codes to add to the stored set; the set never shrinks.
	AutoChargeCodes []string `json:"autoChargeCodes,omitempty"`
	// PendingAcknowledgement is set on a read of one PO while a vendor proposal waits for the buyer
	// (PendingAcknowledgementID, for the approve and decline routes); it is not stored.
	PendingAcknowledgement   bool      `json:"pendingAcknowledgement,omitempty"`
	PendingAcknowledgementID string    `json:"pendingAcknowledgementId,omitempty"`
	CreatedAt                time.Time `json:"createdAt"`
	UpdatedAt                time.Time `json:"updatedAt"`
	// Warnings are computed on create (PRC-AGR-004) and not stored.
	Warnings []Warning `json:"warnings,omitempty"`
}

// PurchaseOrderLine is one PO line. PriceSource is "MANUAL",
// "TRADE_AGREEMENT:<id>", "AGREEMENT:<agreement line id>" or
// "REQUISITION_ESTIMATE" so the price can be explained later.
type PurchaseOrderLine struct {
	// ID is the line's stable id (slice 18), the purchase_order_line_id a drop-ship names; set by the
	// database, ignored on a write.
	ID          string `json:"id,omitempty"`
	LineNo      int    `json:"lineNo"`
	ProductID   string `json:"productId,omitempty"`
	Description string `json:"description,omitempty"`
	// Category is the procurement category the category access rule checks (FR-17.20):
	// the requisition line's on a requisition-backed line, else the one given; fixed once written.
	Category string `json:"category,omitempty"`
	// SiteID and WarehouseID are where the line is delivered. On a read they are always the effective
	// values (the PO header's when the line has none of its own); a line stores a value only when it
	// differs from the header, so a line that follows the header keeps following it until confirmation.
	SiteID            string          `json:"siteId,omitempty"`
	WarehouseID       string          `json:"warehouseId,omitempty"`
	Qty               decimal.Decimal `json:"qty"`
	Unit              string          `json:"unit"`
	UnitPrice         decimal.Decimal `json:"unitPrice"`
	PriceUnit         decimal.Decimal `json:"priceUnit"`
	DiscountPct       decimal.Decimal `json:"discountPct"`
	GrossAmount       decimal.Decimal `json:"grossAmount"`
	DiscountAmount    decimal.Decimal `json:"discountAmount"`
	LineAmount        decimal.Decimal `json:"lineAmount"`
	TaxAmount         decimal.Decimal `json:"taxAmount"`
	DeliveryDate      domain.Day      `json:"deliveryDate"`
	PriceSource       string          `json:"priceSource"`
	RequisitionLineID string          `json:"requisitionLineId,omitempty"`
	AgreementLineID   string          `json:"agreementLineId,omitempty"`
	ReceivedQty       decimal.Decimal `json:"receivedQty"`
	InvoicedQty       decimal.Decimal `json:"invoicedQty"`
	Status            string          `json:"status"`
	// InvoicedRepriceSource is the trade agreement price source a reprice run
	// last handed to payables for this invoiced line (FR-17.22, BR-17.18), so a
	// later run does not hand the same price change over twice.
	InvoicedRepriceSource string `json:"invoicedRepriceSource,omitempty"`
}

// PurchaseOrderCharge is a priced header charge (FR-17.9).
type PurchaseOrderCharge struct {
	Seq    int             `json:"seq"`
	Calc   string          `json:"calc"`
	Value  decimal.Decimal `json:"value"`
	Amount decimal.Decimal `json:"amount"`
	// ChargeCodeID and ChargeCode name the charge code the charge was entered with; the sides below are
	// a copy taken then, so a later change to the code does not move the PO. A charge entered without
	// a code is debited to the item, payable to the PO vendor and allocated by amount.
	ChargeCodeID    string `json:"chargeCodeId,omitempty"`
	ChargeCode      string `json:"chargeCode,omitempty"`
	DebitType       string `json:"debitType"`
	CreditType      string `json:"creditType"`
	DebitAccountID  string `json:"debitAccountId,omitempty"`
	CreditAccountID string `json:"creditAccountId,omitempty"`
	VendorID        string `json:"vendorId,omitempty"`
	AllocationRule  string `json:"allocationRule"`
	// Auto marks a charge an auto charge added to the PO (FR-17.9).
	Auto bool `json:"auto"`
	// LineNo is the line a line charge belongs to (PER_UNIT takes that line's quantity, PERCENT its
	// amount after discount); 0 is a header charge over the whole order.
	LineNo int `json:"lineNo,omitempty"`
}

// LiveCharges turns the charges of a PO into pricing inputs over its live lines, which are priced in the
// order given. A line charge whose line is not among them (it was cancelled) is dropped with the line;
// the charges kept are returned beside their inputs and renumbered from 1.
func LiveCharges(charges []PurchaseOrderCharge, live []PurchaseOrderLine) ([]domain.ChargeInput, []PurchaseOrderCharge) {
	pos := make(map[int]int, len(live))
	for i, l := range live {
		pos[l.LineNo] = i + 1
	}
	inputs := make([]domain.ChargeInput, 0, len(charges))
	kept := make([]PurchaseOrderCharge, 0, len(charges))
	for _, c := range charges {
		in := domain.ChargeInput{Calc: c.Calc, Value: c.Value}
		if c.LineNo != 0 {
			at, ok := pos[c.LineNo]
			if !ok {
				continue
			}
			in.Line = at
		}
		inputs = append(inputs, in)
		c.Seq = len(kept) + 1
		kept = append(kept, c)
	}
	return inputs, kept
}

// insertCharge writes one header charge of a PO.
func insertCharge(ctx context.Context, tx pgx.Tx, poID string, c PurchaseOrderCharge) error {
	p, _ := tenant.FromContext(ctx)
	debit, credit, rule := c.DebitType, c.CreditType, c.AllocationRule
	if debit == "" {
		debit = "ITEM"
	}
	if credit == "" {
		credit = "VENDOR"
	}
	if rule == "" {
		rule = "AMOUNT"
	}
	_, err := tx.Exec(ctx, `insert into purchase_order_charge (tenant_id, purchase_order_id, seq, calc, value, amount, charge_code_id,
		charge_code, debit_type, credit_type, debit_account_id, credit_account_id, vendor_id, allocation_rule, auto, line_no)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)`,
		p.TenantID, poID, c.Seq, c.Calc, c.Value, c.Amount, nullable(c.ChargeCodeID), c.ChargeCode, debit, credit,
		nullable(c.DebitAccountID), nullable(c.CreditAccountID), nullable(c.VendorID), rule, c.Auto, nullableInt(c.LineNo))
	return err
}

// CreatePurchaseOrder stores a DRAFT PO with its priced lines and charges.
func (s *Postgres) CreatePurchaseOrder(ctx context.Context, in PurchaseOrder, idem Idem) (PurchaseOrder, error) {
	in.ID = ids.New()
	var out PurchaseOrder
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		existing, replay, err := claimKey(ctx, tx, idem, in.ID)
		if err != nil {
			return err
		}
		if replay {
			out, err = loadPurchaseOrder(ctx, tx, existing)
			return err
		}
		out, err = insertPurchaseOrder(ctx, tx, in)
		return err
	})
	return out, err
}

// insertPurchaseOrder numbers and writes a DRAFT PO inside the caller's tx
// (in.ID must be set). Before writing it claims the requisition lines the PO
// orders and releases its lines against their agreement lines (FR-17.5), so a
// PRC-AGR or PRC-REQ error rolls the whole document back; after writing it links
// the requisition lines to their PO lines (FR-17.3).
func insertPurchaseOrder(ctx context.Context, tx pgx.Tx, in PurchaseOrder) (PurchaseOrder, error) {
	p, _ := tenant.FromContext(ctx)
	reqIDs, err := claimRequisitionLines(ctx, tx, in)
	if err != nil {
		return PurchaseOrder{}, err
	}
	warnings, err := drawAgreements(ctx, tx, in)
	if err != nil {
		return PurchaseOrder{}, err
	}
	var seq int64
	if err := tx.QueryRow(ctx, `select nextval('purchase_order_number_seq')`).Scan(&seq); err != nil {
		return PurchaseOrder{}, err
	}
	in.Number = fmt.Sprintf("PO-%08d", seq)
	in.ApprovalStatus, in.DocumentStatus, in.VersionNo = "DRAFT", "NONE", 0
	if _, err := tx.Exec(ctx, `insert into purchase_order (tenant_id, id, legal_entity_id, number, vendor_id, site_id, warehouse_id,
		currency, approval_status, document_status, version_no, change_management, subtotal, discount_total, charges_total, tax_total, total, created_by,
		auto_charge_codes)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, coalesce($19::text[], '{}'))`,
		p.TenantID, in.ID, in.LegalEntityID, in.Number, in.VendorID, nullable(in.SiteID), nullable(in.WarehouseID), in.Currency,
		in.ApprovalStatus, in.DocumentStatus, in.VersionNo, in.ChangeManagement, in.Subtotal, in.DiscountTotal, in.ChargesTotal,
		in.TaxTotal, in.Total, userOrNil(p.UserID), in.AutoChargeCodes); err != nil {
		return PurchaseOrder{}, err
	}
	for _, l := range in.Lines {
		if _, err := tx.Exec(ctx, `insert into purchase_order_line (tenant_id, purchase_order_id, line_no, product_id, description,
			qty, unit, unit_price, price_unit, discount_pct, gross_amount, discount_amount, line_amount, tax_amount, delivery_date,
			price_source, requisition_line_id, agreement_line_id, category, site_id, warehouse_id, status)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, 'OPEN')`,
			p.TenantID, in.ID, l.LineNo, nullable(l.ProductID), l.Description, l.Qty, l.Unit, l.UnitPrice, l.PriceUnit,
			l.DiscountPct, l.GrossAmount, l.DiscountAmount, l.LineAmount, l.TaxAmount, l.DeliveryDate.Time, l.PriceSource,
			nullable(l.RequisitionLineID), nullable(l.AgreementLineID), l.Category,
			nullable(lineOverride(l.SiteID, in.SiteID)), nullable(lineOverride(l.WarehouseID, in.WarehouseID))); err != nil {
			return PurchaseOrder{}, err
		}
	}
	for _, c := range in.Charges {
		if err := insertCharge(ctx, tx, in.ID, c); err != nil {
			return PurchaseOrder{}, err
		}
	}
	if err := linkRequisitionLines(ctx, tx, in, reqIDs); err != nil {
		return PurchaseOrder{}, err
	}
	out, err := loadPurchaseOrder(ctx, tx, in.ID)
	out.Warnings = warnings
	return out, err
}

// GetPurchaseOrder returns one PO with its lines and charges.
func (s *Postgres) GetPurchaseOrder(ctx context.Context, id string) (PurchaseOrder, error) {
	var out PurchaseOrder
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		var err error
		if out, err = loadPurchaseOrder(ctx, tx, id); err != nil {
			return err
		}
		p, _ := tenant.FromContext(ctx)
		err = tx.QueryRow(ctx, `select coalesce((select id::text from purchase_order_ack where tenant_id = $1 and purchase_order_id = $2
			and outcome = 'CHANGES_PROPOSED' and decision is null), '')`, p.TenantID, id).Scan(&out.PendingAcknowledgementID)
		out.PendingAcknowledgement = out.PendingAcknowledgementID != ""
		return err
	})
	return out, err
}

func loadPurchaseOrder(ctx context.Context, tx pgx.Tx, id string) (PurchaseOrder, error) {
	p, _ := tenant.FromContext(ctx)
	var o PurchaseOrder
	err := tx.QueryRow(ctx, `select id, legal_entity_id, number, vendor_id, coalesce(site_id::text, ''), coalesce(warehouse_id::text, ''),
		currency, approval_status, document_status, version_no, change_management, subtotal, discount_total, charges_total, tax_total, total,
		row_version, created_at, updated_at, coalesce(created_by::text, ''), coalesce(submitted_by::text, ''),
		coalesce(changed_by::text, ''), auto_charge_codes::text[] from purchase_order where tenant_id = $1 and id = $2`, p.TenantID, id).
		Scan(&o.ID, &o.LegalEntityID, &o.Number, &o.VendorID, &o.SiteID, &o.WarehouseID, &o.Currency, &o.ApprovalStatus,
			&o.DocumentStatus, &o.VersionNo, &o.ChangeManagement, &o.Subtotal, &o.DiscountTotal, &o.ChargesTotal, &o.TaxTotal,
			&o.Total, &o.RowVersion, &o.CreatedAt, &o.UpdatedAt, &o.CreatedBy, &o.SubmittedBy, &o.ChangedBy, &o.AutoChargeCodes)
	if err != nil {
		return PurchaseOrder{}, notFound(err, CodePurchaseOrderNotFound, "purchase order not found")
	}
	lines, err := tx.Query(ctx, `select id::text, line_no, coalesce(product_id::text, ''), description, qty, unit, unit_price, price_unit,
		discount_pct, gross_amount, discount_amount, line_amount, tax_amount, delivery_date, price_source,
		coalesce(requisition_line_id::text, ''), coalesce(agreement_line_id::text, ''), received_qty, invoiced_qty, status,
		coalesce(invoiced_reprice_source, ''), category, coalesce(site_id::text, ''), coalesce(warehouse_id::text, '')
		from purchase_order_line where tenant_id = $1 and purchase_order_id = $2 order by line_no`, p.TenantID, id)
	if err != nil {
		return PurchaseOrder{}, err
	}
	defer lines.Close()
	o.Lines = []PurchaseOrderLine{}
	for lines.Next() {
		var l PurchaseOrderLine
		var due time.Time
		if err := lines.Scan(&l.ID, &l.LineNo, &l.ProductID, &l.Description, &l.Qty, &l.Unit, &l.UnitPrice, &l.PriceUnit, &l.DiscountPct,
			&l.GrossAmount, &l.DiscountAmount, &l.LineAmount, &l.TaxAmount, &due, &l.PriceSource, &l.RequisitionLineID,
			&l.AgreementLineID, &l.ReceivedQty, &l.InvoicedQty, &l.Status, &l.InvoicedRepriceSource, &l.Category, &l.SiteID, &l.WarehouseID); err != nil {
			return PurchaseOrder{}, err
		}
		if l.SiteID == "" {
			l.SiteID = o.SiteID
		}
		if l.WarehouseID == "" {
			l.WarehouseID = o.WarehouseID
		}
		l.DeliveryDate = day(due)
		o.Lines = append(o.Lines, l)
	}
	if err := lines.Err(); err != nil {
		return PurchaseOrder{}, err
	}
	charges, err := tx.Query(ctx, `select seq, calc, value, amount, coalesce(charge_code_id::text, ''), charge_code, debit_type, credit_type,
		coalesce(debit_account_id::text, ''), coalesce(credit_account_id::text, ''), coalesce(vendor_id::text, ''), allocation_rule, auto, coalesce(line_no, 0)
		from purchase_order_charge
		where tenant_id = $1 and purchase_order_id = $2 order by seq`, p.TenantID, id)
	if err != nil {
		return PurchaseOrder{}, err
	}
	defer charges.Close()
	o.Charges = []PurchaseOrderCharge{}
	for charges.Next() {
		var c PurchaseOrderCharge
		if err := charges.Scan(&c.Seq, &c.Calc, &c.Value, &c.Amount, &c.ChargeCodeID, &c.ChargeCode, &c.DebitType, &c.CreditType,
			&c.DebitAccountID, &c.CreditAccountID, &c.VendorID, &c.AllocationRule, &c.Auto, &c.LineNo); err != nil {
			return PurchaseOrder{}, err
		}
		o.Charges = append(o.Charges, c)
	}
	return o, charges.Err()
}

// PurchaseOrderSummary is a list row: the header and its totals without lines.
type PurchaseOrderSummary struct {
	ID             string          `json:"id"`
	Number         string          `json:"number"`
	LegalEntityID  string          `json:"legalEntityId"`
	VendorID       string          `json:"vendorId"`
	Currency       string          `json:"currency"`
	ApprovalStatus string          `json:"approvalStatus"`
	DocumentStatus string          `json:"documentStatus"`
	Total          decimal.Decimal `json:"total"`
	LineCount      int             `json:"lineCount"`
	RowVersion     int             `json:"rowVersion"`
	CreatedAt      time.Time       `json:"createdAt"`
}

// PurchaseOrderFilter narrows ListPurchaseOrders; empty fields do not filter.
type PurchaseOrderFilter struct {
	ApprovalStatus string
	VendorID       string
}

// ListPurchaseOrders returns PO headers newest first, after the cursor id.
func (s *Postgres) ListPurchaseOrders(ctx context.Context, f PurchaseOrderFilter, after string, limit int) (Page[PurchaseOrderSummary], error) {
	p, _ := tenant.FromContext(ctx)
	limit = clampLimit(limit)
	var items []PurchaseOrderSummary
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select o.id, o.number, o.legal_entity_id, o.vendor_id, o.currency, o.approval_status, o.document_status,
			o.total, (select count(*) from purchase_order_line l where l.tenant_id = o.tenant_id and l.purchase_order_id = o.id),
			o.row_version, o.created_at
			from purchase_order o
			where o.tenant_id = $1 and ($2 = '' or o.approval_status = $2) and ($3 = '' or o.vendor_id::text = $3)
			and ($4::uuid is null or o.id < $4::uuid)
			order by o.id desc limit $5`, p.TenantID, f.ApprovalStatus, f.VendorID, nullable(after), limit+1)
		if err != nil {
			return err
		}
		items, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (PurchaseOrderSummary, error) {
			var o PurchaseOrderSummary
			err := row.Scan(&o.ID, &o.Number, &o.LegalEntityID, &o.VendorID, &o.Currency, &o.ApprovalStatus, &o.DocumentStatus, &o.Total,
				&o.LineCount, &o.RowVersion, &o.CreatedAt)
			return o, err
		})
		return err
	})
	if err != nil {
		return Page[PurchaseOrderSummary]{}, err
	}
	if items == nil {
		items = []PurchaseOrderSummary{}
	}
	return pageOf(items, limit, func(o PurchaseOrderSummary) string { return o.ID }), nil
}

// lineOverride is the value a PO line stores for its site or warehouse: nothing when it is the
// header's (the line follows the header), the line's own value otherwise.
func lineOverride(line, header string) string {
	if line == header {
		return ""
	}
	return line
}
