package domain

import "testing"

func TestAgreementCanMove(t *testing.T) {
	cases := []struct {
		from, to string
		ok       bool
	}{
		{AgreementDraft, AgreementActive, true},
		{AgreementDraft, AgreementCanceled, true},
		{AgreementDraft, AgreementSuspended, false},
		{AgreementActive, AgreementSuspended, true},
		{AgreementSuspended, AgreementActive, true},
		{AgreementActive, AgreementCanceled, true},
		{AgreementSuspended, AgreementExpired, true},
		{AgreementActive, AgreementDraft, false},
		{AgreementCanceled, AgreementActive, false},
		{AgreementExpired, AgreementActive, false},
		{AgreementActive, AgreementActive, false},
	}
	for _, c := range cases {
		if got := AgreementCanMove(c.from, c.to); got != c.ok {
			t.Errorf("%s to %s = %v, want %v", c.from, c.to, got, c.ok)
		}
	}
}

func TestAgreementCoversAndEnded(t *testing.T) {
	end := "2026-06-30"
	if AgreementEnded(nil, "2099-01-01") {
		t.Error("an open-ended agreement never ends")
	}
	if AgreementEnded(&end, "2026-06-30") || !AgreementEnded(&end, "2026-07-01") {
		t.Error("validTo is inclusive: the last day is covered, the next is not")
	}
	if !AgreementCovers("2026-01-01", &end, "2026-01-01") || AgreementCovers("2026-01-01", &end, "2025-12-31") {
		t.Error("validFrom is inclusive")
	}
}

func TestVisitIncluded(t *testing.T) {
	line := AgreementLine{IncludedVisits: 2}
	if !VisitIncluded(line, 1) || VisitIncluded(line, 2) {
		t.Error("two included visits allow the first two only")
	}
	if VisitIncluded(AgreementLine{}, 0) {
		t.Error("a line with no included visits includes none")
	}
}
