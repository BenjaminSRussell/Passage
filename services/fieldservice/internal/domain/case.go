package domain

import (
	"slices"
	"time"
)

// CaseStatus is the lifecycle status of a service case (FR-45.5).
type CaseStatus string

// Case statuses. RESOLVED stops the resolution clock; CLOSED and CANCELED are final.
const (
	CaseNew             CaseStatus = "NEW"
	CaseInProgress      CaseStatus = "IN_PROGRESS"
	CaseWaitingCustomer CaseStatus = "WAITING_CUSTOMER"
	CaseWaitingParts    CaseStatus = "WAITING_PARTS"
	CaseResolved        CaseStatus = "RESOLVED"
	CaseClosed          CaseStatus = "CLOSED"
	CaseCanceled        CaseStatus = "CANCELED"
)

// caseNext is the case status machine. Every open status can be resolved or
// canceled; a resolved case can only be closed (reopening is BR-45.9, a later
// slice with generic case management).
var caseNext = map[CaseStatus][]CaseStatus{
	CaseNew:             {CaseInProgress, CaseWaitingCustomer, CaseWaitingParts, CaseResolved, CaseCanceled},
	CaseInProgress:      {CaseWaitingCustomer, CaseWaitingParts, CaseResolved, CaseCanceled},
	CaseWaitingCustomer: {CaseInProgress, CaseWaitingParts, CaseResolved, CaseCanceled},
	CaseWaitingParts:    {CaseInProgress, CaseWaitingCustomer, CaseResolved, CaseCanceled},
	CaseResolved:        {CaseClosed},
}

// CanCaseTransition reports whether a case may move from one status to another.
func CanCaseTransition(from, to CaseStatus) bool {
	return slices.Contains(caseNext[from], to)
}

// Open reports whether a case is still being worked, so its clocks run or are paused.
func (s CaseStatus) Open() bool {
	return s == CaseNew || s == CaseInProgress || s == CaseWaitingCustomer || s == CaseWaitingParts
}

// PausesResolution reports whether a status stops the resolution clock (BR-45.2):
// WAITING_CUSTOMER always, WAITING_PARTS only when the profile says so. The
// response clock never pauses, because leaving NEW is the response.
func PausesResolution(s CaseStatus, pauseOnParts bool) bool {
	return s == CaseWaitingCustomer || s == CaseWaitingParts && pauseOnParts
}

// DefaultWarningPercent is the SLA warning threshold (PRD 8.45.13 default).
const DefaultWarningPercent = 75

// WarningMinutes is the elapsed business minutes at which a clock of the given
// target reaches pct percent, rounded up so the warning never fires early.
func WarningMinutes(target, pct int) int {
	return (target*pct + 99) / 100
}

// SLA clock names and alert kinds. An alert key such as "RESOLUTION_WARNING"
// records on the case that the event was emitted, so each fires once.
const (
	ClockResponse   = "RESPONSE"
	ClockResolution = "RESOLUTION"
	AlertWarning    = "WARNING"
	AlertBreach     = "BREACH"
)

// AlertKey is the key under which an emitted alert is recorded on the case.
func AlertKey(clock, kind string) string { return clock + "_" + kind }

// SLAAlert is one warning or breach to emit for a case clock.
type SLAAlert struct {
	Clock string
	Kind  string
	DueAt time.Time
}

// CaseClocks is the SLA state of a case (BR-45.2). Due and warning instants are
// absolute and recomputed whenever paused minutes change:
// due = add_business_minutes(opened, target + paused).
type CaseClocks struct {
	Status           CaseStatus
	State            string
	OpenedAt         time.Time
	RespondedAt      *time.Time
	ResponseDue      time.Time
	ResponseWarnAt   time.Time
	ResolvedAt       *time.Time
	ResolutionDue    time.Time
	ResolutionWarnAt time.Time
	PausedAt         *time.Time
	PausedMinutes    int
	Alerts           []string
}

// StartClocks returns the clocks of a case opened at opened under target.
func StartClocks(opened time.Time, cal Calendar, target Target) CaseClocks {
	return CaseClocks{
		Status: CaseNew, State: SLARunning, OpenedAt: opened, Alerts: []string{},
		ResponseDue:      cal.Add(opened, target.ResponseMinutes),
		ResponseWarnAt:   cal.Add(opened, WarningMinutes(target.ResponseMinutes, DefaultWarningPercent)),
		ResolutionDue:    cal.Add(opened, target.ResolutionMinutes),
		ResolutionWarnAt: cal.Add(opened, WarningMinutes(target.ResolutionMinutes, DefaultWarningPercent)),
	}
}

func (c CaseClocks) sent(clock, kind string) bool {
	return slices.Contains(c.Alerts, AlertKey(clock, kind))
}

// record marks an alert as emitted. A breach also marks the warning, so a clock
// first seen past its due emits the breach only and never a late warning.
func (c *CaseClocks) record(clock, kind string) {
	keys := []string{AlertKey(clock, kind)}
	if kind == AlertBreach {
		keys = append(keys, AlertKey(clock, AlertWarning))
	}
	for _, k := range keys {
		if !slices.Contains(c.Alerts, k) {
			c.Alerts = append(c.Alerts, k)
		}
	}
}

// Respond stops the response clock at now (BR-45.2: the first of an agent
// acting on the case and a work order being DISPATCHED). A late response emits
// the response breach unless the monitor already did.
func (c CaseClocks) Respond(now time.Time) (CaseClocks, []SLAAlert) {
	if c.RespondedAt != nil || !c.Status.Open() {
		return c, nil
	}
	c.Alerts = slices.Clone(c.Alerts)
	at := now
	c.RespondedAt = &at
	if now.After(c.ResponseDue) && !c.sent(ClockResponse, AlertBreach) {
		c.record(ClockResponse, AlertBreach)
		return c, []SLAAlert{{Clock: ClockResponse, Kind: AlertBreach, DueAt: c.ResponseDue}}
	}
	return c, nil
}

// Transition moves a case to status to at now and applies BR-45.2: an open pause
// is closed and its business minutes move the resolution due and warning
// instants; a pausing status opens a new pause; leaving NEW is the response; a
// clock found past due is BREACHED and stays BREACHED (also when resolved
// later); a case resolved in time is MET. It reports false when the status
// machine does not allow the move.
func (c CaseClocks) Transition(to CaseStatus, now time.Time, cal Calendar, target Target, pauseOnParts bool) (CaseClocks, []SLAAlert, bool) {
	if !CanCaseTransition(c.Status, to) {
		return c, nil, false
	}
	var alerts []SLAAlert
	if to != CaseCanceled {
		c, alerts = c.Respond(now)
	}
	c.Alerts = slices.Clone(c.Alerts)
	if c.PausedAt != nil {
		c.PausedMinutes += cal.Between(*c.PausedAt, now)
		c.PausedAt = nil
	}
	c.ResolutionDue = cal.Add(c.OpenedAt, target.ResolutionMinutes+c.PausedMinutes)
	c.ResolutionWarnAt = cal.Add(c.OpenedAt, WarningMinutes(target.ResolutionMinutes, DefaultWarningPercent)+c.PausedMinutes)
	c.Status = to
	if to == CaseResolved {
		at := now
		c.ResolvedAt = &at
	}

	if c.State == SLAMet || c.State == SLABreached {
		return c, alerts, true
	}
	switch {
	case now.After(c.ResolutionDue):
		c.State = SLABreached
		if !c.sent(ClockResolution, AlertBreach) {
			c.record(ClockResolution, AlertBreach)
			alerts = append(alerts, SLAAlert{Clock: ClockResolution, Kind: AlertBreach, DueAt: c.ResolutionDue})
		}
	case to == CaseResolved:
		c.State = SLAMet
	case to.Open() && PausesResolution(to, pauseOnParts):
		at := now
		c.PausedAt = &at
		c.State = SLAPaused
	default:
		// A canceled case keeps a RUNNING clock that nothing monitors any more.
		c.State = SLARunning
	}
	return c, alerts, true
}

// DueAlerts is what the SLA monitor emits for a case at now (BR-45.2,
// AC-45.3): a warning once a clock reaches its warning instant and a breach
// once now is past its due, each at most once, the breach winning when both are
// due. Only a RUNNING clock of an open case is monitored; the response clock
// only while the case is NEW and nobody has responded. A resolution breach moves the clock to BREACHED.
func (c CaseClocks) DueAlerts(now time.Time) (CaseClocks, []SLAAlert) {
	if c.State != SLARunning || !c.Status.Open() {
		return c, nil
	}
	c.Alerts = slices.Clone(c.Alerts)
	var out []SLAAlert
	check := func(clock string, due, warnAt time.Time) {
		switch {
		case now.After(due):
			if !c.sent(clock, AlertBreach) {
				c.record(clock, AlertBreach)
				out = append(out, SLAAlert{Clock: clock, Kind: AlertBreach, DueAt: due})
			}
		case !now.Before(warnAt):
			if !c.sent(clock, AlertWarning) {
				c.record(clock, AlertWarning)
				out = append(out, SLAAlert{Clock: clock, Kind: AlertWarning, DueAt: due})
			}
		}
	}
	if c.RespondedAt == nil && c.Status == CaseNew {
		check(ClockResponse, c.ResponseDue, c.ResponseWarnAt)
	}
	check(ClockResolution, c.ResolutionDue, c.ResolutionWarnAt)
	if now.After(c.ResolutionDue) {
		c.State = SLABreached
	}
	return c, out
}
