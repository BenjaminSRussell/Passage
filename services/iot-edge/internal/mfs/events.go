package mfs

import (
	"fmt"
	"time"
)

// EventType is the CloudEvents type of an automation event published for the warehouse
// (wave 32, FR-32.12). The warehouse consumes it to confirm transport work.
const EventType = "dev.passage.warehouse.automation.event.v1"

// Event kinds.
const (
	KindArrived  = "arrived"  // a load unit reached a location
	KindDiverted = "diverted" // a sorter sent a load unit to a lane
	KindFault    = "fault"    // the controller reported a fault
)

// Event is the normalized payload of EventType. LoadUnit is the SSCC; it is empty for a fault that
// belongs to no unit. Seq and Station let the consumer drop a repeat and see which controller sent it.
type Event struct {
	Kind     string    `json:"kind"`
	Station  string    `json:"station"`
	Seq      int       `json:"seq"`
	LoadUnit string    `json:"loadUnit,omitempty"`
	Location string    `json:"location,omitempty"`
	Lane     string    `json:"lane,omitempty"`
	Code     string    `json:"code,omitempty"`
	Text     string    `json:"text,omitempty"`
	At       time.Time `json:"at"`
}

// EventFrom maps a telegram from the PLC to an Event. Other types are not events.
func EventFrom(t Telegram, at time.Time) (Event, error) {
	e := Event{Station: t.Sender, Seq: t.Seq, LoadUnit: t.LU, At: at.UTC()}
	if t.LU == NoLU {
		e.LoadUnit = ""
	}
	switch t.Type {
	case TypeArrv:
		e.Kind, e.Location = KindArrived, t.Location
	case TypeDivt:
		e.Kind, e.Location, e.Lane = KindDiverted, t.Location, t.Lane
	case TypeErrr:
		e.Kind, e.Code, e.Text = KindFault, t.Code, t.Text
	default:
		return Event{}, fmt.Errorf("mfs: %s telegram is not an automation event", t.Type)
	}
	return e, nil
}

// TransportOrder is a request to move one load unit, as the warehouse's transport work issues it.
type TransportOrder struct {
	LoadUnit string
	From     string
	To       string
	Priority int // 1 (highest) to 9; 0 means 5
}

// MoveFrom builds the MOVE telegram for an order. The link assigns its sequence number.
func MoveFrom(o TransportOrder) (Telegram, error) {
	if o.Priority == 0 {
		o.Priority = 5
	}
	t := Telegram{Type: TypeMove, LU: o.LoadUnit, From: o.From, To: o.To, Priority: o.Priority}
	if err := t.Validate(); err != nil {
		return Telegram{}, err
	}
	return t, nil
}
