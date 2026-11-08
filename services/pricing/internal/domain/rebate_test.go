package domain

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func rbAgreement(basis string, tiers ...Tier) RebateAgreement {
	return RebateAgreement{ID: "A1", LegalEntityID: "le", Name: "Monthly 2%", CustomerID: "C1", Basis: basis,
		Period: PeriodMonth, Tiers: tiers, Currency: "USD", ValidFrom: msDay("2026-01-01"), ValidTo: msDay("2026-12-31"),
		AccrualAccount: "2150", ExpenseAccount: "4150"}
}

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func TestPeriodKey(t *testing.T) {
	cases := []struct {
		period string
		day    string
		want   string
	}{
		{PeriodMonth, "2026-10-05", "2026-10"},
		{PeriodMonth, "2026-02-28", "2026-02"},
		{PeriodQuarter, "2026-10-05", "2026-Q4"},
		{PeriodQuarter, "2026-03-31", "2026-Q1"},
		{PeriodYear, "2026-07-01", "2026"},
	}
	for _, tc := range cases {
		day, _ := time.Parse("2006-01-02", tc.day)
		if got := PeriodKey(tc.period, day); got != tc.want {
			t.Fatalf("PeriodKey(%s, %s) = %s, want %s", tc.period, tc.day, got, tc.want)
		}
	}
}

func TestPeriodTargetPercentTiers(t *testing.T) {
	a := rbAgreement(BasisNetAmount, Tier{From: decimal.Zero, RatePct: dec("1")}, Tier{From: decimal.NewFromInt(50000), RatePct: dec("3")})
	cases := []struct {
		cum  string
		want string
	}{
		{"0", "0"},
		{"10000", "100"},
		{"49999.99", "500"},
		{"50000", "1500"},
		{"60000", "1800"},
	}
	for _, tc := range cases {
		if got := PeriodTarget(a, dec(tc.cum), decimal.Zero); !got.Equal(dec(tc.want)) {
			t.Fatalf("target at %s = %s, want %s", tc.cum, got, tc.want)
		}
	}
}

// The AC-26.8 example: a 2% monthly rebate on a 10,000.00 invoice accrues 200.00.
func TestPeriodTargetAC268(t *testing.T) {
	a := rbAgreement(BasisNetAmount, Tier{From: decimal.Zero, RatePct: dec("2")})
	if got := PeriodTarget(a, dec("10000"), decimal.Zero); !got.Equal(dec("200")) {
		t.Fatalf("AC-26.8 accrual = %s, want 200.00", got)
	}
}

func TestPeriodTargetQuantityTiers(t *testing.T) {
	a := rbAgreement(BasisQuantity, Tier{From: decimal.Zero, AmountPerUnit: dec("2.50")}, Tier{From: decimal.NewFromInt(100), AmountPerUnit: dec("3")})
	if got := PeriodTarget(a, decimal.Zero, dec("40")); !got.Equal(dec("100")) {
		t.Fatalf("quantity target at 40 = %s, want 100.00", got)
	}
	if got := PeriodTarget(a, decimal.Zero, dec("100")); !got.Equal(dec("300")) {
		t.Fatalf("quantity target at 100 = %s, want 300.00", got)
	}
}

func TestValidateAgreement(t *testing.T) {
	ok := rbAgreement(BasisNetAmount, Tier{From: decimal.Zero, RatePct: dec("2")})
	noCustomer := ok
	noCustomer.CustomerID = ""
	bothCustomers := ok
	bothCustomers.CustomerGroupID = "G1"
	zeroRate := rbAgreement(BasisNetAmount, Tier{From: decimal.Zero})
	descending := rbAgreement(BasisNetAmount, Tier{From: decimal.NewFromInt(100), RatePct: dec("1")}, Tier{From: decimal.NewFromInt(50), RatePct: dec("2")})
	wrongKind := rbAgreement(BasisQuantity, Tier{From: decimal.Zero, RatePct: dec("2")})
	cases := []struct {
		name  string
		a     RebateAgreement
		field string
	}{
		{"valid", ok, ""},
		{"no customer", noCustomer, "customer"},
		{"two customer scopes", bothCustomers, "customer"},
		{"zero rate tier", zeroRate, "tiers"},
		{"thresholds not increasing", descending, "tiers"},
		{"rate on a quantity basis", wrongKind, "tiers"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateAgreement(tc.a)
			if tc.field == "" {
				if err != nil {
					t.Fatalf("valid agreement rejected: %v", err.Fields)
				}
				return
			}
			if err == nil || err.Fields[tc.field] == "" {
				t.Fatalf("want field %s rejected, got %v", tc.field, err)
			}
		})
	}
}

// TestAccrueTierCrossing walks invoices through one period (BR-26.8): the first tier
// accrues at 1%; the invoice that crosses 5,000 recomputes the whole period at 2.5% and
// books the delta; a credit note that drops the period back below 5,000 books a negative delta.
func TestAccrueTierCrossing(t *testing.T) {
	a := rbAgreement(BasisNetAmount, Tier{From: decimal.Zero, RatePct: dec("1")}, Tier{From: decimal.NewFromInt(5000), RatePct: dec("2.5")})
	steps := []struct {
		value, wantInc, wantAccrued string
	}{
		{"3000", "30", "30"},    // 3,000 at 1%
		{"1000", "10", "40"},    // 4,000 at 1%
		{"2000", "110", "150"},  // 6,000 crosses 5,000: 6,000 x 2.5% = 150, delta 110
		{"-1500", "-105", "45"}, // credit: 4,500 back at 1% = 45, delta -105
		{"0.01", "0", "45"},     // 4,500.01 x 1% = 45.0001 rounds to 45.00
	}
	state := PeriodState{}
	for i, s := range steps {
		var inc decimal.Decimal
		state, inc = Accrue(a, state, dec(s.value), decimal.Zero)
		if !inc.Equal(dec(s.wantInc)) || !state.Accrued.Equal(dec(s.wantAccrued)) {
			t.Fatalf("invoice %d (%s): accrued %s (period %s), want %s (period %s)", i+1, s.value, inc, state.Accrued, s.wantInc, s.wantAccrued)
		}
	}
}

// TestAccrueSumsToPeriodTarget is the reconciliation rule (PRD 5.26.9): whatever the order
// and sign of the invoices, the accruals of a period sum to the target at its cumulative basis.
func TestAccrueSumsToPeriodTarget(t *testing.T) {
	a := rbAgreement(BasisNetAmount, Tier{From: decimal.Zero, RatePct: dec("1")}, Tier{From: decimal.NewFromInt(5000), RatePct: dec("2.5")}, Tier{From: decimal.NewFromInt(20000), RatePct: dec("4")})
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 20).Draw(t, "invoices")
		state := PeriodState{}
		sum := decimal.Zero
		for i := 0; i < n; i++ {
			value := decimal.New(rapid.Int64Range(-200000, 3000000).Draw(t, "cents"), -2)
			var inc decimal.Decimal
			state, inc = Accrue(a, state, value, decimal.Zero)
			sum = sum.Add(inc)
		}
		if want := PeriodTarget(a, state.Value, decimal.Zero); !sum.Equal(want) {
			t.Fatalf("sum of accruals %s != period target %s at basis %s", sum, want, state.Value)
		}
	})
}

func TestInProductScope(t *testing.T) {
	const p1, p2, g1, g2 = "00000000-0000-0000-0008-000000000001", "00000000-0000-0000-0008-000000000002",
		"00000000-0000-0000-0009-000000000001", "00000000-0000-0000-0009-000000000002"
	cases := []struct {
		name   string
		a      RebateAgreement
		prod   string
		group  string
		wantIn bool
	}{
		{"no scope takes any line", RebateAgreement{}, p1, g1, true},
		{"no scope takes a line without keys", RebateAgreement{}, "", "", true},
		{"product scope, same product", RebateAgreement{ProductID: p1}, p1, g2, true},
		{"product scope, other product", RebateAgreement{ProductID: p1}, p2, g1, false},
		{"product scope, group only does not match", RebateAgreement{ProductID: p1}, "", g1, false},
		{"group scope, same group", RebateAgreement{ProductGroupID: g1}, p2, g1, true},
		{"group scope, other group", RebateAgreement{ProductGroupID: g1}, p1, g2, false},
		{"group scope, line without group", RebateAgreement{ProductGroupID: g1}, p1, "", false},
	}
	for _, c := range cases {
		if got := InProductScope(c.a, c.prod, c.group); got != c.wantIn {
			t.Errorf("%s: InProductScope = %v; want %v", c.name, got, c.wantIn)
		}
	}
}

func TestValidateAgreementRejectsBothProductScopes(t *testing.T) {
	a := RebateAgreement{PartyRole: PartyCustomer, CustomerID: "c", Basis: BasisNetAmount, Period: PeriodMonth, Currency: "USD",
		ValidFrom: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), ValidTo: time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC),
		Tiers: []Tier{{From: decimal.Zero, RatePct: decimal.NewFromInt(2)}}, ProductID: "p", ProductGroupID: "g"}
	e := ValidateAgreement(a)
	if e == nil || e.Fields["productScope"] == "" {
		t.Fatalf("both product and group = %v; want a productScope field error", e)
	}
	a.ProductGroupID = ""
	if e := ValidateAgreement(a); e != nil {
		t.Fatalf("product scope alone = %v; want valid", e)
	}
}
