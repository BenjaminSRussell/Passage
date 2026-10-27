package domain

import (
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/shopspring/decimal"
)

// Nonconformance sources (FR-33.7).
const (
	SourceCustomer   = "CUSTOMER"
	SourceVendor     = "VENDOR"
	SourceInternal   = "INTERNAL"
	SourceProduction = "PRODUCTION"
	SourceService    = "SERVICE"
)

// Nonconformance statuses (FR-33.7): NEW -> APPROVED -> IN_CORRECTION -> CLOSED, or NEW -> REFUSED.
const (
	NCRNew          = "NEW"
	NCRApproved     = "APPROVED"
	NCRRefused      = "REFUSED"
	NCRInCorrection = "IN_CORRECTION"
	NCRClosed       = "CLOSED"
)

// Nonconformance severities are NCRMinor, NCRMajor and NCRCritical (scorecard.go);
// they are not the inspection Severity of a sampling plan.

// ProblemUsageDecisionReject is the problem type of an NCR raised by a usage decision.
const ProblemUsageDecisionReject = "USAGE_DECISION_REJECT"

// Field limits shared by the API, the store and the web form.
const (
	MaxNCRDescription = 2000
	MaxStatusReason   = 500
)

var ncrTransitions = map[string][]string{
	NCRNew:          {NCRApproved, NCRRefused},
	NCRApproved:     {NCRInCorrection},
	NCRInCorrection: {NCRClosed},
}

// CanNCRTransition reports whether the nonconformance may move from -> to.
// REFUSED and CLOSED are final.
func CanNCRTransition(from, to string) bool { return slices.Contains(ncrTransitions[from], to) }

// ValidNCRStatus, ValidNCRSource and ValidNCRSeverity check list filters and input.
func ValidNCRStatus(s string) bool {
	return s == NCRNew || s == NCRApproved || s == NCRRefused || s == NCRInCorrection || s == NCRClosed
}

func ValidNCRSource(s string) bool {
	return s == SourceCustomer || s == SourceVendor || s == SourceInternal || s == SourceProduction || s == SourceService
}

func ValidNCRSeverity(s string) bool { return s == NCRMinor || s == NCRMajor || s == NCRCritical }

// Nonconformance is a nonconformance report (NCR, FR-33.7) and, once started, its
// 8D correction (FR-33.8). Version is the optimistic version of both: every
// change to the NCR or its correction moves it.
type Nonconformance struct {
	ID                string            `json:"id"`
	Source            string            `json:"source"`
	ItemID            string            `json:"itemId"`
	Dims              map[string]string `json:"dims"`
	Qty               decimal.Decimal   `json:"qty"`
	UOM               string            `json:"uom"`
	ProblemType       string            `json:"problemType"`
	Severity          string            `json:"severity"`
	Description       string            `json:"description"`
	QualityOrderID    string            `json:"qualityOrderId,omitempty"`
	VendorID          string            `json:"vendorId,omitempty"`
	CustomerID        string            `json:"customerId,omitempty"`
	ProductionOrderID string            `json:"productionOrderId,omitempty"`
	QuarantineOrderID string            `json:"quarantineOrderId,omitempty"`
	Status            string            `json:"status"`
	StatusReason      string            `json:"statusReason,omitempty"`
	CreatedBy         string            `json:"createdBy"`
	CreatedAt         time.Time         `json:"createdAt"`
	UpdatedAt         time.Time         `json:"updatedAt"`
	Version           int               `json:"version"`
	Correction        *Correction       `json:"correction,omitempty"`
}

// problemTypeCode is the shape of a problem type code (same as other setup codes).
var problemTypeCode = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{1,31}$`)

// ValidateNCR returns field errors for a new NCR. Link ids are checked as UUIDs by
// the API; this checks the business fields.
func ValidateNCR(n Nonconformance) map[string]string {
	fields := map[string]string{}
	if !ValidNCRSource(n.Source) {
		fields["source"] = "must be CUSTOMER, VENDOR, INTERNAL, PRODUCTION or SERVICE"
	}
	if !ValidNCRSeverity(n.Severity) {
		fields["severity"] = "must be MINOR, MAJOR or CRITICAL"
	}
	if !problemTypeCode.MatchString(n.ProblemType) {
		fields["problemType"] = "must be 2 to 32 characters of A-Z, 0-9, _ or -"
	}
	if !n.Qty.IsPositive() {
		fields["qty"] = "must be positive"
	} else if n.Qty.Exponent() < -6 || n.Qty.GreaterThanOrEqual(decimal.New(1, 12)) {
		fields["qty"] = "must have at most 12 integer digits and 6 decimals"
	}
	if n.UOM == "" || utf8.RuneCountInString(n.UOM) > 10 {
		fields["uom"] = "must be 1 to 10 characters"
	}
	if utf8.RuneCountInString(n.Description) > MaxNCRDescription {
		fields["description"] = "must be at most 2000 characters"
	}
	return fields
}

// NewNCR validates an NCR and returns it in status NEW at version 1.
func NewNCR(n Nonconformance, user string, now time.Time) (Nonconformance, error) {
	if fields := ValidateNCR(n); len(fields) > 0 {
		msgs := make([]string, 0, len(fields))
		for name, msg := range fields {
			msgs = append(msgs, name+" "+msg)
		}
		slices.Sort(msgs)
		return Nonconformance{}, invalid(strings.Join(msgs, "; "))
	}
	if n.Dims == nil {
		n.Dims = map[string]string{}
	}
	n.Status, n.StatusReason, n.Version, n.Correction = NCRNew, "", 1, nil
	n.CreatedBy, n.CreatedAt, n.UpdatedAt = user, now.UTC(), now.UTC()
	return n, nil
}

// NCRFromDecision is the NCR a usage decision raises when it rejects quantity
// (FR-33.6, auto_ncr_on_reject). The quantity is every non-accepting leg (reject,
// rework, return to vendor, scrap). The source follows the trigger: a purchase
// receipt or source inspection is a vendor problem, a production RAF a production
// problem, everything else internal. ok is false when nothing was rejected.
func NCRFromDecision(o Order, d Decision, now time.Time) (Nonconformance, bool) {
	if !d.Split.Reject.IsPositive() {
		return Nonconformance{}, false
	}
	source := SourceInternal
	switch o.Trigger {
	case TriggerPurchaseReceipt, TriggerSourceInspect:
		source = SourceVendor
	case TriggerProductionRAF:
		source = SourceProduction
	}
	var legs []string
	for _, l := range d.Lines {
		if l.Decision != DecisionAccept && l.Decision != DecisionAcceptWithDeviation {
			legs = append(legs, l.Decision+" "+l.Quantity.String())
		}
	}
	n := Nonconformance{
		Source: source, ItemID: o.ItemID, Dims: o.Dims, Qty: d.Split.Reject, UOM: o.UOM,
		ProblemType: ProblemUsageDecisionReject, Severity: NCRMajor, QualityOrderID: o.ID,
		Description: "Raised by the usage decision on quality order " + o.ID + ": " + strings.Join(legs, ", ") + ".",
	}
	if o.SourceRef.Type == "production_order" && o.Trigger == TriggerProductionRAF {
		n.ProductionOrderID = o.SourceRef.ID
	}
	out, err := NewNCR(n, d.DecidedBy, now)
	return out, err == nil
}

func statusError(n Nonconformance, to string) error {
	return fail(CodeNCRStatus, "a nonconformance in "+n.Status+" cannot move to "+to)
}

// Approve moves a NEW NCR to APPROVED.
func (n Nonconformance) Approve(now time.Time) (Nonconformance, error) {
	if !CanNCRTransition(n.Status, NCRApproved) {
		return n, statusError(n, NCRApproved)
	}
	n.Status, n.UpdatedAt = NCRApproved, now.UTC()
	return n, nil
}

// Refuse moves a NEW NCR to REFUSED (final) with a reason.
func (n Nonconformance) Refuse(reason string, now time.Time) (Nonconformance, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" || utf8.RuneCountInString(reason) > MaxStatusReason {
		return n, invalid("reason is required and at most 500 characters")
	}
	if !CanNCRTransition(n.Status, NCRRefused) {
		return n, statusError(n, NCRRefused)
	}
	n.Status, n.StatusReason, n.UpdatedAt = NCRRefused, reason, now.UTC()
	return n, nil
}

// StartCorrection opens the 8D correction of an APPROVED NCR and moves it to
// IN_CORRECTION (FR-33.8).
func (n Nonconformance) StartCorrection(user string, effectivenessDays int, now time.Time) (Nonconformance, error) {
	if !CanNCRTransition(n.Status, NCRInCorrection) {
		return n, statusError(n, NCRInCorrection)
	}
	c, err := NewCorrection(user, effectivenessDays, now)
	if err != nil {
		return n, err
	}
	n.Status, n.Correction, n.UpdatedAt = NCRInCorrection, &c, now.UTC()
	return n, nil
}

// CompleteStep completes the current 8D step of an NCR in correction.
func (n Nonconformance) CompleteStep(step int, notes, user string, now time.Time) (Nonconformance, error) {
	if n.Status != NCRInCorrection || n.Correction == nil {
		return n, fail(CodeCAPAStep, "the nonconformance has no open correction")
	}
	c, err := n.Correction.CompleteStep(step, notes, user, now)
	if err != nil {
		return n, err
	}
	n.Correction, n.UpdatedAt = &c, now.UTC()
	return n, nil
}

// CheckEffectiveness records the effectiveness check. EFFECTIVE closes the
// correction and the NCR; NOT_EFFECTIVE reopens the 8D at D4.
func (n Nonconformance) CheckEffectiveness(result, note, user string, now time.Time) (Nonconformance, error) {
	if n.Status != NCRInCorrection || n.Correction == nil {
		return n, fail(CodeCAPAStep, "the nonconformance has no open correction")
	}
	c, err := n.Correction.CheckEffectiveness(result, note, user, now)
	if err != nil {
		return n, err
	}
	n.Correction, n.UpdatedAt = &c, now.UTC()
	if c.Status == CAPAClosed {
		n.Status = NCRClosed
	}
	return n, nil
}
