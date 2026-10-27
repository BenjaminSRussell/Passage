package domain

import (
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
)

// ErrInvalidConfig wraps every validation failure of an edge configuration document.
var ErrInvalidConfig = errors.New("invalid edge configuration")

// MachineSpec maps an edge machine code to one shop floor resource (FR-25.9).
type MachineSpec struct {
	MachineID  string `json:"machineId"`
	ResourceID string `json:"resourceId"`
}

// TagMapping binds one OPC-UA node id or MQTT topic to a machine event kind. Name
// and Unit describe a measurement tag.
type TagMapping struct {
	Address   string `json:"address"`
	MachineID string `json:"machineId"`
	Kind      string `json:"kind"`
	Name      string `json:"name,omitempty"`
	Unit      string `json:"unit,omitempty"`
}

// WriteRule allowlists one OPC-UA node for write-back, with the limits a setpoint
// must stay within (BR-25.15).
type WriteRule struct {
	Node string  `json:"node"`
	Min  float64 `json:"min"`
	Max  float64 `json:"max"`
	Unit string  `json:"unit,omitempty"`
}

// EdgeConfig is one versioned configuration document (EdgeConfigVersion, FR-25.19):
// the machines, tag mappings and write allowlist a connector runs with.
type EdgeConfig struct {
	Version    int           `json:"version"`
	Machines   []MachineSpec `json:"machines"`
	Tags       []TagMapping  `json:"tags"`
	WriteRules []WriteRule   `json:"writeRules,omitempty"`
}

// maxCountDelta bounds a count delta to integers a float64 holds exactly (2^53).
const maxCountDelta = 1 << 53

func finite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }

func configErr(detail string) error {
	return fmt.Errorf("%w: %s", ErrInvalidConfig, detail)
}

// Validate checks the document's internal consistency: unique machines, resources
// that are UUIDs, tags that point at a known machine and a valid kind, and write
// rules with min <= max.
func (c EdgeConfig) Validate() error {
	if c.Version < 1 {
		return configErr("version must be at least 1")
	}
	machines := map[string]bool{}
	for _, m := range c.Machines {
		switch {
		case m.MachineID == "":
			return configErr("machineId is required")
		case !ids.Valid(m.ResourceID):
			return configErr(fmt.Sprintf("resourceId of machine %s must be a UUID", m.MachineID))
		case machines[m.MachineID]:
			return configErr(fmt.Sprintf("machine %s is listed twice", m.MachineID))
		}
		machines[m.MachineID] = true
	}
	addresses := map[string]bool{}
	for _, t := range c.Tags {
		switch {
		case t.Address == "":
			return configErr("tag address is required")
		case !machines[t.MachineID]:
			return configErr(fmt.Sprintf("tag %s points at unknown machine %q", t.Address, t.MachineID))
		case addresses[t.Address]:
			return configErr(fmt.Sprintf("tag address %s is mapped twice", t.Address))
		case !kinds[t.Kind]:
			return configErr(fmt.Sprintf("tag %s has unknown kind %q", t.Address, t.Kind))
		case t.Kind == KindMeasurement && t.Name == "":
			return configErr(fmt.Sprintf("measurement tag %s needs a name", t.Address))
		}
		addresses[t.Address] = true
	}
	nodes := map[string]bool{}
	for _, w := range c.WriteRules {
		switch {
		case w.Node == "":
			return configErr("write rule node is required")
		case nodes[w.Node]:
			return configErr(fmt.Sprintf("write rule for node %s is listed twice", w.Node))
		case !finite(w.Min) || !finite(w.Max) || w.Min > w.Max:
			return configErr(fmt.Sprintf("write rule for node %s needs finite limits with min <= max", w.Node))
		}
		nodes[w.Node] = true
	}
	return nil
}

// NextApplied enforces one applied version at a time (FR-25.19): a candidate must
// validate and carry a version above the applied one. It returns the version now
// applied; on refusal the current version is returned with the error.
func NextApplied(current int, candidate EdgeConfig) (int, error) {
	if err := candidate.Validate(); err != nil {
		return current, err
	}
	if candidate.Version <= current {
		return current, configErr(fmt.Sprintf("version %d is not newer than applied version %d", candidate.Version, current))
	}
	return candidate.Version, nil
}

// Sample is one raw value read from a tag. For a count tag Number is the delta the
// connector computed with CounterDelta; for a measurement it is the reading; for
// state, downtime and alarm tags Text carries the state or reason code.
type Sample struct {
	Address string
	Seq     int64
	TS      time.Time
	Number  float64
	Text    string
}

// Normalize maps a raw sample to a MachineEvent through its tag mapping (FR-25.9,
// BR-25.4). The result is validated before it is returned.
func (c EdgeConfig) Normalize(s Sample) (MachineEvent, error) {
	var tag *TagMapping
	for i := range c.Tags {
		if c.Tags[i].Address == s.Address {
			tag = &c.Tags[i]
			break
		}
	}
	if tag == nil {
		return MachineEvent{}, fmt.Errorf("%w: no tag mapped at %q", ErrInvalidEvent, s.Address)
	}
	var resourceID string
	for _, m := range c.Machines {
		if m.MachineID == tag.MachineID {
			resourceID = m.ResourceID
			break
		}
	}
	if (tag.Kind == KindCount || tag.Kind == KindMeasurement) && !finite(s.Number) {
		// encoding/json cannot carry NaN or Inf, and int64(Inf) is undefined.
		return MachineEvent{}, fmt.Errorf("%w: value %v at %q is not a finite number", ErrInvalidEvent, s.Number, s.Address)
	}
	e := MachineEvent{MachineID: tag.MachineID, ResourceID: resourceID, Seq: s.Seq, Kind: tag.Kind, TS: s.TS}
	switch tag.Kind {
	case KindCount:
		if s.Number != math.Trunc(s.Number) || math.Abs(s.Number) > maxCountDelta {
			return MachineEvent{}, fmt.Errorf("%w: count delta %v is not a whole number in range", ErrInvalidEvent, s.Number)
		}
		n := int64(s.Number)
		e.GoodCount = &n
	case KindState:
		e.State = s.Text
	case KindDowntime:
		e.State = "DOWN"
		if s.Text != "" {
			reason := s.Text
			e.ReasonCode = &reason
		}
	case KindAlarm:
		if s.Text != "" {
			reason := s.Text
			e.ReasonCode = &reason
		}
	case KindMeasurement:
		e.Measurement = &Measurement{Name: tag.Name, Value: s.Number, Unit: tag.Unit}
	}
	if err := e.Validate(); err != nil {
		return MachineEvent{}, err
	}
	return e, nil
}
