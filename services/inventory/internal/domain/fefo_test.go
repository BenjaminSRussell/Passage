package domain

import (
	"fmt"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func day(n int) *time.Time {
	t := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, n)
	return &t
}

// TestFEFO100Batches is AC4: 100 batches with distinct expiry dates, 10 of them
// blocked. A 50-unit FEFO reservation takes the earliest non-blocked expiries.
func TestFEFO100Batches(t *testing.T) {
	var batches []BatchStock
	receipt := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 100; i++ {
		// Shuffle insertion order so the result cannot depend on input order.
		idx := (i * 37) % 100
		status := "AVAILABLE"
		if idx < 10 { // the ten earliest expiries are blocked
			status = "BLOCKED"
		}
		batches = append(batches, BatchStock{BatchID: fmt.Sprintf("B%03d", idx), Expiry: day(idx), ReceiptDate: receipt,
			Available: decimal.NewFromInt(1), Status: status})
	}
	sel := SelectBatches(PolicyFEFO, batches, decimal.NewFromInt(50), day(0).AddDate(0, 0, -1), 0)
	if !sel.Shortfall.IsZero() || len(sel.Allocations) != 50 {
		t.Fatalf("allocations=%d shortfall=%s", len(sel.Allocations), sel.Shortfall)
	}
	for i, a := range sel.Allocations {
		if want := fmt.Sprintf("B%03d", i+10); a.BatchID != want {
			t.Fatalf("pick %d = %s, want %s", i, a.BatchID, want)
		}
	}
}

func TestSelectBatches_table(t *testing.T) {
	r1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r2 := r1.AddDate(0, 0, 10)
	batches := []BatchStock{
		{BatchID: "late", Expiry: day(30), ReceiptDate: r1, Available: decimal.NewFromInt(5), Status: "AVAILABLE"},
		{BatchID: "soon", Expiry: day(5), ReceiptDate: r2, Available: decimal.NewFromInt(4), Status: "AVAILABLE"},
		{BatchID: "soon-older", Expiry: day(5), ReceiptDate: r1, Available: decimal.NewFromInt(4), Status: "AVAILABLE"},
		{BatchID: "never", Expiry: nil, ReceiptDate: r1, Available: decimal.NewFromInt(100), Status: "AVAILABLE"},
		{BatchID: "quarantined", Expiry: day(1), ReceiptDate: r1, Available: decimal.NewFromInt(9), Status: "QUARANTINE"},
	}
	req := day(0).AddDate(0, 0, -1)
	tests := []struct {
		name      string
		policy    ReservationPolicy
		qty       int64
		margin    int
		want      []string
		shortfall int64
	}{
		{"FEFO tie-breaks on receipt date", PolicyFEFO, 6, 0, []string{"soon-older", "soon"}, 0},
		{"FEFO margin excludes near expiries", PolicyFEFO, 6, 10, []string{"late", "never"}, 0},
		{"FEFO no-expiry batches sort last", PolicyFEFO, 14, 0, []string{"soon-older", "soon", "late", "never"}, 0},
		{"FEFO shortfall is returned, not an error", PolicyFEFO, 200, 0, []string{"soon-older", "soon", "late", "never"}, 200 - 4 - 4 - 5 - 100},
		{"FIFO by receipt date then id", PolicyFIFO, 6, 0, []string{"late", "never"}, 0},
		{"LIFO newest receipt first", PolicyLIFO, 6, 0, []string{"soon", "late"}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sel := SelectBatches(tt.policy, batches, decimal.NewFromInt(tt.qty), req, tt.margin)
			var got []string
			for _, a := range sel.Allocations {
				got = append(got, a.BatchID)
			}
			if fmt.Sprint(got) != fmt.Sprint(tt.want) {
				t.Fatalf("batches = %v, want %v", got, tt.want)
			}
			if !sel.Shortfall.Equal(decimal.NewFromInt(tt.shortfall)) {
				t.Fatalf("shortfall = %s, want %d", sel.Shortfall, tt.shortfall)
			}
		})
	}
}
