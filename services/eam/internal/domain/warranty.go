package domain

import (
	"errors"
	"time"

	"github.com/shopspring/decimal"
)

// ValidStartBasis reports whether s is a known warranty start basis (FR-44.17).
func ValidStartBasis(s string) bool { return s == "PURCHASE" || s == "INSTALL" }

// ErrWarrantyCoverage is returned when a warranty declares neither parts nor labor.
var ErrWarrantyCoverage = errors.New("warranty must cover parts, labor or both")

// WarrantyWindow is the part of a warranty that decides coverage (BR-44.14).
type WarrantyWindow struct {
	Status       string
	StartsOn     time.Time
	Months       int
	PartsCovered bool
	LaborCovered bool
	Exclusions   []string
}

// ValidateWarranty checks the coverage declaration of a warranty.
func ValidateWarranty(w WarrantyWindow) error {
	if w.Months < 1 || w.Months > 240 {
		return errors.New("warranty months must be 1-240")
	}
	if !w.PartsCovered && !w.LaborCovered {
		return ErrWarrantyCoverage
	}
	return nil
}

// WarrantyEnds returns the first instant after the warranty period.
func WarrantyEnds(start time.Time, months int) time.Time { return start.AddDate(0, months, 0) }

// Covers reports whether the warranty covers a work order of jobType at instant
// at (BR-44.14): ACTIVE, inside the period, job type not excluded, and at least
// one of parts or labor covered. An empty job type is never excluded.
func Covers(w WarrantyWindow, at time.Time, jobType string) bool {
	if w.Status != "ACTIVE" {
		return false
	}
	if at.Before(w.StartsOn) || !at.Before(WarrantyEnds(w.StartsOn, w.Months)) {
		return false
	}
	if jobType != "" {
		for _, ex := range w.Exclusions {
			if ex == jobType {
				return false
			}
		}
	}
	return w.PartsCovered || w.LaborCovered
}

// Claim is what a covered work order claims from the vendor at completion
// (BR-44.14): the parts cost when parts are covered and the labor cost when
// labor is covered. Covered lines still cost the work order.
type Claim struct {
	Parts decimal.Decimal
	Labor decimal.Decimal
}

// Total is the covered parts plus the covered labor.
func (c Claim) Total() decimal.Decimal { return c.Parts.Add(c.Labor) }

// ClaimFor returns the claimable amounts of a work order's parts and labor cost
// under a warranty's coverage flags, rounded to the minor unit.
func ClaimFor(partsCovered, laborCovered bool, parts, labor decimal.Decimal) Claim {
	var c Claim
	if partsCovered {
		c.Parts = Money(parts)
	}
	if laborCovered {
		c.Labor = Money(labor)
	}
	return c
}
