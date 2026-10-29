package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	eventSource      = "payables"
	DueDateEvent     = "EVENT"
	DueDateDefaulted = "DEFAULTED"
	defaultLimit     = 100
	maxLimit         = 500
)

var (
	OpenItemCreatedType = events.Type("payables", "openitem", "created", 1)
	OpenItemSettledType = events.Type("payables", "openitem", "settled", 1)
)

var (
	ErrNotFound          = errors.New("open item not found")
	ErrVersionConflict   = errors.New("open item changed since it was read")
	ErrOverSettled       = errors.New("settlement exceeds the open amount")
	ErrInvalidAmount     = errors.New("settlement amount must be positive")
	ErrConflictingReplay = errors.New("source invoice already has an open item with different terms")
)

type OpenItem struct {
	ID              string          `json:"id"`
	LegalEntityID   string          `json:"legalEntityId"`
	VendorID        string          `json:"vendorId"`
	SourceInvoiceID string          `json:"sourceInvoiceId"`
	InvoiceNumber   string          `json:"invoiceNumber"`
	InvoiceDate     string          `json:"invoiceDate"`
	DueDate         string          `json:"dueDate"`
	DueDateSource   string          `json:"dueDateSource"`
	Currency        string          `json:"currency"`
	Amount          decimal.Decimal `json:"amount"`
	OpenAmount      decimal.Decimal `json:"openAmount"`
	DiscountDate    *string         `json:"discountDate,omitempty"`
	DiscountAmount  decimal.Decimal `json:"discountAmount"`
	Status          string          `json:"status"`
	Version         int             `json:"version"`
}

type Filter struct {
	VendorID  string
	DueOnOrBy string
	OpenOnly  bool
}

type Postgres struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

const columns = `id, legal_entity_id, vendor_id, source_invoice_id, invoice_number, invoice_date::text, due_date::text, due_date_source, currency,
	amount::text, open_amount::text, discount_date::text, discount_amount::text, status, version`

type rowScanner interface{ Scan(dest ...any) error }

func scan(row rowScanner) (OpenItem, error) {
	var o OpenItem
	var amount, open, discount string
	if err := row.Scan(&o.ID, &o.LegalEntityID, &o.VendorID, &o.SourceInvoiceID, &o.InvoiceNumber, &o.InvoiceDate, &o.DueDate, &o.DueDateSource,
		&o.Currency, &amount, &open, &o.DiscountDate, &discount, &o.Status, &o.Version); err != nil {
		return OpenItem{}, err
	}
	var err error
	if o.Amount, err = decimal.NewFromString(amount); err != nil {
		return OpenItem{}, err
	}
	if o.OpenAmount, err = decimal.NewFromString(open); err != nil {
		return OpenItem{}, err
	}
	o.DiscountAmount, err = decimal.NewFromString(discount)
	return o, err
}

type openItemEvent struct {
	OpenItemID    string `json:"open_item_id"`
	LegalEntityID string `json:"legal_entity_id"`
	VendorID      string `json:"vendor_id"`
	Currency      string `json:"currency"`
	OpenAmount    string `json:"open_amount"`
	Status        string `json:"status"`
	SettledAmount string `json:"settled_amount,omitempty"`
	SettlementID  string `json:"settlement_id,omitempty"`
}

func enqueue(ctx context.Context, tx pgx.Tx, eventType string, o OpenItem, tenantID string, settled decimal.Decimal, settlementID string) error {
	payload := openItemEvent{
		OpenItemID: o.ID, LegalEntityID: o.LegalEntityID, VendorID: o.VendorID, Currency: o.Currency,
		OpenAmount: o.OpenAmount.String(), Status: o.Status, SettlementID: settlementID,
	}
	if eventType == OpenItemSettledType {
		payload.SettledAmount = settled.String()
	}
	evt, err := events.New(eventType, eventSource, o.ID, tenantID, payload)
	if err != nil {
		return err
	}
	return outbox.Enqueue(ctx, tx, "openitem", o.ID, evt)
}

func (s *Postgres) CreateFromInvoice(ctx context.Context, in OpenItem) (OpenItem, bool, error) {
	p, _ := tenant.FromContext(ctx)
	dueSource := in.DueDateSource
	if dueSource == "" {
		dueSource = DueDateEvent
	}
	var out OpenItem
	created := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `insert into ap_open_item (tenant_id, id, legal_entity_id, vendor_id, source_invoice_id, invoice_number,
			invoice_date, due_date, due_date_source, currency, amount, open_amount, discount_date, discount_amount, status)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $13, $9, $10, $10, $11, $12, 'OPEN')
			on conflict (tenant_id, source_invoice_id) do nothing
			returning `+columns,
			p.TenantID, ids.New(), in.LegalEntityID, in.VendorID, in.SourceInvoiceID, in.InvoiceNumber, in.InvoiceDate, in.DueDate,
			in.Currency, in.Amount.String(), in.DiscountDate, in.DiscountAmount.String(), dueSource)
		var err error
		out, err = scan(row)
		if err == nil {
			created = true
			return enqueue(ctx, tx, OpenItemCreatedType, out, p.TenantID, decimal.Zero, "")
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		out, err = scan(tx.QueryRow(ctx, `select `+columns+` from ap_open_item where source_invoice_id = $1`, in.SourceInvoiceID))
		if err != nil {
			return err
		}
		if !out.Amount.Equal(in.Amount) || out.VendorID != in.VendorID || out.Currency != in.Currency {
			return ErrConflictingReplay
		}
		return nil
	})
	return out, created, err
}

func (s *Postgres) List(ctx context.Context, f Filter, limit int) ([]OpenItem, error) {
	if limit <= 0 {
		limit = defaultLimit
	}
	limit = min(limit, maxLimit)
	items := []OpenItem{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+columns+` from ap_open_item
			where ($1::text = '' or vendor_id::text = $1::text)
			and ($2::text = '' or due_date <= $2::date)
			and (not $3::bool or status not in ('SETTLED', 'REVERSED'))
			order by due_date, id limit $4`, f.VendorID, f.DueOnOrBy, f.OpenOnly, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			o, err := scan(rows)
			if err != nil {
				return err
			}
			items = append(items, o)
		}
		return rows.Err()
	})
	return items, err
}

func (s *Postgres) Settle(ctx context.Context, id string, version int, amount decimal.Decimal, settlementID string) (OpenItem, error) {
	p, _ := tenant.FromContext(ctx)
	if !amount.IsPositive() {
		return OpenItem{}, ErrInvalidAmount
	}
	var out OpenItem
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := scan(tx.QueryRow(ctx, `select `+columns+` from ap_open_item where id = $1 for update`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if cur.Version != version {
			return ErrVersionConflict
		}
		next := cur.OpenAmount.Sub(amount)
		if next.IsNegative() {
			return ErrOverSettled
		}
		status := "PARTIALLY_SETTLED"
		if next.IsZero() {
			status = "SETTLED"
		}
		out, err = scan(tx.QueryRow(ctx, `update ap_open_item set open_amount = $3::numeric, status = $4, version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2 returning `+columns, p.TenantID, id, next.String(), status))
		if err != nil {
			return err
		}
		return enqueue(ctx, tx, OpenItemSettledType, out, p.TenantID, amount, settlementID)
	})
	return out, err
}
