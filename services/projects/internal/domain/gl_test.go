package domain

import "testing"

func TestRequiredProvision_NeverDoubleCountsLossShownInTheMargin(t *testing.T) {
	cases := []struct{ name, eac, contract, cost, revenue, want string }{
		{"no loss expected", "400000", "500000", "120000", "150000", "0"},
		{"worked example period 3: 20,000 loss, 15,000 already in the margin", "520000", "500000", "390000", "375000", "5000"},
		{"margin already shows the whole loss", "520000", "500000", "400000", "375000", "0"},
		{"margin shows more than the loss", "520000", "500000", "420000", "375000", "0"},
		{"loss before any revenue", "120000", "100000", "60000", "0", "0"},
		{"loss with revenue above cost", "120000", "100000", "30000", "50000", "20000"},
	}
	for _, c := range cases {
		got := RequiredProvision(d(c.eac), d(c.contract), d(c.cost), d(c.revenue))
		if !got.Equal(d(c.want)) {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

func TestEstimateLines_NeedsEveryAccountAndMovesOnlyTheDelta(t *testing.T) {
	p := PostingProfile{Key: ProfileKey{ProjectGroup: Wildcard, Category: Wildcard, LineProperty: Wildcard}, WIPAccount: "1410", AccruedRevenueAccount: "1420", InvoicedRevenueAccount: "4000",
		OnAccountAccount: "2400"}
	if _, err := EstimateLines(p, Position{}, Position{Revenue: d("10")}, "USD"); err == nil {
		t.Fatal("a profile without cost of sales and provision accounts must be refused")
	}
	p.CostOfSalesAccount, p.LossProvisionAccount, p.LossExpenseAccount = "5100", "2500", "5200"
	lines, err := EstimateLines(p, Position{Revenue: d("10"), Cost: d("6"), Provision: d("3")}, Position{Revenue: d("10"), Cost: d("6"), Provision: d("1")}, "USD")
	if err != nil || len(lines) != 2 || lines[0].Account != "2500" || lines[0].Credit || lines[1].Account != "5200" || !lines[1].Credit || lines[0].Amount != "2.00" || !lines[0].Control || lines[1].Control {
		t.Fatalf("a released provision debits the liability and credits the expense: %+v %v", lines, err)
	}
}

func TestTransactionLines_CreditTheCategoryOffsetAccount(t *testing.T) {
	p := PostingProfile{WIPAccount: "1410"}
	if _, err := TransactionLines(p, d("5"), "USD"); err == nil {
		t.Fatal("a profile without an offset account must be refused")
	}
	p.OffsetAccount = "2100"
	lines, err := TransactionLines(p, d("5"), "USD")
	if err != nil || len(lines) != 2 || lines[0].Account != "1410" || lines[1].Account != "2100" || !lines[1].Credit {
		t.Fatalf("%+v %v", lines, err)
	}
}
