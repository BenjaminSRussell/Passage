package domain

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestDispose_PartialAC5(t *testing.T) {
	// AC-37.5: cost 10,000, accumulated 4,000, proceeds 7,000, 50% disposal.
	got, err := Dispose(d("10000"), d("4000"), d("7000"), d("50"), "USD")
	if err != nil {
		t.Fatal(err)
	}
	checks := map[string][2]decimal.Decimal{
		"disposed cost":  {got.DisposedCost, d("5000")},
		"disposed acc":   {got.DisposedAccumulated, d("2000")},
		"nbv disposed":   {got.NBVDisposed, d("3000")},
		"gain":           {got.GainLoss, d("4000")},
		"remaining cost": {got.RemainingCost, d("5000")},
		"remaining acc":  {got.RemainingAccumulated, d("2000")},
	}
	for name, c := range checks {
		if !c[0].Equal(c[1]) {
			t.Errorf("%s = %s, want %s", name, c[0], c[1])
		}
	}
}

func TestDispose_RejectsBadPercentage(t *testing.T) {
	for _, pct := range []string{"0", "-5", "100.01"} {
		if _, err := Dispose(d("100"), d("0"), d("0"), d(pct), "USD"); err == nil {
			t.Errorf("percent %s accepted", pct)
		}
	}
}

func TestDispose_PartialsConserveCostAndNBV(t *testing.T) {
	// Property: disposed + remaining equals the original cost and accumulated.
	cost, acc := d("1234.57"), d("777.77")
	for _, pct := range []string{"1", "33.33", "50", "99.99", "100"} {
		got, err := Dispose(cost, acc, d("10"), d(pct), "USD")
		if err != nil {
			t.Fatal(err)
		}
		if !got.DisposedCost.Add(got.RemainingCost).Equal(cost) || !got.DisposedAccumulated.Add(got.RemainingAccumulated).Equal(acc) {
			t.Errorf("pct %s does not conserve cost and accumulated", pct)
		}
	}
}

func TestAfterDisposal(t *testing.T) {
	cases := []struct {
		name    string
		current AssetStatus
		pct     string
		kind    DisposalKind
		want    AssetStatus
		wantErr bool
	}{
		{"partial keeps open", StatusOpen, "40", DisposalSale, StatusOpen, false},
		{"full sale", StatusOpen, "100", DisposalSale, StatusSold, false},
		{"full scrap", StatusOpen, "100", DisposalScrap, StatusScrapped, false},
		{"sold cannot dispose again", StatusSold, "10", DisposalSale, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := AfterDisposal(c.current, d(c.pct), c.kind)
			if (err != nil) != c.wantErr || got != c.want {
				t.Fatalf("got %q, %v; want %q, err=%v", got, err, c.want, c.wantErr)
			}
		})
	}
}

func TestAcquireStatus(t *testing.T) {
	if got, err := AcquireStatus(StatusNotYetAcquired); err != nil || got != StatusOpen {
		t.Fatalf("first acquisition: %q %v", got, err)
	}
	if _, err := AcquireStatus(StatusScrapped); err == nil {
		t.Fatal("acquisition on scrapped asset accepted")
	}
}

func TestIndexedInsuranceAC9(t *testing.T) {
	// AC-37.9: replacement 100,000.00 at index 112.0 (2024) and 118.5 (2026).
	indexed, err := IndexedValue(d("100000"), d("118.5"), d("112.0"), "USD")
	if err != nil {
		t.Fatal(err)
	}
	if indexed.StringFixed(2) != "105803.57" {
		t.Fatalf("indexed = %s, want 105803.57", indexed)
	}
	if !UnderInsured(d("95223.20"), indexed, "USD") {
		t.Error("insured 95,223.20 should be under-insured")
	}
	if UnderInsured(d("95223.21"), indexed, "USD") {
		t.Error("insured 95,223.21 should not be under-insured")
	}
}

func TestIndexFallbackUsesLatestYear(t *testing.T) {
	series := map[int]decimal.Decimal{2024: d("112"), 2025: d("115")}
	got, ok := IndexFor(series, 2026)
	if !ok || !got.Fallback || got.Year != 2025 || !got.Value.Equal(d("115")) {
		t.Fatalf("fallback = %+v ok=%v", got, ok)
	}
	if _, ok := IndexFor(map[int]decimal.Decimal{}, 2026); ok {
		t.Fatal("empty series reported a value")
	}
}

func TestApprovalLevelAC11(t *testing.T) {
	cases := map[string]string{"50000": "PlantManager", "50000.01": "CFO", "320000": "CFO", "500001": "Board"}
	for amount, want := range cases {
		if got := ApprovalLevel(d(amount)); got != want {
			t.Errorf("ApprovalLevel(%s) = %s, want %s", amount, got, want)
		}
	}
}

func TestCapexTransitions(t *testing.T) {
	if !CanTransitionCapex(CapexInApproval, CapexApproved) || CanTransitionCapex(CapexDraft, CapexApproved) {
		t.Fatal("state machine does not match BR-37.12")
	}
	if !CanTransitionCapex(CapexPostponed, CapexDraft) {
		t.Fatal("postponed must return to draft")
	}
}

func TestNeedsReapproval(t *testing.T) {
	// Lower of 10% and 10,000: approved 200,000 gives a limit of 10,000.
	if NeedsReapproval(d("200000"), d("210000")) {
		t.Error("increase equal to the limit should not re-approve")
	}
	if !NeedsReapproval(d("200000"), d("210001")) {
		t.Error("increase above the limit should re-approve")
	}
	// Approved 50,000 gives a limit of 5,000 (10%).
	if !NeedsReapproval(d("50000"), d("55001")) {
		t.Error("10% rule should apply below 100,000")
	}
}
