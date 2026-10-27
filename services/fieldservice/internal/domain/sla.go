package domain

import "time"

// SLA states of a case clock (BR-45.2).
const (
	SLARunning  = "RUNNING"
	SLAPaused   = "PAUSED"
	SLAMet      = "MET"
	SLABreached = "BREACHED"
)

// Target is the response and resolution target of an SLA profile for one priority.
type Target struct {
	Priority          int `json:"priority"`
	ResponseMinutes   int `json:"responseMinutes"`
	ResolutionMinutes int `json:"resolutionMinutes"`
}

// Clock is one SLA clock (response or resolution) of a case.
// Due = add_business_minutes(opened, target + paused), so pauses move the due
// instant without storing a relative offset.
type Clock struct {
	Opened        time.Time
	TargetMinutes int
	PausedMinutes int
	Cal           Calendar
}

// Due returns the absolute instant the clock expires.
func (c Clock) Due() time.Time {
	return c.Cal.Add(c.Opened, c.TargetMinutes+c.PausedMinutes)
}

// Elapsed returns the business minutes since opening, excluding paused minutes.
func (c Clock) Elapsed(now time.Time) int {
	return max(0, c.Cal.Between(c.Opened, now)-c.PausedMinutes)
}

// Warning reports whether elapsed time has reached pct percent of the target
// (BR-45.2 warns at 75 %).
func (c Clock) Warning(now time.Time, pct int) bool {
	return c.TargetMinutes > 0 && c.Elapsed(now)*100 >= pct*c.TargetMinutes
}

// Breached reports whether the clock has run past its due instant.
func (c Clock) Breached(now time.Time) bool {
	return now.After(c.Due())
}
