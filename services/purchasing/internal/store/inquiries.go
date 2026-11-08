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

// lineDue is the due date of a line of openLineSource and lineOpenValue its unrounded open value;
// every open-order inquiry selects these two, so they date and value a line the same way.
const (
	lineDue       = `coalesce(nx.due, l.delivery_date)`
	lineOpenValue = `l.line_amount * (l.qty - l.received_qty) / l.qty`
)

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

// DueLine is an open line of a CONFIRMED purchase order with its due date, the days it is late at
// the as-of date (zero while it is not yet due) and its open value. DeliveryDate is the requested
// date; ConfirmedDate here is the vendor's confirmed date of the schedule row the line is waiting
// on, when it has one. The overdue and backordered inquiries return it, read by one query
// (listDueLines) so the two cannot date a line differently.
type DueLine struct {
	OpenOrderLine
	DueDate   domain.Day      `json:"dueDate"`
	Currency  string          `json:"currency"`
	DaysLate  int             `json:"daysLate"`
	OpenValue decimal.Decimal `json:"openValue"`
}

// OverdueLine is a line of the overdue inquiry: due before the as-of date.
type OverdueLine = DueLine

// BackorderedLine is a line of the backordered inquiry: received in part (some quantity arrived,
// the rest is still owed), due or not.
type BackorderedLine = DueLine

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
	// WithinDays is the look-ahead of the expiring agreements inquiry; nil means the default (30).
	// Zero is a real window: agreements ending on the as-of date, and lapsed ones.
	WithinDays *int
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
			from (select o.vendor_id, o.currency, o.id as po_id, `+lineDue+` as due,
			        `+lineOpenValue+` as open_value
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
	return s.listDueLines(ctx, f, after, limit, "true", "x.due < $4::date")
}

// ListBackorderedLines pages the open lines of CONFIRMED POs that were received in part (some
// quantity received, some still to receive), due or not, earliest due first; the cursor is as for
// ListOverdueLines. A line whose receipts are reversed back to zero leaves the list (it is then
// only open, and overdue once its due date passes). The partial index of migration 0028 covers
// exactly openLineWhere plus received_qty > 0.
func (s *Postgres) ListBackorderedLines(ctx context.Context, f InquiryFilter, after string, limit int) (Page[BackorderedLine], error) {
	return s.listDueLines(ctx, f, after, limit, "l.received_qty > 0", "true")
}

// listDueLines pages the lines of openLineSource that also match lineWhere (a constant predicate
// on l and o) and whose due date x.due matches dueWhere (a constant predicate on the due date and
// $4, the as-of date), ordered by (due date, PO id, line no), which is also the cursor. Days late
// are counted at f.AsOf and are zero for a line not yet due; asOf never moves the sort key, so a
// cursor stays valid under another asOf.
func (s *Postgres) listDueLines(ctx context.Context, f InquiryFilter, after string, limit int, lineWhere, dueWhere string) (Page[DueLine], error) {
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
			return Page[DueLine]{}, &domain.Error{Code: domain.CodeValidation, Detail: "cursor is not one this list returned"}
		}
		curDate, curPO, curLine = &d.Time, &parts[1], &n
	}
	var items []DueLine
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select x.po_id, x.number, x.line_no, x.legal_entity_id, x.vendor_id, x.currency, x.product_id,
			x.description, x.site_id, x.warehouse_id, x.unit, x.qty, x.received_qty, x.open_qty, x.delivery_date, x.due,
			greatest($4::date - x.due, 0), x.open_value, x.due_confirmed
			from (select o.id as po_id, o.number, l.line_no, o.legal_entity_id, o.vendor_id, o.currency, coalesce(l.product_id::text, '') as product_id,
			        l.description, coalesce(coalesce(l.site_id, o.site_id)::text, '') as site_id,
			        coalesce(coalesce(l.warehouse_id, o.warehouse_id)::text, '') as warehouse_id,
			        l.unit, l.qty, l.received_qty, l.qty - l.received_qty as open_qty, l.delivery_date,
			        `+lineDue+` as due, nx.confirmed_date as due_confirmed,
			        `+lineOpenValue+` as open_value
			      from `+openLineSource+`
			      where `+openLineWhere+` and `+lineWhere+`
			      and ($2::uuid is null or o.legal_entity_id = $2::uuid)
			      and ($3::uuid is null or o.vendor_id = $3::uuid)
			      and ($5::uuid is null or l.product_id = $5::uuid)) x
			where `+dueWhere+`
			and ($6::date is null or (x.due, x.po_id, x.line_no) > ($6::date, $7::uuid, $8::int))
			order by x.due, x.po_id, x.line_no limit $9`,
			p.TenantID, nullable(f.LegalEntityID), nullable(f.VendorID), f.AsOf, nullable(f.ProductID),
			curDate, curPO, curLine, limit+1)
		if err != nil {
			return err
		}
		items, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (DueLine, error) {
			var l DueLine
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
		return Page[DueLine]{}, err
	}
	if items == nil {
		items = []DueLine{}
	}
	return pageOf(items, limit, func(l DueLine) string {
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

// ExpiringAgreement is a purchase agreement that ends within the look-ahead window, or that has
// ended while still EFFECTIVE or ON_HOLD (Lapsed). Committed and released amounts add up the lines
// measured in value (VALUE, CATEGORY_VALUE, TOTAL_VALUE) in the agreement currency; QUANTITY lines
// are counted in QtyLines and not summed, since a quantity is not an amount.
type ExpiringAgreement struct {
	ID              string          `json:"id"`
	Number          string          `json:"number"`
	LegalEntityID   string          `json:"legalEntityId"`
	VendorID        string          `json:"vendorId"`
	Currency        string          `json:"currency"`
	Status          string          `json:"status"`
	ValidTo         domain.Day      `json:"validTo"`
	DaysLeft        int             `json:"daysLeft"`
	Lapsed          bool            `json:"lapsed"`
	LineCount       int             `json:"lineCount"`
	QtyLines        int             `json:"qtyLines"`
	CommittedAmount decimal.Decimal `json:"committedAmount"`
	ReleasedAmount  decimal.Decimal `json:"releasedAmount"`
}

// DefaultExpiryWindowDays and MaxExpiryWindowDays bound the expiring agreements look-ahead.
const (
	DefaultExpiryWindowDays = 30
	MaxExpiryWindowDays     = 366
)

// ListExpiringAgreements pages EFFECTIVE and ON_HOLD agreements whose valid_to is on or before
// f.AsOf plus the window (f.WithinDays, default 30), soonest first, lapsed ones (valid_to before
// the as-of date) among them; the cursor is "<valid to>|<agreement id>". An agreement with no end
// date never expires and is not listed.
func (s *Postgres) ListExpiringAgreements(ctx context.Context, f InquiryFilter, after string, limit int) (Page[ExpiringAgreement], error) {
	p, _ := tenant.FromContext(ctx)
	limit = clampLimit(limit)
	window := DefaultExpiryWindowDays
	if f.WithinDays != nil {
		window = *f.WithinDays
	}
	if window < 0 || window > MaxExpiryWindowDays {
		return Page[ExpiringAgreement]{}, &domain.Error{Code: domain.CodeValidation, Detail: "withinDays must be between 0 and " + strconv.Itoa(MaxExpiryWindowDays)}
	}
	var curDate *time.Time
	var curID *string
	if after != "" {
		parts := strings.Split(after, "|")
		d, derr := domain.ParseDay(parts[0])
		if len(parts) != 2 || derr != nil || !ids.Valid(parts[1]) {
			return Page[ExpiringAgreement]{}, &domain.Error{Code: domain.CodeValidation, Detail: "cursor is not one this list returned"}
		}
		curDate, curID = &d.Time, &parts[1]
	}
	var items []ExpiringAgreement
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select a.id, a.number, a.legal_entity_id, a.vendor_id, a.currency, a.status, a.valid_to,
			a.valid_to - $2::date, count(l.id), count(l.id) filter (where l.commitment_type = 'QUANTITY'),
			coalesce(sum(l.committed_amount) filter (where l.commitment_type <> 'QUANTITY'), 0),
			coalesce(sum(l.released_amount) filter (where l.commitment_type <> 'QUANTITY'), 0)
			from purchase_agreement a
			left join purchase_agreement_line l on l.tenant_id = a.tenant_id and l.agreement_id = a.id
			where a.tenant_id = $1 and a.status in ('EFFECTIVE', 'ON_HOLD') and a.valid_to is not null
			and a.valid_to <= $2::date + $3::int
			and ($4::uuid is null or a.legal_entity_id = $4::uuid)
			and ($5::uuid is null or a.vendor_id = $5::uuid)
			and ($6::date is null or (a.valid_to, a.id) > ($6::date, $7::uuid))
			group by a.tenant_id, a.id
			order by a.valid_to, a.id limit $8`,
			p.TenantID, f.AsOf, window, nullable(f.LegalEntityID), nullable(f.VendorID), curDate, curID, limit+1)
		if err != nil {
			return err
		}
		items, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (ExpiringAgreement, error) {
			var a ExpiringAgreement
			var to time.Time
			err := row.Scan(&a.ID, &a.Number, &a.LegalEntityID, &a.VendorID, &a.Currency, &a.Status, &to, &a.DaysLeft,
				&a.LineCount, &a.QtyLines, &a.CommittedAmount, &a.ReleasedAmount)
			a.ValidTo, a.Lapsed = day(to), a.DaysLeft < 0
			minor := money.MinorUnits(a.Currency)
			a.CommittedAmount, a.ReleasedAmount = a.CommittedAmount.RoundBank(minor), a.ReleasedAmount.RoundBank(minor)
			return a, err
		})
		return err
	})
	if err != nil {
		return Page[ExpiringAgreement]{}, err
	}
	if items == nil {
		items = []ExpiringAgreement{}
	}
	return pageOf(items, limit, func(a ExpiringAgreement) string { return a.ValidTo.String() + "|" + a.ID }), nil
}
