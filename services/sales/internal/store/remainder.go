package store

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	salesevents "github.com/BenjaminSRussell/passage/services/sales/internal/events"
)

// CancelRemainder closes what a partly shipped order has not delivered
// (BR-27.5): an undelivered OPEN or RESERVED line is CANCELLED, a line
// delivered in part keeps the delivered quantity and is closed at it. The
// order's total follows, so the credit exposure falls to what was delivered;
// quota and agreement quantity that the closed quantity held go back; the
// remaining schedule lines are cancelled. The order becomes SHIPPED (INVOICED
// when everything delivered was invoiced). It gets a new version with the
// field-level diff, and order.remaindercancelled.v1 is queued in the same
// transaction.
//
// Holds are left as they are. Unlike :cancel the order lives on to be
// invoiced, a hold is released only by its own duty and never by the user who
// applied it, fulfillment keeps its copy of each hold from orderhold.applied and
// released (and refuses a packing slip while one is active, FUL-0001), and a
// FRAUD or MANUAL hold may concern what was already delivered.
func (s *Postgres) CancelRemainder(ctx context.Context, id string, version int, reason, actor string, now time.Time) (Order, error) {
	return s.cancelRemainder(ctx, id, 0, version, reason, actor, now)
}

// CancelLineRemainder closes what one line of a partly shipped order has not
// delivered (BR-27.5, /lines/{n}:cancel-remainder), by the same rules as
// CancelRemainder for that line only. The other lines are not touched and the
// order total, quota, agreement quantity and schedule lines follow the one line.
// A line with nothing left to cancel (delivered in full, or cancelled) is
// refused with SLS-0025; an unknown line number is SLS-0109.
func (s *Postgres) CancelLineRemainder(ctx context.Context, id string, lineNo, version int, reason, actor string, now time.Time) (Order, error) {
	if lineNo < 1 {
		// 0 means "every line" to cancelRemainder; a caller naming a line
		// must never reach the whole-order close.
		return Order{}, problem(http.StatusNotFound, CodeLineNotFound, "order line not found")
	}
	return s.cancelRemainder(ctx, id, lineNo, version, reason, actor, now)
}

// cancelRemainder closes the remainder of every line, or of line lineNo when it
// is not 0.
func (s *Postgres) cancelRemainder(ctx context.Context, id string, lineNo, version int, reason, actor string, now time.Time) (Order, error) {
	p, _ := tenant.FromContext(ctx)
	if len(reason) > changeReasonMaxSize {
		return Order{}, problem(http.StatusUnprocessableEntity, CodeInvalidChange, "reason is too long")
	}
	var out Order
	err := s.orderTx(ctx, p.TenantID, &out, func(tx pgx.Tx) error {
		o, err := loadOrder(ctx, tx, p.TenantID, id, true)
		if err != nil {
			return err
		}
		if o.Version != version {
			return versionConflict()
		}
		if o.Status != domain.OrderPartiallyShipped {
			return problem(http.StatusUnprocessableEntity, CodeNotCancellable, fmt.Sprintf("order is %s; only a partly shipped order has a remainder to cancel", o.Status))
		}
		if lineNo != 0 {
			found := false
			for _, l := range o.Lines {
				found = found || l.LineNo == lineNo
			}
			if !found {
				return problem(http.StatusNotFound, CodeLineNotFound, "order line not found")
			}
		}
		var diff []domain.FieldChange
		var closed []salesevents.RemainderLine
		cancelled := map[int]bool{}
		cut := map[int]Line{}
		var agreementCut decimal.Decimal
		for i := range o.Lines {
			l := &o.Lines[i]
			if lineNo != 0 && l.LineNo != lineNo {
				continue
			}
			before := domain.LineProgress{Status: l.LineStatus, Quantity: l.Quantity, Delivered: l.DeliveredQty, Invoiced: l.InvoicedQty}
			after, gave, err := domain.CancelRemainder(before)
			if err != nil {
				return problem(http.StatusUnprocessableEntity, CodeNotCancellable, fmt.Sprintf("line %d: %s", l.LineNo, err))
			}
			if !gave.IsPositive() {
				continue
			}
			net := decimal.Zero
			if after.Status != domain.LineCancelled {
				net, err = domain.LineNetFor(after.Quantity, l.UnitPrice, l.DiscPct, l.CatchWeight, l.ActualWeight)
				if err != nil {
					return problem(http.StatusInternalServerError, "COM-0500", err.Error())
				}
				diff = append(diff, domain.FieldChange{LineNo: l.LineNo, Field: "quantity", Old: l.Quantity.String(), New: after.Quantity.String()})
				cut[l.LineNo] = *l
			} else {
				cancelled[l.LineNo] = true
			}
			if _, err := tx.Exec(ctx, `update sales_order_line set quantity = $4::numeric, line_net = $5::numeric, line_status = $6
				where tenant_id = $1 and order_id = $2 and id = $3`, p.TenantID, o.ID, l.ID, after.Quantity.String(), net.String(), after.Status); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `update order_schedule_line set status = 'CANCELLED', updated_at = now()
				where tenant_id = $1 and line_id = $2 and status in ('CONFIRMED', 'BACKORDER', 'UNPROMISED')`, p.TenantID, l.ID); err != nil {
				return err
			}
			diff = append(diff, domain.FieldChange{LineNo: l.LineNo, Field: "lineStatus", Old: l.LineStatus, New: after.Status})
			if l.SupplementaryOf == nil {
				agreementCut = agreementCut.Add(gave)
			}
			kept := after.Quantity
			if after.Status == domain.LineCancelled {
				kept = decimal.Zero
			}
			closed = append(closed, salesevents.RemainderLine{LineID: l.ID, LineNo: l.LineNo, ProductID: l.ProductID,
				CancelledQty: gave.String(), KeptQty: kept.String(), LineStatus: after.Status})
			l.Quantity, l.LineNet, l.LineStatus = after.Quantity, net, after.Status
		}
		if len(closed) == 0 {
			if lineNo != 0 {
				return problem(http.StatusUnprocessableEntity, CodeNotCancellable, fmt.Sprintf("line %d is already delivered or cancelled; nothing is left to cancel", lineNo))
			}
			return problem(http.StatusUnprocessableEntity, CodeNotCancellable, "every line is already delivered or cancelled")
		}
		if len(cancelled) > 0 {
			if err := returnAllocationTx(ctx, tx, p.TenantID, o, cancelled, now); err != nil {
				return err
			}
		}
		if len(cut) > 0 {
			if err := shrinkAllocationTx(ctx, tx, p.TenantID, o, cut, now); err != nil {
				return err
			}
		}
		if agreementCut.IsPositive() {
			if err := returnAgreementTx(ctx, tx, p.TenantID, o, actor, now, &agreementCut); err != nil {
				return err
			}
		}
		progress := make([]domain.LineProgress, len(o.Lines))
		for i, l := range o.Lines {
			progress[i] = domain.LineProgress{Status: l.LineStatus, Quantity: l.Quantity, Delivered: l.DeliveredQty, Invoiced: l.InvoicedQty}
		}
		status := domain.OrderStatusFor(progress)
		total := netOf(o.Lines)
		next := o.Version + 1
		if _, err := tx.Exec(ctx, `update sales_order set status = $3, net_total = $4::numeric, version = $5, updated_at = $6 where tenant_id = $1 and id = $2`,
			p.TenantID, o.ID, status, total.String(), next, now); err != nil {
			return err
		}
		diff = append(diff, domain.DiffHeader("status", o.Status, status)...)
		diff = append(diff, domain.DiffHeader("netTotal", o.NetTotal.String(), total.String())...)
		if err := insertVersion(ctx, tx, p.TenantID, o.LegalEntityID, o.ID, next, actor, reason, diff, now); err != nil {
			return err
		}
		if err := enqueue(ctx, tx, "order", o.ID, salesevents.OrderRemainderCancelled, p.TenantID, salesevents.OrderRemainderCancelledData{
			OrderID: o.ID, LegalEntityID: o.LegalEntityID, Version: next, Status: status, Lines: closed, Reason: reason, Actor: actor,
		}); err != nil {
			return err
		}
		out, err = loadOrder(ctx, tx, p.TenantID, o.ID, false)
		return err
	})
	return out, err
}

// shrinkAllocationTx lowers the quota each cut line drew to the quantity the
// line keeps (BR-27.15): the difference goes back to the period it was drawn
// from, and a draw that is no longer needed at all is returned. lines holds the
// lines before the cut, by line number; o has them as they are after it.
func shrinkAllocationTx(ctx context.Context, tx pgx.Tx, tenantID string, o Order, lines map[int]Line, now time.Time) error {
	kept := map[int]Line{}
	for _, l := range o.Lines {
		if _, ok := lines[l.LineNo]; ok {
			kept[l.LineNo] = l
		}
	}
	rows, err := tx.Query(ctx, `select c.id::text, c.quota_id::text, c.line_id::text, c.line_no, c.quantity::text
		from allocation_consumption c where c.tenant_id = $1 and c.order_id = $2 and c.returned_at is null order by c.quota_id, c.line_no`, tenantID, o.ID)
	if err != nil {
		return err
	}
	type draw struct {
		id, quotaID, lineID string
		lineNo              int
		qty, keep           decimal.Decimal
	}
	var todo []draw
	for rows.Next() {
		var d draw
		var qty string
		if err := rows.Scan(&d.id, &d.quotaID, &d.lineID, &d.lineNo, &qty); err != nil {
			rows.Close()
			return err
		}
		d.qty = decimal.RequireFromString(qty)
		if l, ok := kept[d.lineNo]; ok && d.qty.GreaterThan(l.Quantity) {
			d.keep = l.Quantity
			todo = append(todo, d)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(todo) == 0 {
		return nil
	}
	if err := lockOrderQuotasTx(ctx, tx, tenantID, o); err != nil {
		return err
	}
	for _, d := range todo {
		back := d.qty.Sub(d.keep)
		if _, err := tx.Exec(ctx, `update allocation_quota set consumed_qty = consumed_qty - $3::numeric, version = version + 1, updated_at = $4
			where tenant_id = $1 and id = $2`, tenantID, d.quotaID, back.String(), now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update allocation_consumption set quantity = $3::numeric where tenant_id = $1 and id = $2`, tenantID, d.id, d.keep.String()); err != nil {
			return err
		}
		q, err := loadQuota(ctx, tx, tenantID, d.quotaID, false)
		if err != nil {
			return err
		}
		if err := enqueue(ctx, tx, allocationAggregate, q.ID, salesevents.AllocationReturned, tenantID, salesevents.AllocationData{
			QuotaID: q.ID, OrderID: o.ID, LineID: d.lineID, ProductID: kept[d.lineNo].ProductID, Quantity: back.String(),
			Consumed: q.ConsumedQty.String(), Remaining: q.RemainingQty.String()}); err != nil {
			return err
		}
	}
	return nil
}
