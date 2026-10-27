package domain

import (
	"errors"
	"time"
)

// ErrDowntimeSpan is returned when a downtime window is empty or shorter than one minute.
var ErrDowntimeSpan = errors.New("downtime must end after it starts and span at least one minute")

// LifecycleAllowed reports whether an asset may move between lifecycle states (FR-44.2).
// ACTIVE -> INACTIVE, INACTIVE -> DISPOSED, and CREATED -> DISPOSED (scrapped before service).
// Activation stays a separate action. DISPOSED is final.
func LifecycleAllowed(from, to string) bool {
	switch {
	case from == "ACTIVE" && to == "INACTIVE":
		return true
	case from == "INACTIVE" && to == "DISPOSED":
		return true
	case from == "CREATED" && to == "DISPOSED":
		return true
	}
	return false
}

// DowntimeMinutes returns the whole minutes in [from, to) for a downtime window (BR-44.5).
func DowntimeMinutes(from, to time.Time) (int, error) {
	if !to.After(from) {
		return 0, ErrDowntimeSpan
	}
	m := int(to.Sub(from) / time.Minute)
	if m < 1 {
		return 0, ErrDowntimeSpan
	}
	return m, nil
}
