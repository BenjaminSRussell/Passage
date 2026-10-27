package domain

import (
	"errors"
	"fmt"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
)

// EventType is the CloudEvents type of a machine event (BR-25.4).
const EventType = "dev.passage.shopfloor.machine.event.v1"

// Machine event kinds and machine states (BR-25.4, MachineEvent entity).
const (
	KindCount       = "count"
	KindState       = "state"
	KindDowntime    = "downtime"
	KindAlarm       = "alarm"
	KindMeasurement = "measurement"
)

var (
	kinds  = map[string]bool{KindCount: true, KindState: true, KindDowntime: true, KindAlarm: true, KindMeasurement: true}
	states = map[string]bool{"RUNNING": true, "IDLE": true, "DOWN": true, "SETUP": true, "OFF": true}
)

// ErrInvalidEvent wraps every validation failure of a machine event.
var ErrInvalidEvent = errors.New("invalid machine event")

// Measurement is an analog reading attached to a measurement event.
type Measurement struct {
	Name  string  `json:"name"`
	Value float64 `json:"value"`
	Unit  string  `json:"unit"`
}

// MachineEvent is the normalized data payload of dev.passage.shopfloor.machine.event.v1.
// Counts are deltas since the previous count event (see CounterDelta).
type MachineEvent struct {
	MachineID   string            `json:"machineId"`
	ResourceID  string            `json:"resourceId"`
	Seq         int64             `json:"seq"`
	Kind        string            `json:"kind"`
	TS          time.Time         `json:"ts"`
	GoodCount   *int64            `json:"goodCount,omitempty"`
	RejectCount *int64            `json:"rejectCount,omitempty"`
	State       string            `json:"state,omitempty"`
	ReasonCode  *string           `json:"reasonCode,omitempty"`
	Measurement *Measurement      `json:"measurement,omitempty"`
	JobID       *string           `json:"jobId,omitempty"`
	Tags        map[string]string `json:"tags,omitempty"`
}

// Validate checks the BR-25.4 rules that do not depend on the connector's state.
func (e MachineEvent) Validate() error {
	switch {
	case e.MachineID == "":
		return fmt.Errorf("%w: machineId is required", ErrInvalidEvent)
	case !ids.Valid(e.ResourceID):
		return fmt.Errorf("%w: resourceId must be a UUID", ErrInvalidEvent)
	case e.Seq <= 0:
		return fmt.Errorf("%w: seq must be positive", ErrInvalidEvent)
	case !kinds[e.Kind]:
		return fmt.Errorf("%w: unknown kind %q", ErrInvalidEvent, e.Kind)
	case e.TS.IsZero():
		return fmt.Errorf("%w: ts is required", ErrInvalidEvent)
	}
	if e.Kind == KindCount && (e.GoodCount == nil || *e.GoodCount < 0) {
		return fmt.Errorf("%w: count events need a non-negative goodCount", ErrInvalidEvent)
	}
	if e.Kind == KindState && !states[e.State] {
		return fmt.Errorf("%w: unknown state %q", ErrInvalidEvent, e.State)
	}
	if e.Kind == KindMeasurement && (e.Measurement == nil || e.Measurement.Name == "") {
		return fmt.Errorf("%w: measurement events need a named measurement", ErrInvalidEvent)
	}
	return nil
}

// CloudEvent is the CloudEvents 1.0 envelope the connector publishes to Kafka.
type CloudEvent struct {
	SpecVersion     string       `json:"specversion"`
	ID              string       `json:"id"`
	Source          string       `json:"source"`
	Type            string       `json:"type"`
	Subject         string       `json:"subject"`
	Time            string       `json:"time"`
	DataContentType string       `json:"datacontenttype"`
	Data            MachineEvent `json:"data"`
}

// Envelope wraps a valid machine event in the BR-25.4 CloudEvent. The id is
// <machineId>-<seq>, which makes the Kafka producer's retries idempotent, and the
// source is urn:passage:iot-edge:<site>:<edgeId>.
func Envelope(site, edgeID string, e MachineEvent) (CloudEvent, error) {
	if site == "" || edgeID == "" {
		return CloudEvent{}, fmt.Errorf("%w: site and edge id are required", ErrInvalidEvent)
	}
	if err := e.Validate(); err != nil {
		return CloudEvent{}, err
	}
	return CloudEvent{
		SpecVersion:     "1.0",
		ID:              fmt.Sprintf("%s-%d", e.MachineID, e.Seq),
		Source:          fmt.Sprintf("urn:passage:iot-edge:%s:%s", site, edgeID),
		Type:            EventType,
		Subject:         e.MachineID,
		Time:            e.TS.UTC().Format("2006-01-02T15:04:05.000Z07:00"),
		DataContentType: "application/json",
		Data:            e,
	}, nil
}
