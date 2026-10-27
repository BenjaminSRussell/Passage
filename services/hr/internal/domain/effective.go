// Package domain holds HR's pure rules: effective-dated ranges, the manager
// walk, absence accrual arithmetic, the FTE check and PII masking. It performs
// no I/O so every rule is testable without a database.
package domain

import (
	"fmt"
	"time"

	"github.com/shopspring/decimal"
)

// Error is a rule violation carrying a catalog code (HCM-*). The API maps it
// to an RFC 9457 problem with that code.
type Error struct {
	Code   string
	Detail string
}

func (e *Error) Error() string { return e.Code + ": " + e.Detail }

// Range is a half-open validity range [From, To). A nil To is open-ended.
type Range struct {
	From time.Time
	To   *time.Time
}

// Contains reports whether instant t lies inside the range.
func (r Range) Contains(t time.Time) bool {
	return !t.Before(r.From) && (r.To == nil || t.Before(*r.To))
}

// Overlaps reports whether two ranges share at least one instant.
func (r Range) Overlaps(o Range) bool {
	return (o.To == nil || r.From.Before(*o.To)) && (r.To == nil || o.From.Before(*r.To))
}

// AsOfDay is the instant a calendar date denotes: 00:00 UTC of that day.
// Tenant-local day boundaries are a later slice (see NOT-IMPLEMENTED.md).
func AsOfDay(d time.Time) time.Time {
	y, m, day := d.Date()
	return time.Date(y, m, day, 0, 0, 0, 0, time.UTC)
}

// CheckFTE enforces FR-42.3: the assignments on a position may not exceed its
// FTE in total unless the position allows overfill.
func CheckFTE(positionFTE decimal.Decimal, allowOverfill bool, existing, add decimal.Decimal) error {
	if allowOverfill {
		return nil
	}
	if existing.Add(add).GreaterThan(positionFTE) {
		return &Error{Code: "HCM-POS-001", Detail: fmt.Sprintf("Position FTE %s would be exceeded", positionFTE.StringFixed(2))}
	}
	return nil
}

// Employment statuses derived from the validity range (FR-42.2). ON_LEAVE
// needs leave-of-absence actions and arrives with personnel actions.
const (
	StatusPending    = "PENDING"
	StatusActive     = "ACTIVE"
	StatusTerminated = "TERMINATED"
)

// EmploymentStatus is PENDING before the range starts, ACTIVE inside it and
// TERMINATED from its exclusive end on.
func EmploymentStatus(r Range, at time.Time) string {
	switch {
	case at.Before(r.From):
		return StatusPending
	case r.Contains(at):
		return StatusActive
	default:
		return StatusTerminated
	}
}

// DayAfter is the exclusive range end for an inclusive last day: an employment
// whose termination date is D is still active on D and ends at D+1 00:00.
func DayAfter(d time.Time) time.Time {
	return AsOfDay(d).AddDate(0, 0, 1)
}
