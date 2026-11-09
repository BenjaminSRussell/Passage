package domain

import (
	"testing"
	"time"
)

func TestDefaultParametersAreValid(t *testing.T) {
	if f := DefaultParameters().Validate(); len(f) != 0 {
		t.Fatalf("defaults must validate: %v", f)
	}
}

func TestParametersValidate(t *testing.T) {
	tests := []struct {
		name  string
		edit  func(*Parameters)
		field string
	}{
		{"quotation validity zero", func(p *Parameters) { p.QuotationExpiryDays = 0 }, "quotationExpiryDays"},
		{"quotation validity over a year", func(p *Parameters) { p.QuotationExpiryDays = 366 }, "quotationExpiryDays"},
		{"release validity over 30", func(p *Parameters) { p.CreditReleaseValidityDays = 31 }, "creditReleaseValidityDays"},
		{"sla zero", func(p *Parameters) { p.CreditHoldSLABusinessDays = 0 }, "creditHoldSlaBusinessDays"},
		{"sla over 10", func(p *Parameters) { p.CreditHoldSLABusinessDays = 11 }, "creditHoldSlaBusinessDays"},
		{"probability over 100", func(p *Parameters) { p.QuotationProbabilityDefault = 101 }, "quotationProbabilityDefault"},
		{"probability negative", func(p *Parameters) { p.QuotationProbabilityDefault = -1 }, "quotationProbabilityDefault"},
		{"unknown order type", func(p *Parameters) { p.DefaultOrderType = "QUOTE" }, "defaultOrderType"},
		{"no check point", func(p *Parameters) { p.CreditCheckPoints = nil }, "creditCheckPoints"},
		{"unknown check point", func(p *Parameters) { p.CreditCheckPoints = []string{"confirm", "ship"} }, "creditCheckPoints"},
		{"repeated check point", func(p *Parameters) { p.CreditCheckPoints = []string{"confirm", "confirm"} }, "creditCheckPoints"},
		{"no blocking check point", func(p *Parameters) { p.CreditCheckPoints = []string{CheckPointEntry, CheckPointRelease} }, "creditCheckPoints"},
		{"packing slip not active", func(p *Parameters) { p.CreditCheckPoints = []string{CheckPointConfirm, CheckPointPackingSlip} }, "creditCheckPoints"},
		{"invoice not active", func(p *Parameters) { p.CreditCheckPoints = []string{CheckPointConfirm, CheckPointInvoice} }, "creditCheckPoints"},
		{"dedup window zero", func(p *Parameters) { p.IntakeDedupWindowDays = 0 }, "intakeDedupWindowDays"},
		{"dedup window over 90 days", func(p *Parameters) { p.IntakeDedupWindowDays = 91 }, "intakeDedupWindowDays"},
		{"returned is not an allowed default", func(p *Parameters) { p.DefaultOrderType = "RETURNED" }, "defaultOrderType"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := DefaultParameters()
			tc.edit(&p)
			if f := p.Validate(); f[tc.field] == "" || len(f) != 1 {
				t.Fatalf("want only %s: %v", tc.field, f)
			}
		})
	}
}

func TestParametersValidate_AcceptsBounds(t *testing.T) {
	p := Parameters{QuotationExpiryDays: 365, QuotationProbabilityDefault: 0, CreditReleaseValidityDays: 30,
		CreditHoldSLABusinessDays: 10, DefaultOrderType: OrderTypeJournal,
		CreditCheckPoints: []string{CheckPointEntry, CheckPointConfirm, CheckPointRelease}, IntakeDedupWindowDays: 90}
	if f := p.Validate(); len(f) != 0 {
		t.Fatalf("upper bounds must validate: %v", f)
	}
	p = Parameters{QuotationExpiryDays: 1, QuotationProbabilityDefault: 100, CreditReleaseValidityDays: 1,
		CreditHoldSLABusinessDays: 1, DefaultOrderType: "SUBSCRIPTION", CreditCheckPoints: []string{CheckPointConfirm}, IntakeDedupWindowDays: 1}
	if f := p.Validate(); len(f) != 0 {
		t.Fatalf("lower bounds must validate: %v", f)
	}
}

func TestAddBusinessDays_SkipsWeekends(t *testing.T) {
	friday := time.Date(2027, 3, 5, 14, 30, 0, 0, time.UTC)
	if got, want := AddBusinessDays(friday, 2), time.Date(2027, 3, 9, 14, 30, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("friday + 2 = %s, want %s", got, want)
	}
	monday := time.Date(2027, 3, 8, 9, 0, 0, 0, time.UTC)
	if got, want := AddBusinessDays(monday, 2), time.Date(2027, 3, 10, 9, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("monday + 2 = %s, want %s", got, want)
	}
	saturday := time.Date(2027, 3, 6, 9, 0, 0, 0, time.UTC)
	if got, want := AddBusinessDays(saturday, 1), time.Date(2027, 3, 8, 9, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("saturday + 1 = %s, want %s", got, want)
	}
}

func TestParametersChecksAt(t *testing.T) {
	p := DefaultParameters()
	if !p.ChecksAt(CheckPointConfirm) || !p.ChecksAt(CheckPointRelease) || p.ChecksAt(CheckPointEntry) {
		t.Fatalf("default points are confirm and release: %v", p.CreditCheckPoints)
	}
}

func TestParametersPartialDeliveryDefault(t *testing.T) {
	p := DefaultParameters()
	if p.PartialDeliveryDefault != PartialAllowed || len(p.Validate()) != 0 {
		t.Fatalf("default: %+v %v", p, p.Validate())
	}
	p.PartialDeliveryDefault = PartialOneShipment
	if len(p.Validate()) != 0 {
		t.Fatalf("ONE_SHIPMENT is allowed: %v", p.Validate())
	}
	p.PartialDeliveryDefault = "SOMETIMES"
	if p.Validate()["partialDeliveryDefault"] == "" {
		t.Fatal("an unknown policy must be refused")
	}
}
