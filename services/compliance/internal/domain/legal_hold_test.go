package domain

import (
	"testing"
	"time"
)

func TestRetentionEnds_DocumentedDefaults(t *testing.T) {
	anchor := time.Date(2026, 3, 31, 10, 0, 0, 0, time.UTC)
	for class, want := range map[DataClass]time.Time{
		ClassAPPayee:         time.Date(2033, 3, 31, 10, 0, 0, 0, time.UTC),
		ClassFinancialRecord: time.Date(2033, 3, 31, 10, 0, 0, 0, time.UTC),
		ClassEmployment:      time.Date(2032, 3, 31, 10, 0, 0, 0, time.UTC),
		ClassCandidate:       time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC),
		ClassSystemLog:       time.Date(2027, 3, 31, 10, 0, 0, 0, time.UTC),
	} {
		got, err := RetentionEnds(class, anchor)
		if err != nil || !got.Equal(want) {
			t.Errorf("%s ends %v (%v), want %v", class, got, err, want)
		}
	}
	if _, err := RetentionEnds("MARKETING", anchor); err == nil {
		t.Fatal("an unknown class has no retention")
	}
}

func TestHoldScope_NeedsAScopeAndAKnownClass(t *testing.T) {
	for name, tc := range map[string]struct {
		scope HoldScope
		ok    bool
	}{
		"subject":         {HoldScope{SubjectRef: "party:1"}, true},
		"class":           {HoldScope{DataClass: "AP_PAYEE"}, true},
		"record":          {HoldScope{RecordRef: "r-1"}, true},
		"nothing":         {HoldScope{}, false},
		"blank subject":   {HoldScope{SubjectRef: "  "}, false},
		"unknown class":   {HoldScope{DataClass: "MARKETING"}, false},
		"class and extra": {HoldScope{DataClass: "CANDIDATE", SubjectRef: "p"}, true},
	} {
		if err := tc.scope.ValidHold(); (err == nil) != tc.ok {
			t.Errorf("%s: %v", name, err)
		}
	}
}
