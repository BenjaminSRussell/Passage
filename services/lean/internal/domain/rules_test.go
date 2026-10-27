package domain

import (
	"testing"

	"github.com/shopspring/decimal"
)

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func qtyStrings(qs []decimal.Decimal) []string {
	out := make([]string, len(qs))
	for i, q := range qs {
		out[i] = q.String()
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestEventKanbanQuantities(t *testing.T) {
	tests := []struct {
		name          string
		demand, fixed string
		exact         bool
		want          []string
	}{
		{"full size overproduces (BR-24.8)", "50", "20", false, []string{"20", "20", "20"}},
		{"exact last card holds remainder", "50", "20", true, []string{"20", "20", "10"}},
		{"demand below one card", "5", "20", false, []string{"20"}},
		{"demand exact multiple", "40", "20", true, []string{"20", "20"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := EventKanbanQuantities(dec(tc.demand), dec(tc.fixed), tc.exact)
			if err != nil {
				t.Fatal(err)
			}
			if !equalStrings(qtyStrings(got), tc.want) {
				t.Fatalf("got %v, want %v", qtyStrings(got), tc.want)
			}
		})
	}
}

func TestEventKanbanQuantities_Rejects(t *testing.T) {
	if _, err := EventKanbanQuantities(dec("0"), dec("20"), false); err == nil {
		t.Fatal("zero demand accepted")
	}
	if _, err := EventKanbanQuantities(dec("1"), dec("0"), false); err == nil {
		t.Fatal("zero fixed quantity accepted")
	}
	if _, err := EventKanbanQuantities(dec("1000000"), dec("1"), false); err == nil {
		t.Fatal("over-limit generation accepted")
	}
}

func TestFixedKanbanCount(t *testing.T) {
	tests := []struct {
		name   string
		daily  string
		lead   string
		safety string
		fixed  string
		want   int64
	}{
		{"exact product", "10", "2", "0", "20", 1},
		{"safety raises the count", "10", "2", "10", "20", 2},
		{"rounds up", "7", "3", "0", "10", 3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := FixedKanbanCount(dec(tc.daily), dec(tc.lead), dec(tc.safety), dec(tc.fixed))
			if err != nil || got != tc.want {
				t.Fatalf("got %d, %v; want %d", got, err, tc.want)
			}
		})
	}
	if _, err := FixedKanbanCount(dec("1"), dec("1"), dec("0"), dec("0")); err == nil {
		t.Fatal("zero fixed quantity accepted")
	}
}

func TestCheckCapacity(t *testing.T) {
	if err := CheckCapacity("R1", 2, 1, 3); err != nil {
		t.Fatalf("filling to max refused: %v", err)
	}
	err := CheckCapacity("R1", 3, 1, 3)
	if err == nil || err.(*Error).Code != CodeCapacity {
		t.Fatalf("over max: %v", err)
	}
}

func TestValidateRule(t *testing.T) {
	fixed := Rule{Type: RuleManufacturing, Replenishment: ReplenishFixed, FixedQty: dec("20"), NumberOfKanbans: 3}
	event := Rule{Type: RuleWithdrawal, Replenishment: ReplenishEvent, FixedQty: dec("20"), EventSource: "sales"}
	for _, ok := range []Rule{fixed, event, {Type: RuleManufacturing, Replenishment: ReplenishScheduled, FixedQty: dec("1")}} {
		if err := ValidateRule(ok); err != nil {
			t.Fatalf("valid rule %+v refused: %v", ok, err)
		}
	}
	bad := []Rule{
		{Type: "assembly", Replenishment: ReplenishFixed, FixedQty: dec("1"), NumberOfKanbans: 1},
		{Type: RuleManufacturing, Replenishment: ReplenishFixed, FixedQty: dec("0"), NumberOfKanbans: 1},
		{Type: RuleManufacturing, Replenishment: ReplenishFixed, FixedQty: dec("1"), NumberOfKanbans: 0},
		{Type: RuleManufacturing, Replenishment: ReplenishFixed, FixedQty: dec("1"), NumberOfKanbans: MaxKanbansPerRule + 1},
		{Type: RuleManufacturing, Replenishment: ReplenishEvent, FixedQty: dec("1"), EventSource: "email"},
		{Type: RuleManufacturing, Replenishment: "monthly", FixedQty: dec("1")},
	}
	for _, r := range bad {
		if err := ValidateRule(r); err == nil || err.(*Error).Code != CodeInvalid {
			t.Fatalf("rule %+v: err = %v, want COM-0001", r, err)
		}
	}
}

// BR-24.7: a fixed rule never holds more active cards than its number of kanbans.
func TestFixedCards(t *testing.T) {
	r := Rule{Type: RuleManufacturing, Replenishment: ReplenishFixed, FixedQty: dec("25"), NumberOfKanbans: 3}
	n := 0
	newID := func() string { n++; return "k" }
	cards, err := FixedCards("R1", r, 1, 2, newID)
	if err != nil || len(cards) != 2 || cards[0].State != NotPlanned || !cards[1].Qty.Equal(dec("25")) {
		t.Fatalf("cards %+v err %v", cards, err)
	}
	if _, err := FixedCards("R1", r, 2, 2, newID); err == nil || err.(*Error).Code != CodeCapacity {
		t.Fatalf("over capacity: %v", err)
	}
	if _, err := FixedCards("R1", r, 0, 0, newID); err == nil || err.(*Error).Code != CodeInvalid {
		t.Fatalf("zero count: %v", err)
	}
}
