package domain_test

import (
	"errors"
	"testing"

	"github.com/BenjaminSRussell/passage/services/tax/internal/domain"
)

func whRequest(mutate func(*domain.WithholdingRequest)) domain.WithholdingRequest {
	threshold := domain.D("600")
	req := domain.WithholdingRequest{
		Currency: "USD", PartyType: domain.PartyVendor, PaymentDate: domain.Day("2026-06-30"), Code: "NEC",
		GrossAmount: domain.D("1000.00"), PaidToDate: domain.D("0"),
		Codes: []domain.WithholdingCode{
			{Code: "NEC", Jurisdiction: "US", IncomeType: "nonemployee_compensation", Rates: []domain.WithholdingRate{
				{ValidFrom: domain.Day("2024-01-01"), RatePct: domain.D("24"), Threshold: &threshold},
			}},
			{Code: "TDS194C", Jurisdiction: "IN", IncomeType: "contractor", Rates: []domain.WithholdingRate{
				{ValidFrom: domain.Day("2020-01-01"), ValidTo: domain.Day("2025-12-31"), RatePct: domain.D("1")},
				{ValidFrom: domain.Day("2026-01-01"), RatePct: domain.D("2")},
			}},
		},
	}
	if mutate != nil {
		mutate(&req)
	}
	return req
}

func TestCalculateWithholding(t *testing.T) {
	tests := []struct {
		name                            string
		mutate                          func(*domain.WithholdingRequest)
		wantBase, wantWithheld, wantNet string
	}{
		{"excess over threshold on first payment", nil, "400", "96", "904"},
		{"threshold already crossed", func(r *domain.WithholdingRequest) { r.PaidToDate = domain.D("5000") }, "1000", "240", "760"},
		{"below threshold", func(r *domain.WithholdingRequest) { r.GrossAmount = domain.D("500") }, "0", "0", "500"},
		{"cumulative exactly at threshold", func(r *domain.WithholdingRequest) { r.GrossAmount = domain.D("100"); r.PaidToDate = domain.D("500") }, "0", "0", "100"},
		{"one cent over threshold", func(r *domain.WithholdingRequest) { r.GrossAmount = domain.D("100.01"); r.PaidToDate = domain.D("500") }, "0.01", "0", "100.01"},
		{"AC-13.9 550 paid plus 100 invoice over 600", func(r *domain.WithholdingRequest) { r.GrossAmount = domain.D("100"); r.PaidToDate = domain.D("550") }, "50", "12", "88"},
		{"effective-dated rate before change", func(r *domain.WithholdingRequest) {
			r.Code, r.PaymentDate = "TDS194C", domain.Day("2025-12-31")
		}, "1000", "10", "990"},
		{"effective-dated rate after change", func(r *domain.WithholdingRequest) {
			r.Code, r.PaymentDate = "TDS194C", domain.Day("2026-01-01")
		}, "1000", "20", "980"},
		{"rounds to currency minor units", func(r *domain.WithholdingRequest) {
			r.Code, r.GrossAmount = "TDS194C", domain.D("333.33")
			r.PaymentDate = domain.Day("2026-02-01")
		}, "333.33", "6.67", "326.66"},
		{"zero-decimal currency", func(r *domain.WithholdingRequest) {
			r.Currency, r.Code, r.GrossAmount = "JPY", "TDS194C", domain.D("1025")
		}, "1025", "21", "1004"},
		{"customer withholding", func(r *domain.WithholdingRequest) {
			r.PartyType = domain.PartyCustomer
			r.PaidToDate = domain.D("5000")
		}, "1000", "240", "760"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, err := domain.CalculateWithholding(whRequest(tc.mutate))
			if err != nil {
				t.Fatal(err)
			}
			if res.TaxableBase.String() != tc.wantBase || res.Withheld.String() != tc.wantWithheld || res.NetPayable.String() != tc.wantNet {
				t.Fatalf("base %s withheld %s net %s; want %s %s %s", res.TaxableBase, res.Withheld, res.NetPayable, tc.wantBase, tc.wantWithheld, tc.wantNet)
			}
			if len(res.Trace) == 0 {
				t.Error("no determination trace")
			}
		})
	}
}

func TestCalculateWithholding_Errors(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(*domain.WithholdingRequest)
		wantCode string
	}{
		{"unknown code", func(r *domain.WithholdingRequest) { r.Code = "NOPE" }, domain.CodeGroupUnresolved},
		{"no rate on payment date", func(r *domain.WithholdingRequest) { r.PaymentDate = domain.Day("2023-01-01") }, domain.CodeRateNotFound},
		{"rate above 100", func(r *domain.WithholdingRequest) { r.Codes[0].Rates[0].RatePct = domain.D("101") }, domain.CodeRateInvalid},
		{"negative rate", func(r *domain.WithholdingRequest) { r.Codes[0].Rates[0].RatePct = domain.D("-1") }, domain.CodeRateInvalid},
		{"overlapping rates", func(r *domain.WithholdingRequest) {
			r.Code = "TDS194C"
			r.Codes[1].Rates[0].ValidTo = domain.Day("2026-12-31")
		}, domain.CodeRateInvalid},
		{"zero gross", func(r *domain.WithholdingRequest) { r.GrossAmount = domain.D("0") }, domain.CodeValidation},
		{"negative gross", func(r *domain.WithholdingRequest) { r.GrossAmount = domain.D("-5") }, domain.CodeValidation},
		{"negative paid to date", func(r *domain.WithholdingRequest) { r.PaidToDate = domain.D("-1") }, domain.CodeValidation},
		{"bad party type", func(r *domain.WithholdingRequest) { r.PartyType = "employee" }, domain.CodeValidation},
		{"lower-case currency", func(r *domain.WithholdingRequest) { r.Currency = "usd" }, domain.CodeValidation},
		{"missing payment date", func(r *domain.WithholdingRequest) { r.PaymentDate = domain.Date{} }, domain.CodeValidation},
		{"valid_to before valid_from", func(r *domain.WithholdingRequest) { r.Codes[0].Rates[0].ValidTo = domain.Day("2023-01-01") }, domain.CodeValidation},
		{"duplicate code", func(r *domain.WithholdingRequest) { r.Codes = append(r.Codes, r.Codes[0]) }, domain.CodeValidation},
		{"gross with sub-cent decimals", func(r *domain.WithholdingRequest) { r.GrossAmount = domain.D("10.001") }, domain.CodeValidation},
		{"gross with decimals in zero-decimal currency", func(r *domain.WithholdingRequest) { r.Currency, r.GrossAmount = "JPY", domain.D("10.5") }, domain.CodeValidation},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := domain.CalculateWithholding(whRequest(tc.mutate))
			var de *domain.Error
			if !errors.As(err, &de) || de.Code != tc.wantCode {
				t.Fatalf("got %v; want code %s", err, tc.wantCode)
			}
		})
	}
}
