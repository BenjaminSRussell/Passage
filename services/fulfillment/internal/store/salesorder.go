package store

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

// SalesOrderStore is what fulfillment keeps of sales' order.confirmed.v1: the order type per
// sales order (BR-27.7). Release and drop-ship creation read it so a JOURNAL order never ships
// whatever the caller names.
type SalesOrderStore interface {
	// OrderTypes returns the stored order type of each given sales order; an order fulfillment
	// has not heard of is absent from the map.
	OrderTypes(ctx context.Context, orderIDs []string) (map[string]string, error)
	// HeldOrders returns the given sales orders that have an active hold (sales orderhold.applied.v1
	// without its orderhold.released.v1).
	HeldOrders(ctx context.Context, orderIDs []string) (map[string]bool, error)
}

// CodeOrderHeld is FUL-0001 for a post on an order with an active hold (section 5.28.5).
const CodeOrderHeld = CodeStatus

// OrderHoldIn is the part of orderhold.applied.v1 / released.v1 fulfillment stores.
type OrderHoldIn struct {
	OrderID  string
	HoldID   string
	HoldType string
	Active   bool
	Version  int
	EventID  string
}

// ApplyOrderHold stores one hold's state inside tx (the consumer's inbox transaction). An event with a
// lower sales order version than the stored one changes nothing, so a redelivered applied event never
// re-activates a released hold. It reports whether the stored row changed.
func (s *Postgres) ApplyOrderHold(ctx context.Context, tx pgx.Tx, in OrderHoldIn) (bool, error) {
	p, ok := tenant.FromContext(ctx)
	if !ok {
		return false, errors.New("tenant context is required")
	}
	tag, err := tx.Exec(ctx, `insert into sales_order_hold (tenant_id, hold_id, order_id, hold_type, active, order_version, event_id)
		values ($1, $2, $3, $4, $5, $6, $7)
		on conflict (tenant_id, hold_id) do update
		set active = excluded.active, hold_type = excluded.hold_type, order_version = excluded.order_version, event_id = excluded.event_id, updated_at = now()
		where sales_order_hold.order_version <= excluded.order_version`,
		p.TenantID, strings.ToLower(in.HoldID), strings.ToLower(in.OrderID), in.HoldType, in.Active, in.Version, in.EventID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// HeldOrders implements SalesOrderStore.
func (s *Postgres) HeldOrders(ctx context.Context, orderIDs []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(orderIDs) == 0 {
		return out, nil
	}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		held, err := heldOrders(ctx, tx, orderIDs)
		out = held
		return err
	})
	return out, err
}

func heldOrders(ctx context.Context, tx pgx.Tx, orderIDs []string) (map[string]bool, error) {
	rows, err := tx.Query(ctx, `select distinct order_id::text from sales_order_hold where active and order_id = any($1::uuid[])`, orderIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// checkWorkable is checkShippable plus the order hold: pick, pack and auto-pack are refused (FUL-0001)
// while sales holds the order, so no work is started on goods that cannot ship.
func checkWorkable(ctx context.Context, tx pgx.Tx, sh Shipment) error {
	if err := checkShippable(sh); err != nil {
		return err
	}
	return checkNotHeld(ctx, tx, sh.OrderID)
}

func checkNotHeld(ctx context.Context, tx pgx.Tx, orderID string) error {
	held, err := heldOrders(ctx, tx, []string{orderID})
	if err != nil {
		return err
	}
	if held[strings.ToLower(orderID)] {
		return httpx.NewProblem(http.StatusUnprocessableEntity, CodeOrderHeld, "FUL-0001: the order has an active hold; release the hold before picking or packing")
	}
	return nil
}

// checkSalesOrder refuses a post for an order sales holds (FUL-0001) or confirmed as JOURNAL
// (FUL-0065), as fulfillment knows them from sales' events.
func checkSalesOrder(ctx context.Context, tx pgx.Tx, orderID string) error {
	held, err := heldOrders(ctx, tx, []string{orderID})
	if err != nil {
		return err
	}
	if held[strings.ToLower(orderID)] {
		return httpx.NewProblem(http.StatusUnprocessableEntity, CodeOrderHeld, "FUL-0001: the order has an active hold; release the hold before posting the packing slip")
	}
	var journal bool
	if err := tx.QueryRow(ctx, `select exists(select 1 from sales_order_type where order_id = $1::uuid and order_type = 'JOURNAL')`, orderID).Scan(&journal); err != nil {
		return err
	}
	if journal {
		return httpx.NewProblem(http.StatusUnprocessableEntity, CodeJournalOrder, "FUL-0065: sales confirmed the order as JOURNAL, which never ships")
	}
	return nil
}

// OrderConfirmedIn is the part of order.confirmed.v1 fulfillment stores.
type OrderConfirmedIn struct {
	OrderID   string
	OrderType string
	Version   int
	EventID   string
}

// ApplyOrderConfirmed stores the order type of one confirmed sales order inside tx (the consumer's
// inbox transaction). An event with a lower sales order version than the stored one changes
// nothing, so a redelivery or a dead-letter replay never turns a newer type back. It reports
// whether the stored row changed.
func (s *Postgres) ApplyOrderConfirmed(ctx context.Context, tx pgx.Tx, in OrderConfirmedIn) (bool, error) {
	p, ok := tenant.FromContext(ctx)
	if !ok {
		return false, errors.New("tenant context is required")
	}
	tag, err := tx.Exec(ctx, `insert into sales_order_type (tenant_id, order_id, order_type, order_version, event_id)
		values ($1, $2, $3, $4, $5)
		on conflict (tenant_id, order_id) do update
		set order_type = excluded.order_type, order_version = excluded.order_version, event_id = excluded.event_id, updated_at = now()
		where sales_order_type.order_version <= excluded.order_version`,
		p.TenantID, strings.ToLower(in.OrderID), in.OrderType, in.Version, in.EventID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// OrderTypes implements SalesOrderStore.
func (s *Postgres) OrderTypes(ctx context.Context, orderIDs []string) (map[string]string, error) {
	out := map[string]string{}
	if len(orderIDs) == 0 {
		return out, nil
	}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select order_id::text, order_type from sales_order_type where order_id = any($1::uuid[])`, orderIDs)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id, t string
			if err := rows.Scan(&id, &t); err != nil {
				return err
			}
			out[id] = t
		}
		return rows.Err()
	})
	return out, err
}
