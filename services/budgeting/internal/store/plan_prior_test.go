package store

import (
	"context"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
)

func TestPlan_PriorPlanCellsComeFromTheApprovedScenarioOfThePreviousYear(t *testing.T) {
	f := newPlanFixture(t)
	base := f.scenario(t, "Baseline")
	f.write(t, base, cell("6100", "CC1", "2027-01", "1000"), cell("6200", "CC1", "2027-02", "50"))
	next := decodePlanView(t, mustCommand(t)(f.st.CreatePlan(asUser("planner"), ids.New(), "h", NewPlan{LegalEntityID: f.rule.LegalEntityID, RuleVersionID: f.rule.ID, Name: "FY28 operating", FiscalYear: 2028})))

	// Nothing is approved yet, so there is nothing to copy.
	if got, err := f.st.PriorPlanCells(asUser("planner"), next.ID, nil); err != nil || len(got) != 0 {
		t.Fatalf("before approval: %+v, %v", got, err)
	}
	if _, err := f.st.pool.Exec(context.Background(), `update budget_plan_process set status = 'Approved', approved_scenario_id = $2 where id = $1`, f.view.ID, base.ID); err != nil {
		t.Fatal(err)
	}
	got, err := f.st.PriorPlanCells(asUser("planner"), next.ID, nil)
	if err != nil || len(got) != 2 || got[0].Account != "6100" || got[0].Period != "2027-01" || !got[0].Amount.Equal(d("1000")) {
		t.Fatalf("approved prior plan: %+v, %v", got, err)
	}
	if got, err := f.st.PriorPlanCells(asUser("planner"), next.ID, []string{"6200"}); err != nil || len(got) != 1 || got[0].Account != "6200" {
		t.Fatalf("account filter: %+v, %v", got, err)
	}
	// The plan of the same year has no previous year, and another tenant sees no plan.
	if got, err := f.st.PriorPlanCells(asUser("planner"), f.view.ID, nil); err != nil || len(got) != 0 {
		t.Fatalf("first year: %+v, %v", got, err)
	}
	if _, err := f.st.PriorPlanCells(tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(planTenant + 1), UserID: "x", Duties: map[string]struct{}{}}), next.ID, nil); !hasCode(err, CodePlanNotFound) {
		t.Fatalf("other tenant: %v, want %s", err, CodePlanNotFound)
	}
}
