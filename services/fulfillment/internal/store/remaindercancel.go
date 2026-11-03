package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/domain"
	fulevents "github.com/BenjaminSRussell/passage/services/fulfillment/internal/events"
)

// RemainderLineIn is one line of sales' order.remaindercancelled.v1: the quantity the order line keeps.
type RemainderLineIn struct {
	OrderLineID string
	KeptQty     decimal.Decimal
}

// RemainderCancelledIn is the part of order.remaindercancelled.v1 fulfillment acts on.
type RemainderCancelledIn struct {
	OrderID       string
	LegalEntityID string
	Version       int
	Lines         []RemainderLineIn
	EventID       string
}

// RemainderCancelledResult counts what one event changed.
type RemainderCancelledResult struct {
	LinesApplied       int
	ShipmentsCut       int
	ShipmentsCancelled int
	Exceptions         int
}

type cutLine struct {
	lineID, shipmentID                string
	orderLineID                       string
	oldPlanned, oldPicked, newPlanned decimal.Decimal
	newPicked, take, shipped          decimal.Decimal
}

type openLine struct {
	id, shipmentID, status           string
	planned, picked, packed, shipped decimal.Decimal
}

// ApplyOrderRemainderCancelled follows sales cancelling the remainder of a partly shipped order,
// inside tx (the consumer's inbox transaction). Per order line it stores the kept quantity (an
// event with a lower sales order version than the stored one changes nothing for that line) and
// then cuts the quantity that has not been packed: shipment lines shrink to what shipped or is
// packed, picked quantity above the new plan returns to stock, and a shipment left with nothing
// to ship and nothing shipped is cancelled. Inventory releases the reservations from the
// shipment.closed (reason order_remainder_cancelled) and shipment.cancelled events. Quantity
// that is packed but not posted cannot be cut here: the post is refused (FUL-0066) and a
// PACKED_BEYOND_KEPT exception is raised. Quantity a slip already shipped beyond the kept
// quantity is raised as SLIP_POSTED_BEYOND_KEPT and not retried.
//
// The consumer's service principal carries no legal entity, so the transaction runs with the
// legal-entity scope of the event for the duration of the call.
func (s *Postgres) ApplyOrderRemainderCancelled(ctx context.Context, tx pgx.Tx, in RemainderCancelledIn) (RemainderCancelledResult, error) {
	var res RemainderCancelledResult
	p, ok := tenant.FromContext(ctx)
	if !ok {
		return res, errors.New("tenant context is required")
	}
	var saved string
	if err := tx.QueryRow(ctx, `select coalesce(current_setting('passage.legal_entity_scopes', true), ''), set_config('passage.legal_entity_scopes', $1, true)`,
		strings.ToLower(in.LegalEntityID)).Scan(&saved, new(string)); err != nil {
		return res, err
	}
	res, err := applyRemainder(ctx, tx, p.TenantID, in)
	if err != nil {
		return res, err
	}
	_, err = tx.Exec(ctx, `select set_config('passage.legal_entity_scopes', $1, true)`, saved)
	return res, err
}

func applyRemainder(ctx context.Context, tx pgx.Tx, tenantID string, in RemainderCancelledIn) (RemainderCancelledResult, error) {
	var res RemainderCancelledResult
	order := strings.ToLower(in.OrderID)
	// Lock the order's live shipments in id order: a post or pick on one waits for this event
	// and then sees the cut quantities (and the kept quantity the post checks).
	if _, err := tx.Exec(ctx, `select id from shipment where order_id = $1 and status <> 'CANCELLED' order by id for update`, order); err != nil {
		return res, err
	}
	cuts := map[string][]cutLine{}
	var shipmentOrder []string
	for _, l := range in.Lines {
		var stored string
		err := tx.QueryRow(ctx, `insert into order_line_kept (tenant_id, order_line_id, order_id, legal_entity_id, kept_qty, order_version, event_id)
			values ($1, $2, $3, $4, $5, $6, $7)
			on conflict (tenant_id, order_line_id) do update
			set kept_qty = excluded.kept_qty, order_version = excluded.order_version, event_id = excluded.event_id, updated_at = now()
			where order_line_kept.order_version <= excluded.order_version
			returning order_line_id::text`,
			tenantID, strings.ToLower(l.OrderLineID), order, strings.ToLower(in.LegalEntityID), l.KeptQty, in.Version, in.EventID).Scan(&stored)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return res, err
		}
		res.LinesApplied++
		rows, err := loadOpenLines(ctx, tx, order, l.OrderLineID)
		if err != nil {
			return res, err
		}
		lineCuts, exceptions, err := planCut(ctx, tx, tenantID, in, l, rows)
		if err != nil {
			return res, err
		}
		res.Exceptions += exceptions
		for _, c := range lineCuts {
			if _, seen := cuts[c.shipmentID]; !seen {
				shipmentOrder = append(shipmentOrder, c.shipmentID)
			}
			cuts[c.shipmentID] = append(cuts[c.shipmentID], c)
		}
	}
	cut, cancelled, err := applyCutsInOrder(ctx, tx, tenantID, shipmentOrder, cuts)
	res.ShipmentsCut, res.ShipmentsCancelled = cut, cancelled
	return res, err
}

// applyCutsInOrder applies the planned cuts shipment by shipment and counts the shipments cut and cancelled.
func applyCutsInOrder(ctx context.Context, tx pgx.Tx, tenantID string, order []string, cuts map[string][]cutLine) (int, int, error) {
	cut, cancelledN := 0, 0
	for _, shipmentID := range order {
		cancelled, err := applyCuts(ctx, tx, tenantID, shipmentID, cuts[shipmentID])
		if err != nil {
			return cut, cancelledN, err
		}
		cut++
		if cancelled {
			cancelledN++
		}
	}
	return cut, cancelledN, nil
}

// trimToKept re-applies the kept quantity of sales' cancelled remainder to the given order lines
// of an order after packed quantity was given back (a container unpacked): the unpacked quantity
// above what the line keeps could not be cut when the event arrived and is cut now, so the
// shipment can be repacked to the kept quantity and posted. A line sales never cut back has no
// kept row and is left alone. It reports whether any shipment was cut.
func trimToKept(ctx context.Context, tx pgx.Tx, tenantID, orderID string, orderLineIDs []string) (bool, error) {
	cuts := map[string][]cutLine{}
	var shipmentOrder []string
	seen := map[string]bool{}
	for _, id := range orderLineIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		var kept decimal.Decimal
		err := tx.QueryRow(ctx, `select kept_qty from order_line_kept where order_line_id = $1`, id).Scan(&kept)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return false, err
		}
		rows, err := loadOpenLines(ctx, tx, strings.ToLower(orderID), id)
		if err != nil {
			return false, err
		}
		lineCuts, _ := cutsFor(id, kept, rows)
		for _, c := range lineCuts {
			if _, ok := cuts[c.shipmentID]; !ok {
				shipmentOrder = append(shipmentOrder, c.shipmentID)
			}
			cuts[c.shipmentID] = append(cuts[c.shipmentID], c)
		}
	}
	n, _, err := applyCutsInOrder(ctx, tx, tenantID, shipmentOrder, cuts)
	return n > 0, err
}

// loadOpenLines reads the shipment lines of an order line on shipments that are not cancelled,
// newest shipment first (the newest quantity is cut first).
func loadOpenLines(ctx context.Context, tx pgx.Tx, orderID, orderLineID string) ([]openLine, error) {
	rows, err := tx.Query(ctx, `select sl.id, sl.shipment_id, sh.status, sl.qty_planned, sl.qty_picked, sl.qty_packed, sl.qty_shipped
		from shipment_line sl join shipment sh on sh.tenant_id = sl.tenant_id and sh.id = sl.shipment_id
		where sl.order_line_id = $1 and sh.order_id = $2 and sh.status <> 'CANCELLED'
		order by sh.created_at desc, sh.id desc, sl.id desc`, strings.ToLower(orderLineID), orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []openLine
	for rows.Next() {
		var r openLine
		if err := rows.Scan(&r.id, &r.shipmentID, &r.status, &r.planned, &r.picked, &r.packed, &r.shipped); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// cutsFor plans, for one order line that keeps kept, which unposted quantity to cut from which
// shipment lines (rows newest first): the unshipped planned quantity above kept minus what shipped
// is cut, never below what a line has packed or shipped. It returns the cuts and the quantity above
// kept that could not be cut because it is packed.
func cutsFor(orderLineID string, kept decimal.Decimal, rows []openLine) ([]cutLine, decimal.Decimal) {
	shipped, unshipped := decimal.Zero, decimal.Zero
	for _, r := range rows {
		shipped = shipped.Add(r.shipped)
		unshipped = unshipped.Add(r.planned.Sub(r.shipped))
	}
	allowed := decimal.Max(decimal.Zero, kept.Sub(shipped))
	remaining := decimal.Max(decimal.Zero, unshipped.Sub(allowed))
	var cuts []cutLine
	for _, r := range rows {
		if !remaining.IsPositive() {
			break
		}
		reducible := r.planned.Sub(decimal.Max(r.shipped, r.packed))
		take := decimal.Min(reducible, remaining)
		if !take.IsPositive() {
			continue
		}
		newPlanned := r.planned.Sub(take)
		cuts = append(cuts, cutLine{lineID: r.id, shipmentID: r.shipmentID, orderLineID: strings.ToLower(orderLineID), oldPlanned: r.planned, oldPicked: r.picked,
			newPlanned: newPlanned, newPicked: decimal.Min(r.picked, newPlanned), take: take, shipped: r.shipped})
		remaining = remaining.Sub(take)
	}
	return cuts, remaining
}

// planCut decides, for one order line, how much unposted quantity to cut from which shipment
// lines, and raises the exceptions for what cannot be cut.
func planCut(ctx context.Context, tx pgx.Tx, tenantID string, in RemainderCancelledIn, l RemainderLineIn, rows []openLine) ([]cutLine, int, error) {
	shipped := decimal.Zero
	for _, r := range rows {
		shipped = shipped.Add(r.shipped)
	}
	exceptions := 0
	if shipped.GreaterThan(l.KeptQty) {
		var slipID, shipmentID *string
		err := tx.QueryRow(ctx, `select ps.id::text, ps.shipment_id::text from packing_slip_line pl
			join packing_slip ps on ps.tenant_id = pl.tenant_id and ps.id = pl.packing_slip_id
			where pl.order_line_id = $1 and ps.reversal_of_id is null and ps.status in ('POSTED', 'INVOICED')
			order by ps.created_at desc limit 1`, strings.ToLower(l.OrderLineID)).Scan(&slipID, &shipmentID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, 0, err
		}
		qty := shipped.Sub(l.KeptQty)
		raised, err := raiseException(ctx, tx, tenantID, Exception{LegalEntityID: strings.ToLower(in.LegalEntityID), Kind: ExceptionSlipPostedBeyondKept,
			OrderID: strings.ToLower(in.OrderID), OrderLineID: strings.ToLower(l.OrderLineID), ShipmentID: shipmentID, PackingSlipID: slipID,
			Detail: fmt.Sprintf("A packing slip shipped %s of the order line after sales cancelled the remainder; the line keeps %s. %s too many left the warehouse: take them back through a return (wave 30) or raise a new order.",
				shipped, l.KeptQty, qty)}, in.EventID, qty, l.KeptQty)
		if err != nil {
			return nil, 0, err
		}
		if raised {
			exceptions++
		}
	}
	cuts, remaining := cutsFor(l.OrderLineID, l.KeptQty, rows)
	for _, r := range rows {
		if !remaining.IsPositive() {
			break
		}
		q := decimal.Min(r.packed.Sub(r.shipped), remaining)
		if !q.IsPositive() {
			continue
		}
		sid := r.shipmentID
		raised, err := raiseException(ctx, tx, tenantID, Exception{LegalEntityID: strings.ToLower(in.LegalEntityID), Kind: ExceptionPackedBeyondKept,
			OrderID: strings.ToLower(in.OrderID), OrderLineID: strings.ToLower(l.OrderLineID), ShipmentID: &sid,
			Detail: fmt.Sprintf("%s packed but not posted is more than the order line keeps (%s) after sales cancelled the remainder. The post is refused (FUL-0066): unpack the containers and put the goods back; the unpacked quantity above what the line keeps is then cut from the shipment.",
				q, l.KeptQty)}, in.EventID, q, l.KeptQty)
		if err != nil {
			return nil, 0, err
		}
		if raised {
			exceptions++
		}
		remaining = remaining.Sub(q)
	}
	return cuts, exceptions, nil
}

// applyCuts shrinks the lines of one shipment and publishes shipment.closed, or cancels the
// shipment when nothing is left to ship. It reports whether the shipment was cancelled.
func applyCuts(ctx context.Context, tx pgx.Tx, tenantID, shipmentID string, cuts []cutLine) (bool, error) {
	sh, err := loadShipment(ctx, tx, shipmentID)
	if err != nil {
		return false, err
	}
	closed := []fulevents.ClosedLine{}
	cancelledRefs := []fulevents.LineRef{}
	for _, c := range cuts {
		if _, err := tx.Exec(ctx, `update shipment_line set qty_planned = $3, qty_picked = $4, qty_closed = qty_closed + $5, closed_at = now()
			where tenant_id = $1 and id = $2`, tenantID, c.lineID, c.newPlanned, c.newPicked, c.take); err != nil {
			return false, err
		}
		if c.newPicked.LessThan(c.oldPicked) {
			// Unshipped batch and serial entries that now cover more than stays picked cannot be
			// matched to the units that went back: they are dropped and captured again for what
			// stays picked. Entries that still fit are kept.
			if _, err := tx.Exec(ctx, `delete from pick_tracking where tenant_id = $1 and shipment_line_id = $2 and packing_slip_id is null
				and (select coalesce(sum(t.qty), 0) from pick_tracking t where t.tenant_id = $1 and t.shipment_line_id = $2 and t.packing_slip_id is null) > $3::numeric`,
				tenantID, c.lineID, c.newPicked.Sub(c.shipped)); err != nil {
				return false, err
			}
		}
		if !c.newPlanned.IsPositive() {
			// Nothing is left to pick on the line: it no longer holds the shipment in PICKING.
			if _, err := tx.Exec(ctx, `update shipment_line set pick_confirmed = true where tenant_id = $1 and id = $2`, tenantID, c.lineID); err != nil {
				return false, err
			}
		}
		i := lineIndex(sh, c.lineID)
		sh.Lines[i].Planned, sh.Lines[i].Picked = c.newPlanned, c.newPicked
		sh.Lines[i].Closed = sh.Lines[i].Closed.Add(c.take)
		if !c.newPlanned.IsPositive() {
			sh.Lines[i].PickConfirmed = true
		}
		closed = append(closed, fulevents.ClosedLine{OrderLineID: c.orderLineID, Qty: c.take.String(), PickedUnpacked: c.oldPicked.Sub(c.newPicked).String()})
		cancelledRefs = append(cancelledRefs, fulevents.LineRef{OrderLineID: c.orderLineID, Qty: c.take.String()})
	}
	allGone, anyShipped, allShipped, packedWaiting := true, false, true, false
	for _, l := range sh.Lines {
		if l.Planned.IsPositive() {
			allGone = false
		}
		if l.Shipped.IsPositive() {
			anyShipped = true
		}
		if !l.Shipped.Equal(l.Planned) {
			allShipped = false
		}
		if l.Packed.GreaterThan(l.Shipped) {
			packedWaiting = true
		}
	}
	if allGone && !anyShipped && len(sh.Containers) == 0 {
		next, err := domain.CancelShipment(sh.Status, lineQtys(sh), 0)
		if err == nil {
			if err := bumpShipment(ctx, tx, tenantID, &sh, next); err != nil {
				return false, err
			}
			return true, enqueue(ctx, tx, sh.ID, fulevents.ShipmentCancelled, tenantID, fulevents.ShipmentCancelledData{
				ShipmentID: sh.ID, ShipmentNumber: sh.ShipmentNumber, OrderID: sh.OrderID, WarehouseID: sh.WarehouseID, Lines: cancelledRefs})
		}
		if !errors.Is(err, domain.ErrInvalidMove) {
			return false, mapDomain(err)
		}
	}
	next := sh.Status
	switch sh.Status {
	case domain.StatusPartiallyShipped, domain.StatusPicking, domain.StatusPicked:
		if anyShipped && allShipped && !packedWaiting {
			next = domain.StatusShipped
			break
		}
		// The cut may leave the work done: every line confirmed (PICKING -> PICKED) and every
		// picked unit packed (PICKED -> PACKED), as a pick or pack that got there would.
		qtys := lineQtys(sh)
		if next == domain.StatusPicking && domain.AllConfirmed(qtys) {
			next = domain.StatusPicked
		}
		if next == domain.StatusPicked && packedWaiting && domain.AllPacked(qtys) {
			next = domain.StatusPacked
		}
	}
	if err := bumpShipment(ctx, tx, tenantID, &sh, next); err != nil {
		return false, err
	}
	return false, enqueue(ctx, tx, sh.ID, fulevents.ShipmentClosed, tenantID, fulevents.ShipmentClosedData{
		ShipmentID: sh.ID, ShipmentNumber: sh.ShipmentNumber, OrderID: sh.OrderID, WarehouseID: sh.WarehouseID, Lines: closed,
		Reason: fulevents.ReasonOrderRemainderCancelled})
}

// checkKeptQty refuses a post that would ship more of an order line than sales kept when it
// cancelled the remainder (FUL-0066). A line sales never cut back has no row and is not checked.
func checkKeptQty(ctx context.Context, tx pgx.Tx, lines []PackingSlipLine) error {
	post := map[string]decimal.Decimal{}
	var order []string
	for _, l := range lines {
		if _, ok := post[l.OrderLineID]; !ok {
			order = append(order, l.OrderLineID)
		}
		post[l.OrderLineID] = post[l.OrderLineID].Add(l.Qty)
	}
	for _, id := range order {
		var kept, shipped decimal.Decimal
		err := tx.QueryRow(ctx, `select k.kept_qty, coalesce((select sum(sl.qty_shipped) from shipment_line sl where sl.tenant_id = k.tenant_id and sl.order_line_id = k.order_line_id), 0)
			from order_line_kept k where k.order_line_id = $1`, id).Scan(&kept, &shipped)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		if shipped.Add(post[id]).GreaterThan(kept) {
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeKeptExceeded,
				fmt.Sprintf("FUL-0066: sales cancelled the remainder of the order line; it keeps %s, %s has shipped and this post adds %s", kept, shipped, post[id]))
		}
	}
	return nil
}
