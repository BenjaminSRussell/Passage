package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/domain"
)

// BackorderFilter selects the short picks and backorders report (section
// 5.28.14: warehouse, period). WarehouseID "" means every warehouse. From and
// To are inclusive UTC dates on the first short pick; a zero time leaves that
// bound open. IncludeClosed also returns lines whose remainder was re-picked
// or closed (FR-28.8), or whose shipment was cancelled. AfterLineID is the keyset cursor: the last
// shipment line of the previous page.
type BackorderFilter struct {
	WarehouseID   string
	From, To      time.Time
	IncludeClosed bool
	AfterLineID   string
	Limit         int
}

// BackorderRow is one short-picked shipment line with its quantities now.
type BackorderRow struct {
	ShipmentLineID string                `json:"shipmentLineId"`
	ShipmentID     string                `json:"shipmentId"`
	ShipmentNumber string                `json:"shipmentNumber"`
	Status         domain.ShipmentStatus `json:"status"`
	WarehouseID    string                `json:"warehouseId"`
	OrderID        string                `json:"orderId"`
	CustomerID     string                `json:"customerId"`
	OrderLineID    string                `json:"orderLineId"`
	ProductID      string                `json:"productId"`
	Unit           string                `json:"unit"`
	Planned        decimal.Decimal       `json:"qtyPlanned"`
	Picked         decimal.Decimal       `json:"qtyPicked"`
	Shipped        decimal.Decimal       `json:"qtyShipped"`
	// Closed is the planned quantity given up when the shipment's remainder
	// was closed (FR-28.8); Planned is then what shipped.
	Closed        decimal.Decimal `json:"qtyClosed"`
	ShortPickedAt time.Time       `json:"shortPickedAt"`
}

// ListBackorders returns the short-picked lines, newest short pick first, at
// most f.Limit rows. Lines are read through their shipment, so the tenant and
// legal-entity row-level security of the shipment applies.
func (s *Postgres) ListBackorders(ctx context.Context, f BackorderFilter) ([]BackorderRow, error) {
	out := []BackorderRow{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			select sl.id::text, sh.id::text, sh.shipment_number, sh.status, sh.warehouse_id::text, sh.order_id::text,
				sh.customer_id::text, sl.order_line_id::text, sl.product_id::text, sl.unit,
				sl.qty_planned::text, sl.qty_picked::text, sl.qty_shipped::text, sl.qty_closed::text, sl.short_picked_at
			from shipment_line sl
			join shipment sh on sh.tenant_id = sl.tenant_id and sh.id = sl.shipment_id
			where sl.short_picked_at is not null
			  and ($1::text = '' or sh.warehouse_id = nullif($1::text, '')::uuid)
			  and ($2::date is null or sl.short_picked_at >= ($2::date)::timestamp at time zone 'UTC')
			  and ($3::date is null or sl.short_picked_at < ($3::date + 1)::timestamp at time zone 'UTC')
			  and ($4::boolean or (sl.qty_picked < sl.qty_planned and sh.status <> 'CANCELLED'))
			  and ($5::text = '' or (sl.short_picked_at, sl.id) <
			       (select c.short_picked_at, c.id from shipment_line c where c.id = nullif($5::text, '')::uuid))
			order by sl.short_picked_at desc, sl.id desc
			limit $6`,
			f.WarehouseID, dateParam(f.From), dateParam(f.To), f.IncludeClosed, f.AfterLineID, f.Limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r BackorderRow
			var planned, picked, shipped, closed string
			if err := rows.Scan(&r.ShipmentLineID, &r.ShipmentID, &r.ShipmentNumber, &r.Status, &r.WarehouseID, &r.OrderID,
				&r.CustomerID, &r.OrderLineID, &r.ProductID, &r.Unit, &planned, &picked, &shipped, &closed, &r.ShortPickedAt); err != nil {
				return err
			}
			r.Planned, r.Picked, r.Shipped, r.Closed = dec(planned), dec(picked), dec(shipped), dec(closed)
			r.ShortPickedAt = r.ShortPickedAt.UTC()
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}
