package store_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/lean/internal/domain"
	leanevents "github.com/BenjaminSRussell/passage/services/lean/internal/events"
	"github.com/BenjaminSRussell/passage/services/lean/internal/store"
)

// Wave 24 proof "Kanban simulation: event kanbans are generated from a sales
// order and complete through the board", in the database. The sales order of
// testdata/lean-kanban-sim.json (expected cards from the independent
// gen_lean_kanban_sim.py) is fed line by line into three event rules that share
// a Mix → Fill flow. Every job is then taken off the work-cell boards, Mix
// first, started and completed with its card's quantity. Every card must end
// Completed with the reference quantities, the completion events must carry the
// reference totals per rule, and emptying the spent cards clears the boards.
func TestEventKanbanSimulation_SalesOrderThroughTheBoards(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/lean-kanban-sim.json")
	if err != nil {
		t.Fatal(err)
	}
	var sim struct {
		Rules []struct {
			Code     string `json:"code"`
			FixedQty string `json:"fixedQty"`
			ExactQty bool   `json:"exactQty"`
		} `json:"rules"`
		Lines []struct {
			SalesLine        string   `json:"salesLine"`
			Rule             string   `json:"rule"`
			DemandQty        string   `json:"demandQty"`
			ExpectedCards    []string `json:"expectedCards"`
			ExpectedReceived string   `json:"expectedReceived"`
		} `json:"lines"`
		ExpectedCardCount      int               `json:"expectedCardCount"`
		ExpectedReceivedByRule map[string]string `json:"expectedReceivedByRule"`
	}
	if err := json.Unmarshal(raw, &sim); err != nil {
		t.Fatal(err)
	}
	s, admin := setup(t)
	ctx := ctxFor(1)
	mixCell, fillCell := ids.New(), ids.New()
	f := cellFlow(t, s, ctx, mixCell, fillCell, false)
	ruleIDs, ruleCodes := map[string]string{}, map[string]string{}
	for _, r := range sim.Rules {
		in := eventRule(r.Code)
		in.FixedQty, in.ExactQty, in.FlowID = dec(r.FixedQty), r.ExactQty, &f.ID
		created, _, err := s.CreateRule(ctx, in, store.Idem{})
		if err != nil {
			t.Fatalf("rule %s: %v", r.Code, err)
		}
		ruleIDs[r.Code], ruleCodes[created.ID] = created.ID, r.Code
	}

	cardLine := map[string]string{} // kanban id → sales line
	for _, l := range sim.Lines {
		cards, _, err := s.GenerateKanbans(ctx, store.Generate{RuleID: ruleIDs[l.Rule], DemandQty: dec(l.DemandQty),
			EventRefType: ptr("sales-line"), EventRefID: ptr(l.SalesLine)}, store.Idem{Key: "gen-" + l.SalesLine, Fingerprint: "fp"})
		if err != nil || len(cards) != len(l.ExpectedCards) {
			t.Fatalf("%s: %d cards, want %d (%v)", l.SalesLine, len(cards), len(l.ExpectedCards), err)
		}
		for i, c := range cards {
			if !c.Qty.Equal(dec(l.ExpectedCards[i])) || c.State != domain.Planned || c.JobCount != 2 || *c.EventRefID != l.SalesLine {
				t.Fatalf("%s card %d: %+v, want qty %s planned with 2 jobs", l.SalesLine, i, c, l.ExpectedCards[i])
			}
			cardLine[c.ID] = l.SalesLine
		}
	}
	if len(cardLine) != sim.ExpectedCardCount {
		t.Fatalf("%d cards, want %d", len(cardLine), sim.ExpectedCardCount)
	}

	// Work the boards: everything on the Mix cell, then everything on the Fill cell.
	for _, cell := range []string{mixCell, fillCell} {
		board, err := s.BoardJobs(ctx, store.BoardFilter{WorkCellID: cell})
		if err != nil || len(board) != sim.ExpectedCardCount {
			t.Fatalf("cell board has %d jobs, want %d (%v)", len(board), sim.ExpectedCardCount, err)
		}
		for _, j := range board {
			started, _, err := s.StartJob(ctx, j.ID, j.Version, store.Idem{})
			if err != nil {
				t.Fatalf("start %s %s: %v", j.CardNumber, j.ActivityName, err)
			}
			if _, _, err := s.CompleteJob(ctx, started.ID, started.Version, done(j.CardQty.String(), "0", 1), store.Idem{}); err != nil {
				t.Fatalf("complete %s %s: %v", j.CardNumber, j.ActivityName, err)
			}
		}
	}

	byLine := map[string]decimal.Decimal{}
	for id, line := range cardLine {
		k, err := s.GetKanban(ctx, id)
		if err != nil || k.State != domain.Completed {
			t.Fatalf("card %s of %s ended %s (%v)", k.CardNumber, line, k.State, err)
		}
		byLine[line] = byLine[line].Add(k.Qty)
	}
	for _, l := range sim.Lines {
		if !byLine[l.SalesLine].Equal(dec(l.ExpectedReceived)) {
			t.Errorf("%s completed %s, want %s", l.SalesLine, byLine[l.SalesLine], l.ExpectedReceived)
		}
	}
	byRule, completions := map[string]decimal.Decimal{}, 0
	for _, e := range outboxEvents(t, admin, leanevents.KanbanStateChanged) {
		var d leanevents.KanbanStateChangedData
		if err := json.Unmarshal(e.Data, &d); err != nil {
			t.Fatal(err)
		}
		if d.To == string(domain.Completed) {
			completions++
			byRule[ruleCodes[d.RuleID]] = byRule[ruleCodes[d.RuleID]].Add(dec(d.Qty))
		}
	}
	if completions != sim.ExpectedCardCount {
		t.Fatalf("%d completion events, want %d", completions, sim.ExpectedCardCount)
	}
	for code, want := range sim.ExpectedReceivedByRule {
		if !byRule[code].Equal(dec(want)) {
			t.Errorf("completion events of %s carry %s, want %s", code, byRule[code], want)
		}
	}

	// Spent event cards leave both boards once emptied.
	for id := range cardLine {
		k, _ := s.GetKanban(ctx, id)
		if _, _, err := s.MoveKanban(ctx, id, domain.Emptied, k.Version, store.Idem{}); err != nil {
			t.Fatalf("empty %s: %v", k.CardNumber, err)
		}
	}
	for _, cell := range []string{mixCell, fillCell} {
		if board, err := s.BoardJobs(ctx, store.BoardFilter{WorkCellID: cell}); err != nil || len(board) != 0 {
			t.Fatalf("cell board after emptying has %d jobs (%v)", len(board), err)
		}
	}
	for code, id := range ruleIDs {
		if board, err := s.Board(ctx, store.BoardFilter{RuleID: id}); err != nil || len(board) != 0 {
			t.Fatalf("rule %s board after emptying has %d cards (%v)", code, len(board), err)
		}
	}
}
