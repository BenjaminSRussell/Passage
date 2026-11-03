package domain

import (
	"encoding/csv"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func parseCSV(t *testing.T, b []byte) [][]string {
	t.Helper()
	rows, err := csv.NewReader(strings.NewReader(string(b))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestCSVText_NeutralisesFormulas(t *testing.T) {
	for in, want := range map[string]string{"=1+1": "'=1+1", "+x": "'+x", "-x": "'-x", "@SUM": "'@SUM", "\tx": "'\tx", "\rx": "'\rx", "plain": "plain", "": "", "a=b": "a=b"} {
		if got := CSVText(in); got != want {
			t.Errorf("CSVText(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRenderRunbookCSV_StepsWithTimings(t *testing.T) {
	t0 := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	started := t0.Add(10 * time.Minute)
	p := Plan{T0: t0, Steps: []Step{
		{Seq: 1, OffsetMin: -60, Name: "Freeze legacy", Owner: "Legacy lead", PlannedDurationMin: 30, Status: StepDone, ActualDurationMin: 45, CompletedBy: "ann", CompletedAt: &started, StartedAt: &started, StartedBy: "ann"},
		{Seq: 2, OffsetMin: 0, Name: "=Load packs", Owner: "Engineer", PlannedDurationMin: 60, Status: StepPending, GoNoGo: true, Pack: "open_items", StartedAt: &started, StartedBy: "bob"},
		{Seq: 3, OffsetMin: 60, Name: "Open access", Owner: "Manager", PlannedDurationMin: 15, Status: StepPending},
	}}
	b, err := RenderRunbookCSV(p, t0.Add(80*time.Minute), 110, 125)
	if err != nil {
		t.Fatal(err)
	}
	rows := parseCSV(t, b)
	if len(rows) != 4 || strings.Join(rows[0], ",") != strings.Join(RunbookHeader, ",") {
		t.Fatalf("rows %v", rows)
	}
	done, running, idle := rows[1], rows[2], rows[3]
	if done[1] != "2026-12-31T23:00:00Z" || done[13] != "45" || done[14] != "45" || done[15] != ToneRed {
		t.Fatalf("done step %v", done)
	}
	if running[3] != "'=Load packs" || running[6] != "true" || running[7] != "open_items" || running[14] != "70" || running[15] != ToneAmber {
		t.Fatalf("running step %v", running)
	}
	if idle[9] != "" || idle[13] != "" || idle[14] != "" || idle[15] != "" {
		t.Fatalf("a step not started has no timing: %v", idle)
	}
}

func TestRenderRollbackAndDecisionsCSV(t *testing.T) {
	t0 := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	p := Plan{T0: t0, AbortOffsetMin: 36 * 60}
	rows := parseCSV(t, mustCSV(RenderRollbackPlanCSV(p)))
	if len(rows) != 7 || rows[1][3] != RollbackDecision || rows[1][6] != "true" || rows[1][1] != "2027-01-02T12:00:00Z" {
		t.Fatalf("rollback rows %v", rows)
	}
	d := Decision{Seq: 1, Decision: DecisionGo, DecidedBy: "bob", DecidedAt: t0, Approvers: []string{"alice", "bob"}, Reason: "-cmd", PlanVersion: 3,
		Checklist: []ItemResult{{Code: "a", Status: ItemMet}, {Code: "b", Status: ItemOpen}},
		Waivers:   []Waiver{{Pack: "onhand", Reason: " posted by hand "}}}
	rows = parseCSV(t, mustCSV(RenderDecisionsCSV([]Decision{d})))
	if len(rows) != 2 || rows[1][4] != "alice bob" || rows[1][5] != "'-cmd" || rows[1][7] != "b" || rows[1][8] != "onhand: posted by hand" {
		t.Fatalf("decision rows %v", rows)
	}
}

func TestRenderReconciliationCSV_TotalsAndMissingRuns(t *testing.T) {
	tot, err := NewControlTotal("open_items.amount", map[string]string{"currency": "USD", "company": "USMF"}, decimal.RequireFromString("10"), decimal.RequireFromString("9.5"), Tolerance{})
	if err != nil {
		t.Fatal(err)
	}
	run := &PackRun{RunID: "r1", Status: StatusLoaded, CreatedAt: time.Date(2027, 1, 1, 1, 0, 0, 0, time.UTC), Totals: []ControlTotal{tot}}
	rows := parseCSV(t, mustCSV(RenderReconciliationCSV([]ReconciliationRun{{Pack: PackOpenItems, Company: "USMF", Run: run}, {Pack: PackOnhand, Company: "USMF"}})))
	if len(rows) != 3 {
		t.Fatalf("rows %v", rows)
	}
	if r := rows[1]; r[2] != "r1" || r[5] != "open_items.amount" || r[6] != "company=USMF;currency=USD" || r[7] != "10.000000" || r[8] != "9.500000" || r[9] != "-0.500000" || r[11] != "MISMATCH" {
		t.Fatalf("total row %v", r)
	}
	if r := rows[2]; r[0] != "onhand" || r[11] != "NO_RUN" {
		t.Fatalf("no run row %v", r)
	}
}

func mustCSV(b []byte, err error) []byte {
	if err != nil {
		panic(err)
	}
	return b
}
