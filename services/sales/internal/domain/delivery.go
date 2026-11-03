package domain

import (
	"fmt"
	"sort"
	"time"
)

// Delivery date control values (FR-27.3, PRD delivery_date_control_default).
// NONE and SALES_LEAD_TIME decide the confirmed ship date from the order's own
// data; ATP and CTP need the ATP projection and the wave 23 CTP API, so they
// are valid PRD values that this service refuses until those exist.
const (
	DeliveryControlNone          = "NONE"
	DeliveryControlSalesLeadTime = "SALES_LEAD_TIME"
	DeliveryControlATP           = "ATP"
	DeliveryControlCTP           = "CTP"

	// MaxSalesLeadTimeDays bounds sales_lead_time_days.
	MaxSalesLeadTimeDays = 365
	// MaxTransportDays bounds the transport days of a customer's delivery terms.
	MaxTransportDays = 60
)

// ValidateDeliveryControl returns why c cannot be stored as a delivery date
// control, or "" when it can.
func ValidateDeliveryControl(c string) string {
	switch c {
	case DeliveryControlNone, DeliveryControlSalesLeadTime:
		return ""
	case DeliveryControlATP, DeliveryControlCTP:
		return c + " is not active yet: it needs the ATP projection (ATP) or the finite-capacity CTP API (CTP)"
	}
	return "must be NONE, SALES_LEAD_TIME, ATP or CTP"
}

// ShipDecision is the confirmed ship date of one line and why it moved.
type ShipDecision struct {
	Date time.Time
	// PastMoved is set when the requested date was before today and became
	// today (BR-27.2, SLS-0003). LeadMoved is set when the sales lead time
	// pushed the date past the requested one (SALES_LEAD_TIME).
	PastMoved bool
	LeadMoved bool
}

// ShipDate returns the confirmed ship date of a line (BR-27.2): the requested
// date, or today when that is past; with SALES_LEAD_TIME never earlier than
// today plus leadDays. All three arguments are calendar days (midnight UTC).
func ShipDate(control string, requested, today time.Time, leadDays int) ShipDecision {
	d := ShipDecision{Date: requested}
	if requested.Before(today) {
		d.Date, d.PastMoved = today, true
	}
	if control == DeliveryControlSalesLeadTime {
		if earliest := today.AddDate(0, 0, leadDays); d.Date.Before(earliest) {
			d.Date, d.LeadMoved = earliest, true
		}
	}
	return d
}

// DeliveryTerms are a customer's transport days and receiving calendar. The
// zero value is the default: no transport time and every day a receiving day.
type DeliveryTerms struct {
	TransportDays int `json:"transportDays"`
	// ReceivingDays are the ISO weekdays (1 Monday to 7 Sunday) the customer
	// receives goods; empty means every day.
	ReceivingDays []int `json:"receivingDays"`
}

// isoWeekday returns 1 (Monday) to 7 (Sunday).
func isoWeekday(t time.Time) int {
	if wd := int(t.Weekday()); wd != 0 {
		return wd
	}
	return 7
}

// ReceiptDate is the confirmed receipt date of a line shipped on ship: the
// ship date plus the transport days, rolled forward to the customer's next
// receiving day (BR-27.2).
func (t DeliveryTerms) ReceiptDate(ship time.Time) time.Time {
	d := ship.AddDate(0, 0, t.TransportDays)
	if len(t.ReceivingDays) == 0 {
		return d
	}
	for i := 0; i < 7; i++ {
		for _, wd := range t.ReceivingDays {
			if wd == isoWeekday(d) {
				return d
			}
		}
		d = d.AddDate(0, 0, 1)
	}
	return d
}

// Normalise returns the terms with the receiving days sorted.
func (t DeliveryTerms) Normalise() DeliveryTerms {
	days := append([]int{}, t.ReceivingDays...)
	sort.Ints(days)
	t.ReceivingDays = days
	return t
}

// Validate returns the field errors of the terms keyed by JSON name.
func (t DeliveryTerms) Validate() map[string]string {
	f := map[string]string{}
	if t.TransportDays < 0 || t.TransportDays > MaxTransportDays {
		f["transportDays"] = fmt.Sprintf("must be 0 to %d", MaxTransportDays)
	}
	if len(t.ReceivingDays) == 0 {
		f["receivingDays"] = "must name at least one receiving weekday, 1 (Monday) to 7 (Sunday)"
		return f
	}
	seen := map[int]bool{}
	for _, d := range t.ReceivingDays {
		if d < 1 || d > 7 {
			f["receivingDays"] = "weekdays are 1 (Monday) to 7 (Sunday)"
			return f
		}
		if seen[d] {
			f["receivingDays"] = "must not repeat a weekday"
			return f
		}
		seen[d] = true
	}
	return f
}
