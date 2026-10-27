package domain

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestCanMoveDispute_BR306(t *testing.T) {
	tests := []struct {
		from, to string
		want     bool
	}{
		{DisputeOpen, DisputeUnderReview, true},
		{DisputeOpen, DisputeRejected, true},
		{DisputeOpen, DisputeResolvedCredit, false}, // must pass UNDER_REVIEW first
		{DisputeUnderReview, DisputeResolvedCredit, true},
		{DisputeUnderReview, DisputeResolvedWriteOff, true},
		{DisputeUnderReview, DisputeOpen, false},
		{DisputeResolvedPaid, DisputeRejected, false},
	}
	for _, tc := range tests {
		if got := CanMoveDispute(tc.from, tc.to); got != tc.want {
			t.Errorf("CanMoveDispute(%s,%s) = %v, want %v", tc.from, tc.to, got, tc.want)
		}
	}
}

func TestResolutionStatus(t *testing.T) {
	tests := map[string]string{"CREDIT": DisputeResolvedCredit, "PAID": DisputeResolvedPaid,
		"WRITE_OFF": DisputeResolvedWriteOff, "REJECTED": DisputeRejected}
	for in, want := range tests {
		if got, ok := ResolutionStatus(in); !ok || got != want {
			t.Errorf("ResolutionStatus(%s) = %s,%v", in, got, ok)
		}
	}
	if _, ok := ResolutionStatus("MAYBE"); ok {
		t.Error("unknown resolution accepted")
	}
}

func TestPromiseHolds_BR302(t *testing.T) {
	promise := day("2027-03-10")
	tests := []struct {
		asOf string
		want bool
	}{{"2027-03-10", true}, {"2027-03-12", true}, {"2027-03-13", false}}
	for _, tc := range tests {
		if got := PromiseHolds(promise, day(tc.asOf)); got != tc.want {
			t.Errorf("PromiseHolds at %s = %v, want %v", tc.asOf, got, tc.want)
		}
	}
}

func TestValidateAmountWithin(t *testing.T) {
	if ValidateAmountWithin(dec("500.00"), dec("500.00")) != nil {
		t.Error("amount equal to open must be allowed")
	}
	if e := ValidateAmountWithin(dec("500.01"), dec("500.00")); e == nil || e.Code != CodeAmountRange {
		t.Errorf("amount above open: %v", e)
	}
	if ValidateAmountWithin(dec("0"), dec("10")) == nil {
		t.Error("zero amount must be rejected")
	}
}

func TestInterestAmount_BR305(t *testing.T) {
	tests := []struct {
		open, rate string
		days       int
		basis      string
		want       string
	}{
		{"1000.00", "0.09", 30, BasisAct365, "7.40"}, // 1000*0.09*30/365 = 7.397...
		{"1000.00", "0.09", 30, BasisAct360, "7.50"}, // /360
		{"1000.00", "0.00", 90, BasisAct365, "0.00"},
		{"250.00", "0.12", 0, BasisAct365, "0.00"},
	}
	for _, tc := range tests {
		got := InterestAmount(dec(tc.open), dec(tc.rate), tc.days, tc.basis)
		if !got.Equal(dec(tc.want)) {
			t.Errorf("InterestAmount(%s,%s,%d,%s) = %s, want %s", tc.open, tc.rate, tc.days, tc.basis, got, tc.want)
		}
	}
}

func TestPlanInterest_ExclusionsAndMinimum(t *testing.T) {
	asOf := day("2027-03-31")
	items := []DunnableItem{
		{ID: "ok", CustomerID: "C1", Currency: "USD", DueDate: day("2027-01-31"), OpenAmount: dec("1000.00")},
		{ID: "disp", CustomerID: "C2", Currency: "USD", DueDate: day("2027-01-31"), OpenAmount: dec("1000.00"), HasDispute: true},
		{ID: "prom", CustomerID: "C3", Currency: "USD", DueDate: day("2027-01-31"), OpenAmount: dec("1000.00"), PromiseDate: ptr(day("2027-03-30"))},
		{ID: "tiny", CustomerID: "C4", Currency: "USD", DueDate: day("2027-01-31"), OpenAmount: dec("20.00")},
		{ID: "fee", CustomerID: "C1", SourceType: "FEE", Currency: "USD", DueDate: day("2027-01-31"), OpenAmount: dec("1000.00")},
		{ID: "notdue", CustomerID: "C1", Currency: "USD", DueDate: day("2027-03-20"), OpenAmount: dec("1000.00")},
	}
	cands, excl := PlanInterest(asOf, DefaultGraceDays, dec("0.12"), BasisAct365, items)
	if len(cands) != 1 || cands[0].OpenItemID != "ok" {
		t.Fatalf("candidates = %+v, want only item ok", cands)
	}
	// 1000 * 0.12 * 54 / 365 (due 2027-01-31 + 5 grace to 2027-03-31) = 17.75
	if !cands[0].Amount.Equal(decimal.RequireFromString("17.75")) || cands[0].Days != 54 {
		t.Errorf("interest = %s over %d days", cands[0].Amount, cands[0].Days)
	}
	reasons := map[string]string{}
	for _, e := range excl {
		reasons[e.OpenItemID] = e.Reason
	}
	if reasons["disp"] != ReasonDispute || reasons["prom"] != ReasonPromise || reasons["tiny"] != ReasonBelowMinimum {
		t.Errorf("exclusions = %v", reasons)
	}
	if _, ok := reasons["fee"]; ok {
		t.Error("fee items must not accrue interest")
	}
}

func TestPlanInterest_ChargesOnlyDaysAfterTheLastNote(t *testing.T) {
	asOf := day("2027-04-30")
	items := []DunnableItem{
		// Due 2027-01-31 + 5 grace; last note calculated to 2027-03-31: 30 new days.
		{ID: "again", CustomerID: "C1", Currency: "USD", DueDate: day("2027-01-31"), OpenAmount: dec("10000.00"), InterestedTo: ptr(day("2027-03-31"))},
		// A note dated before the grace end does not move the start back.
		{ID: "early", CustomerID: "C2", Currency: "USD", DueDate: day("2027-03-31"), OpenAmount: dec("10000.00"), InterestedTo: ptr(day("2027-04-01"))},
		// Already charged up to the calculation date: nothing new.
		{ID: "done", CustomerID: "C3", Currency: "USD", DueDate: day("2027-01-31"), OpenAmount: dec("10000.00"), InterestedTo: ptr(day("2027-04-30"))},
	}
	cands, _ := PlanInterest(asOf, DefaultGraceDays, dec("0.12"), BasisAct365, items)
	days := map[string]int{}
	for _, c := range cands {
		days[c.OpenItemID] = c.Days
	}
	if days["again"] != 30 || days["early"] != 25 {
		t.Errorf("days = %v, want again 30 (from the last note) and early 25 (from due + grace)", days)
	}
	if _, ok := days["done"]; ok {
		t.Error("an item interested to the calculation date must not be charged again")
	}
}
