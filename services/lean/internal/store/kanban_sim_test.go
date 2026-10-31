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

// simReport is the operator report the reference plans for a card's Mix and
// Fill jobs.
type simReport struct {
	MixReported  string `json:"mixReported"`
	MixScrap     string `json:"mixScrap"`
	FillReported string `json:"fillReported"`
	FillScrap    string `json:"fillScrap"`
}

// Wave 24 proof "Kanban simulation: event kanbans are generated from a sales
// order and complete through the board", in the database. The sales order of
// testdata/lean-kanban-sim.json (expected cards from the independent
// gen_lean_kanban_sim.py) is fed line by line into three event rules that share
// a Mix → Fill flow. The sales order is fixture data handed to GenerateKanbans
// as each line's demand and reference: no sales service is involved (the
// sales-line consumer is the wave-27 handoff in NOT-IMPLEMENTED.md). Every job
// is then taken off the work-cell boards, Mix first, started and completed with
// the operator report the reference plans for it (scrap at both activities, so
// good quantities differ from the card quantities). Every card must end
// Completed; the completion events must carry the Fill job's good and scrap,
// whose totals per sales line and per rule must match the reference; the
// engine's per-activity totals for the period (jobs and good + scrap, the
// backflush input) must match it too; and emptying the spent cards clears the
// boards.
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
			SalesLine        string      `json:"salesLine"`
			Rule             string      `json:"rule"`
			DemandQty        string      `json:"demandQty"`
			ExpectedCards    []string    `json:"expectedCards"`
			ExpectedReceived string      `json:"expectedReceived"`
			ExpectedReports  []simReport `json:"expectedReports"`
			ExpectedFinished string      `json:"expectedFinished"`
		} `json:"lines"`
		ExpectedCardCount      int               `json:"expectedCardCount"`
		ExpectedReceivedByRule map[string]string `json:"expectedReceivedByRule"`
		ExpectedFinishedByRule map[string]string `json:"expectedFinishedByRule"`
		ExpectedActivityWork   []struct {
			Name string `json:"name"`
			Jobs int    `json:"jobs"`
			Qty  string `json:"qty"`
		} `json:"expectedActivityWork"`
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

	cardLine := map[string]string{}      // kanban id → sales line
	cardReport := map[string]simReport{} // kanban id → planned operator reports
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
			cardLine[c.ID], cardReport[c.ID] = l.SalesLine, l.ExpectedReports[i]
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
			rep := cardReport[j.KanbanID]
			good, scrap := rep.MixReported, rep.MixScrap
			if cell == fillCell {
				good, scrap = rep.FillReported, rep.FillScrap
			}
			if _, _, err := s.CompleteJob(ctx, started.ID, started.Version, done(good, scrap, 1), store.Idem{}); err != nil {
				t.Fatalf("complete %s %s: %v", j.CardNumber, j.ActivityName, err)
			}
		}
	}

	for id, line := range cardLine {
		k, err := s.GetKanban(ctx, id)
		if err != nil || k.State != domain.Completed {
			t.Fatalf("card %s of %s ended %s (%v)", k.CardNumber, line, k.State, err)
		}
	}
	// What each card finished comes from its completion event, which the engine
	// fills from the job that completed the card (Fill), not from the input.
	cardQtyByRule, finishedByRule, finishedByLine, completions := map[string]decimal.Decimal{}, map[string]decimal.Decimal{}, map[string]decimal.Decimal{}, 0
	for _, e := range outboxEvents(t, admin, leanevents.KanbanStateChanged) {
		var d leanevents.KanbanStateChangedData
		if err := json.Unmarshal(e.Data, &d); err != nil {
			t.Fatal(err)
		}
		if d.To != string(domain.Completed) {
			continue
		}
		completions++
		line, ok := cardLine[d.KanbanID]
		if !ok || d.ReportedQty == nil || d.ScrapQty == nil {
			t.Fatalf("completion event %+v: unknown card or no report", d)
		}
		if want := cardReport[d.KanbanID]; !dec(*d.ScrapQty).Equal(dec(want.FillScrap)) {
			t.Errorf("card %s completion scrap %s, want the Fill job's %s", d.CardNumber, *d.ScrapQty, want.FillScrap)
		}
		code := ruleCodes[d.RuleID]
		cardQtyByRule[code] = cardQtyByRule[code].Add(dec(d.Qty))
		finishedByRule[code] = finishedByRule[code].Add(dec(*d.ReportedQty))
		finishedByLine[line] = finishedByLine[line].Add(dec(*d.ReportedQty))
	}
	if completions != sim.ExpectedCardCount {
		t.Fatalf("%d completion events, want %d", completions, sim.ExpectedCardCount)
	}
	for _, l := range sim.Lines {
		if !finishedByLine[l.SalesLine].Equal(dec(l.ExpectedFinished)) {
			t.Errorf("%s finished %s, want %s", l.SalesLine, finishedByLine[l.SalesLine], l.ExpectedFinished)
		}
	}
	for code, want := range sim.ExpectedReceivedByRule {
		if !cardQtyByRule[code].Equal(dec(want)) {
			t.Errorf("completion events of %s carry card quantities %s, want %s", code, cardQtyByRule[code], want)
		}
	}
	for code, want := range sim.ExpectedFinishedByRule {
		if !finishedByRule[code].Equal(dec(want)) {
			t.Errorf("completion events of %s finished %s, want %s", code, finishedByRule[code], want)
		}
	}
	// The engine's own per-activity totals since yesterday (the backflush input).
	p := openPeriod(t, s, ctx, f.ID, -1, 0)
	work, err := s.PeriodWork(ctx, f.ID, p.ID)
	if err != nil || len(work.Activities) != len(sim.ExpectedActivityWork) {
		t.Fatalf("period work %+v (%v)", work, err)
	}
	for i, want := range sim.ExpectedActivityWork {
		if got := work.Activities[i]; got.Name != want.Name || got.Jobs != want.Jobs || !got.Qty.Equal(dec(want.Qty)) {
			t.Errorf("activity %d: %s %d jobs, %s processed; want %s %d jobs, %s", i, got.Name, got.Jobs, got.Qty, want.Name, want.Jobs, want.Qty)
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
