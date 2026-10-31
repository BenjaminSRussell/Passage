package domain

import (
	"sort"
	"time"
)

// Machine states and event kinds (BR-25.4, FR-25.11).
const (
	MachineRunning = "RUNNING"
	MachineIdle    = "IDLE"
	MachineDown    = "DOWN"
	MachineSetup   = "SETUP"
	MachineOff     = "OFF"

	EventCount       = "count"
	EventState       = "state"
	EventDowntime    = "downtime"
	EventAlarm       = "alarm"
	EventMeasurement = "measurement"

	// AndonAfter is how long a DOWN machine may stay down before an andon alert.
	AndonAfter = 10 * time.Minute
	// AndonClearAfter is how long the condition must stay clear before an alert closes.
	AndonClearAfter = 5 * time.Minute
	// MaxEventAge bounds how old an edge event may be and still be accepted (BR-25.4).
	MaxEventAge = 7 * 24 * time.Hour
)

// MachineEvent is one normalized edge event. Index is its position in the
// submitted batch so the caller can match accepted events back to their payload.
type MachineEvent struct {
	Index       int
	Seq         int64
	Kind        string
	At          time.Time
	GoodCount   int64
	RejectCount int64
	State       string
}

// SeqGap is a run of sequence numbers that did not arrive.
type SeqGap struct {
	From, To int64
}

// Classified is the outcome of applying BR-25.4 to one machine's batch.
type Classified struct {
	// Accepted is in sequence order and every Seq is above the previous one.
	Accepted []MachineEvent
	// Late lists events at or below lastSeq, in sequence order and once each.
	// A seq at or below the last seen seq is not a duplicate by itself: a run
	// reported as a gap is replayed below the high-water mark, and an earlier
	// batch can commit after a later one. The store keeps a late event unless
	// (machine, seq) is already stored, and counts it as dropped otherwise.
	Late []MachineEvent
	// Dropped counts events older than MaxEventAge and repeats of a seq within the batch.
	Dropped int
	// Stale counts the subset of Dropped that was too old.
	Stale int
	// Gaps lists missing sequence runs between accepted events.
	Gaps []SeqGap
}

// Classify applies BR-25.4 to a machine batch given the last seq already stored.
// Events are considered in seq order. Stale events and repeats within the batch
// are dropped; events at or below lastSeq go to Late for the store to check
// against what it holds (BR-25.4 "seq ≤ last seen" means already seen, and a
// gap replay was never seen). A gap is reported only above lastSeq and only once
// a machine has a sequence history (lastSeq > 0).
func Classify(lastSeq int64, now time.Time, in []MachineEvent) Classified {
	sorted := make([]MachineEvent, len(in))
	copy(sorted, in)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Seq < sorted[j].Seq })
	var c Classified
	last := lastSeq
	for _, e := range sorted {
		if now.Sub(e.At) > MaxEventAge {
			c.Dropped++
			c.Stale++
			continue
		}
		if e.Seq <= lastSeq {
			if n := len(c.Late); n > 0 && c.Late[n-1].Seq == e.Seq {
				c.Dropped++
				continue
			}
			c.Late = append(c.Late, e)
			continue
		}
		if e.Seq <= last {
			c.Dropped++
			continue
		}
		if last > 0 && e.Seq > last+1 {
			c.Gaps = append(c.Gaps, SeqGap{From: last + 1, To: e.Seq - 1})
		}
		c.Accepted = append(c.Accepted, e)
		last = e.Seq
	}
	return c
}

// DowntimeStep is what a state report does to the machine's open downtime.
type DowntimeStep int

const (
	// DowntimeNone leaves downtime unchanged.
	DowntimeNone DowntimeStep = iota
	// DowntimeStart opens a downtime record.
	DowntimeStart
	// DowntimeEnd closes the open downtime record.
	DowntimeEnd
)

// DowntimeStepFor applies FR-25.11: a DOWN report opens a downtime when none is
// open, and any other known state closes the open one.
func DowntimeStepFor(open bool, state string) DowntimeStep {
	switch {
	case state == MachineDown && !open:
		return DowntimeStart
	case state != "" && state != MachineDown && open:
		return DowntimeEnd
	}
	return DowntimeNone
}

// AndonDue reports whether a downtime that began at start has lasted past the
// andon threshold at now (FR-25.11, AC-25.6).
func AndonDue(start, now time.Time) bool {
	return now.Sub(start) > AndonAfter
}

// AndonCleared reports whether the condition has stayed clear for AndonClearAfter
// since the downtime ended (BR-25.10).
func AndonCleared(ended, now time.Time) bool {
	return now.Sub(ended) >= AndonClearAfter
}
