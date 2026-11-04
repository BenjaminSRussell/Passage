package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	salesevents "github.com/BenjaminSRussell/passage/services/sales/internal/events"
)

// AutoCloseFilter selects the orders the nightly auto-close looks at (PRD
// 5.27.16, "Order auto-close"). Empty LegalEntityID means every legal entity
// in the caller's scope.
type AutoCloseFilter struct {
	LegalEntityID string
	// GraceDays is how long an INVOICED order must have been unchanged.
	GraceDays int
	Limit     int
	// DryRun lists the candidates and closes nothing.
	DryRun bool
}

// AutoCloseItem is an order the run looked at.
type AutoCloseItem struct {
	OrderID       string    `json:"orderId"`
	OrderNumber   string    `json:"orderNumber"`
	LegalEntityID string    `json:"legalEntityId"`
	LastChangedAt time.Time `json:"lastChangedAt"`
	// Version is the order version the run saw; after a close it is the new one.
	Version int `json:"version"`
	// Result is CLOSED, CANDIDATE (dry run) or SKIPPED (changed since it was listed).
	Result string `json:"result"`
}

// AutoCloseResult is what one run did.
type AutoCloseResult struct {
	Closed  int             `json:"closed"`
	Skipped int             `json:"skipped"`
	More    bool            `json:"more"`
	Items   []AutoCloseItem `json:"items"`
}

// AutoCloseOrders moves INVOICED orders that nothing has touched for GraceDays
// to CLOSED, oldest first, up to Limit. Each order is closed in its own
// transaction under its row lock, re-checked against the version it was listed
// with (an order that moved, got a hold or was changed in between is SKIPPED),
// so the run is safe to repeat and two runs cannot close an order twice: the
// second finds it CLOSED and does not list it. A JOURNAL or SUBSCRIPTION order
// never reaches INVOICED, and an order with an ACTIVE hold stays open. A version
// with the field diff and order.closed go with each close.
func (s *Postgres) AutoCloseOrders(ctx context.Context, f AutoCloseFilter, actor string, now time.Time) (AutoCloseResult, error) {
	p, _ := tenant.FromContext(ctx)
	cutoff := now.Add(-time.Duration(f.GraceDays) * 24 * time.Hour)
	var cands []AutoCloseItem
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select o.id::text, o.order_number, o.legal_entity_id::text, o.updated_at, o.version
			from sales_order o
			where o.tenant_id = $1 and ($2 = '' or o.legal_entity_id::text = $2) and o.status = 'INVOICED' and o.order_type = 'ORDER'
				and o.updated_at <= $3
				and not exists (select 1 from order_hold h where h.tenant_id = o.tenant_id and h.order_id = o.id and h.status = 'ACTIVE')
			order by o.updated_at, o.id limit $4`, p.TenantID, f.LegalEntityID, cutoff, f.Limit+1)
		if err != nil {
			return err
		}
		cands, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (AutoCloseItem, error) {
			var it AutoCloseItem
			err := row.Scan(&it.OrderID, &it.OrderNumber, &it.LegalEntityID, &it.LastChangedAt, &it.Version)
			return it, err
		})
		return err
	})
	out := AutoCloseResult{Items: []AutoCloseItem{}}
	if err != nil {
		return out, err
	}
	if len(cands) > f.Limit {
		out.More, cands = true, cands[:f.Limit]
	}
	for _, it := range cands {
		if f.DryRun {
			it.Result = "CANDIDATE"
			out.Items = append(out.Items, it)
			continue
		}
		closed, version, err := s.closeInvoiced(ctx, it, cutoff, f.GraceDays, actor, now)
		if err != nil {
			return out, err
		}
		if closed {
			it.Result, it.Version = "CLOSED", version
			out.Closed++
		} else {
			it.Result = "SKIPPED"
			out.Skipped++
		}
		out.Items = append(out.Items, it)
	}
	return out, nil
}

// closeInvoiced closes one candidate, or reports false when it is not what the
// run listed any more.
func (s *Postgres) closeInvoiced(ctx context.Context, it AutoCloseItem, cutoff time.Time, graceDays int, actor string, now time.Time) (bool, int, error) {
	p, _ := tenant.FromContext(ctx)
	closed, version := false, 0
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		o, err := loadOrder(ctx, tx, p.TenantID, it.OrderID, true)
		if err != nil {
			return err
		}
		if o.Status != domain.OrderInvoiced || o.Version != it.Version || o.UpdatedAt.After(cutoff) {
			return nil
		}
		lines := make([]domain.LineProgress, len(o.Lines))
		for i, l := range o.Lines {
			lines[i] = domain.LineProgress{Status: l.LineStatus, Quantity: l.Quantity, Delivered: l.DeliveredQty, Invoiced: l.InvoicedQty}
		}
		if domain.OrderStatusFor(lines) != domain.OrderInvoiced {
			return nil
		}
		var active int
		if err := tx.QueryRow(ctx, `select count(*) from order_hold where tenant_id = $1 and order_id = $2 and status = 'ACTIVE'`,
			p.TenantID, o.ID).Scan(&active); err != nil {
			return err
		}
		if active > 0 {
			return nil
		}
		version = o.Version + 1
		if _, err := tx.Exec(ctx, `update sales_order set status = $3, version = $4, updated_at = $5 where tenant_id = $1 and id = $2`,
			p.TenantID, o.ID, domain.OrderClosed, version, now); err != nil {
			return err
		}
		diff := []domain.FieldChange{{LineNo: 0, Field: "status", Old: o.Status, New: domain.OrderClosed}}
		reason := fmt.Sprintf("auto-closed: invoiced and unchanged for %d days", graceDays)
		if err := insertVersion(ctx, tx, p.TenantID, o.LegalEntityID, o.ID, version, actor, reason, diff, now); err != nil {
			return err
		}
		closed = true
		return enqueue(ctx, tx, "order", o.ID, salesevents.OrderClosed, p.TenantID, salesevents.OrderClosedData{
			OrderID: o.ID, LegalEntityID: o.LegalEntityID, Version: version, PreviousStatus: o.Status, GraceDays: graceDays, Actor: actor})
	})
	return closed, version, err
}
