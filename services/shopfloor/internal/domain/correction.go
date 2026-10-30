package domain

import (
	"time"

	"github.com/shopspring/decimal"
)

// Catalog codes of the supervisor correction rules (README).
const (
	CodeRegistrationNotCorrectable = "SF-0127"
	CodeCorrectionInvalid          = "SF-0128"
	CodeRevisionNothingChanged     = "SF-0129"
)

// CorrectionRequest is a supervisor's edit of a registration (FR-25.8): new times
// and the reason. A nil time keeps the stored one.
type CorrectionRequest struct {
	StartedAt *time.Time
	StoppedAt *time.Time
	Reason    string
}

// StoredRegistration is the part of a registration the correction rules read.
type StoredRegistration struct {
	Kind      string
	Bundled   bool
	StartedAt time.Time
	StoppedAt *time.Time
}

// Correction is the planned result: the new times and the new hours (nil while open).
type Correction struct {
	StartedAt time.Time
	StoppedAt *time.Time
	Hours     *decimal.Decimal
}

// PlanCorrection checks a correction and returns the new times and hours. A bundled
// job registration cannot be corrected, because its hours are a share of the bundle
// split (BR-25.1) that was already published. Clock-in and clock-out are points in
// time and take no stop time. A closed registration lasts at most 24 hours, and a
// correction must change something.
func PlanCorrection(reg StoredRegistration, req CorrectionRequest) (Correction, error) {
	if req.Reason == "" {
		return Correction{}, &Error{Code: CodeCorrectionInvalid, Detail: "a reason is required"}
	}
	if reg.Bundled {
		return Correction{}, &Error{Code: CodeRegistrationNotCorrectable, Detail: "a bundled job registration cannot be corrected; its hours are a share of the bundle"}
	}
	out := Correction{StartedAt: reg.StartedAt, StoppedAt: reg.StoppedAt}
	if req.StartedAt != nil {
		out.StartedAt = *req.StartedAt
	}
	if req.StoppedAt != nil {
		out.StoppedAt = req.StoppedAt
	}
	if reg.Kind == KindClockIn || reg.Kind == KindClockOut {
		if req.StoppedAt != nil {
			return Correction{}, &Error{Code: CodeRegistrationNotCorrectable, Detail: "a " + reg.Kind + " has no stop time"}
		}
		if req.StartedAt == nil {
			return Correction{}, &Error{Code: CodeCorrectionInvalid, Detail: "startedAt is required"}
		}
	}
	if out.StoppedAt != nil {
		if out.StoppedAt.Before(out.StartedAt) {
			return Correction{}, &Error{Code: CodeCorrectionInvalid, Detail: "stoppedAt must not be before startedAt"}
		}
		if out.StoppedAt.Sub(out.StartedAt) > 24*time.Hour {
			return Correction{}, &Error{Code: CodeCorrectionInvalid, Detail: "a registration cannot exceed 24 hours"}
		}
		h := ElapsedHours(out.StartedAt, *out.StoppedAt)
		out.Hours = &h
	}
	sameStop := (reg.StoppedAt == nil && out.StoppedAt == nil) ||
		(reg.StoppedAt != nil && out.StoppedAt != nil && reg.StoppedAt.Equal(*out.StoppedAt))
	if out.StartedAt.Equal(reg.StartedAt) && sameStop {
		return Correction{}, &Error{Code: CodeCorrectionInvalid, Detail: "the correction does not change the registration"}
	}
	return out, nil
}

// EffectiveLine is a stored pay line of a worker-day that counts toward its pay
// (not yet reversed). ID is its pay line id.
type EffectiveLine struct {
	ID         string
	Type       string
	Hours      decimal.Decimal
	Multiplier decimal.Decimal
	Amount     decimal.Decimal
}

// Revision is the change an approved worker-day needs: the lines to reverse and the
// lines to add (BR-25.8, AC-25.8).
type Revision struct {
	Reverse []EffectiveLine
	Add     []PayLine
}

// Empty reports that the recalculated day pays exactly what the stored day pays.
func (r Revision) Empty() bool { return len(r.Reverse) == 0 && len(r.Add) == 0 }

// PlanRevision compares the effective lines of an approved day with the recalculated
// lines. Lines that are equal in type, hours, multiplier and amount stay; every other
// effective line is reversed and every other new line is added. The pay of the day
// after the revision is therefore the sum of the unchanged and the added lines, and
// the reversals cancel exactly what was approved and possibly exported.
func PlanRevision(effective []EffectiveLine, recalculated []PayLine) Revision {
	used := make([]bool, len(recalculated))
	var rev Revision
	for _, e := range effective {
		matched := false
		for i, n := range recalculated {
			if used[i] || n.Type != e.Type || !n.Hours.Equal(e.Hours) || !n.Multiplier.Equal(e.Multiplier) || !n.Amount.Equal(e.Amount) {
				continue
			}
			used[i], matched = true, true
			break
		}
		if !matched {
			rev.Reverse = append(rev.Reverse, e)
		}
	}
	for i, n := range recalculated {
		if !used[i] {
			rev.Add = append(rev.Add, n)
		}
	}
	return rev
}
