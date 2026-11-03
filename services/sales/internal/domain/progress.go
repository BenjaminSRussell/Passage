package domain

import (
	"fmt"

	"github.com/shopspring/decimal"
)

// Delivery and invoice progress of an order line (BR-27.5, FR-28.8). The
// kinds name what was recorded: goods delivered (packing slip posted) or
// invoiced (billing).
const (
	ProgressDelivered = "DELIVERED"
	ProgressInvoiced  = "INVOICED"

	LinePicked    = "PICKED"
	LineDelivered = "DELIVERED"
	LineInvoiced  = "INVOICED"

	OrderPartiallyShipped = "PARTIALLY_SHIPPED"
	OrderShipped          = "SHIPPED"
	OrderInvoiced         = "INVOICED"

	OrderTypeSubscription = "SUBSCRIPTION"
	OrderTypeReturned     = "RETURNED"
)

// Promisable reports whether the open lines of an order in status may still be
// promised and taken by a backorder run: a partly shipped order keeps its
// undelivered OPEN and RESERVED lines in play until they ship (FR-28.8).
func Promisable(status string) bool {
	return status == OrderConfirmed || status == OrderPartiallyShipped
}

// LineProgress is the quantity state of one order line.
type LineProgress struct {
	Status    string
	Quantity  decimal.Decimal
	Delivered decimal.Decimal
	Invoiced  decimal.Decimal
}

// ProgressRefusal says why a quantity cannot be recorded.
type ProgressRefusal struct{ Detail string }

func (r *ProgressRefusal) Error() string { return r.Detail }

// ApplyProgress records qty of kind on a line and returns the line after it.
// Delivered may not pass the ordered quantity (the over-delivery tolerance is 0,
// see NOT-IMPLEMENTED) and invoiced may not pass delivered. A line in OPEN or
// RESERVED that receives its first delivery becomes PICKED (shipped in part,
// no longer changeable); the whole quantity delivered makes it DELIVERED and
// the whole delivered quantity invoiced, once everything is delivered, makes
// it INVOICED. A cancelled line takes nothing.
func ApplyProgress(l LineProgress, kind string, qty decimal.Decimal) (LineProgress, error) {
	if !qty.IsPositive() {
		return l, &ProgressRefusal{"quantity must be greater than 0"}
	}
	if l.Status == LineCancelled {
		return l, &ProgressRefusal{"the line is cancelled"}
	}
	switch kind {
	case ProgressDelivered:
		if l.Delivered.Add(qty).GreaterThan(l.Quantity) {
			return l, &ProgressRefusal{fmt.Sprintf("delivering %s would exceed the ordered quantity %s (already delivered %s)", qty, l.Quantity, l.Delivered)}
		}
		l.Delivered = l.Delivered.Add(qty)
	case ProgressInvoiced:
		if l.Invoiced.Add(qty).GreaterThan(l.Delivered) {
			return l, &ProgressRefusal{fmt.Sprintf("invoicing %s would exceed the delivered quantity %s (already invoiced %s)", qty, l.Delivered, l.Invoiced)}
		}
		l.Invoiced = l.Invoiced.Add(qty)
	default:
		return l, &ProgressRefusal{"kind must be DELIVERED or INVOICED"}
	}
	switch {
	case l.Invoiced.Equal(l.Quantity):
		l.Status = LineInvoiced
	case l.Delivered.Equal(l.Quantity):
		l.Status = LineDelivered
	case l.Delivered.IsPositive():
		l.Status = LinePicked
	}
	return l, nil
}

// OrderStatusFor derives the status of a progressing order from its lines
// (BR-27.5), cancelled lines left out: INVOICED when every line is invoiced,
// SHIPPED when every line is delivered, PARTIALLY_SHIPPED when any quantity was
// delivered, and CONFIRMED otherwise.
func OrderStatusFor(lines []LineProgress) string {
	live, invoiced, delivered, any := 0, 0, 0, false
	for _, l := range lines {
		if l.Status == LineCancelled {
			continue
		}
		live++
		if l.Delivered.IsPositive() {
			any = true
		}
		if l.Delivered.Equal(l.Quantity) {
			delivered++
		}
		if l.Invoiced.Equal(l.Quantity) {
			invoiced++
		}
	}
	switch {
	case live > 0 && invoiced == live:
		return OrderInvoiced
	case live > 0 && delivered == live:
		return OrderShipped
	case any:
		return OrderPartiallyShipped
	}
	return OrderConfirmed
}

// CancelRemainder closes what has not been delivered on a line of a partly
// shipped order (BR-27.5) and returns the line after it with the quantity it
// gave up. A line nothing was delivered on, still OPEN or RESERVED, is
// CANCELLED. A line delivered in part keeps what was delivered: its quantity
// becomes the delivered quantity and it is DELIVERED, or INVOICED once all of
// that was invoiced. A line already delivered in full, or cancelled, is left as
// it is. A line with nothing delivered in any other status cannot be closed.
func CancelRemainder(l LineProgress) (LineProgress, decimal.Decimal, error) {
	switch {
	case l.Status == LineCancelled, l.Delivered.GreaterThanOrEqual(l.Quantity):
		return l, decimal.Zero, nil
	case !l.Delivered.IsPositive():
		if !LineChangeable(l.Status) {
			return l, decimal.Zero, &ProgressRefusal{fmt.Sprintf("the line is %s and nothing was delivered on it", l.Status)}
		}
		gave := l.Quantity
		l.Status = LineCancelled
		return l, gave, nil
	}
	gave := l.Quantity.Sub(l.Delivered)
	l.Quantity = l.Delivered
	l.Status = LineDelivered
	if l.Invoiced.Equal(l.Quantity) {
		l.Status = LineInvoiced
	}
	return l, gave, nil
}
