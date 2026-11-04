package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
)

// MaterialFilter narrows the material requirements (PRD 4.21.14 "Material shortage / release check").
// Without OrderStatus only open orders (not Ended, not Cancelled) count. DueFrom and DueTo bound the
// order's delivery date. All lists lines and items that are fully issued too. The line cursor is the
// last order id and BOM line sequence of the previous page, the item cursor the last item id.
type MaterialFilter struct {
	LegalEntityID string
	SiteID        string
	ItemID        string
	OrderID       string
	OrderStatus   domain.Status
	DueFrom       string
	DueTo         string
	All           bool
	CursorOrder   string
	CursorSeq     int
	CursorItem    string
	Limit         int
}

// MaterialLine is one BOM line of an open order: the quantity it requires, what is issued (the
// line's booked consumption: posted picking lists, backflush included, net of reversals and of the
// share a split moved to another order) and what is left to issue.
type MaterialLine struct {
	OrderID        string
	OrderNumber    string
	OrderStatus    domain.Status
	LegalEntityID  string
	SiteID         string
	ProductID      string
	DueDate        *string
	LineNumber     string
	LineSeq        int
	ItemID         string
	QtyRequired    decimal.Decimal
	QtyIssued      decimal.Decimal
	QtyOutstanding decimal.Decimal
}

// MaterialTotals counts the whole filter.
type MaterialTotals struct {
	Lines  int
	Items  int
	Orders int
}

// MaterialRequirements is a page of lines and the totals of the whole filter.
type MaterialRequirements struct {
	Items      []MaterialLine
	Totals     MaterialTotals
	NextCursor string
}

// MaterialItem is the outstanding demand of one component over the filtered orders.
type MaterialItem struct {
	ItemID         string
	Orders         int
	Lines          int
	QtyRequired    decimal.Decimal
	QtyIssued      decimal.Decimal
	QtyOutstanding decimal.Decimal
	EarliestDue    *string
}

// MaterialItems is a page of components.
type MaterialItems struct {
	Items      []MaterialItem
	NextCursor string
}

const openOrderStatuses = `('Created','Estimated','Scheduled','Released','Started','ReportedAsFinished')`

const materialLines = `with lines as (
	select o.id as order_id, o.order_number, o.status as order_status, o.legal_entity_id, o.site_id, o.product_id, o.delivery_date,
		b.line_number, b.line_seq, b.item_id, b.qty_required, b.qty_consumed as qty_issued
	from production_bom_line b
	join production_order o on o.tenant_id = b.tenant_id and o.id = b.order_id
	where b.tenant_id = $1
	  and ($2 = '' or o.legal_entity_id = nullif($2, '')::uuid)
	  and ($3 = '' or o.site_id = nullif($3, '')::uuid)
	  and ($4 = '' or b.item_id = nullif($4, '')::uuid)
	  and ($5 = '' or o.id = nullif($5, '')::uuid)
	  and (($6 = '' and o.status in ` + openOrderStatuses + `) or o.status = $6)
	  and ($7 = '' or o.delivery_date >= nullif($7, '')::date)
	  and ($8 = '' or o.delivery_date <= nullif($8, '')::date)
), wanted as (
	select *, greatest(qty_required - qty_issued, 0) as qty_outstanding from lines
)
`

func materialArgs(tenantID string, f MaterialFilter) []any {
	return []any{tenantID, f.LegalEntityID, f.SiteID, f.ItemID, f.OrderID, string(f.OrderStatus), f.DueFrom, f.DueTo, f.All}
}

const outstandingOnly = `($9 or qty_outstanding > 0)`

// MaterialRequirements lists the BOM lines of open orders with required, issued and outstanding
// quantities, ordered by order id and line sequence, with totals over the whole filter. Only lines with
// something left to issue are listed unless f.All. RLS on the order limits it to the caller's legal entities.
func (s *Postgres) MaterialRequirements(ctx context.Context, f MaterialFilter) (MaterialRequirements, error) {
	p, _ := tenant.FromContext(ctx)
	if f.Limit <= 0 {
		f.Limit = 50
	}
	out := MaterialRequirements{Items: []MaterialLine{}}
	args := materialArgs(p.TenantID, f)
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, materialLines+`select order_id::text, order_number, order_status, legal_entity_id::text, site_id::text, product_id::text,
				delivery_date::text, line_number, line_seq, item_id::text, qty_required::text, qty_issued::text, qty_outstanding::text
			from wanted where `+outstandingOnly+` and ($10 = '' or (order_id, line_seq) > (nullif($10, '')::uuid, $11::integer))
			order by order_id, line_seq limit $12`, append(args, f.CursorOrder, f.CursorSeq, f.Limit+1)...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var r MaterialLine
			var status, req, issued, outstanding string
			if err := rows.Scan(&r.OrderID, &r.OrderNumber, &status, &r.LegalEntityID, &r.SiteID, &r.ProductID, &r.DueDate, &r.LineNumber, &r.LineSeq,
				&r.ItemID, &req, &issued, &outstanding); err != nil {
				rows.Close()
				return err
			}
			r.OrderStatus = domain.Status(status)
			r.QtyRequired, r.QtyIssued, r.QtyOutstanding = dec(req), dec(issued), dec(outstanding)
			out.Items = append(out.Items, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		return tx.QueryRow(ctx, materialLines+`select count(*), count(distinct item_id), count(distinct order_id) from wanted where `+outstandingOnly, args...).
			Scan(&out.Totals.Lines, &out.Totals.Items, &out.Totals.Orders)
	})
	if err != nil {
		return MaterialRequirements{}, err
	}
	if len(out.Items) > f.Limit {
		out.Items = out.Items[:f.Limit]
		last := out.Items[f.Limit-1]
		out.NextCursor = fmt.Sprintf("%s:%d", last.OrderID, last.LineSeq)
	}
	return out, nil
}

// MaterialItems rolls the same lines up per component: the orders and lines that need it, the
// quantity required, issued and outstanding (one item has one unit, so the sums are meaningful) and
// the earliest delivery date of the orders still waiting for it. Ordered by item id.
func (s *Postgres) MaterialItems(ctx context.Context, f MaterialFilter) (MaterialItems, error) {
	p, _ := tenant.FromContext(ctx)
	if f.Limit <= 0 {
		f.Limit = 50
	}
	out := MaterialItems{Items: []MaterialItem{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, materialLines+`select item_id::text, count(distinct order_id), count(*), sum(qty_required)::text, sum(qty_issued)::text,
				sum(qty_outstanding)::text, (min(delivery_date) filter (where qty_outstanding > 0))::text
			from wanted where `+outstandingOnly+` and ($10 = '' or item_id > nullif($10, '')::uuid)
			group by item_id order by item_id limit $11`, append(materialArgs(p.TenantID, f), f.CursorItem, f.Limit+1)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r MaterialItem
			var req, issued, outstanding string
			if err := rows.Scan(&r.ItemID, &r.Orders, &r.Lines, &req, &issued, &outstanding, &r.EarliestDue); err != nil {
				return err
			}
			r.QtyRequired, r.QtyIssued, r.QtyOutstanding = dec(req), dec(issued), dec(outstanding)
			out.Items = append(out.Items, r)
		}
		return rows.Err()
	})
	if err != nil {
		return MaterialItems{}, err
	}
	if len(out.Items) > f.Limit {
		out.Items = out.Items[:f.Limit]
		out.NextCursor = out.Items[f.Limit-1].ItemID
	}
	return out, nil
}
