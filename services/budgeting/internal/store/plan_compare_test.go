package store

import (
	"encoding/json"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
)

func TestPlan_CompareScenariosGroupsAndShowsTheChange(t *testing.T) {
	f := newPlanFixture(t)
	base := f.scenario(t, "Baseline")
	f.write(t, base, cell("6000", "CC1", "2027-01", "1000"), cell("6000", "CC2", "2027-01", "500"), cell("6100", "CC1", "2027-02", "200"))
	var opt Scenario
	if err := json.Unmarshal(mustCommand(t)(f.st.CreateScenario(asUser("planner"), ids.New(), "h", f.view.ID,
		NewScenario{Name: "Optimistic", Kind: "Optimistic", CopyFromID: base.ID, AdjustmentPct: d("10")})), &opt); err != nil {
		t.Fatal(err)
	}
	cmp, err := f.st.CompareScenarios(asUser("planner"), f.view.ID, base.ID, opt.ID, "account")
	if err != nil {
		t.Fatal(err)
	}
	if len(cmp.Rows) != 2 || cmp.Rows[0].Key != "6000" || !cmp.Rows[0].Base.Equal(d("1500")) || !cmp.Rows[0].Other.Equal(d("1650")) || cmp.Rows[0].DeltaPct != "10.00" {
		t.Fatalf("by account: %+v, want 6000 1500 -> 1650 (+10.00%%)", cmp.Rows)
	}
	if !cmp.Delta.Equal(d("170")) {
		t.Fatalf("total change %s, want 170", cmp.Delta)
	}
	// A key only the other scenario holds shows zero for the base and no percentage.
	f.write(t, opt, cell("6200", "CC9", "2027-03", "50"))
	cmp, err = f.st.CompareScenarios(asUser("planner"), f.view.ID, base.ID, opt.ID, "costCenter")
	if err != nil {
		t.Fatal(err)
	}
	last := cmp.Rows[len(cmp.Rows)-1]
	if last.Key != "CC9" || !last.Base.IsZero() || last.DeltaPct != "" {
		t.Fatalf("new cost center row %+v, want CC9 with zero base and no percentage", last)
	}
	if _, err := f.st.CompareScenarios(asUser("planner"), f.view.ID, base.ID, ids.New(), "period"); !hasCode(err, CodeScenarioNotFound) {
		t.Fatalf("foreign scenario: %v, want %s", err, CodeScenarioNotFound)
	}
}
