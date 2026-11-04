package domain

import (
	"slices"
	"testing"
	"time"
)

func TestDiff_NamesEveryDifferingBucket(t *testing.T) {
	var a OnHand
	if got := Diff(a, a); len(got) != 0 {
		t.Fatalf("Diff(zero, zero) = %v", got)
	}
	b := a
	b.PostedValue = d("1.5")
	b.ReservedPhysical = d("3")
	b.OnOrder = d("2")
	got := Diff(a, b)
	want := []string{"posted_value", "reserved_physical", "on_order"}
	if !slices.Equal(got, want) {
		t.Fatalf("Diff = %v, want %v", got, want)
	}
	// Equal values at different scales are equal (numeric columns come back padded).
	c := a
	c.PostedQty = d("0.000000")
	if got := Diff(a, c); len(got) != 0 {
		t.Fatalf("scale-only difference reported: %v", got)
	}
}

func TestReverseJournal(t *testing.T) {
	when := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	orig := Journal{
		ID: "j1", LegalEntityID: "le", Type: JournalMovement, Status: JournalPosted,
		OffsetAccountID: "500100", Currency: "USD",
		Lines: []JournalLine{
			{ID: "l1", LineNo: 1, ProductID: "p1", Dims: Dimensions{DimSite: "S1"}, Qty: d("4"), EnteredQty: d("4"), EnteredUnit: "EA", CostPrice: d("2.5")},
			{ID: "l2", LineNo: 2, ProductID: "p2", Dims: Dimensions{DimSite: "S1"}, Qty: d("-1"), EnteredQty: d("-1"), EnteredUnit: "EA"},
		},
	}
	rev, err := ReverseJournal(orig, "j2", when)
	if err != nil {
		t.Fatal(err)
	}
	if rev.ID != "j2" || rev.ReversesJournalID != "j1" || rev.Status != JournalDraft || rev.Type != JournalMovement ||
		rev.OffsetAccountID != "500100" || rev.Currency != "USD" || !rev.PostingDate.Equal(when) {
		t.Fatalf("header = %+v", rev)
	}
	if len(rev.Lines) != 2 {
		t.Fatalf("lines = %+v", rev.Lines)
	}
	if l := rev.Lines[0]; !l.Qty.Equal(d("-4")) || !l.EnteredQty.Equal(d("-4")) || l.ReversesLineID != "l1" || !l.CostPrice.Equal(d("2.5")) {
		t.Fatalf("line 1 = %+v", l)
	}
	if l := rev.Lines[1]; !l.Qty.Equal(d("1")) || l.ReversesLineID != "l2" {
		t.Fatalf("line 2 = %+v", l)
	}
	if orig.Lines[0].Qty.Sign() <= 0 {
		t.Fatal("original mutated")
	}
	if err := ValidateJournal(rev, DefaultJournalRules); err != nil {
		t.Fatalf("a reversal of a valid journal must be valid: %v", err)
	}

	draft := orig
	draft.Status = JournalDraft
	if _, err := ReverseJournal(draft, "j3", when); codeOf(err) != CodeJournalNotReady {
		t.Fatalf("draft reversal code = %q", codeOf(err))
	}
	reversed := orig
	reversed.ReversedByJournalID = "j2"
	if _, err := ReverseJournal(reversed, "j4", when); codeOf(err) != CodeJournalNotReady {
		t.Fatalf("second reversal code = %q", codeOf(err))
	}
}

func TestCopyJournalKeepsHeaderAndLinesButNotLinks(t *testing.T) {
	when := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	src := Journal{ID: "j1", LegalEntityID: "le", Type: JournalMovement, Status: JournalPosted, OffsetAccountID: "500100", Currency: "USD", JournalNameID: "n1",
		PostingDate: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		Lines: []JournalLine{{ID: "l1", LineNo: 1, ClientKey: "ck", ProductID: "p", Qty: d("3"), EnteredQty: d("3"),
			EnteredUnit: "EA", CostPrice: d("2")}}}
	cp, err := CopyJournal(src, "j2", when)
	if err != nil {
		t.Fatal(err)
	}
	if cp.ID != "j2" || cp.Status != JournalDraft || cp.Version != 1 || cp.OffsetAccountID != "500100" || cp.JournalNameID != "n1" || !cp.PostingDate.Equal(when) ||
		len(cp.Lines) != 1 || cp.Lines[0].ID != "" || cp.Lines[0].ClientKey != "" || cp.Lines[0].ReversesLineID != "" || !cp.Lines[0].Qty.Equal(src.Lines[0].Qty) {
		t.Fatalf("copy = %+v", cp)
	}
	for name, bad := range map[string]Journal{
		"count":    {Type: JournalCount},
		"reversal": {Type: JournalMovement, ReversesJournalID: "j0"},
	} {
		if _, err := CopyJournal(bad, "j3", when); codeOf(err) != CodeJournalNotReady {
			t.Fatalf("%s: %v, want %s", name, err, CodeJournalNotReady)
		}
	}
}
