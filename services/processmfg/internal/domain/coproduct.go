package domain

import (
	"sort"

	"github.com/shopspring/decimal"
)

// AllocationMethod is the FR-24.4 co-product allocation method.
type AllocationMethod string

const (
	MethodNone     AllocationMethod = "none"
	MethodManual   AllocationMethod = "manual"
	MethodTCA      AllocationMethod = "tca"
	MethodQuantity AllocationMethod = "quantity"
)

// Product is the main product or a co-product of a batch order.
type Product struct {
	ItemID       string
	StandardCost decimal.Decimal
	RafQty       decimal.Decimal
	Percent      decimal.Decimal
}

// ByProduct is a by-product whose burden reduces the batch cost first.
// CostType is per-series, per-quantity, percent or recycled. For percent, Rate
// is the percent of batch cost; otherwise Rate is an amount per unit of Qty.
type ByProduct struct {
	ItemID   string
	CostType string
	Qty      decimal.Decimal
	Rate     decimal.Decimal
}

// Allocated is an amount assigned to one item.
type Allocated struct {
	ItemID  string
	Amount  decimal.Decimal
	Percent decimal.Decimal
}

// AllocationResult partitions the batch cost. Total equals the batch cost.
type AllocationResult struct {
	Main       Allocated
	CoProducts []Allocated
	Burdens    []Allocated
	Total      decimal.Decimal
}

// AllocateCost implements BR-24.3. By-product burdens are taken off the batch
// cost first; the remainder is split by the method's weights (none: main only;
// manual: percentages; tca: standard cost × raf qty; quantity: raf qty), each
// co-product is rounded to 4 places and the residual goes to the main product,
// so the parts always add to the batch cost exactly. A residual that would be
// negative (a weightless main product) is taken back off the co-products. The
// burdens are refused (PMF-0021) only when they exceed the cost both before and
// after rounding; a rounding-only overshoot is taken back off the burdens. Both
// overshoots are spread by trimOvershoot, so no share is ever negative.
func AllocateCost(batchCost decimal.Decimal, method AllocationMethod, main Product, coProducts []Product, byProducts []ByProduct) (AllocationResult, error) {
	var res AllocationResult
	cost := batchCost.Round(AmountPrecision)
	if cost.IsNegative() {
		return res, errorf(CodeInvalid, "batch cost must not be negative")
	}
	burdenTotal, exactTotal := decimal.Zero, decimal.Zero
	for _, bp := range byProducts {
		exact, err := burden(cost, bp)
		if err != nil {
			return res, err
		}
		amt := exact.Round(AmountPrecision)
		res.Burdens = append(res.Burdens, Allocated{ItemID: bp.ItemID, Amount: amt})
		burdenTotal = burdenTotal.Add(amt)
		exactTotal = exactTotal.Add(exact)
	}
	if burdenTotal.GreaterThan(cost) {
		if exactTotal.GreaterThan(cost) {
			return res, errorf(CodeAllocation, "by-product burden %s exceeds batch cost %s", burdenTotal.StringFixed(AmountPrecision), cost.StringFixed(AmountPrecision))
		}
		// Burdens that fit the cost exactly (percent burdens totalling 100 %, say)
		// can exceed it by a few ten-thousandths once each is rounded; that
		// overshoot is taken back off the burdens, as for co-products below.
		trimOvershoot(len(res.Burdens), func(i int) *decimal.Decimal { return &res.Burdens[i].Amount }, burdenTotal.Sub(cost))
		burdenTotal = cost
	}
	net := cost.Sub(burdenTotal)

	all := append([]Product{main}, coProducts...)
	weights := make([]decimal.Decimal, len(all))
	total := decimal.Zero
	for i, p := range all {
		var w decimal.Decimal
		switch method {
		case MethodNone:
			if i == 0 {
				w = decimal.NewFromInt(1)
			}
		case MethodManual:
			w = p.Percent
		case MethodTCA:
			w = p.StandardCost.Mul(p.RafQty)
		case MethodQuantity:
			w = p.RafQty
		default:
			return res, errorf(CodeInvalid, "unknown allocation method %q", method)
		}
		if w.IsNegative() {
			return res, errorf(CodeAllocation, "allocation weight for %s is negative", p.ItemID)
		}
		weights[i] = w
		total = total.Add(w)
	}
	if !total.IsPositive() {
		return res, errorf(CodeAllocation, "allocation weights sum to zero")
	}

	coSum, coPct := decimal.Zero, decimal.Zero
	for i, p := range coProducts {
		amt := net.Mul(weights[i+1]).DivRound(total, 24).Round(AmountPrecision)
		pct := percentOf(weights[i+1], total)
		res.CoProducts = append(res.CoProducts, Allocated{ItemID: p.ItemID, Amount: amt, Percent: pct})
		coSum = coSum.Add(amt)
		coPct = coPct.Add(pct)
	}
	// When the main product weighs (almost) nothing, rounding every co-product
	// up can overshoot the remainder by a few ten-thousandths; the main product
	// cannot take a negative residual, so the overshoot is taken back off the
	// co-products instead (amounts and percentages alike).
	if over := coSum.Sub(net); over.IsPositive() {
		trimOvershoot(len(res.CoProducts), func(i int) *decimal.Decimal { return &res.CoProducts[i].Amount }, over)
		coSum = net
	}
	if over := coPct.Sub(oneHundred()); over.IsPositive() {
		trimOvershoot(len(res.CoProducts), func(i int) *decimal.Decimal { return &res.CoProducts[i].Percent }, over)
		coPct = oneHundred()
	}
	res.Main = Allocated{ItemID: main.ItemID, Amount: net.Sub(coSum), Percent: oneHundred().Sub(coPct)}
	res.Total = res.Main.Amount.Add(coSum).Add(burdenTotal)
	return res, nil
}

// trimOvershoot takes a rounding overshoot back off n values (at(i) points at
// the i-th) one ten-thousandth at a time, largest value first (first index on a
// tie), passing over values already at zero. Each value that rounded up is at
// least one ten-thousandth and gained at most half of one, so one pass always
// suffices and no value goes negative; taking the whole overshoot off the
// single largest value could (four burdens of 0.0001 rounded from 0.00005 on a
// cost of 0.0002 overshoot by 0.0002).
func trimOvershoot(n int, at func(int) *decimal.Decimal, over decimal.Decimal) {
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return at(order[a]).GreaterThan(*at(order[b])) })
	unit := decimal.New(1, -AmountPrecision)
	for over.IsPositive() {
		trimmed := false
		for _, i := range order {
			if !over.IsPositive() {
				break
			}
			if v := at(i); !v.LessThan(unit) {
				*v = v.Sub(unit)
				over = over.Sub(unit)
				trimmed = true
			}
		}
		if !trimmed {
			return // unreachable: the overshoot never exceeds what rounding added
		}
	}
}

func percentOf(w, total decimal.Decimal) decimal.Decimal {
	return w.Mul(oneHundred()).DivRound(total, 24).Round(AmountPrecision)
}

// burden returns a by-product's burden before rounding (cost × rate / 100 is
// exact in decimal, as is qty × rate).
func burden(cost decimal.Decimal, bp ByProduct) (decimal.Decimal, error) {
	switch bp.CostType {
	case "percent":
		if bp.Rate.IsNegative() || bp.Rate.GreaterThan(oneHundred()) {
			return decimal.Zero, errorf(CodeInvalid, "by-product %s percent must be between 0 and 100", bp.ItemID)
		}
		return cost.Mul(bp.Rate).Shift(-2), nil
	case "per-series", "per-quantity", "recycled":
		if bp.Qty.IsNegative() || bp.Rate.IsNegative() {
			return decimal.Zero, errorf(CodeInvalid, "by-product %s quantity and rate must not be negative", bp.ItemID)
		}
		return bp.Qty.Mul(bp.Rate), nil
	}
	return decimal.Zero, errorf(CodeInvalid, "unknown by-product cost type %q", bp.CostType)
}
