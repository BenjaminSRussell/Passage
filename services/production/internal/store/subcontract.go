package store

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
	prodevents "github.com/BenjaminSRussell/passage/services/production/internal/events"
)

// claimSubcontractRequests marks every subcontract operation of a released order
// that has not been requested yet as requested now, and returns the purchase
// requests to enqueue (FR-21.10), in route order. Claiming under the order row
// lock makes the request once per operation: a reset and second release, a bulk
// retry or a later edit of the order finds nothing left to request.
func claimSubcontractRequests(ctx context.Context, tx pgx.Tx, tenantID, orderID, actor string) ([]prodevents.SubcontractPORequestedData, error) {
	rows, err := tx.Query(ctx, `update production_operation p set po_requested_at = now()
		from production_order o
		where p.tenant_id = $1 and p.order_id = $2 and p.subcontract and p.po_requested_at is null
		  and o.tenant_id = p.tenant_id and o.id = p.order_id
		returning p.oper_number, p.resource_id::text, p.process_qty::text, p.subcontract_price::text,
			o.order_number, o.legal_entity_id::text, o.site_id::text, o.warehouse_id::text, o.currency::text, o.delivery_date::text`,
		tenantID, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ops []domain.Operation
	var head prodevents.SubcontractPORequestedData
	for rows.Next() {
		op := domain.Operation{Subcontract: true}
		var qty, price string
		if err := rows.Scan(&op.OperNumber, &op.ResourceID, &qty, &price, &head.OrderNumber, &head.LegalEntityID, &head.SiteID,
			&head.WarehouseID, &head.Currency, &head.DueDate); err != nil {
			return nil, err
		}
		op.ProcessQty, op.SubcontractPrice = dec(qty), dec(price)
		ops = append(ops, op)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(ops, func(i, j int) bool { return ops[i].OperNumber < ops[j].OperNumber })
	out := make([]prodevents.SubcontractPORequestedData, 0, len(ops))
	for _, req := range domain.SubcontractRequests(ops) {
		data := head
		data.OrderID, data.Actor = orderID, actor
		data.OperNumber, data.ResourceID = req.OperNumber, req.ResourceID
		data.Qty, data.UnitPrice, data.Amount = req.Qty.String(), req.UnitPrice.String(), req.Amount.StringFixed(4)
		out = append(out, data)
	}
	return out, nil
}

// enqueueSubcontractRequests writes one subcontract.porequested event per claimed request.
func enqueueSubcontractRequests(ctx context.Context, tx pgx.Tx, tenantID, orderID, actor string, requests []prodevents.SubcontractPORequestedData) error {
	for _, data := range requests {
		if err := enqueue(ctx, tx, orderID, prodevents.SubcontractPORequested, tenantID, actor, data); err != nil {
			return err
		}
	}
	return nil
}

// SubcontractFilter narrows the subcontract operations inquiry (PRD 4.21.14).
// ResourceID is the vendor resource; OperStatus the operation's feedback status;
// DueFrom and DueTo (YYYY-MM-DD) bound the due date, which is the order's
// delivery date. Empty fields do not filter. The cursor is the order id and
// operation number of the last row of the previous page.
type SubcontractFilter struct {
	LegalEntityID string
	SiteID        string
	ResourceID    string
	OperStatus    domain.OperStatus
	OrderStatus   domain.Status
	DueFrom       string
	DueTo         string
	CursorOrder   string
	CursorOper    int
	Limit         int
}

// SubcontractRow is one subcontract operation: the order, the vendor resource,
// the process quantity and what of it was reported good, the vendor unit price
// and the planned amount (unit price × quantity at 4 places), the operation's
// status and when its purchase request was raised (nil before release).
type SubcontractRow struct {
	OrderID       string
	OrderNumber   string
	OrderStatus   domain.Status
	LegalEntityID string
	SiteID        string
	Currency      string
	DueDate       *string
	OperNumber    int
	ResourceID    string
	Qty           decimal.Decimal
	QtyGood       decimal.Decimal
	UnitPrice     decimal.Decimal
	Amount        decimal.Decimal
	OperStatus    domain.OperStatus
	PORequestedAt *time.Time
}

// SubcontractTotal sums the planned subcontract amount per currency over every
// operation the filter selects.
type SubcontractTotal struct {
	Currency   string
	Operations int
	Amount     decimal.Decimal
}

// SubcontractReport is a page of the subcontract operations inquiry and the
// totals of the whole filter.
type SubcontractReport struct {
	Items      []SubcontractRow
	Totals     []SubcontractTotal
	NextCursor string
}

const subcontractOps = `with sub as (
	select o.id, o.order_number, o.status, o.legal_entity_id, o.site_id, o.currency::text as currency, o.delivery_date,
		p.oper_number, p.resource_id, p.process_qty, p.qty_good, p.subcontract_price,
		round(p.subcontract_price * p.process_qty, 4) as amount, p.status as oper_status, p.po_requested_at
	from production_operation p
	join production_order o on o.tenant_id = p.tenant_id and o.id = p.order_id
	where p.tenant_id = $1 and p.subcontract
	  and ($2 = '' or o.legal_entity_id = nullif($2, '')::uuid)
	  and ($3 = '' or o.site_id = nullif($3, '')::uuid)
	  and ($4 = '' or p.resource_id = nullif($4, '')::uuid)
	  and ($5 = '' or p.status = $5)
	  and ($6 = '' or o.status = $6)
	  and ($7 = '' or o.delivery_date >= nullif($7, '')::date)
	  and ($8 = '' or o.delivery_date <= nullif($8, '')::date)
)
`

// SubcontractOperations returns the subcontract operations inquiry (PRD 4.21.14):
// one page ordered by order id and operation number, with the planned amount
// totalled per currency over the whole filter. RLS on the order limits it to the
// caller's legal entities.
func (s *Postgres) SubcontractOperations(ctx context.Context, f SubcontractFilter) (SubcontractReport, error) {
	p, _ := tenant.FromContext(ctx)
	if f.Limit <= 0 {
		f.Limit = 50
	}
	out := SubcontractReport{Items: []SubcontractRow{}, Totals: []SubcontractTotal{}}
	args := []any{p.TenantID, f.LegalEntityID, f.SiteID, f.ResourceID, string(f.OperStatus), string(f.OrderStatus), f.DueFrom, f.DueTo}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, subcontractOps+`select id::text, order_number, status, legal_entity_id::text, site_id::text, currency,
				delivery_date::text, oper_number, resource_id::text, process_qty::text, qty_good::text, subcontract_price::text, amount::text,
				oper_status, po_requested_at
			from sub where ($9 = '' or (id, oper_number) > (nullif($9, '')::uuid, $10::integer))
			order by id, oper_number limit $11`, append(args, f.CursorOrder, f.CursorOper, f.Limit+1)...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var r SubcontractRow
			var status, qty, good, price, amount, operStatus string
			if err := rows.Scan(&r.OrderID, &r.OrderNumber, &status, &r.LegalEntityID, &r.SiteID, &r.Currency, &r.DueDate, &r.OperNumber,
				&r.ResourceID, &qty, &good, &price, &amount, &operStatus, &r.PORequestedAt); err != nil {
				rows.Close()
				return err
			}
			r.OrderStatus, r.OperStatus = domain.Status(status), domain.OperStatus(operStatus)
			r.Qty, r.QtyGood, r.UnitPrice, r.Amount = dec(qty), dec(good), dec(price), dec(amount)
			out.Items = append(out.Items, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		totals, err := tx.Query(ctx, subcontractOps+`select currency, count(*), sum(amount)::text from sub group by currency order by currency`, args...)
		if err != nil {
			return err
		}
		defer totals.Close()
		for totals.Next() {
			var t SubcontractTotal
			var amount string
			if err := totals.Scan(&t.Currency, &t.Operations, &amount); err != nil {
				return err
			}
			t.Amount = dec(amount)
			out.Totals = append(out.Totals, t)
		}
		return totals.Err()
	})
	if err != nil {
		return SubcontractReport{}, err
	}
	if len(out.Items) > f.Limit {
		out.Items = out.Items[:f.Limit]
		last := out.Items[f.Limit-1]
		out.NextCursor = fmt.Sprintf("%s:%d", last.OrderID, last.OperNumber)
	}
	return out, nil
}
