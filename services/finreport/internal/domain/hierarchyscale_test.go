package domain

import (
	"testing"
	"time"
)

// TestEvaluate_HierarchyRowsScale keeps the FR-15.6 budget for the rows a
// hierarchy run writes: 2,000 account-list rows, and 2,000 dimension-value rows,
// each over about 200k balances and 12 columns.
func TestEvaluate_HierarchyRowsScale(t *testing.T) {
	if testing.Short() {
		t.Skip("scale test skipped in -short mode")
	}
	for _, shape := range []string{"accounts", "dimension values"} {
		in := scaleInput(2000, 12, 8)
		for i := range in.Balances {
			in.Balances[i].Dims = map[string]string{"cc": "V" + in.Balances[i].Account}
		}
		for a := range in.Definition.Rows {
			r := &in.Definition.Rows[a]
			acct := r.AccountFrom
			r.AccountFrom, r.AccountTo = "", ""
			if shape == "accounts" {
				r.Accounts = []string{acct}
			} else {
				r.Kind, r.DimensionFilter = RowDimensionFilter, map[string][]string{"cc": {"V" + acct}}
			}
		}
		start := time.Now()
		res, err := Evaluate(in)
		elapsed := time.Since(start)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: %s", shape, elapsed)
		if elapsed > 20*time.Second {
			t.Fatalf("%s: statement took %s, budget is 20s", shape, elapsed)
		}
		for _, c := range res.Cells {
			if err := c.CheckDrill(); err != nil || len(c.Keys) == 0 {
				t.Fatalf("%s: cell %s/%s: keys %d, %v", shape, c.Row, c.Column, len(c.Keys), err)
			}
		}
	}
}
