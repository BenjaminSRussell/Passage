package domain

import (
	"errors"
	"fmt"
	"regexp"
)

// Cost object kinds (FR-38.6, FR-38.9). Internal orders are a cost object kind (PRD open question 3).
const (
	KindCostCenter    = "CostCenter"
	KindInternalOrder = "InternalOrder"
	KindProduct       = "Product"
	KindProject       = "Project"
)

// Cost element kinds: primary elements mirror main accounts, secondary elements carry allocations.
const (
	ElementPrimary   = "Primary"
	ElementSecondary = "Secondary"
)

var (
	codePattern   = regexp.MustCompile(`^[A-Z0-9_-]{2,40}$`)
	periodPattern = regexp.MustCompile(`^\d{4}-(0[1-9]|1[0-2])$`)
	// ErrNoPrimaryElement means a journal line's main account maps to no primary element.
	ErrNoPrimaryElement = errors.New("no primary cost element maps this main account")
)

// ValidCode reports whether code is an allowed cost object or element code.
func ValidCode(code string) bool { return codePattern.MatchString(code) }

// ValidPeriod reports whether p is a YYYY-MM period key.
func ValidPeriod(p string) bool { return periodPattern.MatchString(p) }

// ValidObjectKind reports whether k is a known cost object kind.
func ValidObjectKind(k string) bool {
	switch k {
	case KindCostCenter, KindInternalOrder, KindProduct, KindProject:
		return true
	}
	return false
}

// AccountRange is an inclusive main account range mapped to a primary element.
type AccountRange struct {
	ElementID string
	From      string
	To        string
}

// Overlaps reports whether two inclusive account ranges share an account.
// Accounts compare as text, which matches fixed-width account numbers.
func (a AccountRange) Overlaps(b AccountRange) bool {
	return a.From <= b.To && b.From <= a.To
}

// ValidateRange checks that a range is well formed (From <= To).
func ValidateRange(r AccountRange) error {
	if r.From == "" || r.To == "" {
		return errors.New("account range needs both bounds")
	}
	if r.From > r.To {
		return fmt.Errorf("account range %s..%s is reversed", r.From, r.To)
	}
	return nil
}

// MapPrimary returns the element of the range containing account. Ranges
// must not overlap (checked when elements are created), so at most one matches.
func MapPrimary(ranges []AccountRange, account string) (string, error) {
	for _, r := range ranges {
		if account >= r.From && account <= r.To {
			return r.ElementID, nil
		}
	}
	return "", ErrNoPrimaryElement
}
