package domain

import (
	"reflect"
	"testing"

	"github.com/shopspring/decimal"
)

func fline(period string, typ TxnType, wbs *string, qty, cost, sales string) ForecastLine {
	return ForecastLine{Period: period, TxnType: typ, WBSCode: wbs, Quantity: qty, CostAmount: cost, SalesAmount: sales}
}

func TestNormalizeForecastLines_NumbersAndRoundsHalfEven(t *testing.T) {
	codes := map[string]bool{"1.1": true}
	got, fields := NormalizeForecastLines([]ForecastLine{
		fline("2026-11", TxnHour, ptr("1.1"), "40", "2000.125", "3200.135"),
		fline("2026-11", TxnOnAccount, nil, "0", "0", "-500"),
	}, "USD", codes)
	if fields != nil {
		t.Fatalf("fields: %v", fields)
	}
	want := []ForecastLine{
		{LineNo: 1, Period: "2026-11", TxnType: TxnHour, WBSCode: ptr("1.1"), Quantity: "40.000", CostAmount: "2000.12", SalesAmount: "3200.14"},
		{LineNo: 2, Period: "2026-11", TxnType: TxnOnAccount, Quantity: "0.000", CostAmount: "0.00", SalesAmount: "-500.00"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
	// JPY has no minor unit.
	jpy, _ := NormalizeForecastLines([]ForecastLine{fline("2026-11", TxnCost, nil, "1", "1000.5", "0")}, "JPY", nil)
	if jpy[0].CostAmount != "1000" {
		t.Fatalf("JPY cost = %s", jpy[0].CostAmount)
	}
}

func TestNormalizeForecastLines_FieldErrors(t *testing.T) {
	codes := map[string]bool{"1.1": true}
	cases := []struct {
		name  string
		line  ForecastLine
		field string
	}{
		{"period not a month", fline("2026-13", TxnHour, nil, "1", "1", "1"), "lines[0].period"},
		{"period with a day", fline("2026-11-01", TxnHour, nil, "1", "1", "1"), "lines[0].period"},
		{"unknown type", fline("2026-11", "TRAVEL", nil, "1", "1", "1"), "lines[0].txnType"},
		{"node not in WORKING", fline("2026-11", TxnHour, ptr("9.9"), "1", "1", "1"), "lines[0].wbsCode"},
		{"negative quantity", fline("2026-11", TxnHour, nil, "-1", "1", "1"), "lines[0].quantity"},
		{"quantity too precise", fline("2026-11", TxnHour, nil, "1.0005", "1", "1"), "lines[0].quantity"},
		{"negative cost", fline("2026-11", TxnHour, nil, "1", "-1", "1"), "lines[0].costAmount"},
		{"cost not a number", fline("2026-11", TxnHour, nil, "1", "ten", "1"), "lines[0].costAmount"},
		{"negative sales off on-account", fline("2026-11", TxnFee, nil, "1", "1", "-1"), "lines[0].salesAmount"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, fields := NormalizeForecastLines([]ForecastLine{tc.line}, "USD", codes)
			if got != nil || fields[tc.field] == "" {
				t.Fatalf("lines %+v, fields %v; want an error on %s", got, fields, tc.field)
			}
		})
	}
	_, fields := NormalizeForecastLines([]ForecastLine{
		fline("2026-11", TxnHour, ptr("1.1"), "1", "1", "1"),
		fline("2026-11", TxnHour, nil, "1", "1", "1"),
		fline("2026-11", TxnHour, ptr("1.1"), "2", "2", "2"),
	}, "USD", codes)
	if len(fields) != 1 || fields["lines[2].period"] == "" {
		t.Fatalf("only the third line repeats a key: %v", fields)
	}
	_, fields = NormalizeForecastLines(make([]ForecastLine, MaxForecastLines+1), "USD", codes)
	if fields["lines"] == "" {
		t.Fatalf("too many lines: %v", fields)
	}
}

func TestCompareForecast_JoinsByPeriodAndTypeWithVariance(t *testing.T) {
	lines := []ForecastLine{
		{Period: "2026-11", TxnType: TxnHour, WBSCode: ptr("1.1"), Quantity: "40.000", CostAmount: "2000.00", SalesAmount: "3200.00"},
		{Period: "2026-11", TxnType: TxnHour, WBSCode: ptr("1.2"), Quantity: "10.000", CostAmount: "500.00", SalesAmount: "800.00"},
		{Period: "2026-12", TxnType: TxnCost, Quantity: "1.000", CostAmount: "300.00", SalesAmount: "330.00"},
	}
	d := decimal.RequireFromString
	actuals := []ActualTotal{
		{Period: "2026-11", TxnType: TxnHour, Quantity: d("52"), CostAmount: d("2600"), SalesAmount: d("4160")},
		{Period: "2026-10", TxnType: TxnFee, Quantity: d("1"), CostAmount: d("0"), SalesAmount: d("250")},
	}
	got := CompareForecast(lines, actuals, "USD")
	want := []ForecastRow{
		{Period: "2026-10", TxnType: TxnFee, ForecastQuantity: "0.000", ForecastCost: "0.00", ForecastSales: "0.00",
			ActualQuantity: "1.000", ActualCost: "0.00", ActualSales: "250.00", QuantityVariance: "1.000", CostVariance: "0.00", SalesVariance: "250.00"},
		{Period: "2026-11", TxnType: TxnHour, ForecastQuantity: "50.000", ForecastCost: "2500.00", ForecastSales: "4000.00",
			ActualQuantity: "52.000", ActualCost: "2600.00", ActualSales: "4160.00", QuantityVariance: "2.000", CostVariance: "100.00", SalesVariance: "160.00"},
		{Period: "2026-12", TxnType: TxnCost, ForecastQuantity: "1.000", ForecastCost: "300.00", ForecastSales: "330.00",
			ActualQuantity: "0.000", ActualCost: "0.00", ActualSales: "0.00", QuantityVariance: "-1.000", CostVariance: "-300.00", SalesVariance: "-330.00"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
	cost, sales := ForecastTotals(lines, "USD")
	if cost != "2800.00" || sales != "4330.00" {
		t.Fatalf("totals %s %s", cost, sales)
	}
}

func TestCompareForecast_TypeOrderWithinAPeriod(t *testing.T) {
	lines := []ForecastLine{
		{Period: "2026-11", TxnType: TxnOnAccount, Quantity: "0", CostAmount: "0", SalesAmount: "-1"},
		{Period: "2026-11", TxnType: TxnHour, Quantity: "1", CostAmount: "1", SalesAmount: "1"},
		{Period: "2026-11", TxnType: TxnItem, Quantity: "1", CostAmount: "1", SalesAmount: "1"},
	}
	var got []TxnType
	for _, r := range CompareForecast(lines, nil, "USD") {
		got = append(got, r.TxnType)
	}
	if !reflect.DeepEqual(got, []TxnType{TxnHour, TxnItem, TxnOnAccount}) {
		t.Fatalf("order %v", got)
	}
}
