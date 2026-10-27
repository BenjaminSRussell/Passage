package domain

import (
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// Basis labels a rate card (BR-34.2). Lane lookup is shared by every basis; the
// basis takes part in the overlap rule (BR-34.10).
type Basis string

// Rate-card bases from the data model (§6.34.4).
const (
	BasisLane         Basis = "LANE"
	BasisZone         Basis = "ZONE"
	BasisDistance     Basis = "DISTANCE"
	BasisWeightBreak  Basis = "WEIGHT_BREAK"
	BasisPerPallet    Basis = "PER_PALLET"
	BasisPerContainer Basis = "PER_CONTAINER"
	BasisFlat         Basis = "FLAT"
)

// Units a rate row can be priced in. The unit selects the quantity that the
// row's break band is compared against.
const (
	UnitKG        = "KG"
	UnitPallet    = "PALLET"
	UnitContainer = "CONTAINER"
	UnitShipment  = "SHIPMENT"
	UnitKM        = "KM"
	UnitMile      = "MILE"
)

// kmPerMile converts the shipment distance for rows priced per mile.
var kmPerMile = decimal.RequireFromString("1.609344")

// Quote statuses.
const (
	QuoteOK              = "OK"
	QuoteNoLaneRate      = "NO_LANE_RATE"
	QuoteNoValidRateCard = "NO_VALID_RATE_CARD"
)

// DefaultDimDivisor is the parcel dimensional divisor in cm³ per kg (BR-34.2,
// dim_factor_parcel). A card that sets no divisor uses this value.
const DefaultDimDivisor = 5000

var hundred = decimal.NewFromInt(100)

// RateRow prices a lane or zone pair within a break band.
type RateRow struct {
	Origin      string           `json:"origin"`
	Destination string           `json:"destination"`
	BreakFrom   decimal.Decimal  `json:"breakFrom"`
	BreakTo     *decimal.Decimal `json:"breakTo,omitempty"`
	Rate        decimal.Decimal  `json:"rate"`
	Unit        string           `json:"unit"`
}

// Accessorial is a surcharge priced flat or as a percentage of the base charge.
// A non-zero Pct wins over Flat.
type Accessorial struct {
	Code string          `json:"code"`
	Flat decimal.Decimal `json:"flat"`
	Pct  decimal.Decimal `json:"pct"`
}

// RateCard is an effective-dated price list for one carrier service and basis.
// ValidTo is inclusive; nil means open-ended.
type RateCard struct {
	ID            string          `json:"id"`
	CarrierID     string          `json:"carrierId"`
	ServiceID     string          `json:"serviceId"`
	Basis         Basis           `json:"basis"`
	Currency      string          `json:"currency"`
	ValidFrom     time.Time       `json:"validFrom"`
	ValidTo       *time.Time      `json:"validTo,omitempty"`
	MinimumCharge decimal.Decimal `json:"minimumCharge"`
	DimDivisor    int             `json:"dimDivisor"`
	FuelPct       decimal.Decimal `json:"fuelPct"`
	Rows          []RateRow       `json:"rows"`
	Accessorials  []Accessorial   `json:"accessorials"`
}

// Shipment is the input to rating. Dates are day-granular.
type Shipment struct {
	OriginZone      string
	DestinationZone string
	WeightKg        decimal.Decimal
	VolumeM3        decimal.Decimal
	Pallets         int
	// DistanceKm prices rows in KM or MILE (DISTANCE basis: miles × rate).
	DistanceKm   decimal.Decimal
	ShipDate     time.Time
	Accessorials []string
}

// Step is one line of the explain trace.
type Step struct {
	Name   string          `json:"step"`
	Input  string          `json:"input"`
	Amount decimal.Decimal `json:"amount"`
}

// Charge is one priced line of a quote.
type Charge struct {
	Code   string          `json:"code"`
	Amount decimal.Decimal `json:"amount"`
}

// Quote is the rated result for one carrier service.
type Quote struct {
	CarrierID string          `json:"carrierId"`
	ServiceID string          `json:"serviceId"`
	Currency  string          `json:"currency"`
	Status    string          `json:"status"`
	Total     decimal.Decimal `json:"total"`
	Charges   []Charge        `json:"charges"`
	Trace     []Step          `json:"trace"`
}

// MarshalJSON writes the validity as calendar dates (YYYY-MM-DD) and the rows
// and accessorials as arrays, never null.
func (c RateCard) MarshalJSON() ([]byte, error) {
	type plain RateCard
	out := struct {
		plain
		ValidFrom string  `json:"validFrom"`
		ValidTo   *string `json:"validTo"`
	}{plain: plain(c), ValidFrom: dateOnly(c.ValidFrom).Format(time.DateOnly)}
	if c.ValidTo != nil {
		to := dateOnly(*c.ValidTo).Format(time.DateOnly)
		out.ValidTo = &to
	}
	if out.Rows == nil {
		out.Rows = []RateRow{}
	}
	if out.Accessorials == nil {
		out.Accessorials = []Accessorial{}
	}
	return json.Marshal(out)
}

// Validate reports field errors for a card that is about to be saved.
func (c RateCard) Validate() map[string]string {
	f := map[string]string{}
	if !slices.Contains([]Basis{BasisLane, BasisZone, BasisDistance, BasisWeightBreak, BasisPerPallet, BasisPerContainer, BasisFlat}, c.Basis) {
		f["basis"] = "must be LANE, ZONE, DISTANCE, WEIGHT_BREAK, PER_PALLET, PER_CONTAINER or FLAT"
	}
	if len(c.Currency) != 3 || strings.ToUpper(c.Currency) != c.Currency {
		f["currency"] = "must be an ISO 4217 alpha-3 code"
	}
	if c.ValidTo != nil && dateOnly(*c.ValidTo).Before(dateOnly(c.ValidFrom)) {
		f["validTo"] = "must not be before validFrom"
	}
	if c.DimDivisor <= 0 {
		f["dimDivisor"] = "must be a positive cm³ per kg divisor"
	}
	if c.MinimumCharge.IsNegative() || c.FuelPct.IsNegative() {
		f["amount"] = "minimumCharge and fuelPct must not be negative"
	}
	if len(c.Rows) == 0 {
		f["rows"] = "at least one rate row is required"
	}
	for i, r := range c.Rows {
		key := fmt.Sprintf("rows[%d]", i)
		switch {
		case r.Origin == "" || r.Destination == "":
			f[key] = "origin and destination are required; use * for any"
		case r.Rate.IsNegative() || r.BreakFrom.IsNegative():
			f[key] = "rate and breakFrom must not be negative"
		case r.BreakTo != nil && r.BreakTo.LessThanOrEqual(r.BreakFrom):
			f[key] = "breakTo must be greater than breakFrom"
		case !slices.Contains([]string{UnitKG, UnitPallet, UnitContainer, UnitShipment, UnitKM, UnitMile}, r.Unit):
			f[key] = "unit must be KG, PALLET, CONTAINER, SHIPMENT, KM or MILE"
		}
	}
	for i, a := range c.Accessorials {
		if a.Code == "" || a.Flat.IsNegative() || a.Pct.IsNegative() {
			f[fmt.Sprintf("accessorials[%d]", i)] = "code is required and amounts must not be negative"
		}
	}
	return f
}

// ValidOn reports whether the card applies on day (BR-34.10).
func (c RateCard) ValidOn(day time.Time) bool {
	d := dateOnly(day)
	if d.Before(dateOnly(c.ValidFrom)) {
		return false
	}
	return c.ValidTo == nil || !d.After(dateOnly(*c.ValidTo))
}

// Overlaps reports whether two cards for the same carrier, service and basis
// share a day. Such cards are rejected on save (BR-34.10, TMS-0102).
func Overlaps(a, b RateCard) bool {
	if a.CarrierID != b.CarrierID || a.ServiceID != b.ServiceID || a.Basis != b.Basis {
		return false
	}
	return !dateOnly(a.ValidFrom).After(endOf(b)) && !dateOnly(b.ValidFrom).After(endOf(a))
}

// Chargeable returns the greater of actual weight and dimensional weight, in kg,
// rounded to two places. Dimensional weight is m³ converted to cm³ divided by the
// card divisor (BR-34.2).
func Chargeable(s Shipment, divisor int) decimal.Decimal {
	if divisor <= 0 {
		divisor = DefaultDimDivisor
	}
	dim := s.VolumeM3.Mul(decimal.New(1, 6)).Div(decimal.NewFromInt(int64(divisor)))
	return decimal.Max(s.WeightKg, dim).Round(2)
}

// Rate prices one shipment against one card. Every charge is rounded to cents
// half-up for non-negative amounts, and the total is the sum of the rounded
// charges. The same input always yields the same quote.
func Rate(c RateCard, s Shipment) Quote {
	q := Quote{CarrierID: c.CarrierID, ServiceID: c.ServiceID, Currency: c.Currency, Status: QuoteOK, Total: decimal.Zero,
		Charges: []Charge{}, Trace: []Step{}}
	if !c.ValidOn(s.ShipDate) {
		q.Status = QuoteNoValidRateCard
		q.step("rate_card", QuoteNoValidRateCard+": no card valid on "+dateOnly(s.ShipDate).Format("2006-01-02"), decimal.Zero)
		return q
	}
	cw := Chargeable(s, c.DimDivisor)
	q.step("chargeable_weight", fmt.Sprintf("max(actual %s kg, dimensional)", s.WeightKg.String()), cw)

	row, qty, ok := lookupRow(c.Rows, s, cw)
	if !ok {
		q.Status = QuoteNoLaneRate
		q.step("lane_lookup", s.OriginZone+" to "+s.DestinationZone+": no row in break band", decimal.Zero)
		return q
	}
	base := row.Rate.Mul(qty).Round(2)
	q.step("base", fmt.Sprintf("%s per %s × %s", row.Rate.String(), row.Unit, qty.String()), base)
	if c.Basis == BasisWeightBreak {
		base = q.weightBreakDeficit(c.Rows, row, base)
	}
	if base.LessThan(c.MinimumCharge) {
		base = c.MinimumCharge.Round(2)
		q.step("minimum", "minimum charge "+c.MinimumCharge.String()+" applied", base)
	} else {
		q.step("minimum", "minimum charge "+c.MinimumCharge.String()+" not reached", base)
	}
	q.add("BASE", base)

	for _, code := range s.Accessorials {
		acc, found := findAccessorial(c.Accessorials, code)
		if !found {
			q.step("accessorial", code+": not on rate card", decimal.Zero)
			continue
		}
		if acc.Pct.IsPositive() {
			q.add("ACC:"+acc.Code, percentOf(base, acc.Pct, "accessorial "+acc.Code, &q))
		} else {
			q.add("ACC:"+acc.Code, acc.Flat.Round(2))
			q.step("accessorial", "flat "+acc.Flat.String(), acc.Flat.Round(2))
		}
	}
	if c.FuelPct.IsPositive() {
		q.add("FUEL", percentOf(base, c.FuelPct, "fuel surcharge", &q))
	}
	return q
}

// weightBreakDeficit applies the next-break deficit rule (BR-34.2): when
// rating the shipment as if it weighed the next break's lower bound at the next
// break's rate is cheaper, that lower charge applies. The next break is the
// row for the same lane and unit whose band starts where the chosen band ends.
func (q *Quote) weightBreakDeficit(rows []RateRow, chosen RateRow, base decimal.Decimal) decimal.Decimal {
	if chosen.Unit != UnitKG || chosen.BreakTo == nil {
		return base
	}
	for _, r := range rows {
		if r.Origin != chosen.Origin || r.Destination != chosen.Destination || r.Unit != chosen.Unit || !r.BreakFrom.Equal(*chosen.BreakTo) {
			continue
		}
		deficit := r.Rate.Mul(r.BreakFrom).Round(2)
		if deficit.LessThan(base) {
			q.step("weight_break", fmt.Sprintf("next break %s kg at %s is lower; deficit weight charged", r.BreakFrom.String(), r.Rate.String()), deficit)
			return deficit
		}
		q.step("weight_break", fmt.Sprintf("next break %s kg at %s is not lower", r.BreakFrom.String(), r.Rate.String()), base)
		return base
	}
	return base
}

// ShopRates rates a shipment across carrier services (BR-34.2): for each
// (carrier, service) only the cards valid on the ship date are rated, one per
// basis since valid cards of a basis never overlap (BR-34.10). A carrier service
// with cards but none valid on the ship date yields one NO_VALID_RATE_CARD quote.
// Results are ranked OK first by total (then carrier and service id), then the
// rest in carrier and service order.
func ShopRates(cards []RateCard, s Shipment) []Quote {
	type key struct{ carrier, service string }
	var order []key
	groups := map[key][]RateCard{}
	for _, c := range cards {
		k := key{c.CarrierID, c.ServiceID}
		if _, seen := groups[k]; !seen {
			order = append(order, k)
		}
		groups[k] = append(groups[k], c)
	}
	quotes := make([]Quote, 0, len(order))
	for _, k := range order {
		rated := false
		for _, c := range groups[k] {
			if c.ValidOn(s.ShipDate) {
				quotes = append(quotes, Rate(c, s))
				rated = true
			}
		}
		if !rated {
			quotes = append(quotes, Rate(groups[k][0], s))
		}
	}
	slices.SortStableFunc(quotes, func(a, b Quote) int {
		aOK, bOK := a.Status == QuoteOK, b.Status == QuoteOK
		if aOK != bOK {
			if aOK {
				return -1
			}
			return 1
		}
		if c := a.Total.Cmp(b.Total); c != 0 {
			return c
		}
		if c := strings.Compare(a.CarrierID, b.CarrierID); c != 0 {
			return c
		}
		return cmp.Compare(a.ServiceID, b.ServiceID)
	})
	return quotes
}

func (q *Quote) step(name, input string, amount decimal.Decimal) {
	q.Trace = append(q.Trace, Step{Name: name, Input: input, Amount: amount})
}

func (q *Quote) add(code string, amount decimal.Decimal) {
	q.Charges = append(q.Charges, Charge{Code: code, Amount: amount})
	q.Total = q.Total.Add(amount)
}

func percentOf(base, pct decimal.Decimal, label string, q *Quote) decimal.Decimal {
	amount := base.Mul(pct).Div(hundred).Round(2)
	q.step(label, pct.String()+"% of base", amount)
	return amount
}

func quantity(unit string, s Shipment, cw decimal.Decimal) decimal.Decimal {
	switch unit {
	case UnitKG:
		return cw
	case UnitPallet:
		return decimal.NewFromInt(int64(s.Pallets))
	case UnitKM:
		return s.DistanceKm
	case UnitMile:
		return s.DistanceKm.Div(kmPerMile).Round(2)
	default:
		return decimal.NewFromInt(1)
	}
}

// lookupRow picks the matching row with the most exact zone matches. A row
// matches when its origin and destination equal the shipment's (or are *) and the
// unit quantity falls inside [breakFrom, breakTo).
func lookupRow(rows []RateRow, s Shipment, cw decimal.Decimal) (RateRow, decimal.Decimal, bool) {
	best, bestScore := -1, -1
	var bestQty decimal.Decimal
	for i, r := range rows {
		if (r.Origin != "*" && r.Origin != s.OriginZone) || (r.Destination != "*" && r.Destination != s.DestinationZone) {
			continue
		}
		qty := quantity(r.Unit, s, cw)
		if qty.LessThan(r.BreakFrom) || (r.BreakTo != nil && !qty.LessThan(*r.BreakTo)) {
			continue
		}
		score := 0
		if r.Origin != "*" {
			score++
		}
		if r.Destination != "*" {
			score++
		}
		if score > bestScore {
			best, bestScore, bestQty = i, score, qty
		}
	}
	if best < 0 {
		return RateRow{}, decimal.Zero, false
	}
	return rows[best], bestQty, true
}

func findAccessorial(list []Accessorial, code string) (Accessorial, bool) {
	for _, a := range list {
		if a.Code == code {
			return a, true
		}
	}
	return Accessorial{}, false
}

func dateOnly(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func endOf(c RateCard) time.Time {
	if c.ValidTo == nil {
		return time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC)
	}
	return dateOnly(*c.ValidTo)
}
