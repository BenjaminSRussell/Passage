package domain

import (
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/shopspring/decimal"
)

// Ownership and consignment codes. INV-OWN-001 and INV-OWN-002 are in the
// 3.16.18 catalog; the INV-CNS codes are service codes for the replenishment
// order, which the catalog does not list.
const (
	CodeOwnershipValue      = "INV-OWN-001" // vendor-owned stock cannot be valued or financially posted
	CodeOwnershipExceeds    = "INV-OWN-002" // ownership change exceeds the vendor-owned quantity
	CodeConsignmentNotFound = "INV-CNS-001" // unknown replenishment order or line
	CodeConsignmentOver     = "INV-CNS-002" // receipt beyond the ordered quantity
	CodeConsignmentState    = "INV-CNS-003" // the order is fully received
)

// Replenishment order statuses (FR-16.17).
const (
	ConsignmentCreated  = "CREATED"
	ConsignmentPartial  = "PARTIALLY_RECEIVED"
	ConsignmentReceived = "RECEIVED"
)

// ConsignmentStatuses lists every replenishment order status.
var ConsignmentStatuses = []string{ConsignmentCreated, ConsignmentPartial, ConsignmentReceived}

// consignmentLineDimensions are the dimensions a replenishment line may name.
// Site, warehouse and owner come from the order; inventory status, segment and
// license plate are not set on vendor-owned receipts.
var consignmentLineDimensions = []string{DimLocation, DimBatch, DimSerial, DimConfig, DimSize, DimColor, DimStyle}

// VendorOwned reports whether d names a vendor as owner (BR-16.14). An absent
// owner is the legal entity itself, so company stock keeps its dimension-set
// hash.
func VendorOwned(d Dimensions) bool { return strings.TrimSpace(d[DimOwner]) != "" }

// WithOwner returns a copy of d owned by owner; an empty owner removes the key,
// which is how an ownership change moves stock to the legal entity.
func WithOwner(d Dimensions, owner string) Dimensions {
	out := make(Dimensions, len(d)+1)
	for k, v := range d {
		out[k] = v
	}
	if owner = strings.TrimSpace(owner); owner == "" {
		delete(out, DimOwner)
	} else {
		out[DimOwner] = owner
	}
	return out
}

// ConsignmentLine is one product line of a replenishment order. Price is the
// consignment price in the order currency: it is carried to the ownership
// change until wave 17 trade agreements supply it.
type ConsignmentLine struct {
	ID          string
	LineNo      int
	ProductID   string
	Dims        Dimensions
	Unit        string
	Qty         decimal.Decimal
	ReceivedQty decimal.Decimal
	Price       decimal.Decimal
}

// ConsignmentOrder is a vendor replenishment order (D365
// ConsignmentReplenishmentOrder): vendor-owned stock delivered to a warehouse
// as quantity only, with no GR/IR and no ledger entry.
type ConsignmentOrder struct {
	ID            string
	LegalEntityID string
	Number        string
	VendorID      string
	Site          string
	Warehouse     string
	RequestedDate time.Time
	Currency      string
	Status        string
	Version       int
	CreatedAt     time.Time
	Lines         []ConsignmentLine
}

// LineDims is the dimension set a replenishment line is received into: the
// line's dimensions at the order's site and warehouse, owned by the vendor.
func (o ConsignmentOrder) LineDims(l ConsignmentLine) Dimensions {
	d := WithOwner(l.Dims, o.VendorID)
	d[DimSite], d[DimWarehouse] = o.Site, o.Warehouse
	return d
}

// ValidateNewConsignment checks a replenishment order before it is stored.
func ValidateNewConsignment(o ConsignmentOrder, maxScale int32) error {
	for name, v := range map[string]string{"vendor": o.VendorID, "site": o.Site, "warehouse": o.Warehouse} {
		if v = strings.TrimSpace(v); v == "" || utf8.RuneCountInString(v) > maxDimensionValue {
			return fail(CodeInvalid, "%s is required (at most %d characters)", name, maxDimensionValue)
		}
	}
	if len(o.Currency) != 3 {
		return fail(CodeInvalid, "currency %q is not an ISO 4217 code", o.Currency)
	}
	if len(o.Lines) == 0 {
		return fail(CodeInvalid, "a replenishment order needs at least one line")
	}
	for _, l := range o.Lines {
		if l.ProductID == "" {
			return fail(CodeInvalid, "line %d has no product", l.LineNo)
		}
		if !l.Qty.IsPositive() || !l.Qty.Equal(l.Qty.Truncate(maxScale)) {
			return fail(CodeUOMPrecision, "line %d quantity %s must be positive with at most %d decimals", l.LineNo, l.Qty, maxScale)
		}
		if l.Price.Sign() < 0 || !l.Price.Equal(l.Price.Truncate(maxCostScale)) {
			return fail(CodeInvalid, "line %d consignment price must be non-negative with at most %d decimals", l.LineNo, maxCostScale)
		}
		dims, err := l.Dims.Normalize()
		if err != nil {
			return fail(CodeInvalid, "line %d: %v", l.LineNo, err)
		}
		for name := range dims {
			if !slices.Contains(consignmentLineDimensions, name) {
				return fail(CodeInvalid, "line %d: dimension %s comes from the order or cannot be set on vendor-owned stock", l.LineNo, name)
			}
		}
		if dims[DimSerial] != "" && !l.Qty.Equal(decimal.NewFromInt(1)) {
			return fail(CodeSerialQty, "serial-tracked product %s requires quantity 1 per line (line %d)", l.ProductID, l.LineNo)
		}
	}
	return nil
}

// ConsignmentReceipt books a received quantity against one order line.
type ConsignmentReceipt struct {
	LineID string
	Qty    decimal.Decimal
}

// ApplyConsignmentReceipts books receipts against an order. Each receipt is one
// RECEIVED transaction of vendor-owned stock at no value (FR-16.17,
// BR-16.14). It returns the order with received quantities and status updated
// and the transactions to write; o is not modified. A line cannot receive more
// than it ordered (INV-CNS-002) and a fully received order takes no receipt
// (INV-CNS-003).
func ApplyConsignmentReceipts(o ConsignmentOrder, receipts []ConsignmentReceipt) (ConsignmentOrder, []PostedLine, error) {
	if o.Status == ConsignmentReceived {
		return o, nil, fail(CodeConsignmentState, "replenishment order %s is fully received", o.Number)
	}
	if len(receipts) == 0 {
		return o, nil, fail(CodeInvalid, "a receipt needs at least one line")
	}
	next := o
	next.Lines = slices.Clone(o.Lines)
	plan := make([]PostedLine, 0, len(receipts))
	for _, rc := range receipts {
		i := slices.IndexFunc(next.Lines, func(l ConsignmentLine) bool { return l.ID == rc.LineID })
		if i < 0 {
			return o, nil, fail(CodeConsignmentNotFound, "replenishment line %s is not on order %s", rc.LineID, o.Number)
		}
		if !rc.Qty.IsPositive() {
			return o, nil, fail(CodeInvalid, "receipt quantity must be positive")
		}
		l := next.Lines[i]
		if l.ReceivedQty.Add(rc.Qty).GreaterThan(l.Qty) {
			return o, nil, fail(CodeConsignmentOver, "receipt of %s on line %d exceeds the open quantity %s", rc.Qty, l.LineNo, l.Qty.Sub(l.ReceivedQty))
		}
		l.ReceivedQty = l.ReceivedQty.Add(rc.Qty)
		next.Lines[i] = l
		plan = append(plan, PostedLine{Status: StatusReceived, Value: decimal.Zero,
			Line: JournalLine{ID: l.ID, LineNo: l.LineNo, ProductID: l.ProductID, Dims: o.LineDims(l), Qty: rc.Qty, EnteredQty: rc.Qty, EnteredUnit: l.Unit}})
	}
	next.Status = ConsignmentPartial
	if !slices.ContainsFunc(next.Lines, func(l ConsignmentLine) bool { return l.ReceivedQty.LessThan(l.Qty) }) {
		next.Status = ConsignmentReceived
	}
	return next, plan, nil
}

// ownershipVendor returns the single vendor an OWNERSHIP_CHANGE journal moves
// stock from, or an error when its lines name several (the ownership.changed
// event and the wave 18 settlement are per vendor).
func ownershipVendor(lines []JournalLine) (string, error) {
	vendor := ""
	for _, l := range lines {
		v := strings.TrimSpace(l.Dims[DimOwner])
		if vendor != "" && v != vendor {
			return "", fail(CodeInvalid, "an ownership change moves stock of one vendor; line %d names %s, an earlier line %s", l.LineNo, v, vendor)
		}
		vendor = v
	}
	return vendor, nil
}

// OwnershipVendor is the vendor of a valid OWNERSHIP_CHANGE journal.
func OwnershipVendor(j Journal) string {
	v, _ := ownershipVendor(j.Lines)
	return v
}

// CheckOwnershipLines rejects an ownership change whose lines name more than one vendor.
func CheckOwnershipLines(lines []JournalLine) error {
	_, err := ownershipVendor(lines)
	return err
}
