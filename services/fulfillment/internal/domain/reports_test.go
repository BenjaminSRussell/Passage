package domain

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func day(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestAgeReturnables_FirstInFirstOut(t *testing.T) {
	asOf := day("2026-10-21")
	jul, sep, oct := RTPShipped{Date: day("2026-07-13"), Qty: d("10")}, RTPShipped{Date: day("2026-09-21"), Qty: d("5")}, RTPShipped{Date: day("2026-10-11"), Qty: d("4")}
	tests := []struct {
		name       string
		balance    string
		shipped    []RTPShipped
		chargeDays int
		oldest     string // "" = no age
		days       int
		due        bool
	}{
		{"newest shipment covers the balance", "4", []RTPShipped{jul, oct}, 90, "2026-10-11", 10, false},
		{"balance reaches into the oldest shipment", "6", []RTPShipped{oct, jul}, 90, "2026-07-13", 100, true},
		{"order of the input does not matter", "9", []RTPShipped{jul, oct, sep}, 90, "2026-09-21", 30, false},
		{"exactly charge days is not yet due", "9", []RTPShipped{oct, sep}, 30, "2026-09-21", 30, false},
		{"one day past charge days is due", "10", []RTPShipped{oct, sep, jul}, 99, "2026-07-13", 100, true},
		{"history shorter than the balance uses the oldest shipment", "50", []RTPShipped{oct, sep}, 30, "2026-09-21", 30, false},
		{"zero balance has no age", "0", []RTPShipped{jul}, 30, "", 0, false},
		{"no shipment known has no age", "3", nil, 30, "", 0, false},
		{"time of day is ignored", "1", []RTPShipped{{Date: day("2026-10-20").Add(23 * time.Hour), Qty: d("1")}}, 30, "2026-10-20", 1, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := AgeReturnables(d(tc.balance), tc.shipped, asOf.Add(5*time.Hour), tc.chargeDays)
			if tc.oldest == "" {
				if got.OldestOutstanding != nil || got.DaysOutstanding != 0 || got.ChargeDue {
					t.Fatalf("got %+v, want no age", got)
				}
				return
			}
			if got.OldestOutstanding == nil || got.OldestOutstanding.Format("2006-01-02") != tc.oldest || got.DaysOutstanding != tc.days || got.ChargeDue != tc.due {
				t.Fatalf("got %+v (oldest %v), want %s, %d days, due %v", got, got.OldestOutstanding, tc.oldest, tc.days, tc.due)
			}
		})
	}
}

// The oldest outstanding shipment is the newest one whose cumulative count,
// newest first, reaches the balance; no newer shipment alone covers it.
func TestAgeReturnables_OldestIsTheFirstShipmentThatCoversTheBalance(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 12).Draw(t, "shipments")
		shipped := make([]RTPShipped, n)
		total := int64(0)
		for i := range shipped {
			q := rapid.Int64Range(1, 20).Draw(t, "qty")
			shipped[i] = RTPShipped{Date: day("2026-01-01").AddDate(0, 0, i*7), Qty: decimal.NewFromInt(q)}
			total += q
		}
		balance := rapid.Int64Range(1, total).Draw(t, "balance")
		got := AgeReturnables(decimal.NewFromInt(balance), shipped, day("2026-12-31"), 90)
		if got.OldestOutstanding == nil {
			t.Fatalf("no age for balance %d of %d shipped", balance, total)
		}
		newer, through := int64(0), int64(0)
		for _, s := range shipped {
			if s.Date.After(*got.OldestOutstanding) {
				newer += s.Qty.IntPart()
			}
			if !s.Date.Before(*got.OldestOutstanding) {
				through += s.Qty.IntPart()
			}
		}
		if newer >= balance || through < balance {
			t.Fatalf("oldest %s: newer shipments hold %d, through it %d, balance %d", got.OldestOutstanding.Format("2006-01-02"), newer, through, balance)
		}
	})
}

func TestBackorder(t *testing.T) {
	tests := []struct {
		status          ShipmentStatus
		planned, picked string
		remainder       string
		open            bool
	}{
		{StatusPicked, "100", "90", "10", true}, // AC-28.4
		{StatusPartiallyShipped, "100", "90", "10", true},
		{StatusPicked, "100", "100", "0", false},
		{StatusCancelled, "100", "90", "10", false},
		{StatusPicking, "1.5", "0", "1.5", true},
	}
	for _, tc := range tests {
		rem, open := Backorder(tc.status, d(tc.planned), d(tc.picked))
		if !rem.Equal(d(tc.remainder)) || open != tc.open {
			t.Errorf("Backorder(%s, %s, %s) = %s, %v; want %s, %v", tc.status, tc.planned, tc.picked, rem, open, tc.remainder, tc.open)
		}
	}
}

// Each held shipment is judged against its own legal entity's threshold: a
// long threshold of the oldest shipment's entity never hides the overdue
// containers of a newer shipment of a short-threshold entity.
func TestAgeReturnables_EachShipmentJudgedByItsOwnChargeDays(t *testing.T) {
	asOf := day("2026-10-21")
	slow := RTPShipped{Date: day("2026-06-01"), Qty: d("5"), ChargeDays: 365}  // 142 days old, threshold 365
	fast := RTPShipped{Date: day("2026-09-01"), Qty: d("5"), ChargeDays: 30}   // 50 days old, threshold 30
	recent := RTPShipped{Date: day("2026-10-01"), Qty: d("5"), ChargeDays: 30} // 20 days old, threshold 30
	tests := []struct {
		name     string
		balance  string
		shipped  []RTPShipped
		wantDays int
		wantAge  int
		wantDue  bool
	}{
		{"only the overdue short-threshold shipment held", "5", []RTPShipped{slow, fast}, 30, 50, true},
		{"older long-threshold shipment held too: the newer one is still overdue", "8", []RTPShipped{slow, fast}, 30, 142, true},
		{"nothing overdue reports the oldest held shipment's threshold", "8", []RTPShipped{slow, recent}, 365, 142, false},
		{"not yet overdue short-threshold shipment", "5", []RTPShipped{slow, recent}, 30, 20, false},
		{"no entity setting takes the given threshold", "5", []RTPShipped{{Date: day("2026-06-01"), Qty: d("5")}}, 90, 142, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := AgeReturnables(d(tc.balance), tc.shipped, asOf, 90)
			if got.ChargeDays != tc.wantDays || got.DaysOutstanding != tc.wantAge || got.ChargeDue != tc.wantDue {
				t.Fatalf("got %+v, want threshold %d, age %d, due %v", got, tc.wantDays, tc.wantAge, tc.wantDue)
			}
		})
	}
}
