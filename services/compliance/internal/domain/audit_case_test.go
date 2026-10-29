package domain

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func codeOf(err error) string {
	var de *Error
	if errors.As(err, &de) {
		return de.Code
	}
	return ""
}

func TestApplyCaseAction_HappyPath(t *testing.T) {
	now := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	s := CaseState{Status: CaseOpen}
	var err error
	s, err = ApplyCaseAction(s, CaseTransition{Action: ActionStartReview, Now: now})
	if err != nil || s.Status != CaseInReview {
		t.Fatalf("start review: %v %+v", err, s)
	}
	s, err = ApplyCaseAction(s, CaseTransition{Action: ActionResolveIssue, ResolutionCode: "DUPLICATE_RECOVERED",
		RecoveryAmount: decimal.RequireFromString("1250.50"), Currency: "USD", Now: now})
	if err != nil || s.Status != CaseResolvedIssue || s.RecoveryAmount.String() != "1250.5" || s.Currency != "USD" {
		t.Fatalf("resolve issue: %v %+v", err, s)
	}
	s, err = ApplyCaseAction(s, CaseTransition{Action: ActionClose, Now: now})
	if err != nil || s.Status != CaseClosed || s.ClosedAt == nil || !s.ClosedAt.Equal(now) {
		t.Fatalf("close: %v %+v", err, s)
	}
	s, err = ApplyCaseAction(s, CaseTransition{Action: ActionArchive, Now: now.AddDate(0, 0, 366)})
	if err != nil || s.Status != CaseArchived {
		t.Fatalf("archive after 366 days: %v %+v", err, s)
	}
}

func TestApplyCaseAction_Rejections(t *testing.T) {
	now := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	closedAt := now
	tests := []struct {
		name string
		s    CaseState
		t    CaseTransition
		code string
	}{
		{"cannot skip review", CaseState{Status: CaseOpen}, CaseTransition{Action: ActionResolveNoIssue, ResolutionCode: "NO_ISSUE"}, CodeCaseTransition},
		{"no issue with issue code", CaseState{Status: CaseInReview}, CaseTransition{Action: ActionResolveNoIssue, ResolutionCode: "REFERRED_FRAUD"}, CodeCaseResolution},
		{"issue without code", CaseState{Status: CaseInReview}, CaseTransition{Action: ActionResolveIssue}, CodeCaseResolution},
		{"negative recovery", CaseState{Status: CaseInReview}, CaseTransition{Action: ActionResolveIssue, ResolutionCode: "POLICY_CORRECTED", RecoveryAmount: decimal.NewFromInt(-1), Currency: "USD"}, CodeCaseResolution},
		{"recovery without currency", CaseState{Status: CaseInReview}, CaseTransition{Action: ActionResolveIssue, ResolutionCode: "POLICY_CORRECTED", RecoveryAmount: decimal.NewFromInt(5)}, CodeCaseResolution},
		{"close without resolution", CaseState{Status: CaseResolvedNoIssue}, CaseTransition{Action: ActionClose, Now: now}, CodeCaseResolution},
		{"archived case cannot move", CaseState{Status: CaseArchived}, CaseTransition{Action: ActionArchive, Now: now}, CodeCaseTransition},
		{"archive too soon", CaseState{Status: CaseClosed, ClosedAt: &closedAt}, CaseTransition{Action: ActionArchive, Now: now.AddDate(0, 0, 10)}, CodeCaseArchiveTooSoon},
		{"archive window below minimum", CaseState{Status: CaseClosed, ClosedAt: &closedAt}, CaseTransition{Action: ActionArchive, Now: now.AddDate(0, 0, 90), ArchiveDays: 5}, CodeCaseArchiveTooSoon},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ApplyCaseAction(tc.s, tc.t)
			if got := codeOf(err); got != tc.code {
				t.Fatalf("want %s, got %q (%v)", tc.code, got, err)
			}
		})
	}
}

func TestApplyCaseAction_PropertyNeverSkipsStates(t *testing.T) {
	actions := []CaseAction{ActionStartReview, ActionResolveNoIssue, ActionResolveIssue, ActionClose, ActionArchive}
	rapid.Check(t, func(t *rapid.T) {
		s := CaseState{Status: CaseOpen}
		now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		for step := 0; step < 12; step++ {
			a := rapid.SampledFrom(actions).Draw(t, fmt.Sprintf("action-%d", step))
			code := rapid.SampledFrom([]string{"NO_ISSUE", "DUPLICATE_RECOVERED", "BOGUS"}).Draw(t, "code")
			now = now.AddDate(0, 0, rapid.IntRange(0, 400).Draw(t, "days"))
			next, err := ApplyCaseAction(s, CaseTransition{Action: a, ResolutionCode: code, Now: now, ArchiveDays: 365})
			if err != nil {
				if s != next {
					t.Fatalf("a rejected action must not change state")
				}
				continue
			}
			// An accepted transition always starts from its legal source status.
			if _, ok := transitions[s.Status][a]; !ok {
				t.Fatalf("accepted %s from %s", a, s.Status)
			}
			if next.Status == CaseArchived && (s.ClosedAt == nil || now.Before(s.ClosedAt.AddDate(0, 0, 365))) {
				t.Fatal("archived before the retention window")
			}
			s = next
		}
	})
}

func TestGroupViolations(t *testing.T) {
	vs := []Violation{
		{ID: "v3", PolicyID: "P-DUP", RuleType: "DUPLICATE", DocumentID: "inv-3", PartyID: "V-2"},
		{ID: "v1", PolicyID: "P-DUP", RuleType: "DUPLICATE", DocumentID: "inv-1", PartyID: "V-1"},
		{ID: "v2", PolicyID: "P-DUP", RuleType: "DUPLICATE", DocumentID: "inv-2", PartyID: "V-1"},
		{ID: "v4", PolicyID: "P-KW", RuleType: AuditRuleKeyword, DocumentID: "exp-9", PartyID: "E-1"},
		{ID: "v5", PolicyID: "P-SMP", RuleType: AuditRuleSampling, DocumentID: "jr-4", PartyID: "G"},
		{ID: "v6", PolicyID: "P-DUP", RuleType: "DUPLICATE", DocumentID: "inv-6", PartyID: "V-1", CaseID: "case-1"},
	}
	drafts, err := GroupViolations(vs)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		key      string
		category string
		vids     string
	}{
		{"DUPLICATE_PAYMENT:V-1", CategoryDuplicatePayment, "[v1 v2]"},
		{"DUPLICATE_PAYMENT:V-2", CategoryDuplicatePayment, "[v3]"},
		{"POLICY_BREACH:P-KW", CategoryPolicyBreach, "[v4]"},
		{"SAMPLED_REVIEW:P-SMP", CategorySampledReview, "[v5]"},
	}
	if len(drafts) != len(want) {
		t.Fatalf("want %d drafts, got %d: %+v", len(want), len(drafts), drafts)
	}
	for i, w := range want {
		d := drafts[i]
		if d.Key != w.key || d.Category != w.category || fmt.Sprint(d.ViolationIDs) != w.vids {
			t.Fatalf("draft %d: got %+v", i, d)
		}
	}
}

func TestGroupViolations_UnknownRule(t *testing.T) {
	_, err := GroupViolations([]Violation{{ID: "v", RuleType: "MYSTERY"}})
	if codeOf(err) != CodeAuditRuleType {
		t.Fatalf("want %s, got %v", CodeAuditRuleType, err)
	}
}
