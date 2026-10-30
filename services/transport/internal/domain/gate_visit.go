package domain

import (
	"errors"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// Gate visit statuses, directions and gate log kinds (FR-34.14).
const (
	VisitInYard     = "IN_YARD"
	VisitCheckedOut = "CHECKED_OUT"

	DirectionInbound  = "INBOUND"
	DirectionOutbound = "OUTBOUND"

	GateLogCheckIn  = "CHECK_IN"
	GateLogSealed   = "SEALED"
	GateLogCheckOut = "CHECK_OUT"
	GateLogBlocked  = "CHECK_OUT_BLOCKED"
)

// CodeVisitState is raised when a gate command does not fit the visit: the
// visit is already checked out, or the trailer is already in the yard
// (slice-local; the catalog leaves TMS-0111 to TMS-0120 unassigned).
const CodeVisitState = "TMS-0118"

// Directions are the visit directions: INBOUND delivers to the site, OUTBOUND
// collects a load from it.
var Directions = []string{DirectionInbound, DirectionOutbound}

// ErrCheckOutBeforeCheckIn is returned when the check-out time is earlier than
// the check-in time.
var ErrCheckOutBeforeCheckIn = errors.New("transport: check-out is before check-in")

// Gate field limits. Free time and tolerance follow the §6.34.13 parameter ranges.
const (
	maxTrailerLength = 20
	maxSealLength    = 20
	maxDriverName    = 100
	maxLicenseID     = 32
	MaxFreeTimeMin   = 1440
	MaxToleranceMin  = 480
)

// scacRe is a Standard Carrier Alpha Code: 2 to 4 capital letters.
var scacRe = regexp.MustCompile(`^[A-Z]{2,4}$`)

// GateCheckIn is the input to opening a visit. AppointmentID and ScheduledAt
// come together from the dock appointment (wave 31) the kiosk or guard looked
// up; without them the visit is a walk-in.
type GateCheckIn struct {
	SiteID          string     `json:"siteId"`
	Direction       string     `json:"direction"`
	LoadID          string     `json:"loadId,omitempty"`
	CarrierSCAC     string     `json:"carrierScac"`
	TrailerNo       string     `json:"trailerNo"`
	SealNo          string     `json:"sealNo,omitempty"`
	DriverName      string     `json:"driverName"`
	DriverLicenseID string     `json:"driverLicenseId"`
	AppointmentID   string     `json:"appointmentId,omitempty"`
	ScheduledAt     *time.Time `json:"scheduledAt,omitempty"`
	ArrivedAt       time.Time  `json:"arrivedAt"`
	FreeTimeMin     int        `json:"freeTimeMin"`
	ToleranceMin    int        `json:"toleranceMin"`
	WalkInAllowed   bool       `json:"-"`
}

// GateVisit is one truck or container visit from check-in to check-out. SealNo
// is the recorded seal: from check-in for an inbound sealed trailer, or from
// load confirmation for an outbound load. DriverName and DriverLicenseID are
// personal data (90-day retention, then pseudonymized).
type GateVisit struct {
	ID              string     `json:"id"`
	SiteID          string     `json:"siteId"`
	Direction       string     `json:"direction"`
	LoadID          string     `json:"loadId,omitempty"`
	CarrierSCAC     string     `json:"carrierScac"`
	TrailerNo       string     `json:"trailerNo"`
	SealNo          string     `json:"sealNo,omitempty"`
	DriverName      string     `json:"driverName"`
	DriverLicenseID string     `json:"driverLicenseId"`
	AppointmentID   string     `json:"appointmentId,omitempty"`
	ScheduledAt     *time.Time `json:"scheduledAt,omitempty"`
	WalkIn          bool       `json:"walkIn"`
	Arrival         *Arrival   `json:"arrival,omitempty"`
	CheckedInAt     time.Time  `json:"checkedInAt"`
	CheckedOutAt    *time.Time `json:"checkedOutAt,omitempty"`
	FreeTimeMin     int        `json:"freeTimeMin"`
	Status          string     `json:"status"`
	Detention       *Detention `json:"detention,omitempty"`
	Version         int        `json:"version"`
	LegalEntityID   string     `json:"-"`
}

// GateLogEntry is one append-only row of the gate log. Detail never carries
// driver PII.
type GateLogEntry struct {
	ID         string            `json:"id"`
	VisitID    string            `json:"visitId"`
	Kind       string            `json:"kind"`
	OccurredAt time.Time         `json:"occurredAt"`
	RecordedBy string            `json:"recordedBy"`
	Detail     map[string]string `json:"detail"`
}

// NormalizeCheckIn trims the free-text fields and upper-cases the codes a guard
// may type in any case.
func NormalizeCheckIn(in GateCheckIn) GateCheckIn {
	in.Direction = strings.ToUpper(strings.TrimSpace(in.Direction))
	in.CarrierSCAC = strings.ToUpper(strings.TrimSpace(in.CarrierSCAC))
	in.TrailerNo = strings.ToUpper(strings.TrimSpace(in.TrailerNo))
	in.SealNo = strings.TrimSpace(in.SealNo)
	in.DriverName = strings.TrimSpace(in.DriverName)
	in.DriverLicenseID = strings.TrimSpace(in.DriverLicenseID)
	return in
}

// ValidateCheckIn returns the field errors of a normalized check-in; ids are
// checked by the caller.
func ValidateCheckIn(in GateCheckIn) map[string]string {
	fields := map[string]string{}
	if !slices.Contains(Directions, in.Direction) {
		fields["direction"] = "must be INBOUND or OUTBOUND"
	}
	if !scacRe.MatchString(in.CarrierSCAC) {
		fields["carrierScac"] = "must be a 2 to 4 letter SCAC"
	}
	if in.TrailerNo == "" || len(in.TrailerNo) > maxTrailerLength {
		fields["trailerNo"] = "is required, up to 20 characters"
	}
	if len(in.SealNo) > maxSealLength {
		fields["sealNo"] = "must be at most 20 characters"
	}
	if in.DriverName == "" || len(in.DriverName) > maxDriverName {
		fields["driverName"] = "is required, up to 100 characters"
	}
	if in.DriverLicenseID == "" || len(in.DriverLicenseID) > maxLicenseID {
		fields["driverLicenseId"] = "is required, up to 32 characters"
	}
	if (in.AppointmentID == "") != (in.ScheduledAt == nil) {
		fields["appointmentId"] = "appointmentId and scheduledAt come together from the dock appointment; send both or neither"
	}
	if in.FreeTimeMin < 0 || in.FreeTimeMin > MaxFreeTimeMin {
		fields["freeTimeMin"] = "must be between 0 and 1440"
	}
	if in.ToleranceMin < 0 || in.ToleranceMin > MaxToleranceMin {
		fields["toleranceMin"] = "must be between 0 and 480"
	}
	return fields
}

// OpenVisit applies the check-in rules to a valid check-in: a walk-in needs the
// site to allow walk-ins (TMS-0128), and an appointment's arrival is ON_TIME
// within the tolerance, otherwise EARLY or LATE with TMS-0127 (BR-34.14). The
// visit is returned without an id; the store assigns it.
func OpenVisit(in GateCheckIn) (GateVisit, error) {
	walkIn := in.AppointmentID == ""
	if err := CheckWalkIn(!walkIn, in.WalkInAllowed); err != nil {
		return GateVisit{}, err
	}
	v := GateVisit{
		SiteID:          in.SiteID,
		Direction:       in.Direction,
		LoadID:          in.LoadID,
		CarrierSCAC:     in.CarrierSCAC,
		TrailerNo:       in.TrailerNo,
		SealNo:          in.SealNo,
		DriverName:      in.DriverName,
		DriverLicenseID: in.DriverLicenseID,
		AppointmentID:   in.AppointmentID,
		WalkIn:          walkIn,
		CheckedInAt:     in.ArrivedAt.UTC(),
		FreeTimeMin:     in.FreeTimeMin,
		Status:          VisitInYard,
	}
	if in.ScheduledAt != nil {
		sched := in.ScheduledAt.UTC()
		v.ScheduledAt = &sched
		a := ClassifyArrival(sched, v.CheckedInAt, in.ToleranceMin)
		v.Arrival = &a
	}
	return v, nil
}

// RecordSeal records the seal applied at load confirmation while the trailer is
// in the yard. A later seal replaces an earlier one (a seal broken for an
// inspection is replaced); the gate log keeps each.
func (v GateVisit) RecordSeal(seal string) (GateVisit, error) {
	if v.Status != VisitInYard {
		return v, &Error{Code: CodeVisitState, Detail: "the visit is already checked out; a seal can only be recorded while the trailer is in the yard"}
	}
	v.SealNo = strings.TrimSpace(seal)
	return v, nil
}

// CheckOut closes a visit (BR-34.13, BR-34.14). When a seal is recorded the
// declared seal must equal it; an outbound visit with no recorded seal has no
// confirmed load and is blocked too. Both are TMS-0129. An inbound visit with
// no recorded seal (an unsealed or emptied trailer) leaves without a seal check.
// Detention is the dwell beyond the visit's free time at the hourly rate.
func (v GateVisit) CheckOut(declaredSeal string, at time.Time, hourlyRate decimal.Decimal) (GateVisit, error) {
	if v.Status != VisitInYard {
		return v, &Error{Code: CodeVisitState, Detail: "the visit is already checked out"}
	}
	out := at.UTC()
	if out.Before(v.CheckedInAt) {
		return v, ErrCheckOutBeforeCheckIn
	}
	switch {
	case v.SealNo != "":
		if err := CheckSeal(declaredSeal, v.SealNo); err != nil {
			return v, err
		}
	case v.Direction == DirectionOutbound:
		return v, &Error{Code: CodeSealMismatch,
			Detail: "no seal is recorded for this outbound visit: the load is not confirmed, so check-out is blocked"}
	}
	d := ComputeDetention(v.CheckedInAt, out, v.FreeTimeMin, hourlyRate)
	v.Status = VisitCheckedOut
	v.CheckedOutAt = &out
	v.Detention = &d
	return v, nil
}
