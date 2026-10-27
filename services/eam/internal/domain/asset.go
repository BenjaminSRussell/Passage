package domain

import (
	"fmt"
	"time"
)

// CodeNonMonotonic is EAM-CTR-001: a cumulative counter reading went backwards.
const CodeNonMonotonic = "EAM-CTR-001"

// ReadingError describes a rejected counter reading.
type ReadingError struct {
	Code   string
	Detail string
}

func (e *ReadingError) Error() string { return e.Code + ": " + e.Detail }

// CheckReading implements FR-44.3: cumulative counters are non-decreasing
// unless the reset flag is set; resettable counters accept any value.
func CheckReading(cumulative bool, last, value float64, reset bool) error {
	if !cumulative || reset || value >= last {
		return nil
	}
	return &ReadingError{Code: CodeNonMonotonic,
		Detail: fmt.Sprintf("reading %g is below last reading %g for cumulative counter; set the reset flag or correct the value", value, last)}
}

// UsageWindow is the rolling window for the average daily usage (FR-44.3,
// parameter "Counter avg-usage window", default 30 days).
const UsageWindow = 30 * 24 * time.Hour

// UsagePoint is one stored reading as the usage calculation sees it.
type UsagePoint struct {
	At    time.Time
	Value float64
	Reset bool
}

// RollingUsage is the average daily usage over points (FR-44.3): the sum of
// the increments between consecutive readings divided by the days between the
// first and the last reading. A reset reading, or a drop on a resettable
// counter, counts from zero. points must be ordered by time and already cut
// to the window. ok is false when the points span no time, so the caller
// keeps its previous average.
func RollingUsage(points []UsagePoint) (usage float64, ok bool) {
	if len(points) < 2 {
		return 0, false
	}
	days := points[len(points)-1].At.Sub(points[0].At).Hours() / 24
	if days <= 0 {
		return 0, false
	}
	var total float64
	for i := 1; i < len(points); i++ {
		prev, cur := points[i-1], points[i]
		if cur.Reset || cur.Value < prev.Value {
			total += cur.Value
			continue
		}
		total += cur.Value - prev.Value
	}
	return total / days, true
}

// ActiveState is the lifecycle rule for work orders (FR-44.2): only ACTIVE
// assets receive work orders.
func ActiveState(state string) bool { return state == "ACTIVE" }

// ActivationAllowed reports whether an asset may move from CREATED to ACTIVE.
func ActivationAllowed(state string) bool { return state == "CREATED" || state == "INACTIVE" }
