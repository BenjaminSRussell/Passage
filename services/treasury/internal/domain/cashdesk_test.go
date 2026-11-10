package domain

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"
)

func dd(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func TestDeskBalance_OpeningPlusReceiptsLessPayments(t *testing.T) {
	got := DeskBalance(dd("100"), []DeskEntry{{DeskReceipt, dd("50.25")}, {DeskPayment, dd("20")}, {DeskPayment, dd("0.25")}})
	if !got.Equal(dd("130")) {
		t.Fatalf("balance = %s, want 130", got)
	}
}

func TestCheckDeskEntry_NoOverdrawAndNoAboveMaximum(t *testing.T) {
	max := dd("1000")
	cases := []struct {
		name    string
		balance string
		e       DeskEntry
		code    string
	}{
		{"payment of everything", "100", DeskEntry{DeskPayment, dd("100")}, ""},
		{"payment one cent too much", "100", DeskEntry{DeskPayment, dd("100.01")}, CodeDeskOverdrawn},
		{"receipt up to the maximum", "900", DeskEntry{DeskReceipt, dd("100")}, ""},
		{"receipt over the maximum", "900", DeskEntry{DeskReceipt, dd("100.01")}, CodeDeskOverLimit},
		{"unknown kind", "0", DeskEntry{"transfer", dd("1")}, "COM-0001"},
	}
	for _, c := range cases {
		err := CheckDeskEntry(dd(c.balance), c.e, max)
		var de *Error
		switch {
		case c.code == "" && err != nil:
			t.Errorf("%s: %v, want nil", c.name, err)
		case c.code != "" && (!errors.As(err, &de) || de.Code != c.code):
			t.Errorf("%s: %v, want %s", c.name, err, c.code)
		}
	}
}

func TestCloseDifference_NeedsAReasonOnlyWhenTheCountDiffers(t *testing.T) {
	if diff, err := CloseDifference(dd("130"), dd("130"), ""); err != nil || !diff.IsZero() {
		t.Fatalf("matching count: %s, %v", diff, err)
	}
	diff, err := CloseDifference(dd("130"), dd("129.50"), "")
	var de *Error
	if !errors.As(err, &de) || de.Code != CodeDeskDifference || !diff.Equal(dd("-0.50")) {
		t.Fatalf("short without reason: %s, %v", diff, err)
	}
	if diff, err := CloseDifference(dd("130"), dd("131"), "found in the safe"); err != nil || !diff.Equal(dd("1")) {
		t.Fatalf("surplus with reason: %s, %v", diff, err)
	}
}
