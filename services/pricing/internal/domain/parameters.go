package domain

import (
	"fmt"

	"github.com/shopspring/decimal"
)

// Pricing date basis (PRD 5.26.13 pricing_date_basis, BR-26.2): which date of the document
// is the pricing date when the caller does not name one.
const (
	DateBasisShip     = "SHIP_DATE"
	DateBasisOrder    = "ORDER_DATE"
	DateBasisDelivery = "DELIVERY_DATE"
)

// Total discount basis (PRD 5.26.13 total_discount_basis): the amount TOTAL_DISC applies to.
// AFTER_LINE_DISC is the sum of the line nets, GROSS the sum of the line amounts at the PRICE
// step's unit price, before any line or multiline discount.
const (
	TotalBasisAfterLineDisc = "AFTER_LINE_DISC"
	TotalBasisGross         = "GROSS"
)

// MaxSmartBands bounds the smart rounding table of a legal entity.
const MaxSmartBands = 10

// ValidDateBasis reports whether s is a pricing date basis.
func ValidDateBasis(s string) bool {
	return s == DateBasisShip || s == DateBasisOrder || s == DateBasisDelivery
}

// ValidTotalBasis reports whether s is a total discount basis.
func ValidTotalBasis(s string) bool { return s == TotalBasisAfterLineDisc || s == TotalBasisGross }

// BandSpec is a smart rounding band as stored and sent (smart_rounding_bands): amounts below
// UpTo round up to the next value ending in Ending; the last band has no UpTo.
type BandSpec struct {
	UpTo   string `json:"upTo,omitempty"`
	Ending string `json:"ending"`
}

// DefaultBandSpecs is the PRD default table: 0-10 ends .99, 10-100 ends .95, 100 and up whole.
func DefaultBandSpecs() []BandSpec {
	return []BandSpec{{UpTo: "10", Ending: "0.99"}, {UpTo: "100", Ending: "0.95"}, {Ending: "0"}}
}

// ParseBands validates a smart rounding table and returns the engine bands. The table is
// ordered: UpTo strictly increases, only the last band is unbounded, and each ending is a
// decimal from 0 up to (not including) 1 with at most four places. A fault is keyed
// smartRoundingBands[i].field.
func ParseBands(specs []BandSpec) ([]SmartBand, map[string]string) {
	faults := map[string]string{}
	if len(specs) == 0 || len(specs) > MaxSmartBands {
		faults["smartRoundingBands"] = fmt.Sprintf("must hold 1 to %d bands", MaxSmartBands)
		return nil, faults
	}
	bands := make([]SmartBand, len(specs))
	prev := decimal.Zero
	for i, s := range specs {
		key := fmt.Sprintf("smartRoundingBands[%d]", i)
		e, err := decimal.NewFromString(s.Ending)
		switch {
		case err != nil || e.IsNegative() || !e.LessThan(decimal.NewFromInt(1)) || e.Exponent() < -4:
			faults[key+".ending"] = "must be a decimal from 0 up to 1 with at most four places"
		default:
			bands[i].Ending = e
		}
		last := i == len(specs)-1
		if last {
			if s.UpTo != "" {
				faults[key+".upTo"] = "must be empty on the last band"
			}
			bands[i].Unbounded = true
			continue
		}
		u, err := decimal.NewFromString(s.UpTo)
		switch {
		case err != nil || !u.IsPositive():
			faults[key+".upTo"] = "must be a decimal greater than 0"
		case !u.GreaterThan(prev):
			faults[key+".upTo"] = "must be greater than the band before"
		default:
			bands[i].UpTo, prev = u, u
		}
	}
	if len(faults) > 0 {
		return nil, faults
	}
	return bands, nil
}

// BandSpecs renders engine bands in their canonical form ("0.50" becomes "0.5"), the form
// stored and announced, so equal tables compare equal.
func BandSpecs(bands []SmartBand) []BandSpec {
	out := make([]BandSpec, len(bands))
	for i, b := range bands {
		out[i].Ending = b.Ending.String()
		if !b.Unbounded {
			out[i].UpTo = b.UpTo.String()
		}
	}
	return out
}
