package domain

import (
	"errors"
	"sort"

	"github.com/shopspring/decimal"
)

// Buffer zones of a DDMRP decoupling point (FR-22.16, BR-22.11).
const (
	ZoneRed    = "red"
	ZoneYellow = "yellow"
	ZoneGreen  = "green"
)

// ErrInvalidBuffer is returned for negative or inconsistent buffer parameters.
var ErrInvalidBuffer = errors.New("planning: invalid buffer parameters")

// BufferProfile holds the factors of a buffer profile (FR-22.16).
type BufferProfile struct {
	LeadTimeFactor    decimal.Decimal // LTF, usually 0..1
	VariabilityFactor decimal.Decimal // VF, usually 0..1
	OrderCycleDays    int
	MinOrderQty       decimal.Decimal
	SpikeThresholdPct decimal.Decimal // percent of the red zone, default 50
}

// BufferZones are the zone sizes and the tops of the three zones.
type BufferZones struct {
	RedBase, RedSafety, Yellow, Green decimal.Decimal
	TopOfRed, TopOfYellow, TopOfGreen decimal.Decimal
}

// DayDemand is demand (or history) on a day index; negative days are past due.
type DayDemand struct {
	Day int
	Qty decimal.Decimal
}

// BufferInput is the state of one buffered item at one site.
type BufferInput struct {
	Profile          BufferProfile
	ADU              decimal.Decimal // average daily usage before the adjustment factor
	DLT              decimal.Decimal // decoupled lead time in days
	DAF              decimal.Decimal // dynamic adjustment factor, 1 when none
	OnHand           decimal.Decimal
	OpenSupply       decimal.Decimal // open supply inside the DLT
	Demand           []DayDemand     // candidates for qualified demand
	SpikeHorizonDays int             // spikes are counted from day 1 to this day
	OrderMultiple    decimal.Decimal // order multiple from the default order settings; 0 when none
}

// BufferStatus is the evaluated buffer: zones, net-flow position and the order it implies.
type BufferStatus struct {
	Zones           BufferZones
	AdjustedADU     decimal.Decimal
	QualifiedDemand decimal.Decimal
	NetFlow         decimal.Decimal
	PlannedQty      decimal.Decimal
	Priority        decimal.Decimal // NFP / TOG, rounded to 4 places
	Zone            string
}

// AverageDailyUsage is the mean daily demand over a window of pastDays days
// before day 0 (days -pastDays..-1) and futureDays days from day 0 (days
// 0..futureDays-1). It divides by the window length (BR-22.11 ADU window).
func AverageDailyUsage(history []DayDemand, pastDays, futureDays int) (decimal.Decimal, error) {
	if pastDays < 0 || futureDays < 0 || pastDays+futureDays == 0 {
		return decimal.Zero, ErrInvalidBuffer
	}
	sum := decimal.Zero
	for _, h := range history {
		if h.Day >= -pastDays && h.Day < futureDays {
			sum = sum.Add(h.Qty)
		}
	}
	return sum.Div(decimal.NewFromInt(int64(pastDays + futureDays))), nil
}

// ComputeZones applies the BR-22.11 zone formulas to an already adjusted ADU:
//
//	red base = ADU x DLT x LTF, red safety = red base x VF, yellow = ADU x DLT,
//	green = max(ADU x DLT x LTF, MOQ, ADU x order cycle),
//	TOR = red base + red safety, TOY = TOR + yellow, TOG = TOY + green.
//
// The tops are non-decreasing; they are strictly increasing when the item has usage.
func ComputeZones(p BufferProfile, adu, dlt decimal.Decimal) (BufferZones, error) {
	if err := validateProfile(p); err != nil {
		return BufferZones{}, err
	}
	if adu.IsNegative() || dlt.IsNegative() {
		return BufferZones{}, ErrInvalidBuffer
	}
	adjDLT := adu.Mul(dlt)
	redBase := adjDLT.Mul(p.LeadTimeFactor)
	redSafety := redBase.Mul(p.VariabilityFactor)
	green := decimal.Max(redBase, p.MinOrderQty)
	green = decimal.Max(green, adu.Mul(decimal.NewFromInt(int64(p.OrderCycleDays))))
	tor := redBase.Add(redSafety)
	toy := tor.Add(adjDLT)
	return BufferZones{
		RedBase: redBase, RedSafety: redSafety, Yellow: adjDLT, Green: green,
		TopOfRed: tor, TopOfYellow: toy, TopOfGreen: toy.Add(green),
	}, nil
}

func validateProfile(p BufferProfile) error {
	if p.LeadTimeFactor.IsNegative() || p.VariabilityFactor.IsNegative() || p.MinOrderQty.IsNegative() ||
		p.SpikeThresholdPct.IsNegative() || p.OrderCycleDays < 0 {
		return ErrInvalidBuffer
	}
	return nil
}

// QualifiedDemand sums demand that counts against the net-flow position
// (BR-22.11): everything due on or before day 0 (past due and due today), plus
// single-day spikes after day 0 inside the horizon. A spike is a day whose
// total demand (all lines due that day together) exceeds thresholdPct percent
// of the red zone (TOR).
func QualifiedDemand(demand []DayDemand, topOfRed, thresholdPct decimal.Decimal, horizonDays int) decimal.Decimal {
	threshold := topOfRed.Mul(thresholdPct).Div(hundred)
	sum := decimal.Zero
	perDay := map[int]decimal.Decimal{}
	for _, d := range demand {
		switch {
		case d.Day <= 0:
			sum = sum.Add(d.Qty)
		case d.Day <= horizonDays:
			perDay[d.Day] = perDay[d.Day].Add(d.Qty)
		}
	}
	for _, qty := range perDay {
		if qty.GreaterThan(threshold) {
			sum = sum.Add(qty)
		}
	}
	return sum
}

// EvaluateBuffer computes zones, qualified demand, the net-flow position and,
// when the position is at or below the top of yellow, the planned order that
// lifts it to the top of green (BR-22.11). The planned quantity is raised to the
// minimum order quantity and then rounded up to the order multiple, when either
// is set. Priority is NFP / TOG: the lower the value, the more urgent the buffer.
func EvaluateBuffer(in BufferInput) (BufferStatus, error) {
	if in.DAF.IsNegative() || in.SpikeHorizonDays < 0 || in.OpenSupply.IsNegative() || in.OrderMultiple.IsNegative() {
		return BufferStatus{}, ErrInvalidBuffer
	}
	adj := in.ADU.Mul(in.DAF)
	zones, err := ComputeZones(in.Profile, adj, in.DLT)
	if err != nil {
		return BufferStatus{}, err
	}
	qualified := QualifiedDemand(in.Demand, zones.TopOfRed, in.Profile.SpikeThresholdPct, in.SpikeHorizonDays)
	nfp := in.OnHand.Add(in.OpenSupply).Sub(qualified)

	st := BufferStatus{Zones: zones, AdjustedADU: adj, QualifiedDemand: qualified, NetFlow: nfp, Zone: ZoneGreen}
	switch {
	case nfp.LessThanOrEqual(zones.TopOfRed):
		st.Zone = ZoneRed
	case nfp.LessThanOrEqual(zones.TopOfYellow):
		st.Zone = ZoneYellow
	}
	if nfp.LessThanOrEqual(zones.TopOfYellow) {
		qty := decimal.Max(zones.TopOfGreen.Sub(nfp), in.Profile.MinOrderQty)
		if in.OrderMultiple.IsPositive() {
			qty = roundUpTo(qty, in.OrderMultiple)
		}
		st.PlannedQty = qty
	}
	if zones.TopOfGreen.IsPositive() {
		st.Priority = nfp.Div(zones.TopOfGreen).Round(4)
	}
	return st, nil
}

// roundUpTo returns the smallest multiple of step that is at least qty.
func roundUpTo(qty, step decimal.Decimal) decimal.Decimal {
	if qty.LessThanOrEqual(decimal.Zero) {
		return decimal.Zero
	}
	return qty.Div(step).Ceil().Mul(step)
}

// PriorityOrder returns buffer indexes ordered by planning priority, most
// urgent (lowest value) first. Ties keep input order, so the result is deterministic.
func PriorityOrder(priorities []decimal.Decimal) []int {
	idx := make([]int, len(priorities))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return priorities[idx[a]].LessThan(priorities[idx[b]]) })
	return idx
}

// DLTDays converts a decimal lead time in days to whole planning days, rounding up.
func DLTDays(dlt decimal.Decimal) int {
	return int(dlt.Ceil().IntPart())
}
