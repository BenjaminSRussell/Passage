package domain

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/shopspring/decimal"
)

// CodeVariance is the warning of a received quantity outside the ASN tolerance
// (BR-31.10, WMS-0107). The receipt still posts the actual quantity.
const CodeVariance = "WMS-0107"

// CodeLineReceived is a second receipt of an ASN pallet with another
// Idempotency-Key: the pallet is already received.
const CodeLineReceived = "WMS-RECEIPT-LINE-RECEIVED"

// ErrPalletNotFound is returned when a receipt has no pallet with the
// requested line number.
var ErrPalletNotFound = errors.New("receipt line not found")

// ErrReceivedQty is a received quantity that is not greater than zero; a
// pallet that did not arrive stays EXPECTED.
var ErrReceivedQty = errors.New("the received quantity must be greater than zero")

// MaxASNTolerancePct is the largest allowed ASN variance tolerance (§6.31.13
// asn_variance_tolerance_pct, 0–20).
const MaxASNTolerancePct = 20

// DefaultASNTolerance is the ASN variance tolerance when the request gives
// none: ±2% (BR-31.10).
func DefaultASNTolerance() decimal.Decimal { return decimal.NewFromInt(2) }

// ReceiptStatus is the state of an ASN receipt, derived from its lines.
type ReceiptStatus string

const (
	ReceiptOpen      ReceiptStatus = "OPEN"
	ReceiptPartial   ReceiptStatus = "PARTIALLY_RECEIVED"
	ReceiptCompleted ReceiptStatus = "RECEIVED"
)

// ReceiptLineStatus is the state of one ASN pallet.
type ReceiptLineStatus string

const (
	PalletExpected ReceiptLineStatus = "EXPECTED"
	PalletReceived ReceiptLineStatus = "RECEIVED"
)

// ReceiptLine is one pallet (license plate) of an ASN packing structure and,
// once received, what arrived. Weight and volume are for the expected
// quantity; the put-away line scales them to the received quantity.
type ReceiptLine struct {
	LineNo      int               `json:"lineNo"`
	ItemID      string            `json:"itemId"`
	LPID        string            `json:"lpId"`
	POLineRef   string            `json:"poLineRef,omitempty"`
	ExpectedQty decimal.Decimal   `json:"expectedQty"`
	Weight      decimal.Decimal   `json:"weight"`
	Volume      decimal.Decimal   `json:"volume"`
	Status      ReceiptLineStatus `json:"status"`
	ReceivedQty decimal.Decimal   `json:"receivedQty"`
	VarianceQty decimal.Decimal   `json:"varianceQty"`
	VariancePct decimal.Decimal   `json:"variancePct"`
	Discrepancy bool              `json:"discrepancy"`
	Reason      string            `json:"reason,omitempty"`
	WorkID      string            `json:"workId,omitempty"`
	ReceivedBy  string            `json:"receivedBy,omitempty"`
	ReceivedAt  *time.Time        `json:"receivedAt,omitempty"`
}

// Receipt is an ASN registered in a warehouse with its pallets (FR-31.9). One
// receipt exists per ASN reference and warehouse. Version counts every stored
// change and backs If-Match on receiving.
type Receipt struct {
	ID           string          `json:"id"`
	WarehouseID  string          `json:"warehouseId"`
	ASNRef       string          `json:"asnRef"`
	TolerancePct decimal.Decimal `json:"tolerancePct"`
	Status       ReceiptStatus   `json:"status"`
	Version      int             `json:"version"`
	CreatedAt    time.Time       `json:"createdAt"`
	UpdatedAt    time.Time       `json:"updatedAt"`
	Lines        []ReceiptLine   `json:"lines"`
}

// ReceiptSummary is a receipt header in a list, with its pallet counts.
type ReceiptSummary struct {
	ID            string          `json:"id"`
	WarehouseID   string          `json:"warehouseId"`
	ASNRef        string          `json:"asnRef"`
	TolerancePct  decimal.Decimal `json:"tolerancePct"`
	Status        ReceiptStatus   `json:"status"`
	Lines         int             `json:"lines"`
	Received      int             `json:"received"`
	Discrepancies int             `json:"discrepancies"`
	CreatedAt     time.Time       `json:"createdAt"`
	UpdatedAt     time.Time       `json:"updatedAt"`
}

// Variance is the received quantity minus the ASN quantity and that difference
// as a percentage of the ASN quantity, rounded to 4 decimals for display and
// storage. expected must be positive.
func Variance(expected, received decimal.Decimal) (qty, pct decimal.Decimal) {
	qty = received.Sub(expected)
	pct = qty.Mul(decimal.NewFromInt(100)).DivRound(expected, 4)
	return qty, pct
}

// Line returns pallet n of the receipt.
func (r *Receipt) Line(n int) (ReceiptLine, error) {
	for _, l := range r.Lines {
		if l.LineNo == n {
			return l, nil
		}
	}
	return ReceiptLine{}, ErrPalletNotFound
}

// Receive records qty arriving on pallet n (FR-31.9, BR-31.10). A variance
// whose absolute percentage exceeds the receipt's tolerance is a discrepancy
// with the WMS-0107 text as its reason; the actual quantity is received either
// way. A pallet is received once; a second receipt is CodeLineReceived. The
// receipt's status follows its lines.
func (r *Receipt) Receive(n int, qty decimal.Decimal, user string, at time.Time) (ReceiptLine, error) {
	if !qty.IsPositive() {
		return ReceiptLine{}, ErrReceivedQty
	}
	for i := range r.Lines {
		l := &r.Lines[i]
		if l.LineNo != n {
			continue
		}
		if l.Status == PalletReceived {
			return *l, errorf(CodeLineReceived, "pallet %d (%s) of ASN %s is already received", n, l.LPID, r.ASNRef)
		}
		l.Status, l.ReceivedQty, l.ReceivedBy = PalletReceived, qty, user
		when := at.UTC()
		l.ReceivedAt = &when
		l.VarianceQty, l.VariancePct = Variance(l.ExpectedQty, qty)
		// Compared exactly, before rounding: |received − expected| × 100 > tolerance × expected.
		l.Discrepancy = l.VarianceQty.Abs().Mul(decimal.NewFromInt(100)).GreaterThan(r.TolerancePct.Mul(l.ExpectedQty))
		l.Reason = ""
		if l.Discrepancy {
			l.Reason = fmt.Sprintf("%s: Received %s differs from ASN %s by %s%%; discrepancy recorded.",
				CodeVariance, qty, l.ExpectedQty, l.VariancePct.StringFixed(2))
		}
		r.settle()
		return *l, nil
	}
	return ReceiptLine{}, ErrPalletNotFound
}

// settle derives the receipt status from its pallets.
func (r *Receipt) settle() {
	received := 0
	for _, l := range r.Lines {
		if l.Status == PalletReceived {
			received++
		}
	}
	switch {
	case received == 0:
		r.Status = ReceiptOpen
	case received == len(r.Lines):
		r.Status = ReceiptCompleted
	default:
		r.Status = ReceiptPartial
	}
}

// Discrepancies counts the received pallets outside tolerance.
func (r *Receipt) Discrepancies() int {
	n := 0
	for _, l := range r.Lines {
		if l.Discrepancy {
			n++
		}
	}
	return n
}

// PutAwayLine is the PURCHASE_PUTAWAY line for a received pallet: the received
// quantity as one LP, with weight and volume scaled from the ASN quantity to
// the received one (6 decimals). The LP id, receipt and ASN line travel as
// line attributes so the pallet's identity survives put-away.
func (l ReceiptLine) PutAwayLine(receiptID string) NewLine {
	scale := func(d decimal.Decimal) decimal.Decimal {
		return d.Mul(l.ReceivedQty).DivRound(l.ExpectedQty, 6)
	}
	return NewLine{
		ItemID:    l.ItemID,
		Direction: DirectionPut,
		Qty:       l.ReceivedQty,
		Weight:    scale(l.Weight),
		Volume:    scale(l.Volume),
		LPs:       1,
		Attributes: map[string]string{
			"lpId":      l.LPID,
			"receiptId": receiptID,
			"asnLine":   strconv.Itoa(l.LineNo),
		},
	}
}
