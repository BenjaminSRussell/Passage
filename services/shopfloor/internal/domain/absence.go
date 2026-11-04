package domain

import (
	"sort"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

const (
	KindAbsence = "absence"

	TypeAbsencePaid   = "absence-paid"
	TypeAbsenceUnpaid = "absence-unpaid"

	// MaxAbsenceMultiplier is the upper bound of an absence code's pay multiplier.
	MaxAbsenceMultiplier = 5
)

// FlagAbsenceOverlap marks a day with an absence that overlaps clocked time or another
// absence of the worker: the same hours would be paid twice, so the day cannot be approved
// (SF-0006) until a supervisor corrects the times and the day is recalculated.
const FlagAbsenceOverlap = "absence-overlap"

// Absence is one closed absence registration: the hours the worker was absent under an
// absence code (FR-25.2). It belongs to the local day it starts on, like a worked segment
// that crosses midnight (a registration is at most 24 hours, so an absence never covers more
// than one start day). A paid code pays the hours at Multiplier times the worker's rate; an
// unpaid code has Multiplier 0 and records the hours at amount 0.
type Absence struct {
	ID          string
	Start, Stop time.Time
	Hours       decimal.Decimal
	Paid        bool
	Multiplier  decimal.Decimal
}

// AddAbsences attaches each absence to the local day it starts on, creating the day when the
// worker clocked nothing that day. Absence hours never count toward the overtime thresholds:
// they are paid time, not worked time. An absence that overlaps a worked segment (of any day,
// so a night shift from the day before counts) or another absence flags its day
// absence-overlap.
func AddAbsences(days []Day, absences []Absence, loc *time.Location) []Day {
	byDate := make(map[string]int, len(days))
	out := append([]Day(nil), days...)
	for i, d := range out {
		byDate[d.DateString()] = i
	}
	for k, a := range absences {
		mid := localMidnight(a.Start, loc)
		key := mid.Format(dateLayout)
		i, ok := byDate[key]
		if !ok {
			out = append(out, Day{Date: mid})
			i = len(out) - 1
			byDate[key] = i
		}
		out[i].Absences = append(out[i].Absences, a)
		if absenceOverlaps(a, k, days, absences) {
			addFlag(&out[i], FlagAbsenceOverlap)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Date.Before(out[j].Date) })
	return out
}

// absenceOverlaps reports whether absence k shares time with a worked segment or another absence.
func absenceOverlaps(a Absence, k int, days []Day, absences []Absence) bool {
	// A zero-length absence (corrected away, or replaced by one with another code) pays nothing
	// and overlaps nothing.
	if !a.Stop.After(a.Start) {
		return false
	}
	overlap := func(start, end time.Time) bool {
		return end.After(start) && start.Before(a.Stop) && a.Start.Before(end)
	}
	for _, d := range days {
		for _, s := range d.Segments {
			if overlap(s.Start, s.End) {
				return true
			}
		}
	}
	for j, b := range absences {
		if j != k && overlap(b.Start, b.Stop) {
			return true
		}
	}
	return false
}

// absenceHours is the sum of the day's absence hours at 0.01 h.
func (d Day) absenceHours() decimal.Decimal {
	total := decimal.Zero
	for _, a := range d.Absences {
		total = total.Add(a.Hours.Round(2))
	}
	return total
}

// absenceLines is one pay line per absence with hours: absence-paid at the code's multiplier,
// rounded once to the currency's minor unit like every other line, and absence-unpaid with the
// hours, multiplier 0 and no amount.
func (c Calculation) absenceLines(d Day) []PayLine {
	var out []PayLine
	for _, a := range d.Absences {
		hours := a.Hours.Round(2)
		if !hours.IsPositive() {
			continue
		}
		l := PayLine{Type: TypeAbsenceUnpaid, Hours: hours, Multiplier: decimal.Zero, Amount: decimal.Zero, Sources: []string{a.ID}}
		if a.Paid {
			amount := money.Amount{Value: c.rateOn(d.Date), Currency: c.Currency}.Mul(hours.Mul(a.Multiplier))
			l = PayLine{Type: TypeAbsencePaid, Hours: hours, Multiplier: a.Multiplier, Amount: amount.Value, Sources: []string{a.ID}}
		}
		out = append(out, l)
	}
	return out
}

// AbsenceMultiplier validates the pay multiplier of an absence code. A paid code defaults to 1
// and must be above 0, at most 5, with at most 4 decimals. An unpaid code has no rate effect:
// its multiplier is 0, and any other value sent is refused. The second result is the field error.
func AbsenceMultiplier(paid bool, in *decimal.Decimal) (decimal.Decimal, string) {
	if !paid {
		if in != nil && !in.IsZero() {
			return decimal.Zero, "an unpaid code has no pay multiplier; omit it or send 0"
		}
		return decimal.Zero, ""
	}
	one := decimal.NewFromInt(1)
	switch {
	case in == nil:
		return one, ""
	case !in.IsPositive() || in.GreaterThan(decimal.NewFromInt(MaxAbsenceMultiplier)):
		return one, "must be above 0 and at most 5"
	case !in.Equal(in.Round(4)):
		return one, "must have at most 4 decimals"
	}
	return *in, ""
}
