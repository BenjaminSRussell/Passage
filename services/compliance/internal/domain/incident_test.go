package domain

import (
	"errors"
	"testing"
	"time"

	"pgregory.net/rapid"
)

var incidentAware = time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)

func TestApplyIncidentTransitionLifecycle(t *testing.T) {
	cases := []struct {
		name       string
		in         IncidentTransitionInput
		want       IncidentStatus
		pageOnCall bool
		code       string
	}{
		{name: "open a Sev-2 incident", in: IncidentTransitionInput{Severity: SeverityTwo, Source: SourceAlert, To: IncidentTriage}, want: IncidentTriage},
		{name: "open Sev-1 pages on-call", in: IncidentTransitionInput{Severity: SeverityOne, Source: SourcePenTest, To: IncidentTriage}, want: IncidentTriage, pageOnCall: true},
		{name: "contain after triage", in: IncidentTransitionInput{Severity: SeverityThree, Source: SourceVendor, From: IncidentTriage, To: IncidentContained}, want: IncidentContained},
		{name: "skipping a step refused", in: IncidentTransitionInput{Severity: SeverityThree, Source: SourceVendor, From: IncidentTriage, To: IncidentEradicated}, code: CodeIncidentStep},
		{name: "moving backwards refused", in: IncidentTransitionInput{Severity: SeverityThree, Source: SourceVendor, From: IncidentContained, To: IncidentTriage}, code: CodeIncidentStep},
		{name: "missing target refused", in: IncidentTransitionInput{Severity: SeverityThree, Source: SourceVendor, From: IncidentTriage}, code: CodeIncidentStep},
		{name: "opening must start at triage", in: IncidentTransitionInput{Severity: SeverityThree, Source: SourceVendor, To: IncidentContained}, code: CodeIncidentStep},
		{name: "unknown severity", in: IncidentTransitionInput{Severity: "SEV9", Source: SourceAlert, To: IncidentTriage}, code: CodeIncidentSeverity},
		{name: "unknown source", in: IncidentTransitionInput{Severity: SeverityTwo, Source: "RUMOUR", To: IncidentTriage}, code: CodeIncidentSource},
		{name: "breach without regime", in: IncidentTransitionInput{Severity: SeverityOne, Source: SourceAlert, To: IncidentTriage, PersonalDataBreach: true, AwareAt: incidentAware}, code: CodeIncidentRegimes},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := ApplyIncidentTransition(c.in)
			if c.code != "" {
				expectIncidentCode(t, err, c.code)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if out.Status != c.want || out.PageOnCall != c.pageOnCall {
				t.Fatalf("got %+v", out)
			}
		})
	}
}

func TestIncidentBreachClosure(t *testing.T) {
	base := IncidentTransitionInput{
		Severity: SeverityTwo, Source: SourceAlert, From: IncidentRecovered, To: IncidentClosed,
		PersonalDataBreach: true, AwareAt: incidentAware, Regimes: []Regime{RegimeGDPR},
		Now: incidentAware.Add(5 * 24 * time.Hour),
	}
	expectIncidentCode(t, errOfIncident(ApplyIncidentTransition(base)), CodeBreachJustify)

	base.Justification = "authority notified on day five after forensic triage"
	out, err := ApplyIncidentTransition(base)
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != IncidentClosed || len(out.Clocks) != 2 {
		t.Fatalf("closure with justification should pass: %+v", out)
	}
	if out.Clocks[0].Status != ClockOverdue || out.Clocks[0].DeadlineAt == nil || !out.Clocks[0].DeadlineAt.Equal(incidentAware.Add(72*time.Hour)) {
		t.Fatalf("GDPR authority clock must be overdue at 72 h: %+v", out.Clocks[0])
	}
}

// A notification sent within 72 h meets the clock: closing weeks later needs no
// justification. One sent after the deadline still does (BR-48.18).
func TestIncidentClosureUsesNotificationTime(t *testing.T) {
	in := IncidentTransitionInput{
		Severity: SeverityTwo, Source: SourceAlert, From: IncidentRecovered, To: IncidentClosed,
		PersonalDataBreach: true, AwareAt: incidentAware, Regimes: []Regime{RegimeGDPR, RegimeCERTIn},
		NotifiedAt: map[Regime]time.Time{RegimeGDPR: incidentAware.Add(48 * time.Hour), RegimeCERTIn: incidentAware.Add(5 * time.Hour)},
		Now:        incidentAware.Add(30 * 24 * time.Hour),
	}
	out, err := ApplyIncidentTransition(in)
	if err != nil {
		t.Fatalf("clocks met in time must close freely: %v", err)
	}
	for _, c := range out.Clocks {
		if c.DeadlineAt != nil && (!c.Met || c.Late || c.NotifiedAt == nil || c.Status != ClockNotDue) {
			t.Fatalf("clock should be met: %+v", c)
		}
	}
	in.NotifiedAt[RegimeCERTIn] = incidentAware.Add(7 * time.Hour)
	expectIncidentCode(t, errOfIncident(ApplyIncidentTransition(in)), CodeBreachJustify)

	in.NotifiedAt = map[Regime]time.Time{"MARS": incidentAware}
	expectIncidentCode(t, errOfIncident(ApplyIncidentTransition(in)), CodeBreachRegime)
}

func TestIncidentBreachClocksOnEarlierStep(t *testing.T) {
	out, err := ApplyIncidentTransition(IncidentTransitionInput{
		Severity: SeverityOne, Source: SourceAlert, From: IncidentTriage, To: IncidentContained,
		PersonalDataBreach: true, AwareAt: incidentAware, Regimes: []Regime{RegimeCERTIn},
		Now: incidentAware.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Clocks) != 1 || out.Clocks[0].Status != ClockAtRisk {
		t.Fatalf("CERT-In 6 h clock at +1 h should be AT_RISK (inside its last 12 h): %+v", out.Clocks)
	}
}

func TestIncidentStepProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		i := rapid.IntRange(0, len(incidentSteps)-2).Draw(t, "from")
		from, to := incidentSteps[i], incidentSteps[i+1]
		in := IncidentTransitionInput{Severity: SeverityThree, Source: SourceAlert, From: from, To: to, Now: incidentAware}
		if _, err := ApplyIncidentTransition(in); err != nil {
			t.Fatalf("%s -> %s must be allowed: %v", from, to, err)
		}
		if i+2 < len(incidentSteps) {
			in.To = incidentSteps[i+2]
			_, err := ApplyIncidentTransition(in)
			var de *Error
			if !errors.As(err, &de) || de.Code != CodeIncidentStep {
				t.Fatalf("skipping from %s to %s must be refused, got %v", from, in.To, err)
			}
		}
	})
}

func expectIncidentCode(t *testing.T, err error, code string) {
	t.Helper()
	expectPrivacyCode(t, err, code)
}

func errOfIncident(_ IncidentTransitionOutcome, err error) error { return err }
