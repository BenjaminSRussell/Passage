package domain

import (
	"testing"
)

// TestEquityMethod_AC40_14 is AC-40.14: a 30 % associate with 300,000.00 invested,
// 200,000.00 net income and 20,000.00 dividends. Carrying amount 340,000.00, share
// of profit 60,000.00, and the two entries balance.
func TestEquityMethod_AC40_14(t *testing.T) {
	res, err := EquityMethod(Associate{
		Currency:          "USD",
		OwnershipPct:      d("30"),
		OpeningCarrying:   d("300000"),
		NetIncome:         d("200000"),
		DividendsReceived: d("20000"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.ShareOfProfit.Equal(d("60000")) {
		t.Fatalf("share of profit = %s, want 60000", res.ShareOfProfit)
	}
	if !res.ClosingCarrying.Equal(d("340000")) {
		t.Fatalf("carrying = %s, want 340000", res.ClosingCarrying)
	}
	if len(res.Entries) != 4 {
		t.Fatalf("entries = %+v", res.Entries)
	}
	want := []Entry{
		{RuleCode: RuleEquity, Account: AcctInvestmentAssociate, Debit: d("60000")},
		{RuleCode: RuleEquity, Account: AcctShareOfAssociates, Credit: d("60000")},
		{RuleCode: RuleEquity, Account: AcctDividendIncome, Debit: d("20000")},
		{RuleCode: RuleEquity, Account: AcctInvestmentAssociate, Credit: d("20000")},
	}
	for i, w := range want {
		e := res.Entries[i]
		if e.Account != w.Account || !e.Debit.Equal(w.Debit) || !e.Credit.Equal(w.Credit) {
			t.Fatalf("entry %d = %+v, want %+v", i, e, w)
		}
	}
}

func TestEquityMethod_Rejects(t *testing.T) {
	base := Associate{Currency: "USD", OwnershipPct: d("30"), OpeningCarrying: d("1"), NetIncome: d("1")}
	if _, err := EquityMethod(base); err != nil {
		t.Fatalf("base case failed: %v", err)
	}
	rejects := []struct {
		name string
		a    Associate
	}{
		{"control ownership uses full consolidation", Associate{Currency: "USD", OwnershipPct: d("51"), NetIncome: d("1")}},
		{"zero ownership", Associate{Currency: "USD", OwnershipPct: d("0"), NetIncome: d("1")}},
		{"bad currency", Associate{Currency: "US", OwnershipPct: d("30"), NetIncome: d("1")}},
		{"negative dividends", Associate{Currency: "USD", OwnershipPct: d("30"), DividendsReceived: d("-1")}},
		{"negative opening carrying amount", Associate{Currency: "USD", OwnershipPct: d("30"), OpeningCarrying: d("-1")}},
		{"dividends above the carrying amount", Associate{Currency: "USD", OwnershipPct: d("30"), OpeningCarrying: d("100"), NetIncome: d("0"), DividendsReceived: d("101")}},
	}
	for _, tc := range rejects {
		if _, err := EquityMethod(tc.a); err == nil {
			t.Fatalf("%s: want an error", tc.name)
		}
	}
}

// TestEquityMethod_LossStopsAtZero checks IAS 28.38: a 30 % share of a 500,000.00
// loss is 150,000.00, but only the 100,000.00 carrying amount is recognized and
// 50,000.00 is reported as unrecognized.
func TestEquityMethod_LossStopsAtZero(t *testing.T) {
	res, err := EquityMethod(Associate{Currency: "USD", OwnershipPct: d("30"), OpeningCarrying: d("100000"), NetIncome: d("-500000")})
	if err != nil {
		t.Fatal(err)
	}
	if !res.ShareOfProfit.Equal(d("-100000")) || !res.UnrecognizedLoss.Equal(d("50000")) || !res.ClosingCarrying.IsZero() {
		t.Fatalf("result = %+v", res)
	}
	if res.Entries[0].Account != AcctInvestmentAssociate || !res.Entries[0].Credit.Equal(d("100000")) {
		t.Fatalf("loss must credit the investment: %+v", res.Entries)
	}
	if err := CheckBalanced(res.Entries); err != nil {
		t.Fatal(err)
	}
}

func TestDivestitureGain(t *testing.T) {
	got, err := DivestitureGain(Divestiture{
		Currency:          "USD",
		Proceeds:          d("500"),
		NetAssetsCarrying: d("300"),
		Goodwill:          d("50"),
		NCICarrying:       d("20"),
		CTARecycled:       d("10"),
	})
	if err != nil {
		t.Fatal(err)
	}
	// 500 − (300 + 50 − 20) + 10 = 180
	if !got.Equal(d("180")) {
		t.Fatalf("gain = %s, want 180", got)
	}
	loss, _ := DivestitureGain(Divestiture{Currency: "USD", Proceeds: d("100"), NetAssetsCarrying: d("300")})
	if !loss.Equal(d("-200")) {
		t.Fatalf("loss = %s, want -200", loss)
	}
}

func TestCheckChangeOfControl(t *testing.T) {
	tests := []struct {
		name        string
		prior, next string
		wantErr     bool
	}{
		{"first acquisition of control is not a step", "0", "60", false},
		{"equity to control is a step acquisition", "30", "60", true},
		{"at exactly 50 percent is still joint control", "30", "50", false},
		{"increase within control is fine", "60", "80", false},
		{"decrease from control is a divestiture path", "80", "30", false},
		{"ownership above 100 is invalid", "30", "101", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckChangeOfControl(d(tc.prior), d(tc.next))
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if err != nil && tc.next != "101" && err.(*Error).Code != CodeChangeOfControl {
				t.Fatalf("code = %s", err.(*Error).Code)
			}
		})
	}
}
