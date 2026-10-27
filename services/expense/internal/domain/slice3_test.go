package domain

import (
	"fmt"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func TestRequisitionRequiredByCategory(t *testing.T) {
	lines := []Line{
		{ID: "A", Category: "AIRFARE", TxnDate: day(2026, 3, 10), Amount: amt("10.00"), HasReceipt: true},
		{ID: "B", Category: "MEALS", TxnDate: day(2026, 3, 10), Amount: amt("10.00")},
		{ID: "C", Category: "airfare", TxnDate: day(2026, 3, 11), Amount: amt("10.00"), HasReceipt: true},
	}
	tests := []struct {
		name           string
		required       []string
		hasRequisition bool
		want           []string // line IDs carrying EXP-TR-002
	}{
		{"no requisition, category required (ignoring case)", []string{"AIRFARE"}, false, []string{"A", "C"}},
		{"linked requisition clears the gate", []string{"AIRFARE"}, true, nil},
		{"category not required", []string{"LODGING"}, false, nil},
		{"nothing required", nil, false, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := Context{SubmitDate: day(2026, 3, 20), RequisitionCategories: tc.required, HasRequisition: tc.hasRequisition}
			var got []string
			for _, r := range EvaluateReport(DefaultRules(), lines, ctx) {
				if r.RuleID != RuleRequisitionRequired {
					continue
				}
				if r.Outcome != OutcomeBlock || r.Code != CodeRequisitionRequired || r.Satisfied {
					t.Fatalf("result %+v, want an unsatisfied BLOCK with %s", r, CodeRequisitionRequired)
				}
				got = append(got, r.LineID)
			}
			if fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Fatalf("flagged %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRequisitionGateBlocksSubmission(t *testing.T) {
	lines := []Line{{ID: "A", Category: "AIRFARE", TxnDate: day(2026, 3, 10), Amount: amt("10.00")}}
	ctx := Context{SubmitDate: day(2026, 3, 20), RequisitionCategories: []string{"AIRFARE"}}
	if Submittable(EvaluateReport(DefaultRules(), lines, ctx)) {
		t.Fatal("a line needing a requisition the report lacks must stop submission")
	}
	ctx.HasRequisition = true
	if !Submittable(EvaluateReport(DefaultRules(), lines, ctx)) {
		t.Fatal("with a linked requisition and no other rule applying, the report is submittable")
	}
}

// The EXP-TR-002 result follows the line's own rule results, so results stay
// grouped by line in input order.
func TestRequisitionResultKeepsLineOrder(t *testing.T) {
	lines := []Line{
		{ID: "A", Category: "AIRFARE", TxnDate: day(2026, 3, 10), Amount: amt("30.00")},
		{ID: "B", Category: "MEALS", TxnDate: day(2026, 3, 10), Amount: amt("5.00")},
	}
	got := EvaluateReport(DefaultRules(), lines, Context{SubmitDate: day(2026, 3, 20), RequisitionCategories: []string{"AIRFARE"}})
	var order []string
	for _, r := range got {
		order = append(order, r.LineID+":"+r.RuleID)
	}
	if want := "[A:EXP-RCPT-025 A:EXP-TR-002]"; fmt.Sprint(order) != want {
		t.Fatalf("order %v, want %s", order, want)
	}
}

func TestCheckLodging(t *testing.T) {
	tests := []struct {
		name          string
		amount        string
		nights        int
		rate          string
		justification string
		wantCap       string
		wantOver      bool
		wantSatisfied bool
	}{
		{"exactly the cap is allowed", "225.00", 1, "150.00", "", "225.00", false, false},
		{"a cent over needs justification", "225.01", 1, "150.00", "", "225.00", true, false},
		{"justified stay over the cap is satisfied", "500.00", 2, "150.00", "Conference hotel", "450.00", true, true},
		{"blank justification does not count", "500.00", 2, "150.00", "   ", "450.00", true, false},
		{"three nights under the cap", "600.00", 3, "150.00", "", "675.00", false, false},
		{"no lodging rate: any charge is over", "1.00", 1, "0", "", "0.00", true, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := CheckLodging("L1", amt(tc.amount), tc.nights, amt(tc.rate), tc.justification)
			if got.Cap.StringFixed(2) != tc.wantCap || got.OverCap != tc.wantOver {
				t.Fatalf("cap %s over %v, want %s %v", got.Cap.StringFixed(2), got.OverCap, tc.wantCap, tc.wantOver)
			}
			if !tc.wantOver {
				if got.Result != nil {
					t.Fatalf("result %+v for a stay within the cap", got.Result)
				}
				return
			}
			r := got.Result
			if r == nil || r.RuleID != RuleLodgingCap || r.Outcome != OutcomeRequireJustification || r.Code != CodeBlock || r.LineID != "L1" {
				t.Fatalf("result %+v", r)
			}
			if r.Satisfied != tc.wantSatisfied {
				t.Fatalf("satisfied %v, want %v", r.Satisfied, tc.wantSatisfied)
			}
		})
	}
}

// Over the cap exactly when the average night costs more than 1.5 × the rate.
func TestCheckLodgingMatchesPerNightComparison(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		rate := decimal.New(rapid.Int64Range(0, 100_000).Draw(t, "rate-cents"), -2)
		nights := rapid.IntRange(1, 60).Draw(t, "nights")
		amount := decimal.New(rapid.Int64Range(0, 10_000_000).Draw(t, "amount-cents"), -2)
		got := CheckLodging("L", amount, nights, rate, "")
		perNight := amount.Div(decimal.NewFromInt(int64(nights)))
		want := perNight.GreaterThan(rate.Mul(decimal.RequireFromString("1.5")))
		if got.OverCap != want {
			t.Fatalf("amount %s nights %d rate %s: over %v, per-night says %v", amount, nights, rate, got.OverCap, want)
		}
		if (got.Result != nil) != got.OverCap {
			t.Fatalf("result %+v with over %v", got.Result, got.OverCap)
		}
	})
}

func TestMileageAmount(t *testing.T) {
	tests := []struct{ distance, rate, want string }{
		{"100", "0.70", "70.00"},  // IRS 2025 rate
		{"250", "0.30", "75.00"},  // BMF per km
		{"12.5", "0.705", "8.81"}, // 8.8125 rounds to 8.81
		{"1", "0.005", "0.01"},    // half rounds up
		{"0.1", "0.70", "0.07"},
	}
	for _, tc := range tests {
		if got := MileageAmount(amt(tc.distance), amt(tc.rate)).StringFixed(2); got != tc.want {
			t.Errorf("MileageAmount(%s, %s) = %s, want %s", tc.distance, tc.rate, got, tc.want)
		}
	}
}

func TestMatchAll(t *testing.T) {
	d := day(2026, 4, 1)
	cards := []CardTxn{
		{ID: "c1", WorkerID: "w1", Date: d, Merchant: "Hotel", Amount: amt("100.00")},
		{ID: "c2", WorkerID: "w1", Date: d, Merchant: "Hotel", Amount: amt("100.00")},
		{ID: "c3", WorkerID: "w2", Date: d, Merchant: "Cafe", Amount: amt("12.00")},
		{ID: "c4", WorkerID: "w1", Date: d, Merchant: "Hotel", Amount: amt("100.00")},
	}
	receipts := []Receipt{
		{ID: "r-far", WorkerID: "w1", Date: d.AddDate(0, 0, 2), Merchant: "Hotel", Amount: amt("99.50")},
		{ID: "r-near", WorkerID: "w1", Date: d, Merchant: "hotel ", Amount: amt("100.00")},
		{ID: "r-cafe", WorkerID: "w1", Date: d, Merchant: "Cafe", Amount: amt("12.00")},
	}
	got := MatchAll(cards, receipts, DefaultTolerance())
	// c1 takes the exact receipt, c2 the one 0.5% and 2 days off; c3 has no
	// receipt of its own worker and c4 finds both Hotel receipts used.
	if want := "[{c1 r-near} {c2 r-far}]"; fmt.Sprint(got.Pairs) != want {
		t.Fatalf("pairs %v, want %s", got.Pairs, want)
	}
	if fmt.Sprint(got.UnmatchedCards) != "[c3 c4]" || fmt.Sprint(got.UnmatchedReceipts) != "[r-cafe]" {
		t.Fatalf("unmatched cards %v receipts %v", got.UnmatchedCards, got.UnmatchedReceipts)
	}
	if got.Rate.String() != "0.5" {
		t.Fatalf("rate %s, want 0.5", got.Rate)
	}
}

func TestMatchAllEmpty(t *testing.T) {
	got := MatchAll(nil, nil, DefaultTolerance())
	if !got.Rate.IsZero() || got.UnmatchedCards == nil || got.UnmatchedReceipts == nil || len(got.Pairs) != 0 {
		t.Fatalf("empty batch %+v", got)
	}
}

// Every card and receipt is either paired exactly once or listed as
// unmatched, no pair crosses workers, and the rate is pairs over cards.
func TestMatchAllProperties(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		workers := []string{"w1", "w2"}
		merchants := []string{"Hotel", "Cafe"}
		d := day(2026, 4, 1)
		n := rapid.IntRange(0, 12).Draw(t, "cards")
		m := rapid.IntRange(0, 12).Draw(t, "receipts")
		cards := make([]CardTxn, n)
		for i := range cards {
			cards[i] = CardTxn{ID: fmt.Sprintf("c%d", i), WorkerID: rapid.SampledFrom(workers).Draw(t, "cw"),
				Date: d.AddDate(0, 0, rapid.IntRange(0, 4).Draw(t, "cd")), Merchant: rapid.SampledFrom(merchants).Draw(t, "cm"),
				Amount: decimal.New(rapid.Int64Range(1000, 1010).Draw(t, "ca"), -2)}
		}
		receipts := make([]Receipt, m)
		for i := range receipts {
			receipts[i] = Receipt{ID: fmt.Sprintf("r%d", i), WorkerID: rapid.SampledFrom(workers).Draw(t, "rw"),
				Date: d.AddDate(0, 0, rapid.IntRange(0, 4).Draw(t, "rd")), Merchant: rapid.SampledFrom(merchants).Draw(t, "rm"),
				Amount: decimal.New(rapid.Int64Range(1000, 1010).Draw(t, "ra"), -2)}
		}
		got := MatchAll(cards, receipts, DefaultTolerance())
		cardWorker := map[string]string{}
		for _, c := range cards {
			cardWorker[c.ID] = c.WorkerID
		}
		receiptWorker := map[string]string{}
		for _, r := range receipts {
			receiptWorker[r.ID] = r.WorkerID
		}
		seenC, seenR := map[string]int{}, map[string]int{}
		for _, p := range got.Pairs {
			seenC[p.CardID]++
			seenR[p.ReceiptID]++
			if cardWorker[p.CardID] != receiptWorker[p.ReceiptID] {
				t.Fatalf("pair %v crosses workers", p)
			}
		}
		for _, id := range got.UnmatchedCards {
			seenC[id]++
		}
		for _, id := range got.UnmatchedReceipts {
			seenR[id]++
		}
		for _, c := range cards {
			if seenC[c.ID] != 1 {
				t.Fatalf("card %s accounted %d times", c.ID, seenC[c.ID])
			}
		}
		for _, r := range receipts {
			if seenR[r.ID] != 1 {
				t.Fatalf("receipt %s accounted %d times", r.ID, seenR[r.ID])
			}
		}
		if n > 0 {
			want := decimal.NewFromInt(int64(len(got.Pairs))).Div(decimal.NewFromInt(int64(n))).Round(4)
			if !got.Rate.Equal(want) {
				t.Fatalf("rate %s, want %s", got.Rate, want)
			}
		}
	})
}
