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

// Price search mode (PRD 5.26.13 price_search_mode, BR-26.1 step 5): the default of a new
// condition record's searchNext flag when the caller sends none.
const (
	SearchModeFirstMatch = "FIRST_MATCH"
	SearchModeNext       = "SEARCH_NEXT"
)

// ValidSearchMode reports whether s is a price search mode.
func ValidSearchMode(s string) bool { return s == SearchModeFirstMatch || s == SearchModeNext }

// MaxThresholdCurrencies bounds the small order threshold table of a legal entity.
const MaxThresholdCurrencies = 30

// ParseThresholds validates the small order threshold table of a legal entity (PRD 5.26.13
// small_order_threshold, FR-26.6): currency code to a decimal of at least 0 with at most six
// places. A currency in the table replaces the reference threshold of SmallOrderThreshold;
// 0 switches the surcharge off for that currency. A fault is keyed smallOrderThresholds[CUR].
func ParseThresholds(in map[string]string) (map[string]decimal.Decimal, map[string]string) {
	faults := map[string]string{}
	if len(in) > MaxThresholdCurrencies {
		faults["smallOrderThresholds"] = fmt.Sprintf("must hold at most %d currencies", MaxThresholdCurrencies)
		return nil, faults
	}
	out := make(map[string]decimal.Decimal, len(in))
	for cur, raw := range in {
		key := "smallOrderThresholds[" + cur + "]"
		if !validCurrencyCode(cur) {
			faults[key] = "currency must be three uppercase letters"
			continue
		}
		v, err := decimal.NewFromString(raw)
		switch {
		case err != nil || v.IsNegative():
			faults[key] = "must be a decimal of at least 0"
		case !v.Equal(v.Round(6)): // "200.5000000" is 200.5; trailing zeros are not places
			faults[key] = "must have at most six decimal places"
		case v.GreaterThan(decimal.NewFromInt(1_000_000_000)):
			faults[key] = "must not exceed 1000000000"
		default:
			out[cur] = v
		}
	}
	if len(faults) > 0 {
		return nil, faults
	}
	return out, nil
}

func validCurrencyCode(c string) bool {
	if len(c) != 3 {
		return false
	}
	for _, r := range c {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}

// CanonicalThresholds renders a validated table in its stored form ("200.50" and "200.5" are one
// value), so equal tables compare equal. A nil result is the reference table.
func CanonicalThresholds(in map[string]decimal.Decimal) map[string]string {
	out := make(map[string]string, len(in))
	for cur, v := range in {
		out[cur] = v.String()
	}
	return out
}

// ThresholdFor returns the small order threshold of a currency: the legal entity's table when
// it names the currency, else the reference threshold (SmallOrderThreshold).
func ThresholdFor(table map[string]string, currency string) decimal.Decimal {
	if raw, ok := table[currency]; ok {
		if v, err := decimal.NewFromString(raw); err == nil {
			return v
		}
	}
	return SmallOrderThreshold(currency)
}

// Campaign kill switch effect (PRD 5.26.13 campaign_kill_switch_effect, FR-26.10). Every
// replica that takes a kill or restore empties its own snapshots at once, so the next resolve
// there sees it. NEXT_RESOLVE leaves the other replicas to their snapshot lifetime;
// IMMEDIATE_CACHE_FLUSH has every replica empty its snapshots when it reads campaign.killed or
// campaign.restored.
const (
	KillEffectNextResolve = "NEXT_RESOLVE"
	KillEffectFlush       = "IMMEDIATE_CACHE_FLUSH"
)

// ValidKillEffect reports whether s is a campaign kill switch effect.
func ValidKillEffect(s string) bool { return s == KillEffectNextResolve || s == KillEffectFlush }

// Bounds of the per-tenant snapshot lifetime (PRD 5.26.13 cache_ttl_seconds).
const (
	MinCacheTTLSeconds = 10
	MaxCacheTTLSeconds = 600
)

// ValidCacheTTL reports whether n is a snapshot lifetime in seconds the tenant may set.
func ValidCacheTTL(n int) bool { return n >= MinCacheTTLSeconds && n <= MaxCacheTTLSeconds }
