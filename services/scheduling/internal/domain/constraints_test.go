package domain

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"
)

func mustNetwork(t failer, code string, edges []NetworkEdge) ResourceNetwork {
	t.Helper()
	n, err := NewResourceNetwork(code, edges)
	if err != nil {
		t.Fatalf("network: %v", err)
	}
	return n
}

func mustMatrix(t failer, entries ...SetupEntry) SetupMatrix {
	t.Helper()
	m, err := NewSetupMatrix(entries)
	if err != nil {
		t.Fatalf("matrix: %v", err)
	}
	return m
}

func placementOf(t *testing.T, r Result, opID string) Placement {
	t.Helper()
	for _, pl := range r.Placements {
		if pl.OperationID == opID {
			return pl
		}
	}
	t.Fatalf("operation %s is not placed; unplaced=%v", opID, r.Unplaced)
	return Placement{}
}

func codesOf(ws []Warning) []string {
	out := make([]string, 0, len(ws))
	for _, w := range ws {
		out = append(out, w.Code+" "+w.OperationID)
	}
	return out
}

// br2312 is the BR-23.12 example matrix: A->B 30, A->C 90, B->C 20, C->B 60.
func br2312(t failer) SetupMatrix {
	return mustMatrix(t,
		SetupEntry{FromGroup: "A", ToGroup: "B", Minutes: 30},
		SetupEntry{FromGroup: "A", ToGroup: "C", Minutes: 90},
		SetupEntry{FromGroup: "B", ToGroup: "C", Minutes: 20},
		SetupEntry{FromGroup: "C", ToGroup: "B", Minutes: 60},
	)
}

// mixFill is the AC-23.10 plant: vessel V1 feeds fillers F1 and F2 with a
// 120 min maximum wait; F3 is not connected to V1.
func mixFill(t *testing.T) ([]Resource, ResourceNetwork) {
	net := mustNetwork(t, "MIXFILL", []NetworkEdge{
		{From: "V1", To: "F1", MaxWaitMinutes: intPtr(120)},
		{From: "V1", To: "F2", TransferMinutes: 10, MaxWaitMinutes: intPtr(120)},
	})
	return []Resource{
		res("V1", "V1", true, allDays(0, 1440)),
		res("F1", "F1", true, allDays(0, 1440)),
		res("F2", "F2", true, allDays(0, 1440)),
		res("F3", "F3", true, allDays(0, 1440)),
	}, net
}

// AC-23.10: both connected fillers are busy for four hours, F3 is free but
// has no edge. Mixing is delayed just enough that filling starts within the
// 120 min wait, and nothing lands on F3.
func TestSchedule_NetworkDelaysPredecessorToMeetMaxWait(t *testing.T) {
	resources, net := mixFill(t)
	p := Problem{
		Resources: resources,
		Booked: []Busy{
			{ResourceID: "F1", Start: at(0, 8, 0), End: at(0, 12, 0), JobID: "x1"},
			{ResourceID: "F2", Start: at(0, 8, 0), End: at(0, 12, 0), JobID: "x2"},
		},
		Operations: []Operation{
			{ID: "mix", OrderID: "o1", OperNumber: 10, Minutes: 60, ReadyAt: at(0, 8, 0), Candidates: []string{"V1"}},
			{ID: "fill", OrderID: "o1", OperNumber: 20, Minutes: 60, Network: "MIXFILL", Candidates: []string{"F3", "F2", "F1"}},
		},
		Networks: []ResourceNetwork{net}, Start: monday, HorizonDays: 14,
	}
	r := Schedule(p)
	if len(r.Unplaced) != 0 {
		t.Fatalf("unplaced: %v", r.Unplaced)
	}
	checkFeasible(t, p, r)
	mix, fill := placementOf(t, r, "mix"), placementOf(t, r, "fill")
	if fill.ResourceID != "F1" {
		t.Fatalf("filling on %s, want F1 (F2 also needs 10 min transfer, F3 has no edge)", fill.ResourceID)
	}
	if !mix.Start.Equal(at(0, 9, 0)) || !fill.Start.Equal(at(0, 12, 0)) {
		t.Fatalf("mix %v, fill %v; want mixing delayed to 09:00 so filling at 12:00 waits exactly 120 min", mix.Start, fill.Start)
	}
}

// Without the network the same order fills on the free F3 straight away,
// which shows the network is what holds it back.
func TestSchedule_WithoutNetworkTheFreeFillerIsUsed(t *testing.T) {
	resources, _ := mixFill(t)
	r := Schedule(Problem{
		Resources: resources,
		Operations: []Operation{
			{ID: "mix", OrderID: "o1", OperNumber: 10, Minutes: 60, ReadyAt: at(0, 8, 0), Candidates: []string{"V1"}},
			{ID: "fill", OrderID: "o1", OperNumber: 20, Minutes: 60, Candidates: []string{"F3"}},
		},
		Start: monday, HorizonDays: 14,
	})
	if fill := placementOf(t, r, "fill"); fill.ResourceID != "F3" || !fill.Start.Equal(at(0, 9, 0)) {
		t.Fatalf("fill = %+v, want F3 at 09:00", fill)
	}
}

// The transfer time of an edge is a minimum gap between the operations.
func TestSchedule_NetworkTransferTimeHoldsTheSuccessor(t *testing.T) {
	net := mustNetwork(t, "N", []NetworkEdge{{From: "V1", To: "F2", TransferMinutes: 10, MaxWaitMinutes: intPtr(120)}})
	r := Schedule(Problem{
		Resources: []Resource{res("V1", "V1", true, allDays(0, 1440)), res("F2", "F2", true, allDays(0, 1440))},
		Operations: []Operation{
			{ID: "mix", OrderID: "o1", OperNumber: 10, Minutes: 60, ReadyAt: at(0, 8, 0), Candidates: []string{"V1"}},
			{ID: "fill", OrderID: "o1", OperNumber: 20, Minutes: 60, Network: "N", Candidates: []string{"F2"}},
		},
		Networks: []ResourceNetwork{net}, Start: monday, HorizonDays: 14,
	})
	if fill := placementOf(t, r, "fill"); !fill.Start.Equal(at(0, 9, 10)) {
		t.Fatalf("fill starts %v, want 09:10 after the 10 min transfer", fill.Start)
	}
}

// BR-23.11: when the wait cannot be met within the delay limit the order is
// flagged NETWORK_INFEASIBLE (SCHED-0009) and none of it is placed; a longer
// limit lets the same order through.
func TestSchedule_NetworkInfeasibleBeyondDelayLimit(t *testing.T) {
	net := mustNetwork(t, "N", []NetworkEdge{{From: "V1", To: "F1", MaxWaitMinutes: intPtr(0)}})
	problem := func(limit int) Problem {
		return Problem{
			Resources: []Resource{res("V1", "V1", true, allDays(0, 1440)), res("F1", "F1", true, allDays(0, 1440))},
			Booked:    []Busy{{ResourceID: "F1", Start: monday, End: at(10, 0, 0), JobID: "x1"}},
			Operations: []Operation{
				{ID: "mix", OrderID: "o1", OperNumber: 10, Minutes: 60, Candidates: []string{"V1"}},
				{ID: "fill", OrderID: "o1", OperNumber: 20, Minutes: 60, Network: "N", Candidates: []string{"F1"}},
				{ID: "other", OrderID: "o2", OperNumber: 10, Minutes: 30, Candidates: []string{"V1"}},
			},
			Networks: []ResourceNetwork{net}, Start: monday, HorizonDays: 30, NetworkDelayDays: limit,
		}
	}
	r := Schedule(problem(0))
	if len(r.Placements) != 1 || r.Placements[0].OperationID != "other" {
		t.Fatalf("placements = %+v, want only the unrelated order", r.Placements)
	}
	if !r.Placements[0].Start.Equal(monday) {
		t.Fatalf("other starts %v; the failed attempts must not leave bookings behind", r.Placements[0].Start)
	}
	if len(r.Unplaced) != 2 {
		t.Fatalf("unplaced = %v, want mix and fill", r.Unplaced)
	}
	for _, u := range r.Unplaced {
		if !strings.HasPrefix(u.Reason, CodeNetworkInfeasible+" "+FlagNetworkInfeasible) {
			t.Fatalf("%s reason %q, want %s %s", u.OperationID, u.Reason, CodeNetworkInfeasible, FlagNetworkInfeasible)
		}
	}
	if !strings.Contains(r.Unplaced[1].Reason, "within 7 days") {
		t.Fatalf("fill reason %q should name the 7 day limit", r.Unplaced[1].Reason)
	}

	r = Schedule(problem(14))
	if len(r.Unplaced) != 0 {
		t.Fatalf("with a 14 day limit: unplaced %v", r.Unplaced)
	}
	mix, fill := placementOf(t, r, "mix"), placementOf(t, r, "fill")
	if !fill.Start.Equal(at(10, 0, 0)) || !mix.End.Equal(fill.Start) {
		t.Fatalf("mix %v-%v fill %v, want mixing to end as filling starts on day 10", mix.Start, mix.End, fill.Start)
	}
}

// A successor whose candidates have no edge from the predecessor's resource
// cannot be fixed by waiting, so the order is flagged at once.
func TestSchedule_NetworkWithoutEdgeIsInfeasibleAtOnce(t *testing.T) {
	net := mustNetwork(t, "N", []NetworkEdge{{From: "V1", To: "F1"}})
	r := Schedule(Problem{
		Resources: []Resource{res("V1", "V1", true, allDays(0, 1440)), res("F3", "F3", true, allDays(0, 1440))},
		Operations: []Operation{
			{ID: "mix", OrderID: "o1", OperNumber: 10, Minutes: 60, Candidates: []string{"V1"}},
			{ID: "fill", OrderID: "o1", OperNumber: 20, Minutes: 60, Network: "N", Candidates: []string{"F3"}},
		},
		Networks: []ResourceNetwork{net}, Start: monday, HorizonDays: 14,
	})
	if len(r.Placements) != 0 || len(r.Unplaced) != 2 {
		t.Fatalf("placements=%v unplaced=%v", r.Placements, r.Unplaced)
	}
	if !strings.Contains(r.Unplaced[1].Reason, "from resource V1") {
		t.Fatalf("reason %q should name the predecessor's resource", r.Unplaced[1].Reason)
	}
}

// An operation naming a network the run does not know is unplaced; its
// predecessor keeps its slot.
func TestSchedule_UnknownNetworkUnplacesTheOperation(t *testing.T) {
	r := Schedule(Problem{
		Resources: []Resource{res("V1", "V1", true, allDays(0, 1440)), res("F1", "F1", true, allDays(0, 1440))},
		Operations: []Operation{
			{ID: "mix", OrderID: "o1", OperNumber: 10, Minutes: 60, Candidates: []string{"V1"}},
			{ID: "fill", OrderID: "o1", OperNumber: 20, Minutes: 60, Network: "GONE", Candidates: []string{"F1"}},
		},
		Start: monday, HorizonDays: 14,
	})
	if len(r.Placements) != 1 || len(r.Unplaced) != 1 || !strings.Contains(r.Unplaced[0].Reason, "network GONE is not defined") {
		t.Fatalf("placements=%v unplaced=%v", r.Placements, r.Unplaced)
	}
}

// A firmed predecessor cannot be delayed: the successor takes the best edge
// candidate and the run warns SCHED-0010.
func TestSchedule_FrozenPredecessorWaitOverrunIsAWarning(t *testing.T) {
	resources, net := mixFill(t)
	r := Schedule(Problem{
		Resources: resources,
		Booked: []Busy{
			{ResourceID: "V1", Start: at(0, 8, 0), End: at(0, 9, 0), JobID: "mix"},
			{ResourceID: "F1", Start: at(0, 8, 0), End: at(0, 12, 0), JobID: "x1"},
			{ResourceID: "F2", Start: at(0, 8, 0), End: at(0, 12, 0), JobID: "x2"},
		},
		Fixed: []Placement{{OperationID: "mix", OrderID: "o1", OperNumber: 10, ResourceID: "V1", Start: at(0, 8, 0), End: at(0, 9, 0)}},
		Operations: []Operation{
			{ID: "fill", OrderID: "o1", OperNumber: 20, Minutes: 60, Network: "MIXFILL", Candidates: []string{"F1", "F2", "F3"}},
		},
		Networks: []ResourceNetwork{net}, Start: monday, HorizonDays: 14,
	})
	fill := placementOf(t, r, "fill")
	if fill.ResourceID != "F1" || !fill.Start.Equal(at(0, 12, 0)) {
		t.Fatalf("fill = %s at %v, want F1 at 12:00", fill.ResourceID, fill.Start)
	}
	if len(r.Warnings) != 1 || r.Warnings[0].Code != CodeNetworkWait || !strings.Contains(r.Warnings[0].Detail, "wait 180 min") {
		t.Fatalf("warnings = %+v, want one SCHED-0010 for a 180 min wait", r.Warnings)
	}
}

// AC-23.11 (heuristic half) and BR-23.12: current group A, orders B then C on
// one resource take 30 + 20 = 50 changeover minutes, each changeover laid in
// front of its job.
func TestSchedule_ChangeoversFollowTheSetupMatrix(t *testing.T) {
	r0 := res("R", "R", true, allDays(0, 1440))
	r0.Matrix, r0.CurrentGroup = br2312(t), "A"
	p := Problem{
		Resources: []Resource{r0},
		Operations: []Operation{
			{ID: "ob", OrderID: "ob", OperNumber: 10, Minutes: 60, DueAt: at(0, 12, 0), SetupGroup: "B", Candidates: []string{"R"}},
			{ID: "oc", OrderID: "oc", OperNumber: 10, Minutes: 60, DueAt: at(0, 18, 0), SetupGroup: "C", Candidates: []string{"R"}},
		},
		Start: monday, HorizonDays: 14,
	}
	r := Schedule(p)
	checkFeasible(t, p, r)
	b, c := placementOf(t, r, "ob"), placementOf(t, r, "oc")
	if b.ChangeoverMinutes != 30 || c.ChangeoverMinutes != 20 {
		t.Fatalf("changeovers %d and %d, want 30 and 20", b.ChangeoverMinutes, c.ChangeoverMinutes)
	}
	if !b.End.Equal(monday.Add(90*time.Minute)) || !c.Start.Equal(b.End) || !c.End.Equal(monday.Add(170*time.Minute)) {
		t.Fatalf("B %v-%v C %v-%v, want B 00:00-01:30 and C 01:30-02:50", b.Start, b.End, c.Start, c.End)
	}
	if len(r.Warnings) != 0 {
		t.Fatalf("warnings = %v, want none", r.Warnings)
	}
}

// A pair without a matrix entry falls back to the route setup time and warns
// SCHED-0012; the same group needs no changeover at all.
func TestSchedule_MissingMatrixEntryUsesRouteSetupAndWarns(t *testing.T) {
	r0 := res("R", "R", true, allDays(0, 1440))
	r0.Matrix, r0.CurrentGroup = mustMatrix(t, SetupEntry{FromGroup: "A", ToGroup: "B", Minutes: 30}), "A"
	p := Problem{
		Resources: []Resource{r0},
		Operations: []Operation{
			{ID: "oa", OrderID: "o1", OperNumber: 10, Minutes: 60, SetupGroup: "A", SetupMinutes: 15, Candidates: []string{"R"}},
			{ID: "oc", OrderID: "o2", OperNumber: 10, Minutes: 60, SetupGroup: "C", SetupMinutes: 45, Candidates: []string{"R"}},
		},
		Start: monday, HorizonDays: 14,
	}
	r := Schedule(p)
	checkFeasible(t, p, r)
	if a := placementOf(t, r, "oa"); a.ChangeoverMinutes != 0 {
		t.Fatalf("same group changeover %d, want 0", a.ChangeoverMinutes)
	}
	if c := placementOf(t, r, "oc"); c.ChangeoverMinutes != 45 {
		t.Fatalf("changeover %d, want the route setup time 45", c.ChangeoverMinutes)
	}
	if got := codesOf(r.Warnings); !slices.Equal(got, []string{CodeMissingSetupEntry + " oc"}) || !strings.Contains(r.Warnings[0].Detail, "A -> C") {
		t.Fatalf("warnings = %+v, want one SCHED-0012 for A -> C", r.Warnings)
	}
}

// A job may go into a gap in front of a booked job only when the booked job's
// changeover still covers the step from the new job's group.
func TestSchedule_GapInsertionKeepsTheNextChangeover(t *testing.T) {
	booked := func(cChangeover int) []Busy {
		return []Busy{
			{ResourceID: "R", Start: at(0, 8, 0), End: at(0, 9, 0), JobID: "ja", Group: "A"},
			{ResourceID: "R", Start: at(0, 12, 0), End: at(0, 13, 30), JobID: "jc", Group: "C", Changeover: cChangeover},
		}
	}
	for _, tc := range []struct {
		name       string
		changeover int
		start      time.Time
		co         int
	}{
		{"booked C kept 90 min, B fits in the gap", 90, at(0, 9, 0), 30},
		{"booked C kept only 10 min, B goes after it", 10, at(0, 13, 30), 60},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r0 := res("R", "R", true, allDays(0, 1440))
			r0.Matrix = br2312(t)
			p := Problem{Resources: []Resource{r0}, Booked: booked(tc.changeover), Start: monday, HorizonDays: 14,
				Operations: []Operation{{ID: "ob", OrderID: "o1", OperNumber: 10, Minutes: 60, ReadyAt: at(0, 8, 0), SetupGroup: "B", Candidates: []string{"R"}}}}
			pl := placementOf(t, Schedule(p), "ob")
			if !pl.Start.Equal(tc.start) || pl.ChangeoverMinutes != tc.co {
				t.Fatalf("B at %v with %d min changeover, want %v with %d", pl.Start, pl.ChangeoverMinutes, tc.start, tc.co)
			}
		})
	}
}

// An infinite run has no job sequence, so it charges no changeovers.
func TestSchedule_InfiniteRunChargesNoChangeover(t *testing.T) {
	r0 := res("R", "R", true, allDays(0, 1440))
	r0.Matrix, r0.CurrentGroup = br2312(t), "A"
	r := Schedule(Problem{Resources: []Resource{r0}, Infinite: true, Start: monday, HorizonDays: 14,
		Operations: []Operation{{ID: "ob", OrderID: "o1", OperNumber: 10, Minutes: 60, SetupGroup: "B", Candidates: []string{"R"}}}})
	if pl := placementOf(t, r, "ob"); pl.ChangeoverMinutes != 0 || !pl.End.Equal(monday.Add(time.Hour)) {
		t.Fatalf("placement = %+v, want 60 min with no changeover", pl)
	}
}

// AC-23.13 and BR-23.15: a Monday block for color = white. The white job goes
// inside it; the red job is placed outside it and flagged SCHED-0014.
func TestSchedule_BlockTakesMatchingJobsOnly(t *testing.T) {
	r0 := res("R", "R", true, weekdayShift(8*60, 16*60))
	r0.Blocks = []CapacityBlock{{
		ID: "mon-white", ResourceID: "R", Priority: 1,
		Windows:   []Interval{{Start: at(0, 8, 0), End: at(0, 12, 0)}},
		Condition: []Predicate{{Attribute: "color", Op: "=", Value: "white"}},
	}}
	p := Problem{
		Resources: []Resource{r0},
		Operations: []Operation{
			{ID: "red", OrderID: "a-red", OperNumber: 10, Minutes: 120, ReadyAt: at(0, 8, 0),
				Attributes: map[string]string{"color": "red"}, Candidates: []string{"R"}},
			{ID: "white", OrderID: "b-white", OperNumber: 10, Minutes: 120, ReadyAt: at(0, 8, 0),
				Attributes: map[string]string{"color": "white"}, Candidates: []string{"R"}},
		},
		Start: monday, HorizonDays: 14,
	}
	r := Schedule(p)
	checkFeasible(t, p, r)
	red, white := placementOf(t, r, "red"), placementOf(t, r, "white")
	if !red.Start.Equal(at(0, 12, 0)) {
		t.Fatalf("red job starts %v, want 12:00 after the white block although it is planned first", red.Start)
	}
	if !white.Start.Equal(at(0, 8, 0)) || !white.End.Equal(at(0, 10, 0)) {
		t.Fatalf("white job %v-%v, want inside the block 08:00-10:00", white.Start, white.End)
	}
	if got := codesOf(r.Warnings); !slices.Equal(got, []string{CodeNoMatchingBlock + " red"}) {
		t.Fatalf("warnings = %v, want one SCHED-0014 for red", got)
	}
}

// Property: a job that does not match a block never runs inside its window.
func TestSchedule_PropertyUnmatchedJobsStayOutOfBlocks(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		r0 := res("R", "R", true, weekdayShift(6*60, 18*60))
		window := Interval{Start: at(0, 8, 0), End: at(0, 12, 0)}
		r0.Blocks = []CapacityBlock{{ID: "mon-white", ResourceID: "R", Priority: 1, Windows: []Interval{window},
			Condition: []Predicate{{Attribute: "color", Op: "=", Value: "white"}}}}
		var ops []Operation
		for i := range rapid.IntRange(1, 12).Draw(t, "operations") {
			color := rapid.SampledFrom([]string{"red", "white"}).Draw(t, "color")
			ops = append(ops, Operation{ID: fmt.Sprintf("j%d", i), OrderID: fmt.Sprintf("o%d", i), OperNumber: 10,
				Minutes: rapid.IntRange(10, 180).Draw(t, "minutes"), Attributes: map[string]string{"color": color}, Candidates: []string{"R"}})
		}
		p := Problem{Resources: []Resource{r0}, Operations: ops, Start: monday, HorizonDays: 14}
		r := Schedule(p)
		checkFeasible(t, p, r)
		color := map[string]string{}
		for _, op := range ops {
			color[op.ID] = op.Attributes["color"]
		}
		for _, pl := range r.Placements {
			for _, s := range pl.Segments {
				if color[pl.OperationID] != "white" && s.Start.Before(window.End) && window.Start.Before(s.End) {
					t.Fatalf("unmatched %s placed in the block at %v", pl.OperationID, s)
				}
			}
		}
	})
}

// Property: with networks and changeovers together the schedule stays
// feasible, every network step uses an edge within its limits, and every job
// on a sequenced resource carries at least the changeover its predecessor
// needs (a job without a setup group leaves the setup as it was).
func TestSchedule_PropertyNetworksAndChangeoversStayFeasible(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		m := mustMatrix(t,
			SetupEntry{FromGroup: "g0", ToGroup: "g1", Minutes: 15},
			SetupEntry{FromGroup: "g1", ToGroup: "g0", Minutes: 25},
			SetupEntry{FromGroup: "g1", ToGroup: "g2", Minutes: 40},
		)
		var resources []Resource
		var rids []string
		for i := range rapid.IntRange(2, 4).Draw(t, "resources") {
			id := fmt.Sprintf("R%d", i)
			rids = append(rids, id)
			r0 := res(id, id, true, weekdayShift(6*60, 22*60))
			r0.Matrix, r0.CurrentGroup = m, "g0"
			resources = append(resources, r0)
		}
		var edges []NetworkEdge
		for _, a := range rids {
			for _, b := range rids {
				if a != b && rapid.Bool().Draw(t, "edge") {
					e := NetworkEdge{From: a, To: b, TransferMinutes: rapid.IntRange(0, 30).Draw(t, "transfer")}
					if rapid.Bool().Draw(t, "hasWait") {
						e.MaxWaitMinutes = intPtr(e.TransferMinutes + rapid.IntRange(0, 300).Draw(t, "wait"))
					}
					edges = append(edges, e)
				}
			}
		}
		net := mustNetwork(t, "N", edges)
		var ops []Operation
		for o := range rapid.IntRange(1, 5).Draw(t, "orders") {
			for k := range rapid.IntRange(1, 3).Draw(t, "steps") {
				op := Operation{ID: fmt.Sprintf("o%d-%d", o, k), OrderID: fmt.Sprintf("o%d", o), OperNumber: (k + 1) * 10,
					Minutes: rapid.IntRange(1, 240).Draw(t, "minutes"), SetupMinutes: rapid.IntRange(0, 30).Draw(t, "setup"),
					SetupGroup: rapid.SampledFrom([]string{"", "g0", "g1", "g2"}).Draw(t, "group"),
					Candidates: rids[:rapid.IntRange(1, len(rids)).Draw(t, "candidates")]}
				if k > 0 && rapid.Bool().Draw(t, "networked") {
					op.Network = "N"
				}
				ops = append(ops, op)
			}
		}
		p := Problem{Resources: resources, Operations: ops, Networks: []ResourceNetwork{net}, Start: monday, HorizonDays: 30}
		r := Schedule(p)
		checkFeasible(t, p, r)
		byID := map[string]Placement{}
		for _, pl := range r.Placements {
			byID[pl.OperationID] = pl
		}
		opByID := map[string]Operation{}
		for i, op := range ops {
			opByID[op.ID] = op
			cur, ok := byID[op.ID]
			if !ok || op.Network == "" {
				continue
			}
			prev, ok := byID[ops[i-1].ID]
			if !ok {
				t.Fatalf("%s placed without its predecessor", op.ID)
			}
			edge, ok := net.Edge(prev.ResourceID, cur.ResourceID)
			if !ok || !edge.WaitOK(prev.End, cur.Start) {
				t.Fatalf("%s: step %s->%s (edge %v) waits %v outside the edge limits", op.ID, prev.ResourceID, cur.ResourceID, ok, cur.Start.Sub(prev.End))
			}
		}
		byRes := map[string][]Placement{}
		for _, pl := range r.Placements {
			byRes[pl.ResourceID] = append(byRes[pl.ResourceID], pl)
		}
		for rid, pls := range byRes {
			slices.SortFunc(pls, func(a, b Placement) int { return a.Start.Compare(b.Start) })
			group := "g0"
			for _, pl := range pls {
				op := opByID[pl.OperationID]
				if need, _ := changeoverNeed(m, group, op.SetupGroup, op.SetupMinutes); pl.ChangeoverMinutes < need {
					t.Fatalf("%s on %s after %q carries %d min changeover, needs %d", op.ID, rid, group, pl.ChangeoverMinutes, need)
				}
				if op.SetupGroup != "" {
					group = op.SetupGroup
				}
			}
		}
	})
}

// pl builds a one-segment placement for move inputs.
func pl(op Operation, rid string, start time.Time, co int) Placement {
	end := start.Add(minutes(op.Minutes + co))
	return Placement{OperationID: op.ID, OrderID: op.OrderID, OperNumber: op.OperNumber, ResourceID: rid,
		Start: start, End: end, Segments: []Segment{{start, end}}, ChangeoverMinutes: co}
}

// sequencedLine is resource R with the BR-23.12 matrix, starting in group A,
// holding a (A) 08:00-09:00, b (B, 30 min changeover) 09:00-10:30 and c (C,
// 20 min changeover) 10:30-11:50, each its own order; R2 is a plain resource.
func sequencedLine(t *testing.T) (MoveInput, map[string]Operation) {
	r := res("R", "R", true, allDays(0, 1440))
	r.Matrix, r.CurrentGroup = br2312(t), "A"
	ops := map[string]Operation{
		"a": {ID: "a", OrderID: "oa", OperNumber: 10, Minutes: 60, SetupGroup: "A", Candidates: []string{"R"}},
		"b": {ID: "b", OrderID: "ob", OperNumber: 10, Minutes: 60, SetupGroup: "B", Candidates: []string{"R", "R2"}},
		"c": {ID: "c", OrderID: "oc", OperNumber: 10, Minutes: 60, SetupGroup: "C", Candidates: []string{"R"}},
	}
	current := map[string]Placement{
		"a": pl(ops["a"], "R", at(0, 8, 0), 0),
		"b": pl(ops["b"], "R", at(0, 9, 0), 30),
		"c": pl(ops["c"], "R", at(0, 10, 30), 20),
	}
	var booked []Busy
	for _, id := range []string{"a", "b", "c"} {
		booked = append(booked, busyOf(ops[id], current[id])...)
	}
	return MoveInput{Resources: []Resource{r, res("R2", "R2", true, allDays(0, 1440))}, Start: monday, HorizonDays: 14,
		Booked: booked, Current: current}, ops
}

func TestPlanMove_RepricesTheChangeoverOfTheMovedJob(t *testing.T) {
	in, ops := sequencedLine(t)
	in.Order, in.Moved, in.ResourceID, in.At = []Operation{ops["c"]}, "c", "R", at(0, 14, 0)
	plan := PlanMove(in)
	if plan.Conflict != nil || len(plan.Changes) != 1 {
		t.Fatalf("plan = %+v", plan)
	}
	if got := plan.Changes[0]; got.ChangeoverMinutes != 20 || !got.End.Equal(at(0, 15, 20)) {
		t.Fatalf("moved c = %+v, want the B -> C changeover of 20 min, ending 15:20", got)
	}
}

// Taking b off R leaves c straight after a: A -> C needs 90 min but c carries
// 20, so the move is refused naming c.
func TestPlanMove_RefusesToLeaveTheNextJobShortOfChangeover(t *testing.T) {
	in, ops := sequencedLine(t)
	in.Order, in.Moved, in.ResourceID, in.At = []Operation{ops["b"]}, "b", "R2", at(0, 9, 0)
	plan := PlanMove(in)
	if plan.Conflict == nil || plan.Conflict.Code != CodeOverlap || plan.Conflict.JobID != "c" {
		t.Fatalf("plan = %+v, want %s naming c", plan, CodeOverlap)
	}
}

func TestPlanMove_NetworkStepsAreChecked(t *testing.T) {
	resources, net := mixFill(t)
	mix := Operation{ID: "mix", OrderID: "o1", OperNumber: 10, Minutes: 60, Candidates: []string{"V1"}}
	fill := Operation{ID: "fill", OrderID: "o1", OperNumber: 20, Minutes: 60, Network: "MIXFILL", Candidates: []string{"F1", "F3"}}
	current := map[string]Placement{"mix": pl(mix, "V1", at(0, 8, 0), 0), "fill": pl(fill, "F1", at(0, 9, 0), 0)}
	base := MoveInput{Resources: resources, Start: monday, HorizonDays: 14, Order: []Operation{mix, fill}, Current: current,
		Networks: []ResourceNetwork{net}, Booked: append(busyOf(mix, current["mix"]), busyOf(fill, current["fill"])...)}
	cases := []struct {
		name  string
		moved string
		rid   string
		at    time.Time
		code  string
	}{
		{"filling on F3 has no edge from V1", "fill", "F3", at(0, 9, 0), CodeNetworkInfeasible},
		{"filling 3 hours after mixing breaks the 120 min wait", "fill", "F1", at(0, 12, 0), CodeNetworkWait},
		{"mixing earlier stretches the wait", "mix", "V1", at(0, 5, 0), CodeNetworkWait},
		{"filling within the wait is fine", "fill", "F1", at(0, 10, 0), ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := base
			in.Moved, in.ResourceID, in.At = c.moved, c.rid, c.at
			plan := PlanMove(in)
			switch {
			case c.code == "" && plan.Conflict != nil:
				t.Fatalf("conflict %+v, want none", plan.Conflict)
			case c.code != "" && (plan.Conflict == nil || plan.Conflict.Code != c.code):
				t.Fatalf("plan = %+v, want %s", plan, c.code)
			}
		})
	}
}

func TestPlanMove_JobCannotStartInsideABlockItDoesNotMatch(t *testing.T) {
	r := res("R", "R", true, weekdayShift(8*60, 16*60))
	r.Blocks = []CapacityBlock{{ID: "white", ResourceID: "R", Windows: []Interval{{Start: at(0, 8, 0), End: at(0, 12, 0)}},
		Condition: []Predicate{{Attribute: "color", Op: "=", Value: "white"}}}}
	red := Operation{ID: "red", OrderID: "o", OperNumber: 10, Minutes: 60, Attributes: map[string]string{"color": "red"}, Candidates: []string{"R"}}
	in := MoveInput{Resources: []Resource{r}, Start: monday, HorizonDays: 14, Order: []Operation{red},
		Current: map[string]Placement{"red": pl(red, "R", at(0, 12, 0), 0)}, Moved: "red", ResourceID: "R"}
	in.At = at(0, 9, 0)
	if plan := PlanMove(in); plan.Conflict == nil || plan.Conflict.Code != CodeOutsideCapacity || !strings.Contains(plan.Conflict.Detail, "block") {
		t.Fatalf("plan = %+v, want SCHED-0002 naming the block", plan)
	}
	in.At = at(0, 13, 0)
	if plan := PlanMove(in); plan.Conflict != nil {
		t.Fatalf("move after the block: %+v", plan.Conflict)
	}
}
