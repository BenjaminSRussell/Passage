package domain

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/shopspring/decimal"
)

// invoicesFixture is testdata/invoices-random-1k.json (§5.29.9). Its expected
// amounts come from testdata/gen_invoices_random_1k.py, an exact rational
// reference that shares no code with Compute.
type invoicesFixture struct {
	Seed     int `json:"seed"`
	Invoices []struct {
		ID       string `json:"id"`
		Currency string `json:"currency"`
		Charges  string `json:"charges"`
		Lines    []struct {
			Qty         string `json:"qty"`
			UnitPrice   string `json:"unitPrice"`
			DiscountPct string `json:"discountPct"`
			Tax         string `json:"tax"`
		} `json:"lines"`
		Expected struct {
			LineNets []string `json:"lineNets"`
			Net      string   `json:"net"`
			Charges  string   `json:"charges"`
			Tax      string   `json:"tax"`
			Gross    string   `json:"gross"`
		} `json:"expected"`
	} `json:"invoices"`
}

// TestComputeTotals_InvoicesRandom1k is AC-29.1 on the fixed 1,000-invoice
// fixture: every line net and the net, charges, tax and gross of each invoice
// match the independent reference to the minor unit, in nine currencies with
// 0, 2, 3 and 4 decimals, including values built to fall on a half minor unit.
func TestComputeTotals_InvoicesRandom1k(t *testing.T) {
	raw, err := os.ReadFile("testdata/invoices-random-1k.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx invoicesFixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	if len(fx.Invoices) != 1000 {
		t.Fatalf("fixture has %d invoices, want 1000", len(fx.Invoices))
	}
	currencies := map[string]bool{}
	for _, inv := range fx.Invoices {
		currencies[inv.Currency] = true
		lines := make([]Line, len(inv.Lines))
		tax := make([]decimal.Decimal, len(inv.Lines))
		for i, l := range inv.Lines {
			lines[i] = Line{No: i + 1, Description: "x", Qty: d(l.Qty), UnitPrice: d(l.UnitPrice), DiscountPct: d(l.DiscountPct), RevenueAccount: "4000", TaxCode: "T"}
			if err := lines[i].Validate(); err != nil {
				t.Fatalf("%s line %d is not a valid line: %v", inv.ID, i+1, err)
			}
			tax[i] = d(l.Tax)
		}
		nets, got, err := Compute(inv.Currency, lines, tax, d(inv.Charges))
		if err != nil {
			t.Fatalf("%s: %v", inv.ID, err)
		}
		for i, want := range inv.Expected.LineNets {
			if !nets[i].Equal(d(want)) {
				t.Errorf("%s %s line %d net %s, want %s", inv.ID, inv.Currency, i+1, nets[i], want)
			}
		}
		checks := []struct {
			name      string
			got, want decimal.Decimal
		}{
			{"net", got.Net, d(inv.Expected.Net)},
			{"charges", got.Charges, d(inv.Expected.Charges)},
			{"tax", got.Tax, d(inv.Expected.Tax)},
			{"gross", got.Gross, d(inv.Expected.Gross)},
		}
		for _, c := range checks {
			if !c.got.Equal(c.want) {
				t.Errorf("%s %s %s %s, want %s", inv.ID, inv.Currency, c.name, c.got, c.want)
			}
		}
	}
	if len(currencies) < 9 {
		t.Errorf("fixture covers %d currencies, want 9", len(currencies))
	}
}
