package domain

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestPPV_AC397PostsToDM(t *testing.T) {
	total, split := PPV(d("10"), d("5.20"), Split{CompDM: d("5.00")})
	if !total.Equal(d("2.00")) {
		t.Errorf("PPV = %s, want 2.00", total)
	}
	if !split[CompDM].Equal(d("2.00")) || len(split) != 1 {
		t.Errorf("split = %v, want all in DM", split)
	}
}

func TestPPV_SplitsByStandardComponentRatio(t *testing.T) {
	// Standard 10.00 split 92% material, 5% freight, 3% duty; receipt 1.00 over.
	std := Split{CompDM: d("9.20"), "FREIGHT": d("0.50"), "DUTY": d("0.30")}
	total, split := PPV(d("100"), d("11.00"), std)
	if !total.Equal(d("100.00")) {
		t.Fatalf("total = %s", total)
	}
	sum := decimal.Zero
	for _, v := range split {
		sum = sum.Add(v)
	}
	if !sum.Equal(total) {
		t.Errorf("components %s do not add to %s", sum, total)
	}
	if !split[CompDM].Equal(d("92.00")) {
		t.Errorf("DM share = %s, want 92.00", split[CompDM])
	}
}

func TestMixedCost_AC3911(t *testing.T) {
	got, err := MixedCost([]Alternative{
		{Source: "vendor-A", Percent: d("60"), Split: Split{CompDM: d("10.00")}},
		{Source: "production", Percent: d("40"), Split: Split{CompDM: d("12.50")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Total().Equal(d("11.00")) || !got[CompDM].Equal(d("11.00")) {
		t.Errorf("mixed = %v, want DM 11.00", got)
	}
}

func TestMixedCost_RatiosMustTotal100AndZeroRatioIsExcluded(t *testing.T) {
	if _, err := MixedCost([]Alternative{{Source: "a", Percent: d("60"), Split: Split{CompDM: d("1")}}}); err == nil {
		t.Error("ratios totalling 60 accepted")
	}
	got, err := MixedCost([]Alternative{
		{Source: "a", Percent: d("100"), Split: Split{CompDM: d("2")}},
		{Source: "b", Percent: d("0"), Split: Split{CompDM: d("50")}},
	})
	if err != nil || !got[CompDM].Equal(d("2.000000")) {
		t.Errorf("zero ratio leaked into roll: %v %v", got, err)
	}
}

func TestIssueCOGS_AC3912ByComponent(t *testing.T) {
	unit := Split{CompDM: d("21.20"), CompDL: d("54.00"), CompIndMat: d("2.12"), CompIndLab: d("27.00")}
	lines, total := IssueCOGS(d("10"), unit)
	if !total.Equal(d("1043.20")) {
		t.Fatalf("total = %s, want 1043.20", total)
	}
	want := map[string]string{CompDM: "212.00", CompDL: "540.00", CompIndMat: "21.20", CompIndLab: "270.00"}
	for c, w := range want {
		if lines[c].StringFixed(2) != w {
			t.Errorf("%s = %s, want %s", c, lines[c].StringFixed(2), w)
		}
	}
}

func TestIssueCOGS_ResidualGoesToMaterial(t *testing.T) {
	unit := Split{CompDM: d("0.333"), CompDL: d("0.333"), CompIndLab: d("0.334")}
	lines, total := IssueCOGS(d("1"), unit)
	sum := decimal.Zero
	for _, v := range lines {
		sum = sum.Add(v)
	}
	if !sum.Equal(total) {
		t.Errorf("lines %s do not add to total %s", sum, total)
	}
}

func TestRevalue_AC394Journal(t *testing.T) {
	p := Revalue([]RevalLine{{ItemID: "X", SiteID: "S1", Qty: d("1000"), OldStd: d("10.00"), NewStd: d("10.50")}}, "")
	if !p.Total.Equal(d("500.00")) {
		t.Fatalf("total = %s, want 500.00", p.Total)
	}
	if len(p.Journal) != 2 || p.Journal[0].Account != AccountInventory || !p.Journal[0].Debit.Equal(d("500.00")) ||
		p.Journal[1].Account != AccountCostChangeVar || !p.Journal[1].Credit.Equal(d("500.00")) {
		t.Errorf("journal = %+v", p.Journal)
	}
}

func TestRevalue_LossReversesJournal(t *testing.T) {
	p := Revalue([]RevalLine{{ItemID: "X", Qty: d("10"), OldStd: d("5.00"), NewStd: d("4.00")}}, "")
	if !p.Total.Equal(d("-10.00")) || p.Journal[0].Account != AccountCostChangeVar || !p.Journal[0].Debit.Equal(d("10.00")) {
		t.Errorf("loss journal = %+v total %s", p.Journal, p.Total)
	}
}

func TestRevalue_JournalIsBalancedAndEmptyWhenNoChange(t *testing.T) {
	p := Revalue([]RevalLine{{Qty: d("3"), OldStd: d("1"), NewStd: d("1")}}, "")
	if len(p.Journal) != 0 || !p.Total.IsZero() {
		t.Errorf("no-change preview = %+v", p)
	}
}

func TestConversionAdjustment_AC3910(t *testing.T) {
	total, journal := ConversionAdjustment([]ConversionLine{{ItemID: "X", Qty: d("500"), StdUnit: d("10.00"), ActualValue: d("4925.00")}})
	if !total.Equal(d("75.00")) {
		t.Fatalf("adjustment = %s, want 75.00", total)
	}
	if journal[0].Account != AccountInventory || !journal[0].Debit.Equal(d("75.00")) {
		t.Errorf("journal = %+v", journal)
	}
}

func TestRevalue_RoundsToTheCurrencyMinorUnit(t *testing.T) {
	// 7 × 0.3333 = 2.3331: cents in USD, whole yen in JPY, mils in KWD (BR-39.1).
	line := []RevalLine{{ItemID: "X", Qty: d("7"), OldStd: d("1"), NewStd: d("1.3333")}}
	for currency, want := range map[string]string{"": "2.33", "USD": "2.33", "JPY": "2", "KWD": "2.333"} {
		if got := Revalue(line, currency).Total; !got.Equal(d(want)) {
			t.Errorf("%q total = %s, want %s", currency, got, want)
		}
	}
}
