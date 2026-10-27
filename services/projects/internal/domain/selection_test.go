package domain

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"pgregory.net/rapid"
)

var selBase = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

func genCandidate(i int) *rapid.Generator[Candidate] {
	return rapid.Custom(func(t *rapid.T) Candidate {
		return Candidate{
			ID:           fmt.Sprintf("t%04d", i),
			ProjectID:    rapid.SampledFrom([]string{"P1", "P2", "P3"}).Draw(t, "project"),
			TxnType:      rapid.SampledFrom([]string{"HOUR", "COST", "ITEM", "FEE", "ON_ACCOUNT"}).Draw(t, "type"),
			LineProperty: rapid.SampledFrom([]string{"CHARGEABLE", "NON_CHARGEABLE"}).Draw(t, "property"),
			ProjectStage: rapid.SampledFrom([]Stage{StageCreated, StageInProcess, StageOnHold, StageFinished}).Draw(t, "stage"),
			TxnDate:      selBase.AddDate(0, 0, rapid.IntRange(-10, 40).Draw(t, "day")),
			Proposed:     rapid.Bool().Draw(t, "proposed"),
		}
	})
}

// referenceSelected is the BR-41.3 predicate written as one boolean formula,
// independent of Explain's switch, so the property compares two implementations.
func referenceSelected(c Candidate, crit Criteria) bool {
	inWindow := !c.TxnDate.Before(crit.From) && !c.TxnDate.After(crit.To)
	stageOK := c.ProjectStage == StageInProcess || c.ProjectStage == StageFinished
	typeOK := len(crit.TxnTypes) == 0 || slices.Contains(crit.TxnTypes, c.TxnType)
	projectOK := len(crit.ProjectIDs) == 0 || slices.Contains(crit.ProjectIDs, c.ProjectID)
	return c.LineProperty == "CHARGEABLE" && !c.Proposed && inWindow && stageOK && typeOK && projectOK
}

// TestSelect_NoFalsePositivesOrNegatives is AC-41.3: over generated transactions,
// every selected line satisfies the predicate and no qualifying line is omitted.
func TestSelect_NoFalsePositivesOrNegatives(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(0, 200).Draw(t, "n")
		cands := make([]Candidate, n)
		for i := range cands {
			cands[i] = genCandidate(i).Draw(t, "candidate")
		}
		crit := Criteria{
			From: selBase,
			To:   selBase.AddDate(0, 0, rapid.IntRange(0, 30).Draw(t, "span")),
		}
		if rapid.Bool().Draw(t, "filterTypes") {
			crit.TxnTypes = []string{"HOUR", "COST"}
		}
		if rapid.Bool().Draw(t, "filterProjects") {
			crit.ProjectIDs = []string{"P2"}
		}

		selected, excluded := Select(crit, cands)
		got := map[string]bool{}
		for _, id := range selected {
			got[id] = true
		}
		for _, c := range cands {
			want := referenceSelected(c, crit)
			if got[c.ID] != want {
				t.Fatalf("candidate %s selected=%v, predicate=%v (%+v, %+v)", c.ID, got[c.ID], want, c, crit)
			}
			if _, isExcluded := excluded[c.ID]; isExcluded == want {
				t.Fatalf("candidate %s lands in both or neither list", c.ID)
			}
		}
	})
}

func TestExplain_ReportsFirstFailingRule(t *testing.T) {
	ok := Candidate{ID: "x", ProjectID: "P1", TxnType: "HOUR", LineProperty: "CHARGEABLE", ProjectStage: StageInProcess, TxnDate: selBase}
	crit := Criteria{From: selBase, To: selBase.AddDate(0, 0, 30)}
	cases := []struct {
		name   string
		mutate func(c *Candidate)
		want   string
	}{
		{"selected", func(*Candidate) {}, ""},
		{"not chargeable", func(c *Candidate) { c.LineProperty = "NON_CHARGEABLE" }, ReasonNotChargeable},
		{"already proposed", func(c *Candidate) { c.Proposed = true }, ReasonProposed},
		{"before window", func(c *Candidate) { c.TxnDate = selBase.AddDate(0, 0, -1) }, ReasonOutOfRange},
		{"after window", func(c *Candidate) { c.TxnDate = selBase.AddDate(0, 0, 31) }, ReasonOutOfRange},
		{"stage on hold", func(c *Candidate) { c.ProjectStage = StageOnHold }, ReasonStage},
		{"finished is billable", func(c *Candidate) { c.ProjectStage = StageFinished }, ""},
	}
	for _, tc := range cases {
		c := ok
		tc.mutate(&c)
		if got := Explain(c, crit); got != tc.want {
			t.Errorf("%s: Explain = %q, want %q", tc.name, got, tc.want)
		}
	}
}
