package domain

import (
	"fmt"
	"testing"
	"time"

	"pgregory.net/rapid"
)

var at = day(100)

func open(from int) Range { return Range{From: day(from)} }

// chain builds positions P0 <- P1 <- ... <- Pn, where Pi reports to Pi+1 and
// occupied[i] says whether Pi has a primary incumbent named "W<i>".
func chain(n int, occupied []bool) ([]Assignment, []Edge) {
	var as []Assignment
	var es []Edge
	for i := 0; i <= n; i++ {
		pos := fmt.Sprintf("P%d", i)
		if occupied[i] {
			as = append(as, Assignment{WorkerID: fmt.Sprintf("W%d", i), PositionID: pos, Primary: true, Validity: open(0)})
		}
		if i < n {
			es = append(es, Edge{ChildPositionID: pos, ParentPositionID: fmt.Sprintf("P%d", i+1), Validity: open(0)})
		}
	}
	return as, es
}

func TestManager_Table(t *testing.T) {
	as, es := chain(3, []bool{false, false, true, true})
	// Subject worker sits on P0; P1 vacant, P2 holds W2, so W2 is the manager.
	as = append(as, Assignment{WorkerID: "S", PositionID: "P0", Primary: true, Validity: open(0)})
	cases := []struct {
		name     string
		date     time.Time
		maxDepth int
		want     string
		found    bool
	}{
		{"walks past a vacant reports-to position", at, 10, "W2", true},
		{"depth limit stops the walk early", at, 1, "", false},
		{"no assignment before the hire date", day(-5), 10, "", false},
	}
	for _, c := range cases {
		got, ok := Manager("S", c.date, as, es, c.maxDepth)
		if got != c.want || ok != c.found {
			t.Errorf("%s: got (%q,%v), want (%q,%v)", c.name, got, ok, c.want, c.found)
		}
	}
}

func TestManager_EffectiveDatedEdgeChange(t *testing.T) {
	// S moves from reporting to P1 (held by A) to P2 (held by B) on day 50.
	to := day(50)
	as := []Assignment{
		{WorkerID: "S", PositionID: "P0", Primary: true, Validity: open(0)},
		{WorkerID: "A", PositionID: "P1", Primary: true, Validity: open(0)},
		{WorkerID: "B", PositionID: "P2", Primary: true, Validity: open(0)},
	}
	es := []Edge{
		{ChildPositionID: "P0", ParentPositionID: "P1", Validity: Range{From: day(0), To: &to}},
		{ChildPositionID: "P0", ParentPositionID: "P2", Validity: open(50)},
	}
	if m, _ := Manager("S", day(49), as, es, 10); m != "A" {
		t.Errorf("before the change manager = %q, want A", m)
	}
	if m, _ := Manager("S", day(50), as, es, 10); m != "B" {
		t.Errorf("on the change date manager = %q, want B", m)
	}
}

// Property: the walk returns the first occupied ancestor within maxDepth, as
// an independent reference loop computes it.
func TestManager_MatchesReferenceWalk(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 15).Draw(t, "levels")
		occupied := make([]bool, n+1)
		for i := range occupied {
			occupied[i] = rapid.Bool().Draw(t, "occupied")
		}
		maxDepth := rapid.IntRange(1, 12).Draw(t, "maxDepth")
		as, es := chain(n, occupied)
		as = append(as, Assignment{WorkerID: "S", PositionID: "P0", Primary: true, Validity: open(0)})

		want, found := "", false
		for i := 1; i <= n && i <= maxDepth; i++ {
			if occupied[i] {
				want, found = fmt.Sprintf("W%d", i), true
				break
			}
		}
		got, ok := Manager("S", at, as, es, maxDepth)
		if got != want || ok != found {
			t.Fatalf("occupied=%v maxDepth=%d: got (%q,%v) want (%q,%v)", occupied, maxDepth, got, ok, want, found)
		}
	})
}

func TestWouldCycle(t *testing.T) {
	es := []Edge{
		{ChildPositionID: "B", ParentPositionID: "A", Validity: open(0)},
		{ChildPositionID: "C", ParentPositionID: "B", Validity: open(0)},
	}
	cases := []struct {
		name          string
		child, parent string
		want          bool
	}{
		{"self-report is a cycle", "A", "A", true},
		{"parent is a descendant", "A", "C", true},
		{"unrelated parent is fine", "D", "C", false},
		{"adding a leaf under the root is fine", "E", "A", false},
	}
	for _, c := range cases {
		if got := WouldCycle(es, c.child, c.parent, at); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
