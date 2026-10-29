package domain

import (
	"fmt"
	"time"
)

// Security incident codes (FR-48.29). Breach clock codes are in breach.go.
const (
	CodeIncidentSeverity = "INC-1320" // severity outside Sev-1..Sev-4
	CodeIncidentSource   = "INC-1321" // unknown incident source
	CodeIncidentStep     = "INC-1322" // status may only advance one step at a time
	CodeIncidentRegimes  = "INC-1323" // a confirmed personal-data breach names no regime
)

// IncidentSeverity is Sev-1 (most severe) to Sev-4.
type IncidentSeverity string

const (
	SeverityOne   IncidentSeverity = "SEV1"
	SeverityTwo   IncidentSeverity = "SEV2"
	SeverityThree IncidentSeverity = "SEV3"
	SeverityFour  IncidentSeverity = "SEV4"
)

// IncidentSource says how an incident was raised.
type IncidentSource string

const (
	SourceAlert      IncidentSource = "ALERT"
	SourceUserReport IncidentSource = "USER_REPORT"
	SourceVendor     IncidentSource = "VENDOR"
	SourcePenTest    IncidentSource = "PEN_TEST"
)

// IncidentStatus is the lifecycle of an incident (9.48.4 security_incident).
type IncidentStatus string

const (
	IncidentTriage     IncidentStatus = "TRIAGE"
	IncidentContained  IncidentStatus = "CONTAINED"
	IncidentEradicated IncidentStatus = "ERADICATED"
	IncidentRecovered  IncidentStatus = "RECOVERED"
	IncidentClosed     IncidentStatus = "CLOSED"
)

var incidentSteps = []IncidentStatus{IncidentTriage, IncidentContained, IncidentEradicated, IncidentRecovered, IncidentClosed}

// IncidentTransitionInput moves an incident one step. From is empty when the
// incident is opened, in which case To must be TRIAGE.
type IncidentTransitionInput struct {
	Severity           IncidentSeverity `json:"severity"`
	Source             IncidentSource   `json:"source"`
	From               IncidentStatus   `json:"from,omitempty"`
	To                 IncidentStatus   `json:"to"`
	AwareAt            time.Time        `json:"awareAt"`
	PersonalDataBreach bool             `json:"personalDataBreach"`
	Regimes            []Regime         `json:"regimes,omitempty"`
	USStateWindowDays  int              `json:"usStateWindowDays,omitempty"`
	// NotifiedAt records when each regime's fixed-deadline notification was sent
	// (regimes_json notified_at); a clock notified by its deadline is met.
	NotifiedAt    map[Regime]time.Time `json:"notifiedAt,omitempty"`
	Justification string               `json:"justification,omitempty"`
	Now           time.Time            `json:"-"`
}

// ClockReport is a breach clock with its status at the transition instant. Met
// is true once the notification went out by the deadline; Late once it went out,
// or is still unsent, after it.
type ClockReport struct {
	Clock
	Status     ClockStatus `json:"status"`
	NotifiedAt *time.Time  `json:"notifiedAt,omitempty"`
	Met        bool        `json:"met"`
	Late       bool        `json:"late"`
}

// IncidentTransitionOutcome is the incident after a valid transition.
type IncidentTransitionOutcome struct {
	Status     IncidentStatus `json:"status"`
	PageOnCall bool           `json:"pageOnCall"`
	Clocks     []ClockReport  `json:"clocks"`
}

// ApplyIncidentTransition validates one incident step. A Sev-1 incident links its
// on-call page (FR-48.29 notes). A confirmed personal-data breach computes its clocks
// from awareAt. Closing the incident requires a late-notification justification
// for every fixed-deadline clock whose notification was sent after its deadline,
// or is still unsent once it passed (BR-48.18); a clock notified in time closes
// freely however long the incident stays open.
func ApplyIncidentTransition(in IncidentTransitionInput) (IncidentTransitionOutcome, error) {
	switch in.Severity {
	case SeverityOne, SeverityTwo, SeverityThree, SeverityFour:
	default:
		return IncidentTransitionOutcome{}, &Error{Code: CodeIncidentSeverity, Detail: fmt.Sprintf("severity %q must be SEV1..SEV4", in.Severity)}
	}
	switch in.Source {
	case SourceAlert, SourceUserReport, SourceVendor, SourcePenTest:
	default:
		return IncidentTransitionOutcome{}, &Error{Code: CodeIncidentSource, Detail: fmt.Sprintf("source %q is not supported", in.Source)}
	}
	from, err := incidentIndex(in.From, -1)
	if err != nil {
		return IncidentTransitionOutcome{}, err
	}
	to, err := incidentIndex(in.To, -2)
	if err != nil {
		return IncidentTransitionOutcome{}, err
	}
	if to != from+1 {
		return IncidentTransitionOutcome{}, &Error{Code: CodeIncidentStep, Detail: fmt.Sprintf("status may move from %q only to the next step, not %q", in.From, in.To)}
	}
	out := IncidentTransitionOutcome{Status: in.To, PageOnCall: in.Severity == SeverityOne, Clocks: []ClockReport{}}
	if in.PersonalDataBreach {
		if len(in.Regimes) == 0 {
			return IncidentTransitionOutcome{}, &Error{Code: CodeIncidentRegimes, Detail: "a confirmed personal-data breach must name at least one regime"}
		}
		clocks, err := BreachClocks(in.AwareAt, in.Regimes, in.USStateWindowDays)
		if err != nil {
			return IncidentTransitionOutcome{}, err
		}
		for r := range in.NotifiedAt {
			if !ValidRegime(r) {
				return IncidentTransitionOutcome{}, &Error{Code: CodeBreachRegime, Detail: fmt.Sprintf("notifiedAt names regime %q, which is not supported", r)}
			}
		}
		for _, c := range clocks {
			rep := ClockReport{Clock: c, Status: StatusOf(c, in.Now)}
			sentAt := in.Now
			if at, ok := in.NotifiedAt[c.Regime]; ok && !at.IsZero() && c.DeadlineAt != nil {
				at = at.UTC()
				rep.NotifiedAt, sentAt = &at, at
			}
			if c.DeadlineAt != nil {
				rep.Late = sentAt.After(*c.DeadlineAt)
				rep.Met = rep.NotifiedAt != nil && !rep.Late
				if rep.NotifiedAt != nil {
					rep.Status = ClockNotDue
					if rep.Late {
						rep.Status = ClockOverdue
					}
				}
			}
			if in.To == IncidentClosed && rep.Late {
				if err := ValidateClosure(c, sentAt, in.Justification); err != nil {
					return IncidentTransitionOutcome{}, err
				}
			}
			out.Clocks = append(out.Clocks, rep)
		}
	}
	return out, nil
}

// incidentIndex places a status in the lifecycle. The empty status maps to the
// given sentinel: -1 before TRIAGE for a new incident; -2 marks a missing target, which is refused.
func incidentIndex(s IncidentStatus, empty int) (int, error) {
	if s == "" {
		if empty == -2 {
			return 0, &Error{Code: CodeIncidentStep, Detail: "a target status is required"}
		}
		return empty, nil
	}
	for i, step := range incidentSteps {
		if step == s {
			return i, nil
		}
	}
	return 0, &Error{Code: CodeIncidentStep, Detail: fmt.Sprintf("status %q is not part of the incident lifecycle", s)}
}
