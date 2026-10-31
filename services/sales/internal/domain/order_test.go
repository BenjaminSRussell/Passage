package domain

import (
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func TestCanConfirmAndBlocks(t *testing.T) {
	for status, want := range map[string]bool{OrderDraft: true, OrderOpen: true, OrderConfirmed: false, "CANCELLED": false} {
		if got := CanConfirm(status); got != want {
			t.Errorf("CanConfirm(%s) = %v want %v", status, got, want)
		}
	}
	for holdType, want := range map[string]bool{HoldCredit: true, HoldFraud: true, HoldManual: false, HoldPriceReview: false} {
		if got := BlocksConfirm(holdType); got != want {
			t.Errorf("BlocksConfirm(%s) = %v want %v", holdType, got, want)
		}
	}
}

func TestValidateQuantity(t *testing.T) {
	tests := []struct {
		q  string
		ok bool
	}{
		{"1", true}, {"2.5", true}, {"0.000001", true}, {"1.0000000", true},
		{"0", false}, {"-1", false}, {"1.0000001", false},
	}
	for _, tc := range tests {
		err := ValidateQuantity(dec(tc.q))
		if (err == nil) != tc.ok {
			t.Errorf("ValidateQuantity(%s) err=%v want ok=%v", tc.q, err, tc.ok)
		}
	}
}

func TestLineNet(t *testing.T) {
	tests := []struct{ qty, price, disc, want string }{
		{"2", "10.005", "0", "20.01"},
		{"2", "10", "10", "18"},
		{"3", "0.00005", "0", "0.0002"}, // 0.00015 rounds half to even
		{"3", "0.00025", "0", "0.0008"}, // 0.00075 rounds half to even
		{"1.5", "4", "50", "3"},
	}
	for _, tc := range tests {
		got, err := LineNet(dec(tc.qty), dec(tc.price), dec(tc.disc))
		if err != nil {
			t.Fatal(err)
		}
		if !got.Equal(dec(tc.want)) {
			t.Errorf("LineNet(%s,%s,%s) = %s want %s", tc.qty, tc.price, tc.disc, got, tc.want)
		}
	}
	if _, err := LineNet(dec("1"), dec("-1"), dec("0")); err == nil {
		t.Error("negative price must be rejected")
	}
	if _, err := LineNet(dec("1"), dec("1"), dec("101")); err == nil {
		t.Error("discount above 100 must be rejected")
	}
}

func TestReleaseCovers(t *testing.T) {
	tests := []struct {
		current string
		want    bool
	}{{"3000", true}, {"3150", true}, {"3151", false}, {"2000", true}}
	for _, tc := range tests {
		if got := ReleaseCovers(dec("3000"), dec(tc.current)); got != tc.want {
			t.Errorf("ReleaseCovers(3000, %s) = %v want %v", tc.current, got, tc.want)
		}
	}
}

func TestReleaseValidUntil(t *testing.T) {
	at := time.Date(2027, 3, 1, 9, 0, 0, 0, time.UTC)
	if got := ReleaseValidUntil(at, 7); !got.Equal(at.AddDate(0, 0, 7)) {
		t.Fatalf("got %s", got)
	}
}

func TestFormatOrderNumber(t *testing.T) {
	if got := FormatOrderNumber(42); got != "SO-0000042" {
		t.Fatalf("got %s", got)
	}
}

func TestRequestHash_StableAndSensitive(t *testing.T) {
	a, _ := RequestHash(map[string]int{"qty": 1})
	b, _ := RequestHash(map[string]int{"qty": 1})
	c, _ := RequestHash(map[string]int{"qty": 2})
	if a != b || a == c || strings.Contains(a, " ") {
		t.Fatalf("hash not stable or not sensitive: %s %s %s", a, b, c)
	}
}

// TestLineNet_PropertyWithinRange checks that a line net never leaves the band
// implied by its inputs and always has at most four decimals.
func TestLineNet_PropertyWithinRange(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		qty := decimal.New(int64(rapid.IntRange(1, 1000000).Draw(t, "qty")), -6)
		price := decimal.New(int64(rapid.IntRange(0, 10000000).Draw(t, "price")), -4)
		disc := decimal.NewFromInt(int64(rapid.IntRange(0, 100).Draw(t, "disc")))
		net, err := LineNet(qty, price, disc)
		if err != nil {
			t.Fatal(err)
		}
		gross := qty.Mul(price)
		if net.GreaterThan(gross.Add(decimal.New(1, -4))) || net.IsNegative() {
			t.Fatalf("net %s outside [0, gross %s]", net, gross)
		}
		if !net.Round(lineScale).Equal(net) {
			t.Fatalf("net %s has more than four decimals", net)
		}
	})
}
