// Package session decides the device session lock rules (BR-32.6, FR-32.6):
// a session holds its work's lock while its heartbeat is recent, and an
// older one is STALE and holds nothing. The functions are pure over a clock.
package session

import "time"

const (
	DefaultIdleMinutes = 30
	MinIdleMinutes     = 5
	MaxIdleMinutes     = 240
	MaxSkew            = 300 * time.Second
)

type State string

const (
	Active State = "ACTIVE"
	Stale  State = "STALE"
	Ended  State = "ENDED"
)

type View struct {
	ID            string
	DeviceID      string
	UserID        string
	CurrentWorkID string
	LastSeenAt    time.Time
	Status        State
}

type WorkStatus string

const (
	WorkOpen       WorkStatus = "OPEN"
	WorkAssigned   WorkStatus = "ASSIGNED"
	WorkInProgress WorkStatus = "IN_PROGRESS"
)

// Idle clamps a configured idle timeout to its allowed range.
func Idle(minutes int) time.Duration {
	if minutes < MinIdleMinutes || minutes > MaxIdleMinutes {
		minutes = DefaultIdleMinutes
	}
	return time.Duration(minutes) * time.Minute
}

// StateAt is ACTIVE while the heartbeat is within the idle window and STALE
// once it is older; an ended session stays ended.
func StateAt(s View, now time.Time, idle time.Duration) State {
	if s.Status == Ended {
		return Ended
	}
	if now.Sub(s.LastSeenAt) > idle {
		return Stale
	}
	return Active
}

// LockHolder is the ACTIVE session holding the work, if any. A STALE session
// never holds a lock.
func LockHolder(sessions []View, workID string, now time.Time, idle time.Duration) (View, bool) {
	for _, s := range sessions {
		if s.CurrentWorkID == workID && StateAt(s, now, idle) == Active {
			return s, true
		}
	}
	return View{}, false
}

// Assignable reports whether a work can be given to another user: it is OPEN,
// or ASSIGNED with no ACTIVE session holding it (AC-32.8), since the lock
// timeout returns it to OPEN (BR-31.3).
func Assignable(status WorkStatus, workID string, sessions []View, now time.Time, idle time.Duration) bool {
	if status != WorkOpen && status != WorkAssigned {
		return false
	}
	_, held := LockHolder(sessions, workID, now, idle)
	return !held
}

// Resumed is the outcome of a heartbeat that resumes a STALE session: the
// work is re-locked only if no other ACTIVE session took it and it is still
// OPEN or ASSIGNED.
func Resumed(s View, workID string, status WorkStatus, sessions []View, now time.Time, idle time.Duration) (relocked bool, reason string) {
	if workID == "" || workID != s.CurrentWorkID {
		return false, "no work held"
	}
	for _, other := range sessions {
		if other.ID != s.ID && other.CurrentWorkID == workID && StateAt(other, now, idle) == Active {
			return false, "work is locked by another active session"
		}
	}
	if status != WorkOpen && status != WorkAssigned {
		return false, "work is " + string(status)
	}
	return true, "re-locked"
}

// Skew is the server clock minus the device clock at a heartbeat.
func Skew(server, device time.Time) time.Duration { return server.Sub(device) }

// SkewBlocked reports whether replay must be blocked for the skew (MOB-0114).
func SkewBlocked(skew time.Duration) bool { return skew > MaxSkew || skew < -MaxSkew }
