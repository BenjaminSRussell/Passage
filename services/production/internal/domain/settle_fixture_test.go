package domain

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/shopspring/decimal"
)

type fixtureRow struct {
	Group  CostGroup `json:"group"`
	Key    string    `json:"key"`
	Qty    string    `json:"qty"`
	Amount string    `json:"amount"`
	Fixed  string    `json:"fixed"`
}

type fixtureOrder struct {
	Name     string       `json:"name"`
	OrderQty string       `json:"orderQty"`
	RAFQty   string       `json:"rafQty"`
	Planned  []fixtureRow `json:"planned"`
	Actual   []fixtureRow `json:"actual"`
	Expected struct {
		WIPExact       string            `json:"wipExact"`
		VariancesExact map[string]string `json:"variancesExact"`
	} `json:"expected"`
}

func rows(in []fixtureRow) []CostRow {
	out := make([]CostRow, len(in))
	for i, r := range in {
		out[i] = CostRow{Group: r.Group, Key: r.Key, Qty: d(r.Qty), Amount: d(r.Amount), Fixed: d(r.Fixed)}
	}
	return out
}

// AC2 (PRD 4.21.9 prod-orders-50): 50 orders with known consumptions, full,
// partial and zero output, setup, unplanned and unconsumed items. The
// expectations come from testdata/gen_prod_orders_50.py, an independent
// exact-fraction implementation of BR-21.6. Each settlement must clear the
// order's WIP to the cent, and each variance row must match the exact value
// within the cent rounding the posting applies.
func TestSettle_ProdOrders50Fixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/prod-orders-50.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		Orders []fixtureOrder `json:"orders"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	if len(fx.Orders) != 50 {
		t.Fatalf("want 50 fixture orders, got %d", len(fx.Orders))
	}
	for _, o := range fx.Orders {
		t.Run(o.Name, func(t *testing.T) {
			s, err := Settle(d(o.OrderQty), d(o.RAFQty), rows(o.Planned), rows(o.Actual))
			if err != nil {
				t.Fatal(err)
			}
			wip := d(o.Expected.WIPExact).Round(2)
			if !s.WIPBefore.Equal(wip) {
				t.Fatalf("WIP before End: want %s, got %s", wip, s.WIPBefore)
			}
			sum := decimal.Zero
			got := map[string]decimal.Decimal{}
			for _, v := range s.Variances {
				sum = sum.Add(v.Amount)
				got[fmt.Sprintf("%s/%s", v.Type, v.Group)] = v.Amount
			}
			if !sum.Equal(s.WIPBefore) {
				t.Fatalf("variances %s do not clear WIP %s", sum, s.WIPBefore)
			}
			// Rounding each row to cents and placing the residual can move one row by
			// at most half a cent per row; anything beyond that is a wrong decomposition.
			slack := decimal.New(int64(len(o.Expected.VariancesExact)+1), -2).Div(decimal.NewFromInt(2))
			for key, exact := range o.Expected.VariancesExact {
				if diff := got[key].Sub(d(exact)).Abs(); diff.GreaterThan(slack) {
					t.Errorf("%s: want %s, got %s", key, exact, got[key])
				}
				delete(got, key)
			}
			for key, v := range got {
				if v.Abs().GreaterThan(slack) {
					t.Errorf("unexpected variance %s = %s", key, v)
				}
			}
		})
	}
}
