package domain

import (
	"errors"
	"reflect"
	"testing"
)

func topSide() GroupJournal {
	return GroupJournal{
		PeriodKey: "2026-11", DocumentType: DocAdjustment, Currency: "USD",
		Reason: "Accrue audit fee at group level", AttachmentRef: "doc-123",
		Lines: []GroupJournalLine{
			{FSItem: "AUDIT_FEES", Debit: d("120000.00")},
			{FSItem: "ACCRUALS", Credit: d("120000.00")},
		},
	}
}

func TestGroupJournal_Validate(t *testing.T) {
	if err := topSide().Validate(); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		edit func(*GroupJournal)
		code string
	}{
		{"unbalanced", func(j *GroupJournal) { j.Lines[1].Credit = d("119999.99") }, CodeUnbalancedRule},
		{"unknown type", func(j *GroupJournal) { j.DocumentType = "Other" }, CodeInvalid},
		{"no reason", func(j *GroupJournal) { j.Reason = "" }, CodeInvalid},
		{"no attachment", func(j *GroupJournal) { j.AttachmentRef = "" }, CodeInvalid},
		{"one line", func(j *GroupJournal) { j.Lines = j.Lines[:1] }, CodeInvalid},
		{"both sides", func(j *GroupJournal) { j.Lines[0].Credit = d("1") }, CodeInvalid},
		{"no side", func(j *GroupJournal) { j.Lines[0].Debit = d("0") }, CodeInvalid},
		{"negative", func(j *GroupJournal) { j.Lines[0].Debit = d("-1") }, CodeInvalid},
		{"no FS item", func(j *GroupJournal) { j.Lines[0].FSItem = "" }, CodeInvalid},
		{"sub-cent", func(j *GroupJournal) { j.Lines[0].Debit = d("120000.001") }, CodeInvalid},
		{"bad period", func(j *GroupJournal) { j.PeriodKey = "2026-13" }, CodeInvalid},
		{"bad currency", func(j *GroupJournal) { j.Currency = "usd1" }, CodeInvalid},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			j := topSide()
			j.Lines = append([]GroupJournalLine(nil), j.Lines...)
			c.edit(&j)
			err := j.Validate()
			var de *Error
			if !errors.As(err, &de) || de.Code != c.code {
				t.Fatalf("err = %v; want %s", err, c.code)
			}
		})
	}
}

// TestGroupJournal_Approval: the default threshold is 100,000.00 and applies at
// or above it (CNS-0909).
func TestGroupJournal_Approval(t *testing.T) {
	j := topSide()
	threshold := DefaultApprovalThreshold()
	var de *Error
	if err := j.CheckApproval(threshold); !errors.As(err, &de) || de.Code != CodeApprovalRequired {
		t.Fatalf("err = %v; want CNS-0909", err)
	}
	j.Approved = true
	if err := j.CheckApproval(threshold); err != nil {
		t.Fatalf("approved journal: %v", err)
	}
	j = topSide()
	j.Lines = []GroupJournalLine{{FSItem: "A", Debit: d("100000.00")}, {FSItem: "B", Credit: d("100000.00")}}
	if !j.ApprovalRequired(threshold) {
		t.Fatal("a journal of exactly the threshold needs approval")
	}
	j.Lines = []GroupJournalLine{{FSItem: "A", Debit: d("99999.99")}, {FSItem: "B", Credit: d("99999.99")}}
	if j.ApprovalRequired(threshold) || j.CheckApproval(threshold) != nil {
		t.Fatal("a journal below the threshold needs no approval")
	}
}

// TestGroupJournal_CarryForward is BR-40.18 with a fiscal year starting in
// April: a carried-forward November journal applies to November through March
// and stops at year end unless it is Cumulative.
func TestGroupJournal_CarryForward(t *testing.T) {
	j := topSide()
	periods, past, err := j.Periods(4)
	if err != nil || !reflect.DeepEqual(periods, []string{"2026-11"}) || past {
		t.Fatalf("period-only journal: %v %v %v", periods, past, err)
	}
	j.CarryForward = true
	periods, past, err = j.Periods(4)
	want := []string{"2026-11", "2026-12", "2027-01", "2027-02", "2027-03"}
	if err != nil || !reflect.DeepEqual(periods, want) || past {
		t.Fatalf("carried forward: %v %v %v; want %v", periods, past, err, want)
	}
	for period, applies := range map[string]bool{"2026-10": false, "2026-11": true, "2027-03": true, "2027-04": false} {
		got, err := j.AppliesTo(period, 4)
		if err != nil || got != applies {
			t.Fatalf("AppliesTo(%s) = %v, %v; want %v", period, got, err, applies)
		}
	}
	j.DocumentType = DocCumulative
	if _, past, _ = j.Periods(4); !past {
		t.Fatal("a cumulative journal continues past year end")
	}
	if got, _ := j.AppliesTo("2028-01", 4); !got {
		t.Fatal("a cumulative carried-forward journal applies after year end")
	}
	if _, _, err := j.Periods(13); err == nil {
		t.Fatal("fiscal year start month 13 must be rejected")
	}
}
