package domain

import (
	"slices"
	"time"
)

// Warranty start triggers (FR-45.2, PRD 8.45.13 "Warranty start trigger default").
const (
	StartShip         = "SHIP"
	StartInstall      = "INSTALL"
	StartRegistration = "REGISTRATION"
)

// Service object statuses (PRD 8.45.4 ServiceObject).
const (
	ObjectActive   = "ACTIVE"
	ObjectInactive = "INACTIVE"
	ObjectReturned = "RETURNED"
)

// MaxWarrantyMonths bounds a warranty term to 20 years.
const MaxWarrantyMonths = 240

// WarrantyTerm is the warranty attached to a service object (FR-45.2). Months 0
// means the object has no warranty.
type WarrantyTerm struct {
	StartTrigger  string `json:"startTrigger"`
	Months        int    `json:"months"`
	PartsCovered  bool   `json:"partsCovered"`
	LaborCovered  bool   `json:"laborCovered"`
	TravelCovered bool   `json:"travelCovered"`
}

// Valid reports whether the term names a known trigger and a duration of 0 to 240 months.
func (w WarrantyTerm) Valid() bool {
	return slices.Contains([]string{StartShip, StartInstall, StartRegistration}, w.StartTrigger) &&
		w.Months >= 0 && w.Months <= MaxWarrantyMonths
}

// WarrantyStart returns the day a warranty starts for its trigger (FR-45.2):
// the shipment day, the installation day, or the day the object was registered
// in the installed base. ok is false while the trigger's date is not known yet
// (an INSTALL warranty before installation); the warranty then covers nothing.
func WarrantyStart(trigger string, shipped, installed *time.Time, registered time.Time) (time.Time, bool) {
	switch trigger {
	case StartShip:
		if shipped != nil {
			return Day(*shipped), true
		}
	case StartInstall:
		if installed != nil {
			return Day(*installed), true
		}
	case StartRegistration:
		return Day(registered), true
	}
	return time.Time{}, false
}

// Day truncates an instant to its UTC calendar day.
func Day(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// WarrantyCoverOn reports which parts of a work order a warranty covers on the
// service day (BR-45.4): the term is active from its start day for its months.
// A warranty that has not started (start nil) covers nothing.
func WarrantyCoverOn(w WarrantyTerm, start *time.Time, on time.Time) WarrantyCover {
	if start == nil {
		return WarrantyCover{}
	}
	return WarrantyCover{Active: WarrantyActive(*start, w.Months, Day(on), false),
		Parts: w.PartsCovered, Labor: w.LaborCovered, Travel: w.TravelCovered}
}

// WarrantyEnd is the first day the warranty no longer covers, or nil when it has not started.
func WarrantyEnd(w WarrantyTerm, start *time.Time) *time.Time {
	if start == nil || w.Months <= 0 {
		return nil
	}
	end := addMonthsClamped(*start, w.Months)
	return &end
}
