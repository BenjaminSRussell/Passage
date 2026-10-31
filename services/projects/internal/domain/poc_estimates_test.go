package domain

import (
	"encoding/csv"
	"os"
	"testing"

	"github.com/shopspring/decimal"
)

// TestComputePOC_Estimates12x4MatchReference is Wave 41 AC1 (PRD fixture
// proj-estimates-12x4): 12 fixed-price projects x 4 period-end estimates in
// USD, EUR, GBP, JPY, KWD and BHD, replayed through ComputePOC and
// ProvisionMovement. The expected values come from the independent exact-
// rational reference testdata/gen_poc_estimates.py (python3 -I), which first
// reproduces the PRD worked example and 33 hand-derived literals.
//
// Each period runs on the engine's own previous output (prior revenue to date
// and prior provision), so an error carries into the period revenue and the
// provision movement of the next period; the fixture's prior columns are
// checked against that chain too.
func TestComputePOC_Estimates12x4MatchReference(t *testing.T) {
	f, err := os.Open("../../testdata/proj-estimates-12x4.csv")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	col := map[string]int{}
	for i, h := range rows[0] {
		col[h] = i
	}
	get := func(row []string, name string) decimal.Decimal {
		i, ok := col[name]
		if !ok {
			t.Fatalf("fixture has no column %s", name)
		}
		return d(row[i])
	}
	projects := map[string]bool{}
	var priorRevenue, priorProvision decimal.Decimal
	for _, row := range rows[1:] {
		project, period := row[col["project"]], row[col["period"]]
		projects[project] = true
		if period == "1" {
			priorRevenue, priorProvision = decimal.Zero, decimal.Zero
		}
		if !get(row, "prior_revenue_to_date").Equal(priorRevenue) || !get(row, "prior_loss_provision").Equal(priorProvision) {
			t.Fatalf("%s p%s: fixture prior columns %s / %s, engine chain %s / %s", project, period,
				row[col["prior_revenue_to_date"]], row[col["prior_loss_provision"]], priorRevenue, priorProvision)
		}
		got, err := ComputePOC(POCInput{
			Currency:            row[col["currency"]],
			ContractValue:       get(row, "contract_value"),
			CostToDate:          get(row, "cost_to_date"),
			EstimatedToComplete: get(row, "etc"),
			BilledToDate:        get(row, "billed_to_date"),
			PriorRevenueToDate:  priorRevenue,
		})
		if err != nil {
			t.Fatalf("%s p%s: %v", project, period, err)
		}
		checks := []struct {
			name string
			got  decimal.Decimal
		}{
			{"eac", got.EAC},
			{"poc", got.POC},
			{"revenue_to_date", got.RevenueToDate},
			{"revenue_this_period", got.RevenueThisPeriod},
			{"accrued_revenue", got.AccruedRevenue},
			{"deferred_revenue", got.DeferredRevenue},
			{"gross_profit_to_date", got.GrossProfitToDate},
			{"loss_provision", got.LossProvision},
			{"provision_movement", ProvisionMovement(priorProvision, got.LossProvision)},
		}
		for _, c := range checks {
			if want := get(row, "expect_"+c.name); !c.got.Equal(want) {
				t.Errorf("%s p%s %s = %s, want %s", project, period, c.name, c.got, want)
			}
		}
		// The two balance-sheet positions are exclusive and net to revenue - billed.
		if !got.AccruedRevenue.Sub(got.DeferredRevenue).Equal(got.RevenueToDate.Sub(get(row, "billed_to_date"))) {
			t.Errorf("%s p%s: accrued - deferred != revenue - billed", project, period)
		}
		priorRevenue, priorProvision = got.RevenueToDate, got.LossProvision
	}
	if n := len(rows) - 1; len(projects) != 12 || n != 48 {
		t.Fatalf("fixture has %d projects and %d estimates, want 12 x 4", len(projects), n)
	}
}
