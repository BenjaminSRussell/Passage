package domain

import "github.com/shopspring/decimal"

// SmartBand is one smart-rounding band (FR-26.7): amounts below UpTo are rounded up
// to the next value ending in Ending. The last band is Unbounded.
type SmartBand struct {
	UpTo      decimal.Decimal
	Ending    decimal.Decimal
	Unbounded bool
}

// DefaultSmartBands is the reference rule: 0-10 ends .99, 10-100 ends .95, 100 and up whole.
func DefaultSmartBands() []SmartBand {
	return []SmartBand{
		{UpTo: decimal.NewFromInt(10), Ending: decimal.RequireFromString("0.99")},
		{UpTo: decimal.NewFromInt(100), Ending: decimal.RequireFromString("0.95")},
		{Unbounded: true, Ending: decimal.Zero},
	}
}

// SmartRound rounds v up to the next price point of its band (AC-26.10: 7.7805 -> 7.99).
func SmartRound(v decimal.Decimal, bands []SmartBand) decimal.Decimal {
	band := bands[len(bands)-1]
	for _, b := range bands {
		if b.Unbounded || v.LessThan(b.UpTo) {
			band = b
			break
		}
	}
	candidate := v.Floor().Add(band.Ending)
	if candidate.LessThan(v) {
		candidate = candidate.Add(decimal.NewFromInt(1))
	}
	return candidate
}

// Tier is one step of a rebate or vendor rebate schedule (BR-26.8). A tier pays
// RatePct of invoice value, or AmountPerUnit per unit when RatePct is zero.
type Tier struct {
	From          decimal.Decimal
	RatePct       decimal.Decimal
	AmountPerUnit decimal.Decimal
}

// TierFor returns the tier in force at a cumulative period total: the highest From not above it.
func TierFor(tiers []Tier, cumulative decimal.Decimal) (Tier, bool) {
	var found Tier
	ok := false
	for _, t := range tiers {
		if t.From.LessThanOrEqual(cumulative) && (!ok || t.From.GreaterThan(found.From)) {
			found, ok = t, true
		}
	}
	return found, ok
}

// RebateAccrual accrues one invoice against the tier in force at periodTotal (the
// cumulative period basis including this invoice). Rounded once, half-even.
func RebateAccrual(tiers []Tier, periodTotal, invoiceValue, invoiceQty decimal.Decimal, currency string) decimal.Decimal {
	t, ok := TierFor(tiers, periodTotal)
	if !ok {
		return decimal.Zero
	}
	if t.RatePct.IsPositive() {
		return roundMinor(invoiceValue.Mul(t.RatePct).Shift(-2), currency)
	}
	return roundMinor(invoiceQty.Mul(t.AmountPerUnit), currency)
}

// CommissionAmount is basis x rate for one line (BR-26.12); credit notes pass a negative basis.
func CommissionAmount(basis, ratePct decimal.Decimal, currency string) decimal.Decimal {
	return roundMinor(basis.Mul(ratePct).Shift(-2), currency)
}

// Share is a sales rep's share of a commission, in percent.
type Share struct {
	RepID    string
	SharePct decimal.Decimal
}

// Split is one rep's commission transaction amount.
type Split struct {
	RepID  string
	Amount decimal.Decimal
}

// SplitCommission divides amount across reps by share (BR-26.12): each share rounds
// half-even; the residual minor unit goes to the first rep so the parts sum exactly.
func SplitCommission(amount decimal.Decimal, currency string, shares []Share) []Split {
	out := make([]Split, len(shares))
	total := decimal.Zero
	for i, s := range shares {
		v := roundMinor(amount.Mul(s.SharePct).Shift(-2), currency)
		out[i] = Split{RepID: s.RepID, Amount: v}
		total = total.Add(v)
	}
	if len(out) > 0 {
		out[0].Amount = out[0].Amount.Add(amount.Sub(total))
	}
	return out
}

// Free goods kinds (FR-26.22).
const (
	FreeGoodsInclusive = "INCLUSIVE"
	FreeGoodsExclusive = "EXCLUSIVE"
)

// FreeGoodsRule grants Free units for every Every units ordered, once MinQty is reached.
type FreeGoodsRule struct {
	Kind   string
	Every  decimal.Decimal
	Free   decimal.Decimal
	MinQty decimal.Decimal
	// Calc is PROPORTIONAL (fractional, 6 dp), UNIT_REFERENCE (the proportional quantity
	// rounded down to whole units) or WHOLE_UNITS (floor of whole multiples of Every).
	Calc string
}

// FreeGoodsResult: Priced units are charged. Inclusive free units come out of the
// ordered quantity; exclusive free units are added on top of it.
type FreeGoodsResult struct {
	Priced decimal.Decimal
	Free   decimal.Decimal
}

// FreeGoods splits an ordered quantity into priced and free units (BR-26.13).
func FreeGoods(ordered decimal.Decimal, r FreeGoodsRule) FreeGoodsResult {
	if r.Every.IsZero() || ordered.LessThan(r.MinQty) {
		return FreeGoodsResult{Priced: ordered}
	}
	var free decimal.Decimal
	switch r.Calc {
	case CalcProportional:
		free = ordered.Mul(r.Free).Div(r.Every).Truncate(6)
	case CalcUnitReference:
		free = ordered.Mul(r.Free).Div(r.Every).Floor()
	default:
		free = ordered.Div(r.Every).Floor().Mul(r.Free)
	}
	if r.Kind == FreeGoodsInclusive {
		return FreeGoodsResult{Priced: ordered.Sub(free), Free: free}
	}
	return FreeGoodsResult{Priced: ordered, Free: free}
}
