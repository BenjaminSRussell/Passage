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

// OperationFilter narrows the operations overview (PRD 4.21.14). DueFrom and
// DueTo bound the order's delivery date (the operation's due date until
// scheduling, wave 23); empty fields do not filter. The cursor is the last
// order id and operation number of the previous page.
type OperationFilter struct {
	LegalEntityID string
	SiteID        string
	ResourceID    string
	OrderID       string
	OperStatus    domain.OperStatus
	OrderStatus   domain.Status
	DueFrom       string
	DueTo         string
	CursorOrder   string
	CursorOper    int
	Limit         int
}

// OperationRow is one operation of an order with its planned and reported hours
// and quantities. PlannedHours is setup + run × process quantity, the hours the
// estimate costs labor and overhead on (a subcontract operation is costed at
// its price instead, so its hours are the vendor's route time); HoursReported
// is what route cards posted.
type OperationRow struct {
	OrderID       string
	OrderNumber   string
	OrderStatus   domain.Status
	LegalEntityID string
	SiteID        string
	ProductID     string
	DueDate       *string
	OperNumber    int
	ResourceID    string
	Subcontract   bool
	ProcessQty    decimal.Decimal
	PlannedHours  decimal.Decimal
	HoursReported decimal.Decimal
	QtyGood       decimal.Decimal
	QtyError      decimal.Decimal
	OperStatus    domain.OperStatus
}

// OperationTotals sums the whole filter: operation counts by status and the
// hours and quantities.
type OperationTotals struct {
	Operations    int
	Open          int
	Started       int
	Finished      int
	PlannedHours  decimal.Decimal
	HoursReported decimal.Decimal
	QtyGood       decimal.Decimal
	QtyError      decimal.Decimal
}

// OperationsOverview is a page of the operations overview and the totals of the whole filter.
type OperationsOverview struct {
	Items      []OperationRow
	Totals     OperationTotals
	NextCursor string
}

const operationsSelected = `with ops as (
	select o.id, o.order_number, o.status as order_status, o.legal_entity_id, o.site_id, o.product_id, o.delivery_date,
		p.oper_number, p.resource_id, p.subcontract, p.process_qty,
		p.setup_time_h + p.run_time_h_per * p.process_qty as planned_hours,
		p.hours_reported, p.qty_good, p.qty_error, p.status as oper_status
	from production_operation p
	join production_order o on o.tenant_id = p.tenant_id and o.id = p.order_id
	where p.tenant_id = $1
	  and ($2 = '' or o.legal_entity_id = nullif($2, '')::uuid)
	  and ($3 = '' or o.site_id = nullif($3, '')::uuid)
	  and ($4 = '' or p.resource_id = nullif($4, '')::uuid)
	  and ($5 = '' or p.status = $5)
	  and ($6 = '' or o.status = $6)
	  and ($7 = '' or o.delivery_date >= nullif($7, '')::date)
	  and ($8 = '' or o.delivery_date <= nullif($8, '')::date)
	  and ($9 = '' or o.id = nullif($9, '')::uuid)
)
`

// OperationsOverview returns the operations overview (PRD 4.21.14): one page
// ordered by order id and operation number, with totals over the whole filter.
// RLS on the order limits it to the caller's legal entities.
func (s *Postgres) OperationsOverview(ctx context.Context, f OperationFilter) (OperationsOverview, error) {
	p, _ := tenant.FromContext(ctx)
	if f.Limit <= 0 {
		f.Limit = 50
	}
	out := OperationsOverview{Items: []OperationRow{}}
	var plannedSum, reportedSum, goodSum, badSum string
	args := []any{p.TenantID, f.LegalEntityID, f.SiteID, f.ResourceID, string(f.OperStatus), string(f.OrderStatus), f.DueFrom, f.DueTo, f.OrderID}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, operationsSelected+`select id::text, order_number, order_status, legal_entity_id::text, site_id::text, product_id::text,
				delivery_date::text, oper_number, resource_id::text, subcontract, process_qty::text, planned_hours::text, hours_reported::text,
				qty_good::text, qty_error::text, oper_status
			from ops where ($10 = '' or (id, oper_number) > (nullif($10, '')::uuid, $11::integer))
			order by id, oper_number limit $12`, append(args, f.CursorOrder, f.CursorOper, f.Limit+1)...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var r OperationRow
			var orderStatus, operStatus, qty, planned, reported, good, bad string
			if err := rows.Scan(&r.OrderID, &r.OrderNumber, &orderStatus, &r.LegalEntityID, &r.SiteID, &r.ProductID, &r.DueDate, &r.OperNumber,
				&r.ResourceID, &r.Subcontract, &qty, &planned, &reported, &good, &bad, &operStatus); err != nil {
				rows.Close()
				return err
			}
			r.OrderStatus, r.OperStatus = domain.Status(orderStatus), domain.OperStatus(operStatus)
			r.ProcessQty, r.PlannedHours, r.HoursReported, r.QtyGood, r.QtyError = dec(qty), dec(planned), dec(reported), dec(good), dec(bad)
			out.Items = append(out.Items, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		return tx.QueryRow(ctx, operationsSelected+`select count(*), count(*) filter (where oper_status = 'open'),
				count(*) filter (where oper_status = 'started'), count(*) filter (where oper_status = 'finished'),
				coalesce(sum(planned_hours), 0)::text, coalesce(sum(hours_reported), 0)::text,
				coalesce(sum(qty_good), 0)::text, coalesce(sum(qty_error), 0)::text from ops`, args...).
			Scan(&out.Totals.Operations, &out.Totals.Open, &out.Totals.Started, &out.Totals.Finished, &plannedSum, &reportedSum, &goodSum, &badSum)
	})
	if err != nil {
		return OperationsOverview{}, err
	}
	out.Totals.PlannedHours, out.Totals.HoursReported = dec(plannedSum), dec(reportedSum)
	out.Totals.QtyGood, out.Totals.QtyError = dec(goodSum), dec(badSum)
	if len(out.Items) > f.Limit {
		out.Items = out.Items[:f.Limit]
		last := out.Items[f.Limit-1]
		out.NextCursor = fmt.Sprintf("%s:%d", last.OrderID, last.OperNumber)
	}
	return out, nil
}
