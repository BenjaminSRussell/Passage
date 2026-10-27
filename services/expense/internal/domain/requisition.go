package domain

import "time"

// RequisitionStatus is the travel requisition lifecycle (FR-43.16).
type RequisitionStatus string

const (
	RequisitionDraft     RequisitionStatus = "DRAFT"
	RequisitionSubmitted RequisitionStatus = "SUBMITTED"
	RequisitionApproved  RequisitionStatus = "APPROVED"
	RequisitionRejected  RequisitionStatus = "REJECTED"
	RequisitionClosed    RequisitionStatus = "CLOSED"
)

// requisitionTransitions: APPROVED closes once its report posts or 60 days after
// the last travel date (BR-43.13), so only APPROVED → CLOSED is a manual edge.
var requisitionTransitions = map[RequisitionStatus][]RequisitionStatus{
	RequisitionDraft:     {RequisitionSubmitted},
	RequisitionSubmitted: {RequisitionApproved, RequisitionRejected},
	RequisitionApproved:  {RequisitionClosed},
}

// CanTransitionRequisition reports whether a requisition may move between statuses.
func CanTransitionRequisition(from, to RequisitionStatus) bool {
	for _, next := range requisitionTransitions[from] {
		if next == to {
			return true
		}
	}
	return false
}

// TripWindow is the approved travel range of a requisition, inclusive, by calendar day.
type TripWindow struct {
	From time.Time
	To   time.Time
}

// closeAfterLastTravelDays is the BR-43.13 grace period before automatic closure.
const closeAfterLastTravelDays = 60

// OutOfRangeDates returns the travel dates that fall outside the approved window.
// Each out-of-range date is a WARN (BR-43.13); the input order is preserved.
func OutOfRangeDates(w TripWindow, travelDates []time.Time) []time.Time {
	var out []time.Time
	from := dayOnly(w.From)
	to := dayOnly(w.To)
	for _, d := range travelDates {
		day := dayOnly(d)
		if day.Before(from) || day.After(to) {
			out = append(out, d)
		}
	}
	return out
}

// CloseDate is the day an approved requisition closes on its own: 60 days after
// the last travel date (BR-43.13).
func CloseDate(lastTravel time.Time) time.Time {
	return dayOnly(lastTravel).AddDate(0, 0, closeAfterLastTravelDays)
}

// CloseDue is true when a requisition should close: its linked report is posted,
// or asOf has reached the close date (60 days after the last travel date).
func CloseDue(lastTravel, asOf time.Time, reportPosted bool) bool {
	if reportPosted {
		return true
	}
	return !dayOnly(asOf).Before(CloseDate(lastTravel))
}

func dayOnly(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}
