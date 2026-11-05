package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/compliance/internal/domain"
)

func auditCall(path string, body any, duties ...string) call {
	return call{method: http.MethodPost, path: path, body: body, duties: duties}
}

func decodeInto(t *testing.T, body []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(body, v); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
}

func TestEvaluateAuditKeyword(t *testing.T) {
	h := router(newAPI(&fakeStore{}, true))
	mon := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	req := AuditEvaluateRequest{
		PolicyID: ids.New(),
		Version:  2,
		Spec:     domain.AuditSpec{Type: domain.AuditRuleKeyword, Keywords: []string{"gift cards"}},
		Documents: []domain.AuditDocument{
			{ID: "exp-1", PartyID: "E-1", Amount: decimal.NewFromInt(90), Currency: "USD", Date: mon, Texts: []string{"Gift-cards for the team"}},
			{ID: "exp-2", PartyID: "E-2", Amount: decimal.NewFromInt(10), Currency: "USD", Date: mon, Texts: []string{"taxi"}},
		},
	}
	rec := serve(t, h, auditCall("/v1/compliance/audit-policies:evaluate", req, DutyAuditPolicyRun))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var res AuditEvaluateResponse
	decodeInto(t, rec.Body.Bytes(), &res)
	want := domain.ViolationKey(req.PolicyID, 2, "exp-1")
	if res.Count != 1 || res.Code != "AUD-0701" || res.Findings[0].DocumentID != "exp-1" || res.Findings[0].Key != want {
		t.Fatalf("want one keyword finding on exp-1 keyed %s with AUD-0701, got %+v", want, res)
	}

	none := AuditEvaluateRequest{PolicyID: ids.New(), Version: 1, Spec: domain.AuditSpec{Type: domain.AuditRuleWeekendHoliday}}
	rec = serve(t, h, auditCall("/v1/compliance/audit-policies:evaluate", none, DutyAuditPolicyRun))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"findings":[]`) || strings.Contains(rec.Body.String(), "AUD-0701") {
		t.Fatalf("no documents must return an empty list and no warning: %d %s", rec.Code, rec.Body.String())
	}
}

// TestEvaluateAuditDuplicate runs AC-48.13 through the endpoint on a small
// window: the later of two matching invoices is the one flagged.
func TestEvaluateAuditDuplicate(t *testing.T) {
	h := router(newAPI(&fakeStore{}, true))
	d := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	req := AuditEvaluateRequest{PolicyID: ids.New(), Version: 1, Spec: domain.AuditSpec{Type: domain.AuditRuleDuplicate}, Documents: []domain.AuditDocument{
		{ID: "inv-1", PartyID: "V-1", Number: "INV-0042", Amount: decimal.RequireFromString("1000.00"), Currency: "USD", Date: d},
		{ID: "inv-2", PartyID: "V-1", Number: "inv 42", Amount: decimal.RequireFromString("1003.00"), Currency: "USD", Date: d.AddDate(0, 0, 10)},
		{ID: "inv-3", PartyID: "V-2", Number: "INV-0042", Amount: decimal.RequireFromString("1000.00"), Currency: "USD", Date: d},
	}}
	rec := serve(t, h, auditCall("/v1/compliance/audit-policies:evaluate", req, DutyAuditPolicyRun))
	var res AuditEvaluateResponse
	decodeInto(t, rec.Body.Bytes(), &res)
	if rec.Code != http.StatusOK || res.Count != 1 || res.Findings[0].DocumentID != "inv-2" || res.Findings[0].Evidence["duplicateOf"] != "inv-1" {
		t.Fatalf("want inv-2 flagged as a repeat of inv-1: %d %+v", rec.Code, res)
	}
}

func TestEvaluateAuditRefusals(t *testing.T) {
	h := router(newAPI(&fakeStore{}, true))
	pol := ids.New()
	doc := func(id string) domain.AuditDocument {
		return domain.AuditDocument{ID: id, Date: time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)}
	}
	for _, tc := range []struct {
		name  string
		body  AuditEvaluateRequest
		field string
	}{
		{"policy id not a UUID", AuditEvaluateRequest{PolicyID: "nope", Version: 1, Spec: domain.AuditSpec{Type: domain.AuditRuleKeyword}}, "policyId"},
		{"version zero", AuditEvaluateRequest{PolicyID: pol, Spec: domain.AuditSpec{Type: domain.AuditRuleKeyword}}, "version"},
		{"no rule type", AuditEvaluateRequest{PolicyID: pol, Version: 1}, "spec.type"},
		{"document without id", AuditEvaluateRequest{PolicyID: pol, Version: 1, Spec: domain.AuditSpec{Type: domain.AuditRuleKeyword}, Documents: []domain.AuditDocument{doc("")}}, "documents[0].id"},
		{"repeated document", AuditEvaluateRequest{PolicyID: pol, Version: 1, Spec: domain.AuditSpec{Type: domain.AuditRuleKeyword}, Documents: []domain.AuditDocument{doc("a"), doc("a")}}, "documents[1].id"},
		{"dated rule without date", AuditEvaluateRequest{PolicyID: pol, Version: 1, Spec: domain.AuditSpec{Type: domain.AuditRuleWeekendHoliday}, Documents: []domain.AuditDocument{{ID: "a"}}}, "documents[0].date"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := serve(t, h, auditCall("/v1/compliance/audit-policies:evaluate", tc.body, DutyAuditPolicyRun))
			if rec.Code != http.StatusBadRequest || problemCode(t, rec) != codeInvalid || !strings.Contains(rec.Body.String(), fmt.Sprintf("%q", tc.field)) {
				t.Fatalf("want 400 on %s, got %d %s", tc.field, rec.Code, rec.Body.String())
			}
		})
	}
	for _, tc := range []struct {
		spec domain.AuditSpec
		code string
	}{
		{domain.AuditSpec{Type: "MYSTERY"}, domain.CodeAuditRuleType},
		{domain.AuditSpec{Type: domain.AuditRuleThreshold, Currency: "USD"}, domain.CodeAuditRuleConfig},
	} {
		rec := serve(t, h, auditCall("/v1/compliance/audit-policies:evaluate", AuditEvaluateRequest{PolicyID: pol, Version: 1, Spec: tc.spec}, DutyAuditPolicyRun))
		if rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != tc.code {
			t.Fatalf("%s: want 422 %s, got %d %s", tc.spec.Type, tc.code, rec.Code, rec.Body.String())
		}
	}
}

func TestGroupCases(t *testing.T) {
	h := router(newAPI(&fakeStore{}, true))
	pol := ids.New()
	req := GroupCasesRequest{Violations: []domain.Violation{
		{ID: "v2", PolicyID: pol, RuleType: domain.AuditRuleDuplicate, DocumentID: "inv-2", PartyID: "V-1"},
		{ID: "v1", PolicyID: pol, RuleType: domain.AuditRuleDuplicate, DocumentID: "inv-1", PartyID: "V-1"},
		{ID: "v3", PolicyID: pol, RuleType: domain.AuditRuleDuplicate, DocumentID: "inv-9", PartyID: "V-1", CaseID: "case-1"},
	}}
	rec := serve(t, h, auditCall("/v1/compliance/audit-cases:group", req, DutyAuditCaseManage))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var res GroupCasesResponse
	decodeInto(t, rec.Body.Bytes(), &res)
	// AC-48.13: one case per vendor; a violation already in a case is left alone.
	if len(res.Cases) != 1 || res.Cases[0].Key != "DUPLICATE_PAYMENT:V-1" || fmt.Sprint(res.Cases[0].ViolationIDs) != "[v1 v2]" {
		t.Fatalf("want one vendor case with v1 and v2: %+v", res.Cases)
	}

	rec = serve(t, h, auditCall("/v1/compliance/audit-cases:group", GroupCasesRequest{}, DutyAuditCaseManage))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"cases":[]`) {
		t.Fatalf("empty grouping: %d %s", rec.Code, rec.Body.String())
	}
}

func TestGroupCasesRefusals(t *testing.T) {
	h := router(newAPI(&fakeStore{}, true))
	pol := ids.New()
	for _, tc := range []struct {
		name  string
		vs    []domain.Violation
		field string
	}{
		{"missing id", []domain.Violation{{PolicyID: pol, RuleType: domain.AuditRuleKeyword, DocumentID: "d"}}, "violations[0].id"},
		{"repeated id", []domain.Violation{{ID: "v", PolicyID: pol, RuleType: domain.AuditRuleKeyword, DocumentID: "d"}, {ID: "v", PolicyID: pol, RuleType: domain.AuditRuleKeyword, DocumentID: "e"}}, "violations[1].id"},
		{"duplicate without vendor", []domain.Violation{{ID: "v", PolicyID: pol, RuleType: domain.AuditRuleDuplicate, DocumentID: "d"}}, "violations[0].partyId"},
	} {
		rec := serve(t, h, auditCall("/v1/compliance/audit-cases:group", GroupCasesRequest{Violations: tc.vs}, DutyAuditCaseManage))
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), fmt.Sprintf("%q", tc.field)) {
			t.Fatalf("%s: want 400 on %s, got %d %s", tc.name, tc.field, rec.Code, rec.Body.String())
		}
	}
	rec := serve(t, h, auditCall("/v1/compliance/audit-cases:group", GroupCasesRequest{Violations: []domain.Violation{{ID: "v", PolicyID: pol, RuleType: "MYSTERY", DocumentID: "d"}}}, DutyAuditCaseManage))
	if rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != domain.CodeAuditRuleType {
		t.Fatalf("unknown rule type: %d %s", rec.Code, rec.Body.String())
	}
}

func TestTransitionCase(t *testing.T) {
	h := router(newAPI(&fakeStore{}, true))
	path := "/v1/compliance/audit-cases:transition"
	decode := func(t *testing.T, body []byte) CaseTransitionResponse {
		var res CaseTransitionResponse
		decodeInto(t, body, &res)
		return res
	}

	rec := serve(t, h, auditCall(path, CaseTransitionRequest{Status: domain.CaseOpen, Action: domain.ActionStartReview}, DutyAuditCaseManage))
	if rec.Code != http.StatusOK || decode(t, rec.Body.Bytes()).Status != domain.CaseInReview {
		t.Fatalf("start review: %d %s", rec.Code, rec.Body.String())
	}

	resolve := CaseTransitionRequest{Status: domain.CaseInReview, Action: domain.ActionResolveIssue, ResolutionCode: "DUPLICATE_RECOVERED",
		RecoveryAmount: decimal.RequireFromString("1250.50"), Currency: "USD"}
	rec = serve(t, h, auditCall(path, resolve, DutyAuditCaseManage))
	if got := decode(t, rec.Body.Bytes()); rec.Code != http.StatusOK || got.Status != domain.CaseResolvedIssue || got.RecoveryAmount.String() != "1250.5" {
		t.Fatalf("resolve: %d %s", rec.Code, rec.Body.String())
	}

	// CLOSE reads the resolution already on the case and stamps the server time.
	rec = serve(t, h, auditCall(path, CaseTransitionRequest{Status: domain.CaseResolvedIssue, Action: domain.ActionClose, ResolutionCode: "DUPLICATE_RECOVERED"}, DutyAuditCaseManage))
	if got := decode(t, rec.Body.Bytes()); rec.Code != http.StatusOK || got.Status != domain.CaseClosed || got.ClosedAt == nil || !got.ClosedAt.Equal(now) || !got.AsOf.Equal(now) {
		t.Fatalf("close: %d %s", rec.Code, rec.Body.String())
	}
	rec = serve(t, h, auditCall(path, CaseTransitionRequest{Status: domain.CaseResolvedIssue, Action: domain.ActionClose}, DutyAuditCaseManage))
	if rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != domain.CodeCaseResolution {
		t.Fatalf("close without resolution: %d %s", rec.Code, rec.Body.String())
	}

	// ARCHIVE uses audit.case_archive_days (365 by default) against the server clock.
	early := now.AddDate(0, 0, -364)
	rec = serve(t, h, auditCall(path, CaseTransitionRequest{Status: domain.CaseClosed, Action: domain.ActionArchive, ClosedAt: &early}, DutyAuditCaseManage))
	if rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != domain.CodeCaseArchiveTooSoon {
		t.Fatalf("archive too soon: %d %s", rec.Code, rec.Body.String())
	}
	old := now.AddDate(0, 0, -365)
	rec = serve(t, h, auditCall(path, CaseTransitionRequest{Status: domain.CaseClosed, Action: domain.ActionArchive, ClosedAt: &old}, DutyAuditCaseManage))
	if rec.Code != http.StatusOK || decode(t, rec.Body.Bytes()).Status != domain.CaseArchived {
		t.Fatalf("archive: %d %s", rec.Code, rec.Body.String())
	}
	a := newAPI(&fakeStore{}, true)
	a.cfg.CaseArchiveDays = 30
	rec = serve(t, router(a), auditCall(path, CaseTransitionRequest{Status: domain.CaseClosed, Action: domain.ActionArchive, ClosedAt: &early}, DutyAuditCaseManage))
	if rec.Code != http.StatusOK {
		t.Fatalf("tenant archive window of 30 days: %d %s", rec.Code, rec.Body.String())
	}

	rec = serve(t, h, auditCall(path, CaseTransitionRequest{Status: domain.CaseOpen, Action: domain.ActionClose}, DutyAuditCaseManage))
	if rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != domain.CodeCaseTransition {
		t.Fatalf("close from OPEN must be refused: %d %s", rec.Code, rec.Body.String())
	}
	future := now.Add(time.Hour)
	for _, bad := range []CaseTransitionRequest{
		{Status: "LOST", Action: domain.ActionStartReview},
		{Status: domain.CaseOpen, Action: "SHRED"},
		{Status: domain.CaseInReview, Action: domain.ActionResolveIssue, ResolutionCode: "POLICY_CORRECTED", Currency: "usd"},
		{Status: domain.CaseClosed, Action: domain.ActionArchive, ClosedAt: &future},
	} {
		if rec := serve(t, h, auditCall(path, bad, DutyAuditCaseManage)); rec.Code != http.StatusBadRequest {
			t.Fatalf("%+v: want 400, got %d %s", bad, rec.Code, rec.Body.String())
		}
	}
}

func planCall(user string, body any) call {
	return call{method: http.MethodPost, path: "/v1/security/elevated-sessions:plan", body: body, user: user, duties: []string{DutyBreakGlassRequest}}
}

func TestPlanElevatedSession(t *testing.T) {
	h := router(newAPI(&fakeStore{}, true))
	me := ids.New()
	req := ElevatedPlanRequest{Roles: []string{"QualityInspector"}, ReasonCode: "INCIDENT_RESPONSE", TicketRef: "INC-4411"}
	rec := serve(t, h, planCall(me, req))
	if rec.Code != http.StatusOK {
		t.Fatalf("plan: %d %s", rec.Code, rec.Body.String())
	}
	var plan ElevatedPlanResponse
	decodeInto(t, rec.Body.Bytes(), &plan)
	// AC-48.18: a 60-minute session from the server time, reviewed within 3 business days.
	if plan.RequesterID != me || plan.TargetUserID != me || plan.Minutes != 60 || !plan.StartsAt.Equal(now) ||
		!plan.EndsAt.Equal(now.Add(time.Hour)) || !plan.ReviewDueAt.Equal(domain.AddBusinessDays(now.Add(time.Hour), 3)) ||
		!plan.NeedsApproval || plan.ApproverDuty != domain.ApproverBreakGlass {
		t.Fatalf("plan: %+v", plan)
	}

	// The tenant setting, not the request, decides self-activation and the cap.
	a := newAPI(&fakeStore{}, true)
	a.cfg.BreakGlassSelfActivate, a.cfg.BreakGlassMaxMinutes = true, 30
	rec = serve(t, router(a), planCall(me, req))
	decodeInto(t, rec.Body.Bytes(), &plan)
	if rec.Code != http.StatusOK || plan.NeedsApproval || !plan.RequiresPostReview || plan.Minutes != 30 {
		t.Fatalf("self-activated tenant: %d %s", rec.Code, rec.Body.String())
	}
	long := req
	long.RequestedMinutes = 60
	if rec := serve(t, router(a), planCall(me, long)); rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != domain.CodeBreakGlassRequest {
		t.Fatalf("over the tenant cap: %d %s", rec.Code, rec.Body.String())
	}
}

func TestPlanElevatedSessionRefusals(t *testing.T) {
	h := router(newAPI(&fakeStore{}, true))
	me := ids.New()
	self := ElevatedPlanRequest{Roles: []string{"compliance.security.admin"}, ReasonCode: "INCIDENT_RESPONSE", TicketRef: "INC-4411"}
	// BR-48.19: naming yourself explicitly, in any case, is still a self-grant.
	for _, target := range []string{"", strings.ToUpper(me)} {
		body := self
		body.TargetUserID = target
		rec := serve(t, h, planCall(me, body))
		if rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != domain.CodeBreakGlassSelfGrant {
			t.Fatalf("self grant (target %q) must be refused: %d %s", target, rec.Code, rec.Body.String())
		}
	}
	other := self
	other.TargetUserID = ids.New()
	rec := serve(t, h, planCall(me, other))
	var plan ElevatedPlanResponse
	decodeInto(t, rec.Body.Bytes(), &plan)
	if rec.Code != http.StatusOK || plan.ApproverDuty != domain.ApproverPlatformAdmin {
		t.Fatalf("a protected role for another user waits for a second platform.global.admin: %d %s", rec.Code, rec.Body.String())
	}

	if rec := serve(t, h, planCall("", self)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no user identity: %d", rec.Code)
	}
	bad := self
	bad.TargetUserID = "someone"
	if rec := serve(t, h, planCall(me, bad)); rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed target: %d", rec.Code)
	}
	noTicket := ElevatedPlanRequest{Roles: []string{"QualityInspector"}, ReasonCode: "INCIDENT_RESPONSE"}
	if rec := serve(t, h, planCall(me, noTicket)); rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != domain.CodeBreakGlassRequest {
		t.Fatalf("missing ticket: %d %s", rec.Code, rec.Body.String())
	}
}
