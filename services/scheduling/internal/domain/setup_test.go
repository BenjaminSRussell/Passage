package domain

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

// brMatrix is the BR-23.12 example: A->B 30, A->C 90, B->C 20, C->B 60.
func brMatrix(t *testing.T) SetupMatrix {
	t.Helper()
	m, err := NewSetupMatrix([]SetupEntry{
		{FromGroup: "A", ToGroup: "B", Minutes: 30},
		{FromGroup: "A", ToGroup: "C", Minutes: 90},
		{FromGroup: "B", ToGroup: "C", Minutes: 20},
		{FromGroup: "C", ToGroup: "B", Minutes: 60},
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// TestSequenceNearest_BR23_12Example covers AC-23.11: orders B and C from state
// A sequence as B then C with 50 changeover minutes.
func TestSequenceNearest_BR23_12Example(t *testing.T) {
	due := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	orders := []SetupOrder{
		{OrderID: "oc", SetupGroup: "C", Due: due, Minutes: 60},
		{OrderID: "ob", SetupGroup: "B", Due: due, Minutes: 60},
	}
	seq := SequenceNearest(brMatrix(t), "A", orders)
	if got := []string{seq.Orders[0].OrderID, seq.Orders[1].OrderID}; got[0] != "ob" || got[1] != "oc" {
		t.Fatalf("sequence = %v, want [ob oc]", got)
	}
	if seq.TotalMinutes != 50 {
		t.Fatalf("changeover minutes = %d, want 50", seq.TotalMinutes)
	}
	if len(seq.Changeovers) != 2 || seq.Changeovers[0].BeforeOrderID != "" || seq.Changeovers[1].AfterOrderID != "oc" {
		t.Fatalf("changeovers = %+v", seq.Changeovers)
	}
	if len(seq.Missing) != 0 {
		t.Fatalf("unexpected missing entries %v", seq.Missing)
	}
}

// TestSequenceNearest_TieBreaks checks that equal changeovers fall back to the
// earliest due date and then to the order id (BR-23.12). With no matrix entries
// and no route setup every changeover costs zero, so only the tie rules order
// the sequence.
func TestSequenceNearest_TieBreaks(t *testing.T) {
	early := time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC)
	late := early.AddDate(0, 0, 3)
	m, _ := NewSetupMatrix(nil)
	orders := []SetupOrder{
		{OrderID: "o2", SetupGroup: "X", Due: late},
		{OrderID: "o3", SetupGroup: "Y", Due: early},
		{OrderID: "o1", SetupGroup: "Z", Due: early},
		{OrderID: "o0", SetupGroup: "Z"},
	}
	seq := SequenceNearest(m, "X", orders)
	want := []string{"o1", "o3", "o2", "o0"}
	for i, o := range seq.Orders {
		if o.OrderID != want[i] {
			t.Fatalf("position %d = %s, want %s (earliest due, then id, no due date last)", i, o.OrderID, want[i])
		}
	}
}

// TestSequenceNearest_MissingEntryUsesRouteSetup covers SCHED-0012 and
// FR-23.15: a pair without a matrix entry costs the route's static setup
// time, is reported, and so is not preferred over a cheaper known entry.
func TestSequenceNearest_MissingEntryUsesRouteSetup(t *testing.T) {
	m, err := NewSetupMatrix([]SetupEntry{{FromGroup: "A", ToGroup: "C", Minutes: 40, Cost: decimal.RequireFromString("12.50")}})
	if err != nil {
		t.Fatal(err)
	}
	seq := SequenceNearest(m, "A", []SetupOrder{
		{OrderID: "ob", SetupGroup: "B", RouteSetupMinutes: 60},
		{OrderID: "oc", SetupGroup: "C", RouteSetupMinutes: 60},
	})
	if seq.Orders[0].OrderID != "oc" {
		t.Fatalf("the known 40 min changeover must beat the 60 min route setup, got %v first", seq.Orders[0].OrderID)
	}
	if len(seq.Missing) != 1 || seq.Missing[0] != "C->B" || seq.TotalMinutes != 100 || !seq.TotalCost.Equal(decimal.RequireFromString("12.5")) {
		t.Fatalf("missing=%v total=%d cost=%s", seq.Missing, seq.TotalMinutes, seq.TotalCost)
	}
	if c := seq.Changeovers[1]; !c.Missing || c.Minutes != 60 || !c.Cost.IsZero() {
		t.Fatalf("fallback changeover = %+v", c)
	}
}

// TestSequence_UnknownCurrentState starts a resource with no known setup
// group: the first order needs no changeover and nothing is reported missing.
func TestSequence_UnknownCurrentState(t *testing.T) {
	seq := SequenceAsGiven(brMatrix(t), "", []SetupOrder{{OrderID: "o1", SetupGroup: "B"}, {OrderID: "o2", SetupGroup: "C"}})
	if len(seq.Changeovers) != 1 || seq.Changeovers[0].FromGroup != "B" || seq.TotalMinutes != 20 || len(seq.Missing) != 0 {
		t.Fatalf("sequence = %+v", seq)
	}
}

func TestNewSetupMatrix_Rejects(t *testing.T) {
	cases := []struct {
		name    string
		entries []SetupEntry
	}{
		{"unnamed group", []SetupEntry{{FromGroup: "", ToGroup: "B"}}},
		{"self pair", []SetupEntry{{FromGroup: "A", ToGroup: "A"}}},
		{"negative minutes", []SetupEntry{{FromGroup: "A", ToGroup: "B", Minutes: -1}}},
		{"negative cost", []SetupEntry{{FromGroup: "A", ToGroup: "B", Cost: decimal.NewFromInt(-1)}}},
		{"group too long", []SetupEntry{{FromGroup: "A", ToGroup: "B234567890123456789012345678901234"}}},
		{"duplicate pair", []SetupEntry{{FromGroup: "A", ToGroup: "B"}, {FromGroup: "A", ToGroup: "B"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewSetupMatrix(tc.entries); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestLookup_SameGroupIsFree(t *testing.T) {
	m := brMatrix(t)
	if e, ok := m.Lookup("B", "B"); !ok || e.Minutes != 0 {
		t.Fatalf("same group = %+v ok=%v", e, ok)
	}
	if _, ok := m.Lookup("B", "A"); ok {
		t.Fatal("B->A has no entry and must not be found")
	}
}

func TestCleanoutDue(t *testing.T) {
	rule := CleanoutRule{SetupGroup: "PEANUT", AfterBatches: 5, AfterMinutes: 600, AtCampaignEnd: true}
	cases := []struct {
		name          string
		batches, mins int
		end           bool
		want          string
	}{
		{"nothing yet", 1, 60, false, CleanoutNone},
		{"batch limit reached", 5, 60, false, CleanoutBatches},
		{"run time reached", 1, 600, false, CleanoutRunTime},
		{"campaign end", 1, 60, true, CleanoutCampaignEnd},
		{"campaign end is reported before batch limit", 5, 600, true, CleanoutCampaignEnd},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CleanoutDue(rule, tc.batches, tc.mins, tc.end); got != tc.want {
				t.Fatalf("CleanoutDue = %q, want %q", got, tc.want)
			}
		})
	}
	noEnd := CleanoutRule{AfterBatches: 2}
	if got := CleanoutDue(noEnd, 0, 0, true); got != CleanoutNone {
		t.Fatalf("rule without AtCampaignEnd fired at campaign end: %q", got)
	}
}

// TestSequenceNearest_Property checks on random matrices that the sequence is a
// permutation of the input and that the reported minutes equal the sum of the
// changeovers between consecutive groups.
func TestSequenceNearest_Property(t *testing.T) {
	groups := []string{"A", "B", "C"}
	rapid.Check(t, func(t *rapid.T) {
		var entries []SetupEntry
		for _, f := range groups {
			for _, to := range groups {
				if f != to {
					entries = append(entries, SetupEntry{FromGroup: f, ToGroup: to,
						Minutes: rapid.IntRange(0, 120).Draw(t, "minutes")})
				}
			}
		}
		m, err := NewSetupMatrix(entries)
		if err != nil {
			t.Fatal(err)
		}
		n := rapid.IntRange(0, 12).Draw(t, "n")
		orders := make([]SetupOrder, n)
		for i := range orders {
			orders[i] = SetupOrder{OrderID: string(rune('a' + i)), SetupGroup: rapid.SampledFrom(groups).Draw(t, "group")}
		}
		seq := SequenceNearest(m, "A", orders)
		if len(seq.Orders) != n {
			t.Fatalf("sequence has %d orders, want %d", len(seq.Orders), n)
		}
		seen := map[string]bool{}
		for _, o := range seq.Orders {
			if seen[o.OrderID] {
				t.Fatalf("order %s scheduled twice", o.OrderID)
			}
			seen[o.OrderID] = true
		}
		sum, prev := 0, "A"
		for _, o := range seq.Orders {
			if o.SetupGroup != prev {
				e, _ := m.Lookup(prev, o.SetupGroup)
				sum += e.Minutes
			}
			prev = o.SetupGroup
		}
		if sum != seq.TotalMinutes {
			t.Fatalf("total %d != recomputed %d", seq.TotalMinutes, sum)
		}
	})
}
