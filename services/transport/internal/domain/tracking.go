package domain

import (
	"sort"
	"time"
)

// Stop statuses, in the order a stop moves through them.
const (
	StopPending   = "PENDING"
	StopArrived   = "ARRIVED"
	StopPickedUp  = "PICKED_UP"
	StopDelivered = "DELIVERED"
)

var stopRank = map[string]int{StopPending: 0, StopArrived: 1, StopPickedUp: 2, StopDelivered: 3}

// trackingCodes maps the supported EDI 214 status codes to the stop status they
// set. Unknown codes are rejected at ingest; they are never guessed.
var trackingCodes = map[string]string{
	"X3": StopArrived,   // arrived at pickup location
	"X1": StopArrived,   // arrived at delivery location
	"AF": StopPickedUp,  // carrier departed with the freight
	"D1": StopDelivered, // delivered
}

// TrackingCodes lists the supported EDI 214 status codes in milestone order.
var TrackingCodes = []string{"X3", "AF", "X1", "D1"}

// TrackingSources are the accepted origins of a status event (FR-34.6).
var TrackingSources = map[string]bool{"EDI214": true, "API": true, "TELEMATICS": true, "DRIVER": true}

// IsTrackingCode reports whether code is a supported status code.
func IsTrackingCode(code string) bool {
	_, ok := trackingCodes[code]
	return ok
}

// TrackingEvent is one status report for a stop on a load. Source and Ref are the
// idempotency key: the same report delivered twice is stored once.
type TrackingEvent struct {
	ID         string    `json:"id"`
	LoadID     string    `json:"loadId"`
	StopNo     int       `json:"stopNo"`
	Code       string    `json:"code"`
	OccurredAt time.Time `json:"occurredAt"`
	Location   string    `json:"location,omitempty"`
	Source     string    `json:"source"`
	Ref        string    `json:"ref"`
	ReceivedAt time.Time `json:"receivedAt"`
}

// SameReport reports whether e and o describe the same status report, so a
// redelivery under the same source and ref is a replay rather than a conflict.
func (e TrackingEvent) SameReport(o TrackingEvent) bool {
	return e.LoadID == o.LoadID && e.StopNo == o.StopNo && e.Code == o.Code && e.OccurredAt.Equal(o.OccurredAt) &&
		e.Location == o.Location && e.Source == o.Source && e.Ref == o.Ref
}

// StopState is the projected status of one stop. Times are the first time each
// milestone was reached. PickupInferred is true when a delivery was reported with
// no earlier pickup event (BR-34.9).
type StopState struct {
	StopNo         int        `json:"stopNo"`
	Status         string     `json:"status"`
	ArrivedAt      *time.Time `json:"arrivedAt,omitempty"`
	PickedUpAt     *time.Time `json:"pickedUpAt,omitempty"`
	DeliveredAt    *time.Time `json:"deliveredAt,omitempty"`
	PickupInferred bool       `json:"pickupInferred"`
}

// ProjectStops applies events in occurred_at order, ties broken by source and
// ref, so the result does not depend on the order in which events arrived. Events
// with an unknown code are skipped; ingest rejects them before they are stored.
func ProjectStops(events []TrackingEvent) []StopState {
	ordered := append([]TrackingEvent{}, events...)
	sort.SliceStable(ordered, func(i, j int) bool {
		a, b := ordered[i], ordered[j]
		if !a.OccurredAt.Equal(b.OccurredAt) {
			return a.OccurredAt.Before(b.OccurredAt)
		}
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		return a.Ref < b.Ref
	})
	byStop := map[int]*StopState{}
	for _, ev := range ordered {
		target, ok := trackingCodes[ev.Code]
		if !ok {
			continue
		}
		st := byStop[ev.StopNo]
		if st == nil {
			st = &StopState{StopNo: ev.StopNo, Status: StopPending}
			byStop[ev.StopNo] = st
		}
		at := ev.OccurredAt
		switch target {
		case StopArrived:
			if st.ArrivedAt == nil {
				st.ArrivedAt = &at
			}
		case StopPickedUp:
			if st.PickedUpAt == nil || st.PickupInferred {
				st.PickedUpAt = &at
				st.PickupInferred = false
			}
		case StopDelivered:
			if st.PickedUpAt == nil {
				st.PickedUpAt = &at
				st.PickupInferred = true
			}
			if st.DeliveredAt == nil {
				st.DeliveredAt = &at
			}
		}
		if stopRank[target] > stopRank[st.Status] {
			st.Status = target
		}
	}
	out := make([]StopState, 0, len(byStop))
	for _, st := range byStop {
		out = append(out, *st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StopNo < out[j].StopNo })
	return out
}
