package domain

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestReportLifecycleGuards(t *testing.T) {
	cases := []struct {
		status          ReportStatus
		addLine, submit bool
	}{
		{ReportDraft, true, true},
		{ReportSubmitted, false, false},
		{ReportStatus("APPROVED"), false, false},
		{ReportStatus("RETURNED"), false, false},
	}
	for _, c := range cases {
		if got := CanAddLine(c.status); got != c.addLine {
			t.Errorf("CanAddLine(%s) = %v; want %v", c.status, got, c.addLine)
		}
		if got := CanSubmit(c.status); got != c.submit {
			t.Errorf("CanSubmit(%s) = %v; want %v", c.status, got, c.submit)
		}
	}
}

// A report with an unreceipted 30.00 line, a duplicate of it that has a receipt, a
// justified meal over 75.00 and a clean line: the receipt requirement and the
// duplicate block submission, the justified meal and the clean line do not.
func TestBlockersAndForLineOnAReport(t *testing.T) {
	day := civil(2025, 3, 3)
	lines := []Line{
		{ID: "a", Category: "TRAVEL", TxnDate: day, Amount: decimal.NewFromInt(30), Merchant: "ACME"},
		{ID: "b", Category: "TRAVEL", TxnDate: day, Amount: decimal.NewFromInt(30), Merchant: " acme ", HasReceipt: true},
		{ID: "c", Category: "MEALS", TxnDate: day, Amount: decimal.NewFromInt(80), Merchant: "Bistro", HasReceipt: true, Justification: "Client dinner"},
		{ID: "d", Category: "TRAVEL", TxnDate: day, Amount: decimal.NewFromInt(10), Merchant: "Bus"},
	}
	results := EvaluateReport(DefaultRules(), lines, Context{SubmitDate: day})
	blockers := Blockers(results)
	if len(blockers) != 2 {
		t.Fatalf("blockers = %+v; want the receipt on a and the duplicate on b", blockers)
	}
	if blockers[0].LineID != "a" || blockers[0].RuleID != "EXP-RCPT-025" || blockers[0].Code != CodeReceipt {
		t.Errorf("first blocker = %+v; want EXP-RCPT-025 (%s) on a", blockers[0], CodeReceipt)
	}
	if blockers[1].LineID != "b" || blockers[1].RuleID != "EXP-DUP-001" || blockers[1].Outcome != OutcomeBlock {
		t.Errorf("second blocker = %+v; want the EXP-DUP-001 BLOCK on b", blockers[1])
	}
	meal := ForLine(results, "c")
	if len(meal) != 1 || meal[0].RuleID != "EXP-MEAL-075" || !meal[0].Satisfied {
		t.Errorf("meal results = %+v; want one satisfied EXP-MEAL-075", meal)
	}
	if got := ForLine(results, "d"); len(got) != 0 {
		t.Errorf("clean line d carries %+v; want none", got)
	}
	if got := Blockers(ForLine(results, "c")); len(got) != 0 {
		t.Errorf("a justified meal must not block, got %+v", got)
	}
}

func TestBlockersOfNothingIsEmpty(t *testing.T) {
	if got := Blockers(nil); len(got) != 0 {
		t.Fatalf("Blockers(nil) = %+v", got)
	}
	if got := ForLine(nil, "x"); len(got) != 0 {
		t.Fatalf("ForLine(nil) = %+v", got)
	}
}
