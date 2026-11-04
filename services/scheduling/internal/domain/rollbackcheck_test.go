package domain

import (
	"testing"
	"time"
)

func hr(h float64) time.Time {
	return time.Date(2027, 1, 4, 0, 0, 0, 0, time.UTC).Add(time.Duration(h * float64(time.Hour)))
}

func placed(id, order string, oper int, from, to float64) Placement {
	return Placement{OperationID: id, OrderID: order, OperNumber: oper, ResourceID: "r1",
		Segments: []Segment{{Start: hr(from), End: hr(to)}}, Start: hr(from), End: hr(to)}
}

func TestChainBreaksOnlyBlamesPairsWithARestoredJob(t *testing.T) {
	ops := []Operation{{ID: "a", OrderID: "o1", OperNumber: 10}, {ID: "b", OrderID: "o1", OperNumber: 20, QueueBefore: 60}, {ID: "c", OrderID: "o1", OperNumber: 30}}
	a, b, c := placed("a", "o1", 10, 8, 10), placed("b", "o1", 20, 10.5, 12), placed("c", "o1", 30, 11, 13)
	plan := map[string]Placement{"a": a, "b": b, "c": c}

	// b waits only 30 min behind a against a 60 min queue before; c starts before b ends.
	if got := ChainBreaks(ops, plan, map[string]bool{}); len(got) != 0 {
		t.Fatalf("nothing restored: %+v, want no break", got)
	}
	got := ChainBreaks(ops, plan, map[string]bool{"a": true})
	if len(got) != 1 || got[0].JobID != "a" {
		t.Fatalf("a restored: %+v, want the a-b queue break blamed on a", got)
	}
	got = ChainBreaks(ops, plan, map[string]bool{"c": true})
	if len(got) != 1 || got[0].JobID != "c" {
		t.Fatalf("c restored: %+v, want the b-c break on c", got)
	}

	// An unplaced operation in between is skipped: a and c are compared directly.
	delete(plan, "b")
	if got := ChainBreaks(ops, plan, map[string]bool{"c": true}); len(got) != 0 {
		t.Fatalf("b unplaced, c after a: %+v, want no break", got)
	}
}

func TestChainBreaksHonoursOverlapRelease(t *testing.T) {
	ops := []Operation{{ID: "a", OrderID: "o1", OperNumber: 10}, {ID: "b", OrderID: "o1", OperNumber: 20}}
	a := placed("a", "o1", 10, 8, 12)
	a.OverlapBP = MaxOverlapBP / 4 // released after an hour of its four
	early := placed("b", "o1", 20, 9, 13)
	if got := ChainBreaks(ops, map[string]Placement{"a": a, "b": early}, map[string]bool{"b": true}); len(got) != 0 {
		t.Fatalf("b from the release, ending after a: %+v, want no break", got)
	}
	tooEarly := placed("b", "o1", 20, 9, 11) // starts at the release but ends before a
	if got := ChainBreaks(ops, map[string]Placement{"a": a, "b": tooEarly}, map[string]bool{"b": true}); len(got) != 1 {
		t.Fatalf("b ending before a: %+v, want a break", got)
	}
}

func TestChangeoverShortfallsAroundRestoredJobs(t *testing.T) {
	m, err := NewSetupMatrix([]SetupEntry{{FromGroup: "A", ToGroup: "B", Minutes: 30}, {FromGroup: "B", ToGroup: "A", Minutes: 45}})
	if err != nil {
		t.Fatal(err)
	}
	r := Resource{ID: "r1", Finite: true, Matrix: m, CurrentGroup: "A"}
	bs := []Busy{
		{ResourceID: "r1", JobID: "x", Start: hr(8), End: hr(9), Group: "A"},
		{ResourceID: "r1", JobID: "y", Start: hr(9), End: hr(10), Group: "B", Changeover: 10}, // needs 30 after A
		{ResourceID: "r1", JobID: "y", Start: hr(10), End: hr(11), Group: "B"},                // second segment
		{ResourceID: "r1", JobID: "z", Start: hr(11), End: hr(12), Group: "A", Changeover: 45},
	}
	if got := ChangeoverShortfalls(r, bs, map[string]bool{"z": true}); len(got) != 0 {
		t.Fatalf("z restored: %+v, want none (z has its 45 min, y is not next to a restored job before it)", got)
	}
	got := ChangeoverShortfalls(r, bs, map[string]bool{"x": true})
	if len(got) != 1 || got[0].JobID != "y" {
		t.Fatalf("x restored: %+v, want y short of its changeover after x", got)
	}
	if got := ChangeoverShortfalls(Resource{ID: "r1", Finite: false, Matrix: m}, bs, map[string]bool{"x": true}); got != nil {
		t.Fatalf("infinite resource: %+v, want no check", got)
	}
}
