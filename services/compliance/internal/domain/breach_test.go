package domain

import (
	"errors"
	"testing"
	"time"
)

func codeOfErr(err error) string {
	var de *Error
	if errors.As(err, &de) {
		return de.Code
	}
	return ""
}

func TestBreachClocks_Deadlines(t *testing.T) {
	aware := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		regimes  []Regime
		days     int
		wantDue  []time.Time // in output order, only fixed deadlines
		wantRegs int         // total clocks
	}{
		{"GDPR authority and subjects", []Regime{RegimeGDPR}, 0, []time.Time{aware.Add(72 * time.Hour)}, 2},
		{"UK GDPR 72 h", []Regime{RegimeUKGDPR}, 0, []time.Time{aware.Add(72 * time.Hour)}, 1},
		{"CERT-In 6 h", []Regime{RegimeCERTIn}, 0, []time.Time{aware.Add(6 * time.Hour)}, 1},
		{"US state default 30 days", []Regime{RegimeUSState}, 0, []time.Time{aware.AddDate(0, 0, 30)}, 1},
		{"US state 45 days", []Regime{RegimeUSState}, 45, []time.Time{aware.AddDate(0, 0, 45)}, 1},
		{"PIPEDA has no fixed hour", []Regime{RegimePIPEDA}, 0, nil, 1},
		{"LFPDPPP has no fixed hour", []Regime{RegimeLFPDPPP}, 0, nil, 1},
		{"state window ignored without US_STATE", []Regime{RegimeUKGDPR}, 90, []time.Time{aware.Add(72 * time.Hour)}, 1},
		{"output follows regime order", []Regime{RegimeCERTIn, RegimeUKGDPR}, 0, []time.Time{aware.Add(72 * time.Hour), aware.Add(6 * time.Hour)}, 2},
		{"duplicate regimes count once", []Regime{RegimeCERTIn, RegimeCERTIn}, 0, []time.Time{aware.Add(6 * time.Hour)}, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := BreachClocks(aware, tc.regimes, tc.days)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != tc.wantRegs {
				t.Fatalf("want %d clocks, got %d: %+v", tc.wantRegs, len(got), got)
			}
			var due []time.Time
			for _, c := range got {
				if c.DeadlineAt != nil {
					due = append(due, c.DeadlineAt.UTC())
				}
			}
			if len(due) != len(tc.wantDue) {
				t.Fatalf("fixed deadlines: want %v, got %v", tc.wantDue, due)
			}
			for i := range due {
				if !due[i].Equal(tc.wantDue[i]) {
					t.Fatalf("deadline %d: want %s got %s", i, tc.wantDue[i], due[i])
				}
			}
		})
	}
}

func TestBreachClocks_AC48_19(t *testing.T) {
	aware := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	got, err := BreachClocks(aware, []Regime{RegimeGDPR}, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	if got[0].Regime != RegimeGDPR || got[0].DeadlineAt == nil || !got[0].DeadlineAt.Equal(want) {
		t.Fatalf("GDPR authority deadline: want %s, got %+v", want, got[0])
	}
}

func TestBreachClocks_Errors(t *testing.T) {
	aware := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		awareAt time.Time
		regimes []Regime
		days    int
		code    string
	}{
		{"zero aware time", time.Time{}, []Regime{RegimeGDPR}, 0, CodeBreachAwareAt},
		{"state window too short", aware, []Regime{RegimeUSState}, 29, CodeBreachStateDays},
		{"state window too long", aware, []Regime{RegimeUSState}, 61, CodeBreachStateDays},
		{"unknown regime", aware, []Regime{"NOWHERE"}, 0, CodeBreachRegime},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := BreachClocks(tc.awareAt, tc.regimes, tc.days)
			if got := codeOfErr(err); got != tc.code {
				t.Fatalf("want %s, got %q (%v)", tc.code, got, err)
			}
		})
	}
}

func TestStatusOf(t *testing.T) {
	due := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	c := Clock{Regime: RegimeGDPR, DeadlineAt: &due}
	tests := []struct {
		now  time.Time
		want ClockStatus
	}{
		{due.Add(-72 * time.Hour), ClockNotDue},
		{due.Add(-12 * time.Hour), ClockAtRisk},
		{due.Add(time.Minute), ClockOverdue},
	}
	for _, tc := range tests {
		if got := StatusOf(c, tc.now); got != tc.want {
			t.Fatalf("at %s: want %s, got %s", tc.now, tc.want, got)
		}
	}
	if got := StatusOf(Clock{Regime: RegimePIPEDA}, due); got != ClockNoFixedDeadline {
		t.Fatalf("no deadline: got %s", got)
	}
}

func TestValidateClosure_LateNeedsJustification(t *testing.T) {
	due := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	c := Clock{Regime: RegimeGDPR, DeadlineAt: &due}
	late := due.Add(time.Hour)
	if err := ValidateClosure(c, due.Add(-time.Hour), ""); err != nil {
		t.Fatalf("on time closes freely: %v", err)
	}
	if got := codeOfErr(ValidateClosure(c, late, "")); got != CodeBreachJustify {
		t.Fatalf("late without justification: want %s, got %q", CodeBreachJustify, got)
	}
	if got := codeOfErr(ValidateClosure(c, late, "    late     ")); got != CodeBreachJustify {
		t.Fatalf("padding does not count towards the justification: got %q", got)
	}
	if err := ValidateClosure(c, late, "authority portal was down until 14:00"); err != nil {
		t.Fatalf("late with justification should close: %v", err)
	}
	if err := ValidateClosure(Clock{Regime: RegimePIPEDA}, late, ""); err != nil {
		t.Fatalf("clock without fixed deadline closes freely: %v", err)
	}
}
