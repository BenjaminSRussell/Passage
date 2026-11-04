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
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/domain"
	fulevents "github.com/BenjaminSRussell/passage/services/fulfillment/internal/events"
)

// followDropShip makes the drop-ship of an order line follow sales' cancelled remainder: the
// quantity the vendor has not shipped above what the line keeps is closed (no booking, as for
// :close-remainder), a drop-ship with nothing shipped and nothing kept is cancelled, and a
// vendor confirmation already booked above the kept quantity raises a
// DROPSHIP_CONFIRMED_BEYOND_KEPT exception (nothing is reversed). dropship.closed or
// dropship.cancelled carries reason order_remainder_cancelled so sales does not cancel again.
// It runs in the consumer's transaction, after the shipment lines of the order line were cut.
func followDropShip(ctx context.Context, tx pgx.Tx, tenantID string, in RemainderCancelledIn, l RemainderLineIn) (cut, cancelled, exceptions int, err error) {
	d, err := scanDropShip(tx.QueryRow(ctx, `select `+dropShipColumns+` from drop_ship
		where order_line_id = $1 and status <> 'CANCELLED' for update`, strings.ToLower(l.OrderLineID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, 0, 0, nil
	}
	if err != nil {
		return 0, 0, 0, err
	}
	if d.QtyConfirmed.GreaterThan(l.KeptQty) {
		qty := d.QtyConfirmed.Sub(l.KeptQty)
		raised, err := raiseException(ctx, tx, tenantID, Exception{LegalEntityID: strings.ToLower(in.LegalEntityID), Kind: ExceptionDropShipBeyondKept,
			OrderID: strings.ToLower(in.OrderID), OrderLineID: strings.ToLower(l.OrderLineID),
			Detail: fmt.Sprintf("Drop-ship %s: the vendor confirmed %s of the order line before sales cancelled the remainder; the line keeps %s. %s too many shipped to the customer: arrange a vendor return or raise a new order. The booked cost is not reversed.",
				d.Number, d.QtyConfirmed, l.KeptQty, qty)}, in.EventID, qty, l.KeptQty)
		if err != nil {
			return 0, 0, 0, err
		}
		if raised {
			exceptions++
		}
	}
	keep := decimal.Max(decimal.Zero, l.KeptQty.Sub(d.QtyConfirmed))
	closeQty := d.QtyOpen.Sub(keep)
	if !closeQty.IsPositive() {
		return 0, 0, exceptions, nil
	}
	if d.QtyConfirmed.IsZero() && keep.IsZero() {
		d, err = scanDropShip(tx.QueryRow(ctx, `update drop_ship set status = 'CANCELLED', version = version + 1, updated_at = now() where id = $1 returning `+dropShipColumns, d.ID))
		if err != nil {
			return 0, 0, 0, err
		}
		data := dropShipData(d)
		data.Reason = fulevents.ReasonOrderRemainderCancelled
		return 0, 1, exceptions, enqueueFor(ctx, tx, "drop_ship", d.ID, fulevents.DropShipCancelled, tenantID, data)
	}
	closed := d.QtyClosed.Add(closeQty)
	status := d.Status
	if d.QtyConfirmed.IsPositive() {
		status = domain.DropShipStatusAfter(d.Qty, d.QtyConfirmed, closed)
	}
	d, err = scanDropShip(tx.QueryRow(ctx, `update drop_ship set status = $2, qty_closed = $3,
			closed_at = case when $4 then coalesce(closed_at, now()) else closed_at end, version = version + 1, updated_at = now()
		where id = $1 returning `+dropShipColumns, d.ID, status, closed, !domain.DropShipOpenQty(d.Qty, d.QtyConfirmed, closed).IsPositive()))
	if err != nil {
		return 0, 0, 0, err
	}
	data := dropShipData(d)
	data.Reason = fulevents.ReasonOrderRemainderCancelled
	return 1, 0, exceptions, enqueueFor(ctx, tx, "drop_ship", d.ID, fulevents.DropShipClosed, tenantID, data)
}

// keptOf returns the quantity sales kept for an order line when it cancelled the remainder, and
// false for a line sales never cut back.
func keptOf(ctx context.Context, tx pgx.Tx, orderLineID string) (decimal.Decimal, bool, error) {
	var kept decimal.Decimal
	err := tx.QueryRow(ctx, `select kept_qty from order_line_kept where order_line_id = $1`, strings.ToLower(orderLineID)).Scan(&kept)
	if errors.Is(err, pgx.ErrNoRows) {
		return decimal.Zero, false, nil
	}
	return kept, err == nil, err
}

// checkDropShipKept refuses a drop-ship create or vendor confirmation that would put more of an
// order line on the vendor than sales kept (FUL-0066); total is the quantity the line would then carry.
func checkDropShipKept(ctx context.Context, tx pgx.Tx, orderLineID string, total decimal.Decimal) error {
	kept, ok, err := keptOf(ctx, tx, orderLineID)
	if err != nil || !ok || !total.GreaterThan(kept) {
		return err
	}
	return httpx.NewProblem(http.StatusUnprocessableEntity, CodeKeptExceeded,
		fmt.Sprintf("FUL-0066: sales cancelled the remainder of the order line; it keeps %s and the drop-ship would carry %s", kept, total))
}
