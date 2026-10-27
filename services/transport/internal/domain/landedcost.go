package domain

import (
	"slices"

	"github.com/shopspring/decimal"
)

// Allocation methods for a landed-cost line (FR-34.9).
const (
	MethodWeight   = "WEIGHT"
	MethodVolume   = "VOLUME"
	MethodValue    = "VALUE"
	MethodQuantity = "QUANTITY"
	MethodEqual    = "EQUAL"
)

// AllocationMethods lists the methods Allocate accepts.
var AllocationMethods = []string{MethodWeight, MethodVolume, MethodValue, MethodQuantity, MethodEqual}

// codeAllocationInput marks an allocation request that cannot be computed. The
// message catalog has no dedicated code, so it is a validation failure.
const codeAllocationInput = "COM-0001"

// ReceiptLine is a purchase receipt line that absorbs landed cost. Value decides
// who takes the residual and is also the basis of the VALUE method.
type ReceiptLine struct {
	Line     int
	Value    decimal.Decimal
	Weight   decimal.Decimal
	Volume   decimal.Decimal
	Quantity decimal.Decimal
}

// Allocation is the cost assigned to one receipt line.
type Allocation struct {
	Line   int             `json:"line"`
	Amount decimal.Decimal `json:"amount"`
}

// Allocated is the outcome of one cost line. Shares sum to the cost exactly.
// ResidualLine is the line that took the rounding residual (TMS-0110).
type Allocated struct {
	Shares       []Allocation    `json:"shares"`
	Residual     decimal.Decimal `json:"residual"`
	ResidualLine int             `json:"residualLine"`
}

// AllocateByWeight is Allocate with the WEIGHT method.
func AllocateByWeight(cost decimal.Decimal, lines []ReceiptLine) (Allocated, *Error) {
	return Allocate(cost, MethodWeight, lines)
}

// Allocate splits cost across receipt lines in proportion to the method's basis
// (BR-34.6): each share is round(cost × basis_i ÷ Σ basis, 2). The rounding
// residual goes to the highest-value line; ties go to the lowest line number.
// The result sums to cost to the cent.
func Allocate(cost decimal.Decimal, method string, lines []ReceiptLine) (Allocated, *Error) {
	if !slices.Contains(AllocationMethods, method) {
		return Allocated{}, &Error{Code: codeAllocationInput, Detail: "allocation method must be WEIGHT, VOLUME, VALUE, QUANTITY or EQUAL"}
	}
	if len(lines) == 0 {
		return Allocated{}, &Error{Code: codeAllocationInput, Detail: "a landed cost line needs at least one receipt line"}
	}
	if cost.IsNegative() {
		return Allocated{}, &Error{Code: codeAllocationInput, Detail: "landed cost must not be negative"}
	}
	total := decimal.Zero
	for _, l := range lines {
		b := basis(method, l)
		if b.IsNegative() {
			return Allocated{}, &Error{Code: codeAllocationInput, Detail: "receipt line " + method + " basis must not be negative"}
		}
		total = total.Add(b)
	}
	if !total.IsPositive() {
		return Allocated{}, &Error{Code: codeAllocationInput, Detail: "total receipt " + method + " basis must be positive"}
	}
	out := Allocated{Shares: make([]Allocation, len(lines)), Residual: decimal.Zero}
	assigned := decimal.Zero
	target := 0
	for i, l := range lines {
		share := cost.Mul(basis(method, l)).Div(total).Round(2)
		out.Shares[i] = Allocation{Line: l.Line, Amount: share}
		assigned = assigned.Add(share)
		if l.Value.GreaterThan(lines[target].Value) || (l.Value.Equal(lines[target].Value) && l.Line < lines[target].Line) {
			target = i
		}
	}
	out.Residual = cost.Sub(assigned)
	out.Shares[target].Amount = out.Shares[target].Amount.Add(out.Residual)
	out.ResidualLine = lines[target].Line
	return out, nil
}

func basis(method string, l ReceiptLine) decimal.Decimal {
	switch method {
	case MethodVolume:
		return l.Volume
	case MethodValue:
		return l.Value
	case MethodQuantity:
		return l.Quantity
	case MethodEqual:
		return decimal.NewFromInt(1)
	default:
		return l.Weight
	}
}
