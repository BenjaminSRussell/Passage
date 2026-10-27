package domain

import (
	"testing"
	"time"

	"pgregory.net/rapid"
)

func intPtr(v int) *int { return &v }

// mixingNetwork is the FR-23.13 example: vessel V1 feeds fillers F1 and F2 only
// (F1 within a 120 min hold), and V2 feeds F3 only.
func mixingNetwork(t *testing.T) ResourceNetwork {
	t.Helper()
	n, err := NewResourceNetwork("mixing", []NetworkEdge{
		{From: "V1", To: "F1", TransferMinutes: 5, MaxWaitMinutes: intPtr(120)},
		{From: "V1", To: "F2", TransferMinutes: 10},
		{From: "V2", To: "F3", TransferMinutes: 0},
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestNewResourceNetwork_Rejects(t *testing.T) {
	cases := []struct {
		name  string
		edges []NetworkEdge
	}{
		{"missing resource", []NetworkEdge{{From: "V1"}}},
		{"self loop", []NetworkEdge{{From: "V1", To: "V1"}}},
		{"duplicate edge", []NetworkEdge{{From: "V1", To: "F1"}, {From: "V1", To: "F1"}}},
		{"negative transfer", []NetworkEdge{{From: "V1", To: "F1", TransferMinutes: -1}}},
		{"negative max wait", []NetworkEdge{{From: "V1", To: "F1", MaxWaitMinutes: intPtr(-1)}}},
		{"max wait shorter than the transfer", []NetworkEdge{{From: "V1", To: "F1", TransferMinutes: 30, MaxWaitMinutes: intPtr(20)}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewResourceNetwork("n", tc.edges); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
	if _, err := NewResourceNetwork("", nil); err == nil {
		t.Fatal("network without a code accepted")
	}
}

// TestSuccessors_ExcludesDisallowedFillers covers AC-23.10's placement rule: no
// filling job may land on F3 when the mixing vessel is V1.
func TestSuccessors_ExcludesDisallowedFillers(t *testing.T) {
	n := mixingNetwork(t)
	got := n.Successors("V1", []string{"F3", "F2", "F1"})
	if len(got) != 2 || got[0].To != "F1" || got[1].To != "F2" {
		t.Fatalf("successors = %+v, want F1 (5 min) then F2 (10 min)", got)
	}
}

func TestChainAllowed(t *testing.T) {
	n := mixingNetwork(t)
	cases := []struct {
		path []string
		want bool
	}{
		{[]string{"V1", "F1"}, true},
		{[]string{"V1", "F3"}, false},
		{[]string{"V1", "F1", "F2"}, false},
		{[]string{"V2", "F3"}, true},
		{[]string{"V1"}, true},
		{nil, true},
	}
	for _, tc := range cases {
		if got := n.ChainAllowed(tc.path); got != tc.want {
			t.Fatalf("ChainAllowed(%v) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// TestWaitOK checks BR-23.11: the gap between operations must cover the transfer
// and must not exceed the maximum wait.
func TestWaitOK(t *testing.T) {
	n := mixingNetwork(t)
	e1, _ := n.Edge("V1", "F1")
	e2, _ := n.Edge("V1", "F2")
	end := time.Date(2026, 3, 2, 10, 0, 0, 0, time.UTC)
	cases := []struct {
		name  string
		edge  NetworkEdge
		start time.Time
		want  bool
	}{
		{"within max wait", e1, end.Add(60 * time.Minute), true},
		{"exactly max wait", e1, end.Add(120 * time.Minute), true},
		{"beyond max wait", e1, end.Add(150 * time.Minute), false},
		{"transfer not covered", e1, end.Add(3 * time.Minute), false},
		{"no max wait", e2, end.Add(10 * time.Hour), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.edge.WaitOK(end, tc.start); got != tc.want {
				t.Fatalf("WaitOK = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestNetwork_RandomWalkProperty builds random networks and walks them along
// edges: every walk must pass ChainAllowed, and Successors must return only
// allowed candidates in non-decreasing transfer order.
func TestNetwork_RandomWalkProperty(t *testing.T) {
	names := []string{"R0", "R1", "R2", "R3", "R4"}
	rapid.Check(t, func(t *rapid.T) {
		var edges []NetworkEdge
		for _, f := range names {
			for _, to := range names {
				if f != to && rapid.Bool().Draw(t, "edge") {
					edges = append(edges, NetworkEdge{From: f, To: to, TransferMinutes: rapid.IntRange(0, 30).Draw(t, "transfer")})
				}
			}
		}
		n, err := NewResourceNetwork("rnd", edges)
		if err != nil {
			t.Fatal(err)
		}
		cur := rapid.SampledFrom(names).Draw(t, "start")
		walk := []string{cur}
		for step := 0; step < 6; step++ {
			succ := n.Successors(cur, names)
			for i := 1; i < len(succ); i++ {
				if succ[i].TransferMinutes < succ[i-1].TransferMinutes {
					t.Fatalf("successors out of transfer order: %+v", succ)
				}
			}
			if len(succ) == 0 {
				break
			}
			cur = rapid.SampledFrom(succ).Draw(t, "next").To
			walk = append(walk, cur)
		}
		if !n.ChainAllowed(walk) {
			t.Fatalf("walk %v not allowed by its own edges", walk)
		}
	})
}
