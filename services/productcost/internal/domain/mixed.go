package domain

import (
	"fmt"
	"sort"

	"github.com/shopspring/decimal"
)

// AltStatusExcluded marks an alternative with a 0.00% ratio: it is reported
// but left out of the roll (BR-39.12).
const AltStatusExcluded = "Excluded"

// AltRef names one procurement alternative of a mixed-cost item: the catalog
// item rolled for it and its mixing ratio in percent (FR-39.14).
type AltRef struct {
	Source  string          `json:"source"`
	Percent decimal.Decimal `json:"percent"`
	ItemID  string          `json:"itemId"`
}

// AltRoll is the rolled standard of one alternative.
type AltRoll struct {
	Source      string          `json:"source"`
	Percent     decimal.Decimal `json:"percent"`
	ItemID      string          `json:"itemId"`
	Status      string          `json:"status"`
	Unit        decimal.Decimal `json:"unitCost"`
	Split       Split           `json:"split"`
	Diagnostics []Diagnostic    `json:"diagnostics,omitempty"`
}

// MixedRoll is the ratio-weighted standard of a mixed-cost item.
type MixedRoll struct {
	ItemID       string          `json:"itemId"`
	Status       string          `json:"status"`
	Unit         decimal.Decimal `json:"unitCost"`
	Split        Split           `json:"split"`
	Alternatives []AltRoll       `json:"alternatives"`
	Diagnostics  []Diagnostic    `json:"diagnostics,omitempty"`
}

// RollMixed costs an item whose supply mixes several alternatives (FR-39.14).
// Each alternative with a non-zero ratio is rolled with its own quantity
// structure and the item cost is the ratio-weighted split (AC-39.11). An
// alternative without a valid cost fails the item unless fallback applies.
func RollMixed(cat Catalog, itemID string, alts []AltRef, opts RollOptions) (MixedRoll, error) {
	if err := opts.Validate(); err != nil {
		return MixedRoll{}, err
	}
	percents := make([]decimal.Decimal, len(alts))
	for i, a := range alts {
		percents[i] = a.Percent
	}
	if err := CheckRatios(percents); err != nil {
		return MixedRoll{}, err
	}
	out := MixedRoll{ItemID: itemID, Status: StatusPending, Split: Split{}}
	mix := make([]Alternative, 0, len(alts))
	for _, a := range alts {
		ar := AltRoll{Source: a.Source, Percent: a.Percent, ItemID: a.ItemID, Status: AltStatusExcluded, Split: Split{}}
		if !a.Percent.IsZero() {
			r, ok := RollOne(cat, a.ItemID, opts)
			ar.Status = r.Status
			ar.Diagnostics = r.Diagnostics
			if !ok {
				ar.Status = StatusError
				ar.Diagnostics = append(ar.Diagnostics, errDiag(CodeMissingComponent, "alternative "+a.ItemID+" is not in the catalog", a.Source))
			}
			if ar.Status == StatusPending {
				ar.Split = r.Combined()
				ar.Unit = ar.Split.Total()
				mix = append(mix, Alternative{Source: a.Source, Percent: a.Percent, Split: ar.Split})
			} else {
				out.Status = StatusError
				out.Diagnostics = append(out.Diagnostics, ar.Diagnostics...)
			}
		}
		out.Alternatives = append(out.Alternatives, ar)
	}
	if out.Status == StatusError {
		return out, nil
	}
	// Excluded alternatives have a zero ratio and add nothing, so mix alone
	// carries the full 100.00% weight.
	split, err := MixedCost(mix)
	if err != nil {
		return out, err
	}
	out.Split = split
	out.Unit = split.Total()
	return out, nil
}

// CheckRatios validates mixing ratios: none is negative and together they
// total exactly 100.00 (BR-39.12).
func CheckRatios(percents []decimal.Decimal) error {
	total := decimal.Zero
	for _, p := range percents {
		if p.IsNegative() {
			return fmt.Errorf("%s: negative mixing ratio", CodeMixingRatioTotal)
		}
		total = total.Add(p)
	}
	if !total.Equal(decimal.NewFromInt(mixingRatioTotalWanted)) {
		return fmt.Errorf("%s: mixing ratios total %s", CodeMixingRatioTotal, total.StringFixed(2))
	}
	return nil
}

// mixingUnits is 100.00% expressed in hundredths of a percent.
const mixingUnits = 10000

// DeriveRatios turns receipt quantities per source over the look-back window
// into mixing ratios (derive-ratios, FR-39.14). Each ratio is floored to 0.01%
// and the shortfall goes to the sources with the largest fractional remainders
// (ties by source code), so the ratios total exactly 100.00.
func DeriveRatios(qty map[string]decimal.Decimal) (map[string]decimal.Decimal, error) {
	total := decimal.Zero
	for src, q := range qty {
		if q.IsNegative() {
			return nil, fmt.Errorf("negative receipt quantity for %s", src)
		}
		total = total.Add(q)
	}
	if !total.IsPositive() {
		return nil, fmt.Errorf("%s: no receipts in the look-back window", CodeMixingRatioTotal)
	}
	type share struct {
		src   string
		units int64
		frac  decimal.Decimal
	}
	keys := sortedKeys(qty)
	shares := make([]share, 0, len(keys))
	sum := int64(0)
	for _, src := range keys {
		raw := qty[src].Mul(decimal.NewFromInt(mixingUnits)).Div(total)
		fl := raw.Floor()
		shares = append(shares, share{src: src, units: fl.IntPart(), frac: raw.Sub(fl)})
		sum += fl.IntPart()
	}
	// Largest remainder first; the stable sort keeps source order on ties.
	sort.SliceStable(shares, func(i, j int) bool { return shares[i].frac.GreaterThan(shares[j].frac) })
	for i := range shares {
		if sum >= mixingUnits {
			break
		}
		shares[i].units++
		sum++
	}
	out := make(map[string]decimal.Decimal, len(shares))
	for _, s := range shares {
		out[s.src] = decimal.New(s.units, -2)
	}
	return out, nil
}
