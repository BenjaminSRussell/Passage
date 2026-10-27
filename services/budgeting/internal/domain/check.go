package domain

import "github.com/shopspring/decimal"

// Result is the outcome of one budget check line.
type Result string

const (
	Pass Result = "pass"
	Warn Result = "warn"
	Fail Result = "fail"
)

// Balance is the per-combination state read under SELECT ... FOR UPDATE.
type Balance struct {
	Budget          decimal.Decimal
	Actuals         decimal.Decimal
	Encumbrances    decimal.Decimal
	PreEncumbrances decimal.Decimal
}

// Available is budget - actuals - encumbrances - pre-encumbrances (FR-38.4 step 5).
func (b Balance) Available() decimal.Decimal {
	return b.Budget.Sub(b.Actuals).Sub(b.Encumbrances).Sub(b.PreEncumbrances)
}

// Thresholds are the rule's warn and block percentages of available funds.
type Thresholds struct {
	WarnPct  decimal.Decimal
	BlockPct decimal.Decimal
}

// CheckInput is one document line presented to the check.
type CheckInput struct {
	Amount   decimal.Decimal // amount of the document line (steps 2-3 resolved by the caller)
	Prior    decimal.Decimal // the line's stored reservation, read by the store (step 4)
	Relieved decimal.Decimal // part of Prior already relieved by invoices; a revision cannot go below it
	Override bool            // caller holds the budget override duty (step 7)
}

// CheckOutcome is the result of steps 4-8 with the trace of steps observed.
type CheckOutcome struct {
	Result    Result
	Available decimal.Decimal
	Net       decimal.Decimal
	OverPct   decimal.Decimal // percent over available; zero when within funds
	Reserve   decimal.Decimal // added to encumbrances when Result is not Fail
	Trace     []string
}

// NetChange is FR-38.4 step 4: the line's new amount less its own prior
// reservation. A revision below the part already relieved by invoices only
// releases the open remainder, so the reservation never drops under relief.
func NetChange(amount, prior, relieved decimal.Decimal) decimal.Decimal {
	return decimal.Max(amount, relieved).Sub(prior)
}

// Check evaluates FR-38.4 steps 4-8 for one line. Net change at or below zero
// always passes (BR-38.3): a revision that lowers an amount releases funds.
func Check(b Balance, t Thresholds, in CheckInput) CheckOutcome {
	out := CheckOutcome{Available: b.Available(), Net: NetChange(in.Amount, in.Prior, in.Relieved)}
	out.Trace = append(out.Trace, "relieve-prior", "available")
	over := out.Net.Sub(out.Available)
	switch {
	case out.Net.Sign() <= 0 || over.Sign() <= 0:
		out.Result = Pass
	case out.Available.Sign() <= 0:
		out.OverPct = decimal.NewFromInt(100)
		out.Result = Fail
	default:
		out.OverPct = over.Div(out.Available).Mul(decimal.NewFromInt(100))
		switch {
		case out.OverPct.LessThanOrEqual(t.WarnPct):
			out.Result = Pass
		case out.OverPct.LessThanOrEqual(t.BlockPct):
			out.Result = Warn
		default:
			out.Result = Fail
		}
	}
	out.Trace = append(out.Trace, "threshold:"+string(out.Result))
	if out.Result == Fail && in.Override {
		out.Result = Warn
		out.Trace = append(out.Trace, "override")
	}
	if out.Result != Fail {
		out.Reserve = out.Net
		out.Trace = append(out.Trace, "reserve")
	}
	return out
}
