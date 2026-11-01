package domain

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestParseShortPickAction(t *testing.T) {
	for _, code := range []string{"KEEP_OPEN", "CANCEL_REMAINDER", "REMAINDER_BACKORDER"} {
		if a, err := ParseShortPickAction(code); err != nil || string(a) != code {
			t.Errorf("%s: %v %v", code, a, err)
		}
	}
	for _, code := range []string{"", "keep_open", "SHRUG", "BACKORDER", " REMAINDER_BACKORDER"} {
		if _, err := ParseShortPickAction(code); err == nil {
			t.Errorf("%q accepted", code)
		}
	}
}

func TestBackorderLines_OnlyLinesThatOweQuantity(t *testing.T) {
	closed := []ClosedQty{{Closed: decimal.Zero}, {Closed: decimal.NewFromInt(4)}, {Closed: decimal.NewFromInt(-1)}, {Closed: decimal.RequireFromString("0.5")}}
	got := BackorderLines(closed)
	if len(got) != 2 || got[0].LineIndex != 1 || !got[0].Qty.Equal(decimal.NewFromInt(4)) || got[1].LineIndex != 3 {
		t.Fatalf("BackorderLines = %+v", got)
	}
	if len(BackorderLines(nil)) != 0 {
		t.Fatal("no lines expected")
	}
}
