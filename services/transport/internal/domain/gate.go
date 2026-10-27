package domain

import (
	"fmt"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// Arrival classifications at the gate (BR-34.14).
const (
	ArrivalOnTime = "ON_TIME"
	ArrivalEarly  = "EARLY"
	ArrivalLate   = "LATE"
)

// Defaults of the site and carrier settings gate_tolerance_min (the ON_TIME
// window either side of the appointment) and detention_free_time_min (§6.34.13).
const (
	DefaultGateToleranceMin     = 60
	DefaultDetentionFreeTimeMin = 120
)

// Gate and yard codes (§6.34.18), raised by the gate check-in and check-out.
const (
	CodeArrivalOutsideWindow = "TMS-0127"
	CodeNoAppointment        = "TMS-0128"
	CodeSealMismatch         = "TMS-0129"
	CodeDetention            = "TMS-0126"
)

// Arrival is the gate classification of one check-in.
type Arrival struct {
	Status   string `json:"status"`
	DeltaMin int    `json:"deltaMin"`
	Code     string `json:"code,omitempty"`
}

// ClassifyArrival compares the arrival with the appointment. Within the tolerance
// on either side it is ON_TIME; earlier is EARLY and later is LATE, both raising a
// warning (BR-34.14). Minutes are truncated toward zero.
func ClassifyArrival(scheduled, arrived time.Time, toleranceMin int) Arrival {
	delta := int(arrived.Sub(scheduled) / time.Minute)
	a := Arrival{Status: ArrivalOnTime, DeltaMin: delta}
	switch {
	case delta < -toleranceMin:
		a.Status = ArrivalEarly
	case delta > toleranceMin:
		a.Status = ArrivalLate
	}
	if a.Status != ArrivalOnTime {
		a.Code = CodeArrivalOutsideWindow
	}
	return a
}

// CheckSeal blocks check-out when the declared seal is not the seal recorded at
// load confirmation (BR-34.14). The comparison ignores surrounding space and case.
func CheckSeal(declared, recorded string) error {
	d := strings.TrimSpace(declared)
	r := strings.TrimSpace(recorded)
	if d == "" || r == "" || !strings.EqualFold(d, r) {
		return &Error{Code: CodeSealMismatch,
			Detail: fmt.Sprintf("seal number %q does not match recorded seal %q", d, r)}
	}
	return nil
}

// CheckWalkIn rejects a check-in with no appointment when the site does not allow
// walk-ins (FR-34.14, TMS-0128).
func CheckWalkIn(hasAppointment, walkInAllowed bool) error {
	if hasAppointment || walkInAllowed {
		return nil
	}
	return &Error{Code: CodeNoAppointment, Detail: "no appointment found and walk-ins are not allowed at this site"}
}

// Detention is the dwell beyond free time and the charge built from it
// (BR-34.13). Code is TMS-0126 when detention accrued.
type Detention struct {
	DwellMin     int             `json:"dwellMin"`
	DetentionMin int             `json:"detentionMin"`
	Hours        int             `json:"hours"`
	Charge       decimal.Decimal `json:"charge"`
	Code         string          `json:"code,omitempty"`
}

// ComputeDetention returns dwell = checkOut − checkIn, detention = max(0, dwell −
// free time), and the expected charge = ceil(detention ÷ 60) × hourly rate.
func ComputeDetention(checkIn, checkOut time.Time, freeTimeMin int, hourlyRate decimal.Decimal) Detention {
	dwell := int(checkOut.Sub(checkIn) / time.Minute)
	d := Detention{DwellMin: dwell}
	if extra := dwell - freeTimeMin; extra > 0 {
		d.DetentionMin = extra
		d.Hours = (extra + 59) / 60
		d.Code = CodeDetention
	}
	d.Charge = hourlyRate.Mul(decimal.NewFromInt(int64(d.Hours))).Round(2)
	return d
}
