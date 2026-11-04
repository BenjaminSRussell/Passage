package domain

import "testing"

// A COUNT journal may carry vendor-owned lines (quantity-only variance); any
// other non-ownership journal still refuses them (INV-OWN-001, BR-16.14).
func TestCountJournalAcceptsVendorOwnedLinesOthersDoNot(t *testing.T) {
	line := ownLine("V1", "-3", "0")
	count := Journal{Type: JournalCount, Currency: "USD", Lines: []JournalLine{line}}
	if got := codeOf(ValidateJournal(count, DefaultJournalRules)); got != "" {
		t.Fatalf("COUNT with a vendor-owned line: %q", got)
	}
	adj := Journal{Type: JournalAdjustment, Currency: "USD", Lines: []JournalLine{line}}
	if got := codeOf(ValidateJournal(adj, DefaultJournalRules)); got != CodeOwnershipValue {
		t.Fatalf("ADJUSTMENT with a vendor-owned line: %q, want %s", got, CodeOwnershipValue)
	}
}

func TestPlanPostingCountOfVendorOwnedStockIsPhysicalOnly(t *testing.T) {
	loss, gain := ownLine("V1", "-3", "0"), ownLine("V1", "2", "0")
	company := ownLine("", "-1", "5")
	plan := PlanPosting(Journal{Type: JournalCount, Currency: "USD", Lines: []JournalLine{loss, gain, company}})
	want := []Status{StatusDeducted, StatusReceived, StatusSold}
	for i, st := range want {
		if plan[i].Status != st {
			t.Fatalf("line %d status %s, want %s", i+1, plan[i].Status, st)
		}
	}
}
