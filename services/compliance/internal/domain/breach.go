package domain

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// Breach notification codes (FR-48.29, BR-48.18).
const (
	CodeBreachAwareAt   = "INC-1310" // aware_at missing or zero
	CodeBreachStateDays = "INC-1311" // US state window outside 30..60 days
	CodeBreachRegime    = "INC-1312" // unknown regime
	CodeBreachJustify   = "INC-1302" // deadline missed without late-notification justification
	// CodeBreachNotConfirmed refuses clocks for an incident not confirmed as a personal-data breach.
	CodeBreachNotConfirmed = "INC-1303"
	breachAtRiskHours      = 12
	minJustification       = 10
	defaultStateDays       = 30
	minStateDays           = 30
	maxStateDays           = 60
	gdprAuthorityHours     = 72
	ukICOHours             = 72
	certInHours            = 6
	stateDaysUnspecified   = 0
)

// Regime is a breach-notification regime that can apply to a confirmed breach.
type Regime string

const (
	RegimeGDPR    Regime = "GDPR"
	RegimeUKGDPR  Regime = "UK_GDPR"
	RegimeUSState Regime = "US_STATE"
	RegimePIPEDA  Regime = "PIPEDA"
	RegimeCERTIn  Regime = "CERT_IN"
	// RegimeLFPDPPP is Mexico's data protection law, one of the PRD's
	// incident.breach_regimes values: subjects are told without delay, with no fixed hour.
	RegimeLFPDPPP Regime = "LFPDPPP"
)

// Regimes lists the supported regimes in output order.
var Regimes = []Regime{RegimeGDPR, RegimeUKGDPR, RegimeUSState, RegimePIPEDA, RegimeCERTIn, RegimeLFPDPPP}

// ClockStatus is the position of a clock relative to "now".
type ClockStatus string

const (
	ClockNoFixedDeadline ClockStatus = "NO_FIXED_DEADLINE"
	ClockNotDue          ClockStatus = "NOT_DUE"
	ClockAtRisk          ClockStatus = "AT_RISK"
	ClockOverdue         ClockStatus = "OVERDUE"
)

// Clock is one notification obligation of a confirmed breach. DeadlineAt is nil
// where the regime states no fixed hour ("without undue delay", "as soon as feasible").
type Clock struct {
	Regime     Regime     `json:"regime"`
	Recipient  string     `json:"recipient"`
	Basis      string     `json:"basis"`
	DeadlineAt *time.Time `json:"deadlineAt,omitempty"`
}

// regimeOrder fixes the output order of BreachClocks.
var regimeOrder = func() map[Regime]int {
	m := make(map[Regime]int, len(Regimes))
	for i, r := range Regimes {
		m[r] = i
	}
	return m
}()

// ValidRegime reports whether r is a supported regime.
func ValidRegime(r Regime) bool {
	_, ok := regimeOrder[r]
	return ok
}

// BreachClocks returns the notification clocks for a confirmed personal-data
// breach. Clocks start at awareAt, never at detection (BR-48.18), and deadlines
// are UTC instants. stateDays applies to US_STATE only: 0 means the default of 30,
// otherwise it must be within 30..60. It is ignored when US_STATE is not requested.
func BreachClocks(awareAt time.Time, regimes []Regime, stateDays int) ([]Clock, error) {
	if awareAt.IsZero() {
		return nil, &Error{Code: CodeBreachAwareAt, Detail: "awareAt is required; the breach clock starts at awareness"}
	}
	for _, r := range regimes {
		if !ValidRegime(r) {
			return nil, &Error{Code: CodeBreachRegime, Detail: fmt.Sprintf("regime %q is not supported", r)}
		}
		if r == RegimeUSState {
			if stateDays == stateDaysUnspecified {
				stateDays = defaultStateDays
			}
			if stateDays < minStateDays || stateDays > maxStateDays {
				return nil, &Error{Code: CodeBreachStateDays, Detail: fmt.Sprintf("US state window %d days is outside %d..%d", stateDays, minStateDays, maxStateDays)}
			}
		}
	}
	aware := awareAt.UTC()
	seen := map[Regime]bool{}
	var out []Clock
	for _, r := range regimes {
		if seen[r] {
			continue
		}
		seen[r] = true
		switch r {
		case RegimeGDPR:
			out = append(out,
				hourClock(r, "supervisory authority", "GDPR Art. 33: within 72 h of awareness", aware, gdprAuthorityHours),
				Clock{Regime: r, Recipient: "data subjects", Basis: "GDPR Art. 34: without undue delay when high risk"})
		case RegimeUKGDPR:
			out = append(out, hourClock(r, "ICO", "UK GDPR Art. 33: within 72 h of awareness", aware, ukICOHours))
		case RegimeUSState:
			d := aware.AddDate(0, 0, stateDays)
			out = append(out, Clock{Regime: r, Recipient: "state attorney general and residents", Basis: fmt.Sprintf("US state law: %d days (per state table)", stateDays), DeadlineAt: &d})
		case RegimePIPEDA:
			out = append(out, Clock{Regime: r, Recipient: "Privacy Commissioner and individuals", Basis: "PIPEDA: as soon as feasible"})
		case RegimeCERTIn:
			out = append(out, hourClock(r, "CERT-In", "CERT-In directions: 6 h for reportable incidents", aware, certInHours))
		case RegimeLFPDPPP:
			out = append(out, Clock{Regime: r, Recipient: "data subjects", Basis: "LFPDPPP: without delay when rights are significantly affected"})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return regimeOrder[out[i].Regime] < regimeOrder[out[j].Regime] })
	return out, nil
}

func hourClock(r Regime, recipient, basis string, aware time.Time, hours int) Clock {
	d := aware.Add(time.Duration(hours) * time.Hour)
	return Clock{Regime: r, Recipient: recipient, Basis: basis, DeadlineAt: &d}
}

// StatusOf reports where a clock stands at now. A clock inside its last 12 hours is AT_RISK,
// which is what the deadline monitor pages on.
func StatusOf(c Clock, now time.Time) ClockStatus {
	if c.DeadlineAt == nil {
		return ClockNoFixedDeadline
	}
	switch {
	case now.After(*c.DeadlineAt):
		return ClockOverdue
	case c.DeadlineAt.Sub(now) <= breachAtRiskHours*time.Hour:
		return ClockAtRisk
	default:
		return ClockNotDue
	}
}

// ValidateClosure decides whether a clock may be closed at now. A missed fixed
// deadline needs a late-notification justification (BR-48.18). Clocks without a
// fixed deadline close freely. Surrounding spaces do not count towards the
// justification's minimum length.
func ValidateClosure(c Clock, now time.Time, justification string) error {
	if StatusOf(c, now) != ClockOverdue {
		return nil
	}
	if utf8.RuneCountInString(strings.TrimSpace(justification)) < minJustification {
		return &Error{Code: CodeBreachJustify, Detail: fmt.Sprintf("%s notification missed its deadline; a late-notification justification of at least %d characters is required", c.Regime, minJustification)}
	}
	return nil
}
