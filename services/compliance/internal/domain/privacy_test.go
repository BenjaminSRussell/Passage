package domain

import (
	"errors"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"
)

var privacyNow = time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)

func expectPrivacyCode(t *testing.T, err error, code string) {
	t.Helper()
	var de *Error
	if !errors.As(err, &de) || de.Code != code {
		t.Fatalf("want %s, got %v", code, err)
	}
}

func TestPlanPrivacyRequest(t *testing.T) {
	base := PrivacyPlanInput{Type: RequestAccess, Regulation: RegulationGDPR, ReceivedAt: privacyNow, Now: privacyNow}
	cases := []struct {
		name     string
		mod      func(*PrivacyPlanInput)
		wantDays int
		wantCode string
	}{
		{name: "GDPR default is 30 days", wantDays: 30},
		{name: "CCPA default is 45 days", mod: func(in *PrivacyPlanInput) { in.Regulation = RegulationCCPA }, wantDays: 45},
		{name: "shorter override", mod: func(in *PrivacyPlanInput) { in.DeadlineDays = 10 }, wantDays: 10},
		{name: "longer override refused", mod: func(in *PrivacyPlanInput) { in.DeadlineDays = 31 }, wantCode: CodePrivacyDeadline},
		{name: "unknown type", mod: func(in *PrivacyPlanInput) { in.Type = "DELETE" }, wantCode: CodePrivacyInput},
		{name: "unknown regulation", mod: func(in *PrivacyPlanInput) { in.Regulation = "LGPD" }, wantCode: CodePrivacyInput},
		{name: "missing receipt", mod: func(in *PrivacyPlanInput) { in.ReceivedAt = time.Time{} }, wantCode: CodePrivacyInput},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := base
			if c.mod != nil {
				c.mod(&in)
			}
			plan, err := PlanPrivacyRequest(in)
			if c.wantCode != "" {
				expectPrivacyCode(t, err, c.wantCode)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if plan.DaysRemaining != c.wantDays || !plan.DeadlineAt.Equal(privacyNow.AddDate(0, 0, c.wantDays)) {
				t.Fatalf("deadline %v days %d, want %d days", plan.DeadlineAt, plan.DaysRemaining, c.wantDays)
			}
			if len(plan.Steps) == 0 {
				t.Fatal("plan must list steps")
			}
		})
	}
}

func TestPlanPrivacyRequestDueSoon(t *testing.T) {
	in := PrivacyPlanInput{Type: RequestAccess, Regulation: RegulationGDPR, ReceivedAt: privacyNow, Now: privacyNow.AddDate(0, 0, 26)}
	plan, err := PlanPrivacyRequest(in)
	if err != nil || !plan.DueSoon || plan.DaysRemaining != 4 {
		t.Fatalf("day 26 of 30 should be due soon with 4 days left: %+v %v", plan, err)
	}
}

func TestApplyPrivacyTransition(t *testing.T) {
	hold := []string{"HOLD-7"}
	ok := []string{"email", "phone"}
	cases := []struct {
		name string
		in   PrivacyTransitionInput
		want PrivacyState
		code string
		late bool
	}{
		{name: "verify with two attributes and link", in: PrivacyTransitionInput{Type: RequestAccess, From: PrivacyReceived, Action: ActionVerify, MatchedAttributes: ok, EmailLinkVerified: true}, want: PrivacyVerified},
		{name: "duplicate attribute counts once", in: PrivacyTransitionInput{Type: RequestAccess, From: PrivacyReceived, Action: ActionVerify, MatchedAttributes: []string{"email", "email"}, EmailLinkVerified: true}, code: CodePrivacyVerify},
		{name: "no e-mail link", in: PrivacyTransitionInput{Type: RequestAccess, From: PrivacyReceived, Action: ActionVerify, MatchedAttributes: ok}, code: CodePrivacyVerify},
		{name: "erasure under hold is held", in: PrivacyTransitionInput{Type: RequestErasure, From: PrivacyVerified, Action: ActionDiscover, HoldIDs: hold}, want: PrivacyHeld},
		{name: "access under hold is discovered", in: PrivacyTransitionInput{Type: RequestAccess, From: PrivacyVerified, Action: ActionDiscover, HoldIDs: hold}, want: PrivacyDiscovered},
		{name: "release refused while hold stands", in: PrivacyTransitionInput{Type: RequestErasure, From: PrivacyHeld, Action: ActionReleaseHold, HoldIDs: hold}, code: CodePrivacyLegalHold},
		{name: "release after hold cleared", in: PrivacyTransitionInput{Type: RequestErasure, From: PrivacyHeld, Action: ActionReleaseHold}, want: PrivacyDiscovered},
		{name: "approve erasure under hold refused", in: PrivacyTransitionInput{Type: RequestErasure, From: PrivacyDiscovered, Action: ActionApprove, HoldIDs: hold}, code: CodePrivacyLegalHold},
		{name: "approve from received refused", in: PrivacyTransitionInput{Type: RequestAccess, From: PrivacyReceived, Action: ActionApprove}, code: CodePrivacyTransition},
		{name: "deliver after approval", in: PrivacyTransitionInput{Type: RequestAccess, From: PrivacyApproved, Action: ActionDeliver}, want: PrivacyDelivered},
		{name: "late close needs justification", in: PrivacyTransitionInput{Type: RequestAccess, From: PrivacyDelivered, Action: PrivacyActionClose, DeadlineAt: privacyNow.Add(-time.Hour), Now: privacyNow}, code: CodePrivacyLate},
		{name: "late close with justification", in: PrivacyTransitionInput{Type: RequestAccess, From: PrivacyDelivered, Action: PrivacyActionClose, DeadlineAt: privacyNow.Add(-time.Hour), Now: privacyNow, Justification: "volume of domains"}, want: PrivacyClosed, late: true},
		{name: "on-time close", in: PrivacyTransitionInput{Type: RequestAccess, From: PrivacyDelivered, Action: PrivacyActionClose, DeadlineAt: privacyNow.Add(time.Hour), Now: privacyNow}, want: PrivacyClosed},
		{name: "reject needs reason", in: PrivacyTransitionInput{Type: RequestAccess, From: PrivacyReceived, Action: ActionReject}, code: CodePrivacyReason},
		{name: "reject with reason", in: PrivacyTransitionInput{Type: RequestAccess, From: PrivacyReceived, Action: ActionReject, Justification: "identity cannot be proven"}, want: PrivacyRejected},
		{name: "reject a closed request", in: PrivacyTransitionInput{Type: RequestAccess, From: PrivacyClosed, Action: ActionReject, Justification: "too late anyway"}, code: CodePrivacyTransition},
		{name: "unknown from state", in: PrivacyTransitionInput{Type: RequestAccess, From: "LIMBO", Action: ActionReject, Justification: "identity cannot be proven"}, code: CodePrivacyTransition},
		{name: "unknown action", in: PrivacyTransitionInput{Type: RequestAccess, From: PrivacyReceived, Action: "PURGE"}, code: CodePrivacyTransition},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := ApplyPrivacyTransition(c.in)
			if c.code != "" {
				expectPrivacyCode(t, err, c.code)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if out.State != c.want || out.Late != c.late {
				t.Fatalf("got %+v, want %s late=%v", out, c.want, c.late)
			}
		})
	}
}

func TestPseudonymizeSubject(t *testing.T) {
	record := map[string]string{"name": "Ada Lovelace", "email": "ada@example.com", "tax_id": "GB123", "amount": "1200.00", "currency": "GBP", "posting_date": "2026-10-01"}
	in := PseudonymizeInput{SubjectRef: "party-42", Record: record}

	held := in
	held.HoldIDs = []string{"HOLD-7"}
	_, err := PseudonymizeSubject(held)
	expectPrivacyCode(t, err, CodePrivacyLegalHold)
	blank := in
	blank.SubjectRef = " "
	_, err = PseudonymizeSubject(blank)
	expectPrivacyCode(t, err, CodePrivacyInput)

	res, err := PseudonymizeSubject(in)
	if err != nil {
		t.Fatal(err)
	}
	if res.Record["amount"] != "1200.00" || res.Record["posting_date"] != "2026-10-01" || res.Record["currency"] != "GBP" {
		t.Fatalf("amounts, dates and dimensions must be kept: %v", res.Record)
	}
	if !strings.HasPrefix(res.Record["email"], "ERASED-") || res.Record["email"] == record["email"] {
		t.Fatalf("e-mail not pseudonymized: %v", res.Record)
	}
	if res.Record["erased"] != "true" || len(res.Pseudonymized) != 3 {
		t.Fatalf("want erased flag and 3 pseudonymized fields: %+v", res)
	}
	again, _ := PseudonymizeSubject(in)
	if again.Record["email"] != res.Record["email"] {
		t.Fatal("pseudonyms must be deterministic per subject and field")
	}
	if record["name"] != "Ada Lovelace" {
		t.Fatal("input record must not be mutated")
	}
}

func TestPseudonymizeSubjectProperty(t *testing.T) {
	keys := append([]string{"amount", "currency", "posting_date", "line_no"}, ErasedFields...)
	rapid.Check(t, func(t *rapid.T) {
		rec := map[string]string{}
		for _, k := range keys {
			if rapid.Bool().Draw(t, "present") {
				rec[k] = rapid.StringMatching(`[A-Za-z0-9 ]{1,12}`).Draw(t, k)
			}
		}
		res, err := PseudonymizeSubject(PseudonymizeInput{SubjectRef: "s", Record: rec})
		if err != nil {
			t.Fatal(err)
		}
		isPII := map[string]bool{}
		for _, f := range ErasedFields {
			isPII[f] = true
		}
		for k, v := range rec {
			if isPII[k] {
				if !strings.HasPrefix(res.Record[k], "ERASED-") {
					t.Fatalf("PII %s not pseudonymized: %q", k, res.Record[k])
				}
			} else if res.Record[k] != v {
				t.Fatalf("non-PII %s changed from %q to %q", k, v, res.Record[k])
			}
		}
	})
}

func TestPurgeCheck(t *testing.T) {
	cases := []struct {
		name string
		in   PurgeCandidate
		want bool
	}{
		{name: "closed period, no hold", in: PurgeCandidate{RecordClass: "AR_INVOICE"}, want: true},
		{name: "legal hold blocks", in: PurgeCandidate{RecordClass: "AR_INVOICE", HoldIDs: []string{"H1"}}},
		{name: "open fiscal period blocks", in: PurgeCandidate{RecordClass: "GL", FiscalPeriodOpen: true}},
		{name: "open document reference blocks", in: PurgeCandidate{RecordClass: "PARTY", OpenDocumentRefs: []string{"SO-9"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := PurgeCheck(c.in)
			if d.Purgeable != c.want {
				t.Fatalf("purgeable %v, want %v (%v)", d.Purgeable, c.want, d.Reasons)
			}
			if !c.want && (d.Code != CodePrivacyPurge || len(d.Reasons) == 0) {
				t.Fatalf("blocked purge needs code and reasons: %+v", d)
			}
		})
	}
}
