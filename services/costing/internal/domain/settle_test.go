package domain

import (
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func jan(d int) time.Time { return time.Date(2026, time.January, d, 0, 0, 0, 0, time.UTC) }

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// fr201 is the FR-20.1 example: receipts 100 @ 10.00 (Jan 5) and 100 @ 12.00
// (Jan 20), then an issue of 150 on Jan 25.
func fr201() []Txn {
	return []Txn{
		{ID: "R1", Kind: Receipt, Qty: dec("100"), Value: dec("1000.00"), FinancialDate: jan(5)},
		{ID: "R2", Kind: Receipt, Qty: dec("100"), Value: dec("1200.00"), FinancialDate: jan(20)},
		{ID: "I1", Kind: Issue, Qty: dec("150"), FinancialDate: jan(25)},
	}
}

func TestSettle_FR201Example(t *testing.T) {
	tests := []struct {
		name          string
		method        Method
		wantCost      string
		wantRemaining string
	}{
		{"FIFO", FIFO, "1600.00", "600.00"},
		{"LIFO_DATE", LIFODate, "1700.00", "500.00"},
		{"LIFO same period", LIFO, "1700.00", "500.00"},
		{"WEIGHTED_AVG_PERIODIC", WeightedAvgPeriodic, "1650.00", "550.00"},
		{"WEIGHTED_AVG_RUNNING", WeightedAvgRunning, "1650.00", "550.00"},
		{"MOVING_AVG", MovingAvg, "1650.00", "550.00"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Settle(fr201(), Options{Method: tc.method, Scale: 2})
			if err != nil {
				t.Fatal(err)
			}
			if got := res.IssueCosts["I1"].StringFixed(2); got != tc.wantCost {
				t.Errorf("issue cost = %s, want %s", got, tc.wantCost)
			}
			if got := res.RemainingValue.StringFixed(2); got != tc.wantRemaining {
				t.Errorf("remaining value = %s, want %s", got, tc.wantRemaining)
			}
			if !res.RemainingQty.Equal(dec("50")) {
				t.Errorf("remaining qty = %s, want 50", res.RemainingQty)
			}
			if len(res.Exceptions) != 0 {
				t.Errorf("unexpected exceptions %+v", res.Exceptions)
			}
		})
	}
}

func TestSettle_FIFOSettlementRows(t *testing.T) {
	res, err := Settle(fr201(), Options{Method: FIFO, Scale: 2})
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		receipt string
		qty     string
		value   string
	}{{"R1", "100", "1000.00"}, {"R2", "50", "600.00"}}
	if len(res.Settlements) != len(want) {
		t.Fatalf("settlements = %d, want %d", len(res.Settlements), len(want))
	}
	for i, w := range want {
		s := res.Settlements[i]
		if s.Seq != i+1 || s.ReceiptID != w.receipt || !s.Qty.Equal(dec(w.qty)) || s.Value.StringFixed(2) != w.value {
			t.Errorf("row %d = %+v, want receipt %s qty %s value %s", i, s, w.receipt, w.qty, w.value)
		}
	}
	if len(res.Layers) != 1 || res.Layers[0].ReceiptID != "R2" || !res.Layers[0].Qty.Equal(dec("50")) {
		t.Errorf("layers = %+v, want one layer R2 with 50 left", res.Layers)
	}
}

func TestSettle_LIFOPeriodDiffersFromLIFODate(t *testing.T) {
	// The issue on Jan 10 precedes the Jan 20 receipt. LIFO pools the period, so
	// it takes the newer receipt first (12.00); LIFO_DATE only sees the Jan 5
	// receipt before the issue (10.00).
	txns := []Txn{
		{ID: "R1", Kind: Receipt, Qty: dec("100"), Value: dec("1000.00"), FinancialDate: jan(5)},
		{ID: "I1", Kind: Issue, Qty: dec("50"), FinancialDate: jan(10)},
		{ID: "R2", Kind: Receipt, Qty: dec("100"), Value: dec("1200.00"), FinancialDate: jan(20)},
	}
	period, err := Settle(txns, Options{Method: LIFO, Scale: 2})
	if err != nil {
		t.Fatal(err)
	}
	if got := period.IssueCosts["I1"].StringFixed(2); got != "600.00" {
		t.Errorf("LIFO issue cost = %s, want 600.00 (newest in period at 12.00)", got)
	}
	bydate, err := Settle(txns, Options{Method: LIFODate, Scale: 2})
	if err != nil {
		t.Fatal(err)
	}
	if got := bydate.IssueCosts["I1"].StringFixed(2); got != "500.00" {
		t.Errorf("LIFO_DATE issue cost = %s, want 500.00 (R1 at 10.00)", got)
	}
}

func TestSettle_ShortageFallsBackAndIsListed(t *testing.T) {
	txns := []Txn{
		{ID: "R1", Kind: Receipt, Qty: dec("100"), Value: dec("1000.00"), FinancialDate: jan(5)},
		{ID: "I1", Kind: Issue, Qty: dec("150"), FinancialDate: jan(25)},
	}
	tests := []struct {
		name     string
		opt      Options
		wantCost string
	}{
		{"running average of the stock before the issue", Options{Method: FIFO, Scale: 2}, "1500.00"},
		{"standard fallback", Options{Method: FIFO, Scale: 2, Fallback: FallbackStandard, Standard: dec("9")}, "1450.00"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Settle(txns, tc.opt)
			if err != nil {
				t.Fatal(err)
			}
			if got := res.IssueCosts["I1"].StringFixed(2); got != tc.wantCost {
				t.Errorf("issue cost = %s, want %s", got, tc.wantCost)
			}
			if len(res.Exceptions) != 1 || !res.Exceptions[0].Qty.Equal(dec("50")) {
				t.Errorf("exceptions = %+v, want one of 50 units", res.Exceptions)
			}
		})
	}
}

func TestSettle_FallbackUsesAverageBeforeTheIssue(t *testing.T) {
	// BR-20.3: the shortfall is valued at the running average of the stock on
	// hand before the issue (2,200 / 200 = 11.00), not at the last receipt.
	txns := []Txn{
		{ID: "R1", Kind: Receipt, Qty: dec("100"), Value: dec("1000.00"), FinancialDate: jan(5)},
		{ID: "R2", Kind: Receipt, Qty: dec("100"), Value: dec("1200.00"), FinancialDate: jan(6)},
		{ID: "I1", Kind: Issue, Qty: dec("250"), FinancialDate: jan(25)},
	}
	res, err := Settle(txns, Options{Method: FIFO, Scale: 2})
	if err != nil {
		t.Fatal(err)
	}
	if got := res.IssueCosts["I1"].StringFixed(2); got != "2750.00" {
		t.Errorf("issue cost = %s, want 2750.00 (2,200.00 settled + 50 x 11.00)", got)
	}
	if len(res.Exceptions) != 1 || res.Exceptions[0].Value.StringFixed(2) != "550.00" {
		t.Errorf("exceptions = %+v, want one valued 550.00", res.Exceptions)
	}
}

func TestSettle_FallbackWithoutStockUsesStandardThenLastReceipt(t *testing.T) {
	issueOnly := []Txn{{ID: "I1", Kind: Issue, Qty: dec("10"), FinancialDate: jan(1)}}
	res, err := Settle(issueOnly, Options{Method: FIFO, Scale: 2, Standard: dec("9.00")})
	if err != nil {
		t.Fatal(err)
	}
	if got := res.IssueCosts["I1"].StringFixed(2); got != "90.00" {
		t.Errorf("no stock, standard 9.00: issue cost = %s, want 90.00", got)
	}
	consumed := []Txn{
		{ID: "R1", Kind: Receipt, Qty: dec("10"), Value: dec("120.00"), FinancialDate: jan(1)},
		{ID: "I1", Kind: Issue, Qty: dec("10"), FinancialDate: jan(2)},
		{ID: "I2", Kind: Issue, Qty: dec("5"), FinancialDate: jan(3)},
	}
	res, err = Settle(consumed, Options{Method: WeightedAvgRunning, Scale: 2})
	if err != nil {
		t.Fatal(err)
	}
	if got := res.IssueCosts["I2"].StringFixed(2); got != "60.00" {
		t.Errorf("no stock, no standard: issue cost = %s, want 60.00 (last receipt 12.00)", got)
	}
}

func TestSettle_LaterReceiptSettlesEarlierShortage(t *testing.T) {
	// BR-20.3: the issue on Jan 10 finds only 100 on hand; the receipt on
	// Jan 20 (or, for the periodic average, in February) settles the other 50,
	// so the run ends with no exception and stock of 50 @ 12.00.
	tests := []struct {
		name     string
		method   Method
		r2Date   time.Time
		wantRows int
	}{
		{"FIFO", FIFO, jan(20), 2},
		{"LIFO_DATE", LIFODate, jan(20), 2},
		{"WEIGHTED_AVG_RUNNING", WeightedAvgRunning, jan(20), 2},
		{"WEIGHTED_AVG_PERIODIC next month", WeightedAvgPeriodic, time.Date(2026, time.February, 3, 0, 0, 0, 0, time.UTC), 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			txns := []Txn{
				{ID: "R1", Kind: Receipt, Qty: dec("100"), Value: dec("1000.00"), FinancialDate: jan(5)},
				{ID: "I1", Kind: Issue, Qty: dec("150"), FinancialDate: jan(10)},
				{ID: "R2", Kind: Receipt, Qty: dec("100"), Value: dec("1200.00"), FinancialDate: tc.r2Date},
			}
			res, err := Settle(txns, Options{Method: tc.method, Scale: 2})
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Exceptions) != 0 {
				t.Fatalf("exceptions = %+v, want none", res.Exceptions)
			}
			if got := res.IssueCosts["I1"].StringFixed(2); got != "1600.00" {
				t.Errorf("issue cost = %s, want 1600.00", got)
			}
			if !res.RemainingQty.Equal(dec("50")) || res.RemainingValue.StringFixed(2) != "600.00" {
				t.Errorf("remaining = %s / %s, want 50 / 600.00", res.RemainingQty, res.RemainingValue)
			}
			if len(res.Settlements) != tc.wantRows {
				t.Errorf("settlements = %+v, want %d rows", res.Settlements, tc.wantRows)
			}
		})
	}
}

func TestSettle_LIFOPoolsAcrossPeriods(t *testing.T) {
	// LIFO takes the newest receipt of the issue's own period first, then the
	// newest of earlier periods: 20 from R3 (Feb), then 30 from R2 (Jan 20).
	txns := []Txn{
		{ID: "R1", Kind: Receipt, Qty: dec("100"), Value: dec("1000.00"), FinancialDate: jan(5)},
		{ID: "R2", Kind: Receipt, Qty: dec("100"), Value: dec("1200.00"), FinancialDate: jan(20)},
		{ID: "I1", Kind: Issue, Qty: dec("50"), FinancialDate: time.Date(2026, time.February, 2, 0, 0, 0, 0, time.UTC)},
		{ID: "R3", Kind: Receipt, Qty: dec("20"), Value: dec("300.00"), FinancialDate: time.Date(2026, time.February, 10, 0, 0, 0, 0, time.UTC)},
	}
	res, err := Settle(txns, Options{Method: LIFO, Scale: 2})
	if err != nil {
		t.Fatal(err)
	}
	if got := res.IssueCosts["I1"].StringFixed(2); got != "660.00" {
		t.Errorf("issue cost = %s, want 660.00 (20 x 15.00 + 30 x 12.00)", got)
	}
	if len(res.Settlements) != 2 || res.Settlements[0].ReceiptID != "R3" || res.Settlements[1].ReceiptID != "R2" {
		t.Errorf("settlements = %+v, want R3 then R2", res.Settlements)
	}
}

func TestSettle_StandardCost(t *testing.T) {
	txns := []Txn{
		{ID: "R1", Kind: Receipt, Qty: dec("100"), Value: dec("1000.00"), FinancialDate: jan(5)},
		{ID: "I1", Kind: Issue, Qty: dec("100"), FinancialDate: jan(25)},
	}
	res, err := Settle(txns, Options{Method: Standard, Scale: 2, Standard: dec("10.00")})
	if err != nil {
		t.Fatal(err)
	}
	if got := res.IssueCosts["I1"].StringFixed(2); got != "1000.00" {
		t.Errorf("standard issue cost = %s, want 1000.00", got)
	}
	if res.Settlements[0].Pool != "STANDARD" || !res.RemainingValue.IsZero() {
		t.Errorf("standard settlement = %+v remaining %s", res.Settlements[0], res.RemainingValue)
	}
}

func TestSettle_RoundingResidualIsAbsorbedByLastRow(t *testing.T) {
	// 3 units for 10.00: the unit cost is 3.3333..., so each row rounds to
	// the cent and the last row takes the remainder.
	txns := []Txn{
		{ID: "R1", Kind: Receipt, Qty: dec("3"), Value: dec("10.00"), FinancialDate: jan(1)},
		{ID: "I1", Kind: Issue, Qty: dec("1"), FinancialDate: jan(2)},
		{ID: "I2", Kind: Issue, Qty: dec("2"), FinancialDate: jan(3)},
	}
	res, err := Settle(txns, Options{Method: FIFO, Scale: 2})
	if err != nil {
		t.Fatal(err)
	}
	if got := res.IssueCosts["I1"].StringFixed(2); got != "3.33" {
		t.Errorf("first issue = %s, want 3.33", got)
	}
	if got := res.IssueCosts["I2"].StringFixed(2); got != "6.67" {
		t.Errorf("second issue = %s, want 6.67", got)
	}
	if !res.RemainingValue.IsZero() {
		t.Errorf("remaining value = %s, want 0", res.RemainingValue)
	}
}

func TestSettle_RejectsBadInput(t *testing.T) {
	good := fr201()
	dup := append([]Txn{}, good...)
	dup[1].ID = "R1"
	tests := []struct {
		name string
		txns []Txn
		opt  Options
		code string
	}{
		{"unknown method", good, Options{Method: "HIFO", Scale: 2}, CodeInvalidTxn},
		{"zero quantity", []Txn{{ID: "X", Kind: Issue, Qty: dec("0"), FinancialDate: jan(1)}}, Options{Method: FIFO, Scale: 2}, CodeInvalidTxn},
		{"duplicate id", dup, Options{Method: FIFO, Scale: 2}, CodeDuplicateTxn},
		{"standard without cost", good, Options{Method: Standard, Scale: 2}, CodeInvalidTxn},
		{"standard fallback without cost", good, Options{Method: FIFO, Scale: 2, Fallback: FallbackStandard}, CodeInvalidTxn},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Settle(tc.txns, tc.opt)
			var de *Error
			if !errors.As(err, &de) || de.Code != tc.code {
				t.Fatalf("err = %v, want code %s", err, tc.code)
			}
		})
	}
}

func TestPPV_StandardCostExample(t *testing.T) {
	// AC-20.4: standard 10.00, PO 10.50, invoice 10.80, 100 units.
	if got := ReceiptPPV(dec("100"), dec("10.50"), dec("10.00"), 2); got.StringFixed(2) != "50.00" {
		t.Errorf("receipt PPV = %s, want 50.00", got)
	}
	if got := InvoicePPV(dec("100"), dec("10.80"), dec("10.50"), 2); got.StringFixed(2) != "30.00" {
		t.Errorf("invoice PPV = %s, want 30.00", got)
	}
	if got := ReceiptPPV(dec("100"), dec("9.50"), dec("10.00"), 2); got.StringFixed(2) != "-50.00" {
		t.Errorf("favourable receipt PPV = %s, want -50.00", got)
	}
}

func TestInvoiceGRIR_SplitInvoicesClearExactlyTheReceipt(t *testing.T) {
	// 3 received at PO 0.335 clear round(1.005) = 1.00; invoiced 1 then 2.
	receiptQty, receiptGRIR, po := dec("3"), dec("1.00"), dec("0.335")
	first, err := InvoiceGRIR(receiptQty, receiptGRIR, dec("0"), dec("0"), dec("1"), po, 2)
	if err != nil || first.StringFixed(2) != "0.34" {
		t.Fatalf("first invoice GR/IR = %s, %v, want 0.34", first, err)
	}
	second, err := InvoiceGRIR(receiptQty, receiptGRIR, dec("1"), first, dec("2"), po, 2)
	if err != nil || second.StringFixed(2) != "0.66" || !first.Add(second).Equal(receiptGRIR) {
		t.Fatalf("second invoice GR/IR = %s, %v, want 0.66 so the receipt clears to zero", second, err)
	}
	if got := dec("2").Mul(dec("0.345")).RoundBank(2).Sub(second); got.StringFixed(2) != "0.03" {
		t.Errorf("second invoice PPV = %s, want payable 0.69 less 0.66", got)
	}
	for name, tc := range map[string]struct{ prior, qty, po string }{
		"over-invoiced":  {"1", "3", "0.335"},
		"other PO price": {"0", "1", "0.40"},
	} {
		var de *Error
		if _, err := InvoiceGRIR(receiptQty, receiptGRIR, dec(tc.prior), dec("0.34"), dec(tc.qty), dec(tc.po), 2); !errors.As(err, &de) || de.Code != CodeGRIRAllocation {
			t.Errorf("%s: %v, want %s", name, err, CodeGRIRAllocation)
		}
	}
}

func TestCheckPPVInputs(t *testing.T) {
	if err := CheckPPVInputs(dec("0"), dec("1")); err == nil {
		t.Error("zero quantity accepted")
	}
	if err := CheckPPVInputs(dec("1"), dec("-1")); err == nil {
		t.Error("negative price accepted")
	}
	if err := CheckPPVInputs(dec("1"), dec("0"), dec("2")); err != nil {
		t.Errorf("valid inputs rejected: %v", err)
	}
}
