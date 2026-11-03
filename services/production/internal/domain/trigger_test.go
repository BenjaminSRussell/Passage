package domain

import (
	"testing"

	"pgregory.net/rapid"
)

func rework(resource string) TriggerOperation {
	return TriggerOperation{ResourceID: resource, RunHPer: d("0.5"), LaborRate: d("30")}
}

func TestParseCondition(t *testing.T) {
	cases := []struct {
		in, canonical string
	}{
		{"qty_error > 0", "qty_error > 0"},
		{"  hours   >=   2.50 ", "hours >= 2.5"},
		{"qty_good != 10", "qty_good != 10"},
		{"qty_error == -1", "qty_error == -1"},
		{"qty_error>0", ""},
		{"scrap > 0", ""},
		{"qty_error ~ 0", ""},
		{"qty_error > x", ""},
		{"hours > 1e-2000000000", ""},
		{"hours > 1e3", ""},
		{"hours > .5", ""},
		{"hours > 1E5", ""},
		{"hours > 0.000001", "hours > 0.000001"},
		{"qty_error > 0 and hours > 1", "qty_error > 0 and hours > 1"},
		{"qty_error > 0 or hours >= 8.0 and qty_good < 5", "qty_error > 0 or hours >= 8 and qty_good < 5"},
		{"error_pct >= 5", "error_pct >= 5"},
		{"qty_total == 100", "qty_total == 100"},
		{"qty_error > 0 and", ""},
		{"qty_error > 0 xor hours > 1", ""},
		{"qty_error > 0 and hours", ""},
		{"a > 1 and a > 1 and a > 1 and a > 1 and a > 1 and a > 1", ""},
		{"qty_error > 0 and hours > 1 and hours < 9 and qty_good > 1 and qty_good < 9 and qty_total > 3", ""},
		{"", ""},
	}
	for _, c := range cases {
		got, err := ParseCondition(c.in)
		if c.canonical == "" {
			if codeOf(err) != CodeTriggerCondition {
				t.Errorf("ParseCondition(%q) = %v, want %s", c.in, err, CodeTriggerCondition)
			}
			continue
		}
		if err != nil || got.String() != c.canonical {
			t.Errorf("ParseCondition(%q) = %q, %v; want %q", c.in, got.String(), err, c.canonical)
		}
	}
}

func TestCondition_Holds(t *testing.T) {
	f := Feedback{Hours: d("1.5"), QtyGood: d("97"), QtyError: d("3")}
	cases := map[string]bool{
		"qty_error > 0": true, "qty_error >= 3": true, "qty_error < 3": false, "qty_error <= 3": true,
		"qty_error == 3.000": true, "qty_error != 3": false, "hours > 1.5": false, "hours >= 1.5": true,
		"qty_good < 100": true, "qty_good > 97": false,
	}
	for cond, want := range cases {
		c, err := ParseCondition(cond)
		if err != nil {
			t.Fatalf("%s: %v", cond, err)
		}
		if got := c.Holds(f); got != want {
			t.Errorf("%s holds = %v, want %v", cond, got, want)
		}
	}
	// error_pct and qty_total are derived; and binds tighter than or.
	derived := map[string]bool{
		"error_pct >= 3": true, "error_pct > 3": false, "qty_total == 100": true,
		"qty_error > 5 or hours > 1": true, "qty_error > 5 and hours > 1": false,
		"qty_error > 5 and hours > 1 or qty_good == 97": true, "qty_error > 0 and hours > 1 and qty_good < 100": true,
	}
	for cond, want := range derived {
		c, err := ParseCondition(cond)
		if err != nil {
			t.Fatalf("%s: %v", cond, err)
		}
		if got := c.Holds(f); got != want {
			t.Errorf("%s holds = %v, want %v", cond, got, want)
		}
	}
	// With nothing reported the error percentage is 0, not a division by zero.
	if c, _ := ParseCondition("error_pct > 0"); c.Holds(Feedback{Hours: d("2")}) {
		t.Error("error_pct > 0 held without a quantity")
	}
	// A route card that reports no error quantity does not fire "qty_error > 0".
	if c, _ := ParseCondition("qty_error > 0"); c.Holds(Feedback{Hours: d("2")}) {
		t.Error("qty_error > 0 held without an error quantity")
	}
}

func TestCheckTriggerPoint(t *testing.T) {
	ops := []TriggerOperation{rework("r1")}
	cases := []struct {
		name string
		tp   TriggerPoint
		code string
	}{
		{"insert", TriggerPoint{Condition: "qty_error > 0", Function: FnInsertOperations, Operations: ops}, ""},
		{"rework order", TriggerPoint{Condition: "qty_error > 0", Function: FnCreateReworkOrder, Operations: ops}, ""},
		{"notify", TriggerPoint{Condition: "hours > 8", Function: FnNotify}, ""},
		{"on finish", TriggerPoint{Condition: "error_pct > 2 and hours > 8", Event: EventOperationFinished, Function: FnNotify}, ""},
		{"unknown event", TriggerPoint{Condition: "hours > 8", Event: "start", Function: FnNotify}, CodeInvalidInput},
		{"bad condition", TriggerPoint{Condition: "qty_error", Function: FnNotify}, CodeTriggerCondition},
		{"unknown function", TriggerPoint{Condition: "qty_error > 0", Function: "explode"}, CodeInvalidInput},
		{"release-next", TriggerPoint{Condition: "qty_good > 0", Function: FnReleaseNext}, CodeTriggerFunctionUnsupported},
		{"start-workflow", TriggerPoint{Condition: "qty_good > 0", Function: FnStartWorkflow}, CodeTriggerFunctionUnsupported},
		{"insert without operations", TriggerPoint{Condition: "qty_error > 0", Function: FnInsertOperations}, CodeInvalidInput},
		{"rework without route", TriggerPoint{Condition: "qty_error > 0", Function: FnCreateReworkOrder}, CodeInvalidInput},
		{"notify with operations", TriggerPoint{Condition: "qty_error > 0", Function: FnNotify, Operations: ops}, CodeInvalidInput},
		{"operation without resource", TriggerPoint{Condition: "qty_error > 0", Function: FnInsertOperations,
			Operations: []TriggerOperation{{RunHPer: d("1")}}}, CodeInvalidInput},
		{"negative rate", TriggerPoint{Condition: "qty_error > 0", Function: FnInsertOperations,
			Operations: []TriggerOperation{{ResourceID: "r", LaborRate: d("-1")}}}, CodeInvalidInput},
		{"scrap of 100", TriggerPoint{Condition: "qty_error > 0", Function: FnCreateReworkOrder,
			Operations: []TriggerOperation{{ResourceID: "r", ScrapPct: d("100")}}}, CodeInvalidInput},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := codeOf(CheckTriggerPoint(c.tp)); got != c.code {
				t.Fatalf("code = %q, want %q", got, c.code)
			}
		})
	}
}

// BR-21.13: due triggers of the operation in ascending sequence; a fired non-repeatable
// trigger is skipped, a repeatable one fires again; other operations are ignored.
func TestDueTriggers(t *testing.T) {
	tps := []TriggerPoint{
		{ID: "c", OperNumber: 20, Sequence: 30, Condition: "qty_error > 0", Repeatable: true, FiredCount: 4},
		{ID: "a", OperNumber: 20, Sequence: 10, Condition: "qty_error > 0"},
		{ID: "x", OperNumber: 20, Sequence: 5, Condition: "qty_error > 0", FiredCount: 1},
		{ID: "b", OperNumber: 20, Sequence: 20, Condition: "hours >= 1"},
		{ID: "n", OperNumber: 20, Sequence: 1, Condition: "qty_good > 100"},
		{ID: "o", OperNumber: 30, Sequence: 1, Condition: "qty_error > 0"},
	}
	due, err := DueTriggers(tps, 20, EventFeedback, Feedback{Hours: d("1"), QtyGood: d("7"), QtyError: d("3")})
	if err != nil {
		t.Fatal(err)
	}
	var got string
	for _, tp := range due {
		got += tp.ID
	}
	if got != "abc" {
		t.Fatalf("due = %q, want abc", got)
	}
	// A trigger on the operation finishing is not due on feedback, and the other way round.
	tps = append(tps, TriggerPoint{ID: "f", OperNumber: 20, Sequence: 40, Condition: "qty_error > 0", Event: EventOperationFinished})
	if due, _ := DueTriggers(tps, 20, EventFeedback, Feedback{QtyError: d("3")}); len(due) == 0 || due[len(due)-1].ID == "f" {
		t.Fatalf("feedback due = %+v", due)
	}
	if due, _ := DueTriggers(tps, 20, EventOperationFinished, Feedback{QtyError: d("3")}); len(due) != 1 || due[0].ID != "f" {
		t.Fatalf("finish due = %+v", due)
	}
	if _, err := DueTriggers([]TriggerPoint{{OperNumber: 20, Condition: "bad"}}, 20, EventFeedback, Feedback{}); codeOf(err) != CodeTriggerCondition {
		t.Fatalf("a stored bad condition: %v", err)
	}
}

func route(numbers ...int) []Operation {
	out := make([]Operation, len(numbers))
	for i, n := range numbers {
		out[i] = Operation{OperNumber: n, ResourceID: "r", ProcessQty: d("100")}
	}
	return out
}

// AC-21.13: "qty_error > 0 → insert R10, R20" on operation 20 with 3 error units inserts
// operations 25 and 26 for the 3 units, between 20 and 30.
func TestInsertAfter_AC2113(t *testing.T) {
	got, err := InsertAfter(route(10, 20, 30), 20, []TriggerOperation{rework("R10"), rework("R20")}, Feedback{QtyError: d("3")})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].OperNumber != 25 || got[1].OperNumber != 26 || got[0].ResourceID != "R10" || got[1].ResourceID != "R20" {
		t.Fatalf("inserted = %+v", got)
	}
	for _, op := range got {
		if !op.ProcessQty.Equal(d("3")) || op.Status != OperOpen || !op.LaborRate.Equal(d("30")) {
			t.Fatalf("inserted operation = %+v", op)
		}
	}
}

func TestInsertAfter_Numbering(t *testing.T) {
	two := []TriggerOperation{rework("a"), rework("b")}
	cases := []struct {
		name    string
		ops     []Operation
		current int
		want    []int
		code    string
	}{
		{"last operation", route(10, 20), 20, []int{25, 26}, ""},
		{"collision renumbers into the gap", route(10, 20, 26), 20, []int{21, 22}, ""},
		{"after an earlier insertion", route(10, 20, 25, 26, 30), 20, []int{21, 22}, ""},
		{"no room", route(10, 20, 22), 20, nil, CodeTriggerFunctionFailed},
		{"unknown operation", route(10, 20), 15, nil, CodeTriggerFunctionFailed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := InsertAfter(c.ops, c.current, two, Feedback{})
			if codeOf(err) != c.code {
				t.Fatalf("err = %v, want %q", err, c.code)
			}
			if c.code != "" {
				return
			}
			for i, op := range got {
				if op.OperNumber != c.want[i] {
					t.Fatalf("numbers = %+v, want %v", got, c.want)
				}
				// Without an error quantity the inserted operation processes what the reporting one does.
				if !op.ProcessQty.Equal(d("100")) {
					t.Fatalf("process qty = %s", op.ProcessQty)
				}
			}
		})
	}
	if _, err := InsertAfter(route(10), 10, nil, Feedback{}); codeOf(err) != CodeTriggerFunctionFailed {
		t.Fatalf("no operations: %v", err)
	}
}

// Property: whatever the route, an insertion either fails with
// PRODUCTION_TRIGGER_FUNCTION_FAILED or yields new numbers strictly between the reporting
// operation and the next one, so the merged route still strictly increases and no
// existing operation is renumbered.
func TestInsertAfter_Property(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		numbers := rapid.SliceOfNDistinct(rapid.IntRange(1, 200), 1, 12, rapid.ID[int]).Draw(t, "numbers")
		ops := route(sortedInts(numbers)...)
		current := ops[rapid.IntRange(0, len(ops)-1).Draw(t, "at")].OperNumber
		n := rapid.IntRange(1, 6).Draw(t, "inserted")
		inserted := make([]TriggerOperation, n)
		for i := range inserted {
			inserted[i] = rework("r")
		}
		got, err := InsertAfter(ops, current, inserted, Feedback{QtyError: d("2")})
		if err != nil {
			if codeOf(err) != CodeTriggerFunctionFailed {
				t.Fatalf("unexpected error %v", err)
			}
			return
		}
		merged := append(append([]Operation(nil), ops...), got...)
		sortOps(merged)
		if _, err := PlanOperations(merged, d("10")); err != nil {
			t.Fatalf("merged route does not strictly increase: %v", err)
		}
		next := 0
		for _, op := range ops {
			if op.OperNumber > current && (next == 0 || op.OperNumber < next) {
				next = op.OperNumber
			}
		}
		for _, op := range got {
			if op.OperNumber <= current || (next != 0 && op.OperNumber >= next) {
				t.Fatalf("operation %d not between %d and %d", op.OperNumber, current, next)
			}
		}
	})
}

func sortedInts(in []int) []int {
	out := append([]int(nil), in...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func sortOps(ops []Operation) {
	for i := 1; i < len(ops); i++ {
		for j := i; j > 0 && ops[j].OperNumber < ops[j-1].OperNumber; j-- {
			ops[j], ops[j-1] = ops[j-1], ops[j]
		}
	}
}

func TestReworkRoute(t *testing.T) {
	ops := []TriggerOperation{rework("a"), {ResourceID: "b", ScrapPct: d("50")}}
	got, err := ReworkRoute(ops, Feedback{QtyError: d("3")})
	if err != nil {
		t.Fatal(err)
	}
	// BR-21.8: the second operation scraps half, so both start 6 for 3 good units.
	if len(got) != 2 || got[0].OperNumber != 10 || got[1].OperNumber != 20 || !got[0].ProcessQty.Equal(d("6")) || !got[1].ProcessQty.Equal(d("6")) {
		t.Fatalf("route = %+v", got)
	}
	if _, err := ReworkRoute(ops, Feedback{QtyGood: d("5")}); codeOf(err) != CodeTriggerFunctionFailed {
		t.Fatalf("no error quantity: %v", err)
	}
	if _, err := ReworkRoute(nil, Feedback{QtyError: d("1")}); codeOf(err) != CodeTriggerFunctionFailed {
		t.Fatalf("no route: %v", err)
	}
}
