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
	CodeTransferShipQty     = "INV-TRF-007" // ship quantity above the open quantity, or nothing left to ship
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
	PlannedReceipt   time.Time // zero when no receipt date was planned
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
	if !o.PlannedReceipt.IsZero() && o.PlannedReceipt.Before(o.ShipDate) {
		return fail(CodeInvalid, "planned receipt date must not be before the ship date")
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

// CanShip reports whether the order may be shipped: a CREATED order ships, and
// a shipped or partially received order ships again while a line still has
// quantity left to ship (partial shipments, BR-16.13).
func CanShip(o TransferOrder) error {
	switch o.Status {
	case TransferCreated, TransferShipped, TransferPartial:
		return nil
	}
	return fail(CodeTransferState, "transfer order in status %s cannot be shipped", o.Status)
}

// ShipQty is a quantity to ship on one line.
type ShipQty struct {
	LineID string
	Qty    decimal.Decimal
}

// ValidateShipScale refuses a ship quantity with more decimals than maxScale
// (INV-UOM precision), as on the order lines, so a shipment never moves a
// quantity the line columns would round.
func ValidateShipScale(req []ShipQty, maxScale int32) error {
	for _, r := range req {
		if !r.Qty.Equal(r.Qty.Truncate(maxScale)) {
			return fail(CodeUOMPrecision, "ship quantity %s has more than %d decimals", r.Qty, maxScale)
		}
	}
	return nil
}

// UnshippedQty is the ordered quantity of the line not shipped yet; zero for a
// closed line.
func (l TransferLine) UnshippedQty() decimal.Decimal {
	if l.Closed {
		return decimal.Zero
	}
	if q := l.Qty.Sub(l.ShippedQty); q.IsPositive() {
		return q
	}
	return decimal.Zero
}

// ApplyShip ships quantities on the order's lines and returns the order and the
// quantity moved per line id. Without ship quantities every open line ships
// what is left of it; with them each named line ships that quantity, at most
// what is left (INV-TRF-007) and each line at most once. Without transit
// (one-step transfer, SAP 301) the receipt happens at shipment, so the shipped
// quantity is received and a fully shipped line closes.
func ApplyShip(o TransferOrder, req []ShipQty) (TransferOrder, map[string]decimal.Decimal, error) {
	want := map[string]decimal.Decimal{}
	if len(req) == 0 {
		for _, l := range o.Lines {
			if q := l.UnshippedQty(); q.IsPositive() {
				want[l.ID] = q
			}
		}
	}
	for _, r := range req {
		if _, dup := want[r.LineID]; dup {
			return o, nil, fail(CodeInvalid, "line %s is listed twice", r.LineID)
		}
		if !r.Qty.IsPositive() {
			return o, nil, fail(CodeInvalid, "ship quantity must be positive")
		}
		want[r.LineID] = r.Qty
	}
	out := o
	out.Lines = make([]TransferLine, len(o.Lines))
	moved := map[string]decimal.Decimal{}
	for i, l := range o.Lines {
		if q, ok := want[l.ID]; ok {
			if left := l.UnshippedQty(); q.GreaterThan(left) {
				return o, nil, fail(CodeTransferShipQty, "line %d ships %s but only %s is left to ship", l.LineNo, q, left)
			}
			l.ShippedQty = l.ShippedQty.Add(q)
			if !o.UseTransit {
				l.ReceivedQty = l.ReceivedQty.Add(q)
				l.Closed = !l.ShippedQty.LessThan(l.Qty)
			}
			moved[l.ID] = q
			delete(want, l.ID)
		}
		out.Lines[i] = l
	}
	for id := range want {
		return o, nil, fail(CodeTransferNotFound, "transfer line %s not found on this order", id)
	}
	if len(moved) == 0 {
		return o, nil, fail(CodeTransferShipQty, "nothing left to ship on transfer order %s", o.Number)
	}
	out.Status = SettleTransferStatus(out)
	return out, moved, nil
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
// closes when its whole ordered quantity has shipped and been received, or
// short when closeShort is set and the shortfall against the ordered (or
// shipped, when larger) quantity is within the underdelivery tolerance
// (INV-TRF-005 otherwise); the shortfall in transit then stays in transit and
// an unshipped remainder is not shipped.
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
	line.ShippedQty = l.ShippedQty.Add(out.FromSource)
	fullyShipped := !line.ShippedQty.LessThan(l.Qty)
	switch {
	case !next.LessThan(line.ShippedQty) && (fullyShipped || !closeShort):
		// A line shipped in part stays open for the rest of its quantity.
		line.Closed = fullyShipped
	case closeShort:
		// The shortfall is measured against the larger of the ordered and the
		// shipped quantity, so closing a partly shipped line short also gives
		// up its unshipped remainder only within the underdelivery tolerance.
		base := decimal.Max(l.Qty, line.ShippedQty)
		short := base.Sub(next)
		allowed := base.Mul(l.UnderPct).Div(hundred)
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
//   - an open shipment (not closed short, quantity still in transit) is due on
//     the planned receipt date when the order has one, else lateReceiptDays
//     after its ship date, so it is a late receipt once it has
//     been in transit longer than that.
//
// Received and cancelled orders, and lines closed short (in transit for
// investigation, AC-16.11), are never late. The empty string means on time,
// with zero days.
func TransferLineLateness(orderStatus string, l TransferLine, shipDate, plannedReceipt, asOf time.Time, lateReceiptDays int) (string, int) {
	switch orderStatus {
	case TransferCreated:
		if days := InTransitDays(shipDate, asOf); days > 0 {
			return LateShipment, days
		}
	case TransferShipped, TransferPartial:
		days := InTransitDays(shipDate, asOf) - lateReceiptDays
		if !plannedReceipt.IsZero() {
			days = InTransitDays(plannedReceipt, asOf) // a planned receipt date replaces the allowance
		}
		if days > 0 && !l.Closed && l.InTransit().IsPositive() {
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
