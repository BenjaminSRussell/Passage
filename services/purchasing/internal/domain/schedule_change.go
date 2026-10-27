package domain

import (
	"fmt"
	"sort"

	"github.com/shopspring/decimal"
)

// ScheduleChange is one requested row of a schedule replacement. LineNo 0 adds a
// new row (Qty > 0). An existing row with Qty 0, or absent from the request, is
// removed; a zero row is how a firm-zone line is removed with its reason code.
// Reason is the reason code a firm-zone reduction needs (BR-17.13).
type ScheduleChange struct {
	LineNo       int
	DeliveryDate Day
	Qty          decimal.Decimal
	Reason       string
}

// ApplySchedule computes the schedule of sa after changes, judged at asOf. Each
// existing row is checked in the zone of its current date (BR-17.13): a
// reduction inside the firm zone needs a reason (PRC-SA-001), inside the
// trade-off zone it warns, in the forecast zone it passes. Moving a row to
// another date takes its whole quantity off the old date, and removing a row
// (absent, or sent with quantity 0) reduces it to zero, so both are judged as
// reductions. No row may drop below what was already received against it
// (PRC-SA-002). It returns the rows in line-number order and one warning per
// trade-off reduction.
func ApplySchedule(sa SchedulingAgreement, changes []ScheduleChange, asOf Day) ([]ScheduleLine, []string, error) {
	zones := sa.Profile.Zones()
	current := make(map[int]ScheduleLine, len(sa.Lines))
	maxNo := 0
	for _, l := range sa.Lines {
		current[l.LineNo] = l
		maxNo = max(maxNo, l.LineNo)
	}
	var next []ScheduleLine
	var warnings []string
	seen := make(map[int]bool, len(changes))
	for _, c := range changes {
		if c.LineNo == 0 {
			if !c.Qty.IsPositive() {
				return nil, nil, fail(CodeValidation, "a new schedule line needs a quantity above zero")
			}
			maxNo++
			next = append(next, ScheduleLine{LineNo: maxNo, DeliveryDate: c.DeliveryDate, Qty: c.Qty, ReceivedQty: decimal.Zero})
			continue
		}
		old, ok := current[c.LineNo]
		if !ok {
			return nil, nil, fail(CodeValidation, "schedule line %d does not exist", c.LineNo)
		}
		if seen[c.LineNo] {
			return nil, nil, fail(CodeValidation, "schedule line %d appears twice", c.LineNo)
		}
		seen[c.LineNo] = true
		line, warning, err := changeLine(old, zones, asOf, c)
		if err != nil {
			return nil, nil, err
		}
		if warning != "" {
			warnings = append(warnings, warning)
		}
		if c.Qty.IsZero() {
			continue // removed
		}
		next = append(next, line)
	}
	for _, old := range sortedLines(sa.Lines) {
		if seen[old.LineNo] {
			continue
		}
		// Removal: the row leaves the schedule, so its quantity goes to zero.
		_, warning, err := changeLine(old, zones, asOf, ScheduleChange{LineNo: old.LineNo, DeliveryDate: old.DeliveryDate, Qty: decimal.Zero})
		if err != nil {
			return nil, nil, err
		}
		if warning != "" {
			warnings = append(warnings, warning)
		}
	}
	probe := sa
	probe.Lines = next
	if err := probe.Validate(); err != nil {
		return nil, nil, err
	}
	sort.SliceStable(next, func(i, j int) bool { return next[i].LineNo < next[j].LineNo })
	return next, warnings, nil
}

// changeLine applies one row change after the zone and received checks. The
// returned warning is non-empty for a trade-off reduction.
func changeLine(old ScheduleLine, zones ZoneProfile, asOf Day, c ScheduleChange) (ScheduleLine, string, error) {
	// What leaves the old date: the whole row when it moves, else the cut.
	remaining := c.Qty
	if !c.DeliveryDate.Equal(old.DeliveryDate.Time) {
		remaining = decimal.Zero
	}
	zone := zones.ClassifyZone(asOf, old.DeliveryDate)
	warn, err := CheckScheduleReduction(zone, old.Qty, remaining, c.Reason)
	if err != nil {
		return ScheduleLine{}, "", fail(CodeFirmZone, "schedule line %d (%s) is in the firm zone; reducing or moving it needs a reason code",
			old.LineNo, old.DeliveryDate)
	}
	if c.Qty.LessThan(old.ReceivedQty) {
		return ScheduleLine{}, "", fail(CodeReceivedAboveRelease,
			"schedule line %d cannot drop to %s below the %s already received", old.LineNo, c.Qty, old.ReceivedQty)
	}
	warning := ""
	if warn {
		warning = fmt.Sprintf("schedule line %d (%s) was reduced inside the trade-off zone", old.LineNo, old.DeliveryDate)
	}
	line := old
	line.DeliveryDate, line.Qty = c.DeliveryDate, c.Qty
	if remaining.LessThan(old.Qty) && c.Reason != "" {
		line.ReductionReason = c.Reason
	}
	return line, warning, nil
}

func sortedLines(lines []ScheduleLine) []ScheduleLine {
	out := append([]ScheduleLine(nil), lines...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].LineNo < out[j].LineNo })
	return out
}
