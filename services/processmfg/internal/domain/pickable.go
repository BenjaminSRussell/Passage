package domain

import (
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/shopspring/decimal"
)

// Pick-time codes. PMF-0003 and PMF-0005 are in the PRD 4.24.18 catalog;
// PMF-0016 is added by this service for a batch whose disposition blocks it
// (FR-24.5, AC-24.10), which the catalog does not code.
const (
	CodeBatchExpired = "PMF-0003"
	CodePotencyRange = "PMF-0005"
	CodeNotPickable  = "PMF-0016"
)

// Batch dispositions (FR-24.5). DispositionAvailable is in customer.go.
const (
	DispositionOnHold     = "on-hold"
	DispositionRejected   = "rejected"
	DispositionQuarantine = "quarantine"
)

// ValidDisposition reports whether s is one of the four dispositions.
func ValidDisposition(s string) bool {
	switch s {
	case DispositionAvailable, DispositionOnHold, DispositionRejected, DispositionQuarantine:
		return true
	}
	return false
}

// ExpiryAttribute is the batch attribute that holds the expiry date (FR-24.6).
const ExpiryAttribute = "expiry"

// MinOverrideReason is the shortest audit reason, in characters, for consuming
// an expired batch (BR-24.10).
const MinOverrideReason = 10

// PotencyBand is the potency range a line accepts (FR-24.3), inclusive.
type PotencyBand struct {
	Attribute string
	Min, Max  decimal.Decimal
}

// PickLine is the line a batch is picked for. A nil Potency puts no potency
// constraint on the line.
type PickLine struct {
	PostingDate time.Time
	Potency     *PotencyBand
}

// ExpiryOverride is the consume-expired override (BR-24.10). DutyHeld is
// whether the caller holds the duty "Consume expired batch"; Reason is the
// audit reason.
type ExpiryOverride struct {
	DutyHeld bool
	Reason   string
}

// CheckPickable applies the pick-time rules to one batch in the order a
// picking list evaluates them: the disposition must be available (FR-24.5,
// AC-24.10, PMF-0016); an expiry before the posting date is refused with
// PMF-0003 (AC-24.4) unless the override duty is held with an audit reason of
// at least ten characters (BR-24.10); and recorded potency must lie inside the
// line's band, so a batch without potency is not pickable for a banded line
// (FR-24.3, PMF-0005). overridden is true when an expired batch passes on the
// override: BR-24.15 makes that an EBR exception.
func CheckPickable(b Batch, line PickLine, ov ExpiryOverride) (overridden bool, err error) {
	if b.Disposition != DispositionAvailable {
		return false, errorf(CodeNotPickable, "batch %s is %s; not pickable", b.ID, b.Disposition)
	}
	if exp := b.Values[ExpiryAttribute].Date; exp != nil && civil(*exp).Before(civil(line.PostingDate)) {
		if !ov.DutyHeld {
			return false, errorf(CodeBatchExpired, "batch %s expired on %s", b.ID, exp.Format("2006-01-02"))
		}
		if utf8.RuneCountInString(strings.TrimSpace(ov.Reason)) < MinOverrideReason {
			return false, errorf(CodeInvalid, "consuming an expired batch needs an audit reason of at least %d characters", MinOverrideReason)
		}
		overridden = true
	}
	if band := line.Potency; band != nil {
		p := b.Values[band.Attribute].Num
		if p == nil {
			return false, errorf(CodePotencyRange, "batch %s has no recorded %s; not pickable", b.ID, band.Attribute)
		}
		if !PotencyInRange(*p, band.Min, band.Max) {
			return false, errorf(CodePotencyRange, "batch %s %s %s%% is outside %s–%s%%; not pickable", b.ID, band.Attribute, p, band.Min, band.Max)
		}
	}
	return overridden, nil
}

// Picked is one batch of a picking proposal and the quantity taken from it.
type Picked struct {
	BatchID         string
	Qty             decimal.Decimal
	Expiry          *time.Time
	ExpiredOverride bool
}

// Excluded is a candidate batch left out of a proposal, with the code and text
// of the rule that excluded it.
type Excluded struct {
	BatchID string
	Code    string
	Detail  string
}

// PickProposal is the outcome of ProposePicking. Shortfall is the part of the
// need the pickable batches cannot cover (zero when covered).
type PickProposal struct {
	Picked    []Picked
	Excluded  []Excluded
	Shortfall decimal.Decimal
}

// ProposePicking builds the batch proposal for a line that needs qty: every
// candidate goes through CheckPickable, the pickable ones are taken first
// expiry first (FEFO, FR-24.6; batches without expiry last, then input order)
// until the need is met, and the others are listed with their code. A short
// override reason is a malformed request (COM-0001) rather than an exclusion.
func ProposePicking(candidates []Batch, line PickLine, need decimal.Decimal, ov ExpiryOverride) (PickProposal, error) {
	if !need.IsPositive() {
		return PickProposal{}, errorf(CodeInvalid, "the line quantity must be positive")
	}
	seen := make(map[string]bool, len(candidates))
	var ok []Batch
	override := map[string]bool{}
	out := PickProposal{Picked: []Picked{}, Excluded: []Excluded{}}
	for _, b := range candidates {
		if seen[b.ID] {
			return PickProposal{}, errorf(CodeInvalid, "batch %s listed twice", b.ID)
		}
		seen[b.ID] = true
		overridden, err := CheckPickable(b, line, ov)
		if err != nil {
			de, isDomain := err.(*Error)
			if !isDomain || de.Code == CodeInvalid {
				return PickProposal{}, err
			}
			out.Excluded = append(out.Excluded, Excluded{BatchID: b.ID, Code: de.Code, Detail: de.Detail})
			continue
		}
		override[b.ID] = overridden
		ok = append(ok, b)
	}
	sort.SliceStable(ok, func(i, j int) bool { return expiresBefore(ok[i], ok[j]) })
	left := need
	for _, b := range ok {
		if !left.IsPositive() {
			break
		}
		take := decimal.Min(left, b.Qty)
		if !take.IsPositive() {
			continue
		}
		out.Picked = append(out.Picked, Picked{BatchID: b.ID, Qty: take, Expiry: b.Values[ExpiryAttribute].Date, ExpiredOverride: override[b.ID]})
		left = left.Sub(take)
	}
	out.Shortfall = left
	return out, nil
}
