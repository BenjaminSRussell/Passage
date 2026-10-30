package domain

import (
	"time"

	"github.com/shopspring/decimal"
)

// Transfer order problem codes (message catalog 3.16.18, INV-TRF-*). 004 to 006
// are service codes outside the catalog.
const (
	CodeTransferTransit     = "INV-TRF-001" // transit warehouse missing while transit is used
	CodeTransferOverReceipt = "INV-TRF-002" // received quantity above shipped plus tolerance
	CodeTransferCancel      = "INV-TRF-003" // quantity still in transit blocks cancellation
	CodeTransferNotFound    = "INV-TRF-004" // unknown transfer order or line
	CodeTransferShortClose  = "INV-TRF-005" // close-short beyond the underdelivery tolerance
	CodeTransferState       = "INV-TRF-006" // the order or line is not in a state for the action
)

// Transfer order statuses (BR-16.13). DRAFT is not used yet: orders are created
// CREATED.
const (
	TransferCreated   = "CREATED"
	TransferShipped   = "SHIPPED"
	TransferPartial   = "PARTIALLY_RECEIVED"
	TransferReceived  = "RECEIVED"
	TransferCancelled = "CANCELLED"
)

var hundred = decimal.NewFromInt(100)

// TransferLine is one product moved between two warehouses. Dims are the source
// dimensions; their warehouse is always the shipping warehouse.
type TransferLine struct {
	ID          string
	LineNo      int
	ProductID   string
	Dims        Dimensions
	Unit        string // inventory unit; quantities are kept in it
	Qty         decimal.Decimal
	ShippedQty  decimal.Decimal
	ReceivedQty decimal.Decimal
	UnderPct    decimal.Decimal // allowed shortfall at close-short, percent of shipped
	OverPct     decimal.Decimal // allowed excess over shipped, percent
	Closed      bool
}

// InTransit is the quantity shipped but not yet received on the line. A line
// closed short keeps its shortfall in transit, for investigation (AC-16.11).
func (l TransferLine) InTransit() decimal.Decimal {
	if q := l.ShippedQty.Sub(l.ReceivedQty); q.IsPositive() {
		return q
	}
	return decimal.Zero
}

// TransferOrder is an inter-warehouse stock transfer (FR-16.15). ToSite is the
// receiving site when it differs from the lines' (shipping) site; the transit
// warehouse stays at the shipping site.
type TransferOrder struct {
	ID               string
	LegalEntityID    string
	Number           string
	Status           string
	FromWarehouse    string
	ToWarehouse      string
	ToSite           string
	TransitWarehouse string
	UseTransit       bool
	ShipDate         time.Time // planned until shipped, then the actual ship date
	ReceiptDate      time.Time // zero until the order is fully received
	Version          int
	CreatedAt        time.Time
	Lines            []TransferLine
}

// WithWarehouse returns a copy of d with the warehouse dimension replaced.
func WithWarehouse(d Dimensions, warehouse string) Dimensions {
	out := make(Dimensions, len(d)+1)
	for k, v := range d {
		out[k] = v
	}
	out[DimWarehouse] = warehouse
	return out
}

// SourceDims are the line's dimensions at the shipping warehouse.
func (o TransferOrder) SourceDims(l TransferLine) Dimensions {
	return WithWarehouse(l.Dims, o.FromWarehouse)
}

// TransitDims are the line's dimensions at the transit warehouse.
func (o TransferOrder) TransitDims(l TransferLine) Dimensions {
	return WithWarehouse(l.Dims, o.TransitWarehouse)
}

// DestinationDims are the line's dimensions at the receiving warehouse (and
// receiving site, when set).
func (o TransferOrder) DestinationDims(l TransferLine) Dimensions {
	out := WithWarehouse(l.Dims, o.ToWarehouse)
	if o.ToSite != "" {
		out[DimSite] = o.ToSite
	}
	return out
}

// ValidateNewTransfer checks an order before it is stored. Lines follow the
// journal line rules (site mandatory, no reserved dimension, at most maxScale
// decimals, serial quantity 1) at the shipping warehouse.
func ValidateNewTransfer(o TransferOrder, maxScale int32) error {
	if o.FromWarehouse == "" || o.ToWarehouse == "" {
		return fail(CodeInvalid, "from and to warehouse are required")
	}
	if o.FromWarehouse == o.ToWarehouse {
		return fail(CodeInvalid, "from and to warehouse must differ")
	}
	if o.UseTransit {
		if o.TransitWarehouse == "" {
			return fail(CodeTransferTransit, "shipping warehouse %s has no transit warehouse configured", o.FromWarehouse)
		}
		if o.TransitWarehouse == o.FromWarehouse || o.TransitWarehouse == o.ToWarehouse {
			return fail(CodeInvalid, "transit warehouse must differ from the shipping and receiving warehouses")
		}
	}
	if len(o.Lines) == 0 {
		return fail(CodeInvalid, "transfer order has no lines")
	}
	for _, l := range o.Lines {
		if l.Unit == "" {
			return fail(CodeInvalid, "line %d has no unit", l.LineNo)
		}
		if !l.Qty.IsPositive() {
			return fail(CodeInvalid, "line %d quantity must be positive", l.LineNo)
		}
		if w := l.Dims[DimWarehouse]; w != "" && w != o.FromWarehouse {
			return fail(CodeInvalid, "line %d dimensions name warehouse %s, not the shipping warehouse", l.LineNo, w)
		}
		jl := JournalLine{LineNo: l.LineNo, ProductID: l.ProductID, Dims: o.SourceDims(l), Qty: l.Qty, EnteredQty: l.Qty}
		if err := ValidateLine(jl, maxScale); err != nil {
			return err
		}
		for _, p := range []decimal.Decimal{l.UnderPct, l.OverPct} {
			if p.IsNegative() || p.GreaterThan(hundred) {
				return fail(CodeInvalid, "line %d tolerance must be between 0 and 100 percent", l.LineNo)
			}
		}
	}
	return nil
}

// CanShip reports whether the order may be shipped: only CREATED orders ship,
// and they ship their full quantity.
func CanShip(o TransferOrder) error {
	if o.Status != TransferCreated {
		return fail(CodeTransferState, "transfer order in status %s cannot be shipped", o.Status)
	}
	return nil
}

// ApplyShip sets every line shipped in full. Without transit (one-step transfer,
// SAP 301) the receipt happens at shipment, so lines are received and closed.
func ApplyShip(o TransferOrder) TransferOrder {
	out := o
	out.Lines = make([]TransferLine, len(o.Lines))
	for i, l := range o.Lines {
		l.ShippedQty = l.Qty
		if !o.UseTransit {
			l.ReceivedQty = l.Qty
			l.Closed = true
		}
		out.Lines[i] = l
	}
	out.Status = SettleTransferStatus(out)
	return out
}

// Receipt is a receipt booked on a transfer line: the line after it, and where
// the received quantity comes from. FromTransit is taken from the quantity in
// transit; FromSource is an over-delivery beyond what is in transit, which left
// the shipping warehouse unrecorded and is issued from there now (the line's
// shipped quantity grows by it).
type Receipt struct {
	Line        TransferLine
	FromTransit decimal.Decimal
	FromSource  decimal.Decimal
}

// ApplyReceive books a receipt of qty on a line that has shipped quantity.
// Over-receipt beyond the overdelivery tolerance is INV-TRF-002. The line
// closes when the shipped quantity is reached, or short when closeShort is set
// and the shortfall is within the underdelivery tolerance (INV-TRF-005
// otherwise); the shortfall then stays in transit.
func ApplyReceive(l TransferLine, qty decimal.Decimal, closeShort bool) (Receipt, error) {
	if l.Closed {
		return Receipt{Line: l}, fail(CodeTransferState, "line %d is closed", l.LineNo)
	}
	if !qty.IsPositive() {
		return Receipt{Line: l}, fail(CodeInvalid, "received quantity must be positive")
	}
	limit := l.ShippedQty.Mul(hundred.Add(l.OverPct)).Div(hundred)
	next := l.ReceivedQty.Add(qty)
	if next.GreaterThan(limit) {
		return Receipt{Line: l}, fail(CodeTransferOverReceipt, "received quantity %s exceeds shipped quantity %s plus tolerance %s %%",
			next, l.ShippedQty, l.OverPct)
	}
	fromTransit := decimal.Min(qty, l.InTransit())
	out := Receipt{FromTransit: fromTransit, FromSource: qty.Sub(fromTransit)}
	line := l
	line.ReceivedQty = next
	switch {
	case !next.LessThan(l.ShippedQty):
		line.ShippedQty = l.ShippedQty.Add(out.FromSource)
		line.Closed = true
	case closeShort:
		short := l.ShippedQty.Sub(next)
		allowed := l.ShippedQty.Mul(l.UnderPct).Div(hundred)
		if short.GreaterThan(allowed) {
			return Receipt{Line: l}, fail(CodeTransferShortClose, "shortfall %s exceeds underdelivery tolerance %s %%", short, l.UnderPct)
		}
		line.Closed = true
	}
	out.Line = line
	return out, nil
}

// CanReceive reports whether receipts may be booked against the order.
func CanReceive(o TransferOrder) error {
	if o.Status != TransferShipped && o.Status != TransferPartial {
		return fail(CodeTransferState, "transfer order in status %s cannot be received", o.Status)
	}
	return nil
}

// CanCancel allows cancellation only before anything has shipped (BR-16.13):
// shipped quantity in transit must be received or returned by a reverse
// transfer (INV-TRF-003).
func CanCancel(o TransferOrder) error {
	switch o.Status {
	case TransferCreated:
		return nil
	case TransferShipped, TransferPartial:
		inTransit := decimal.Zero
		for _, l := range o.Lines {
			inTransit = inTransit.Add(l.InTransit())
		}
		return fail(CodeTransferCancel, "transfer order %s cannot be cancelled: %s in transit", o.Number, inTransit)
	default:
		return fail(CodeTransferState, "transfer order in status %s cannot be cancelled", o.Status)
	}
}

// SettleTransferStatus derives the order status from its lines: RECEIVED when
// every line is closed, PARTIALLY_RECEIVED when anything was received, SHIPPED
// when anything shipped, CREATED otherwise.
func SettleTransferStatus(o TransferOrder) string {
	if len(o.Lines) == 0 {
		return o.Status
	}
	allClosed, anyReceived, anyShipped := true, false, false
	for _, l := range o.Lines {
		allClosed = allClosed && l.Closed
		anyReceived = anyReceived || l.ReceivedQty.IsPositive()
		anyShipped = anyShipped || l.ShippedQty.IsPositive()
	}
	switch {
	case allClosed:
		return TransferReceived
	case anyReceived:
		return TransferPartial
	case anyShipped:
		return TransferShipped
	default:
		return TransferCreated
	}
}

// InTransitDays is the whole number of days a shipment has been in transit.
func InTransitDays(shipDate, now time.Time) int {
	if now.Before(shipDate) {
		return 0
	}
	return int(now.Sub(shipDate).Hours() / 24)
}

// Lateness of a transfer order line (transfer order overview, PRD 3.16.14).
const (
	LateShipment = "LATE_SHIPMENT" // not shipped and the planned ship date has passed
	LateReceipt  = "LATE_RECEIPT"  // shipped, still in transit beyond the receipt allowance
)

// DefaultLateReceiptDays is how many days a shipment may stay in transit before
// the overview calls it late.
const DefaultLateReceiptDays = 7

// TransferLineLateness classifies a line of an order in orderStatus as of asOf
// and returns how many days it is overdue. A line is late once its due date has
// passed, mirroring "days in transit > N" of the stock in transit inquiry:
//   - an order still CREATED is due on its planned ship date, so it is a late
//     shipment from the day after;
//   - an open shipment (not closed short, quantity still in transit) is due
//     lateReceiptDays after its ship date, so it is a late receipt once it has
//     been in transit longer than that.
//
// Received and cancelled orders, and lines closed short (in transit for
// investigation, AC-16.11), are never late. The empty string means on time,
// with zero days.
func TransferLineLateness(orderStatus string, l TransferLine, shipDate, asOf time.Time, lateReceiptDays int) (string, int) {
	switch orderStatus {
	case TransferCreated:
		if days := InTransitDays(shipDate, asOf); days > 0 {
			return LateShipment, days
		}
	case TransferShipped, TransferPartial:
		if days := InTransitDays(shipDate, asOf) - lateReceiptDays; days > 0 && !l.Closed && l.InTransit().IsPositive() {
			return LateReceipt, days
		}
	}
	return "", 0
}

// TransferLineRemaining is the quantity of the line still to be received: the
// ordered quantity less what was received, zero once the line is closed short,
// over-received or its order is cancelled.
func TransferLineRemaining(orderStatus string, l TransferLine) decimal.Decimal {
	if l.Closed || orderStatus == TransferCancelled {
		return decimal.Zero
	}
	if q := l.Qty.Sub(l.ReceivedQty); q.IsPositive() {
		return q
	}
	return decimal.Zero
}
