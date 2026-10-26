package domain

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func TestSettleAdvanceExamples(t *testing.T) {
	// AC-43.9: advance 1,000.00 against a report of 820.00 leaves 180.00 open.
	s := SettleAdvance(amt("1000"), amt("820"))
	if !s.Applied.Equal(amt("820")) || !s.OpenAfter.Equal(amt("180")) || !s.APInvoiceAmount.IsZero() {
		t.Fatalf("820 case = %+v", s)
	}
	// BR-43.9 second example: report 1,150.00 applies 1,000.00 and leaves 150.00 on the AP invoice.
	s = SettleAdvance(amt("1000"), amt("1150"))
	if !s.Applied.Equal(amt("1000")) || !s.OpenAfter.IsZero() || !s.APInvoiceAmount.Equal(amt("150")) {
		t.Fatalf("1150 case = %+v", s)
	}
}

func TestSettleAdvancesAC439(t *testing.T) {
	// AC-43.9: a PAID advance of 1,000.00 and an approved report of 820.00 →
	// 820.00 settles, 180.00 stays open, PARTIALLY_SETTLED; after a recorded
	// return of 180.00 the advance is SETTLED.
	adv := Advance{ID: "EXP-ADV-1", PaidOn: civil(2026, time.March, 1), Paid: amt("1000")}
	s := SettleAdvances([]Advance{adv}, amt("820"))
	if len(s.Applications) != 1 {
		t.Fatalf("applications %+v", s.Applications)
	}
	app := s.Applications[0]
	if !app.Applied.Equal(amt("820")) || !app.OpenAfter.Equal(amt("180")) || app.Status != AdvancePartiallySettled || !s.APInvoiceAmount.IsZero() {
		t.Fatalf("settlement %+v", s)
	}
	if got := AdvanceStatus(adv.Paid, amt("820").Add(amt("180"))); got != AdvanceSettled {
		t.Fatalf("after return status = %s, want SETTLED", got)
	}
	if got := AdvanceStatus(adv.Paid, decimal.Zero); got != AdvancePaid {
		t.Fatalf("untouched advance status = %s, want PAID", got)
	}
}

func TestSettleAdvancesOldestFirst(t *testing.T) {
	advances := []Advance{
		{ID: "B", PaidOn: civil(2026, time.March, 5), Paid: amt("500")},
		{ID: "A", PaidOn: civil(2026, time.March, 1), Paid: amt("400"), Cleared: amt("100")},    // 300 open
		{ID: "C", PaidOn: civil(2026, time.February, 1), Paid: amt("200"), Cleared: amt("200")}, // settled already
	}
	s := SettleAdvances(advances, amt("650"))
	if len(s.Applications) != 2 || s.Applications[0].AdvanceID != "A" || s.Applications[1].AdvanceID != "B" {
		t.Fatalf("order %+v", s.Applications)
	}
	if !s.Applications[0].Applied.Equal(amt("300")) || s.Applications[0].Status != AdvanceSettled {
		t.Errorf("A = %+v", s.Applications[0])
	}
	if !s.Applications[1].Applied.Equal(amt("350")) || !s.Applications[1].OpenAfter.Equal(amt("150")) || s.Applications[1].Status != AdvancePartiallySettled {
		t.Errorf("B = %+v", s.Applications[1])
	}
	if !s.Applied.Equal(amt("650")) || !s.APInvoiceAmount.IsZero() {
		t.Errorf("applied %s AP %s", s.Applied, s.APInvoiceAmount)
	}
	// BR-43.9: a report above the advances leaves the rest on the AP invoice.
	if s := SettleAdvances(advances, amt("1150")); !s.Applied.Equal(amt("800")) || !s.APInvoiceAmount.Equal(amt("350")) {
		t.Errorf("1,150 report applied %s AP %s", s.Applied, s.APInvoiceAmount)
	}
}

func TestSettleAdvanceConservesValue(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		open := decimal.New(rapid.Int64Range(0, 1_000_000).Draw(t, "open"), -2)
		reimb := decimal.New(rapid.Int64Range(0, 1_000_000).Draw(t, "reimb"), -2)
		s := SettleAdvance(open, reimb)
		if !s.Applied.Add(s.APInvoiceAmount).Equal(reimb) {
			t.Fatalf("applied %s + AP %s != reimbursable %s", s.Applied, s.APInvoiceAmount, reimb)
		}
		if !s.Applied.Add(s.OpenAfter).Equal(open) {
			t.Fatalf("applied %s + open %s != advance %s", s.Applied, s.OpenAfter, open)
		}
	})
}

func TestExceedsRequisition(t *testing.T) {
	tol := amt("0.10")
	tests := []struct {
		name              string
		estimated, actual string
		want              bool
	}{
		{"AC-43.10 2,300 over 2,000 at 10%", "2000", "2300", true},
		{"exactly at the ceiling", "2000", "2200", false},
		{"under the estimate", "2000", "1500", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ExceedsRequisition(amt(tc.estimated), amt(tc.actual), tol); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestMatchesCardTransactions(t *testing.T) {
	day := time.Date(2026, time.April, 1, 0, 0, 0, 0, time.UTC)
	card := CardTxn{ID: "c1", WorkerID: "w1", Date: day, Merchant: "Hotel Alpha", Amount: amt("100.00")}
	tol := DefaultTolerance()
	tests := []struct {
		name string
		rcpt Receipt
		want bool
	}{
		{"exact", Receipt{WorkerID: "w1", Date: day, Merchant: "Hotel Alpha", Amount: amt("100.00")}, true},
		{"within 1% and merchant case-insensitive", Receipt{WorkerID: "w1", Date: day.AddDate(0, 0, -1), Merchant: " hotel alpha ", Amount: amt("99.50")}, true},
		{"amount 2% off", Receipt{WorkerID: "w1", Date: day, Merchant: "Hotel Alpha", Amount: amt("98.00")}, false},
		{"three days apart", Receipt{WorkerID: "w1", Date: day.AddDate(0, 0, 3), Merchant: "Hotel Alpha", Amount: amt("100.00")}, false},
		{"another merchant", Receipt{WorkerID: "w1", Date: day, Merchant: "Cafe", Amount: amt("100.00")}, false},
		{"another worker", Receipt{WorkerID: "w2", Date: day, Merchant: "Hotel Alpha", Amount: amt("100.00")}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Matches(card, tc.rcpt, tol); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestMatchReceiptsIsOneToOne(t *testing.T) {
	day := time.Date(2026, time.April, 1, 0, 0, 0, 0, time.UTC)
	cards := []CardTxn{
		{ID: "c2", WorkerID: "w1", Date: day, Merchant: "Cafe", Amount: amt("10.00")},
		{ID: "c1", WorkerID: "w1", Date: day, Merchant: "Cafe", Amount: amt("10.05")},
		{ID: "c3", WorkerID: "w2", Date: day, Merchant: "Cafe", Amount: amt("10.00")},
	}
	receipts := []Receipt{
		{ID: "r1", WorkerID: "w1", Date: day, Merchant: "Cafe", Amount: amt("10.05")},
		{ID: "r2", WorkerID: "w1", Date: day.AddDate(0, 0, 1), Merchant: "Cafe", Amount: amt("10.00")},
	}
	got := MatchReceipts(cards, receipts, DefaultTolerance())
	// c1 (first by ID on the same day) takes its exact receipt r1; c2 then takes
	// r2; c3 belongs to another worker and stays unassigned.
	want := []Pair{{CardID: "c1", ReceiptID: "r1"}, {CardID: "c2", ReceiptID: "r2"}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("pairs %+v, want %+v", got, want)
	}
}

func TestMatchesCountsCalendarDays(t *testing.T) {
	card := CardTxn{WorkerID: "w1", Date: time.Date(2026, time.April, 4, 1, 0, 0, 0, time.UTC), Merchant: "M", Amount: amt("10")}
	rcpt := Receipt{WorkerID: "w1", Date: time.Date(2026, time.April, 1, 23, 0, 0, 0, time.UTC), Merchant: "M", Amount: amt("10")}
	if Matches(card, rcpt, DefaultTolerance()) {
		t.Fatal("receipt three calendar days before the card matched a 2-day window")
	}
}

func TestMatchesNeverCrossesWorkers(t *testing.T) {
	day := time.Date(2026, time.April, 1, 0, 0, 0, 0, time.UTC)
	rapid.Check(t, func(t *rapid.T) {
		cents := rapid.Int64Range(1, 1_000_000).Draw(t, "cents")
		amount := decimal.New(cents, -2)
		card := CardTxn{WorkerID: "worker-a", Date: day, Merchant: "M", Amount: amount}
		rcpt := Receipt{WorkerID: "worker-b", Date: day, Merchant: "M", Amount: amount}
		if Matches(card, rcpt, DefaultTolerance()) {
			t.Fatal("matched across workers")
		}
	})
}
