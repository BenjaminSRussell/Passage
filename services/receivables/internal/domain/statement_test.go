package domain

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func TestBuildStatement_RunningBalanceGroupingAndCredits(t *testing.T) {
	asOf := time.Date(2027, 3, 31, 0, 0, 0, 0, time.UTC)
	d := func(day int) time.Time { return time.Date(2027, 3, day, 0, 0, 0, 0, time.UTC) }
	due := func(t time.Time) *time.Time { return &t }
	dec := decimal.RequireFromString
	docs := []StatementCandidate{
		{SourceType: "INVOICE", DocumentNumber: "INV-2", DocumentDate: d(10), DueDate: due(d(10).AddDate(0, 0, 30)), Currency: "USD", Amount: dec("500"), OpenAsOf: dec("500")},
		{SourceType: "INVOICE", DocumentNumber: "INV-1", DocumentDate: d(1), DueDate: due(d(1).AddDate(0, 0, -20)), Currency: "USD", Amount: dec("1000"), OpenAsOf: dec("600")},
		{SourceType: "INVOICE", DocumentNumber: "INV-3", DocumentDate: d(5), DueDate: due(d(5).AddDate(0, 0, 30)), Currency: "EUR", Amount: dec("250"), OpenAsOf: dec("250")},
		{SourceType: "INVOICE", DocumentNumber: "INV-0", DocumentDate: d(2), DueDate: due(d(2)), Currency: "USD", Amount: dec("80"), OpenAsOf: dec("0")},
		{SourceType: "PAYMENT", DocumentNumber: "CHK-7", DocumentDate: d(20), Currency: "USD", Amount: dec("-150"), OpenAsOf: dec("-150")},
	}
	got := BuildStatement(asOf, docs)
	if len(got) != 2 || got[0].Currency != "EUR" || got[1].Currency != "USD" {
		t.Fatalf("sections = %+v, want EUR then USD", got)
	}
	usd := got[1]
	want := []struct{ doc, running string }{{"INV-1", "600"}, {"INV-2", "1100"}, {"CHK-7", "950"}}
	if len(usd.Lines) != len(want) {
		t.Fatalf("USD lines = %+v, want %d (the settled INV-0 omitted)", usd.Lines, len(want))
	}
	for i, w := range want {
		if usd.Lines[i].DocumentNumber != w.doc || usd.Lines[i].RunningBalance.String() != w.running {
			t.Errorf("line %d = %s running %s, want %s running %s", i, usd.Lines[i].DocumentNumber, usd.Lines[i].RunningBalance, w.doc, w.running)
		}
	}
	if usd.Lines[2].DueDate != nil {
		t.Errorf("a payment credit has no due date, got %s", *usd.Lines[2].DueDate)
	}
	if usd.ClosingBalance.String() != "950" || got[0].ClosingBalance.String() != "250" {
		t.Errorf("closing = %s / %s, want 950 USD and 250 EUR", usd.ClosingBalance, got[0].ClosingBalance)
	}
	// The aging footer covers debits only: INV-1 is 50 days overdue, INV-2 is current.
	if len(usd.Aging) != 2 || usd.Aging[0].Bucket != BucketCurrent || usd.Aging[0].Amount.String() != "500" ||
		usd.Aging[1].Bucket != Bucket31to60 || usd.Aging[1].Amount.String() != "600" {
		t.Errorf("USD aging = %+v, want CURRENT 500 and 31-60 600", usd.Aging)
	}
}

func TestBuildStatement_EmptyWhenNothingOpen(t *testing.T) {
	if got := BuildStatement(time.Now(), nil); got == nil || len(got) != 0 {
		t.Fatalf("sections = %#v, want an empty list", got)
	}
}

// The closing balance is the sum of the open amounts and equals the last running balance.
func TestBuildStatement_ClosingIsTheSum(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(0, 20).Draw(t, "n")
		asOf := time.Date(2027, 3, 31, 0, 0, 0, 0, time.UTC)
		var docs []StatementCandidate
		sum := decimal.Zero
		for i := 0; i < n; i++ {
			open := decimal.New(rapid.Int64Range(-50000, 50000).Draw(t, "cents"), -2)
			docs = append(docs, StatementCandidate{SourceType: "INVOICE", DocumentNumber: "D", Currency: "USD",
				DocumentDate: asOf.AddDate(0, 0, -rapid.IntRange(0, 90).Draw(t, "age")), Amount: open, OpenAsOf: open})
			sum = sum.Add(open)
		}
		secs := BuildStatement(asOf, docs)
		if sum.IsZero() && len(secs) == 0 {
			return
		}
		got := decimal.Zero
		for _, s := range secs {
			got = got.Add(s.ClosingBalance)
			if len(s.Lines) > 0 && !s.Lines[len(s.Lines)-1].RunningBalance.Equal(s.ClosingBalance) {
				t.Fatalf("last running %s != closing %s", s.Lines[len(s.Lines)-1].RunningBalance, s.ClosingBalance)
			}
		}
		if !got.Equal(sum) {
			t.Fatalf("closing %s, want %s", got, sum)
		}
	})
}
