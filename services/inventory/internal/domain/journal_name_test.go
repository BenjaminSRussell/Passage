package domain

import "testing"

func TestJournalNameValidate(t *testing.T) {
	ok := JournalName{Code: "MOVE-1", Type: JournalMovement, OffsetAccountID: "5000"}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := []JournalName{
		{Code: "move", Type: JournalMovement},
		{Code: "", Type: JournalMovement},
		{Code: "A", Type: JournalCount},
		{Code: "A", Type: JournalAdjustment, OffsetAccountID: "5000"},
		{Code: "A", Type: JournalMovement, OffsetAccountID: "bad account"},
	}
	for i, n := range bad {
		if codeOf(n.Validate()) != CodeInvalid {
			t.Errorf("case %d: want %s, got %v", i, CodeInvalid, n.Validate())
		}
	}
}

func TestJournalNameRules(t *testing.T) {
	n := JournalName{LegalEntityID: "le1", Code: "ADJ", Type: JournalAdjustment, Active: true, RequiresApproval: true}
	if err := n.CheckForJournal("le1", JournalAdjustment); err != nil {
		t.Fatal(err)
	}
	if codeOf(n.CheckForJournal("le2", JournalAdjustment)) != CodeJournalNameUnusable {
		t.Error("another legal entity must be refused")
	}
	if codeOf(n.CheckForJournal("le1", JournalMovement)) != CodeJournalNameUnusable {
		t.Error("another type must be refused")
	}
	n.Active = false
	if codeOf(n.CheckForJournal("le1", JournalAdjustment)) != CodeJournalNameUnusable {
		t.Error("an inactive name must be refused")
	}
	if codeOf(n.CheckPostable(JournalDraft)) != CodeApprovalRequired {
		t.Error("a DRAFT post must be refused when approval is required")
	}
	if err := n.CheckPostable(JournalApproved); err != nil {
		t.Error(err)
	}
	n.RequiresApproval = false
	if err := n.CheckPostable(JournalDraft); err != nil {
		t.Error(err)
	}
}
