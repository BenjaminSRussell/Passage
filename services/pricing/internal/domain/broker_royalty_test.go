package domain

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func day(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

func broker() BrokerContract {
	return BrokerContract{Name: "Agent Smith", BrokerVendorID: "v1", RateKind: RatePercentNet, Rate: d("2.5"), Currency: "USD",
		ValidFrom: day("2027-01-01"), ValidTo: day("2027-12-31"), PayableAccount: BrokerPayableAccount, ExpenseAccount: BrokerExpenseAccount,
		Status: AgreementActive}
}

func royalty() RoyaltyAgreement {
	return RoyaltyAgreement{Name: "Brand licence", LicensorVendorID: "v2", ProductGroupID: "BRANDED", Basis: RoyaltyNetSales, Period: PeriodMonth,
		Tiers: []Tier{{From: d("0"), RatePct: d("4")}, {From: d("50000"), RatePct: d("5")}}, MinimumRoyalty: d("1000"), Currency: "USD",
		ValidFrom: day("2027-01-01"), ValidTo: day("2027-12-31"), PayableAccount: RoyaltyPayableAccount, ExpenseAccount: RoyaltyExpenseAccount,
		Status: AgreementActive}
}

func line(net, qty string) ContractLine {
	return ContractLine{InvoiceLineID: "INV-1/1", InvoiceDate: day("2027-03-10"), CustomerID: "c1", CustomerGroupID: "g1", ProductID: "p1",
		ProductGroupID: "BRANDED", Currency: "USD", NetAmount: d(net), Quantity: d(qty)}
}

func TestValidateBrokerContract(t *testing.T) {
	if f := ValidateBrokerContract(broker()); len(f) != 0 {
		t.Fatalf("valid contract rejected: %v", f)
	}
	bad := broker()
	bad.Rate, bad.RateKind, bad.CustomerID, bad.CustomerGroupID = d("101"), RatePercentNet, "c", "g"
	bad.ValidTo = day("2026-01-01")
	f := ValidateBrokerContract(bad)
	for _, k := range []string{"rate", "customer", "validity"} {
		if f[k] == "" {
			t.Errorf("no error for %s: %v", k, f)
		}
	}
	perUnit := broker()
	perUnit.RateKind, perUnit.Rate = RatePerUnit, d("150")
	if f := ValidateBrokerContract(perUnit); len(f) != 0 {
		t.Fatalf("an amount per unit above 100 is fine: %v", f)
	}
}

func TestBrokerApplies(t *testing.T) {
	c := broker()
	c.CustomerGroupID, c.ProductGroupID = "g1", "BRANDED"
	l := line("1000", "10")
	if !BrokerApplies(c, l) {
		t.Fatal("scoped contract should apply")
	}
	for name, mutate := range map[string]func(*BrokerContract, *ContractLine){
		"other group":    func(c *BrokerContract, _ *ContractLine) { c.CustomerGroupID = "g2" },
		"other product":  func(c *BrokerContract, _ *ContractLine) { c.ProductGroupID = "OTHER" },
		"currency":       func(_ *BrokerContract, l *ContractLine) { l.Currency = "EUR" },
		"before":         func(_ *BrokerContract, l *ContractLine) { l.InvoiceDate = day("2026-12-31") },
		"after":          func(_ *BrokerContract, l *ContractLine) { l.InvoiceDate = day("2028-01-01") },
		"closed":         func(c *BrokerContract, _ *ContractLine) { c.Status = AgreementClosed },
		"specific other": func(c *BrokerContract, _ *ContractLine) { c.CustomerGroupID, c.CustomerID = "", "c9" },
	} {
		cc, ll := c, l
		mutate(&cc, &ll)
		if BrokerApplies(cc, ll) {
			t.Errorf("%s: contract should not apply", name)
		}
	}
	all := broker()
	if !BrokerApplies(all, l) {
		t.Fatal("an unscoped contract applies to every customer and product")
	}
}

func TestBrokerAmount(t *testing.T) {
	c := broker()
	if got := BrokerAmount(c, line("10000.00", "5")); !got.Equal(d("250.00")) {
		t.Fatalf("2.5%% of 10,000.00 = %s; want 250.00", got)
	}
	if got := BrokerAmount(c, line("0.10", "1")); !got.Equal(d("0")) { // 0.0025 rounds half-even to 0.00
		t.Fatalf("0.0025 rounds to %s; want 0", got)
	}
	u := broker()
	u.RateKind, u.Rate = RatePerUnit, d("0.35")
	if got := BrokerAmount(u, line("999", "12")); !got.Equal(d("4.20")) {
		t.Fatalf("12 x 0.35 = %s; want 4.20", got)
	}
	jp := broker()
	jp.Currency = "JPY"
	l := line("1050", "1")
	l.Currency = "JPY"
	if got := BrokerAmount(jp, l); !got.Equal(d("26")) { // 26.25 -> 26
		t.Fatalf("JPY 2.5%% of 1050 = %s; want 26", got)
	}
}

func TestBrokerCreditReversesProportionally(t *testing.T) {
	// Original: 10,000.00 net accrued 250.00. A 4,000.00 credit reverses 100.00, then the
	// final 6,000.00 reverses exactly the 150.00 left.
	got, err := BrokerCredit(d("10000"), d("250"), d("0"), d("0"), d("4000"), "USD")
	if err != nil || !got.Equal(d("-100")) {
		t.Fatalf("first credit = %s, %v; want -100", got, err)
	}
	got, err = BrokerCredit(d("10000"), d("250"), d("4000"), d("100"), d("6000"), "USD")
	if err != nil || !got.Equal(d("-150")) {
		t.Fatalf("last credit = %s, %v; want -150", got, err)
	}
	if _, err = BrokerCredit(d("10000"), d("250"), d("4000"), d("100"), d("6000.01"), "USD"); err == nil || err.Code != CodeCreditExceedsOriginal {
		t.Fatalf("over-credit error = %v; want PRC-0059", err)
	}
}

func TestBrokerCreditNeverLeavesResidue(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		basis := decimal.New(rapid.Int64Range(1, 5_000_000).Draw(t, "basis"), -2)
		c := broker()
		orig := BrokerAmount(c, ContractLine{Currency: "USD", NetAmount: basis})
		parts := rapid.IntRange(1, 6).Draw(t, "parts")
		left, credited, creditedAmt, sum := basis, decimal.Zero, decimal.Zero, decimal.Zero
		for i := 0; i < parts; i++ {
			part := left
			if i < parts-1 {
				part = left.Mul(decimal.New(rapid.Int64Range(0, 100).Draw(t, "pct"), -2)).Round(2)
			}
			amt, err := BrokerCredit(basis, orig, credited, creditedAmt, part, "USD")
			if err != nil {
				t.Fatalf("credit %s of %s: %v", part, left, err)
			}
			credited, creditedAmt, left, sum = credited.Add(part), creditedAmt.Add(amt.Neg()), left.Sub(part), sum.Add(amt)
		}
		if !sum.Add(orig).IsZero() {
			t.Fatalf("original %s and credits %s do not net to zero", orig, sum)
		}
	})
}

func TestValidateRoyaltyAgreement(t *testing.T) {
	if f := ValidateRoyaltyAgreement(royalty()); len(f) != 0 {
		t.Fatalf("valid agreement rejected: %v", f)
	}
	bad := royalty()
	bad.ProductID = "p1" // both a product and a product group
	bad.MinimumRoyalty = d("-1")
	bad.Tiers = []Tier{{From: d("10"), RatePct: d("4")}, {From: d("5"), RatePct: d("5")}}
	f := ValidateRoyaltyAgreement(bad)
	for _, k := range []string{"product", "minimumRoyalty", "tiers"} {
		if f[k] == "" {
			t.Errorf("no error for %s: %v", k, f)
		}
	}
	q := royalty()
	q.Basis, q.Tiers = RoyaltyQuantity, []Tier{{From: d("0"), AmountPerUnit: d("0.5")}}
	if f := ValidateRoyaltyAgreement(q); len(f) != 0 {
		t.Fatalf("quantity royalty rejected: %v", f)
	}
	q.Tiers = []Tier{{From: d("0"), RatePct: d("5")}}
	if f := ValidateRoyaltyAgreement(q); f["tiers"] == "" {
		t.Fatal("a quantity royalty takes an amount per unit, not a rate")
	}
	none := royalty()
	none.Basis = "X"
	if f := ValidateRoyaltyAgreement(none); f["basis"] == "" {
		t.Fatal("bad basis accepted")
	}
}

// FR-26.17: tiered royalty accrues like a rebate, and the period's accruals sum to the target.
func TestRoyaltyTiersRecomputeThePeriod(t *testing.T) {
	a := royalty()
	view := RoyaltyRebateView(a)
	st, inc := Accrue(view, PeriodState{}, d("40000"), d("0"))
	if !inc.Equal(d("1600")) {
		t.Fatalf("first accrual = %s; want 1,600.00 (4%% of 40,000)", inc)
	}
	st, inc = Accrue(view, st, d("20000"), d("0"))
	if !inc.Equal(d("1400")) || !st.Accrued.Equal(d("3000")) {
		t.Fatalf("second accrual = %s (period %s); want 1,400.00 so the period is 5%% of 60,000 = 3,000.00", inc, st.Accrued)
	}
	_, inc = Accrue(view, st, d("-30000"), d("0")) // credit drops back under the 50,000 tier
	if !inc.Equal(d("-1800")) {
		t.Fatalf("credit accrual = %s; want -1,800.00 (4%% of 30,000 = 1,200 less 3,000)", inc)
	}
}

func TestMinimumShortfall(t *testing.T) {
	cases := []struct{ min, accrued, want string }{
		{"1000", "0", "1000"}, {"1000", "600.50", "399.50"}, {"1000", "1000", "0"}, {"1000", "1500", "0"}, {"0", "0", "0"},
	}
	for _, c := range cases {
		if got := MinimumShortfall(d(c.min), d(c.accrued)); !got.Equal(d(c.want)) {
			t.Errorf("shortfall(%s, %s) = %s; want %s", c.min, c.accrued, got, c.want)
		}
	}
}

func TestPeriodBounds(t *testing.T) {
	cases := []struct{ key, first, last string }{
		{"2027-02", "2027-02-01", "2027-02-28"}, {"2028-02", "2028-02-01", "2028-02-29"},
		{"2027-Q4", "2027-10-01", "2027-12-31"}, {"2027", "2027-01-01", "2027-12-31"},
	}
	for _, c := range cases {
		f, l, ok := PeriodBounds(c.key)
		if !ok || dayOf(f) != c.first || dayOf(l) != c.last {
			t.Errorf("PeriodBounds(%s) = %s %s %v; want %s %s", c.key, dayOf(f), dayOf(l), ok, c.first, c.last)
		}
	}
	if _, _, ok := PeriodBounds("2027-13"); ok {
		t.Error("month 13 accepted")
	}
	if PeriodMatches(PeriodMonth, "2027-Q1") || !PeriodMatches(PeriodQuarter, "2027-Q1") || !PeriodMatches(PeriodYear, "2027") || PeriodMatches(PeriodYear, "2027-01") {
		t.Error("PeriodMatches mixes granularities")
	}
	if !PeriodOverlapsValidity("2027-12", day("2027-12-31"), day("2028-06-30")) || PeriodOverlapsValidity("2026-12", day("2027-01-01"), day("2027-12-31")) {
		t.Error("PeriodOverlapsValidity wrong at the edges")
	}
}
