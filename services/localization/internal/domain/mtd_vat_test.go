package domain

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/shopspring/decimal"
)

func loadFixture(t *testing.T) VATReturn {
	t.Helper()
	raw, err := os.ReadFile("../../../../countrypacks/uk/fixtures/mtd-vat-26a1.json")
	if err != nil {
		t.Fatal(err)
	}
	var r VATReturn
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func dec(s string) *decimal.Decimal {
	d := decimal.RequireFromString(s)
	return &d
}

// AC1 for the UK pack: the statutory fixture validates against the MTD VAT schema with 0 errors.
func TestUKFixtureValidatesWithZeroErrors(t *testing.T) {
	if errs := ValidateMTDVAT(loadFixture(t)); len(errs) != 0 {
		t.Fatalf("fixture has %d errors: %+v", len(errs), errs)
	}
}

// The fixture carries exactly HMRC's request fields, so the UK pack's
// requiredFields and the fixture cannot drift apart.
func TestUKFixtureMatchesPackRequiredFields(t *testing.T) {
	raw, err := os.ReadFile("../../../../countrypacks/uk/fixtures/mtd-vat-26a1.json")
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	packs := loadRepoPacks(t)
	var required []string
	for _, r := range packs["UK"].StatutoryReports {
		if r.Code == "UK_MTD_VAT" {
			required = r.RequiredFields
		}
	}
	if len(required) != len(fields) {
		t.Fatalf("pack requires %d fields, fixture has %d", len(required), len(fields))
	}
	for _, f := range required {
		if _, ok := fields[f]; !ok {
			t.Errorf("fixture lacks required field %s", f)
		}
	}
}

func TestValidateMTDVAT_Table(t *testing.T) {
	no := false
	tests := []struct {
		name   string
		mutate func(*VATReturn)
		field  string
	}{
		{"missing box 7", func(r *VATReturn) { r.TotalValuePurchasesExVAT = nil }, "totalValuePurchasesExVAT"},
		{"three decimals in box 1", func(r *VATReturn) { r.VATDueSales = dec("1000.001") }, "vatDueSales"},
		{"pence in box 6", func(r *VATReturn) { r.TotalValueSalesExVAT = dec("4000.50") }, "totalValueSalesExVAT"},
		{"negative box 5", func(r *VATReturn) { r.NetVATDue = dec("-750.00") }, "netVatDue"},
		{"box 1 out of range", func(r *VATReturn) { r.VATDueSales = dec("10000000000000.00") }, "vatDueSales"},
		{"box 5 out of range", func(r *VATReturn) { r.NetVATDue = dec("100000000000.00") }, "netVatDue"},
		{"box 3 not the sum", func(r *VATReturn) { r.TotalVATDue = dec("999.00") }, "totalVatDue"},
		{"box 5 not the net", func(r *VATReturn) { r.NetVATDue = dec("700.00") }, "netVatDue"},
		{"not finalised", func(r *VATReturn) { r.Finalised = &no }, "finalised"},
		{"finalised missing", func(r *VATReturn) { r.Finalised = nil }, "finalised"},
		{"bad period key", func(r *VATReturn) { r.PeriodKey = "2026-Q1" }, "periodKey"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := loadFixture(t)
			tc.mutate(&r)
			errs := ValidateMTDVAT(r)
			found := false
			for _, e := range errs {
				if e.Field == tc.field {
					found = true
				}
			}
			if !found {
				t.Errorf("no error on %s: %+v", tc.field, errs)
			}
		})
	}
}

// HMRC accepts negative boxes 1 to 4 and 6 to 9 (a period of net refunds or
// returned goods); box 5 is the absolute difference, so a repayment return
// with box 4 above box 3 is valid. The draft rejected both.
func TestValidateMTDVAT_RepaymentReturnIsValid(t *testing.T) {
	yes := true
	r := VATReturn{
		PeriodKey: "#001", VATDueSales: dec("-120.50"), VATDueAcquisitions: dec("20.50"), TotalVATDue: dec("-100.00"),
		VATReclaimedCurrPeriod: dec("400.00"), NetVATDue: dec("500.00"),
		TotalValueSalesExVAT: dec("-600"), TotalValuePurchasesExVAT: dec("2000"), TotalValueGoodsSuppliedExVAT: dec("0"),
		TotalAcquisitionsExVAT: dec("100.00"), Finalised: &yes,
	}
	if errs := ValidateMTDVAT(r); len(errs) != 0 {
		t.Fatalf("repayment return rejected: %+v", errs)
	}
}

// Amounts decode from JSON number text without passing through float64.
func TestVATReturnDecodesExactDecimals(t *testing.T) {
	var r VATReturn
	if err := json.Unmarshal([]byte(`{"vatDueSales": 0.10, "vatDueAcquisitions": 0.20, "totalVatDue": 0.30}`), &r); err != nil {
		t.Fatal(err)
	}
	if !r.TotalVATDue.Equal(r.VATDueSales.Add(*r.VATDueAcquisitions)) {
		t.Fatalf("0.10 + 0.20 != 0.30 after decoding: %s %s %s", r.VATDueSales, r.VATDueAcquisitions, r.TotalVATDue)
	}
}
