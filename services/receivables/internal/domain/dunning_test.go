package domain

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func TestTargetLevel_BR302(t *testing.T) {
	tests := []struct {
		days, grace, want int
	}{
		{0, 5, 0}, {5, 5, 0}, {6, 5, 1}, {14, 5, 1}, {15, 5, 2}, {29, 5, 2},
		{30, 5, 3}, {44, 5, 3}, {45, 5, 4}, {400, 5, 4},
		// A long grace holds every level until it has passed.
		{20, 20, 0}, {21, 20, 2}, {31, 30, 3},
	}
	for _, tc := range tests {
		if got := TargetLevel(tc.days, tc.grace); got != tc.want {
			t.Errorf("TargetLevel(%d, %d) = %d, want %d", tc.days, tc.grace, got, tc.want)
		}
	}
}

func TestLetterFee_BR302(t *testing.T) {
	tests := []struct {
		cur          string
		level, prior int
		want         string
	}{
		{"USD", 2, 1, "0"}, {"USD", 3, 2, "25"}, {"EUR", 3, 2, "20"},
		{"USD", 4, 3, "50"}, {"EUR", 4, 3, "40"}, {"USD", 4, 4, "0"}, {"USD", 3, 3, "0"},
	}
	for _, tc := range tests {
		if got := LetterFee(tc.cur, tc.level, tc.prior); !got.Equal(dec(tc.want)) {
			t.Errorf("LetterFee(%s, %d, %d) = %s, want %s", tc.cur, tc.level, tc.prior, got, tc.want)
		}
	}
}

func TestPlanDunning_ExactLetterSet(t *testing.T) {
	asOf := day("2027-03-31")
	due := func(d string) time.Time { return day(d) }
	items := []DunnableItem{
		// C1: 60 days overdue, eligible -> level 1 raised to 2 (one level per run)
		{ID: "a1", CustomerID: "C1", Currency: "USD", DocumentNumber: "INV-1", DueDate: due("2027-01-30"), OpenAmount: dec("800.00")},
		// C2: disputed item, excluded
		{ID: "b1", CustomerID: "C2", Currency: "USD", DueDate: due("2027-02-01"), OpenAmount: dec("900.00"), HasDispute: true},
		// C3: blocked
		{ID: "c1", CustomerID: "C3", Currency: "USD", DueDate: due("2027-02-01"), OpenAmount: dec("900.00"), DunningBlock: true},
		// C4: promise holds (promise 2027-03-30, +2 days >= asOf)
		{ID: "d1", CustomerID: "C4", Currency: "USD", DueDate: due("2027-02-01"), OpenAmount: dec("900.00"), PromiseDate: ptr(day("2027-03-30"))},
		// C5: promise broken long ago -> eligible, level 3 target from level 2
		{ID: "e1", CustomerID: "C5", Currency: "EUR", DueDate: due("2027-02-01"), OpenAmount: dec("300.00"), DunningLevel: 2, PromiseDate: ptr(day("2027-03-01"))},
		// C6: below minimum (30 < 50)
		{ID: "f1", CustomerID: "C6", Currency: "USD", DueDate: due("2027-02-01"), OpenAmount: dec("30.00")},
		// C7: not yet past grace
		{ID: "g1", CustomerID: "C7", Currency: "USD", DueDate: due("2027-03-28"), OpenAmount: dec("900.00")},
		// C8: letter 2027-03-25 is inside the 10-day interval
		{ID: "h1", CustomerID: "C8", Currency: "USD", DueDate: due("2027-02-01"), OpenAmount: dec("900.00"), LastDunnedAt: ptr(day("2027-03-25"))},
	}
	plan := PlanDunning(asOf, DefaultGraceDays, DefaultIntervalDays, items)

	got := map[string]PlannedLetter{}
	for _, l := range plan.Letters {
		got[l.CustomerID] = l
	}
	if len(got) != 2 {
		t.Fatalf("letters for %d customers, want 2: %v", len(got), got)
	}
	if l := got["C1"]; l.Level != 1 || l.Fee.IsPositive() {
		t.Errorf("C1 level %d fee %s, want level 1 (one level per run) no fee", l.Level, l.Fee)
	}
	if l := got["C5"]; l.Level != 3 || !l.Fee.Equal(dec("20")) || l.PriorLevel != 2 {
		t.Errorf("C5 level %d fee %s prior %d, want 3 / 20.00 / 2", l.Level, l.Fee, l.PriorLevel)
	}
	want := map[string]string{"b1": ReasonDispute, "c1": ReasonBlocked, "d1": ReasonPromise,
		"f1": ReasonBelowMinimum, "h1": ReasonInterval}
	gotReasons := map[string]string{}
	for _, e := range plan.Exclusions {
		gotReasons[e.OpenItemID] = e.Reason
	}
	for id, reason := range want {
		if gotReasons[id] != reason {
			t.Errorf("exclusion %s = %q, want %q", id, gotReasons[id], reason)
		}
	}
	if _, ok := gotReasons["g1"]; ok {
		t.Errorf("g1 inside grace must not be excluded, only skipped")
	}
}

func TestReviseLetter_ExclusionsEvaluatedAtIssue(t *testing.T) {
	asOf := day("2027-03-31")
	planned := PlannedLetter{CustomerID: "C1", Currency: "USD", Level: 2, Amount: dec("1000.00"), Lines: []LetterLine{
		{OpenItemID: "a", OpenAmount: dec("600.00"), FromLevel: 0, ToLevel: 1},
		{OpenItemID: "b", OpenAmount: dec("400.00"), FromLevel: 1, ToLevel: 2},
	}}
	current := map[string]DunnableItem{
		"a": {ID: "a", DueDate: day("2027-02-01"), OpenAmount: dec("600.00"), DunningLevel: 0, HasDispute: true},
		"b": {ID: "b", DueDate: day("2027-02-01"), OpenAmount: dec("400.00"), DunningLevel: 1},
	}
	out, ok := ReviseLetter(asOf, planned, current)
	if !ok || len(out.Lines) != 1 || out.Lines[0].OpenItemID != "b" || out.Level != 2 {
		t.Fatalf("revise = %+v ok=%v, want only item b at level 2", out, ok)
	}
	current["b"] = DunnableItem{ID: "b", DueDate: day("2027-02-01"), OpenAmount: dec("20.00"), DunningLevel: 1}
	if _, ok := ReviseLetter(asOf, planned, current); ok {
		t.Errorf("letter below minimum after revision must be skipped")
	}
}

func TestCanMoveRun(t *testing.T) {
	tests := []struct {
		from, to string
		want     bool
	}{
		{"DRAFT", "REVIEWED", true}, {"DRAFT", "CANCELLED", true}, {"REVIEWED", "ISSUED", true},
		{"REVIEWED", "CANCELLED", true}, {"DRAFT", "ISSUED", false}, {"ISSUED", "CANCELLED", false},
	}
	for _, tc := range tests {
		if got := CanMoveRun(tc.from, tc.to); got != tc.want {
			t.Errorf("CanMoveRun(%s,%s) = %v", tc.from, tc.to, got)
		}
	}
}

func TestNextLevel_PropertyNeverJumpsOrLowers(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cur := rapid.IntRange(0, 4).Draw(t, "current")
		target := rapid.IntRange(0, 4).Draw(t, "target")
		next := NextLevel(cur, target)
		if next < cur {
			t.Fatalf("level lowered: %d -> %d", cur, next)
		}
		if next > cur+1 {
			t.Fatalf("level jumped more than one: %d -> %d", cur, next)
		}
		if next > target && target > cur {
			t.Fatalf("overshot target: %d -> %d target %d", cur, next, target)
		}
	})
}

func TestTargetLevel_PropertyMonotonic(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		a := rapid.IntRange(-10, 500).Draw(t, "a")
		b := rapid.IntRange(-10, 500).Draw(t, "b")
		if a > b {
			a, b = b, a
		}
		if TargetLevel(a, DefaultGraceDays) > TargetLevel(b, DefaultGraceDays) {
			t.Fatalf("target not monotonic at %d and %d", a, b)
		}
	})
}
