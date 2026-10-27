package domain

import "time"

// Ring is a stage of a connector version rollout (FR-25.19): canary, then ring1,
// then all plant nodes.
type Ring string

const (
	RingCanary Ring = "canary"
	RingOne    Ring = "ring1"
	RingAll    Ring = "all"
)

// RolloutHealthyWindow is how long a ring must stay healthy before promotion.
const RolloutHealthyWindow = 24 * time.Hour

// RollbackWindow is how long an upgraded connector may go without a heartbeat
// before it is rolled back to the previous binary.
const RollbackWindow = 5 * time.Minute

// NextRing returns the ring a healthy rollout promotes to. ok is false for the
// last ring and for an unknown ring.
func NextRing(r Ring) (next Ring, ok bool) {
	switch r {
	case RingCanary:
		return RingOne, true
	case RingOne:
		return RingAll, true
	default:
		return r, false
	}
}

// CanPromote reports whether a ring has been healthy for the whole healthy window.
func CanPromote(healthySince, now time.Time) bool {
	return now.Sub(healthySince) >= RolloutHealthyWindow
}

// RollbackDue reports whether an upgrade must roll back (FR-25.19). That happens
// once RollbackWindow has passed since upgradedAt with no heartbeat at or after
// upgradedAt. A nil lastHeartbeat means none has been seen.
func RollbackDue(upgradedAt time.Time, lastHeartbeat *time.Time, now time.Time) bool {
	if now.Sub(upgradedAt) < RollbackWindow {
		return false
	}
	if lastHeartbeat != nil && !lastHeartbeat.Before(upgradedAt) {
		return false
	}
	return true
}
