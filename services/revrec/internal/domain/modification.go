package domain

import (
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

// Contract modification types (ASC 606-10-25-10 to 25-13, IFRS 15.20 to 15.21). A modification that adds distinct goods at their stand-alone
// price is a separate contract and is not handled here (POST :allocate does that); these two re-allocate the existing obligations:
//
//   - PROSPECTIVE: the remaining goods or services are distinct from those already transferred, so the change is accounted for as if
//     the old contract ended and a new one began. The consideration still to recognize (the new transaction price less what is
//     already recognized or booked) is allocated over the obligations with performance left, by their stand-alone selling prices
//     weighted by the share of each left. Recognized revenue is never touched and nothing is caught up.
//   - CATCH_UP: the remaining goods are not distinct and form part of a single obligation partially satisfied, so the change is
//     accounted for as an adjustment of revenue at the modification date. The whole new transaction price is allocated again over
//     the obligations, and each obligation recognizes a cumulative catch-up so that its revenue to date equals its new
//     allocation times its progress at the effective date.
const (
	ModProspective = "PROSPECTIVE"
	ModCatchUp     = "CATCH_UP"
)

// Error codes of a modification.
const (
	// CodeModBelowRecognized: the change would take revenue back out of what is already recognized; that is a credit note.
	CodeModBelowRecognized = "REV-0402"
	// CodeModNothingLeft: a prospective modification needs an obligation with performance left.
	CodeModNothingLeft = "REV-0404"
)

// ValidModificationType reports whether t is PROSPECTIVE or CATCH_UP.
func ValidModificationType(t string) bool { return t == ModProspective || t == ModCatchUp }

// ModLine is a stored schedule line as the modification sees it.
type ModLine struct {
	ID     string
	Date   *time.Time
	Amount decimal.Decimal
	Status string
}

// ModObligation is a performance obligation with its schedule, before the modification.
type ModObligation struct {
	ID        string
	Method    string
	SSP       decimal.Decimal
	SSPSource string
	Allocated decimal.Decimal
	Lines     []ModLine
}

// ModificationInput is what a modification works on. PriceChange is signed. NewSSP revises the stand-alone selling price of the named
// obligations (by id) at the modification date.
type ModificationInput struct {
	Type             string
	EffectiveDate    time.Time
	Currency         string
	TransactionPrice decimal.Decimal
	PriceChange      decimal.Decimal
	ResidualRule     string
	NewSSP           map[string]decimal.Decimal
	Obligations      []ModObligation
}

// LineChange is the new amount of a SCHEDULED line.
type LineChange struct {
	ID     string
	Amount decimal.Decimal
}

// NewLine is a schedule line the modification adds: the catch-up of an obligation with no line of its own left to carry it. Recognized
// is true for a milestone obligation that is already satisfied (the line is recognized on the effective date at once); a ratable line
// stays SCHEDULED, dated on the effective date, and the next recognition run takes it.
type NewLine struct {
	Date       time.Time
	Amount     decimal.Decimal
	Recognized bool
}

// ObligationChange is the outcome for one obligation.
type ObligationChange struct {
	ID              string
	AllocatedBefore decimal.Decimal
	AllocatedAfter  decimal.Decimal
	SSPBefore       decimal.Decimal
	SSPAfter        decimal.Decimal
	// Progress is the share of the obligation transferred at the effective date (CATCH_UP only; zero for PROSPECTIVE).
	Progress decimal.Decimal
	// CatchUp is the cumulative catch-up: revenue to date at the new allocation less what is recognized and less the old amounts of the lines
	// that were already earned on the effective date but not yet recognized (they are recognized anyway). Negative when the change lowers
	// those earned lines.
	CatchUp decimal.Decimal
	Lines   []LineChange
	NewLine *NewLine
}

// ModificationResult is the re-allocation of a whole contract.
type ModificationResult struct {
	TransactionPriceAfter decimal.Decimal
	Obligations           []ObligationChange
	CatchUp               decimal.Decimal
}

func modErr(code, detail string) error { return &Error{Code: code, Detail: detail} }

func recognizedOf(o ModObligation) decimal.Decimal {
	sum := decimal.Zero
	for _, l := range o.Lines {
		if l.Status == ScheduleRecognized {
			sum = sum.Add(l.Amount)
		}
	}
	return sum
}

func scheduledOf(o ModObligation) []ModLine {
	var out []ModLine
	for _, l := range o.Lines {
		if l.Status == ScheduleScheduled {
			out = append(out, l)
		}
	}
	return out
}

func sum(lines []ModLine) decimal.Decimal {
	s := decimal.Zero
	for _, l := range lines {
		s = s.Add(l.Amount)
	}
	return s
}

// liveTotal is the amount of all lines that still count (not REVERSED).
func liveTotal(o ModObligation) decimal.Decimal {
	s := decimal.Zero
	for _, l := range o.Lines {
		if l.Status != ScheduleReversed {
			s = s.Add(l.Amount)
		}
	}
	return s
}

// Progress is the share of an obligation transferred at the effective date: for a ratable obligation the amount of the lines that are
// recognized or dated on or before the date over the amount of all its lines; for a milestone 1 once satisfied, else 0.
func Progress(o ModObligation, effective time.Time) decimal.Decimal {
	switch o.Method {
	case MethodMilestone:
		for _, l := range o.Lines {
			if l.Status == ScheduleRecognized {
				return decimal.NewFromInt(1)
			}
		}
		return decimal.Zero
	case MethodRatable:
		total := liveTotal(o)
		if !total.IsPositive() {
			return decimal.Zero
		}
		earned := decimal.Zero
		for _, l := range o.Lines {
			if l.Status == ScheduleRecognized || (l.Status == ScheduleScheduled && l.Date != nil && !dateOnly(*l.Date).After(dateOnly(effective))) {
				earned = earned.Add(l.Amount)
			}
		}
		return earned.DivRound(total, 18)
	}
	return decimal.NewFromInt(1)
}

// leftShare is the share of an obligation's lines still SCHEDULED: what performance remains for a prospective modification.
func leftShare(o ModObligation) decimal.Decimal {
	total := liveTotal(o)
	if !total.IsPositive() {
		return decimal.Zero
	}
	return sum(scheduledOf(o)).DivRound(total, 18)
}

// distribute splits total over lines in proportion to their old amounts, rounding half-even to the currency's minor unit with the last
// line taking the residual. With no weight at all the first line takes everything.
func distribute(total decimal.Decimal, lines []ModLine, currency string) []LineChange {
	if len(lines) == 0 {
		return nil
	}
	exp := money.MinorUnits(currency)
	weight := sum(lines)
	out := make([]LineChange, len(lines))
	given := decimal.Zero
	for i, l := range lines {
		out[i].ID = l.ID
		switch {
		case i == len(lines)-1:
			out[i].Amount = total.Sub(given)
		case weight.IsZero():
			out[i].Amount = decimal.Zero
		default:
			out[i].Amount = total.Mul(l.Amount).DivRound(weight, 18).RoundBank(exp)
		}
		given = given.Add(out[i].Amount)
	}
	if weight.IsZero() {
		out[0].Amount, out[len(out)-1].Amount = total, decimal.Zero
		if len(out) == 1 {
			out[0].Amount = total
		}
	}
	return out
}

// Modify re-allocates a contract's obligations after a change of price or of stand-alone selling price (see the type constants).
// POINT_IN_TIME and USAGE obligations are left as they are: their revenue was booked by the invoice or arrives with usage.
func Modify(in ModificationInput) (ModificationResult, error) {
	if !ValidModificationType(in.Type) {
		return ModificationResult{}, modErr("COM-0001", "modification type must be PROSPECTIVE or CATCH_UP")
	}
	rule := in.ResidualRule
	if rule == "" {
		rule = ResidualLargest
	}
	price := in.TransactionPrice.Add(in.PriceChange)
	if !price.IsPositive() {
		return ModificationResult{}, modErr("REV-0001", "the transaction price after the change must be greater than 0")
	}
	if in.Type == ModCatchUp {
		return modifyCatchUp(in, price, rule)
	}
	return modifyProspective(in, price, rule)
}

func sspOf(in ModificationInput, o ModObligation) decimal.Decimal {
	if v, ok := in.NewSSP[o.ID]; ok {
		return v
	}
	return o.SSP
}

func modifyCatchUp(in ModificationInput, price decimal.Decimal, rule string) (ModificationResult, error) {
	exp := money.MinorUnits(in.Currency)
	pool := price
	var live []ModObligation
	for _, o := range in.Obligations {
		if o.Method == MethodPointInTime || o.Method == MethodUsage {
			pool = pool.Sub(o.Allocated)
			continue
		}
		live = append(live, o)
	}
	res := ModificationResult{TransactionPriceAfter: price}
	if len(live) == 0 {
		return res, modErr(CodeModNothingLeft, "the contract has no ratable or milestone obligation to re-allocate")
	}
	if !pool.IsPositive() {
		return res, modErr(CodeModBelowRecognized, "the new transaction price leaves nothing for the obligations that can be re-allocated")
	}
	dobs := make([]Obligation, len(live))
	for i, o := range live {
		dobs[i] = Obligation{ID: o.ID, SSP: sspOf(in, o), SSPSource: o.SSPSource, Method: o.Method}
	}
	alloc, err := Allocate(money.Amount{Value: pool, Currency: in.Currency}, dobs, rule)
	if err != nil {
		return res, err
	}
	for i, o := range live {
		after := alloc[i].Allocated
		p := Progress(o, in.EffectiveDate)
		target := after.Mul(p).Round(exp)
		recognized := recognizedOf(o)
		catch := target.Sub(recognized)
		if catch.IsNegative() {
			return res, modErr(CodeModBelowRecognized, "the change would take "+catch.Neg().StringFixed(exp)+" back out of revenue already recognized on one obligation; reverse it with a credit note instead")
		}
		ch := ObligationChange{ID: o.ID, AllocatedBefore: o.Allocated, AllocatedAfter: after, SSPBefore: o.SSP, SSPAfter: alloc[i].SSP, Progress: p}
		var earned, future []ModLine
		for _, l := range scheduledOf(o) {
			if l.Date != nil && !dateOnly(*l.Date).After(dateOnly(in.EffectiveDate)) {
				earned = append(earned, l)
			} else {
				future = append(future, l)
			}
		}
		ch.CatchUp = catch.Sub(sum(earned))
		ch.Lines = append(ch.Lines, distribute(catch, earned, in.Currency)...)
		if len(earned) == 0 && catch.IsPositive() {
			ch.NewLine = &NewLine{Date: dateOnly(in.EffectiveDate), Amount: catch, Recognized: o.Method == MethodMilestone}
		}
		left := after.Sub(target)
		if len(future) == 0 && left.IsPositive() {
			return res, modErr(CodeModNothingLeft, "an obligation has revenue left to schedule but no schedule line to carry it")
		}
		ch.Lines = append(ch.Lines, distribute(left, future, in.Currency)...)
		res.Obligations = append(res.Obligations, ch)
		res.CatchUp = res.CatchUp.Add(ch.CatchUp)
	}
	return res, nil
}

func modifyProspective(in ModificationInput, price decimal.Decimal, rule string) (ModificationResult, error) {
	res := ModificationResult{TransactionPriceAfter: price}
	rc := price
	var left []ModObligation
	for _, o := range in.Obligations {
		if o.Method == MethodPointInTime || o.Method == MethodUsage || len(scheduledOf(o)) == 0 {
			rc = rc.Sub(o.Allocated)
			continue
		}
		rc = rc.Sub(recognizedOf(o))
		left = append(left, o)
	}
	if len(left) == 0 {
		return res, modErr(CodeModNothingLeft, "every obligation is already satisfied; there is nothing left to modify prospectively")
	}
	if rc.IsNegative() {
		return res, modErr(CodeModBelowRecognized, "the new transaction price is below the revenue already recognized; reverse the difference with a credit note instead")
	}
	dobs := make([]Obligation, len(left))
	for i, o := range left {
		weight := sspOf(in, o).Mul(leftShare(o))
		if !weight.IsPositive() {
			return res, modErr("REV-0001", "every obligation with performance left needs a positive standalone selling price")
		}
		dobs[i] = Obligation{ID: o.ID, SSP: weight, SSPSource: "EXPLICIT", Method: o.Method}
	}
	alloc, err := Allocate(money.Amount{Value: rc, Currency: in.Currency}, dobs, rule)
	if err != nil {
		return res, err
	}
	for i, o := range left {
		share := alloc[i].Allocated
		ch := ObligationChange{ID: o.ID, AllocatedBefore: o.Allocated, AllocatedAfter: recognizedOf(o).Add(share), SSPBefore: o.SSP, SSPAfter: sspOf(in, o)}
		ch.Lines = distribute(share, scheduledOf(o), in.Currency)
		res.Obligations = append(res.Obligations, ch)
	}
	return res, nil
}
