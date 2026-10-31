package domain

import (
	"testing"

	"github.com/shopspring/decimal"
)

func ai(id, acct, amt, assign string) AutoItem {
	return AutoItem{ID: id, Account: acct, Amount: decimal.RequireFromString(amt), PostingDate: "2026-03-05", Assignment: assign,
		Dimensions: map[string]string{"DEPT": "SALES"}}
}

func TestValidCriterion(t *testing.T) {
	for _, c := range []string{"assignment", "source_line_ref", "document_id", "amount_txn", "posting_date", "dim.DEPT"} {
		if !ValidCriterion(c) {
			t.Errorf("%s should be valid", c)
		}
	}
	for _, c := range []string{"", "amount", "dim.", "dim.dept", "dim.1X", "Assignment"} {
		if ValidCriterion(c) {
			t.Errorf("%s should be invalid", c)
		}
	}
}

func TestGroupItems_ByAccountAndCriteria(t *testing.T) {
	items := []AutoItem{
		ai("1", "2100", "100.00", "PO1"), ai("2", "2100", "-100.00", "PO1"),
		ai("3", "2100", "50.00", "PO2"), // alone
		ai("4", "2100", "10.00", ""),    // no assignment
		ai("5", "2200", "70.00", "PO1"), ai("6", "2200", "-70.00", "PO1"),
	}
	groups, unmatched := GroupItems(items, []string{"assignment"})
	if len(groups) != 2 || unmatched != 2 {
		t.Fatalf("groups %d unmatched %d, want 2 and 2", len(groups), unmatched)
	}
	if groups[0].Account != "2100" || groups[1].Account != "2200" || !groups[0].Net.IsZero() {
		t.Fatalf("unexpected groups %+v", groups)
	}
	if groups[0].Hash == groups[1].Hash || len(groups[0].Hash) != 32 {
		t.Fatalf("hashes %q %q", groups[0].Hash, groups[1].Hash)
	}
}

func TestGroupItems_AmountCriterionMeetsDebitAndCredit(t *testing.T) {
	groups, _ := GroupItems([]AutoItem{ai("1", "2100", "25.50", "A"), ai("2", "2100", "-25.5", "B")}, []string{"amount_txn", "dim.DEPT", "posting_date"})
	if len(groups) != 1 || !groups[0].Net.IsZero() {
		t.Fatalf("got %+v", groups)
	}
}

func TestGroupHash_DependsOnItems(t *testing.T) {
	a, _ := GroupItems([]AutoItem{ai("1", "2100", "1", "A"), ai("2", "2100", "-1", "A")}, []string{"assignment"})
	b, _ := GroupItems([]AutoItem{ai("1", "2100", "1", "A"), ai("3", "2100", "-1", "A")}, []string{"assignment"})
	if a[0].Hash == b[0].Hash {
		t.Fatal("different items must give different group hashes")
	}
}

func TestDecideGroup(t *testing.T) {
	g := AutoGroup{Items: make([]AutoItem, 2), Debit: decimal.RequireFromString("100"), Credit: decimal.RequireFromString("99.98"), Net: decimal.RequireFromString("0.02")}
	if d := DecideGroup(g, nil); d.Clear || d.Reason != "LEDGER_CLEARING_UNBALANCED" {
		t.Fatalf("no tolerance: %+v", d)
	}
	tol := Tolerance{Debit: decimal.RequireFromString("0.05"), Credit: decimal.RequireFromString("0.05")}
	if d := DecideGroup(g, &tol); !d.Clear {
		t.Fatalf("within tolerance: %+v", d)
	}
	tight := Tolerance{Debit: decimal.RequireFromString("0.01"), Credit: decimal.RequireFromString("0.01")}
	if d := DecideGroup(g, &tight); d.Clear {
		t.Fatalf("outside tolerance cleared: %+v", d)
	}
	g.Net = decimal.Zero
	if d := DecideGroup(g, nil); !d.Clear {
		t.Fatalf("zero net: %+v", d)
	}
}
