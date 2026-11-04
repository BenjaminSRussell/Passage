package store

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
)

// Purchasing inquiries (PRD 3.17.14): read-only views over purchase orders. Open value is the
// line amount pro rata to the quantity still to receive, rounded half to even to the currency's
// minor units; amounts of different currencies are never added together, so the vendor view
// groups by currency as well.
//
// A line is due on the date the vendor confirmed when it did, otherwise on the requested date.
// For a line with a delivery schedule, the received quantity is taken off the schedule rows in
// row order and the line is due on the first row not yet covered (its confirmed date, else its
// own date); a line without a schedule is due on its delivery date. Legal entities outside the
// caller's scope are left out by row-level security on purchase_order (and its child tables).

// openLineSource is the FROM clause the open-order inquiries share: OPEN lines of CONFIRMED POs
// with quantity still to receive, with the due date (due), the confirmed date of the schedule
// row the line waits on (due_confirmed) and the unrounded open value (open_value).
const openLineSource = `purchase_order_line l
	join purchase_order o on o.tenant_id = l.tenant_id and o.id = l.purchase_order_id
	left join lateral (
	  select coalesce(s.confirmed_date, s.delivery_date) as due, s.confirmed_date
	  from (select ds.seq, ds.delivery_date, ds.confirmed_date, sum(ds.qty) over (order by ds.seq) as cum
	        from purchase_delivery_schedule ds
	        where ds.tenant_id = l.tenant_id and ds.purchase_order_id = l.purchase_order_id and ds.line_no = l.line_no) s
	  where s.cum > l.received_qty
	  order by s.seq limit 1) nx on true`

const openLineWhere = `l.tenant_id = $1 and o.approval_status = 'CONFIRMED' and l.status = 'OPEN' and l.qty > l.received_qty`

// VendorOpenOrders is one row of "open orders by vendor": a vendor and currency with their
// CONFIRMED purchase orders that still have quantity to receive.
type VendorOpenOrders struct {
	VendorID     string          `json:"vendorId"`
	Currency     string          `json:"currency"`
	OrderCount   int             `json:"orderCount"`
	LineCount    int             `json:"lineCount"`
	OpenValue    decimal.Decimal `json:"openValue"`
	OverdueLines int             `json:"overdueLines"`
	// EarliestDue is the earliest due date (confirmed, else requested) of the open lines.
	EarliestDue  domain.Day      `json:"earliestDue"`
	OverdueValue decimal.Decimal `json:"overdueValue"`
}

// OverdueLine is an open line of a CONFIRMED purchase order whose due date is before the as-of
// date, with the days it is late. DeliveryDate is the requested date; ConfirmedDate here is the
// vendor's confirmed date of the schedule row the line is waiting on, when it has one.
type OverdueLine struct {
	OpenOrderLine
	DueDate   domain.Day      `json:"dueDate"`
	Currency  string          `json:"currency"`
	DaysLate  int             `json:"daysLate"`
	OpenValue decimal.Decimal `json:"openValue"`
}

// PriceHistoryEntry is one priced purchase order line of a product (ME1P order price history).
type PriceHistoryEntry struct {
	PurchaseOrderID string          `json:"purchaseOrderId"`
	Number          string          `json:"number"`
	LineNo          int             `json:"lineNo"`
	OrderDate       domain.Day      `json:"orderDate"`
	VendorID        string          `json:"vendorId"`
	Currency        string          `json:"currency"`
	Unit            string          `json:"unit"`
	Qty             decimal.Decimal `json:"qty"`
	UnitPrice       decimal.Decimal `json:"unitPrice"`
	PriceUnit       decimal.Decimal `json:"priceUnit"`
	DiscountPct     decimal.Decimal `json:"discountPct"`
	PriceSource     string          `json:"priceSource"`
	AgreementLineID string          `json:"agreementLineId,omitempty"`
}

// InquiryFilter narrows the inquiries; empty fields do not filter. AsOf is the date overdue is
// measured against.
type InquiryFilter struct {
	LegalEntityID string
	VendorID      string
	ProductID     string
	AsOf          time.Time
	From, To      *domain.Day
}

// ListOpenOrdersByVendor pages vendor and currency groups ordered by vendor then currency; the
// cursor is "<vendor id>|<currency>".
func (s *Postgres) ListOpenOrdersByVendor(ctx context.Context, f InquiryFilter, after string, limit int) (Page[VendorOpenOrders], error) {
	p, _ := tenant.FromContext(ctx)
	limit = clampLimit(limit)
	var curVendor, curCcy *string
	if after != "" {
		parts := strings.Split(after, "|")
		if len(parts) != 2 || !ids.Valid(parts[0]) || len(parts[1]) != 3 {
			return Page[VendorOpenOrders]{}, &domain.Error{Code: domain.CodeValidation, Detail: "cursor is not one this list returned"}
		}
		curVendor, curCcy = &parts[0], &parts[1]
	}
	var items []VendorOpenOrders
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		// The cursor predicate sits on the group keys before grouping, so a later page does not
		// aggregate the vendors an earlier one already returned.
		rows, err := tx.Query(ctx, `select x.vendor_id, x.currency, count(distinct x.po_id), count(*), sum(x.open_value),
			count(*) filter (where x.due < $5::date), min(x.due),
			coalesce(sum(x.open_value) filter (where x.due < $5::date), 0)
			from (select o.vendor_id, o.currency, o.id as po_id, coalesce(nx.due, l.delivery_date) as due,
			        l.line_amount * (l.qty - l.received_qty) / l.qty as open_value
			      from `+openLineSource+`
			      where `+openLineWhere+`
			      and ($2::uuid is null or o.legal_entity_id = $2::uuid)
			      and ($3::uuid is null or o.vendor_id = $3::uuid)
			      and ($4::uuid is null or (o.vendor_id, o.currency) > ($4::uuid, $6::char(3)))) x
			group by x.vendor_id, x.currency
			order by x.vendor_id, x.currency limit $7`,
			p.TenantID, nullable(f.LegalEntityID), nullable(f.VendorID), curVendor, f.AsOf, curCcy, limit+1)
		if err != nil {
			return err
		}
		items, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (VendorOpenOrders, error) {
			var v VendorOpenOrders
			var due time.Time
			err := row.Scan(&v.VendorID, &v.Currency, &v.OrderCount, &v.LineCount, &v.OpenValue, &v.OverdueLines, &due, &v.OverdueValue)
			v.EarliestDue = day(due)
			minor := money.MinorUnits(v.Currency)
			v.OpenValue, v.OverdueValue = v.OpenValue.RoundBank(minor), v.OverdueValue.RoundBank(minor)
			return v, err
		})
		return err
	})
	if err != nil {
		return Page[VendorOpenOrders]{}, err
	}
	if items == nil {
		items = []VendorOpenOrders{}
	}
	return pageOf(items, limit, func(v VendorOpenOrders) string { return v.VendorID + "|" + v.Currency }), nil
}

// ListOverdueLines pages open lines of CONFIRMED POs with a due date before f.AsOf, oldest due
// first; the cursor is "<due date>|<po id>|<line no>".
func (s *Postgres) ListOverdueLines(ctx context.Context, f InquiryFilter, after string, limit int) (Page[OverdueLine], error) {
	p, _ := tenant.FromContext(ctx)
	limit = clampLimit(limit)
	var curDate *time.Time
	var curPO *string
	var curLine *int
	if after != "" {
		parts := strings.Split(after, "|")
		d, derr := domain.ParseDay(parts[0])
		n, nerr := 0, error(nil)
		if len(parts) == 3 {
			n, nerr = strconv.Atoi(parts[2])
		}
		if len(parts) != 3 || derr != nil || nerr != nil || !ids.Valid(parts[1]) {
			return Page[OverdueLine]{}, &domain.Error{Code: domain.CodeValidation, Detail: "cursor is not one this list returned"}
		}
		curDate, curPO, curLine = &d.Time, &parts[1], &n
	}
	var items []OverdueLine
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select x.po_id, x.number, x.line_no, x.legal_entity_id, x.vendor_id, x.currency, x.product_id,
			x.description, x.site_id, x.warehouse_id, x.unit, x.qty, x.received_qty, x.open_qty, x.delivery_date, x.due,
			$4::date - x.due, x.open_value, x.due_confirmed
			from (select o.id as po_id, o.number, l.line_no, o.legal_entity_id, o.vendor_id, o.currency, coalesce(l.product_id::text, '') as product_id,
			        l.description, coalesce(coalesce(l.site_id, o.site_id)::text, '') as site_id,
			        coalesce(coalesce(l.warehouse_id, o.warehouse_id)::text, '') as warehouse_id,
			        l.unit, l.qty, l.received_qty, l.qty - l.received_qty as open_qty, l.delivery_date,
			        coalesce(nx.due, l.delivery_date) as due, nx.confirmed_date as due_confirmed,
			        l.line_amount * (l.qty - l.received_qty) / l.qty as open_value
			      from `+openLineSource+`
			      where `+openLineWhere+`
			      and ($2::uuid is null or o.legal_entity_id = $2::uuid)
			      and ($3::uuid is null or o.vendor_id = $3::uuid)
			      and ($5::uuid is null or l.product_id = $5::uuid)) x
			where x.due < $4::date
			and ($6::date is null or (x.due, x.po_id, x.line_no) > ($6::date, $7::uuid, $8::int))
			order by x.due, x.po_id, x.line_no limit $9`,
			p.TenantID, nullable(f.LegalEntityID), nullable(f.VendorID), f.AsOf, nullable(f.ProductID),
			curDate, curPO, curLine, limit+1)
		if err != nil {
			return err
		}
		items, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (OverdueLine, error) {
			var l OverdueLine
			var requested, due time.Time
			var confirmed *time.Time
			err := row.Scan(&l.PurchaseOrderID, &l.Number, &l.LineNo, &l.LegalEntityID, &l.VendorID, &l.Currency, &l.ProductID, &l.Description,
				&l.SiteID, &l.WarehouseID, &l.Unit, &l.Qty, &l.ReceivedQty, &l.OpenQty, &requested, &due, &l.DaysLate, &l.OpenValue, &confirmed)
			l.DeliveryDate, l.DueDate = day(requested), day(due)
			if confirmed != nil {
				d := day(*confirmed)
				l.ConfirmedDate = &d
			}
			l.OpenValue = l.OpenValue.RoundBank(money.MinorUnits(l.Currency))
			return l, err
		})
		return err
	})
	if err != nil {
		return Page[OverdueLine]{}, err
	}
	if items == nil {
		items = []OverdueLine{}
	}
	return pageOf(items, limit, func(l OverdueLine) string {
		return fmt.Sprintf("%s|%s|%d", l.DueDate, l.PurchaseOrderID, l.LineNo)
	}), nil
}

// ListPriceHistory pages the priced lines of one product on orders that reached approval
// (APPROVED, CONFIRMED, FINALIZED), newest order first; the cursor is "<created at RFC3339Nano>|<po id>|<line no>".
func (s *Postgres) ListPriceHistory(ctx context.Context, f InquiryFilter, after string, limit int) (Page[PriceHistoryEntry], error) {
	p, _ := tenant.FromContext(ctx)
	limit = clampLimit(limit)
	var curAt *time.Time
	var curPO *string
	var curLine *int
	if after != "" {
		parts := strings.Split(after, "|")
		at, aerr := time.Parse(time.RFC3339Nano, parts[0])
		n, nerr := 0, error(nil)
		if len(parts) == 3 {
			n, nerr = strconv.Atoi(parts[2])
		}
		if len(parts) != 3 || aerr != nil || nerr != nil || !ids.Valid(parts[1]) {
			return Page[PriceHistoryEntry]{}, &domain.Error{Code: domain.CodeValidation, Detail: "cursor is not one this list returned"}
		}
		curAt, curPO, curLine = &at, &parts[1], &n
	}
	var from, to *time.Time
	if f.From != nil {
		from = &f.From.Time
	}
	if f.To != nil {
		t := f.To.Time.AddDate(0, 0, 1) // inclusive end day
		to = &t
	}
	type row struct {
		PriceHistoryEntry
		at time.Time
	}
	var items []row
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select o.id, o.number, l.line_no, o.created_at, o.vendor_id, o.currency, l.unit, l.qty,
			l.unit_price, l.price_unit, l.discount_pct, l.price_source, coalesce(l.agreement_line_id::text, '')
			from purchase_order_line l
			join purchase_order o on o.tenant_id = l.tenant_id and o.id = l.purchase_order_id
			where l.tenant_id = $1 and l.product_id = $2::uuid and l.status <> 'CANCELLED'
			and o.approval_status in ('APPROVED', 'CONFIRMED', 'FINALIZED')
			and ($3::uuid is null or o.legal_entity_id = $3::uuid)
			and ($4::uuid is null or o.vendor_id = $4::uuid)
			and ($5::timestamptz is null or o.created_at >= $5::timestamptz)
			and ($6::timestamptz is null or o.created_at < $6::timestamptz)
			and ($7::timestamptz is null or (o.created_at, o.id, l.line_no) < ($7::timestamptz, $8::uuid, $9::int))
			order by o.created_at desc, o.id desc, l.line_no desc limit $10`,
			p.TenantID, f.ProductID, nullable(f.LegalEntityID), nullable(f.VendorID), from, to, curAt, curPO, curLine, limit+1)
		if err != nil {
			return err
		}
		items, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
			var e row
			err := r.Scan(&e.PurchaseOrderID, &e.Number, &e.LineNo, &e.at, &e.VendorID, &e.Currency, &e.Unit, &e.Qty,
				&e.UnitPrice, &e.PriceUnit, &e.DiscountPct, &e.PriceSource, &e.AgreementLineID)
			e.OrderDate = day(e.at.UTC().Truncate(24 * time.Hour))
			return e, err
		})
		return err
	})
	if err != nil {
		return Page[PriceHistoryEntry]{}, err
	}
	out := make([]PriceHistoryEntry, 0, len(items))
	for _, it := range items {
		out = append(out, it.PriceHistoryEntry)
	}
	pg := pageOf(out, limit, func(e PriceHistoryEntry) string { return "" })
	if len(items) > limit {
		last := items[limit-1]
		pg.Next = fmt.Sprintf("%s|%s|%d", last.at.UTC().Format(time.RFC3339Nano), last.PurchaseOrderID, last.LineNo)
	}
	return pg, nil
}
