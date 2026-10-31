package domain

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/shopspring/decimal"
)

// kanbanSim is testdata/lean-kanban-sim.json, written by the independent
// reference testdata/gen_lean_kanban_sim.py (exact rationals, no engine code).
type kanbanSim struct {
	SalesOrder string `json:"salesOrder"`
	Rules      []struct {
		Code     string `json:"code"`
		FixedQty string `json:"fixedQty"`
		ExactQty bool   `json:"exactQty"`
	} `json:"rules"`
	Lines []struct {
		SalesLine        string   `json:"salesLine"`
		Rule             string   `json:"rule"`
		DemandQty        string   `json:"demandQty"`
		Note             string   `json:"note"`
		ExpectedCards    []string `json:"expectedCards"`
		ExpectedReceived string   `json:"expectedReceived"`
	} `json:"lines"`
	ExpectedCardCount      int               `json:"expectedCardCount"`
	ExpectedReceivedByRule map[string]string `json:"expectedReceivedByRule"`
}

// TestEventKanbanSimulation_SalesOrderFixture runs the lean-kanban-sim sales
// order (Wave 24 proof, AC-24.6 widened): eleven lines over three event rules,
// each generating ⌈demand / fixed⌉ cards (the exact rule's last card holding the
// remainder), every card refused a Planned → Completed drag (AC-24.7), then
// walked Prepared → In progress → Completed, receiving the reference totals.
func TestEventKanbanSimulation_SalesOrderFixture(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/lean-kanban-sim.json")
	if err != nil {
		t.Fatal(err)
	}
	var sim kanbanSim
	if err := json.Unmarshal(raw, &sim); err != nil {
		t.Fatal(err)
	}
	type rule struct {
		fixed decimal.Decimal
		exact bool
	}
	rules := map[string]rule{}
	for _, r := range sim.Rules {
		rules[r.Code] = rule{decimal.RequireFromString(r.FixedQty), r.ExactQty}
	}
	n := 0
	newID := func() string { n++; return fmt.Sprintf("card-%d", n) }
	received := map[string]decimal.Decimal{}
	cards := 0
	for _, l := range sim.Lines {
		r := rules[l.Rule]
		got, err := GenerateEventKanbans(l.Rule, decimal.RequireFromString(l.DemandQty), r.fixed, r.exact, newID)
		if err != nil {
			t.Fatalf("%s: %v", l.SalesLine, err)
		}
		if len(got) != len(l.ExpectedCards) {
			t.Fatalf("%s (%s): %d cards, want %d", l.SalesLine, l.Note, len(got), len(l.ExpectedCards))
		}
		line := decimal.Zero
		for i := range got {
			k := &got[i]
			if !k.Qty.Equal(decimal.RequireFromString(l.ExpectedCards[i])) || k.State != Planned {
				t.Fatalf("%s card %d: %s in %s, want %s in planned", l.SalesLine, i, k.Qty, k.State, l.ExpectedCards[i])
			}
			if err := k.Move(Completed); err == nil || k.State != Planned {
				t.Fatalf("%s card %d: Planned → Completed accepted", l.SalesLine, i)
			}
			for _, to := range []State{Prepared, InProgress, Completed} {
				if err := k.Move(to); err != nil {
					t.Fatalf("%s card %d: %v", l.SalesLine, i, err)
				}
			}
			line = line.Add(k.Qty)
		}
		if !line.Equal(decimal.RequireFromString(l.ExpectedReceived)) {
			t.Fatalf("%s: received %s, want %s", l.SalesLine, line, l.ExpectedReceived)
		}
		received[l.Rule] = received[l.Rule].Add(line)
		cards += len(got)
	}
	if cards != sim.ExpectedCardCount {
		t.Fatalf("%d cards, want %d", cards, sim.ExpectedCardCount)
	}
	for code, want := range sim.ExpectedReceivedByRule {
		if !received[code].Equal(decimal.RequireFromString(want)) {
			t.Fatalf("rule %s received %s, want %s", code, received[code], want)
		}
	}
}
