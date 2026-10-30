package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/payables/internal/domain"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func day(s string) *time.Time {
	t, _ := time.Parse(time.DateOnly, s)
	return &t
}

func TestBuildProposal_GroupsDiscountsAndValuation(t *testing.T) {
	run := *day("2027-03-31")
	groups, err := domain.BuildProposal(domain.ProposalInput{
		RunDate: run, LegalEntityCurrency: "USD", DefaultMethod: "TRANSFER",
		VendorMethods: map[string]string{"v2": "ACH"},
		Rates:         map[string]decimal.Decimal{"EUR": d("1.0825"), "JPY": d("0.0066")},
		Candidates: []domain.ProposalCandidate{
			{ID: "c1", VendorID: "v1", Currency: "USD", OpenAmount: d("1000.00"), DiscountAmount: d("20.00"), DiscountDate: day("2027-03-31")},
			{ID: "c2", VendorID: "v1", Currency: "USD", OpenAmount: d("500.00"), DiscountAmount: d("10.00"), DiscountDate: day("2027-03-30")},
			{ID: "c3", VendorID: "v1", Currency: "EUR", OpenAmount: d("200.00")},
			{ID: "c4", VendorID: "v2", Currency: "USD", OpenAmount: d("300.00"), DiscountAmount: d("6.00"), DiscountDate: day("2027-04-05"), PartiallySettled: true},
			{ID: "c5", VendorID: "v1", Currency: "USD", OpenAmount: d("100.00"), DiscountAmount: d("100.00"), DiscountDate: day("2027-04-05")},
			{ID: "c6", VendorID: "v3", Currency: "JPY", OpenAmount: d("10000")},
			{ID: "c7", VendorID: "v3", Currency: "JPY", OpenAmount: d("12345")},
			{ID: "c8", VendorID: "v1", Currency: "USD", OpenAmount: d("50.00"), DiscountAmount: d("1.00"), DiscountDate: day("2027-04-05")},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	type want struct{ vendor, currency, method, gross, discount, net, valued string }
	wants := []want{
		{"v1", "EUR", "TRANSFER", "200", "0", "200", "216.5"},
		{"v1", "USD", "TRANSFER", "1650", "21", "1629", "1629"},
		{"v2", "USD", "ACH", "300", "0", "300", "300"},
		{"v3", "JPY", "TRANSFER", "22345", "0", "22345", "147.48"},
	}
	if len(groups) != len(wants) {
		t.Fatalf("%d groups, want %d: %+v", len(groups), len(wants), groups)
	}
	for i, w := range wants {
		g := groups[i]
		if g.VendorID != w.vendor || g.Currency != w.currency || g.Method != w.method ||
			!g.Gross.Equal(d(w.gross)) || !g.Discount.Equal(d(w.discount)) || !g.Net.Equal(d(w.net)) || !g.Valued.Equal(d(w.valued)) || g.ID == "" {
			t.Errorf("group %d = %+v, want %+v", i, g, w)
		}
	}
	usd := groups[1].Lines
	if len(usd) != 4 || !usd[0].Pay.Equal(d("980")) || !usd[1].Pay.Equal(d("500")) || !usd[2].Pay.Equal(d("100")) || !usd[3].Pay.Equal(d("49")) {
		t.Fatalf("v1 USD lines = %+v, want pay 980 (window open on the discount date), 500 (window closed), 100 (discount equals the amount), 49", usd)
	}
	jpy := groups[3].Lines
	if !jpy[0].Valued.Equal(d("66")) || !jpy[1].Valued.Equal(d("81.48")) || !jpy[1].Rate.Equal(d("0.0066")) {
		t.Fatalf("v3 JPY lines = %+v, want 66 and 81.48 (12345 x 0.0066 = 81.477 rounded to cents)", jpy)
	}
}

func TestBuildProposal_RoundsEachLineBeforeSumming(t *testing.T) {
	groups, err := domain.BuildProposal(domain.ProposalInput{
		RunDate: *day("2027-03-31"), LegalEntityCurrency: "USD", DefaultMethod: "TRANSFER",
		Rates: map[string]decimal.Decimal{"EUR": d("1.005")},
		Candidates: []domain.ProposalCandidate{
			{ID: "a", VendorID: "v1", Currency: "EUR", OpenAmount: d("0.50")},
			{ID: "b", VendorID: "v1", Currency: "EUR", OpenAmount: d("0.50")},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !groups[0].Valued.Equal(d("1.00")) {
		t.Fatalf("valued = %s, want 1.00 (each 0.5025 rounds to 0.50)", groups[0].Valued)
	}
}

func TestBuildProposal_MissingRateIsRejected(t *testing.T) {
	_, err := domain.BuildProposal(domain.ProposalInput{
		RunDate: *day("2027-03-31"), LegalEntityCurrency: "USD", DefaultMethod: "TRANSFER",
		Candidates: []domain.ProposalCandidate{{ID: "a", VendorID: "v1", Currency: "EUR", OpenAmount: d("10")}},
	})
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != domain.CodeCurrency {
		t.Fatalf("err = %v, want %s", err, domain.CodeCurrency)
	}
}

func TestBuildProposal_SameCurrencyNeedsNoRate(t *testing.T) {
	groups, err := domain.BuildProposal(domain.ProposalInput{
		RunDate: *day("2027-03-31"), LegalEntityCurrency: "USD", DefaultMethod: "CHECK",
		Candidates: []domain.ProposalCandidate{{ID: "a", VendorID: "v1", Currency: "USD", OpenAmount: d("10.50")}},
	})
	if err != nil || len(groups) != 1 || !groups[0].Lines[0].Rate.Equal(d("1")) || !groups[0].Valued.Equal(d("10.50")) {
		t.Fatalf("groups=%+v err=%v", groups, err)
	}
}

func TestBuildProposal_NoCandidatesYieldsNoGroups(t *testing.T) {
	groups, err := domain.BuildProposal(domain.ProposalInput{RunDate: *day("2027-03-31"), LegalEntityCurrency: "USD", DefaultMethod: "ACH"})
	if err != nil || len(groups) != 0 {
		t.Fatalf("groups=%+v err=%v", groups, err)
	}
}

func TestValidPaymentMethod(t *testing.T) {
	for _, m := range []string{"TRANSFER", "ACH", "CHECK", "CARD", "WIRE"} {
		if !domain.ValidPaymentMethod(m) {
			t.Errorf("%s rejected", m)
		}
	}
	for _, m := range []string{"", "transfer", "CASH"} {
		if domain.ValidPaymentMethod(m) {
			t.Errorf("%q accepted", m)
		}
	}
}
