package domain

import (
	"time"
)

// CodeDoorOverlap is an appointment that overlaps another on the same door
// (FR-31.8, AC-31.8 DOOR_SLOT_OVERLAP; message WMS-0108 in §6.31.18).
const CodeDoorOverlap = "WMS-0108"

// SlotGranularity is the scheduling grid of dock appointments (FR-31.8).
const SlotGranularity = 15 * time.Minute

// MaxAppointment bounds one appointment to a day, a guard against typing the
// wrong date.
const MaxAppointment = 24 * time.Hour

// AppointmentDirection is whether an appointment unloads (INBOUND) or loads
// (OUTBOUND) at the door.
type AppointmentDirection string

const (
	Inbound  AppointmentDirection = "INBOUND"
	Outbound AppointmentDirection = "OUTBOUND"
)

// DockDoor is a door of a warehouse that appointments are booked on. Its code
// is unique in the warehouse.
type DockDoor struct {
	ID          string    `json:"id"`
	WarehouseID string    `json:"warehouseId"`
	Code        string    `json:"code"`
	CreatedAt   time.Time `json:"createdAt"`
}

// Appointment books a door for the half-open window [Start, End) for one
// inbound or outbound movement, identified by Reference (an ASN, shipment or
// load number).
type Appointment struct {
	ID          string               `json:"id"`
	WarehouseID string               `json:"warehouseId"`
	DoorID      string               `json:"doorId"`
	DoorCode    string               `json:"doorCode"`
	Direction   AppointmentDirection `json:"direction"`
	Reference   string               `json:"reference"`
	Start       time.Time            `json:"start"`
	End         time.Time            `json:"end"`
	CreatedAt   time.Time            `json:"createdAt"`
}

// OnSlotGrid reports whether t falls on the 15-minute grid. Every UTC offset
// in use is a whole number of quarter hours, so a time on the grid in UTC is
// on it in the warehouse's local time too.
func OnSlotGrid(t time.Time) bool {
	return t.Equal(t.Truncate(SlotGranularity))
}

// Overlaps reports whether two windows share any time. Windows are half open:
// 09:00–09:45 and 09:45–10:00 do not overlap.
func Overlaps(a, b Appointment) bool {
	return a.Start.Before(b.End) && b.Start.Before(a.End)
}

// CheckDoorSlot refuses cand with WMS-0108 when an existing appointment on the
// same door overlaps it (AC-31.8). Appointments of other doors are ignored.
func CheckDoorSlot(existing []Appointment, cand Appointment) error {
	for _, e := range existing {
		if e.DoorID == cand.DoorID && Overlaps(e, cand) {
			door := cand.DoorCode
			if door == "" {
				door = cand.DoorID
			}
			return errorf(CodeDoorOverlap, "Door %s is already booked %s–%s (DOOR_SLOT_OVERLAP, appointment %s).",
				door, e.Start.UTC().Format("2006-01-02 15:04"), e.End.UTC().Format("2006-01-02 15:04 UTC"), e.Reference)
		}
	}
	return nil
}
