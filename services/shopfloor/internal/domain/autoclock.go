package domain

import "time"

// Catalog code of the auto clock-out rule (README).
const CodeAutoClockOutInvalid = "SF-0130"

const (
	// FlagAutoClockOut marks a clock-out the server wrote for a worker who left a
	// clock-in open (D2h). A supervisor reviews it and corrects its time.
	FlagAutoClockOut = "auto-clock-out"
	// DefaultAutoClockOutHours is how long a clock-in may stay open before the sweep
	// closes it: the registration limit of 24 hours is far above a shift.
	DefaultAutoClockOutHours = 16
	// MinAutoClockOutHours and MaxAutoClockOutHours bound the threshold a caller may set.
	MinAutoClockOutHours = 4
	MaxAutoClockOutHours = 24
)

// OpenShift is a worker whose latest clock punch is a clock-in.
type OpenShift struct {
	WorkerID       string
	RegistrationID string
	ClockedInAt    time.Time
}

// AutoClockOut is the clock-out the sweep writes: at the clock-in plus the threshold,
// so the closed segment is exactly the threshold long and the day cannot be inflated by
// how late the sweep ran.
type AutoClockOut struct {
	WorkerID    string
	ClockInID   string
	ClockedInAt time.Time
	ClockOutAt  time.Time
}

// CheckAutoClockOutHours validates the threshold in hours.
func CheckAutoClockOutHours(hours int) error {
	if hours < MinAutoClockOutHours || hours > MaxAutoClockOutHours {
		return &Error{Code: CodeAutoClockOutInvalid, Detail: "afterHours must be 4 to 24"}
	}
	return nil
}

// PlanAutoClockOut returns a clock-out for every open shift older than afterHours at
// now, oldest first. A shift that is not yet that old, or that starts in the future
// (device clock ahead), is left open.
func PlanAutoClockOut(open []OpenShift, now time.Time, afterHours int) []AutoClockOut {
	limit := time.Duration(afterHours) * time.Hour
	var out []AutoClockOut
	for _, o := range open {
		if now.Sub(o.ClockedInAt) < limit {
			continue
		}
		out = append(out, AutoClockOut{WorkerID: o.WorkerID, ClockInID: o.RegistrationID, ClockedInAt: o.ClockedInAt, ClockOutAt: o.ClockedInAt.Add(limit)})
	}
	return out
}
