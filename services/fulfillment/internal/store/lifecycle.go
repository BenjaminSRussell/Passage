package store

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/domain"
	fulevents "github.com/BenjaminSRussell/passage/services/fulfillment/internal/events"
)

// CancelShipment cancels a shipment that has shipped nothing and holds no
// container (BR-28.6). The order lines become releasable again (release skips
// only lines on a shipment that is not cancelled). Releasing the lines'
// reservations is inventory's reaction to shipment.cancelled; no inventory row
// is written here.
func (s *Postgres) CancelShipment(ctx context.Context, id string, version int, idem Idem) (Shipment, error) {
	p, _ := tenant.FromContext(ctx)
	var out Shipment
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		res, err := runIdempotent(ctx, tx, p.TenantID, idem, func() (Shipment, error) {
			sh, err := loadShipment(ctx, tx, id)
			if err != nil {
				return sh, err
			}
			if err := checkVersion(sh, version); err != nil {
				return sh, err
			}
			next, err := domain.CancelShipment(sh.Status, lineQtys(sh), len(sh.Containers))
			if err != nil {
				return sh, mapDomain(err)
			}
			if err := bumpShipment(ctx, tx, p.TenantID, &sh, next); err != nil {
				return sh, err
			}
			refs := []fulevents.LineRef{}
			for _, l := range sh.Lines {
				refs = append(refs, fulevents.LineRef{OrderLineID: l.OrderLineID, Qty: l.Planned.String()})
			}
			return sh, enqueue(ctx, tx, sh.ID, fulevents.ShipmentCancelled, p.TenantID, fulevents.ShipmentCancelledData{
				ShipmentID: sh.ID, ShipmentNumber: sh.ShipmentNumber, OrderID: sh.OrderID, WarehouseID: sh.WarehouseID, Lines: refs,
			})
		})
		out = res
		return err
	})
	return out, err
}

// CloseRemainder gives up the open remainder of a shipment that has shipped
// part of its quantity (FR-28.8, CANCEL_REMAINDER). Every line shrinks to what
// shipped, qty_closed records the quantity given up, the shipment becomes
// SHIPPED and shipment.closed carries the closed and the picked-but-unpacked quantities
// so inventory can release the reservation and any picked stock never packed.
func (s *Postgres) CloseRemainder(ctx context.Context, id string, version int, idem Idem) (Shipment, error) {
	p, _ := tenant.FromContext(ctx)
	var out Shipment
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		res, err := runIdempotent(ctx, tx, p.TenantID, idem, func() (Shipment, error) {
			sh, err := loadShipment(ctx, tx, id)
			if err != nil {
				return sh, err
			}
			if err := checkVersion(sh, version); err != nil {
				return sh, err
			}
			next, closed, err := domain.CloseRemainder(sh.Status, lineQtys(sh))
			if err != nil {
				return sh, mapDomain(err)
			}
			lines := []fulevents.ClosedLine{}
			for i := range sh.Lines {
				l := &sh.Lines[i]
				c := closed[i]
				if !c.Closed.IsPositive() {
					continue
				}
				if _, err := tx.Exec(ctx, `update shipment_line set qty_closed = qty_closed + $3, closed_at = now(),
					qty_planned = qty_shipped, qty_picked = qty_shipped, qty_packed = qty_shipped
					where tenant_id = $1 and id = $2`, p.TenantID, l.ID, c.Closed); err != nil {
					return sh, err
				}
				lines = append(lines, fulevents.ClosedLine{OrderLineID: l.OrderLineID, Qty: c.Closed.String(), PickedUnpacked: c.PickedUnpacked.String()})
				l.Closed = l.Closed.Add(c.Closed)
				l.Planned, l.Picked, l.Packed = l.Shipped, l.Shipped, l.Shipped
			}
			if err := bumpShipment(ctx, tx, p.TenantID, &sh, next); err != nil {
				return sh, err
			}
			return sh, enqueue(ctx, tx, sh.ID, fulevents.ShipmentClosed, p.TenantID, fulevents.ShipmentClosedData{
				ShipmentID: sh.ID, ShipmentNumber: sh.ShipmentNumber, OrderID: sh.OrderID, WarehouseID: sh.WarehouseID, Lines: lines,
			})
		})
		out = res
		return err
	})
	return out, err
}

// asnData builds the advance ship notice (FR-28.9) for slip from the shipment's
// CLOSED containers, the ones that leave with it, mapping shipment lines back
// to order lines. Call it before the containers are marked SHIPPED.
func asnData(sh Shipment, slip PackingSlip) fulevents.ASNIssuedData {
	orderLine := map[string]string{}
	for _, l := range sh.Lines {
		orderLine[l.ID] = l.OrderLineID
	}
	data := fulevents.ASNIssuedData{ShipmentID: sh.ID, ShipmentNumber: sh.ShipmentNumber, OrderID: sh.OrderID,
		PackingSlipID: slip.ID, PackingSlipNumber: slip.Number, Containers: []fulevents.ASNContainer{}}
	if sh.CarrierCode != nil {
		data.Carrier = *sh.CarrierCode
	}
	for _, c := range sh.Containers {
		if c.Status != "CLOSED" {
			continue
		}
		asn := fulevents.ASNContainer{SSCC: c.SSCC, ContainerType: c.ContainerType, Lines: []fulevents.ASNLine{}}
		for _, cl := range c.Lines {
			asn.Lines = append(asn.Lines, fulevents.ASNLine{OrderLineID: orderLine[cl.ShipmentLineID], Qty: cl.Qty.String()})
		}
		data.Containers = append(data.Containers, asn)
	}
	return data
}

// countSlipReturnables counts the returnable containers that shipped with one
// slip, per type: the containers a cancellation takes back off the balance.
func countSlipReturnables(ctx context.Context, tx pgx.Tx, slipID string) ([]returnableCount, error) {
	rows, err := tx.Query(ctx, `select container_type, count(*) from container
		where packing_slip_id = $1 and returnable group by container_type order by container_type`, slipID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []returnableCount{}
	for rows.Next() {
		var c returnableCount
		if err := rows.Scan(&c.ContainerType, &c.Qty); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// optional maps "" to a SQL null.
func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
