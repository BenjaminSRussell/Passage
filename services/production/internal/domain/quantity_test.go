package domain

import (
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// AC3: a RAF of 100 units consumes BOM qty × 100 × (1 + scrap%) exactly.
func TestRequiredQty_BackflushIsExact(t *testing.T) {
	tests := []struct {
		name                  string
		qtyPer, series, scrap string
		roundUp               bool
		orderQty, want        string
	}{
		{"whole quantities with 5% scrap", "3", "1", "5", false, "100", "315"},
		{"no scrap", "2.5", "1", "0", false, "100", "250"},
		{"series of 10 divides", "30", "10", "0", false, "100", "300"},
		{"fraction rounds up at 6 places", "1", "3", "0", false, "1", "0.333334"},
		{"round up flag forces a whole unit", "3", "1", "5", true, "100", "315"},
		{"round up flag on a fraction", "1", "3", "0", true, "1", "1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := RequiredQty(d(tt.qtyPer), d(tt.orderQty), d(tt.series), d(tt.scrap), tt.roundUp)
			if err != nil {
				t.Fatal(err)
			}
			if !got.Equal(d(tt.want)) {
				t.Fatalf("want %s, got %s", tt.want, got)
			}
		})
	}
}

func TestRequiredQty_RejectsZeroSeries(t *testing.T) {
	if _, err := RequiredQty(d("1"), d("1"), d("0"), d("0"), false); err == nil {
		t.Fatal("zero series size accepted")
	}
}

func TestRequiredQty_ScrapIsProportional(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		qtyPer := decimal.New(rapid.Int64Range(1, 1000).Draw(t, "qty"), 0)
		orderQty := decimal.New(rapid.Int64Range(1, 500).Draw(t, "order"), 0)
		scrap := decimal.New(rapid.Int64Range(0, 50).Draw(t, "scrap"), 0)
		got, err := RequiredQty(qtyPer, orderQty, decimal.NewFromInt(1), scrap, false)
		if err != nil {
			t.Fatal(err)
		}
		want := qtyPer.Mul(orderQty).Mul(hundred.Add(scrap)).Div(hundred)
		if !got.Equal(want) {
			t.Fatalf("want %s, got %s", want, got)
		}
	})
}

// BR-21.8: scrap compounds backward and rounds up at each operation.
func TestStartQuantities(t *testing.T) {
	tests := []struct {
		name   string
		order  string
		scraps []string
		want   []string
	}{
		{"no scrap", "100", []string{"0", "0"}, []string{"100", "100"}},
		{"scrap on the last operation only", "90", []string{"0", "10"}, []string{"100", "100"}},
		{"scrap on the first operation only", "90", []string{"10", "0"}, []string{"100", "90"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scraps := make([]decimal.Decimal, len(tt.scraps))
			for i, s := range tt.scraps {
				scraps[i] = d(s)
			}
			got, err := StartQuantities(d(tt.order), scraps)
			if err != nil {
				t.Fatal(err)
			}
			for i := range got {
				if !got[i].Equal(d(tt.want[i])) {
					t.Fatalf("op %d: want %s, got %s", i, tt.want[i], got[i])
				}
			}
		})
	}
}

func TestStartQuantities_Compounding(t *testing.T) {
	// Last operation (10% scrap) starts 100 / 0.9 = 111.111111..., rounded up to 111.111112.
	// First operation (5% scrap) starts 111.111112 / 0.95 = 116.959065..., rounded up to 116.959066.
	got, err := StartQuantities(d("100"), []decimal.Decimal{d("5"), d("10")})
	if err != nil {
		t.Fatal(err)
	}
	if !got[1].Equal(d("111.111112")) {
		t.Fatalf("last operation: want 111.111112, got %s", got[1])
	}
	if !got[0].Equal(d("116.959066")) {
		t.Fatalf("first operation: want 116.959066, got %s", got[0])
	}
}

func TestStartQuantities_RejectsFullScrap(t *testing.T) {
	if _, err := StartQuantities(d("10"), []decimal.Decimal{d("100")}); err == nil {
		t.Fatal("scrap of 100% accepted")
	}
}
