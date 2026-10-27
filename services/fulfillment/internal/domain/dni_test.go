package domain

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func TestDeliveredNotInvoiced_TableDriven(t *testing.T) {
	tests := []struct {
		name string
		rows []DNIRow
		want []DNITotal
	}{
		{"empty", nil, []DNITotal{}},
		{
			"two currencies sorted",
			[]DNIRow{
				{Currency: "USD", Amount: decimal.RequireFromString("1.10")},
				{Currency: "EUR", Amount: decimal.RequireFromString("2.00")},
				{Currency: "USD", Amount: decimal.RequireFromString("0.90")},
			},
			[]DNITotal{
				{Currency: "EUR", Count: 1, Amount: decimal.RequireFromString("2.00")},
				{Currency: "USD", Count: 2, Amount: decimal.RequireFromString("2.00")},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DeliveredNotInvoiced(tt.rows)
			if len(got) != len(tt.want) {
				t.Fatalf("len = %d, want %d (%+v)", len(got), len(tt.want), got)
			}
			for i := range got {
				if got[i].Currency != tt.want[i].Currency || got[i].Count != tt.want[i].Count || !got[i].Amount.Equal(tt.want[i].Amount) {
					t.Fatalf("total[%d] = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestDaysOutstanding(t *testing.T) {
	d := func(s string) time.Time {
		v, err := time.Parse("2006-01-02T15:04:05Z", s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	tests := []struct {
		name    string
		posting time.Time
		asOf    time.Time
		want    int
	}{
		{"same day late clock", d("2026-10-21T23:59:00Z"), d("2026-10-21T00:00:01Z"), 0},
		{"three days", d("2026-10-18T09:00:00Z"), d("2026-10-21T08:00:00Z"), 3},
		{"posting after as-of is zero", d("2026-10-25T00:00:00Z"), d("2026-10-21T00:00:00Z"), 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DaysOutstanding(tt.posting, tt.asOf); got != tt.want {
				t.Fatalf("DaysOutstanding = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestDeliveredNotInvoiced_Property: totals partition the rows (counts sum to the
// row count, amounts sum to the row total) and do not depend on row order.
func TestDeliveredNotInvoiced_Property(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(0, 40).Draw(t, "rows")
		rows := make([]DNIRow, n)
		sum := decimal.Zero
		for i := range rows {
			cents := rapid.Int64Range(0, 1_000_000).Draw(t, "cents")
			cur := rapid.SampledFrom([]string{"EUR", "USD", "GBP"}).Draw(t, "cur")
			rows[i] = DNIRow{Currency: cur, Amount: decimal.New(cents, -2)}
			sum = sum.Add(rows[i].Amount)
		}
		totals := DeliveredNotInvoiced(rows)
		count, amount := 0, decimal.Zero
		for _, tot := range totals {
			count += tot.Count
			amount = amount.Add(tot.Amount)
		}
		if count != n || !amount.Equal(sum) {
			t.Fatalf("count=%d amount=%s, want %d and %s", count, amount, n, sum)
		}
		for i, j := 0, n-1; i < j; i, j = i+1, j-1 {
			rows[i], rows[j] = rows[j], rows[i]
		}
		reversed := DeliveredNotInvoiced(rows)
		if len(reversed) != len(totals) {
			t.Fatalf("order changed the number of totals")
		}
		for i := range totals {
			if reversed[i].Currency != totals[i].Currency || !reversed[i].Amount.Equal(totals[i].Amount) {
				t.Fatalf("order changed total %d: %+v vs %+v", i, reversed[i], totals[i])
			}
		}
	})
}
