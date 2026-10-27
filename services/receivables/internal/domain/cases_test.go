package domain

import (
	"testing"
	"time"
)

func TestCaseSLAHours_DefaultsPerCategory(t *testing.T) {
	for category, want := range map[string]int{CaseCategoryCollection: 72, CaseCategoryDispute: 120, CaseCategoryDeduction: 240} {
		if got, ok := CaseSLAHours(category); !ok || got != want {
			t.Errorf("CaseSLAHours(%s) = %d, %v; want %d", category, got, ok, want)
		}
		if !ValidCaseCategory(category) {
			t.Errorf("%s should be a valid category", category)
		}
	}
	if _, ok := CaseSLAHours("OTHER"); ok || ValidCaseCategory("OTHER") {
		t.Fatal("an unknown category has no SLA")
	}
	// The dispute entity and DISPUTE cases share one SLA.
	if h, _ := CaseSLAHours(CaseCategoryDispute); h != DisputeSLAHours {
		t.Fatalf("DISPUTE case SLA %d differs from the dispute SLA %d", h, DisputeSLAHours)
	}
}

func TestCaseSLABreached_OnlyOpenCasesPastDue(t *testing.T) {
	due := time.Date(2027, 3, 4, 9, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		status string
		now    time.Time
		want   bool
	}{
		{"open before due", CaseOpen, due.Add(-time.Minute), false},
		{"open exactly at due", CaseOpen, due, false},
		{"open after due", CaseOpen, due.Add(time.Minute), true},
		{"closed after due", CaseClosed, due.Add(time.Hour), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := CaseSLABreached(tc.status, due, tc.now); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
	if CaseCanChange(CaseClosed) || !CaseCanChange(CaseOpen) {
		t.Fatal("only OPEN cases can be assigned or closed")
	}
	if !ValidCasePriority("HIGH") || ValidCasePriority("URGENT") {
		t.Fatal("priority must be LOW, NORMAL or HIGH")
	}
}
