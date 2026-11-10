package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	salesevents "github.com/BenjaminSRussell/passage/services/sales/internal/events"
)

const (
	CodeRMANotFound      = "SLS-0125"
	CodeRMAState         = "SLS-0119"
	CodeRMAExpired       = "SLS-0120"
	CodeARUnavailable    = "SLS-0121"
	CodeRMAInvoice       = "SLS-0122"
	CodeRMAOverReturn    = "SLS-0123"
	CodeRMAUpstream      = "SLS-0124"
	CodeRMASelfAuthorize = "AUTH-0003"

	rmaSequence        = "RMA"
	rmaIdempotencyKey  = "return_order_tenant_id_idempotency_key_key"
	rmaNumberUnique    = "return_order_tenant_id_legal_entity_id_rma_number_key"
	returnOrderColumns = `id::text, legal_entity_id::text, rma_number, customer_id::text, original_invoice_id::text, original_invoice_number,
		return_reason_code, status, currency::text, expires_at::text, version, gross::text, restocking::text, net::text, tax::text,
		credit_note_id, journal_posting_id, disposition_date::text, credit_posting_date::text, created_by, updated_by, created_at, updated_at`
	returnLineColumns = `id::text, line_no, product_id::text, site_id::text, warehouse_id::text, quantity_expected::text, quantity_received::text,
		quantity_accepted::text, unit, unit_price::text, unit_cost::text, credit_only, disposition_code, return_cost_method, restocking_pct::text,
		restocking_amount::text, inventory_receipt_id, credit_note_line_id`
)

var ErrInvoiceNotFound = errors.New("invoice not found")

// Invoice is the billing invoice an RMA returns goods from.
type Invoice struct {
	ID, Number, LegalEntityID, CustomerID, Currency string
	Returnable                                      bool
	Lines                                           []InvoiceLine
}

// InvoiceLine is one billed line; Net is the line total after discount and CreditedQty what billing credit notes
// already took back from it.
type InvoiceLine struct {
	LineNo                                int
	ProductID, OrderLineID, PackingSlipID string
	Qty, Net, CreditedQty                 decimal.Decimal
	TaxCode                               string
	Tax, CreditedTax                      decimal.Decimal
}

// InvoiceReader loads a billing invoice with its lines.
type InvoiceReader interface {
	ReadInvoice(ctx context.Context, legalEntityID, invoiceID string) (Invoice, error)
}

// CostReader answers the unit cost a returned line is valued at: the cost the goods shipped with, or the current average.
type CostReader interface {
	OriginalUnitCost(ctx context.Context, legalEntityID, packingSlipID string, orderLineIDs []string) (decimal.Decimal, bool, error)
	CurrentUnitCost(ctx context.Context, legalEntityID, productID, siteID, warehouseID string) (decimal.Decimal, bool, error)
}

// InventoryReceiver restocks an accepted RETURN_TO_STOCK line; Key is stable per line so a retry finds the same receipt.
type InventoryReceiver interface {
	ReceiveReturn(ctx context.Context, in ReturnReceipt) (string, error)
}

// ReturnReceipt is one RMA line received back into stock at its return cost.
type ReturnReceipt struct {
	RMAID, RMANumber, LineID, LegalEntityID, ProductID, SiteID, WarehouseID, Currency, Unit, PostingDate, Key string
	Quantity, UnitCost                                                                                        decimal.Decimal
}

// ARCreditor posts the RMA credit note to AR and the GL.
type ARCreditor interface {
	PostRMACredit(ctx context.Context, in ARCredit) (CreditNote, error)
}

// ARCredit is the credit-note request sales sends receivables (AC-30.5); receivables derives customer, currency and ledger itself.
type ARCredit struct {
	RMAID, RMANumber, LegalEntityID, InvoiceNumber, InvoiceID, PostingDate, IdempotencyKey string
	Gross, Restocking, Net                                                                 decimal.Decimal
	TaxLines                                                                               []ARTaxLine
	InvoiceLines                                                                           []ARInvoiceLine
}

type ARInvoiceLine struct {
	LineNo int             `json:"lineNo"`
	Qty    decimal.Decimal `json:"qty"`
}

type ARTaxLine struct {
	Code   string
	Amount decimal.Decimal
}

// CreditNote is the AR open item and journal produced from an RMA credit.
type CreditNote struct {
	OpenItemID, DocumentNumber, JournalPostingID string
}

// WithInventory sets the inventory collaborator used when accepted goods are restocked.
func (s *Postgres) WithInventory(r InventoryReceiver) *Postgres {
	s.inventory = r
	return s
}

// WithAR sets the receivables collaborator used on credit.
func (s *Postgres) WithAR(c ARCreditor) *Postgres {
	s.ar = c
	return s
}

// WithReturnSources sets the billing and cost readers an RMA is created from.
func (s *Postgres) WithReturnSources(i InvoiceReader, c CostReader) *Postgres {
	s.invoices, s.costs = i, c
	return s
}

// NewReturnLine is one line of a create request; price and quantity limits come from the invoice.
type NewReturnLine struct {
	LineNo           int
	ProductID        string
	SiteID           string
	WarehouseID      string
	Quantity         decimal.Decimal
	Unit             string
	Disposition      string
	ReturnCostMethod string
	RestockingPct    *decimal.Decimal
}

// NewReturnOrder is a validated create request.
type NewReturnOrder struct {
	LegalEntityID     string
	CustomerID        string
	OriginalInvoiceID string
	ReturnReasonCode  string
	IdempotencyKey    string
	RequestHash       string
	Actor             string
	Lines             []NewReturnLine
}

// ReceiveLine is the quantity received on one RMA line; lines left out receive nothing.
type ReceiveLine struct {
	LineNo   int
	Quantity decimal.Decimal
}

// InspectLine records the accepted quantity and final disposition of one received line.
type InspectLine struct {
	LineNo      int
	Accepted    decimal.Decimal
	Disposition string
}

// ReturnLine is a stored RMA line.
type ReturnLine struct {
	ID                 string           `json:"id"`
	LineNo             int              `json:"lineNo"`
	ProductID          string           `json:"productId"`
	SiteID             string           `json:"siteId,omitempty"`
	WarehouseID        string           `json:"warehouseId,omitempty"`
	QuantityExpected   decimal.Decimal  `json:"quantityExpected"`
	QuantityReceived   decimal.Decimal  `json:"quantityReceived"`
	QuantityAccepted   decimal.Decimal  `json:"quantityAccepted"`
	Unit               string           `json:"unit"`
	UnitPrice          decimal.Decimal  `json:"unitPrice"`
	UnitCost           *decimal.Decimal `json:"unitCost,omitempty"`
	CreditOnly         bool             `json:"creditOnly"`
	Disposition        string           `json:"disposition,omitempty"`
	ReturnCostMethod   string           `json:"returnCostMethod"`
	RestockingPct      *decimal.Decimal `json:"restockingPct,omitempty"`
	RestockingAmount   decimal.Decimal  `json:"restockingAmount"`
	InventoryReceiptID *string          `json:"inventoryReceiptId,omitempty"`
	CreditNoteLineID   *string          `json:"creditNoteLineId,omitempty"`
}

// ReturnOrder is a stored RMA header with lines.
type ReturnOrder struct {
	ID                string          `json:"id"`
	LegalEntityID     string          `json:"legalEntityId"`
	RMANumber         string          `json:"rmaNumber"`
	CustomerID        string          `json:"customerId"`
	OriginalInvoiceID string          `json:"originalInvoiceId"`
	OriginalInvoiceNo string          `json:"originalInvoiceNumber"`
	ReturnReasonCode  string          `json:"returnReasonCode"`
	Status            string          `json:"status"`
	Currency          string          `json:"currency"`
	ExpiresAt         *string         `json:"expiresAt,omitempty"`
	Version           int             `json:"version"`
	Gross             decimal.Decimal `json:"gross"`
	Restocking        decimal.Decimal `json:"restocking"`
	Net               decimal.Decimal `json:"net"`
	Tax               decimal.Decimal `json:"tax"`
	CreditNoteID      *string         `json:"creditNoteId,omitempty"`
	JournalPostingID  *string         `json:"journalPostingId,omitempty"`
	DispositionDate   *string         `json:"dispositionDate,omitempty"`
	CreditPostingDate *string         `json:"creditPostingDate,omitempty"`
	Lines             []ReturnLine    `json:"lines"`
	CreatedBy         string          `json:"createdBy"`
	UpdatedBy         string          `json:"updatedBy"`
	CreatedAt         time.Time       `json:"createdAt"`
	UpdatedAt         time.Time       `json:"updatedAt"`
}

// ReturnOrderFilter narrows the list.
type ReturnOrderFilter struct {
	LegalEntityID string
	CustomerID    string
	Status        string
	Limit         int
}

func rmaNotFound() error {
	return problem(http.StatusNotFound, CodeRMANotFound, "return order not found")
}

func rmaState(detail string) error {
	return problem(http.StatusConflict, CodeRMAState, detail)
}

func upstreamProblem(ctx context.Context, what string, err error) error {
	slog.ErrorContext(ctx, what+" failed", "err", err, "correlationId", middleware.GetReqID(ctx))
	var refused interface{ Refusal() (int, string) }
	if errors.As(err, &refused) {
		if status, code := refused.Refusal(); status >= http.StatusBadRequest && status < http.StatusInternalServerError {
			return problem(http.StatusUnprocessableEntity, CodeRMAUpstream, fmt.Sprintf("%s refused the request (%d %s)", what, status, code))
		}
	}
	return problem(http.StatusServiceUnavailable, CodeRMAUpstream, what+" is unavailable; the return order keeps its state, retry")
}

// restockable reports whether goods of the line can go back to stock: it names a site and a warehouse and its return
// cost is known (stored at create, or CURRENT_COST read when restocked).
func (l ReturnLine) restockable() bool {
	return l.SiteID != "" && l.WarehouseID != "" && (l.UnitCost != nil || l.ReturnCostMethod == domain.CostCurrent)
}

func (l ReturnLine) pendingRestock() bool {
	return l.Disposition == domain.DispositionReturnToStock && l.QuantityAccepted.IsPositive() && l.InventoryReceiptID == nil
}

func (o ReturnOrder) pendingRestocks() int {
	n := 0
	for _, l := range o.Lines {
		if l.pendingRestock() {
			n++
		}
	}
	return n
}

// CreateReturnOrder stores a REQUESTED RMA priced and limited by the original invoice. A repeat of Idempotency-Key
// with the same body returns the stored RMA.
func (s *Postgres) CreateReturnOrder(ctx context.Context, in NewReturnOrder) (ReturnOrder, bool, error) {
	p, _ := tenant.FromContext(ctx)
	if stored, found, err := s.returnByKey(ctx, p.TenantID, in); found || err != nil {
		return stored, false, err
	}
	if s.invoices == nil || s.costs == nil {
		return ReturnOrder{}, false, problem(http.StatusServiceUnavailable, CodeRMAUpstream, "billing is not configured; a return order cannot be priced")
	}
	invoice, err := s.invoices.ReadInvoice(ctx, in.LegalEntityID, in.OriginalInvoiceID)
	if errors.Is(err, ErrInvoiceNotFound) {
		return ReturnOrder{}, false, problem(http.StatusUnprocessableEntity, CodeRMAInvoice, "the original invoice does not exist")
	}
	if err != nil {
		return ReturnOrder{}, false, upstreamProblem(ctx, "billing", err)
	}
	if invoice.LegalEntityID != in.LegalEntityID || !invoice.Returnable || (in.CustomerID != "" && invoice.CustomerID != in.CustomerID) {
		return ReturnOrder{}, false, problem(http.StatusUnprocessableEntity, CodeRMAInvoice, "the original invoice is not a posted invoice of this legal entity and customer")
	}
	priced, err := s.priceReturnLines(ctx, in, invoice)
	if err != nil {
		return ReturnOrder{}, false, err
	}
	var out ReturnOrder
	created := false
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtext($1))`, p.TenantID+invoice.ID); err != nil {
			return err
		}
		if err := checkReturnable(ctx, tx, p.TenantID, invoice, priced); err != nil {
			return err
		}
		seq, err := nextSequence(ctx, tx, p.TenantID, in.LegalEntityID, rmaSequence)
		if err != nil {
			return err
		}
		id := ids.New()
		if _, err := tx.Exec(ctx, `insert into return_order (tenant_id, id, legal_entity_id, rma_number, customer_id, original_invoice_id, original_invoice_number,
				return_reason_code, status, currency, idempotency_key, request_hash, created_by, updated_by)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $13)`,
			p.TenantID, id, in.LegalEntityID, domain.FormatRMANumber(seq), invoice.CustomerID, invoice.ID, invoice.Number,
			in.ReturnReasonCode, domain.RMARequested, invoice.Currency, in.IdempotencyKey, in.RequestHash, in.Actor); err != nil {
			return err
		}
		for _, l := range priced {
			var site, warehouse, pct, cost, orderLine any
			if l.SiteID != "" {
				site = l.SiteID
			}
			if l.WarehouseID != "" {
				warehouse = l.WarehouseID
			}
			if l.RestockingPct != nil {
				pct = l.RestockingPct.String()
			}
			if l.unitCost != nil {
				cost = l.unitCost.String()
			}
			if l.orderLineID != "" {
				orderLine = l.orderLineID
			}
			if _, err := tx.Exec(ctx, `insert into return_order_line (tenant_id, id, legal_entity_id, return_order_id, line_no, product_id, site_id, warehouse_id,
					quantity_expected, unit, unit_price, unit_cost, credit_only, order_line_id, disposition_code, return_cost_method, restocking_pct)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)`,
				p.TenantID, ids.New(), in.LegalEntityID, id, l.LineNo, l.ProductID, site, warehouse, l.Quantity.String(), l.Unit, l.unitPrice.String(), cost,
				l.Disposition == domain.DispositionCreditOnly, orderLine, l.Disposition, l.ReturnCostMethod, pct); err != nil {
				return err
			}
		}
		created = true
		out, err = loadReturn(ctx, tx, p.TenantID, id, false)
		return err
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		if pgErr.ConstraintName == rmaIdempotencyKey {
			stored, found, lookupErr := s.returnByKey(ctx, p.TenantID, in)
			if found || lookupErr != nil {
				return stored, false, lookupErr
			}
		}
		return ReturnOrder{}, false, problem(http.StatusConflict, CodeRMAState, "the return order number was taken by a concurrent request; retry")
	}
	return out, created, err
}

type pricedLine struct {
	NewReturnLine
	unitPrice   decimal.Decimal
	unitCost    *decimal.Decimal
	orderLineID string
}

func (s *Postgres) returnByKey(ctx context.Context, tenantID string, in NewReturnOrder) (ReturnOrder, bool, error) {
	var out ReturnOrder
	found := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var id, hash string
		err := tx.QueryRow(ctx, `select id::text, request_hash from return_order where tenant_id = $1 and idempotency_key = $2`, tenantID, in.IdempotencyKey).Scan(&id, &hash)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if hash != in.RequestHash {
			return problem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
		}
		found = true
		out, err = loadReturn(ctx, tx, tenantID, id, false)
		return err
	})
	return out, found, err
}

func (s *Postgres) priceReturnLines(ctx context.Context, in NewReturnOrder, invoice Invoice) ([]pricedLine, error) {
	out := make([]pricedLine, 0, len(in.Lines))
	for _, l := range in.Lines {
		var qty, net decimal.Decimal
		slips := map[string][]string{}
		orderLine := ""
		for _, il := range invoice.Lines {
			if il.ProductID != l.ProductID {
				continue
			}
			qty, net = qty.Add(il.Qty), net.Add(il.Net)
			if il.PackingSlipID != "" {
				slips[il.PackingSlipID] = append(slips[il.PackingSlipID], il.OrderLineID)
			}
			if orderLine == "" {
				orderLine = il.OrderLineID
			}
		}
		if !qty.IsPositive() {
			return nil, problem(http.StatusUnprocessableEntity, CodeRMAInvoice, fmt.Sprintf("line %d: the product is not on the original invoice", l.LineNo))
		}
		pl := pricedLine{NewReturnLine: l, unitPrice: net.Div(qty).Round(6), orderLineID: orderLine}
		if pl.Unit == "" {
			pl.Unit = "EA"
		}
		if pl.ReturnCostMethod == "" {
			pl.ReturnCostMethod = domain.CostOriginal
		}
		switch pl.ReturnCostMethod {
		case domain.CostZero:
			zero := decimal.Zero
			pl.unitCost = &zero
		case domain.CostOriginal:
			if domain.PhysicalReturn(l.Disposition) {
				cost, err := s.originalCost(ctx, in.LegalEntityID, slips)
				if err != nil {
					return nil, err
				}
				if cost == nil {
					return nil, problem(http.StatusUnprocessableEntity, CodeRMAInvoice, fmt.Sprintf("line %d: the original shipment cost is unknown; use CURRENT_COST or ZERO", l.LineNo))
				}
				pl.unitCost = cost
			}
		}
		out = append(out, pl)
	}
	return out, nil
}

// originalCost takes the cost of the first packing slip that shipped the product; several slips at different costs use that one (ceiling).
func (s *Postgres) originalCost(ctx context.Context, legalEntityID string, slips map[string][]string) (*decimal.Decimal, error) {
	for slip, orderLines := range slips {
		cost, ok, err := s.costs.OriginalUnitCost(ctx, legalEntityID, slip, orderLines)
		if err != nil {
			return nil, upstreamProblem(ctx, "fulfillment", err)
		}
		if ok {
			return &cost, nil
		}
	}
	return nil, nil
}

func checkReturnable(ctx context.Context, tx pgx.Tx, tenantID string, invoice Invoice, lines []pricedLine) error {
	// Returnable is what was invoiced, less what billing credit notes already credited, less other live RMAs.
	invoiced, billingCredited := map[string]decimal.Decimal{}, map[string]decimal.Decimal{}
	for _, il := range invoice.Lines {
		invoiced[il.ProductID] = invoiced[il.ProductID].Add(il.Qty)
		billingCredited[il.ProductID] = billingCredited[il.ProductID].Add(il.CreditedQty)
	}
	rows, err := tx.Query(ctx, `select l.product_id::text, coalesce(sum(l.quantity_expected), 0)::text from return_order_line l
		join return_order o on o.tenant_id = l.tenant_id and o.id = l.return_order_id
		where o.tenant_id = $1 and o.original_invoice_id = $2 and o.status not in ('REJECTED', 'EXPIRED') group by l.product_id`, tenantID, invoice.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	returned := map[string]decimal.Decimal{}
	for rows.Next() {
		var product, qty string
		if err := rows.Scan(&product, &qty); err != nil {
			return err
		}
		returned[product] = decOrZero(&qty)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	asked := map[string]decimal.Decimal{}
	for _, l := range lines {
		asked[l.ProductID] = asked[l.ProductID].Add(l.Quantity)
	}
	for product, qty := range asked {
		if left := invoiced[product].Sub(billingCredited[product]).Sub(returned[product]); qty.GreaterThan(left) {
			return problem(http.StatusUnprocessableEntity, CodeRMAOverReturn,
				fmt.Sprintf("product %s: %s requested, only %s of %s invoiced is still returnable (%s credited by billing, %s on other RMAs)",
					product, qty, left, invoiced[product], billingCredited[product], returned[product]))
		}
	}
	return nil
}

// GetReturnOrder returns one RMA with its lines.
func (s *Postgres) GetReturnOrder(ctx context.Context, id string) (ReturnOrder, error) {
	p, _ := tenant.FromContext(ctx)
	var out ReturnOrder
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = loadReturn(ctx, tx, p.TenantID, id, false)
		return err
	})
	return out, err
}

// ListReturnOrders lists RMAs newest first.
func (s *Postgres) ListReturnOrders(ctx context.Context, f ReturnOrderFilter) ([]ReturnOrder, error) {
	p, _ := tenant.FromContext(ctx)
	out := []ReturnOrder{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+returnOrderColumns+` from return_order
			where tenant_id = $1 and ($2 = '' or legal_entity_id = nullif($2, '')::uuid) and ($3 = '' or customer_id = nullif($3, '')::uuid)
			and ($4 = '' or status = $4)
			order by created_at desc, id desc limit $5`, p.TenantID, f.LegalEntityID, f.CustomerID, f.Status, f.Limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		var headers []ReturnOrder
		for rows.Next() {
			o, err := scanReturn(rows)
			if err != nil {
				return err
			}
			headers = append(headers, o)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		for i := range headers {
			lines, err := loadReturnLines(ctx, tx, p.TenantID, headers[i].ID)
			if err != nil {
				return err
			}
			headers[i].Lines = lines
		}
		out = headers
		return nil
	})
	return out, err
}

// AuthorizeReturnOrder moves REQUESTED → AUTHORIZED and sets the 30-day expiry; whoever created the RMA may not authorize it.
func (s *Postgres) AuthorizeReturnOrder(ctx context.Context, id string, version int, actor string, now time.Time) (ReturnOrder, error) {
	return s.moveReturn(ctx, id, version, actor, now, domain.RMAAuthorize, func(ctx context.Context, tx pgx.Tx, tenantID string, o *ReturnOrder) error {
		if o.CreatedBy == actor {
			return problem(http.StatusForbidden, CodeRMASelfAuthorize, "the user who created a return order may not authorize it")
		}
		_, err := tx.Exec(ctx, `update return_order set authorized_by = $3 where tenant_id = $1 and id = $2`, tenantID, o.ID, actor)
		return err
	})
}

// RejectReturnOrder ends a REQUESTED or AUTHORIZED RMA before any goods are received (REJECTED); its quantity is
// returnable again on another RMA.
func (s *Postgres) RejectReturnOrder(ctx context.Context, id string, version int, actor string, now time.Time) (ReturnOrder, error) {
	return s.moveReturn(ctx, id, version, actor, now, domain.RMAReject, nil)
}

// ReceiveReturnOrder records the physical receipt (into quarantine); lines left out receive 0 and stock is booked only
// once inspection accepts goods.
func (s *Postgres) ReceiveReturnOrder(ctx context.Context, id string, version int, lines []ReceiveLine, actor string, now time.Time) (ReturnOrder, error) {
	return s.moveReturn(ctx, id, version, actor, now, domain.RMAReceive, func(ctx context.Context, tx pgx.Tx, tenantID string, o *ReturnOrder) error {
		byNo := map[int]decimal.Decimal{}
		for _, l := range lines {
			if _, dup := byNo[l.LineNo]; dup {
				return rmaState("each line may be received once per call")
			}
			byNo[l.LineNo] = l.Quantity
		}
		for no := range byNo {
			if !hasLine(o.Lines, no) {
				return rmaState(fmt.Sprintf("line %d is not on the return order", no))
			}
		}
		for i := range o.Lines {
			l := &o.Lines[i]
			qty := byNo[l.LineNo]
			if qty.IsNegative() || qty.GreaterThan(l.QuantityExpected) {
				return rmaState(fmt.Sprintf("line %d received quantity must be between 0 and %s", l.LineNo, l.QuantityExpected))
			}
			l.QuantityReceived = qty
			if _, err := tx.Exec(ctx, `update return_order_line set quantity_received = $4 where tenant_id = $1 and return_order_id = $2 and id = $3`,
				tenantID, o.ID, l.ID, qty.String()); err != nil {
				return err
			}
		}
		return nil
	})
}

// InspectReturnOrder records, per received line, the accepted quantity and the disposition; moves RECEIVED → INSPECTED.
// A line restocked at CURRENT_COST gets its cost read here, so a product with no on-hand average is refused at
// inspection (where the line can still be dispositioned CREDIT_ONLY) rather than after the disposition commit.
func (s *Postgres) InspectReturnOrder(ctx context.Context, id string, version int, lines []InspectLine, actor string, now time.Time) (ReturnOrder, error) {
	costs, err := s.inspectionCosts(ctx, id, lines)
	if err != nil {
		return ReturnOrder{}, err
	}
	return s.moveReturn(ctx, id, version, actor, now, domain.RMAInspect, func(ctx context.Context, tx pgx.Tx, tenantID string, o *ReturnOrder) error {
		byNo := map[int]InspectLine{}
		for _, l := range lines {
			if _, dup := byNo[l.LineNo]; dup || !hasLine(o.Lines, l.LineNo) {
				return rmaState(fmt.Sprintf("line %d is repeated or not on the return order", l.LineNo))
			}
			byNo[l.LineNo] = l
		}
		for i := range o.Lines {
			l := &o.Lines[i]
			got, inspected := byNo[l.LineNo]
			switch {
			case l.QuantityReceived.IsZero() && inspected:
				return rmaState(fmt.Sprintf("line %d received nothing; it cannot be inspected", l.LineNo))
			case l.QuantityReceived.IsPositive() && !inspected:
				return rmaState(fmt.Sprintf("line %d was received and must be inspected", l.LineNo))
			case !inspected:
				continue
			case got.Accepted.IsNegative() || got.Accepted.GreaterThan(l.QuantityReceived):
				return rmaState(fmt.Sprintf("line %d accepted quantity must be between 0 and %s", l.LineNo, l.QuantityReceived))
			case got.Disposition != domain.DispositionReturnToStock && got.Disposition != domain.DispositionCreditOnly:
				return rmaState(fmt.Sprintf("line %d disposition must be RETURN_TO_STOCK or CREDIT_ONLY", l.LineNo))
			case got.Disposition == domain.DispositionReturnToStock && got.Accepted.IsPositive() && !l.restockable():
				// A line created CREDIT_ONLY carries no stock destination and, at ORIGINAL_COST, no resolved cost: restocking
				// it would post empty dimensions or an unknown cost and leave the RMA DISPOSITIONED with no way forward.
				return rmaState(fmt.Sprintf("line %d has no site, warehouse or return cost to restock into; inspect it CREDIT_ONLY or create a RETURN_TO_STOCK return", l.LineNo))
			}
			l.QuantityAccepted, l.Disposition = got.Accepted, got.Disposition
			if _, err := tx.Exec(ctx, `update return_order_line set quantity_accepted = $4, disposition_code = $5 where tenant_id = $1 and return_order_id = $2 and id = $3`,
				tenantID, o.ID, l.ID, got.Accepted.String(), got.Disposition); err != nil {
				return err
			}
			if cost, ok := costs[l.LineNo]; ok && l.UnitCost == nil && got.Disposition == domain.DispositionReturnToStock && got.Accepted.IsPositive() {
				if _, err := tx.Exec(ctx, `update return_order_line set unit_cost = $4 where tenant_id = $1 and return_order_id = $2 and id = $3 and unit_cost is null`,
					tenantID, o.ID, l.ID, cost.String()); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// inspectionCosts reads the CURRENT_COST of each line the inspection restocks that has no stored cost yet.
func (s *Postgres) inspectionCosts(ctx context.Context, id string, lines []InspectLine) (map[int]decimal.Decimal, error) {
	current, err := s.GetReturnOrder(ctx, id)
	if err != nil || current.Status != domain.RMAReceived {
		// The state move reports a wrong status or a missing RMA.
		return nil, nil
	}
	out := map[int]decimal.Decimal{}
	for _, in := range lines {
		if in.Disposition != domain.DispositionReturnToStock || !in.Accepted.IsPositive() {
			continue
		}
		for _, l := range current.Lines {
			if l.LineNo != in.LineNo || l.UnitCost != nil || l.ReturnCostMethod != domain.CostCurrent || l.SiteID == "" || l.WarehouseID == "" {
				continue
			}
			if s.costs == nil {
				return nil, problem(http.StatusServiceUnavailable, CodeRMAUpstream, "inventory is not configured; the current cost cannot be read")
			}
			cost, ok, err := s.costs.CurrentUnitCost(ctx, current.LegalEntityID, l.ProductID, l.SiteID, l.WarehouseID)
			if err != nil {
				return nil, upstreamProblem(ctx, "inventory cost", err)
			}
			if !ok {
				return nil, problem(http.StatusUnprocessableEntity, CodeRMAInvoice,
					fmt.Sprintf("line %d: the product has no on-hand average cost at the site and warehouse; inspect it CREDIT_ONLY", l.LineNo))
			}
			out[l.LineNo] = cost
		}
	}
	return out, nil
}

func hasLine(lines []ReturnLine, no int) bool {
	for _, l := range lines {
		if l.LineNo == no {
			return true
		}
	}
	return false
}

// DispositionReturnOrder moves INSPECTED → DISPOSITIONED, then restocks the accepted RETURN_TO_STOCK quantity after that
// commit under the per-line key rma-receive:<lineID>. When a restock failed, repeating the call on the DISPOSITIONED
// return order retries only the lines without a receipt.
func (s *Postgres) DispositionReturnOrder(ctx context.Context, id string, version int, actor string, now time.Time) (ReturnOrder, error) {
	current, err := s.GetReturnOrder(ctx, id)
	if err != nil {
		return ReturnOrder{}, err
	}
	resume := current.Status == domain.RMADispositioned && current.pendingRestocks() > 0
	if resume && current.Version != version {
		return ReturnOrder{}, versionConflict()
	}
	if !resume {
		if current.Status == domain.RMAInspected && current.pendingRestocks() > 0 && s.inventory == nil {
			return ReturnOrder{}, problem(http.StatusServiceUnavailable, CodeRMAUpstream, "inventory is not configured; accepted goods cannot be restocked")
		}
		date := now.UTC().Format(dayLayout)
		if current, err = s.moveReturn(ctx, id, version, actor, now, domain.RMADisposition, func(ctx context.Context, tx pgx.Tx, tenantID string, o *ReturnOrder) error {
			_, err := tx.Exec(ctx, `update return_order set disposition_date = $3 where tenant_id = $1 and id = $2`, tenantID, o.ID, date)
			return err
		}); err != nil {
			return ReturnOrder{}, err
		}
	}
	if current.pendingRestocks() == 0 {
		return current, nil
	}
	if s.inventory == nil {
		return ReturnOrder{}, problem(http.StatusServiceUnavailable, CodeRMAUpstream, "inventory is not configured; accepted goods cannot be restocked")
	}
	if err := s.restock(ctx, current); err != nil {
		return ReturnOrder{}, err
	}
	return s.GetReturnOrder(ctx, id)
}

func (s *Postgres) restock(ctx context.Context, o ReturnOrder) error {
	p, _ := tenant.FromContext(ctx)
	postingDate := ""
	if o.DispositionDate != nil {
		postingDate = *o.DispositionDate
	}
	for _, l := range o.Lines {
		if !l.pendingRestock() {
			continue
		}
		cost := l.UnitCost
		if cost == nil && l.ReturnCostMethod != domain.CostCurrent {
			return problem(http.StatusUnprocessableEntity, CodeRMAInvoice, fmt.Sprintf("line %d: the return cost is unknown", l.LineNo))
		}
		if cost == nil {
			current, ok, err := s.costs.CurrentUnitCost(ctx, o.LegalEntityID, l.ProductID, l.SiteID, l.WarehouseID)
			if err != nil {
				return upstreamProblem(ctx, "inventory cost", err)
			}
			if !ok {
				return problem(http.StatusUnprocessableEntity, CodeRMAInvoice, fmt.Sprintf("line %d: no current cost for the product", l.LineNo))
			}
			cost = &current
			if err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
				_, err := tx.Exec(ctx, `update return_order_line set unit_cost = $4 where tenant_id = $1 and return_order_id = $2 and id = $3 and unit_cost is null`,
					p.TenantID, o.ID, l.ID, current.String())
				return err
			}); err != nil {
				return err
			}
		}
		receiptID, err := s.inventory.ReceiveReturn(ctx, ReturnReceipt{RMAID: o.ID, RMANumber: o.RMANumber, LineID: l.ID, LegalEntityID: o.LegalEntityID,
			ProductID: l.ProductID, SiteID: l.SiteID, WarehouseID: l.WarehouseID, Currency: o.Currency, Unit: l.Unit, PostingDate: postingDate,
			Key: "rma-receive:" + l.ID, Quantity: l.QuantityAccepted, UnitCost: *cost})
		if err != nil {
			return upstreamProblem(ctx, "inventory", err)
		}
		if err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `update return_order_line set inventory_receipt_id = $4 where tenant_id = $1 and return_order_id = $2 and id = $3 and inventory_receipt_id is null`,
				p.TenantID, o.ID, l.ID, receiptID)
			return err
		}); err != nil {
			return err
		}
	}
	return nil
}

// CreditReturnOrder computes the credit from the accepted quantities and stores it (DISPOSITIONED → CREDITED), asks
// receivables to post the note after that commit, then closes the RMA. When receivables failed, repeating the call on
// the CREDITED return order retries the post with the stored amounts and date.
//
// A return that leaves nothing to credit (no goods accepted and no credit-only line, or a restocking charge that takes
// the whole value) is closed by the same call with its zero net and no credit note: receivables is not asked.
func (s *Postgres) CreditReturnOrder(ctx context.Context, id string, version int, actor string, now time.Time) (ReturnOrder, error) {
	current, err := s.GetReturnOrder(ctx, id)
	if err != nil {
		return ReturnOrder{}, err
	}
	needsAR := current.Net.IsPositive()
	var invoice Invoice
	if current.Status == domain.RMADispositioned {
		plan, err := planCredit(current)
		if err != nil {
			return ReturnOrder{}, err
		}
		needsAR = plan.total.Net.IsPositive()
		if needsAR {
			if invoice, err = s.readTaxBasis(ctx, current); err != nil {
				return ReturnOrder{}, err
			}
		}
	}
	if needsAR && s.ar == nil {
		return ReturnOrder{}, problem(http.StatusServiceUnavailable, CodeARUnavailable, "receivables is not configured; the credit note cannot be posted")
	}
	if current.Status == domain.RMACredited {
		if current.Version != version {
			return ReturnOrder{}, versionConflict()
		}
	} else if current, err = s.moveReturn(ctx, id, version, actor, now, domain.RMACreditStart, s.storeCredit(now, invoice)); err != nil {
		return ReturnOrder{}, err
	}
	if !current.Net.IsPositive() {
		return s.moveReturn(ctx, id, current.Version, actor, now, domain.RMACredit, nil)
	}
	taxLines, err := s.creditTaxLines(ctx, current.ID)
	if err != nil {
		return ReturnOrder{}, err
	}
	invoiceLines, err := s.creditInvoiceLines(ctx, current.ID)
	if err != nil {
		return ReturnOrder{}, err
	}
	note, err := s.ar.PostRMACredit(ctx, ARCredit{RMAID: current.ID, RMANumber: current.RMANumber, LegalEntityID: current.LegalEntityID,
		InvoiceNumber: current.OriginalInvoiceNo, InvoiceID: current.OriginalInvoiceID, PostingDate: *current.CreditPostingDate, IdempotencyKey: "rma-credit-" + current.ID,
		Gross: current.Gross, Restocking: current.Restocking, Net: current.Net, TaxLines: taxLines, InvoiceLines: invoiceLines})
	if err != nil {
		return ReturnOrder{}, upstreamProblem(ctx, "receivables", err)
	}
	return s.moveReturn(ctx, id, current.Version, actor, now, domain.RMACredit, func(ctx context.Context, tx pgx.Tx, tenantID string, o *ReturnOrder) error {
		o.CreditNoteID, o.JournalPostingID = &note.OpenItemID, &note.JournalPostingID
		_, err := tx.Exec(ctx, `update return_order set credit_note_id = $3, journal_posting_id = $4 where tenant_id = $1 and id = $2`,
			tenantID, o.ID, note.OpenItemID, note.JournalPostingID)
		return err
	})
}

func (s *Postgres) storeCredit(now time.Time, invoice Invoice) func(context.Context, pgx.Tx, string, *ReturnOrder) error {
	return func(ctx context.Context, tx pgx.Tx, tenantID string, o *ReturnOrder) error {
		if n := o.pendingRestocks(); n > 0 {
			return rmaState(fmt.Sprintf("%d accepted line(s) are not restocked yet; repeat :disposition", n))
		}
		plan, err := planCredit(*o)
		if err != nil {
			return err
		}
		for i, idx := range plan.lines {
			l := &o.Lines[idx]
			l.RestockingAmount = plan.charges[i]
			if _, err := tx.Exec(ctx, `update return_order_line set restocking_amount = $4 where tenant_id = $1 and return_order_id = $2 and id = $3`,
				tenantID, o.ID, l.ID, l.RestockingAmount.String()); err != nil {
				return err
			}
		}
		date := now.UTC().Format(dayLayout)
		o.Gross, o.Restocking, o.Net = plan.total.Gross, plan.total.Restocking, plan.total.Net
		var creditLines []ARInvoiceLine
		if o.Tax, creditLines, err = storeReversedTax(ctx, tx, tenantID, *o, invoice, plan); err != nil {
			return err
		}
		stored := ""
		if len(creditLines) > 0 {
			raw, _ := json.Marshal(creditLines)
			stored = string(raw)
		}
		_, err = tx.Exec(ctx, `update return_order set gross = $3, restocking = $4, net = $5, tax = $6, credit_posting_date = $7,
			credit_invoice_lines = nullif($8, '')::jsonb where tenant_id = $1 and id = $2`,
			tenantID, o.ID, o.Gross.String(), o.Restocking.String(), o.Net.String(), o.Tax.String(), date, stored)
		return err
	}
}

// creditPlan is the credit note of an RMA: the totals and, per credited line (by index into the RMA's lines), its
// restocking charge with the per-note floor applied.
type creditPlan struct {
	total   domain.CreditNoteAmounts
	lines   []int
	charges []decimal.Decimal
	qty     []decimal.Decimal
}

func planCredit(o ReturnOrder) (creditPlan, error) {
	var plan creditPlan
	var in []domain.ReturnCredit
	for i, l := range o.Lines {
		qty := domain.CreditQuantity(l.CreditOnly, l.QuantityExpected, l.QuantityReceived, l.QuantityAccepted)
		if !qty.IsPositive() {
			continue
		}
		plan.lines = append(plan.lines, i)
		plan.qty = append(plan.qty, qty)
		in = append(in, domain.ReturnCredit{Disposition: l.Disposition, Quantity: qty, UnitPrice: l.UnitPrice, Currency: o.Currency, RestockingPct: l.RestockingPct})
	}
	if len(in) == 0 {
		return plan, nil
	}
	total, charges, err := domain.CreditNote(in)
	if err != nil {
		return creditPlan{}, rmaState(err.Error())
	}
	plan.total, plan.charges = total, charges
	return plan, nil
}

func (s *Postgres) moveReturn(ctx context.Context, id string, version int, actor string, now time.Time, action string, extra func(context.Context, pgx.Tx, string, *ReturnOrder) error) (ReturnOrder, error) {
	p, _ := tenant.FromContext(ctx)
	var out ReturnOrder
	var expired error
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		o, err := loadReturn(ctx, tx, p.TenantID, id, true)
		if err != nil {
			return err
		}
		if o.Version != version {
			return versionConflict()
		}
		if action == domain.RMAReceive && o.Status == domain.RMAAuthorized && o.ExpiresAt != nil && *o.ExpiresAt < now.UTC().Format(dayLayout) {
			if _, err := tx.Exec(ctx, `update return_order set status = $3, version = version + 1, updated_at = $4, updated_by = $5
				where tenant_id = $1 and id = $2`, p.TenantID, id, domain.RMAExpired, now, actor); err != nil {
				return err
			}
			expired = problem(http.StatusConflict, CodeRMAExpired, "the return order has expired")
			return nil
		}
		next, expires, err := domain.AdvanceRMA(o.Status, action, now)
		if err != nil {
			var ref *domain.RMARefusal
			if errors.As(err, &ref) {
				return rmaState(ref.Detail)
			}
			return err
		}
		if extra != nil {
			if err := extra(ctx, tx, p.TenantID, &o); err != nil {
				return err
			}
		}
		var exp any
		if expires != nil {
			day := expires.UTC().Format(dayLayout)
			exp = day
		}
		tag, err := tx.Exec(ctx, `update return_order set status = $4, version = version + 1, updated_at = $5, updated_by = $6, expires_at = coalesce($7::date, expires_at)
			where tenant_id = $1 and id = $2 and version = $3`, p.TenantID, id, version, next, now, actor, exp)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return versionConflict()
		}
		out, err = loadReturn(ctx, tx, p.TenantID, id, false)
		if err != nil {
			return err
		}
		if eventType := rmaEventType(action); eventType != "" {
			return enqueue(ctx, tx, "returnorder", id, eventType, p.TenantID, rmaEvent(out, actor))
		}
		return nil
	})
	if err == nil {
		err = expired
	}
	return out, err
}

func rmaEventType(action string) string {
	switch action {
	case domain.RMAAuthorize:
		return salesevents.ReturnOrderAuthorized
	case domain.RMAReceive:
		return salesevents.ReturnOrderReceived
	case domain.RMAInspect:
		return salesevents.ReturnOrderInspected
	case domain.RMADisposition:
		return salesevents.ReturnOrderDispositioned
	case domain.RMACredit:
		return salesevents.ReturnOrderCredited
	default:
		return ""
	}
}

func rmaEvent(o ReturnOrder, actor string) salesevents.ReturnOrderData {
	lines := make([]salesevents.ReturnOrderLine, 0, len(o.Lines))
	for _, l := range o.Lines {
		lines = append(lines, salesevents.ReturnOrderLine{
			LineID: l.ID, LineNo: l.LineNo, ProductID: l.ProductID, Disposition: l.Disposition,
			QuantityExpected: l.QuantityExpected.String(), QuantityReceived: l.QuantityReceived.String(),
			UnitPrice: l.UnitPrice.String(), Restocking: l.RestockingAmount.String(),
			InventoryReceiptID: l.InventoryReceiptID,
		})
	}
	return salesevents.ReturnOrderData{
		ReturnOrderID: o.ID, RMANumber: o.RMANumber, LegalEntityID: o.LegalEntityID, CustomerID: o.CustomerID,
		Status: o.Status, Currency: o.Currency, Gross: o.Gross.String(), Restocking: o.Restocking.String(), Net: o.Net.String(), Tax: o.Tax.String(),
		CreditNoteID: o.CreditNoteID, JournalPostingID: o.JournalPostingID, Lines: lines, Actor: actor, Version: o.Version,
	}
}

func loadReturn(ctx context.Context, tx pgx.Tx, tenantID, id string, lock bool) (ReturnOrder, error) {
	q := `select ` + returnOrderColumns + ` from return_order where tenant_id = $1 and id = $2`
	if lock {
		q += " for update"
	}
	o, err := scanReturn(tx.QueryRow(ctx, q, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return ReturnOrder{}, rmaNotFound()
	}
	if err != nil {
		return ReturnOrder{}, err
	}
	o.Lines, err = loadReturnLines(ctx, tx, tenantID, id)
	return o, err
}

func loadReturnLines(ctx context.Context, tx pgx.Tx, tenantID, id string) ([]ReturnLine, error) {
	rows, err := tx.Query(ctx, `select `+returnLineColumns+` from return_order_line where tenant_id = $1 and return_order_id = $2 order by line_no`, tenantID, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ReturnLine{}
	for rows.Next() {
		l, err := scanReturnLine(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

type scanner interface{ Scan(dest ...any) error }

func scanReturn(row scanner) (ReturnOrder, error) {
	var o ReturnOrder
	var expires, gross, restock, net, tax *string
	if err := row.Scan(&o.ID, &o.LegalEntityID, &o.RMANumber, &o.CustomerID, &o.OriginalInvoiceID, &o.OriginalInvoiceNo, &o.ReturnReasonCode, &o.Status, &o.Currency,
		&expires, &o.Version, &gross, &restock, &net, &tax, &o.CreditNoteID, &o.JournalPostingID, &o.DispositionDate, &o.CreditPostingDate,
		&o.CreatedBy, &o.UpdatedBy, &o.CreatedAt, &o.UpdatedAt); err != nil {
		return ReturnOrder{}, err
	}
	o.ExpiresAt = expires
	o.Gross, o.Restocking, o.Net, o.Tax = decOrZero(gross), decOrZero(restock), decOrZero(net), decOrZero(tax)
	return o, nil
}

func scanReturnLine(row scanner) (ReturnLine, error) {
	var l ReturnLine
	var site, warehouse, expected, received, accepted, price, cost, pct, restock, disposition *string
	if err := row.Scan(&l.ID, &l.LineNo, &l.ProductID, &site, &warehouse, &expected, &received, &accepted, &l.Unit, &price, &cost, &l.CreditOnly,
		&disposition, &l.ReturnCostMethod, &pct, &restock, &l.InventoryReceiptID, &l.CreditNoteLineID); err != nil {
		return ReturnLine{}, err
	}
	if site != nil {
		l.SiteID = *site
	}
	if warehouse != nil {
		l.WarehouseID = *warehouse
	}
	if disposition != nil {
		l.Disposition = *disposition
	}
	l.QuantityExpected, l.QuantityReceived, l.QuantityAccepted = decOrZero(expected), decOrZero(received), decOrZero(accepted)
	l.UnitPrice, l.RestockingAmount = decOrZero(price), decOrZero(restock)
	if cost != nil {
		d := decOrZero(cost)
		l.UnitCost = &d
	}
	if pct != nil && *pct != "" {
		d := decOrZero(pct)
		l.RestockingPct = &d
	}
	return l, nil
}

func decOrZero(s *string) decimal.Decimal {
	if s == nil || *s == "" {
		return decimal.Zero
	}
	d, err := decimal.NewFromString(*s)
	if err != nil {
		return decimal.Zero
	}
	return d
}
