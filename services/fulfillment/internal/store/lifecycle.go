package store

import (
	"context"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
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
			// A cancelled shipment frees the serial numbers it had taken.
			if _, err := tx.Exec(ctx, `delete from pick_tracking where tenant_id = $1 and shipment_id = $2`, p.TenantID, sh.ID); err != nil {
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

// UnpackContainer takes a CLOSED container apart before it ships. The packed
// quantities of its lines return to the unpacked pool, a PACKED shipment goes
// back to PICKED (so it can be repacked or, once empty, cancelled) and
// container.unpacked tells consumers the SSCC is void. The container row stays
// with status UNPACKED so the SSCC is never issued again (BR-28.5).
func (s *Postgres) UnpackContainer(ctx context.Context, id string, version int, containerID string, idem Idem) (Shipment, error) {
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
			idx := -1
			for i, c := range sh.Containers {
				if c.ID == containerID {
					idx = i
				}
			}
			if idx < 0 {
				return sh, httpx.NewProblem(http.StatusNotFound, CodeContainerNotFound, "container not found on this shipment")
			}
			cont := sh.Containers[idx]
			if cont.Status == "CLOSED" {
				if err := domain.UnpackNested(cont.ParentContainerID != nil, len(cont.ChildContainerIDs)); err != nil {
					return sh, mapDomain(err)
				}
			}
			next, err := domain.UnpackContainer(sh.Status, cont.Status)
			if err != nil {
				return sh, mapDomain(err)
			}
			// Bump the shipment first: a concurrent command on the same version
			// then waits on the shipment row and fails the version check (412)
			// instead of unpacking the container a second time.
			if err := bumpShipment(ctx, tx, p.TenantID, &sh, next); err != nil {
				return sh, err
			}
			if _, err := tx.Exec(ctx, `update container set status = 'UNPACKED', unpacked_at = now() where tenant_id = $1 and id = $2 and status = 'CLOSED'`,
				p.TenantID, cont.ID); err != nil {
				return sh, err
			}
			refs := []fulevents.LineRef{}
			for _, cl := range cont.Lines {
				if _, err := tx.Exec(ctx, `update shipment_line set qty_packed = qty_packed - $3 where tenant_id = $1 and id = $2`,
					p.TenantID, cl.ShipmentLineID, cl.Qty); err != nil {
					return sh, err
				}
				li := lineIndex(sh, cl.ShipmentLineID)
				sh.Lines[li].Packed = sh.Lines[li].Packed.Sub(cl.Qty)
				refs = append(refs, fulevents.LineRef{OrderLineID: sh.Lines[li].OrderLineID, Qty: cl.Qty.String()})
			}
			sh.Containers = append(sh.Containers[:idx], sh.Containers[idx+1:]...)
			if err := enqueue(ctx, tx, sh.ID, fulevents.ContainerUnpacked, p.TenantID, fulevents.ContainerUnpackedData{
				ShipmentID: sh.ID, ShipmentNumber: sh.ShipmentNumber, ContainerID: cont.ID, SSCC: cont.SSCC, Lines: refs,
			}); err != nil {
				return sh, err
			}
			// Sales may have cancelled the remainder while this quantity was packed (a
			// PACKED_BEYOND_KEPT exception): what is unpacked above the kept quantity is cut now.
			orderLines := make([]string, 0, len(refs))
			for _, r := range refs {
				orderLines = append(orderLines, r.OrderLineID)
			}
			trimmed, err := trimToKept(ctx, tx, p.TenantID, sh.OrderID, orderLines)
			if err != nil || !trimmed {
				return sh, err
			}
			return loadShipment(ctx, tx, sh.ID)
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
			_, err = closeRemainderTx(ctx, tx, p.TenantID, &sh, nil)
			return sh, err
		})
		out = res
		return err
	})
	return out, err
}

// closeRemainderTx closes the open remainder of sh inside tx. With backorderDate
// set it also moves the remainder to a new RELEASED shipment of the same order
// and warehouse planned for that date, and returns it; shipment.closed names the
// new shipment so sales keeps the order lines open.
func closeRemainderTx(ctx context.Context, tx pgx.Tx, tenantID string, sh *Shipment, backorderDate *time.Time) (*Shipment, error) {
	next, closed, err := domain.CloseRemainder(sh.Status, lineQtys(*sh))
	if err != nil {
		return nil, mapDomain(err)
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
			where tenant_id = $1 and id = $2`, tenantID, l.ID, c.Closed); err != nil {
			return nil, err
		}
		lines = append(lines, fulevents.ClosedLine{OrderLineID: l.OrderLineID, Qty: c.Closed.String(), PickedUnpacked: c.PickedUnpacked.String()})
		l.Closed = l.Closed.Add(c.Closed)
		l.Planned, l.Picked, l.Packed = l.Shipped, l.Shipped, l.Shipped
	}
	// Entries that never shipped go with the closed remainder: their
	// serials are free again and the line keeps only what shipped.
	if _, err := tx.Exec(ctx, `delete from pick_tracking where tenant_id = $1 and shipment_id = $2 and packing_slip_id is null`,
		tenantID, sh.ID); err != nil {
		return nil, err
	}
	for i := range sh.Lines {
		sh.Lines[i].Tracking = shippedEntries(sh.Lines[i].Tracking)
	}
	if err := bumpShipment(ctx, tx, tenantID, sh, next); err != nil {
		return nil, err
	}
	data := fulevents.ShipmentClosedData{ShipmentID: sh.ID, ShipmentNumber: sh.ShipmentNumber, OrderID: sh.OrderID, WarehouseID: sh.WarehouseID, Lines: lines}
	var backorder *Shipment
	if backorderDate != nil {
		b, err := insertBackorder(ctx, tx, tenantID, *sh, closed, *backorderDate)
		if err != nil {
			return nil, err
		}
		sh.BackorderID = &b.ID
		data.BackorderShipmentID = b.ID
		backorder = &b
	}
	return backorder, enqueue(ctx, tx, sh.ID, fulevents.ShipmentClosed, tenantID, data)
}

// insertBackorder creates the shipment that takes over the remainder closed on
// sh: the lines that owed quantity, planned for the closed quantity.
func insertBackorder(ctx context.Context, tx pgx.Tx, tenantID string, sh Shipment, closed []domain.ClosedQty, date time.Time) (Shipment, error) {
	group := domain.ReleaseGroup{OrderID: sh.OrderID, WarehouseID: sh.WarehouseID, ShipDate: date, Priority: sh.Priority}
	carrier := ""
	if sh.CarrierCode != nil {
		carrier = *sh.CarrierCode
	}
	for _, b := range domain.BackorderLines(closed) {
		l := sh.Lines[b.LineIndex]
		zone := ""
		if l.Zone != nil {
			zone = *l.Zone
		}
		group.Lines = append(group.Lines, domain.ReleaseLine{OrderID: sh.OrderID, OrderLineID: l.OrderLineID, CustomerID: sh.CustomerID,
			LegalEntityID: sh.LegalEntityID, WarehouseID: sh.WarehouseID, ProductID: l.ProductID, Unit: l.Unit, Qty: b.Qty,
			CarrierCode: carrier, Zone: zone, TrackingRequired: l.TrackingRequired, OrderType: sh.OrderType, Priority: sh.Priority})
	}
	origin := sh.ID
	return insertReleased(ctx, tx, tenantID, group, &origin)
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
	ssccOf := map[string]string{}
	for _, c := range sh.Containers {
		ssccOf[c.ID] = c.SSCC
	}
	for _, c := range sh.Containers {
		if c.Status != "CLOSED" {
			continue
		}
		asn := fulevents.ASNContainer{SSCC: c.SSCC, ContainerType: c.ContainerType, Lines: []fulevents.ASNLine{}}
		if c.ParentContainerID != nil {
			asn.ParentSSCC = ssccOf[*c.ParentContainerID]
		}
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

// deref maps nil to "".
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
