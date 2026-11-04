package domain

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"
)

func TestCheckBOMLines(t *testing.T) {
	ln := func(product, qty, cost string) JournalLine {
		return JournalLine{LineNo: 1, ProductID: product, Qty: decimal.RequireFromString(qty), CostPrice: decimal.RequireFromString(cost)}
	}
	ok := []JournalLine{ln("c1", "-2", "0"), ln("c2", "-1", "3"), ln("p", "1", "0")}
	if err := CheckBOMLines(ok, true); err != nil {
		t.Fatalf("valid BOM: %v", err)
	}
	if err := CheckBOMLines(ok[:2], false); err != nil {
		t.Fatalf("a draft may lack the parent: %v", err)
	}
	for name, lines := range map[string][]JournalLine{
		"no parent":       ok[:2],
		"no component":    ok[2:],
		"two parents":     append(ok[:3:3], ln("q", "1", "0")),
		"costed parent":   {ln("c1", "-1", "0"), ln("p", "1", "1")},
		"parent consumed": {ln("p", "-1", "0"), ln("p", "1", "0")},
	} {
		var de *Error
		if err := CheckBOMLines(lines, true); !errors.As(err, &de) || de.Code != CodeBOMLine {
			t.Fatalf("%s: %v, want %s", name, err, CodeBOMLine)
		}
	}
	for _, p := range PlanPosting(Journal{Type: JournalBOM, Lines: ok}) {
		want := StatusSold
		if p.Line.Qty.IsPositive() {
			want = StatusPurchased
		}
		if p.Status != want {
			t.Fatalf("line %s posts %s, want %s", p.Line.ProductID, p.Status, want)
		}
	}
}
