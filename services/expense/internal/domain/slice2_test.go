package domain

import (
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func day(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

// trip builds consecutive days from start; each entry says whether the night is
// charged and which meals were provided.
func trip(start time.Time, days ...PerDiemDay) []PerDiemDay {
	for i := range days {
		days[i].Date = start.AddDate(0, 0, i)
	}
	return days
}

func TestComputePerDiem(t *testing.T) {
	usd := PerDiemRate{Currency: "USD", Lodging: amt("150.00"), MealsIncidentals: amt("80.00")}
	start := day(2026, 3, 1)
	tests := []struct {
		name      string
		rate      PerDiemRate
		days      []PerDiemDay
		wantMIE   []string
		wantLodge string
		wantTotal string
	}{
		{"one-day trip is both first and last day: 75 percent",
			usd, trip(start, PerDiemDay{Lodging: true}), []string{"60.00"}, "150.00", "210.00"},
		{"breakfast on a full day removes 20 percent of the rate",
			usd, trip(start, PerDiemDay{Lodging: true}, PerDiemDay{Lodging: true, Meals: MealsProvided{Breakfast: true}}, PerDiemDay{}),
			[]string{"60.00", "64.00", "60.00"}, "300.00", "484.00"},
		{"meals on a partial day are deducted at the full-day share, not 75 percent of it",
			usd, trip(start, PerDiemDay{Lodging: true, Meals: MealsProvided{Breakfast: true}}, PerDiemDay{Meals: MealsProvided{Dinner: true}}),
			[]string{"44.00", "20.00"}, "150.00", "214.00"},
		{"deductions larger than a partial day floor at zero",
			usd, trip(start, PerDiemDay{Lodging: true}, PerDiemDay{Meals: MealsProvided{Lunch: true, Dinner: true}}),
			[]string{"60.00", "0.00"}, "150.00", "210.00"},
		{"all meals provided on a full day pays no M&IE",
			usd, trip(start, PerDiemDay{Lodging: true}, PerDiemDay{Lodging: true, Meals: MealsProvided{Breakfast: true, Lunch: true, Dinner: true}}, PerDiemDay{}),
			[]string{"60.00", "0.00", "60.00"}, "300.00", "420.00"},
		{"zero-decimal currency rounds to whole units",
			PerDiemRate{Currency: "JPY", Lodging: amt("12000"), MealsIncidentals: amt("5001")},
			trip(start, PerDiemDay{Lodging: true}), []string{"3751"}, "12000", "15751"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ComputePerDiem(tc.rate, tc.days)
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Lines) != len(tc.wantMIE) {
				t.Fatalf("lines %d, want %d", len(got.Lines), len(tc.wantMIE))
			}
			sum := decimal.Zero
			for i, want := range tc.wantMIE {
				if !got.Lines[i].MIE.Equal(amt(want)) {
					t.Errorf("day %d M&IE %s, want %s", i+1, got.Lines[i].MIE, want)
				}
				sum = sum.Add(amt(want))
			}
			if !got.MIE.Equal(sum) || !got.Lodging.Equal(amt(tc.wantLodge)) || !got.Total.Equal(amt(tc.wantTotal)) {
				t.Fatalf("got M&IE %s lodging %s total %s; want %s %s %s", got.MIE, got.Lodging, got.Total, sum, tc.wantLodge, tc.wantTotal)
			}
			first, last := got.Lines[0], got.Lines[len(got.Lines)-1]
			if !first.Partial || !last.Partial {
				t.Fatal("first and last day must be partial")
			}
			for i := 1; i < len(got.Lines)-1; i++ {
				if got.Lines[i].Partial {
					t.Fatalf("middle day %s marked partial", got.Lines[i].Date)
				}
			}
		})
	}
}

func TestComputePerDiemRejectsBadDays(t *testing.T) {
	rate := PerDiemRate{Currency: "USD", MealsIncidentals: amt("80")}
	if _, err := ComputePerDiem(rate, nil); !errors.Is(err, ErrNoPerDiemDays) {
		t.Fatalf("no days: got %v, want ErrNoPerDiemDays", err)
	}
	repeated := []PerDiemDay{{Date: day(2026, 3, 1)}, {Date: day(2026, 3, 1)}}
	if _, err := ComputePerDiem(rate, repeated); !errors.Is(err, ErrPerDiemDaysNotConsecutive) {
		t.Fatalf("repeated date: got %v, want ErrPerDiemDaysNotConsecutive", err)
	}
	gap := []PerDiemDay{{Date: day(2026, 3, 1)}, {Date: day(2026, 3, 3)}}
	if _, err := ComputePerDiem(rate, gap); !errors.Is(err, ErrPerDiemDaysNotConsecutive) {
		t.Fatalf("gap: got %v, want ErrPerDiemDaysNotConsecutive", err)
	}
	backwards := []PerDiemDay{{Date: day(2026, 3, 2)}, {Date: day(2026, 3, 1)}}
	if _, err := ComputePerDiem(rate, backwards); !errors.Is(err, ErrPerDiemDaysNotConsecutive) {
		t.Fatalf("out of order: got %v, want ErrPerDiemDaysNotConsecutive", err)
	}
}

func TestPerDiemProperties(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		mie := decimal.New(rapid.Int64Range(0, 100000).Draw(t, "mie-cents"), -2)
		n := rapid.IntRange(1, 30).Draw(t, "days")
		var days []PerDiemDay
		for i := 0; i < n; i++ {
			days = append(days, PerDiemDay{
				Lodging: rapid.Bool().Draw(t, "lodging"),
				Meals: MealsProvided{
					Breakfast: rapid.Bool().Draw(t, "b"),
					Lunch:     rapid.Bool().Draw(t, "l"),
					Dinner:    rapid.Bool().Draw(t, "d"),
				},
			})
		}
		days = trip(day(2026, 1, 1), days...)
		rate := PerDiemRate{Currency: "USD", Lodging: amt("100"), MealsIncidentals: mie}
		res, err := ComputePerDiem(rate, days)
		if err != nil {
			t.Fatal(err)
		}
		if !res.Total.Equal(res.Lodging.Add(res.MIE)) {
			t.Fatalf("total %s != lodging %s + M&IE %s", res.Total, res.Lodging, res.MIE)
		}
		// Removing every provided meal can only raise M&IE, and never above the
		// full rate on each day.
		bare := make([]PerDiemDay, len(days))
		for i, d := range days {
			bare[i] = PerDiemDay{Date: d.Date, Lodging: d.Lodging}
		}
		noMeals, _ := ComputePerDiem(rate, bare)
		for i, l := range res.Lines {
			if l.MIE.IsNegative() || l.MIE.GreaterThan(noMeals.Lines[i].MIE) || noMeals.Lines[i].MIE.GreaterThan(mie) {
				t.Fatalf("day %d M&IE %s outside [0, %s] (rate %s)", i, l.MIE, noMeals.Lines[i].MIE, mie)
			}
			if !l.MIE.Equal(l.MIE.Round(2)) {
				t.Fatalf("day %d M&IE %s not rounded to cents", i, l.MIE)
			}
		}
	})
}

func TestCashAdvanceSettlement(t *testing.T) {
	paid := CashAdvance{ID: "a1", Currency: "USD", Amount: amt("1000"), Paid: amt("1000"),
		Settled: amt("820"), TripEnd: day(2026, 1, 1)}
	if s, err := paid.SettlementStatus(); err != nil || s != AdvancePartiallySettled {
		t.Fatalf("after report of 820: status %s err %v, want PARTIALLY_SETTLED", s, err)
	}
	if !paid.Open().Equal(amt("180")) {
		t.Fatalf("open %s, want 180", paid.Open())
	}
	back, err := paid.Return(amt("180"))
	if err != nil {
		t.Fatal(err)
	}
	if s, _ := back.SettlementStatus(); s != AdvanceSettled {
		t.Fatalf("after return of 180: status %s, want SETTLED", s)
	}
	if _, err := paid.Return(amt("181")); !errors.Is(err, ErrOverReturn) {
		t.Fatalf("return over open: got %v, want ErrOverReturn", err)
	}
	if _, err := paid.Return(amt("0")); err == nil {
		t.Fatal("zero return must be rejected")
	}
	written := paid
	written.WrittenOff = amt("180")
	if s, _ := written.SettlementStatus(); s != AdvanceSettled || !written.Open().IsZero() {
		t.Fatalf("written off: status %s open %s, want SETTLED and 0", s, written.Open())
	}
	over := paid
	over.Returned = amt("500")
	if !over.Open().IsZero() {
		t.Fatalf("open %s must never be negative", over.Open())
	}
}

func TestCashAdvanceNotPaid(t *testing.T) {
	a := CashAdvance{Amount: amt("500"), Paid: amt("0")}
	if _, err := a.SettlementStatus(); !errors.Is(err, ErrAdvanceNotPaid) {
		t.Fatalf("got %v, want ErrAdvanceNotPaid", err)
	}
	if _, err := a.Return(amt("1")); !errors.Is(err, ErrAdvanceNotPaid) {
		t.Fatalf("return before payment: got %v, want ErrAdvanceNotPaid", err)
	}
}

func TestCashAdvanceAging(t *testing.T) {
	open := CashAdvance{Paid: amt("1000"), Settled: amt("820"), TripEnd: day(2026, 1, 1)}
	closed := CashAdvance{Paid: amt("1000"), Settled: amt("1000"), TripEnd: day(2026, 1, 1)}
	tests := []struct {
		name     string
		asOf     time.Time
		advances []CashAdvance
		blocked  bool
	}{
		{"on the due date is not overdue", day(2026, 1, 31), []CashAdvance{open}, false},
		{"day after due date is overdue", day(2026, 2, 1), []CashAdvance{open}, true},
		{"settled advances never block", day(2026, 6, 1), []CashAdvance{closed}, false},
		{"one overdue among settled blocks", day(2026, 6, 1), []CashAdvance{closed, open}, true},
		{"written-off remainder never blocks", day(2026, 6, 1), []CashAdvance{{Paid: amt("1000"), Settled: amt("820"), WrittenOff: amt("180"), TripEnd: day(2026, 1, 1)}}, false},
		{"no advances never block", day(2026, 6, 1), nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := NewAdvancesBlocked(tc.advances, tc.asOf, 30); got != tc.blocked {
				t.Fatalf("blocked %v, want %v", got, tc.blocked)
			}
		})
	}
}

func TestAdvanceTransitions(t *testing.T) {
	tests := []struct {
		from, to AdvanceLifecycle
		ok       bool
	}{
		{AdvanceDraft, AdvanceSubmitted, true},
		{AdvanceSubmitted, AdvanceRejected, true},
		{AdvanceApproved, AdvancePaid, true},
		{AdvancePaid, AdvanceSettled, true},
		{AdvancePartiallySettled, AdvanceSettled, true},
		{AdvanceDraft, AdvancePaid, false},
		{AdvanceSettled, AdvancePaid, false},
		{AdvanceRejected, AdvanceApproved, false},
	}
	for _, tc := range tests {
		if got := CanTransitionAdvance(tc.from, tc.to); got != tc.ok {
			t.Errorf("%s -> %s: got %v want %v", tc.from, tc.to, got, tc.ok)
		}
	}
}

func TestRequisitionWindowAndClose(t *testing.T) {
	w := TripWindow{From: day(2026, 3, 10), To: day(2026, 3, 14)}
	out := OutOfRangeDates(w, []time.Time{day(2026, 3, 9), day(2026, 3, 10), day(2026, 3, 15)})
	if len(out) != 2 || !out[0].Equal(day(2026, 3, 9)) || !out[1].Equal(day(2026, 3, 15)) {
		t.Fatalf("out of range %v", out)
	}
	last := day(2026, 3, 14)
	if got := CloseDate(last); !got.Equal(day(2026, 5, 13)) {
		t.Fatalf("close date %s, want 2026-05-13 (60 days after 2026-03-14)", got)
	}
	if CloseDue(last, day(2026, 5, 12), false) {
		t.Fatal("59 days after last travel is not yet due")
	}
	if !CloseDue(last, day(2026, 5, 13), false) {
		t.Fatal("60 days after last travel the requisition closes")
	}
	if !CloseDue(last, day(2026, 3, 15), true) {
		t.Fatal("a posted linked report closes the requisition")
	}
	if !CanTransitionRequisition(RequisitionSubmitted, RequisitionApproved) {
		t.Fatal("submitted -> approved must be allowed")
	}
	if CanTransitionRequisition(RequisitionDraft, RequisitionApproved) {
		t.Fatal("draft cannot skip submission")
	}
}

const sampleCSV = `card_last4,txn_date,post_date,merchant,mcc,amount,currency
4242,2026-03-02,2026-03-03,ACME AIR,3000,412.50,usd
4242,2026-03-04,2026-03-04,,5812,20.00,USD
4242,2026-03-05,2026-03-06,HOTEL ONE,7011,not-money,USD
9999,2026-03-07,2026-03-07,CAFE,5814,-8.25,EUR
`

func TestParseCardFeedCSV(t *testing.T) {
	feed, err := ParseCardFeed("csv", []byte(sampleCSV))
	if err != nil {
		t.Fatal(err)
	}
	if feed.Format != FeedCSV {
		t.Fatalf("format %q, want CSV", feed.Format)
	}
	if len(feed.Transactions) != 2 {
		t.Fatalf("transactions %d, want 2: %+v", len(feed.Transactions), feed.Transactions)
	}
	first, refund := feed.Transactions[0], feed.Transactions[1]
	if !first.Amount.Equal(amt("412.50")) || first.Currency != "USD" || first.Line != 2 || first.MCC != "3000" {
		t.Fatalf("first txn %+v", first)
	}
	if !refund.Amount.Equal(amt("-8.25")) || refund.Currency != "EUR" || refund.Line != 5 {
		t.Fatalf("refund %+v", refund)
	}
	// Rows are numbered by file line; the header is line 1.
	if len(feed.Rejected) != 2 || feed.Rejected[0].Line != 3 || feed.Rejected[1].Line != 4 {
		t.Fatalf("rejected %+v, want lines 3 (blank merchant) and 4 (bad amount)", feed.Rejected)
	}
	sum := feed.FileHash
	again, _ := ParseCardFeed(FeedCSV, []byte(sampleCSV))
	if again.FileHash != sum || len(sum) != 64 {
		t.Fatal("file hash must be a stable SHA-256 of the content")
	}
	changed, _ := ParseCardFeed(FeedCSV, []byte(sampleCSV+"\n"))
	if changed.FileHash == sum {
		t.Fatal("a different file must hash differently")
	}
}

func TestParseCardFeedCSVKeepsGoingAfterABadRow(t *testing.T) {
	// Line 3 has a stray quote; line 4 is a quoted merchant spanning two file
	// lines; line 6 must still be read.
	data := "\uFEFFcard_last4,txn_date,post_date,merchant,amount,currency\n" +
		"4242,2026-03-02,2026-03-02,CAFE,4.50,USD\n" +
		"4242,2026-03-03,2026-03-03,BAD \"QUOTE,9.00,USD\n" +
		"4242,2026-03-04,2026-03-04,\"TWO\nLINES\",7.00,USD\n" +
		"4242,2026-03-05,2026-03-05,TAXI,12.00,USD\n"
	feed, err := ParseCardFeed(FeedCSV, []byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(feed.Transactions) != 3 {
		t.Fatalf("transactions %+v, want 3", feed.Transactions)
	}
	if feed.Transactions[2].Merchant != "TAXI" || feed.Transactions[2].Line != 6 {
		t.Fatalf("last txn %+v, want TAXI on line 6", feed.Transactions[2])
	}
	if len(feed.Rejected) != 1 || feed.Rejected[0].Line != 3 {
		t.Fatalf("rejected %+v, want the stray quote on line 3", feed.Rejected)
	}
}

func TestParseCardFeedAmountPrecisionFollowsCurrency(t *testing.T) {
	data := "card_last4,txn_date,post_date,merchant,amount,currency\n" +
		"1111,2026-03-02,2026-03-02,SUSHI,1200,JPY\n" +
		"1111,2026-03-02,2026-03-02,SUSHI,1200.50,JPY\n" +
		"1111,2026-03-02,2026-03-02,SOUK,12.345,KWD\n" +
		"1111,2026-03-02,2026-03-02,DINER,12.345,USD\n" +
		"1111,2026-03-02,2026-03-02,DINER,0.00,USD\n" +
		"1111,2026-03-02,2026-03-01,DINER,5.00,USD\n" +
		"1111,2026-03-02,2026-03-02,DINER,5.00,U1D\n"
	feed, err := ParseCardFeed(FeedCSV, []byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(feed.Transactions) != 2 || !feed.Transactions[1].Amount.Equal(amt("12.345")) {
		t.Fatalf("transactions %+v, want JPY 1200 and KWD 12.345 unchanged", feed.Transactions)
	}
	wantLines := []int{3, 5, 6, 7, 8}
	if len(feed.Rejected) != len(wantLines) {
		t.Fatalf("rejected %+v, want lines %v", feed.Rejected, wantLines)
	}
	for i, l := range wantLines {
		if feed.Rejected[i].Line != l {
			t.Fatalf("rejected %+v, want lines %v", feed.Rejected, wantLines)
		}
	}
}

func TestParseCardFeedCSVMissingColumns(t *testing.T) {
	for _, data := range []string{"card_last4,txn_date\n1234,2026-01-01\n", ""} {
		if _, err := ParseCardFeed(FeedCSV, []byte(data)); !errors.Is(err, ErrMissingColumns) {
			t.Fatalf("%q: got %v, want ErrMissingColumns", data, err)
		}
	}
}

const sampleOFX = `OFXHEADER:100
DATA:OFXSGML
<OFX>
<CREDITCARDMSGSRSV1><CCSTMTTRNRS><CCSTMTRS>
<CURDEF>USD
<CCACCTFROM><ACCTID>XXXX-XXXX-XXXX-4242
</CCACCTFROM>
<BANKTRANLIST>
<STMTTRN>
<TRNTYPE>DEBIT
<DTPOSTED>20260303120000[0:GMT]
<DTUSER>20260302
<TRNAMT>-412.50
<FITID>1001
<SIC>3000
<NAME>ACME AIR
<MEMO>TICKET
</STMTTRN>
<STMTTRN>
<TRNTYPE>CREDIT
<DTPOSTED>20260304
<TRNAMT>25.00
<FITID>1002
<NAME>REFUND HOTEL
</STMTTRN>
<STMTTRN>
<TRNTYPE>DEBIT
<DTPOSTED>20260305
<TRNAMT>lots
<NAME>BROKEN
</STMTTRN>
</BANKTRANLIST>
</CCSTMTRS></CCSTMTTRNRS>
<CCSTMTTRNRS><CCSTMTRS>
<CURDEF>EUR
<CCACCTFROM><ACCTID>5555666677778888
</CCACCTFROM>
<BANKTRANLIST>
<STMTTRN>
<TRNTYPE>DEBIT
<DTPOSTED>20260306
<TRNAMT>-30.00
<NAME>BAHN
</STMTTRN>
</BANKTRANLIST>
</CCSTMTRS></CCSTMTTRNRS></CREDITCARDMSGSRSV1>
</OFX>
`

func TestParseCardFeedOFX(t *testing.T) {
	feed, err := ParseCardFeed("ofx", []byte(sampleOFX))
	if err != nil {
		t.Fatal(err)
	}
	if len(feed.Transactions) != 3 {
		t.Fatalf("txns %+v, want 3", feed.Transactions)
	}
	spend := feed.Transactions[0]
	if !spend.Amount.Equal(amt("412.50")) || spend.CardLast4 != "4242" || spend.Currency != "USD" || spend.MCC != "3000" {
		t.Fatalf("spend %+v", spend)
	}
	if spend.TxnDate.Format(dateLayout) != "2026-03-02" || spend.PostDate.Format(dateLayout) != "2026-03-03" {
		t.Fatalf("dates %s / %s, want DTUSER 2026-03-02 and DTPOSTED 2026-03-03",
			spend.TxnDate.Format(dateLayout), spend.PostDate.Format(dateLayout))
	}
	refund := feed.Transactions[1]
	if !refund.Amount.Equal(amt("-25.00")) || refund.TxnDate.Format(dateLayout) != "2026-03-04" {
		t.Fatalf("refund %+v, want -25.00 dated by DTPOSTED", refund)
	}
	second := feed.Transactions[2]
	if second.CardLast4 != "8888" || second.Currency != "EUR" || !second.Amount.Equal(amt("30.00")) {
		t.Fatalf("second statement txn %+v, want card 8888 in EUR", second)
	}
	if len(feed.Rejected) != 1 || feed.Rejected[0].Line != 3 {
		t.Fatalf("rejected %+v, want the third STMTTRN", feed.Rejected)
	}
}

func TestParseCardFeedErrors(t *testing.T) {
	if _, err := ParseCardFeed("VCF", []byte("x")); !errors.Is(err, ErrUnsupportedFormat) {
		t.Fatalf("VCF: got %v, want ErrUnsupportedFormat", err)
	}
	if _, err := ParseCardFeed(FeedOFX, []byte("not an ofx file")); !errors.Is(err, ErrMalformedFeed) {
		t.Fatalf("plain text as OFX: got %v, want ErrMalformedFeed", err)
	}
}
