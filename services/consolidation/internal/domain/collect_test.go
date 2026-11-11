package domain

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"
)

func asError(err error, target **Error) bool { return errors.As(err, target) }

var testMapping = []AccountMapping{
	{"1000", "CASH", AccountBalanceSheet, ""},
	{"2000", "PAYABLES", AccountBalanceSheet, ""},
	{"4000", "REVENUE", AccountIncome, ""},
}

func TestCollect_TranslatesBalanceSheetAtClosingAndIncomeAtAverage(t *testing.T) {
	units := []UnitLedger{
		{UnitID: "us", Currency: "USD", Rows: []LedgerRow{{"1000", pct("500.00")}, {"4000", pct("-200.00")}}},
		{UnitID: "de", Currency: "EUR", Rows: []LedgerRow{{"1000", pct("100.00")}, {"4000", pct("-50.00")}, {"9999", pct("7.00")}}},
	}
	lines, err := Collect("USD", testMapping, units, map[string]CollectRate{"EUR": {Average: pct("1.08"), Closing: pct("1.10")}})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]decimal.Decimal{}
	for _, l := range lines {
		got[l.UnitID+"/"+l.MainAccount] = l.GroupAmount
	}
	want := map[string]string{"us/1000": "500.00", "us/4000": "-200.00", "de/1000": "110.00", "de/4000": "-54.00", "de/9999": "7.70"}
	for k, v := range want {
		if !got[k].Equal(pct(v)) {
			t.Errorf("%s = %s, want %s", k, got[k], v)
		}
	}
}

func TestCollect_NeedsARateForAForeignUnit(t *testing.T) {
	units := []UnitLedger{{UnitID: "de", Currency: "EUR", Rows: []LedgerRow{{"1000", pct("1")}}}}
	_, err := Collect("USD", testMapping, units, nil)
	var de *Error
	if !asError(err, &de) || de.Code != CodeCollectionError {
		t.Fatalf("want %s, got %v", CodeCollectionError, err)
	}
}

func TestBuildTrialBalance_AggregatesPerAccountAndKeepsUnitContributions(t *testing.T) {
	units := []UnitLedger{
		{UnitID: "us", Currency: "USD", Rows: []LedgerRow{{"1000", pct("500.00")}, {"2000", pct("-500.00")}}},
		{UnitID: "de", Currency: "EUR", Rows: []LedgerRow{{"1000", pct("93.00")}, {"2000", pct("-100.00")}, {"9999", pct("7.00")}}},
	}
	lines, err := Collect("USD", testMapping, units, map[string]CollectRate{"EUR": {Average: pct("1.08"), Closing: pct("1.10")}})
	if err != nil {
		t.Fatal(err)
	}
	tb := BuildTrialBalance(lines)
	if len(tb.Rows) != 2 || tb.Rows[0].Account != "CASH" || tb.Rows[1].Account != "PAYABLES" {
		t.Fatalf("rows = %+v", tb.Rows)
	}
	if !tb.Rows[0].Total.Equal(pct("602.30")) || !tb.Rows[0].Units["de"].Equal(pct("102.30")) {
		t.Errorf("CASH total %s, de %s", tb.Rows[0].Total, tb.Rows[0].Units["de"])
	}
	if !tb.Total.IsZero() || !tb.UnitDifference["de"].IsZero() {
		t.Errorf("balanced ledgers stay balanced when every account takes the closing rate, mapped or not: total %s, de %s", tb.Total, tb.UnitDifference["de"])
	}
	if len(tb.Unmapped) != 1 || tb.Unmapped[0].MainAccount != "9999" {
		t.Errorf("unmapped = %+v", tb.Unmapped)
	}
}

func TestBuildTrialBalance_ShowsTheTranslationDifferenceOfAForeignUnit(t *testing.T) {
	// Assets at the closing rate and income at the average rate no longer net to zero: the difference is the CTA.
	units := []UnitLedger{{UnitID: "de", Currency: "EUR", Rows: []LedgerRow{{"1000", pct("100.00")}, {"4000", pct("-100.00")}}}}
	lines, _ := Collect("USD", testMapping, units, map[string]CollectRate{"EUR": {Average: pct("1.08"), Closing: pct("1.10")}})
	tb := BuildTrialBalance(lines)
	if !tb.UnitDifference["de"].Equal(pct("2.00")) {
		t.Fatalf("translation difference = %s, want 2.00", tb.UnitDifference["de"])
	}
}
