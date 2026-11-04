package domain

import (
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func invoiceLine(net string) CommissionLine {
	return CommissionLine{InvoiceLineID: "INV-1/1", InvoiceDate: time.Date(2027, 3, 10, 0, 0, 0, 0, time.UTC), Currency: "USD", LineNet: dec(net)}
}

func creditLine(net string) CommissionLine {
	l := invoiceLine(net)
	l.InvoiceLineID, l.ReversesInvoiceLineID = "CN-1/1", "INV-1/1"
	return l
}

func amounts(splits []Split) map[string]string {
	out := map[string]string{}
	for _, s := range splits {
		out[s.RepID] = s.Amount.StringFixed(2)
	}
	return out
}

var sixtyForty = []Share{{RepID: "R1", SharePct: dec("60")}, {RepID: "R2", SharePct: dec("40")}}

// AC-26.12: reps R1 60% / R2 40%, a 5% revenue rule and a 10,000.00 USD invoice post
// 300.00 and 200.00; the credit note of that invoice posts -300.00 and -200.00.
func TestCommission_AC26_12(t *testing.T) {
	rule := CommissionRule{SalesGroupID: "SG-E", Basis: CommissionRevenue, RatePct: dec("5")}
	basis, splits, err := CalculateCommission(rule, sixtyForty, invoiceLine("10000.00"))
	if err != nil {
		t.Fatal(err)
	}
	if !basis.Equal(dec("10000")) || amounts(splits)["R1"] != "300.00" || amounts(splits)["R2"] != "200.00" {
		t.Fatalf("basis %s splits %v; want 10000 and 300.00 / 200.00", basis, amounts(splits))
	}
	orig := CommissionOriginal{Basis: rule.Basis, RatePct: rule.RatePct, LineNet: dec("10000"), Currency: "USD", Shares: sixtyForty, Credited: decimal.Zero}
	_, back, err := CreditCommission(orig, creditLine("-10000.00"))
	if err != nil {
		t.Fatal(err)
	}
	if amounts(back)["R1"] != "-300.00" || amounts(back)["R2"] != "-200.00" {
		t.Fatalf("credit splits %v; want -300.00 / -200.00", amounts(back))
	}
}

func TestCommission_RuleResolvesSpecificToGeneral(t *testing.T) {
	rules := []CommissionRule{
		{SalesGroupID: "SG-E", Basis: CommissionRevenue, RatePct: dec("2")},
		{SalesGroupID: "SG-E", ItemGroup: "IG-1", Basis: CommissionRevenue, RatePct: dec("3")},
		{SalesGroupID: "SG-E", CustomerGroup: "CG-1", Basis: CommissionMargin, RatePct: dec("4")},
		{SalesGroupID: "SG-E", CustomerGroup: "CG-1", ItemGroup: "IG-1", Basis: CommissionRevenue, RatePct: dec("5")},
		{SalesGroupID: "SG-W", Basis: CommissionRevenue, RatePct: dec("9")},
		{SalesGroupID: "SG-N", CustomerGroup: "CG-1", Basis: CommissionRevenue, RatePct: dec("7")},
	}
	cases := []struct {
		name, sg, cg, ig, want string
	}{
		{"(sales group, customer group, item group)", "SG-E", "CG-1", "IG-1", "5"},
		{"(sales group, customer group, all)", "SG-E", "CG-1", "IG-9", "4"},
		{"(sales group, all, item group)", "SG-E", "CG-9", "IG-1", "3"},
		{"(sales group, all, all)", "SG-E", "CG-9", "IG-9", "2"},
		{"a line without groups only matches all", "SG-E", "", "", "2"},
		{"another sales group's rules never apply", "SG-W", "CG-1", "IG-1", "9"},
		{"no level matches: PRC-0013", "SG-N", "CG-2", "IG-1", ""},
		{"unknown sales group: PRC-0013", "SG-X", "CG-1", "IG-1", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, ok := ResolveCommissionRule(rules, c.sg, c.cg, c.ig)
			if c.want == "" {
				if ok {
					t.Fatalf("want no rule, got %+v", r)
				}
				return
			}
			if !ok || r.RatePct.String() != c.want {
				t.Fatalf("rate = %s (ok %v), want %s", r.RatePct, ok, c.want)
			}
		})
	}
}

func TestCommission_MarginBasisNeedsTheCost(t *testing.T) {
	rule := CommissionRule{SalesGroupID: "SG-E", Basis: CommissionMargin, RatePct: dec("10")}
	l := invoiceLine("1000.00")
	if _, _, err := CalculateCommission(rule, sixtyForty, l); err == nil || err.Fields["costSnapshot"] == "" {
		t.Fatalf("a MARGIN rule without a cost must be refused, got %v", err)
	}
	l.CostSnapshot, l.HasCost = dec("640.00"), true
	basis, splits, err := CalculateCommission(rule, sixtyForty, l)
	if err != nil {
		t.Fatal(err)
	}
	if !basis.Equal(dec("360")) || amounts(splits)["R1"] != "21.60" || amounts(splits)["R2"] != "14.40" {
		t.Fatalf("basis %s splits %v; want margin 360 and 21.60 / 14.40", basis, amounts(splits))
	}
}

// BR-26.12: each rep rounds half-even and the residual cent goes to the first rep.
func TestCommission_ResidualCentGoesToTheFirstRep(t *testing.T) {
	thirds := []Share{{RepID: "R1", SharePct: dec("33.333333")}, {RepID: "R2", SharePct: dec("33.333333")}, {RepID: "R3", SharePct: dec("33.333334")}}
	rule := CommissionRule{Basis: CommissionRevenue, RatePct: dec("1")}
	_, splits, _ := CalculateCommission(rule, thirds, invoiceLine("1000.00")) // 10.00 to split
	got := amounts(splits)
	if got["R1"] != "3.34" || got["R2"] != "3.33" || got["R3"] != "3.33" {
		t.Fatalf("splits %v; want 3.34 / 3.33 / 3.33", got)
	}
}

func TestCommission_CreditsAreCappedAtTheInvoicedNet(t *testing.T) {
	orig := CommissionOriginal{Basis: CommissionRevenue, RatePct: dec("5"), LineNet: dec("1000"), Currency: "USD", Shares: sixtyForty, Credited: dec("-600")}
	if _, _, err := CreditCommission(orig, creditLine("-400")); err != nil {
		t.Fatalf("crediting the remaining 400 must pass: %v", err)
	}
	_, _, err := CreditCommission(orig, creditLine("-400.01"))
	if err == nil || err.Code != CodeCommissionCredit {
		t.Fatalf("crediting more than invoiced must be PRC-0037, got %v", err)
	}
	other := creditLine("-10")
	other.Currency = "EUR"
	if _, _, err := CreditCommission(orig, other); err == nil || err.Code != CodeCommissionCredit {
		t.Fatalf("a credit in another currency must be refused, got %v", err)
	}
}

// A credit uses the original rate and shares, so a full credit nets every rep to zero even
// after the rule or the shares changed.
func TestCommission_FullCreditAlwaysNetsToZero(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cents := rapid.Int64Range(0, 100_000_000).Draw(t, "net")
		ratePct := decimal.New(rapid.Int64Range(0, 100_000).Draw(t, "rate"), -3)
		first := rapid.Int64Range(1, 99).Draw(t, "share")
		shares := []Share{{RepID: "A", SharePct: decimal.NewFromInt(first)}, {RepID: "B", SharePct: decimal.NewFromInt(100 - first)}}
		net := decimal.New(cents, -2)
		rule := CommissionRule{Basis: CommissionRevenue, RatePct: ratePct}
		_, inv, err := CalculateCommission(rule, shares, invoiceLine(net.String()))
		if err != nil {
			t.Fatal(err)
		}
		orig := CommissionOriginal{Basis: rule.Basis, RatePct: ratePct, LineNet: net, Currency: "USD", Shares: shares, Credited: decimal.Zero}
		_, cr, cerr := CreditCommission(orig, creditLine(net.Neg().String()))
		if cerr != nil {
			t.Fatal(cerr)
		}
		total := decimal.Zero
		for i := range inv {
			if !inv[i].Amount.Add(cr[i].Amount).IsZero() {
				t.Fatalf("rep %s: invoice %s + credit %s != 0", inv[i].RepID, inv[i].Amount, cr[i].Amount)
			}
			total = total.Add(inv[i].Amount)
		}
		if !total.Equal(CommissionAmount(net, ratePct, "USD")) {
			t.Fatalf("splits sum to %s, want %s", total, CommissionAmount(net, ratePct, "USD"))
		}
	})
}

func TestValidateShares(t *testing.T) {
	cases := []struct {
		name   string
		shares []Share
		field  string
	}{
		{"sixty forty", sixtyForty, ""},
		{"empty", nil, "reps"},
		{"sum below 100", []Share{{"R1", dec("60")}}, "reps"},
		{"sum above 100", []Share{{"R1", dec("60")}, {"R2", dec("50")}}, "reps"},
		{"zero share", []Share{{"R1", dec("0")}, {"R2", dec("100")}}, "reps[0].sharePct"},
		{"duplicate rep", []Share{{"R1", dec("50")}, {"R1", dec("50")}}, "reps[1].repId"},
		{"blank rep", []Share{{" ", dec("100")}}, "reps[0].repId"},
		// The rep id is a statement path segment: / and : stop the gateway glob, . and .. are normalised away.
		{"rep id with a slash", []Share{{"R1", dec("50")}, {"EU/R2", dec("50")}}, "reps[1].repId"},
		{"rep id with a colon", []Share{{"worker:7", dec("100")}}, "reps[0].repId"},
		{"rep id dot dot", []Share{{"..", dec("100")}}, "reps[0].repId"},
		{"rep id of 65 characters", []Share{{strings.Repeat("r", 65), dec("100")}}, "reps[0].repId"},
		{"rep id of 64 characters with dots and dashes", []Share{{"w-1." + strings.Repeat("r", 60), dec("100")}}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ValidateShares(c.shares)
			if c.field == "" {
				if got != nil {
					t.Fatalf("want valid, got %v", got)
				}
				return
			}
			if _, ok := got[c.field]; !ok {
				t.Fatalf("want an error on %q, got %v", c.field, got)
			}
		})
	}
}

func TestValidateCommissionLine(t *testing.T) {
	noDate := invoiceLine("1")
	noDate.InvoiceDate = time.Time{}
	self := creditLine("-1")
	self.ReversesInvoiceLineID = self.InvoiceLineID
	badCost := invoiceLine("100")
	badCost.CostSnapshot, badCost.HasCost = dec("-1"), true
	cases := []struct {
		name  string
		in    CommissionLine
		field string
	}{
		{"invoice", invoiceLine("100"), ""},
		{"zero-net invoice", invoiceLine("0"), ""},
		{"credit", creditLine("-100"), ""},
		{"missing date", noDate, "invoiceDate"},
		{"credit must be negative", creditLine("5"), "lineNet"},
		{"negative net needs the reversed line", invoiceLine("-5"), "lineNet"},
		{"credit cannot reverse itself", self, "reversesInvoiceLineId"},
		{"cost sign follows the net", badCost, "costSnapshot"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ValidateCommissionLine(c.in)
			if c.field == "" {
				if got != nil {
					t.Fatalf("want valid, got %v", got)
				}
				return
			}
			if _, ok := got[c.field]; !ok {
				t.Fatalf("want an error on %q, got %v", c.field, got)
			}
		})
	}
	if f := ValidateCommissionRule(CommissionRule{Basis: "PROFIT", RatePct: dec("101")}); f["basis"] == "" || f["ratePct"] == "" {
		t.Fatalf("rule errors = %v", f)
	}
}
