package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

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
	CodeRMANotFound    = "SLS-0118"
	CodeRMAState       = "SLS-0119"
	CodeRMAExpired     = "SLS-0120"
	CodeARUnavailable  = "SLS-0121"
	rmaSequence        = "RMA"
	returnOrderColumns = `id::text, legal_entity_id::text, rma_number, customer_id::text, original_invoice_id::text,
		return_reason_code, status, currency::text, expires_at::text, version, gross::text, restocking::text, net::text,
		credit_note_id, journal_posting_id, created_by, updated_by, created_at, updated_at`
	returnLineColumns = `id::text, line_no, product_id::text, site_id::text, warehouse_id::text,
		quantity_expected::text, quantity_received::text, unit, unit_price::text, disposition_code, return_cost_method,
		restocking_pct::text, restocking_amount::text, inventory_receipt_id, credit_note_line_id`
)

// InventoryReceiver restocks a RETURN_TO_STOCK line (wave 16 journals in production).
type InventoryReceiver interface {
	ReceiveReturn(ctx context.Context, in ReturnReceipt) (string, error)
}

// ReturnReceipt is one RMA line received back into inventory at return cost.
type ReturnReceipt struct {
	RMAID, RMANumber, LineID, ProductID, SiteID, WarehouseID, Currency string
	Quantity, UnitCost                                                 decimal.Decimal
}

// ARCreditor posts the RMA credit note to AR and the GL.
type ARCreditor interface {
	PostRMACredit(ctx context.Context, in ARCredit) (CreditNote, error)
}

// ARCredit is the credit-note request sales sends receivables (AC-30.5).
type ARCredit struct {
	RMAID, RMANumber, CustomerID, LegalEntityID, Currency, PostingDate, IdempotencyKey string
	Gross, Restocking, Net                                                             decimal.Decimal
}

// CreditNote is the AR open item and journal produced from an RMA credit.
type CreditNote struct {
	OpenItemID, DocumentNumber, JournalPostingID string
}

// WithInventory sets the inventory collaborator used on RETURN_TO_STOCK receive.
func (s *Postgres) WithInventory(r InventoryReceiver) *Postgres {
	s.inventory = r
	return s
}

// WithAR sets the receivables collaborator used on credit.
func (s *Postgres) WithAR(c ARCreditor) *Postgres {
	s.ar = c
	return s
}

// NewReturnLine is one line of a create request.
type NewReturnLine struct {
	LineNo           int
	ProductID        string
	SiteID           string
	WarehouseID      string
	Quantity         decimal.Decimal
	Unit             string
	UnitPrice        decimal.Decimal
	Disposition      string
	ReturnCostMethod string
	RestockingPct    *decimal.Decimal
}

// NewReturnOrder is a validated create request.
type NewReturnOrder struct {
	LegalEntityID     string
	CustomerID        string
	OriginalInvoiceID *string
	ReturnReasonCode  string
	Currency          string
	IdempotencyKey    string
	RequestHash       string
	Actor             string
	Lines             []NewReturnLine
}

// ReceiveLine is the quantity received on one RMA line.
type ReceiveLine struct {
	LineNo   int
	Quantity decimal.Decimal
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
	Unit               string           `json:"unit"`
	UnitPrice          decimal.Decimal  `json:"unitPrice"`
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
	OriginalInvoiceID *string         `json:"originalInvoiceId,omitempty"`
	ReturnReasonCode  string          `json:"returnReasonCode"`
	Status            string          `json:"status"`
	Currency          string          `json:"currency"`
	ExpiresAt         *string         `json:"expiresAt,omitempty"`
	Version           int             `json:"version"`
	Gross             decimal.Decimal `json:"gross"`
	Restocking        decimal.Decimal `json:"restocking"`
	Net               decimal.Decimal `json:"net"`
	CreditNoteID      *string         `json:"creditNoteId,omitempty"`
	JournalPostingID  *string         `json:"journalPostingId,omitempty"`
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

// CreateReturnOrder stores a REQUESTED RMA. A repeat of Idempotency-Key with
// the same body returns the stored RMA.
func (s *Postgres) CreateReturnOrder(ctx context.Context, in NewReturnOrder) (ReturnOrder, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out ReturnOrder
	created := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var existing, hash string
		err := tx.QueryRow(ctx, `select id::text, request_hash from return_order where tenant_id = $1 and idempotency_key = $2`,
			p.TenantID, in.IdempotencyKey).Scan(&existing, &hash)
		if err == nil {
			if hash != in.RequestHash {
				return problem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
			}
			out, err = loadReturn(ctx, tx, p.TenantID, existing, false)
			return err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		seq, err := nextSequence(ctx, tx, p.TenantID, in.LegalEntityID, rmaSequence)
		if err != nil {
			return err
		}
		id := ids.New()
		if _, err := tx.Exec(ctx, `insert into return_order (tenant_id, id, legal_entity_id, rma_number, customer_id, original_invoice_id,
				return_reason_code, status, currency, idempotency_key, request_hash, created_by, updated_by)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $12)`,
			p.TenantID, id, in.LegalEntityID, domain.FormatRMANumber(seq), in.CustomerID, in.OriginalInvoiceID,
			in.ReturnReasonCode, domain.RMARequested, in.Currency, in.IdempotencyKey, in.RequestHash, in.Actor); err != nil {
			return err
		}
		for _, l := range in.Lines {
			method := l.ReturnCostMethod
			if method == "" {
				method = domain.CostOriginal
			}
			unit := l.Unit
			if unit == "" {
				unit = "EA"
			}
			var site, warehouse any
			if l.SiteID != "" {
				site = l.SiteID
			}
			if l.WarehouseID != "" {
				warehouse = l.WarehouseID
			}
			var pct any
			if l.RestockingPct != nil {
				pct = l.RestockingPct.String()
			}
			if _, err := tx.Exec(ctx, `insert into return_order_line (tenant_id, id, return_order_id, line_no, product_id, site_id, warehouse_id,
					quantity_expected, unit, unit_price, disposition_code, return_cost_method, restocking_pct)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
				p.TenantID, ids.New(), id, l.LineNo, l.ProductID, site, warehouse, l.Quantity.String(), unit, l.UnitPrice.String(),
				l.Disposition, method, pct); err != nil {
				return err
			}
		}
		created = true
		out, err = loadReturn(ctx, tx, p.TenantID, id, false)
		return err
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return ReturnOrder{}, false, problem(http.StatusConflict, CodeIdempotencyReuse, "a concurrent request stored this return order first; retry to receive it")
	}
	return out, created, err
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
			where tenant_id = $1 and ($2 = '' or legal_entity_id::text = $2) and ($3 = '' or customer_id::text = $3)
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

// AuthorizeReturnOrder moves REQUESTED → AUTHORIZED and sets the 30-day expiry.
func (s *Postgres) AuthorizeReturnOrder(ctx context.Context, id string, version int, actor string, now time.Time) (ReturnOrder, error) {
	return s.moveReturn(ctx, id, version, actor, now, domain.RMAAuthorize, nil)
}

// ReceiveReturnOrder records received quantities and restocks RETURN_TO_STOCK lines.
func (s *Postgres) ReceiveReturnOrder(ctx context.Context, id string, version int, lines []ReceiveLine, actor string, now time.Time) (ReturnOrder, error) {
	return s.moveReturn(ctx, id, version, actor, now, domain.RMAReceive, func(ctx context.Context, tx pgx.Tx, tenantID string, o *ReturnOrder) error {
		byNo := map[int]decimal.Decimal{}
		for _, l := range lines {
			if _, dup := byNo[l.LineNo]; dup {
				return rmaState("each line may be received once per call")
			}
			byNo[l.LineNo] = l.Quantity
		}
		for i := range o.Lines {
			l := &o.Lines[i]
			qty, ok := byNo[l.LineNo]
			if !ok {
				qty = l.QuantityExpected
			}
			if qty.GreaterThan(l.QuantityExpected) || !qty.IsPositive() {
				return rmaState(fmt.Sprintf("line %d received quantity must be between 0 and %s", l.LineNo, l.QuantityExpected))
			}
			l.QuantityReceived = qty
			if _, err := tx.Exec(ctx, `update return_order_line set quantity_received = $4 where tenant_id = $1 and return_order_id = $2 and id = $3`,
				tenantID, o.ID, l.ID, qty.String()); err != nil {
				return err
			}
			if !domain.PhysicalReturn(l.Disposition) {
				continue
			}
			receipt := ReturnReceipt{RMAID: o.ID, RMANumber: o.RMANumber, LineID: l.ID, ProductID: l.ProductID,
				SiteID: l.SiteID, WarehouseID: l.WarehouseID, Currency: o.Currency, Quantity: qty, UnitCost: l.UnitPrice}
			rid := ids.New()
			if s.inventory != nil {
				var err error
				rid, err = s.inventory.ReceiveReturn(ctx, receipt)
				if err != nil {
					return err
				}
			}
			l.InventoryReceiptID = &rid
			if _, err := tx.Exec(ctx, `update return_order_line set inventory_receipt_id = $4 where tenant_id = $1 and return_order_id = $2 and id = $3`,
				tenantID, o.ID, l.ID, rid); err != nil {
				return err
			}
		}
		return nil
	})
}

// InspectReturnOrder moves RECEIVED → INSPECTED.
func (s *Postgres) InspectReturnOrder(ctx context.Context, id string, version int, actor string, now time.Time) (ReturnOrder, error) {
	return s.moveReturn(ctx, id, version, actor, now, domain.RMAInspect, nil)
}

// DispositionReturnOrder moves INSPECTED → DISPOSITIONED when every line has a disposition.
func (s *Postgres) DispositionReturnOrder(ctx context.Context, id string, version int, actor string, now time.Time) (ReturnOrder, error) {
	return s.moveReturn(ctx, id, version, actor, now, domain.RMADisposition, func(_ context.Context, _ pgx.Tx, _ string, o *ReturnOrder) error {
		for _, l := range o.Lines {
			if l.Disposition == "" {
				return rmaState(fmt.Sprintf("line %d has no disposition", l.LineNo))
			}
		}
		return nil
	})
}

// CreditReturnOrder posts the credit note (AR open item + GL) and closes the RMA.
func (s *Postgres) CreditReturnOrder(ctx context.Context, id string, version int, actor string, now time.Time) (ReturnOrder, error) {
	return s.moveReturn(ctx, id, version, actor, now, domain.RMACredit, func(ctx context.Context, tx pgx.Tx, tenantID string, o *ReturnOrder) error {
		if s.ar == nil {
			return problem(http.StatusServiceUnavailable, CodeARUnavailable, "receivables is not configured; the credit note cannot be posted")
		}
		var gross, restock, net decimal.Decimal
		for i := range o.Lines {
			l := &o.Lines[i]
			qty := l.QuantityReceived
			if qty.IsZero() {
				qty = l.QuantityExpected
			}
			amt, err := domain.CreditAmounts(domain.ReturnCredit{Disposition: l.Disposition, Quantity: qty, UnitPrice: l.UnitPrice,
				Currency: o.Currency, RestockingPct: l.RestockingPct})
			if err != nil {
				return rmaState(err.Error())
			}
			l.RestockingAmount = amt.Restocking
			gross, restock, net = gross.Add(amt.Gross), restock.Add(amt.Restocking), net.Add(amt.Net)
			if _, err := tx.Exec(ctx, `update return_order_line set restocking_amount = $4 where tenant_id = $1 and return_order_id = $2 and id = $3`,
				tenantID, o.ID, l.ID, amt.Restocking.String()); err != nil {
				return err
			}
		}
		note, err := s.ar.PostRMACredit(ctx, ARCredit{RMAID: o.ID, RMANumber: o.RMANumber, CustomerID: o.CustomerID,
			LegalEntityID: o.LegalEntityID, Currency: o.Currency, PostingDate: now.UTC().Format(dayLayout),
			IdempotencyKey: "rma-credit-" + o.ID, Gross: gross, Restocking: restock, Net: net})
		if err != nil {
			return err
		}
		o.Gross, o.Restocking, o.Net = gross, restock, net
		o.CreditNoteID, o.JournalPostingID = &note.OpenItemID, &note.JournalPostingID
		if _, err := tx.Exec(ctx, `update return_order set gross = $3, restocking = $4, net = $5, credit_note_id = $6, journal_posting_id = $7
			where tenant_id = $1 and id = $2`, tenantID, o.ID, gross.String(), restock.String(), net.String(), note.OpenItemID, note.JournalPostingID); err != nil {
			return err
		}
		return nil
	})
}

func (s *Postgres) moveReturn(ctx context.Context, id string, version int, actor string, now time.Time, action string, extra func(context.Context, pgx.Tx, string, *ReturnOrder) error) (ReturnOrder, error) {
	p, _ := tenant.FromContext(ctx)
	var out ReturnOrder
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
			return problem(http.StatusConflict, CodeRMAExpired, "the return order has expired")
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
			o.ExpiresAt = &day
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
		return enqueue(ctx, tx, "returnorder", id, rmaEventType(action), p.TenantID, rmaEvent(out, actor))
	})
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
	default:
		return salesevents.ReturnOrderCredited
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
		Status: o.Status, Currency: o.Currency, Gross: o.Gross.String(), Restocking: o.Restocking.String(), Net: o.Net.String(),
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
	var invoice, expires, gross, restock, net *string
	if err := row.Scan(&o.ID, &o.LegalEntityID, &o.RMANumber, &o.CustomerID, &invoice, &o.ReturnReasonCode, &o.Status, &o.Currency,
		&expires, &o.Version, &gross, &restock, &net, &o.CreditNoteID, &o.JournalPostingID, &o.CreatedBy, &o.UpdatedBy, &o.CreatedAt, &o.UpdatedAt); err != nil {
		return ReturnOrder{}, err
	}
	o.OriginalInvoiceID, o.ExpiresAt = invoice, expires
	o.Gross, o.Restocking, o.Net = decOrZero(gross), decOrZero(restock), decOrZero(net)
	return o, nil
}

func scanReturnLine(row scanner) (ReturnLine, error) {
	var l ReturnLine
	var site, warehouse, expected, received, price, pct, restock, disposition *string
	if err := row.Scan(&l.ID, &l.LineNo, &l.ProductID, &site, &warehouse, &expected, &received, &l.Unit, &price, &disposition,
		&l.ReturnCostMethod, &pct, &restock, &l.InventoryReceiptID, &l.CreditNoteLineID); err != nil {
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
	l.QuantityExpected, l.QuantityReceived, l.UnitPrice, l.RestockingAmount = decOrZero(expected), decOrZero(received), decOrZero(price), decOrZero(restock)
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
