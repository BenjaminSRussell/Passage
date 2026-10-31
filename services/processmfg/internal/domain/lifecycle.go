package domain

import (
	"sort"

	"github.com/shopspring/decimal"
)

// Batch-order statuses (PRD 4.24.4). "closed" belongs to the wave-21 order
// engine's period close and is not entered by this service yet.
const (
	StatusPlanned  = "planned"
	StatusReleased = "released"
	StatusStarted  = "started"
	StatusReported = "reported"
	StatusEnded    = "ended"
)

// Lifecycle commands, in the order they run.
const (
	CmdRelease = "release"
	CmdStart   = "start"
	CmdReport  = "report-finished"
	CmdEnd     = "end"
)

// transitions maps a command to the status it needs and the status it leads to.
func transitions() map[string][2]string {
	return map[string][2]string{
		CmdRelease: {StatusPlanned, StatusReleased},
		CmdStart:   {StatusReleased, StatusStarted},
		CmdReport:  {StatusStarted, StatusReported},
		CmdEnd:     {StatusReported, StatusEnded},
	}
}

// NextStatus returns the status a command leads to from status from, and the
// status the command requires. ok is false when from is not the required one
// (or the command is unknown).
func NextStatus(cmd, from string) (to, requires string, ok bool) {
	t, found := transitions()[cmd]
	if !found {
		return "", "", false
	}
	return t[1], t[0], from == t[0]
}

// OutputRef is a co- or by-product line of the order with the quantity
// reported as finished.
type OutputRef struct {
	LineNumber int
	ItemID     string
	RafQty     decimal.Decimal
}

// CostBasis is what a method weighs a product by: the standard cost (TCA) or
// the percentage (manual).
type CostBasis struct {
	StandardCost decimal.Decimal
	Percent      decimal.Decimal
}

// ByRate prices a by-product line (the cost types of BR-24.3).
type ByRate struct {
	CostType string
	Rate     decimal.Decimal
}

// OrderAllocationInput is an ended order's cost and outputs.
type OrderAllocationInput struct {
	Method     AllocationMethod
	BatchCost  decimal.Decimal
	MainItemID string
	MainRafQty decimal.Decimal
	MainBasis  CostBasis
	CoProducts []OutputRef
	CoBasis    map[int]CostBasis // by line number
	ByProducts []OutputRef
	ByRates    map[int]ByRate // by line number
}

// Recipient is one share of the batch cost. Role is main, co-product or
// by-product-burden; the main item is line 0.
type Recipient struct {
	Role       string
	LineNumber int
	ItemID     string
	RafQty     decimal.Decimal
	Amount     decimal.Decimal
	Percent    decimal.Decimal
}

// AllocateOrder runs BR-24.3 on the lines of an order and returns the shares,
// main first, then co-products and burdens by line number. The shares add up
// to the batch cost exactly; the function refuses to answer otherwise.
func AllocateOrder(in OrderAllocationInput) ([]Recipient, error) {
	if !in.MainRafQty.IsPositive() {
		return nil, errorf(CodeInvalid, "the order has no reported quantity of its own item")
	}
	main := Product{ItemID: in.MainItemID, StandardCost: in.MainBasis.StandardCost, RafQty: in.MainRafQty, Percent: in.MainBasis.Percent}
	co := make([]Product, len(in.CoProducts))
	coLines := map[int]bool{}
	for i, o := range in.CoProducts {
		b := in.CoBasis[o.LineNumber]
		co[i] = Product{ItemID: o.ItemID, StandardCost: b.StandardCost, RafQty: o.RafQty, Percent: b.Percent}
		coLines[o.LineNumber] = true
	}
	for n := range in.CoBasis {
		if !coLines[n] {
			return nil, errorf(CodeInvalid, "line %d is not a co-product line of the order", n)
		}
	}
	byLines := map[int]OutputRef{}
	for _, o := range in.ByProducts {
		byLines[o.LineNumber] = o
	}
	rated := make([]int, 0, len(in.ByRates))
	for n := range in.ByRates {
		if _, ok := byLines[n]; !ok {
			return nil, errorf(CodeInvalid, "line %d is not a by-product line of the order", n)
		}
		rated = append(rated, n)
	}
	sort.Ints(rated)
	by := make([]ByProduct, len(rated))
	for i, n := range rated {
		r := in.ByRates[n]
		by[i] = ByProduct{ItemID: byLines[n].ItemID, CostType: r.CostType, Qty: byLines[n].RafQty, Rate: r.Rate}
	}
	res, err := AllocateCost(in.BatchCost, in.Method, main, co, by)
	if err != nil {
		return nil, err
	}
	out := []Recipient{{Role: "main", LineNumber: 0, ItemID: in.MainItemID, RafQty: in.MainRafQty, Amount: res.Main.Amount, Percent: res.Main.Percent}}
	sum := res.Main.Amount
	for i, c := range res.CoProducts {
		o := in.CoProducts[i]
		out = append(out, Recipient{Role: "co-product", LineNumber: o.LineNumber, ItemID: o.ItemID, RafQty: o.RafQty, Amount: c.Amount, Percent: c.Percent})
		sum = sum.Add(c.Amount)
	}
	for i, b := range res.Burdens {
		o := byLines[rated[i]]
		out = append(out, Recipient{Role: "by-product-burden", LineNumber: o.LineNumber, ItemID: o.ItemID, RafQty: o.RafQty, Amount: b.Amount})
		sum = sum.Add(b.Amount)
	}
	if !sum.Equal(in.BatchCost.Round(AmountPrecision)) {
		return nil, errorf(CodeAllocation, "allocated %s differs from batch cost %s", sum.StringFixed(AmountPrecision), in.BatchCost.StringFixed(AmountPrecision))
	}
	sort.SliceStable(out[1:], func(i, j int) bool {
		a, b := out[1+i], out[1+j]
		if a.Role != b.Role {
			return a.Role == "co-product"
		}
		return a.LineNumber < b.LineNumber
	})
	return out, nil
}
