package domain

import "time"

// MaxEventAge is the oldest device timestamp the connector still publishes (BR-25.4).
const MaxEventAge = 7 * 24 * time.Hour

// Decision is the outcome of Sequencer.Accept for one event.
type Decision int

const (
	// Accepted means the event is new and should be published.
	Accepted Decision = iota
	// DuplicateDropped means seq was already seen or is older than the last seen seq.
	DuplicateDropped
	// StaleDropped means the device timestamp is older than MaxEventAge.
	StaleDropped
)

// String names the decision for logs and metrics labels.
func (d Decision) String() string {
	switch d {
	case Accepted:
		return "accepted"
	case DuplicateDropped:
		return "duplicate"
	case StaleDropped:
		return "stale"
	default:
		return "unknown"
	}
}

// Gap is an inclusive range of sequence numbers skipped before an accepted event.
type Gap struct {
	From, To int64
}

// Sequencer tracks the last accepted sequence number per machine. It is not safe
// for concurrent use; the connector runs one sequencer per ingest goroutine.
type Sequencer struct {
	last map[string]int64
}

// NewSequencer returns an empty sequencer.
func NewSequencer() *Sequencer {
	return &Sequencer{last: make(map[string]int64)}
}

// Accept applies BR-25.4 to one event. Events with seq <= the last seen seq for
// the machine are dropped as duplicates, and events older than MaxEventAge are
// dropped as stale. An accepted event whose seq skips ahead returns the missing
// range as a Gap so the connector can raise SF-0008 and replay from the buffer.
func (s *Sequencer) Accept(machineID string, seq int64, ts, now time.Time) (Decision, *Gap) {
	last, seen := s.last[machineID]
	if seen && seq <= last {
		return DuplicateDropped, nil
	}
	if now.Sub(ts) > MaxEventAge {
		return StaleDropped, nil
	}
	s.last[machineID] = seq
	if !seen || seq == last+1 {
		return Accepted, nil
	}
	return Accepted, &Gap{From: last + 1, To: seq - 1}
}

// Last returns the last accepted seq for a machine, or 0 when none was seen.
func (s *Sequencer) Last(machineID string) int64 {
	return s.last[machineID]
}
