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
	// CodeModBelowRecognized: the change would take back more revenue than revrec holds for the obligations it re-allocates (the
	// price falls below what point-in-time and usage obligations were billed at); billing's credit note does that.
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
	// Credited is what credit notes took off the line (revenue_schedule.credited_amount). It changes the revenue, not the
	// performance: the line's share of the obligation's measure of progress is Amount + Credited, and a line credit notes took to
	// zero (REVERSED) still counts in the measure.
	Credited decimal.Decimal
}

// measure is a line's weight in the measure of progress: its amount before credit notes. A line a modification cancelled (REVERSED
// with nothing credited) weighs nothing.
func measure(l ModLine) decimal.Decimal {
	if l.Status == ScheduleReversed && !l.Credited.IsPositive() {
		return decimal.Zero
	}
	return l.Amount.Add(l.Credited)
}

// pending reports whether a line's performance is not transferred yet as far as its status goes: SCHEDULED, or REVERSED by credit
// notes (the time it stands for still passes, only its revenue was credited).
func pending(l ModLine) bool {
	return l.Status == ScheduleScheduled || (l.Status == ScheduleReversed && l.Credited.IsPositive())
}

// earnedBy reports whether a pending line is dated on or before the effective date.
func earnedBy(l ModLine, effective time.Time) bool {
	return l.Date != nil && !dateOnly(*l.Date).After(dateOnly(effective))
}

// ModObligation is a performance obligation with its schedule, before the modification.
type ModObligation struct {
	ID        string
	Method    string
	SSP       decimal.Decimal
	SSPSource string
	Allocated decimal.Decimal
	// Reversed is the recognized revenue credit notes already reversed on the obligation (it is netted off the recognized lines).
	Reversed decimal.Decimal
	Lines    []ModLine
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
	// Scope reduces or removes obligations (by id): the share of each one's remaining, not yet transferred performance that is still
	// promised after the modification, from 0 (the obligation is removed) to 1 (unchanged, the default). What was already transferred is
	// never taken back by a scope change. Without a NewSSP for it, the obligation's SSP is scaled to what stays promised: SSP x (share
	// transferred + scope x share left).
	Scope       map[string]decimal.Decimal
	Obligations []ModObligation
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
	// Reversal is the recognized revenue the modification takes back out (positive; Dr 4000 / Cr 2400 at the effective date): a
	// negative cumulative catch-up beyond the earned lines, or the obligation's share of a price concession on performance already
	// transferred (IFRS 15.88, ASC 606-10-32-43).
	Reversal decimal.Decimal
	// Removed is true when the modification removes the obligation (scope 0): its unrecognized lines are cancelled.
	Removed bool
	Lines   []LineChange
	NewLine *NewLine
}

// ModificationResult is the re-allocation of a whole contract.
type ModificationResult struct {
	TransactionPriceAfter decimal.Decimal
	Obligations           []ObligationChange
	CatchUp               decimal.Decimal
	Reversal              decimal.Decimal
}

func modErr(code, detail string) error { return &Error{Code: code, Detail: detail} }

// recognizedOf is the obligation's recognized revenue net of what credit notes reversed.
func recognizedOf(o ModObligation) decimal.Decimal {
	sum := decimal.Zero
	for _, l := range o.Lines {
		if l.Status == ScheduleRecognized {
			sum = sum.Add(l.Amount)
		}
	}
	return sum.Sub(o.Reversed)
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

// liveTotal is the measure of all the obligation's lines (see measure): credit notes do not shrink it.
func liveTotal(o ModObligation) decimal.Decimal {
	s := decimal.Zero
	for _, l := range o.Lines {
		s = s.Add(measure(l))
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
			if l.Status == ScheduleRecognized || (pending(l) && earnedBy(l, effective)) {
				earned = earned.Add(measure(l))
			}
		}
		return earned.DivRound(total, 18)
	}
	return decimal.NewFromInt(1)
}

// leftShare is the share of an obligation's measure not recognized yet (SCHEDULED lines, and lines credit notes took to zero): what
// performance remains for a prospective modification.
func leftShare(o ModObligation) decimal.Decimal {
	total := liveTotal(o)
	if !total.IsPositive() {
		return decimal.Zero
	}
	left := decimal.Zero
	for _, l := range o.Lines {
		if pending(l) {
			left = left.Add(measure(l))
		}
	}
	return left.DivRound(total, 18)
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
	for id, sc := range in.Scope {
		if sc.IsNegative() || sc.GreaterThan(decimal.NewFromInt(1)) {
			return ModificationResult{}, modErr("COM-0001", "the remaining scope of obligation "+id+" must be between 0 and 1")
		}
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

func scopeOf(in ModificationInput, o ModObligation) decimal.Decimal {
	if v, ok := in.Scope[o.ID]; ok {
		return v
	}
	return decimal.NewFromInt(1)
}

// split returns an obligation's SCHEDULED lines dated on or before the effective date (earned, the next run recognizes them anyway)
// and those after it (future performance).
func split(o ModObligation, effective time.Time) (earned, future []ModLine) {
	for _, l := range scheduledOf(o) {
		if l.Date != nil && !dateOnly(*l.Date).After(dateOnly(effective)) {
			earned = append(earned, l)
		} else {
			future = append(future, l)
		}
	}
	return earned, future
}

// scoped is an obligation's progress and stand-alone selling price after a scope change at the effective date. For a ratable
// obligation the transferred measure is the recognized lines plus the earned lines, the
// remaining measure the future lines times the scope; for a milestone it is all or nothing. With scope 1 the progress equals
// Progress.
func scoped(in ModificationInput, o ModObligation) (progress, ssp decimal.Decimal) {
	sc, base := scopeOf(in, o), sspOf(in, o)
	_, explicit := in.NewSSP[o.ID]
	one := decimal.NewFromInt(1)
	if o.Method == MethodMilestone {
		if Progress(o, in.EffectiveDate).Equal(one) {
			return one, base
		}
		if explicit && sc.IsPositive() {
			return decimal.Zero, base
		}
		return decimal.Zero, base.Mul(sc)
	}
	// The measure is the lines (time for a ratable obligation) before credit notes, which change the revenue and not the performance.
	done, left := decimal.Zero, decimal.Zero
	for _, l := range o.Lines {
		switch {
		case l.Status == ScheduleRecognized || (pending(l) && earnedBy(l, in.EffectiveDate)):
			done = done.Add(measure(l))
		case pending(l):
			left = left.Add(measure(l))
		}
	}
	whole := done.Add(left)
	kept := done.Add(left.Mul(sc))
	if !kept.IsPositive() {
		return decimal.Zero, decimal.Zero
	}
	progress = done.DivRound(kept, 18)
	if explicit || !whole.IsPositive() {
		return progress, base
	}
	return progress, base.Mul(kept).DivRound(whole, 6)
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
		return res, modErr(CodeModBelowRecognized, "the new transaction price does not cover what the point-in-time and usage obligations were billed at; credit the invoice instead")
	}
	type plan struct {
		o        ModObligation
		progress decimal.Decimal
		ssp      decimal.Decimal
	}
	plans := make([]plan, len(live))
	var dobs []Obligation
	var index []int
	for i, o := range live {
		p, ssp := scoped(in, o)
		plans[i] = plan{o: o, progress: p, ssp: ssp}
		if ssp.IsPositive() {
			dobs = append(dobs, Obligation{ID: o.ID, SSP: ssp, SSPSource: o.SSPSource, Method: o.Method})
			index = append(index, i)
		}
	}
	if len(dobs) == 0 {
		return res, modErr(CodeModNothingLeft, "the modification removes every obligation before any performance; cancel the contract instead")
	}
	alloc, err := Allocate(money.Amount{Value: pool, Currency: in.Currency}, dobs, rule)
	if err != nil {
		return res, err
	}
	after := make([]decimal.Decimal, len(live))
	sspAfter := make([]decimal.Decimal, len(live))
	for k, i := range index {
		after[i], sspAfter[i] = alloc[k].Allocated, alloc[k].SSP
	}
	for i, pl := range plans {
		o := pl.o
		if sspAfter[i].IsZero() {
			sspAfter[i] = o.SSP
		}
		target := after[i].Mul(pl.progress).Round(exp)
		catch := target.Sub(recognizedOf(o))
		ch := ObligationChange{ID: o.ID, AllocatedBefore: o.Allocated, AllocatedAfter: after[i], SSPBefore: o.SSP, SSPAfter: sspAfter[i],
			Progress: pl.progress, Removed: scopeOf(in, o).IsZero()}
		earned, future := split(o, in.EffectiveDate)
		ch.CatchUp = catch.Sub(sum(earned))
		if catch.IsNegative() {
			// A negative cumulative catch-up (ASC 606-10-25-13(b) works both ways): the earned lines are not recognized and what is
			// still too much comes back out of recognized revenue at the effective date.
			ch.Lines = append(ch.Lines, distribute(decimal.Zero, earned, in.Currency)...)
			ch.Reversal = catch.Neg()
		} else {
			ch.Lines = append(ch.Lines, distribute(catch, earned, in.Currency)...)
			if len(earned) == 0 && catch.IsPositive() {
				ch.NewLine = &NewLine{Date: dateOnly(in.EffectiveDate), Amount: catch, Recognized: o.Method == MethodMilestone}
			}
		}
		left := after[i].Sub(target)
		if len(future) == 0 && left.IsPositive() {
			return res, modErr(CodeModNothingLeft, "an obligation has revenue left to schedule but no schedule line to carry it")
		}
		ch.Lines = append(ch.Lines, distribute(left, future, in.Currency)...)
		res.Obligations = append(res.Obligations, ch)
		res.CatchUp = res.CatchUp.Add(ch.CatchUp)
		res.Reversal = res.Reversal.Add(ch.Reversal)
	}
	return res, nil
}

// modifyProspective treats the remaining performance as a new contract (ASC 606-10-25-13(a), IFRS 15.21(a)): the consideration still
// to recognize is the new transaction price less the point-in-time and usage allocations and less the revenue the ratable and
// milestone obligations recognized, and it is allocated over the performance left weighted by SSP, the share left and the scope.
// When the new price is below what is already recognized, the shortfall is a price concession on performance already transferred:
// it is allocated to the ratable and milestone obligations in proportion to their recognized revenue and taken out of revenue at the
// effective date (IFRS 15.88, ASC 606-10-32-43), and nothing is left for the remaining performance.
func modifyProspective(in ModificationInput, price decimal.Decimal, rule string) (ModificationResult, error) {
	exp := money.MinorUnits(in.Currency)
	res := ModificationResult{TransactionPriceAfter: price}
	rc := price
	var left, deferred []ModObligation
	for _, o := range in.Obligations {
		switch {
		case o.Method == MethodPointInTime || o.Method == MethodUsage:
			rc = rc.Sub(o.Allocated)
			continue
		case len(scheduledOf(o)) == 0:
			rc = rc.Sub(recognizedOf(o))
		default:
			rc = rc.Sub(recognizedOf(o))
			left = append(left, o)
		}
		deferred = append(deferred, o)
	}
	if len(left) == 0 && !rc.IsNegative() {
		return res, modErr(CodeModNothingLeft, "every obligation is already satisfied; there is nothing left to modify prospectively")
	}
	changes := map[string]*ObligationChange{}
	var order []string
	change := func(o ModObligation) *ObligationChange {
		if c, ok := changes[o.ID]; ok {
			return c
		}
		c := &ObligationChange{ID: o.ID, AllocatedBefore: o.Allocated, AllocatedAfter: recognizedOf(o), SSPBefore: o.SSP, SSPAfter: sspOf(in, o),
			Removed: scopeOf(in, o).IsZero()}
		changes[o.ID], order = c, append(order, o.ID)
		return c
	}
	for _, o := range left {
		c := change(o)
		c.Lines = distribute(decimal.Zero, scheduledOf(o), in.Currency)
		if _, explicit := in.NewSSP[o.ID]; !explicit {
			// What stays promised: the transferred share plus the scope of the share left.
			sh := leftShare(o)
			c.SSPAfter = o.SSP.Mul(decimal.NewFromInt(1).Sub(sh).Add(sh.Mul(scopeOf(in, o)))).Round(6)
		}
	}
	if rc.IsNegative() {
		shortfall := rc.Neg()
		base := decimal.Zero
		for _, o := range deferred {
			base = base.Add(recognizedOf(o))
		}
		if shortfall.GreaterThan(base) {
			return res, modErr(CodeModBelowRecognized, "the new transaction price is below what the point-in-time and usage obligations were billed at; credit the invoice instead")
		}
		given := decimal.Zero
		var takers []ModObligation
		for _, o := range deferred {
			if recognizedOf(o).IsPositive() {
				takers = append(takers, o)
			}
		}
		for k, o := range takers {
			share := shortfall.Mul(recognizedOf(o)).DivRound(base, 18).RoundBank(exp)
			if k == len(takers)-1 {
				share = shortfall.Sub(given)
			}
			given = given.Add(share)
			c := change(o)
			c.Reversal = share
			c.AllocatedAfter = recognizedOf(o).Sub(share)
			res.Reversal = res.Reversal.Add(share)
		}
	} else {
		var dobs []Obligation
		var carriers []ModObligation
		for _, o := range left {
			weight := sspOf(in, o).Mul(leftShare(o)).Mul(scopeOf(in, o))
			if weight.IsPositive() {
				dobs = append(dobs, Obligation{ID: o.ID, SSP: weight, SSPSource: "EXPLICIT", Method: o.Method})
				carriers = append(carriers, o)
			}
		}
		if len(dobs) == 0 && rc.IsPositive() {
			return res, modErr(CodeModNothingLeft, "the modification removes all remaining performance but leaves consideration to recognize")
		}
		if len(dobs) > 0 {
			alloc, err := Allocate(money.Amount{Value: rc, Currency: in.Currency}, dobs, rule)
			if err != nil {
				return res, err
			}
			for i, o := range carriers {
				c := change(o)
				c.AllocatedAfter = recognizedOf(o).Add(alloc[i].Allocated)
				c.Lines = distribute(alloc[i].Allocated, scheduledOf(o), in.Currency)
			}
		}
	}
	for _, id := range order {
		res.Obligations = append(res.Obligations, *changes[id])
	}
	return res, nil
}
