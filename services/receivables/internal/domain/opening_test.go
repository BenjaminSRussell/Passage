package domain

import (
	"testing"

	"github.com/shopspring/decimal"
)

func goodRow() OpeningRow {
	return OpeningRow{LegacyKey: "BSID-1000-0090001-2026", DocumentNumber: "90001", CustomerID: "11111111-1111-4111-8111-111111111111",
		Kind: "INVOICE", Side: "DEBIT", DocumentDate: "2026-09-01", DueDate: "2026-10-01", Currency: "USD",
		OriginalAmount: decimal.RequireFromString("1000.00"), OpenAmount: decimal.RequireFromString("400.00")}
}

func TestValidateOpeningRow(t *testing.T) {
	if f := ValidateOpeningRow(goodRow()); len(f) != 0 {
		t.Fatalf("good row refused: %v", f)
	}
	cases := map[string]func(*OpeningRow){
		"legacyKey":          func(r *OpeningRow) { r.LegacyKey = " " },
		"documentNumber":     func(r *OpeningRow) { r.DocumentNumber = "" },
		"customerId":         func(r *OpeningRow) { r.CustomerID = "C-1" },
		"kind":               func(r *OpeningRow) { r.Kind = "FEE" },
		"side":               func(r *OpeningRow) { r.Kind, r.Side = "CREDIT_NOTE", "DEBIT" },
		"documentDate":       func(r *OpeningRow) { r.DocumentDate = "01.09.2026" },
		"dueDate":            func(r *OpeningRow) { r.DueDate = "" },
		"currency":           func(r *OpeningRow) { r.Currency = "usd" },
		"originalAmount":     func(r *OpeningRow) { r.OriginalAmount = decimal.RequireFromString("-5") },
		"openAmount":         func(r *OpeningRow) { r.OpenAmount = decimal.RequireFromString("1000.01") },
		"cashDiscountAmount": func(r *OpeningRow) { r.CashDiscountAmount = decimal.RequireFromString("500") },
		"cashDiscountDate":   func(r *OpeningRow) { d := "2026-08-01"; r.CashDiscountDate = &d },
	}
	for field, mutate := range cases {
		r := goodRow()
		mutate(&r)
		if f := ValidateOpeningRow(r); f[field] == "" {
			t.Errorf("%s: want a rule, got %v", field, f)
		}
	}
	zero := goodRow()
	zero.OpenAmount = decimal.Zero
	if ValidateOpeningRow(zero)["openAmount"] == "" {
		t.Error("an item with nothing open must be refused")
	}
	seven := goodRow()
	seven.OpenAmount = decimal.RequireFromString("1.0000001")
	if ValidateOpeningRow(seven)["openAmount"] == "" {
		t.Error("seven decimals must be refused")
	}
}

func TestOpeningSideSetsTheSign(t *testing.T) {
	r := goodRow()
	if a, o := r.Signed(); !a.Equal(decimal.RequireFromString("1000")) || !o.Equal(decimal.RequireFromString("400")) {
		t.Fatalf("debit: %s %s", a, o)
	}
	r.Kind, r.Side = "PAYMENT", "CREDIT"
	if a, o := r.Signed(); !a.Equal(decimal.RequireFromString("-1000")) || !o.Equal(decimal.RequireFromString("-400")) {
		t.Fatalf("credit: %s %s", a, o)
	}
	r.Kind = "ADJUSTMENT"
	if f := ValidateOpeningRow(r); len(f) != 0 {
		t.Fatalf("an adjustment may be a credit: %v", f)
	}
}
