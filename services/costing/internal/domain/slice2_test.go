package domain

import (
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func domainErrCode(err error) string {
	var de *Error
	if errors.As(err, &de) {
		return de.Code
	}
	return ""
}

// Marking (FR-20.8): the FR-20.1 data with I1 marked to R2 settles R2 first, so
// the 150 issue costs 100 × 12.00 + 50 × 10.00 = 1,700.00 under FIFO.
func TestSettle_MarkingPairsMarkedReceiptFirst(t *testing.T) {
	txns := fr201()
	txns[2].MarkedReceiptID = "R2"
	tests := []struct {
		name          string
		method        Method
		wantCost      string
		wantRemaining string
	}{
		{"FIFO", FIFO, "1700.00", "500.00"},
		{"LIFO_DATE", LIFODate, "1700.00", "500.00"},
		// The marked receipt is already the newest layer, so LIFO is unchanged.
		{"LIFO", LIFO, "1700.00", "500.00"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Settle(txns, Options{Method: tc.method, Scale: 2})
			if err != nil {
				t.Fatal(err)
			}
			if got := res.IssueCosts["I1"].StringFixed(2); got != tc.wantCost {
				t.Errorf("issue cost = %s, want %s", got, tc.wantCost)
			}
			if got := res.RemainingValue.StringFixed(2); got != tc.wantRemaining {
				t.Errorf("remaining = %s, want %s", got, tc.wantRemaining)
			}
		})
	}
}

func TestSettle_MarkingFallsBackWhenMarkedLayerIsEmpty(t *testing.T) {
	txns := []Txn{
		{ID: "R1", Kind: Receipt, Qty: dec("100"), Value: dec("1000.00"), FinancialDate: jan(5)},
		{ID: "R2", Kind: Receipt, Qty: dec("100"), Value: dec("1200.00"), FinancialDate: jan(20)},
		{ID: "I0", Kind: Issue, Qty: dec("100"), FinancialDate: jan(21), MarkedReceiptID: "R2"},
		{ID: "I1", Kind: Issue, Qty: dec("50"), FinancialDate: jan(25)},
	}
	res, err := Settle(txns, Options{Method: FIFO, Scale: 2})
	if err != nil {
		t.Fatal(err)
	}
	// I0 drains R2; I1 then takes the remaining R1 under FIFO: 50 × 10.00.
	if got := res.IssueCosts["I1"].StringFixed(2); got != "500.00" {
		t.Errorf("I1 cost = %s, want 500.00", got)
	}
	if got := res.IssueCosts["I0"].StringFixed(2); got != "1200.00" {
		t.Errorf("I0 cost = %s, want 1200.00", got)
	}
}

func TestSettle_MarkingValidation(t *testing.T) {
	tests := []struct {
		name string
		mark string
		date time.Time
		want string
	}{
		{"unknown receipt", "NOPE", jan(25), CodeInvalidTxn},
		{"receipt after issue", "R2", jan(10), CodeInvalidTxn},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			txns := fr201()
			txns[2].MarkedReceiptID = tc.mark
			txns[2].FinancialDate = tc.date
			_, err := Settle(txns, Options{Method: FIFO, Scale: 2})
			if got := domainErrCode(err); got != tc.want {
				t.Errorf("code = %q (err %v), want %q", got, err, tc.want)
			}
		})
	}
}

// Split valuation (FR-20.16, AC-20.12): DOMESTIC 100 @ 10.00 and IMPORT
// 100 @ 14.00; 50 IMPORT issued under the running average costs 700.00.
func splitFixture() []Txn {
	return []Txn{
		{ID: "D1", Kind: Receipt, Qty: dec("100"), Value: dec("1000.00"), FinancialDate: jan(5), ValuationType: "DOMESTIC"},
		{ID: "M1", Kind: Receipt, Qty: dec("100"), Value: dec("1400.00"), FinancialDate: jan(20), ValuationType: "IMPORT"},
		{ID: "I1", Kind: Issue, Qty: dec("50"), FinancialDate: jan(25), ValuationType: "IMPORT"},
	}
}

func TestSettleByValuationType_AC2012(t *testing.T) {
	merged, perType, err := SettleByValuationType(splitFixture(), Options{Method: WeightedAvgRunning, Scale: 2}, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := merged.IssueCosts["I1"].StringFixed(2); got != "700.00" {
		t.Errorf("issue cost = %s, want 700.00", got)
	}
	if got := perType["DOMESTIC"].RemainingValue.StringFixed(2); got != "1000.00" {
		t.Errorf("DOMESTIC remaining = %s, want 1000.00", got)
	}
	if got := perType["IMPORT"].RemainingValue.StringFixed(2); got != "700.00" {
		t.Errorf("IMPORT remaining = %s, want 700.00", got)
	}
	if got := merged.RemainingValue.StringFixed(2); got != "1700.00" {
		t.Errorf("merged remaining = %s, want 1700.00", got)
	}
}

func TestSettleByValuationType_IssueTypeResolution(t *testing.T) {
	base := func(issueType string) []Txn {
		txns := splitFixture()
		txns[2].ValuationType = issueType
		return txns
	}
	tests := []struct {
		name        string
		txns        []Txn
		defaultType string
		wantCode    string
		wantCost    string
	}{
		{"untyped issue with two types holding stock is refused", base(""), "", CodeValuationTypeRequired, ""},
		{"default type resolves untyped issue", base(""), "DOMESTIC", "", "500.00"},
		{"untyped issue takes the only type with stock", func() []Txn {
			txns := splitFixture()
			txns = append(txns[:1], txns[2:]...)
			txns[1].ValuationType = ""
			return txns
		}(), "", "", "500.00"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			merged, _, err := SettleByValuationType(tc.txns, Options{Method: WeightedAvgRunning, Scale: 2}, tc.defaultType)
			if tc.wantCode != "" {
				if got := domainErrCode(err); got != tc.wantCode {
					t.Fatalf("code = %q (err %v), want %q", got, err, tc.wantCode)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := merged.IssueCosts["I1"].StringFixed(2); got != tc.wantCost {
				t.Errorf("issue cost = %s, want %s", got, tc.wantCost)
			}
		})
	}
}

// With no valuation types anywhere, the split settlement equals plain Settle.
func TestSettleByValuationType_UnsplitEqualsSettle(t *testing.T) {
	for _, m := range []Method{FIFO, LIFODate, WeightedAvgPeriodic, WeightedAvgRunning, MovingAvg} {
		plain, err := Settle(fr201(), Options{Method: m, Scale: 2})
		if err != nil {
			t.Fatal(err)
		}
		merged, perType, err := SettleByValuationType(fr201(), Options{Method: m, Scale: 2}, "")
		if err != nil {
			t.Fatal(err)
		}
		if len(perType) != 1 {
			t.Fatalf("%s: %d partitions, want 1", m, len(perType))
		}
		if !merged.IssueCosts["I1"].Equal(plain.IssueCosts["I1"]) || !merged.RemainingValue.Equal(plain.RemainingValue) {
			t.Errorf("%s: split %v / %s differs from plain %v / %s", m,
				merged.IssueCosts, merged.RemainingValue, plain.IssueCosts, plain.RemainingValue)
		}
	}
}

func TestCanChangeCategory(t *testing.T) {
	tests := []struct {
		qty  string
		want string
	}{
		{"0", ""},
		{"0.000", ""},
		{"1", CodeCategoryLocked},
		{"-3", CodeCategoryLocked},
	}
	for _, tc := range tests {
		err := CanChangeCategory(dec(tc.qty))
		if got := domainErrCode(err); got != tc.want {
			t.Errorf("qty %s: code %q, want %q", tc.qty, got, tc.want)
		}
	}
}

// Parallel valuation ledgers (FR-20.15, AC-20.11): the primary EUR ledger uses
// the periodic average (1,650.00); the USD group ledger at 1.10 uses FIFO
// (1,600.00 × 1.10 = 1,760.00). Each ledger settles its own values.
func TestSettleLedgers_AC2011(t *testing.T) {
	specs := []LedgerSpec{
		{ID: "IFRS", Method: WeightedAvgPeriodic, Currency: "EUR", Scale: 2, Rate: dec("1"), Primary: true},
		{ID: "GROUP", Method: FIFO, Currency: "USD", Scale: 2, Rate: dec("1.10")},
	}
	res, err := SettleLedgers(fr201(), specs)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 {
		t.Fatalf("got %d ledger results", len(res))
	}
	if got := res[0].Result.IssueCosts["I1"].StringFixed(2); got != "1650.00" {
		t.Errorf("primary issue cost = %s, want 1650.00", got)
	}
	if got := res[1].Result.IssueCosts["I1"].StringFixed(2); got != "1760.00" {
		t.Errorf("group issue cost = %s, want 1760.00", got)
	}
	if got := res[1].Result.RemainingValue.StringFixed(2); got != "660.00" {
		t.Errorf("group remaining = %s, want 660.00", got)
	}
	if !res[0].Primary || res[1].Primary || res[1].Currency != "USD" {
		t.Errorf("ledger flags wrong: %+v", res)
	}
}

func TestSettleLedgers_Validation(t *testing.T) {
	primary := LedgerSpec{ID: "P", Method: FIFO, Currency: "EUR", Scale: 2, Rate: dec("1"), Primary: true}
	tests := []struct {
		name  string
		specs []LedgerSpec
	}{
		{"none", nil},
		{"no primary", []LedgerSpec{{ID: "A", Method: FIFO, Currency: "USD", Scale: 2, Rate: dec("1")}}},
		{"two primaries", []LedgerSpec{primary, {ID: "Q", Method: FIFO, Currency: "EUR", Scale: 2, Rate: dec("1"), Primary: true}}},
		{"duplicate id", []LedgerSpec{primary, {ID: "P", Method: FIFO, Currency: "USD", Scale: 2, Rate: dec("1.1")}}},
		{"zero rate", []LedgerSpec{primary, {ID: "G", Method: FIFO, Currency: "USD", Scale: 2, Rate: dec("0")}}},
		{"primary rate not one", []LedgerSpec{{ID: "P", Method: FIFO, Currency: "EUR", Scale: 2, Rate: dec("1.2"), Primary: true}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := SettleLedgers(fr201(), tc.specs)
			if got := domainErrCode(err); got != CodeLedgerInvalid {
				t.Errorf("code = %q (err %v), want %s", got, err, CodeLedgerInvalid)
			}
		})
	}
}

// Aging (FR-20.17, AC-20.13): layers received 400, 100 and 20 days before the
// as-of date with 10, 50 and 100 units at 5.00. The 400-day layer is over 365
// days, so it lands in "> 365"; the AC's "181–365 = 50.00" label does not match
// its own 400-day age, and this test follows the age (see README).
func TestAging_AC2013(t *testing.T) {
	asOf := jan(31)
	layers := []Layer{
		{ReceiptID: "OLD", ReceivedOn: asOf.AddDate(0, 0, -400), Qty: dec("10"), Value: dec("50.00")},
		{ReceiptID: "MID", ReceivedOn: asOf.AddDate(0, 0, -100), Qty: dec("50"), Value: dec("250.00")},
		{ReceiptID: "NEW", ReceivedOn: asOf.AddDate(0, 0, -20), Qty: dec("100"), Value: dec("500.00")},
	}
	rep, err := Aging(layers, asOf, DefaultBuckets())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"0-30": "500.00", "31-90": "0.00", "91-180": "250.00", "181-365": "0.00", "> 365": "50.00"}
	for _, b := range rep.Buckets {
		if got := b.Value.StringFixed(2); got != want[b.Label] {
			t.Errorf("bucket %s = %s, want %s", b.Label, got, want[b.Label])
		}
	}
	if got := rep.TotalValue.StringFixed(2); got != "800.00" {
		t.Errorf("total = %s, want 800.00", got)
	}
}

func TestBucketsFromBounds(t *testing.T) {
	tests := []struct {
		name   string
		bounds []int
		labels []string
		err    bool
	}{
		{"defaults", []int{30, 90, 180, 365}, []string{"0-30", "31-90", "91-180", "181-365", "> 365"}, false},
		{"custom", []int{7}, []string{"0-7", "> 7"}, false},
		{"empty", nil, nil, true},
		{"descending", []int{90, 30}, nil, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b, err := BucketsFromBounds(tc.bounds)
			if (err != nil) != tc.err {
				t.Fatalf("err = %v, want error %v", err, tc.err)
			}
			for i, l := range tc.labels {
				if b[i].Label != l {
					t.Errorf("label %d = %s, want %s", i, b[i].Label, l)
				}
			}
		})
	}
}

func TestAging_BoundaryDays(t *testing.T) {
	asOf := jan(31)
	tests := []struct {
		days  int
		label string
	}{
		{0, "0-30"}, {30, "0-30"}, {31, "31-90"}, {90, "31-90"}, {91, "91-180"},
		{180, "91-180"}, {181, "181-365"}, {365, "181-365"}, {366, "> 365"},
	}
	for _, tc := range tests {
		rep, err := Aging([]Layer{{ReceiptID: "L", ReceivedOn: asOf.AddDate(0, 0, -tc.days), Qty: dec("1"), Value: dec("1")}}, asOf, DefaultBuckets())
		if err != nil {
			t.Fatal(err)
		}
		for _, b := range rep.Buckets {
			if (b.Label == tc.label) != b.Value.IsPositive() {
				t.Errorf("%d days: bucket %s value %s; expected bucket %s", tc.days, b.Label, b.Value, tc.label)
			}
		}
	}
}

func TestAging_RefusesLayerAfterAsOf(t *testing.T) {
	_, err := Aging([]Layer{{ReceiptID: "F", ReceivedOn: jan(40), Qty: dec("1"), Value: dec("1")}}, jan(31), DefaultBuckets())
	if got := domainErrCode(err); got != CodeInvalidTxn {
		t.Errorf("code = %q, want %s", got, CodeInvalidTxn)
	}
}

// AgingLayers reprices FIFO layers to the average so the aging total equals the
// value report's remaining value under running average (FR-20.17).
func TestAgingLayers_AverageTiesToRemainingValue(t *testing.T) {
	layers, _, err := AgingLayers(fr201(), Options{Method: WeightedAvgRunning, Scale: 2})
	if err != nil {
		t.Fatal(err)
	}
	total := decimal.Zero
	for _, l := range layers {
		total = total.Add(l.Value)
	}
	if got := total.StringFixed(2); got != "550.00" {
		t.Errorf("aging total = %s, want 550.00 (running average remaining value)", got)
	}
	rep, err := Aging(layers, jan(31), DefaultBuckets())
	if err != nil {
		t.Fatal(err)
	}
	if got := rep.Buckets[0].Value.StringFixed(2); got != "550.00" {
		t.Errorf("R2 is 11 days old, so the 0-30 bucket = %s, want 550.00", got)
	}
}

// Marking reserves the marked quantity, so an earlier unmarked issue cannot
// consume it (FR-20.8 "pairs marked transactions first"). FIFO without the
// mark would give I0 1,600.00 and I1 600.00.
func TestSettle_MarkingReservesReceiptAgainstEarlierIssues(t *testing.T) {
	txns := []Txn{
		{ID: "R1", Kind: Receipt, Qty: dec("100"), Value: dec("1000.00"), FinancialDate: jan(5)},
		{ID: "R2", Kind: Receipt, Qty: dec("100"), Value: dec("1200.00"), FinancialDate: jan(6)},
		{ID: "I0", Kind: Issue, Qty: dec("150"), FinancialDate: jan(10)},
		{ID: "I1", Kind: Issue, Qty: dec("50"), FinancialDate: jan(12), MarkedReceiptID: "R1"},
	}
	res, err := Settle(txns, Options{Method: FIFO, Scale: 2})
	if err != nil {
		t.Fatal(err)
	}
	if got := res.IssueCosts["I0"].StringFixed(2); got != "1700.00" {
		t.Errorf("I0 = %s, want 1700.00 (50 x 10.00 + 100 x 12.00)", got)
	}
	if got := res.IssueCosts["I1"].StringFixed(2); got != "500.00" {
		t.Errorf("I1 = %s, want 500.00 (50 x 10.00 from the marked R1)", got)
	}
	if !res.RemainingQty.IsZero() {
		t.Errorf("remaining qty = %s, want 0", res.RemainingQty)
	}
}

// Marking applies under the average methods too ("regardless of method"): the
// marked part settles at the marked receipt's cost and leaves the pool; the
// rest is valued at the average of what is left. STANDARD ignores marks.
func TestSettle_MarkingUnderAverageMethods(t *testing.T) {
	txns := fr201()
	txns[2].MarkedReceiptID = "R2"
	tests := []struct {
		method        Method
		wantCost      string
		wantRemaining string
		wantMarkedRow bool
	}{
		// 100 x 12.00 from R2, then 50 of the R1 pool at 10.00.
		{WeightedAvgRunning, "1700.00", "500.00", true},
		{MovingAvg, "1700.00", "500.00", true},
		{WeightedAvgPeriodic, "1700.00", "500.00", true},
		{Standard, "1650.00", "550.00", false},
	}
	for _, tc := range tests {
		t.Run(string(tc.method), func(t *testing.T) {
			res, err := Settle(txns, Options{Method: tc.method, Scale: 2, Standard: dec("11.00")})
			if err != nil {
				t.Fatal(err)
			}
			if got := res.IssueCosts["I1"].StringFixed(2); got != tc.wantCost {
				t.Errorf("issue cost = %s, want %s", got, tc.wantCost)
			}
			if got := res.RemainingValue.StringFixed(2); got != tc.wantRemaining {
				t.Errorf("remaining = %s, want %s", got, tc.wantRemaining)
			}
			marked := false
			for _, s := range res.Settlements {
				marked = marked || (s.ReceiptID == "R2" && s.Pool == "")
			}
			if marked != tc.wantMarkedRow {
				t.Errorf("settlement against R2 present = %v, want %v: %+v", marked, tc.wantMarkedRow, res.Settlements)
			}
		})
	}
}

// A marked issue sequenced before its same-day receipt (ids order them) waits
// for the receipt instead of falling back to the method.
func TestSettle_MarkingSameDayReceiptAfterIssue(t *testing.T) {
	txns := []Txn{
		{ID: "A-R1", Kind: Receipt, Qty: dec("100"), Value: dec("1000.00"), FinancialDate: jan(5)},
		{ID: "B-I1", Kind: Issue, Qty: dec("40"), FinancialDate: jan(9), MarkedReceiptID: "C-R2"},
		{ID: "C-R2", Kind: Receipt, Qty: dec("40"), Value: dec("600.00"), FinancialDate: jan(9)},
	}
	for _, m := range []Method{FIFO, LIFODate, WeightedAvgRunning} {
		res, err := Settle(txns, Options{Method: m, Scale: 2})
		if err != nil {
			t.Fatal(err)
		}
		if got := res.IssueCosts["B-I1"].StringFixed(2); got != "600.00" {
			t.Errorf("%s: issue cost = %s, want 600.00 from C-R2", m, got)
		}
		if len(res.Exceptions) != 0 {
			t.Errorf("%s: exceptions %+v, want none", m, res.Exceptions)
		}
	}
}

// Several issues marking one receipt share it; the last carries the residual
// so the receipt's settlements sum to its value exactly (BR-20.4).
func TestSettle_MarkingSharedReceiptSumsToValue(t *testing.T) {
	txns := []Txn{
		{ID: "R1", Kind: Receipt, Qty: dec("3"), Value: dec("10.00"), FinancialDate: jan(5)},
		{ID: "I1", Kind: Issue, Qty: dec("1"), FinancialDate: jan(6), MarkedReceiptID: "R1"},
		{ID: "I2", Kind: Issue, Qty: dec("1"), FinancialDate: jan(7), MarkedReceiptID: "R1"},
		{ID: "I3", Kind: Issue, Qty: dec("2"), FinancialDate: jan(8), MarkedReceiptID: "R1"},
	}
	res, err := Settle(txns, Options{Method: WeightedAvgRunning, Scale: 2})
	if err != nil {
		t.Fatal(err)
	}
	sum := decimal.Zero
	for _, s := range res.Settlements {
		if s.ReceiptID == "R1" {
			sum = sum.Add(s.Value)
		}
	}
	if got := sum.StringFixed(2); got != "10.00" {
		t.Errorf("R1 settlements sum to %s, want 10.00", got)
	}
	// I3 asked for 2 but only 1 was left on R1: the other unit is an exception.
	if len(res.Exceptions) != 1 || res.Exceptions[0].IssueID != "I3" || !res.Exceptions[0].Qty.Equal(dec("1")) {
		t.Errorf("exceptions = %+v, want 1 unit of I3", res.Exceptions)
	}
}

func TestSettle_MarkOnReceiptRefused(t *testing.T) {
	txns := fr201()
	txns[1].MarkedReceiptID = "R1"
	if _, err := Settle(txns, Options{Method: FIFO, Scale: 2}); domainErrCode(err) != CodeInvalidTxn {
		t.Errorf("err = %v, want %s", err, CodeInvalidTxn)
	}
}

func TestPartitionByValuationType(t *testing.T) {
	t.Run("untyped receipt of a split product is refused", func(t *testing.T) {
		txns := splitFixture()
		txns[0].ValuationType = ""
		if _, _, err := PartitionByValuationType(txns, ""); domainErrCode(err) != CodeValuationTypeRequired {
			t.Errorf("err = %v, want %s", err, CodeValuationTypeRequired)
		}
	})
	t.Run("default type applies to untyped receipts", func(t *testing.T) {
		txns := splitFixture()
		txns[0].ValuationType = ""
		types, parts, err := PartitionByValuationType(txns, "DOMESTIC")
		if err != nil {
			t.Fatal(err)
		}
		if len(types) != 2 || len(parts["DOMESTIC"]) != 1 || len(parts["IMPORT"]) != 2 {
			t.Errorf("types %v parts %v", types, parts)
		}
	})
	t.Run("an untyped marked issue takes its receipt's type", func(t *testing.T) {
		txns := splitFixture()
		txns[2].ValuationType = ""
		txns[2].MarkedReceiptID = "D1"
		merged, perType, err := SettleByValuationType(txns, Options{Method: WeightedAvgRunning, Scale: 2}, "")
		if err != nil {
			t.Fatal(err)
		}
		if got := merged.IssueCosts["I1"].StringFixed(2); got != "500.00" {
			t.Errorf("issue cost = %s, want 500.00 from DOMESTIC", got)
		}
		if got := perType["IMPORT"].RemainingValue.StringFixed(2); got != "1400.00" {
			t.Errorf("IMPORT remaining = %s, want untouched 1400.00", got)
		}
	})
	t.Run("a typed issue marking a receipt of another type is refused", func(t *testing.T) {
		txns := splitFixture()
		txns[2].MarkedReceiptID = "D1"
		if _, _, err := PartitionByValuationType(txns, ""); domainErrCode(err) != CodeInvalidTxn {
			t.Errorf("err = %v, want %s", err, CodeInvalidTxn)
		}
	})
	t.Run("an untyped issue with no stock anywhere and one known type takes it", func(t *testing.T) {
		txns := []Txn{
			{ID: "D1", Kind: Receipt, Qty: dec("10"), Value: dec("100.00"), FinancialDate: jan(5), ValuationType: "DOMESTIC"},
			{ID: "I1", Kind: Issue, Qty: dec("10"), FinancialDate: jan(6)},
			{ID: "I2", Kind: Issue, Qty: dec("5"), FinancialDate: jan(7)},
		}
		_, parts, err := PartitionByValuationType(txns, "")
		if err != nil {
			t.Fatal(err)
		}
		if len(parts["DOMESTIC"]) != 3 {
			t.Errorf("parts = %v, want everything in DOMESTIC", parts)
		}
	})
}

// Aging a split product reprices each type at its own average (AC-20.12 data
// under running average): DOMESTIC 1,000.00 and IMPORT 700.00 tie to 1,700.00.
func TestAgingByValuationType(t *testing.T) {
	total, perType, remaining, err := AgingByValuationType(splitFixture(), Options{Method: WeightedAvgRunning, Scale: 2}, "", jan(31), DefaultBuckets())
	if err != nil {
		t.Fatal(err)
	}
	if got := remaining.StringFixed(2); got != "1700.00" {
		t.Errorf("remaining = %s, want 1700.00", got)
	}
	if !total.TotalValue.Equal(remaining) {
		t.Errorf("total %s does not tie to remaining %s", total.TotalValue, remaining)
	}
	if len(perType) != 2 || perType[0].ValuationType != "DOMESTIC" || perType[1].ValuationType != "IMPORT" {
		t.Fatalf("perType = %+v", perType)
	}
	if got := perType[1].Report.TotalValue.StringFixed(2); got != "700.00" {
		t.Errorf("IMPORT aged value = %s, want 700.00", got)
	}
}

// STANDARD with a STANDARD fallback ages through FIFO layers that need the
// standard unit cost as well; this used to fail with CST-TXN-001.
func TestAgingLayers_StandardWithStandardFallback(t *testing.T) {
	opt := Options{Method: Standard, Scale: 2, Standard: dec("11.00"), Fallback: FallbackStandard}
	layers, res, err := AgingLayers(fr201(), opt)
	if err != nil {
		t.Fatal(err)
	}
	total := decimal.Zero
	for _, l := range layers {
		total = total.Add(l.Value)
	}
	if !total.Equal(res.RemainingValue) || total.StringFixed(2) != "550.00" {
		t.Errorf("aged %s, remaining %s, want both 550.00", total, res.RemainingValue)
	}
}
