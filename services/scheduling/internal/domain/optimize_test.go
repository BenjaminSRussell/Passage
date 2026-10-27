package domain

import (
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// trapMatrix is a matrix where nearest-neighbour is far from optimal: from A
// it takes the cheap A->B (1) and then pays B->C (100); A->C (2) then C->B (1)
// costs 3.
func trapMatrix(t *testing.T) SetupMatrix {
	t.Helper()
	m, err := NewSetupMatrix([]SetupEntry{
		{FromGroup: "A", ToGroup: "B", Minutes: 1},
		{FromGroup: "A", ToGroup: "C", Minutes: 2},
		{FromGroup: "B", ToGroup: "C", Minutes: 100},
		{FromGroup: "C", ToGroup: "B", Minutes: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func trapOrders() []SetupOrder {
	return []SetupOrder{{OrderID: "ob", SetupGroup: "B", Minutes: 60}, {OrderID: "oc", SetupGroup: "C", Minutes: 60}}
}

// The matrix sent to scheduler-opt prices every step the way the Go
// sequencers do, so the solver optimises the same objective.
func TestChangeoverMatrix_PricesLikeTheSequencers(t *testing.T) {
	orders := []SetupOrder{
		{OrderID: "ob", SetupGroup: "B"},
		{OrderID: "oc", SetupGroup: "C"},
		{OrderID: "ob2", SetupGroup: "B"},
		{OrderID: "oz", SetupGroup: "Z", RouteSetupMinutes: 15},
	}
	got := ChangeoverMatrix(brMatrix(t), "A", orders)
	// Index 0 is the current group A; 1 ob(B), 2 oc(C), 3 ob2(B), 4 oz(Z).
	want := [][]int{
		{0, 30, 90, 30, 15},
		{0, 0, 20, 0, 15},
		{0, 60, 0, 60, 15},
		{0, 0, 20, 0, 15},
		{0, 0, 0, 0, 0},
	}
	// Z has no entries at all, so steps out of Z fall back to the target
	// order's route setup (zero for B and C here).
	for a := range want {
		for b := range want[a] {
			if got[a][b] != want[a][b] {
				t.Fatalf("matrix[%d][%d] = %d, want %d (all %v)", a, b, got[a][b], want[a][b], got)
			}
		}
	}
	unknown := ChangeoverMatrix(brMatrix(t), "", orders[:2])
	if unknown[0][1] != 0 || unknown[0][2] != 0 || unknown[1][2] != 20 {
		t.Fatalf("unknown current group must not charge a leading setup: %v", unknown)
	}
	// Every path cost through the matrix equals SequenceAsGiven's minutes.
	path := []int{2, 1, 4, 3}
	ordered := []SetupOrder{orders[1], orders[0], orders[3], orders[2]}
	sum, prev := 0, 0
	for _, p := range path {
		sum += got[prev][p]
		prev = p
	}
	if seq := SequenceAsGiven(brMatrix(t), "A", ordered); seq.TotalMinutes != sum {
		t.Fatalf("matrix path costs %d, sequencer %d", sum, seq.TotalMinutes)
	}
}

// BR-23.9: a solver order is kept only when it is certified, complete and
// strictly cheaper than nearest-neighbour priced with the current matrix.
func TestChooseSequence(t *testing.T) {
	m := trapMatrix(t)
	cases := []struct {
		name          string
		proposal      *SolverProposal
		wantStatus    string
		wantOptimized bool
		wantMinutes   int
		wantFirst     string
	}{
		{"solver not asked", nil, SolverUnavailable, false, 101, "ob"},
		{"optimal and cheaper", &SolverProposal{OrderIDs: []string{"oc", "ob"}, Status: SolverOptimal}, SolverOptimal, true, 3, "oc"},
		{"feasible and cheaper", &SolverProposal{OrderIDs: []string{"oc", "ob"}, Status: SolverFeasible}, SolverFeasible, true, 3, "oc"},
		{"unknown keeps heuristic", &SolverProposal{Status: SolverUnknown}, SolverUnknown, false, 101, "ob"},
		{"no gain keeps heuristic", &SolverProposal{OrderIDs: []string{"ob", "oc"}, Status: SolverOptimal}, SolverOptimal, false, 101, "ob"},
		{"duplicate id refused", &SolverProposal{OrderIDs: []string{"oc", "oc"}, Status: SolverOptimal}, SolverOptimal, false, 101, "ob"},
		{"foreign id refused", &SolverProposal{OrderIDs: []string{"oc", "zz"}, Status: SolverOptimal}, SolverOptimal, false, 101, "ob"},
		{"short answer refused", &SolverProposal{OrderIDs: []string{"oc"}, Status: SolverOptimal}, SolverOptimal, false, 101, "ob"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seq, status, optimized := chooseSequence(m, "A", trapOrders(), tc.proposal)
			if status != tc.wantStatus || optimized != tc.wantOptimized || seq.TotalMinutes != tc.wantMinutes || seq.Orders[0].OrderID != tc.wantFirst {
				t.Fatalf("got status %s optimized %v minutes %d first %s", status, optimized, seq.TotalMinutes, seq.Orders[0].OrderID)
			}
		})
	}
}

// The cp-sat rule plans cleanouts and the carried counter on the solver's
// order, and reports the solver status.
func TestPlanCampaign_CPSATUsesTheSolverOrder(t *testing.T) {
	orders := []CampaignOrder{{SetupOrder: trapOrders()[0], Qty: w(10)}, {SetupOrder: trapOrders()[1], Qty: w(10)}}
	plan, err := PlanCampaign(CampaignInput{Status: CampaignProposed, Rule: SequenceCPSAT, Matrix: trapMatrix(t), CurrentGroup: "A",
		Cleanouts: []CleanoutRule{{SetupGroup: "B", DurationMinutes: 45, AtCampaignEnd: true}}, Carry: CleanoutCounter{Batches: 3, Minutes: 500},
		Orders: orders, Proposal: &SolverProposal{OrderIDs: []string{"oc", "ob"}, Status: SolverOptimal}})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Optimized || plan.SolverStatus != SolverOptimal || plan.ChangeoverMinutes != 3 || plan.Sequence.Orders[1].OrderID != "ob" {
		t.Fatalf("plan = %+v", plan)
	}
	if len(plan.Cleanouts) != 1 || plan.Cleanouts[0].AfterOrderID != "ob" || plan.CarryOut != (CleanoutCounter{}) {
		t.Fatalf("cleanouts %+v carry %+v, want the campaign-end cleanout after ob and a reset counter", plan.Cleanouts, plan.CarryOut)
	}
	if msg := SolverMessage(plan.SolverStatus, plan.Optimized); !strings.Contains(msg, "status OPTIMAL; optimized schedule kept") {
		t.Fatalf("message %q", msg)
	}
	heur, err := PlanCampaign(CampaignInput{Status: CampaignProposed, Rule: SequenceCPSAT, Matrix: trapMatrix(t), CurrentGroup: "A", Orders: orders})
	if err != nil || heur.Optimized || heur.SolverStatus != SolverUnavailable || heur.ChangeoverMinutes != 101 {
		t.Fatalf("no proposal: plan %+v err %v", heur, err)
	}
	if msg := SolverMessage(heur.SolverStatus, heur.Optimized); !strings.Contains(msg, "UNAVAILABLE; heuristic schedule kept") {
		t.Fatalf("message %q", msg)
	}
}

// BR-23.13: the counter a campaign leaves behind is what ran after its last
// cleanout, carried in counter included when no cleanout fired.
func TestPlanCampaign_CarryOut(t *testing.T) {
	orders := []CampaignOrder{
		{SetupOrder: SetupOrder{OrderID: "o1", SetupGroup: "P", Minutes: 60}, Qty: w(1)},
		{SetupOrder: SetupOrder{OrderID: "o2", SetupGroup: "P", Minutes: 70}, Qty: w(1)},
		{SetupOrder: SetupOrder{OrderID: "o3", SetupGroup: "P", Minutes: 80}, Qty: w(1)},
	}
	rule := CleanoutRule{SetupGroup: "P", DurationMinutes: 30, AfterBatches: 4}
	cases := []struct {
		name  string
		carry CleanoutCounter
		want  CleanoutCounter
		fired int
	}{
		{"no cleanout keeps counting", CleanoutCounter{Batches: 0, Minutes: 10}, CleanoutCounter{Batches: 3, Minutes: 220}, 0},
		{"carried batches fire mid-campaign", CleanoutCounter{Batches: 2, Minutes: 400}, CleanoutCounter{Batches: 1, Minutes: 80}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := PlanCampaign(CampaignInput{Status: CampaignProposed, Rule: SequenceManual, CurrentGroup: "P",
				Cleanouts: []CleanoutRule{rule}, Carry: tc.carry, Orders: orders})
			if err != nil {
				t.Fatal(err)
			}
			if plan.CarryOut != tc.want || len(plan.Cleanouts) != tc.fired {
				t.Fatalf("carry out %+v cleanouts %+v, want %+v and %d", plan.CarryOut, plan.Cleanouts, tc.want, tc.fired)
			}
		})
	}
}

// Whatever certified order the solver returns, the kept sequence is a
// permutation of the campaign and never costs more than nearest-neighbour;
// with the brute-force optimum as the proposal it reaches the optimum.
func TestChooseSequence_Property(t *testing.T) {
	groups := []string{"A", "B", "C", "D"}
	rapid.Check(t, func(t *rapid.T) {
		var entries []SetupEntry
		for _, from := range groups {
			for _, to := range groups {
				if from != to && rapid.Bool().Draw(t, "has entry") {
					entries = append(entries, SetupEntry{FromGroup: from, ToGroup: to, Minutes: rapid.IntRange(0, 200).Draw(t, "minutes")})
				}
			}
		}
		m, err := NewSetupMatrix(entries)
		if err != nil {
			t.Fatal(err)
		}
		n := rapid.IntRange(1, 6).Draw(t, "n")
		orders := make([]SetupOrder, n)
		for i := range orders {
			orders[i] = SetupOrder{OrderID: string(rune('a' + i)), SetupGroup: rapid.SampledFrom(groups).Draw(t, "group"),
				RouteSetupMinutes: rapid.IntRange(0, 60).Draw(t, "route setup")}
		}
		current := rapid.SampledFrom(append([]string{""}, groups...)).Draw(t, "current")
		heuristic := SequenceNearest(m, current, orders)
		best, bestIDs := -1, []string(nil)
		permute(orders, func(p []SetupOrder) {
			if c := SequenceAsGiven(m, current, p).TotalMinutes; best < 0 || c < best {
				best, bestIDs = c, idsOf(p)
			}
		})
		seq, _, _ := chooseSequence(m, current, orders, &SolverProposal{OrderIDs: bestIDs, Status: SolverOptimal})
		if seq.TotalMinutes != min(best, heuristic.TotalMinutes) || len(seq.Orders) != n {
			t.Fatalf("kept %d minutes, optimum %d, heuristic %d", seq.TotalMinutes, best, heuristic.TotalMinutes)
		}
		// The matrix sent to the solver prices the optimum identically.
		cm := ChangeoverMatrix(m, current, orders)
		index := map[string]int{}
		for i, o := range orders {
			index[o.OrderID] = i + 1
		}
		sum, prev := 0, 0
		for _, id := range bestIDs {
			sum += cm[prev][index[id]]
			prev = index[id]
		}
		if sum != best {
			t.Fatalf("matrix prices the optimum at %d, sequencer at %d", sum, best)
		}
	})
}

func idsOf(orders []SetupOrder) []string {
	out := make([]string, len(orders))
	for i, o := range orders {
		out[i] = o.OrderID
	}
	return out
}

func permute(orders []SetupOrder, visit func([]SetupOrder)) {
	var rec func(k int)
	p := append([]SetupOrder(nil), orders...)
	rec = func(k int) {
		if k == len(p) {
			visit(p)
			return
		}
		for i := k; i < len(p); i++ {
			p[k], p[i] = p[i], p[k]
			rec(k + 1)
			p[k], p[i] = p[i], p[k]
		}
	}
	rec(0)
}
