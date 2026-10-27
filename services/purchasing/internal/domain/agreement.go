package domain

import (
	"github.com/shopspring/decimal"
)

// Commitment types of a purchase agreement line (FR-17.5).
const (
	CommitQuantity      = "QUANTITY"
	CommitValue         = "VALUE"
	CommitCategoryValue = "CATEGORY_VALUE"
	CommitTotalValue    = "TOTAL_VALUE"
)

// Maximum enforcement of an agreement line (FR-17.5).
const (
	EnforceHard = "HARD"
	EnforceSoft = "SOFT"
	EnforceNone = "NONE"
)

// Agreement statuses (data model 3.17.4).
const (
	AgreementDraft     = "DRAFT"
	AgreementEffective = "EFFECTIVE"
	AgreementOnHold    = "ON_HOLD"
	AgreementClosed    = "CLOSED"
)

// Agreement actions (POST /agreements/{id}:<action>).
const (
	AgreementActivate = "activate"
	AgreementHold     = "hold"
	AgreementClose    = "close"
)

// DefaultThresholdPct is the release level that emits agreement.threshold.v1
// (3.17.13 agreement_threshold_pct, default 90).
var DefaultThresholdPct = decimal.NewFromInt(90)

// Agreement codes. PRC-AGR-003/004/005 are the PRD message catalog codes; the
// others are service codes listed in the README.
const (
	CodeAgreementLimit     = "PRC-AGR-003" // HARD limit would be exceeded
	CodeAgreementThreshold = "PRC-AGR-004" // warning: release level at or above the threshold
	CodeAgreementWindow    = "PRC-AGR-005" // delivery date outside validity (BR-17.10)
	CodeAgreementParty     = "PRC-AGR-006" // another legal entity, vendor or currency
	CodeAgreementInactive  = "PRC-AGR-007" // agreement is not EFFECTIVE
	CodeAgreementItem      = "PRC-AGR-008" // PO line product or unit does not match the agreement line
	CodeAgreementState     = "PRC-AGR-STATE"
)

// Agreement is a blanket purchase agreement (FR-17.5) with its lines.
type Agreement struct {
	ID            string
	Number        string
	LegalEntityID string
	VendorID      string
	Currency      string
	ValidFrom     Day
	ValidTo       *Day
	Status        string
	Version       int
	Lines         []AgreementLine
}

// AgreementLine is one commitment. Only the measure named by CommitmentType is
// enforced; released quantity and amount are both tracked for reporting.
type AgreementLine struct {
	ID              string
	LineNo          int
	CommitmentType  string
	ProductID       string
	Category        string
	Unit            string
	CommittedQty    decimal.Decimal
	CommittedAmount decimal.Decimal
	UnitPrice       decimal.Decimal
	DiscountPct     decimal.Decimal
	MaxEnforced     string
	PriceOverride   bool
	ReleasedQty     decimal.Decimal
	ReleasedAmount  decimal.Decimal
}

// Draw is what one PO line takes from an agreement line: its quantity and its
// net line amount in the agreement currency.
type Draw struct {
	Qty    decimal.Decimal
	Amount decimal.Decimal
}

// DrawResult reports the outcome of an accepted draw.
type DrawResult struct {
	// Overrun is set when a SOFT limit is exceeded; the draw still applies (BR-17.10).
	Overrun bool
	// ThresholdCrossed is set when the release level moves from below the
	// threshold to at or above it with this draw.
	ThresholdCrossed bool
	PctBefore        decimal.Decimal
	PctAfter         decimal.Decimal
}

// Notify reports whether the draw emits agreement.threshold.v1: on the draw
// that crosses the threshold, and on every SOFT overrun (BR-17.10).
func (r DrawResult) Notify() bool { return r.ThresholdCrossed || r.Overrun }

// Measure returns the committed and released figures of the line's commitment
// type: quantity for QUANTITY, amount for the value types.
func (l AgreementLine) Measure() (committed, released decimal.Decimal) {
	if l.CommitmentType == CommitQuantity {
		return l.CommittedQty, l.ReleasedQty
	}
	return l.CommittedAmount, l.ReleasedAmount
}

// Remaining is committed minus released on the line's measure; negative after
// a SOFT or NONE overrun.
func (l AgreementLine) Remaining() decimal.Decimal {
	c, r := l.Measure()
	return c.Sub(r)
}

// PctReleased is released as a percentage of committed on the line's measure.
func (l AgreementLine) PctReleased() decimal.Decimal {
	c, r := l.Measure()
	return Pct(r, c)
}

// Pct is used as a percentage of committed, rounded half-even to 2 places. A
// zero commitment with any release is 100.
func Pct(used, committed decimal.Decimal) decimal.Decimal {
	if committed.Sign() <= 0 {
		if used.Sign() > 0 {
			return decimal.NewFromInt(100)
		}
		return decimal.Zero
	}
	return used.Mul(decimal.NewFromInt(100)).DivRound(committed, 8).RoundBank(2)
}

// Draw takes d from the line. A HARD line refuses an overrun with PRC-AGR-003
// and is left unchanged; SOFT applies the draw and reports the overrun; NONE is
// unlimited. Both released figures grow so reports show quantity and value.
func (l *AgreementLine) Draw(d Draw, threshold decimal.Decimal) (DrawResult, error) {
	committed, released := l.Measure()
	inc := d.Amount
	if l.CommitmentType == CommitQuantity {
		inc = d.Qty
	}
	next := released.Add(inc)
	res := DrawResult{PctBefore: Pct(released, committed), PctAfter: Pct(next, committed)}
	if next.GreaterThan(committed) {
		switch l.MaxEnforced {
		case EnforceHard:
			return DrawResult{}, fail(CodeAgreementLimit, "Agreement line %d would be exceeded: released %s + %s > committed %s (HARD)",
				l.LineNo, released, inc, committed)
		case EnforceSoft:
			res.Overrun = true
		}
	}
	res.ThresholdCrossed = res.PctBefore.LessThan(threshold) && res.PctAfter.GreaterThanOrEqual(threshold)
	l.ReleasedQty = l.ReleasedQty.Add(d.Qty)
	l.ReleasedAmount = l.ReleasedAmount.Add(d.Amount)
	return res, nil
}

// Release is the PO line facts an agreement check needs.
type Release struct {
	LegalEntityID string
	VendorID      string
	Currency      string
	ProductID     string
	Unit          string
	DeliveryDate  Day
}

// CheckRelease verifies that a PO line may draw from line l of the agreement:
// the agreement is EFFECTIVE (PRC-AGR-007); it belongs to the PO's legal
// entity, vendor and currency (PRC-AGR-006); the line's delivery date is within
// validity (BR-17.10, PRC-AGR-005); and a QUANTITY or VALUE line is for the PO
// line's product, in the same unit for QUANTITY (PRC-AGR-008). CATEGORY_VALUE
// is not checked against a category because PO lines do not carry one yet.
func (a Agreement) CheckRelease(l AgreementLine, r Release) error {
	if a.Status != AgreementEffective {
		return fail(CodeAgreementInactive, "Agreement %s is %s; only EFFECTIVE agreements can be released against", a.Number, a.Status)
	}
	if a.LegalEntityID != r.LegalEntityID || a.VendorID != r.VendorID || a.Currency != r.Currency {
		return fail(CodeAgreementParty, "Agreement %s is for another legal entity, vendor or currency than the purchase order", a.Number)
	}
	if !a.Covers(r.DeliveryDate) {
		to := "open"
		if a.ValidTo != nil {
			to = a.ValidTo.String()
		}
		return fail(CodeAgreementWindow, "PO delivery date %s is outside agreement validity %s–%s", r.DeliveryDate, a.ValidFrom, to)
	}
	switch l.CommitmentType {
	case CommitQuantity, CommitValue:
		if l.ProductID != r.ProductID {
			return fail(CodeAgreementItem, "Agreement %s line %d is for another product", a.Number, l.LineNo)
		}
		if l.CommitmentType == CommitQuantity && l.Unit != r.Unit {
			return fail(CodeAgreementItem, "Agreement %s line %d is committed in %s, not %s", a.Number, l.LineNo, l.Unit, r.Unit)
		}
	}
	return nil
}

// Covers reports whether day falls within valid_from..valid_to (inclusive; an
// open valid_to never ends).
func (a Agreement) Covers(day Day) bool {
	return !day.Before(a.ValidFrom.Time) && (a.ValidTo == nil || !day.After(a.ValidTo.Time))
}

// NextStatus applies an agreement action: activate (DRAFT or ON_HOLD →
// EFFECTIVE), hold (EFFECTIVE → ON_HOLD) or close (EFFECTIVE or ON_HOLD →
// CLOSED). Anything else is PRC-AGR-STATE.
func NextStatus(current, action string) (string, error) {
	switch {
	case action == AgreementActivate && (current == AgreementDraft || current == AgreementOnHold):
		return AgreementEffective, nil
	case action == AgreementHold && current == AgreementEffective:
		return AgreementOnHold, nil
	case action == AgreementClose && (current == AgreementEffective || current == AgreementOnHold):
		return AgreementClosed, nil
	}
	return "", fail(CodeAgreementState, "an agreement that is %s cannot be %s", current, actionPast(action))
}

func actionPast(action string) string {
	switch action {
	case AgreementActivate:
		return "activated"
	case AgreementHold:
		return "put on hold"
	case AgreementClose:
		return "closed"
	}
	return action
}

// ValidateAgreementLine checks that the commitment type and its measure agree:
// QUANTITY needs a product, a unit and a positive quantity; VALUE a product and
// a positive amount; CATEGORY_VALUE a category and a positive amount;
// TOTAL_VALUE a positive amount. It returns the offending field and a message.
func ValidateAgreementLine(l AgreementLine) (field, msg string) {
	switch l.CommitmentType {
	case CommitQuantity:
		switch {
		case l.ProductID == "":
			return "productId", "is required for a QUANTITY commitment"
		case l.Unit == "":
			return "unit", "is required for a QUANTITY commitment"
		case !l.CommittedQty.IsPositive():
			return "committedQty", "must be positive for a QUANTITY commitment"
		}
	case CommitValue:
		switch {
		case l.ProductID == "":
			return "productId", "is required for a VALUE commitment"
		case !l.CommittedAmount.IsPositive():
			return "committedAmount", "must be positive for a VALUE commitment"
		}
	case CommitCategoryValue:
		switch {
		case l.Category == "":
			return "category", "is required for a CATEGORY_VALUE commitment"
		case !l.CommittedAmount.IsPositive():
			return "committedAmount", "must be positive for a CATEGORY_VALUE commitment"
		}
	case CommitTotalValue:
		if !l.CommittedAmount.IsPositive() {
			return "committedAmount", "must be positive for a TOTAL_VALUE commitment"
		}
	default:
		return "commitmentType", "must be QUANTITY, VALUE, CATEGORY_VALUE or TOTAL_VALUE"
	}
	return "", ""
}
