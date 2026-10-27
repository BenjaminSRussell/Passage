package domain

import (
	"strings"
	"testing"
)

// TestVariance_AC248: the variance equals material + conversion − output.
func TestVariance_AC248(t *testing.T) {
	got := Variance(dec("900"), dec("300"), dec("1150"))
	if !got.Equal(dec("50")) {
		t.Fatalf("variance = %s, want 50", got)
	}
	if !Variance(dec("1"), dec("0.00005"), dec("0")).Equal(dec("1.0001")) {
		t.Fatal("variance not rounded to 4 places")
	}
}

func TestCheckSettleable(t *testing.T) {
	ok := []Kanban{{ID: "a", State: Completed}, {ID: "b", State: Emptied}}
	if err := CheckSettleable(ok); err != nil {
		t.Fatalf("completed and emptied cards refused: %v", err)
	}
	open := append(ok, Kanban{ID: "c", State: InProgress}, Kanban{ID: "d", State: Planned})
	err := CheckSettleable(open)
	if err == nil || err.(*Error).Code != CodeOpenKanbans {
		t.Fatalf("open cards: %v", err)
	}
	if !strings.Contains(err.Error(), "c, d") {
		t.Fatalf("open cards not listed: %v", err)
	}
	if err := CheckSettleable(nil); err != nil {
		t.Fatalf("empty period refused: %v", err)
	}
}

func TestValidateFlow(t *testing.T) {
	good := []Activity{
		{ID: "1", Name: "cut", Kind: "process", OutputLocationID: "L1"},
		{ID: "2", Name: "move", Kind: "transfer", PredecessorIDs: []string{"1"}},
		{ID: "3", Name: "vendor", Kind: "service", PredecessorIDs: []string{"2"}},
	}
	if err := ValidateFlow(good); err != nil {
		t.Fatalf("connected flow refused: %v", err)
	}
	noOutput := []Activity{{ID: "1", Name: "cut", Kind: "process"}}
	if err := ValidateFlow(noOutput); err == nil || err.(*Error).Code != CodeNoOutput {
		t.Fatalf("process without output: %v", err)
	}
	dangling := []Activity{{ID: "1", Name: "cut", Kind: "transfer", PredecessorIDs: []string{"9"}}}
	if err := ValidateFlow(dangling); err == nil || err.(*Error).Code != CodeInvalid {
		t.Fatalf("unknown predecessor: %v", err)
	}
	badKind := []Activity{{ID: "1", Name: "cut", Kind: "assembly"}}
	if err := ValidateFlow(badKind); err == nil || err.(*Error).Code != CodeInvalid {
		t.Fatalf("unknown kind: %v", err)
	}
}

func TestValidateFlow_RejectsCyclesDuplicatesAndEmpty(t *testing.T) {
	loop := []Activity{
		{ID: "1", Name: "cut", Kind: "transfer", PredecessorIDs: []string{"2"}},
		{ID: "2", Name: "bend", Kind: "transfer", PredecessorIDs: []string{"1"}},
	}
	if err := ValidateFlow(loop); err == nil || err.(*Error).Code != CodeInvalid || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("cycle: %v", err)
	}
	self := []Activity{{ID: "1", Name: "cut", Kind: "transfer", PredecessorIDs: []string{"1"}}}
	if err := ValidateFlow(self); err == nil {
		t.Fatal("self predecessor accepted")
	}
	dup := []Activity{{ID: "1", Name: "a", Kind: "transfer"}, {ID: "1", Name: "b", Kind: "transfer"}}
	if err := ValidateFlow(dup); err == nil {
		t.Fatal("duplicate id accepted")
	}
	if err := ValidateFlow(nil); err == nil {
		t.Fatal("empty flow accepted")
	}
}
