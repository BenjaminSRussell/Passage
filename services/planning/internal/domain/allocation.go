package domain

import (
	"regexp"
	"sort"
	"strconv"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
)

// Limits of an allocation key and of one allocation run (FR-22.8).
const (
	MaxAllocationLines   = 1000
	MaxAllocationPeriods = 60
)

var (
	allocationUom       = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,10}$`)
	maxAllocationFactor = decimal.NewFromInt(1000000)
	allocationHundred   = decimal.NewFromInt(100)
)

// AllocationLine is one item of an allocation key: its percentage share of the
// aggregate forecast and the inventory units per forecast unit (BR-22.10).
type AllocationLine struct {
	ItemID    string
	Share     decimal.Decimal
	UomFactor decimal.Decimal
}

// AllocationKeySpec is the setup of one allocation key (FR-22.8).
type AllocationKeySpec struct {
	Code        string
	Name        string
	ForecastUom string
	Lines       []AllocationLine
}

// Validate returns one message per invalid field, keyed by wire name. The shares
// must sum to exactly 100, each have at most 6 decimals, and each item may
// appear once; a factor is above 0 with at most 9 decimals.
func (k AllocationKeySpec) Validate() map[string]string {
	errs := map[string]string{}
	if !coverageGroupCode.MatchString(k.Code) {
		errs["code"] = "must be 2 to 20 characters of A-Z, 0-9, _ or -"
	}
	if n := len([]rune(k.Name)); n < 1 || n > 120 {
		errs["name"] = "must be 1 to 120 characters"
	}
	if !allocationUom.MatchString(k.ForecastUom) {
		errs["forecastUom"] = "must be 1 to 10 characters of letters, digits, . _ / or -"
	}
	if n := len(k.Lines); n == 0 || n > MaxAllocationLines {
		errs["lines"] = "must hold 1 to " + strconv.Itoa(MaxAllocationLines) + " items"
		return errs
	}
	seen := map[string]bool{}
	sum := decimal.Zero
	for i, l := range k.Lines {
		prefix := "lines[" + strconv.Itoa(i) + "]."
		if !ids.Valid(l.ItemID) {
			errs[prefix+"itemId"] = "must be a UUID"
		} else if seen[l.ItemID] {
			errs[prefix+"itemId"] = "repeats an item of this key"
		}
		seen[l.ItemID] = true
		if !l.Share.IsPositive() || l.Share.GreaterThan(allocationHundred) || l.Share.Exponent() < -6 {
			errs[prefix+"share"] = "must be above 0 and at most 100 with at most 6 decimals"
		}
		if !l.UomFactor.IsPositive() || l.UomFactor.GreaterThan(maxAllocationFactor) || l.UomFactor.Exponent() < -9 {
			errs[prefix+"uomFactor"] = "must be above 0 and at most 1000000 with at most 9 decimals"
		}
		sum = sum.Add(l.Share)
	}
	if _, bad := errs["lines"]; !bad && !sum.Equal(allocationHundred) {
		errs["lines"] = "shares must add up to 100; they add up to " + sum.String()
	}
	return errs
}

// AllocatedCell is the share of one item for one period: Qty is its part of the
// aggregate in the forecast unit and InventoryQty the same part converted to the
// item's inventory unit (BR-22.10), which is what the forecast cell stores.
type AllocatedCell struct {
	ItemID       string
	Qty          decimal.Decimal
	InventoryQty decimal.Decimal
}

// AllocateForecast splits an aggregate quantity of the forecast unit across the
// lines of a key and converts each part to the item's inventory unit. qty must be
// 0 or more with at most 6 decimals. The cells come back ordered by item id; ok is
// false when a converted quantity exceeds MaxForecastQty.
//
// Each part is the aggregate times the share cut down to 6 decimals; the cut-off
// remainder goes to the line with the largest share (ties to the lowest item id),
// so the parts add up to the aggregate exactly.
//
// The parts are then converted with the same rule per factor: for the lines that
// share a factor, the inventory quantities add up to the sum of their parts times
// the factor, rounded half away from zero to 6 decimals. Each part times the factor
// is cut down to 6 decimals and the group's remainder goes to its largest share
// (ties to the lowest item id). Rounding each part on its own would not hold: a
// third of 1 at a factor of 0.5 is 0.166667 three times, 0.500001 in all. A part at
// the factor 1 converts to itself, and with one factor for the whole key the
// inventory quantities add up to the aggregate times that factor.
func AllocateForecast(qty decimal.Decimal, lines []AllocationLine) (cells []AllocatedCell, ok bool) {
	ordered := append([]AllocationLine(nil), lines...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ItemID < ordered[j].ItemID })
	cells = make([]AllocatedCell, len(ordered))
	all := make([]int, len(ordered))
	exact := make([]decimal.Decimal, len(ordered))
	for i, l := range ordered {
		cells[i].ItemID = l.ItemID
		all[i] = i
		exact[i] = qty.Mul(l.Share).Shift(-2)
	}
	for i, v := range splitLargestRemainder(ordered, all, exact, qty) {
		cells[i].Qty = v
	}
	// One group per factor, in the order the factors first appear.
	groups := map[string][]int{}
	var factors []string
	for i, l := range ordered {
		f := l.UomFactor.String()
		if _, seen := groups[f]; !seen {
			factors = append(factors, f)
		}
		groups[f] = append(groups[f], i)
	}
	for _, f := range factors {
		members := groups[f]
		factor := ordered[members[0]].UomFactor
		parts := decimal.Zero
		converted := make([]decimal.Decimal, len(ordered))
		for _, i := range members {
			parts = parts.Add(cells[i].Qty)
			converted[i] = cells[i].Qty.Mul(factor)
		}
		for i, v := range splitLargestRemainder(ordered, members, converted, parts.Mul(factor).Round(6)) {
			cells[i].InventoryQty = v
		}
	}
	for _, c := range cells {
		if c.InventoryQty.GreaterThan(MaxForecastQty) {
			return nil, false
		}
	}
	return cells, true
}

// splitLargestRemainder cuts the exact value of each member line down to 6 decimals
// and adds what the cuts leave out of total to the member with the largest share,
// ties to the first. members are in item order; the values are 0 or more and total
// is at least their sum cut down to 6 decimals, so no value turns negative. The
// result maps each member to its value.
func splitLargestRemainder(lines []AllocationLine, members []int, exact []decimal.Decimal, total decimal.Decimal) map[int]decimal.Decimal {
	out := make(map[int]decimal.Decimal, len(members))
	sum := decimal.Zero
	largest := members[0]
	for _, i := range members {
		out[i] = exact[i].Truncate(6)
		sum = sum.Add(out[i])
		if lines[i].Share.GreaterThan(lines[largest].Share) {
			largest = i
		}
	}
	out[largest] = out[largest].Add(total.Sub(sum))
	return out
}
