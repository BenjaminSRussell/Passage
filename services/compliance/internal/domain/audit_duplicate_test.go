package domain

import (
	"fmt"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

var invDate = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

func inv(id, vendor, number, amount string, dayOffset int) Invoice {
	return Invoice{
		ID: id, VendorID: vendor, Number: number, Amount: decimal.RequireFromString(amount),
		Currency: "USD", InvoiceDate: invDate.AddDate(0, 0, dayOffset),
	}
}

func TestNormalizeInvoiceNumber(t *testing.T) {
	tests := map[string]string{
		"INV-0042":  "INV42",
		"inv 42":    "INV42",
		"000":       "0",
		"":          "",
		"A/00-7":    "A7",
		"INV-00042": "INV42",
	}
	for in, want := range tests {
		if got := NormalizeInvoiceNumber(in); got != want {
			t.Fatalf("%q: want %q, got %q", in, want, got)
		}
	}
}

func TestIsDuplicate_BR48_14(t *testing.T) {
	base := inv("a", "V1", "INV-100", "1000.00", 0)
	tests := []struct {
		name string
		b    Invoice
		want bool
	}{
		{"same number and amount", inv("b", "V1", "INV-100", "1000.00", 3), true},
		{"number one edit apart", inv("b", "V1", "INV-101", "1000.00", 3), true},
		{"leading zeros ignored", inv("b", "V1", "INV-0100", "1000.00", 3), true},
		{"amount within 0.5 percent", inv("b", "V1", "INV-100", "1004.00", 3), true},
		{"amount outside tolerance", inv("b", "V1", "INV-100", "1010.00", 3), false},
		{"date outside 30 days", inv("b", "V1", "INV-100", "1000.00", 45), false},
		{"other vendor", inv("b", "V2", "INV-100", "1000.00", 3), false},
		{"numbers two edits apart", inv("b", "V1", "INV-199", "1000.00", 3), false},
		{"posted credit note excluded", func() Invoice { c := inv("b", "V1", "INV-100", "1000.00", 3); c.PostedCredit = true; return c }(), false},
		{"other currency", func() Invoice { c := inv("b", "V1", "INV-100", "1000.00", 3); c.Currency = "EUR"; return c }(), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsDuplicate(base, tc.b); got != tc.want {
				t.Fatalf("want %v, got %v", tc.want, got)
			}
		})
	}
}

func TestAmountTolerance(t *testing.T) {
	if got := AmountTolerance(decimal.NewFromInt(1), decimal.NewFromInt(1)); !got.Equal(decimal.RequireFromString("0.01")) {
		t.Fatalf("floor: got %s", got)
	}
	if got := AmountTolerance(decimal.NewFromInt(10000), decimal.NewFromInt(9000)); !got.Equal(decimal.RequireFromString("50")) {
		t.Fatalf("0.5%% of 10000: got %s", got)
	}
}

func TestFindDuplicates_PlantedFixture(t *testing.T) {
	// 1,000 invoices over 100 vendors with 12 planted duplicates (AC-48.13).
	var invs []Invoice
	for i := 0; i < 1000; i++ {
		v := fmt.Sprintf("V%03d", i%100)
		invs = append(invs, inv(fmt.Sprintf("I%04d", i), v, fmt.Sprintf("INV-%d", 5000+i), fmt.Sprintf("%d.00", 100+i), i*7))
	}
	// Plant 12 duplicates of invoices 0..11 under the same vendor, one edit apart on the number.
	for i := 0; i < 12; i++ {
		src := invs[i]
		invs = append(invs, inv(fmt.Sprintf("D%04d", i), src.VendorID, src.Number+"X", src.Amount.String(), i*7+2))
	}
	violations := DuplicateViolations(FindDuplicates(invs))
	if len(violations) != 12 {
		t.Fatalf("want 12 violations, got %d: %+v", len(violations), violations)
	}
	first := map[string]bool{}
	for _, v := range violations {
		if v.DocumentID[0] != 'D' || len(v.DuplicateOf) != 1 || v.DuplicateOf[0][0] != 'I' {
			t.Fatalf("the planted copy is the violation, not the original: %+v", v)
		}
		first[ViolationKey("POL-1", 1, v.DocumentID)] = true
	}
	// A rerun of the same policy version produces only keys the first run already wrote.
	created := 0
	for _, v := range DuplicateViolations(FindDuplicates(invs)) {
		if !first[ViolationKey("POL-1", 1, v.DocumentID)] {
			created++
		}
	}
	if created != 0 {
		t.Fatalf("rerun must create no violations, created %d", created)
	}
	if groups := GroupByVendor(violations); len(groups) != 12 {
		t.Fatalf("one case per vendor: want 12 vendors, got %d", len(groups))
	}
}

func TestDuplicateViolations_ChainFlagsLaterDocuments(t *testing.T) {
	invs := []Invoice{
		inv("c", "V1", "INV-7", "500.00", 10),
		inv("a", "V1", "INV-7", "500.00", 0),
		inv("b", "V1", "INV-07", "500.00", 5),
	}
	got := DuplicateViolations(FindDuplicates(invs))
	if len(got) != 2 || got[0].DocumentID != "b" || got[1].DocumentID != "c" {
		t.Fatalf("want b and c flagged, a left as the original: %+v", got)
	}
	if fmt.Sprint(got[1].DuplicateOf) != "[a b]" {
		t.Fatalf("c repeats a and b: %+v", got[1])
	}
}

func TestIsDuplicate_EmptyNumbersNeverMatch(t *testing.T) {
	if IsDuplicate(inv("a", "V1", "--", "10.00", 0), inv("b", "V1", "", "10.00", 1)) {
		t.Fatal("invoices without a usable number must not match on number")
	}
}

func TestViolationKey_IdempotentPerVersion(t *testing.T) {
	if ViolationKey("P1", 2, "doc-9") == ViolationKey("P1", 3, "doc-9") {
		t.Fatal("a new policy version must produce new keys")
	}
	if ViolationKey("P1", 2, "doc-9") != ViolationKey("P1", 2, "doc-9") {
		t.Fatal("same version and document must produce the same key")
	}
}

func TestLevenshtein(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{{"", "", 0}, {"abc", "", 3}, {"kitten", "sitting", 3}, {"INV1", "INV2", 1}}
	for _, tc := range tests {
		if got := Levenshtein(tc.a, tc.b); got != tc.want {
			t.Fatalf("%q/%q: want %d got %d", tc.a, tc.b, tc.want, got)
		}
	}
}

func TestIsDuplicate_PropertySymmetricAndReflexiveExclusion(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		num := rapid.StringMatching(`[A-Z0-9]{1,8}`).Draw(t, "num")
		amt := rapid.IntRange(1, 100000).Draw(t, "amt")
		off := rapid.IntRange(0, 60).Draw(t, "off")
		a := inv("a", "V1", num, fmt.Sprintf("%d.00", amt), 0)
		b := inv("b", "V1", num, fmt.Sprintf("%d.00", amt), off)
		ab, ba := IsDuplicate(a, b), IsDuplicate(b, a)
		if ab != ba {
			t.Fatalf("symmetry broken for %s %d off %d", num, amt, off)
		}
		if ab != (off <= dupWindowDays) {
			t.Fatalf("identical numbers and amounts: duplicate must equal date-window check (off=%d)", off)
		}
		if IsDuplicate(a, a) {
			t.Fatal("an invoice is never a duplicate of itself")
		}
	})
}
