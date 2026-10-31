package domain

import (
	"time"

	"github.com/shopspring/decimal"
)

// Schedule line statuses (FR-27.6). CONFIRMED lines are promised on or before
// the requested date, BACKORDER lines after it, UNPROMISED lines have no date
// because no supply covers them. CANCELLED and SUPERSEDED lines are history.
const (
	SchedConfirmed  = "CONFIRMED"
	SchedBackorder  = "BACKORDER"
	SchedUnpromised = "UNPROMISED"
	SchedCancelled  = "CANCELLED"
	SchedSuperseded = "SUPERSEDED"
)

// Schedule line sources: LINE copies the line's confirmed ship date, ATP is a
// slice of a promise placed on an ATP bucket.
const (
	SchedSourceLine = "LINE"
	SchedSourceATP  = "ATP"
)

// ScheduleEntry is one planned schedule line of an order line. Date is empty
// for an UNPROMISED entry; BucketDate is the date of the ATP bucket an ATP
// slice was placed on (on or before Date, which never precedes the request).
type ScheduleEntry struct {
	Date       string          `json:"date,omitempty"`
	Quantity   decimal.Decimal `json:"quantity"`
	Status     string          `json:"status"`
	Source     string          `json:"source"`
	BucketDate string          `json:"bucketDate,omitempty"`
}

// ScheduleStatus classifies a schedule line by its delivery date against the
// line's requested date (both yyyy-mm-dd). No date means UNPROMISED.
func ScheduleStatus(date, requested string) string {
	switch {
	case date == "":
		return SchedUnpromised
	case date > requested:
		return SchedBackorder
	}
	return SchedConfirmed
}

// PlanSchedule turns a promise into schedule lines in date order: the promised
// slices (AC-27.8: 40 on the requested date, 5 on the next supply date) and,
// when no supply covers the rest, one undated UNPROMISED line for it. The
// quantities add up to the promised quantity. periods are the buckets the
// promise was computed on; each dated entry names its bucket's date.
func PlanSchedule(p Promise, periods []Period, requested time.Time) []ScheduleEntry {
	req := requested.Format("2006-01-02")
	out := make([]ScheduleEntry, 0, len(p.Schedule)+1)
	for _, s := range p.Schedule {
		d := s.Date.Format("2006-01-02")
		e := ScheduleEntry{Date: d, Quantity: s.Quantity, Status: ScheduleStatus(d, req), Source: SchedSourceATP}
		if s.Period >= 0 && s.Period < len(periods) {
			e.BucketDate = periods[s.Period].Date.Format("2006-01-02")
		}
		out = append(out, e)
	}
	if p.Backorder.IsPositive() {
		out = append(out, ScheduleEntry{Quantity: p.Backorder, Status: SchedUnpromised, Source: SchedSourceATP})
	}
	return out
}
