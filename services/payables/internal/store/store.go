package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/fieldcrypt"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/payables/internal/domain"
)

const (
	eventSource      = "payables"
	DueDateEvent     = "EVENT"
	DueDateDefaulted = "DEFAULTED"
	defaultLimit     = 100
	maxLimit         = 500
)

var (
	OpenItemCreatedType  = events.Type("payables", "openitem", "created", 1)
	OpenItemSettledType  = events.Type("payables", "openitem", "settled", 1)
	OpenItemGroupedType  = events.Type("payables", "openitem", "grouped", 1)
	OpenItemHeldType     = events.Type("payables", "openitem", "held", 1)
	OpenItemReleasedType = events.Type("payables", "openitem", "released", 1)
	VendorBlockedType    = events.Type("payables", "vendor", "blocked", 1)
	VendorUnblockedType  = events.Type("payables", "vendor", "unblocked", 1)
)

var (
	ErrNotFound          = errors.New("open item not found")
	ErrVersionConflict   = errors.New("open item changed since it was read")
	ErrOverSettled       = errors.New("settlement exceeds the open amount")
	ErrInvalidAmount     = errors.New("settlement amount must be positive")
	ErrConflictingReplay = errors.New("source invoice already has an open item with different terms")
	ErrItemClosed        = errors.New("open item is settled or reversed")
)

type OpenItem struct {
	ID              string          `json:"id"`
	LegalEntityID   string          `json:"legalEntityId"`
	VendorID        string          `json:"vendorId"`
	VendorGroupID   string          `json:"vendorGroupId,omitempty"`
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
	PaymentHold     bool            `json:"paymentHold"`
	PayeeUnverified bool            `json:"-"`
}

type Filter struct {
	LegalEntityID string
	VendorID      string
	DueFrom       string
	DueOnOrBy     string
	Currency      string
	Status        string
	OpenOnly      bool
	AfterDue      string
	AfterID       string
}

type Postgres struct {
	pool  *pgxpool.Pool
	crypt *fieldcrypt.Keys
}

func (s *Postgres) WithFieldCrypt(k *fieldcrypt.Keys) *Postgres {
	s.crypt = k
	return s
}

func New(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

const columns = `id, legal_entity_id, vendor_id, coalesce(vendor_group_id::text, ''), source_invoice_id, invoice_number, invoice_date::text, due_date::text, due_date_source, currency,
	amount::text, open_amount::text, discount_date::text, discount_amount::text, status, version, payment_hold`

type rowScanner interface{ Scan(dest ...any) error }

func scan(row rowScanner) (OpenItem, error) {
	var o OpenItem
	var amount, open, discount string
	if err := row.Scan(&o.ID, &o.LegalEntityID, &o.VendorID, &o.VendorGroupID, &o.SourceInvoiceID, &o.InvoiceNumber, &o.InvoiceDate, &o.DueDate, &o.DueDateSource,
		&o.Currency, &amount, &open, &o.DiscountDate, &discount, &o.Status, &o.Version, &o.PaymentHold); err != nil {
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
	OpenItemID      string `json:"open_item_id"`
	LegalEntityID   string `json:"legal_entity_id"`
	VendorID        string `json:"vendor_id"`
	VendorGroupID   string `json:"vendor_group_id,omitempty"`
	SourceInvoiceID string `json:"source_invoice_id"`
	Currency        string `json:"currency"`
	OpenAmount      string `json:"open_amount"`
	Status          string `json:"status"`
	SettledAmount   string `json:"settled_amount,omitempty"`
	SettlementID    string `json:"settlement_id,omitempty"`
}

func enqueue(ctx context.Context, tx pgx.Tx, eventType string, o OpenItem, tenantID string, settled decimal.Decimal, settlementID string) error {
	payload := openItemEvent{
		OpenItemID: o.ID, LegalEntityID: o.LegalEntityID, VendorID: o.VendorID, VendorGroupID: o.VendorGroupID, SourceInvoiceID: o.SourceInvoiceID, Currency: o.Currency,
		OpenAmount: o.OpenAmount.String(), Status: o.Status, SettlementID: settlementID,
	}
	if eventType == OpenItemSettledType {
		payload.SettledAmount = settled.String()
	}
	return enqueueEvent(ctx, tx, eventType, "openitem", o.ID, tenantID, payload)
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
			invoice_date, due_date, due_date_source, currency, amount, open_amount, discount_date, discount_amount, status, vendor_group_id, payee_unverified)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $13, $9, $10, $10, $11, $12, 'OPEN', $14::uuid, $15)
			on conflict (tenant_id, source_invoice_id) do nothing
			returning `+columns,
			p.TenantID, ids.New(), in.LegalEntityID, in.VendorID, in.SourceInvoiceID, in.InvoiceNumber, in.InvoiceDate, in.DueDate,
			in.Currency, in.Amount.String(), in.DiscountDate, in.DiscountAmount.String(), dueSource, nullable(in.VendorGroupID), in.PayeeUnverified)
		var err error
		out, err = scan(row)
		if err == nil {
			created = true
			return enqueue(ctx, tx, OpenItemCreatedType, out, p.TenantID, decimal.Zero, "")
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		out, err = reselectBySource(ctx, tx, in.SourceInvoiceID)
		if err != nil {
			return err
		}
		if !out.Amount.Equal(in.Amount) || out.VendorID != in.VendorID || out.LegalEntityID != in.LegalEntityID || out.Currency != in.Currency {
			return ErrConflictingReplay
		}
		if in.VendorGroupID == "" || out.VendorGroupID != "" {
			return nil
		}
		grouped, err := scan(tx.QueryRow(ctx, `update ap_open_item set vendor_group_id = $2::uuid, updated_at = now()
			where source_invoice_id = $1 and vendor_group_id is null returning `+columns, in.SourceInvoiceID, in.VendorGroupID))
		if errors.Is(err, pgx.ErrNoRows) {
			out, err = reselectBySource(ctx, tx, in.SourceInvoiceID)
			return err
		}
		if err != nil {
			return err
		}
		out = grouped
		return enqueue(ctx, tx, OpenItemGroupedType, out, p.TenantID, decimal.Zero, "")
	})
	return out, created, err
}

func reselectBySource(ctx context.Context, tx pgx.Tx, sourceInvoiceID string) (OpenItem, error) {
	return scan(tx.QueryRow(ctx, `select `+columns+` from ap_open_item where source_invoice_id = $1`, sourceInvoiceID))
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (s *Postgres) List(ctx context.Context, f Filter, limit int) ([]OpenItem, error) {
	if limit <= 0 {
		limit = defaultLimit
	}
	limit = min(limit, maxLimit)
	items := []OpenItem{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+columns+` from ap_open_item
			where ($1::uuid is null or legal_entity_id = $1::uuid)
			and ($2::uuid is null or vendor_id = $2::uuid)
			and ($3::date is null or due_date >= $3::date)
			and ($4::date is null or due_date <= $4::date)
			and ($5::text is null or currency = $5::text)
			and ($6::text is null or status = $6::text)
			and (not $7::bool or status not in ('SETTLED', 'REVERSED'))
			and ($8::date is null or (due_date, id) > ($8::date, $9::uuid))
			order by due_date, id limit $10`,
			nullable(f.LegalEntityID), nullable(f.VendorID), nullable(f.DueFrom), nullable(f.DueOnOrBy), nullable(f.Currency), nullable(f.Status),
			f.OpenOnly, nullable(f.AfterDue), nullable(f.AfterID), limit)
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

func (s *Postgres) Get(ctx context.Context, id string) (OpenItem, error) {
	var out OpenItem
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scan(tx.QueryRow(ctx, `select `+columns+` from ap_open_item where id = $1::uuid`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	})
	return out, err
}

type holdEvent struct {
	OpenItemID    string `json:"open_item_id"`
	LegalEntityID string `json:"legal_entity_id"`
	VendorID      string `json:"vendor_id"`
	PaymentHold   bool   `json:"payment_hold"`
	Reason        string `json:"reason,omitempty"`
	Actor         string `json:"actor"`
}

type vendorBlockEvent struct {
	VendorID      string `json:"vendor_id"`
	LegalEntityID string `json:"legal_entity_id"`
	Reason        string `json:"reason,omitempty"`
	Actor         string `json:"actor"`
}

func enqueueEvent(ctx context.Context, tx pgx.Tx, eventType, aggregate, aggregateID, tenantID string, payload any) error {
	evt, err := events.New(eventType, eventSource, aggregateID, tenantID, payload)
	if err != nil {
		return err
	}
	return outbox.Enqueue(ctx, tx, aggregate, aggregateID, evt)
}

func (s *Postgres) SetPaymentHold(ctx context.Context, id string, held bool, reason string) (bool, error) {
	p, _ := tenant.FromContext(ctx)
	changed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := scan(tx.QueryRow(ctx, `select `+columns+` from ap_open_item where id = $1::uuid for update`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if cur.Status == "SETTLED" || cur.Status == "REVERSED" {
			return ErrItemClosed
		}
		if cur.PaymentHold == held {
			return nil
		}
		if !held {
			reason = ""
		}
		if _, err := tx.Exec(ctx, `update ap_open_item set payment_hold = $2, payment_hold_reason = $3, payment_held_by = $4, updated_at = now() where id = $1::uuid`,
			id, held, nullable(reason), nullable(heldBy(held, p.UserID))); err != nil {
			return err
		}
		changed = true
		eventType := OpenItemReleasedType
		if held {
			eventType = OpenItemHeldType
		}
		return enqueueEvent(ctx, tx, eventType, "openitem", id, p.TenantID, holdEvent{OpenItemID: id, LegalEntityID: cur.LegalEntityID, VendorID: cur.VendorID, PaymentHold: held, Reason: reason, Actor: p.UserID})
	})
	return changed, err
}

func heldBy(held bool, user string) string {
	if held {
		return user
	}
	return ""
}

func (s *Postgres) BlockVendor(ctx context.Context, legalEntityID, vendorID, reason string) error {
	p, _ := tenant.FromContext(ctx)
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `insert into ap_vendor_block (tenant_id, legal_entity_id, vendor_id, reason, blocked_by) values ($1, $2::uuid, $3::uuid, $4, $5)
			on conflict (tenant_id, legal_entity_id, vendor_id) do update set reason = excluded.reason, blocked_by = excluded.blocked_by`,
			p.TenantID, legalEntityID, vendorID, reason, p.UserID); err != nil {
			return err
		}
		return enqueueEvent(ctx, tx, VendorBlockedType, "vendor", vendorID, p.TenantID, vendorBlockEvent{VendorID: vendorID, LegalEntityID: legalEntityID, Reason: reason, Actor: p.UserID})
	})
}

func (s *Postgres) UnblockVendor(ctx context.Context, legalEntityID, vendorID string) error {
	p, _ := tenant.FromContext(ctx)
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `delete from ap_vendor_block where legal_entity_id = $1::uuid and vendor_id = $2::uuid`, legalEntityID, vendorID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return enqueueEvent(ctx, tx, VendorUnblockedType, "vendor", vendorID, p.TenantID, vendorBlockEvent{VendorID: vendorID, LegalEntityID: legalEntityID, Actor: p.UserID})
	})
}

func (s *Postgres) Settle(ctx context.Context, id string, version int, amount decimal.Decimal, settlementID string) (OpenItem, error) {
	p, _ := tenant.FromContext(ctx)
	var out OpenItem
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = settleTx(ctx, tx, p.TenantID, id, version, amount, settlementID)
		return err
	})
	return out, err
}

func settleTx(ctx context.Context, tx pgx.Tx, tenantID, id string, version int, amount decimal.Decimal, settlementID string) (OpenItem, error) {
	if !amount.IsPositive() {
		return OpenItem{}, ErrInvalidAmount
	}
	cur, err := scan(tx.QueryRow(ctx, `select `+columns+` from ap_open_item where id = $1 for update`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return OpenItem{}, ErrNotFound
	}
	if err != nil {
		return OpenItem{}, err
	}
	if cur.Version != version {
		return OpenItem{}, ErrVersionConflict
	}
	next := cur.OpenAmount.Sub(amount)
	if next.IsNegative() {
		return OpenItem{}, ErrOverSettled
	}
	status := "PARTIALLY_SETTLED"
	if next.IsZero() {
		status = "SETTLED"
	}
	out, err := scan(tx.QueryRow(ctx, `update ap_open_item set open_amount = $3::numeric, status = $4, version = version + 1, updated_at = now()
		where tenant_id = $1 and id = $2 returning `+columns, tenantID, id, next.String(), status))
	if err != nil {
		return OpenItem{}, err
	}
	return out, enqueue(ctx, tx, OpenItemSettledType, out, tenantID, amount, settlementID)
}

func (s *Postgres) AgingRows(ctx context.Context, legalEntityID, asOf string) ([]domain.AgingRow, error) {
	rows := []domain.AgingRow{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		res, err := tx.Query(ctx, `select vendor_id::text, coalesce(vendor_group_id::text, ''), currency, due_date, sum(open_amount)::text from ap_open_item
			where legal_entity_id = $1::uuid and invoice_date <= $2::date and open_amount > 0
			group by vendor_id, vendor_group_id, currency, due_date order by vendor_id, vendor_group_id, currency, due_date`, legalEntityID, asOf)
		if err != nil {
			return err
		}
		defer res.Close()
		for res.Next() {
			var r domain.AgingRow
			var open string
			if err := res.Scan(&r.VendorID, &r.VendorGroupID, &r.Currency, &r.DueDate, &open); err != nil {
				return err
			}
			if r.OpenAmount, err = decimal.NewFromString(open); err != nil {
				return err
			}
			rows = append(rows, r)
		}
		return res.Err()
	})
	return rows, err
}
