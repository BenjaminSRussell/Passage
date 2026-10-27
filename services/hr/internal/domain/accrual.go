package domain

import (
	"fmt"

	"github.com/shopspring/decimal"
)

// AccrualAmount is BR-42.3: rate × proration factor, where the factor is
// days employed in the period ÷ days in the period when prorating, rounded
// half-up to two decimals.
func AccrualAmount(rate decimal.Decimal, employedDays, daysInPeriod int, prorate bool) decimal.Decimal {
	if !prorate || employedDays >= daysInPeriod {
		return rate.Round(2)
	}
	if employedDays <= 0 || daysInPeriod <= 0 {
		return decimal.Zero
	}
	return rate.Mul(decimal.NewFromInt(int64(employedDays))).Div(decimal.NewFromInt(int64(daysInPeriod))).Round(2)
}

// Bucket is one balance bucket: balance = accrued − used + adjusted − expired.
type Bucket struct {
	Accrued, Used, Adjusted, Expired decimal.Decimal
}

// Balance is the bucket's current value.
func (b Bucket) Balance() decimal.Decimal {
	return b.Accrued.Sub(b.Used).Add(b.Adjusted).Sub(b.Expired)
}

// Ledger holds the CARRY_OVER and CURRENT buckets of one absence plan.
type Ledger struct {
	Carry   Bucket
	Current Bucket
}

// Total is the spendable balance across both buckets.
func (l Ledger) Total() decimal.Decimal {
	return l.Carry.Balance().Add(l.Current.Balance())
}

// Accrue adds an accrual to CURRENT. The total never exceeds maxBalance; the
// excess is returned as discarded so the caller can log it.
func (l Ledger) Accrue(amount, maxBalance decimal.Decimal) (Ledger, decimal.Decimal) {
	room := maxBalance.Sub(l.Total())
	if room.LessThan(decimal.Zero) {
		room = decimal.Zero
	}
	kept := decimal.Min(amount, room)
	l.Current.Accrued = l.Current.Accrued.Add(kept)
	return l, amount.Sub(kept)
}

// YearEnd closes the plan year: the total carries over up to carryMax and the
// rest expires. CURRENT is cleared. A negative total carries as is (BR-42.4).
func (l Ledger) YearEnd(carryMax decimal.Decimal) (Ledger, decimal.Decimal) {
	total := l.Total()
	if total.LessThan(decimal.Zero) {
		return Ledger{Carry: Bucket{Accrued: total}}, decimal.Zero
	}
	carried := decimal.Min(total, carryMax)
	return Ledger{Carry: Bucket{Accrued: carried}}, total.Sub(carried)
}

// Consume takes qty from CARRY_OVER first, then CURRENT (AC-42.3). The balance
// may not fall below floor (zero when negatives are disallowed; −rate×2 when
// allowed, BR-42.4). A breach returns HCM-ABS-002.
func (l Ledger) Consume(qty, floor decimal.Decimal) (Ledger, error) {
	if l.Total().Sub(qty).LessThan(floor) {
		return l, &Error{Code: "HCM-ABS-002", Detail: fmt.Sprintf("Requested %s exceeds balance %s", qty.StringFixed(2), l.Total().StringFixed(2))}
	}
	fromCarry := decimal.Min(qty, decimal.Max(l.Carry.Balance(), decimal.Zero))
	l.Carry.Used = l.Carry.Used.Add(fromCarry)
	l.Current.Used = l.Current.Used.Add(qty.Sub(fromCarry))
	return l, nil
}
