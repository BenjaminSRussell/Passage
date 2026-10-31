package domain

import "testing"

func TestIncidentOutcomeAndRecordable(t *testing.T) {
	cases := []struct {
		name    string
		facts   IncidentFacts
		outcome string
		record  bool
	}{
		{"first aid only", IncidentFacts{}, OutcomeNone, false},
		{"death beats days away", IncidentFacts{Death: true, DaysAway: 3}, OutcomeDeath, true},
		{"three days away", IncidentFacts{DaysAway: 3}, OutcomeDaysAway, true},
		{"restricted work", IncidentFacts{DaysRestricted: 2}, OutcomeRestricted, true},
		{"days away beats restricted", IncidentFacts{DaysAway: 1, DaysRestricted: 9}, OutcomeDaysAway, true},
		{"medical treatment", IncidentFacts{MedicalTreatment: true}, OutcomeOther, true},
		{"loss of consciousness", IncidentFacts{LossOfConsciousness: true}, OutcomeOther, true},
	}
	for _, c := range cases {
		if got := c.facts.Outcome(); got != c.outcome {
			t.Errorf("%s: outcome %s, want %s", c.name, got, c.outcome)
		}
		if got := c.facts.Recordable(); got != c.record {
			t.Errorf("%s: recordable %v, want %v", c.name, got, c.record)
		}
	}
}

func TestResolveRecordableOverrideNeedsReason(t *testing.T) {
	away := IncidentFacts{DaysAway: 3}
	no := false
	if got, err := ResolveRecordable(away, nil, ""); err != nil || !got {
		t.Fatalf("default = %v, %v; want recordable", got, err)
	}
	if got, err := ResolveRecordable(away, &no, "short"); err == nil || err.Code != CodeIncidentRecordable || got {
		t.Fatalf("override without a reason = %v, %v; want %s", got, err, CodeIncidentRecordable)
	}
	if got, err := ResolveRecordable(away, &no, "reviewed with the safety officer"); err != nil || got {
		t.Fatalf("justified override = %v, %v; want not recordable", got, err)
	}
	yes := true
	if _, err := ResolveRecordable(IncidentFacts{}, &yes, ""); err == nil {
		t.Fatal("marking a first-aid case recordable without a reason must be refused")
	}
	same := true
	if got, err := ResolveRecordable(away, &same, ""); err != nil || !got {
		t.Fatalf("an override equal to the default needs no reason, got %v, %v", got, err)
	}
}

func TestNextIncidentStatus(t *testing.T) {
	if err := NextIncidentStatus(IncidentReported, IncidentClosed, "a", "b"); err == nil || err.Code != CodeIncidentState {
		t.Errorf("REPORTED to CLOSED = %v, want %s", err, CodeIncidentState)
	}
	if err := NextIncidentStatus(IncidentClosed, IncidentUnderReview, "a", "b"); err == nil {
		t.Error("a closed incident cannot reopen")
	}
	if err := NextIncidentStatus(IncidentReported, IncidentUnderReview, "a", "a"); err != nil {
		t.Errorf("the reporter may start the review: %v", err)
	}
	if err := NextIncidentStatus(IncidentUnderReview, IncidentClosed, "a", "a"); err == nil || err.Code != CodeIncidentSelfClose {
		t.Errorf("reporter closing own incident = %v, want %s", err, CodeIncidentSelfClose)
	}
	if err := NextIncidentStatus(IncidentUnderReview, IncidentClosed, "a", "b"); err != nil {
		t.Errorf("another admin closes: %v", err)
	}
}

func TestSummarize(t *testing.T) {
	s := Summarize([]OSHARow{
		{OutcomeDeath, "INJURY", 0, 0},
		{OutcomeDaysAway, "INJURY", 3, 5},
		{OutcomeDaysAway, "RESPIRATORY", 10, 0},
		{OutcomeRestricted, "SKIN_DISORDER", 0, 7},
		{OutcomeOther, "HEARING_LOSS", 0, 0},
	})
	if s.Deaths != 1 || s.DaysAwayCases != 2 || s.RestrictedCases != 1 || s.OtherRecordable != 1 || s.TotalDaysAway != 13 || s.TotalDaysRestricted != 12 {
		t.Errorf("summary = %+v", s)
	}
	if s.ByType["INJURY"] != 2 || s.ByType["POISONING"] != 0 || len(s.ByType) != len(IncidentTypes) {
		t.Errorf("by type = %v", s.ByType)
	}
}
