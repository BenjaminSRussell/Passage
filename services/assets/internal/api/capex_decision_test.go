package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/assets/internal/domain"
	"github.com/BenjaminSRussell/passage/services/assets/internal/store"
)

// DecideCapexRequest mirrors the store: version check, the domain move, the
// segregation-of-duties check and the approved amount on approve.
func (f *fakeStore) DecideCapexRequest(ctx context.Context, id string, in store.CapexDecisionInput) (store.CapexRequest, error) {
	f.lastDecision = in
	c, ok := f.capex[id]
	if !ok {
		return store.CapexRequest{}, httpx.NewProblem(http.StatusNotFound, store.CodeCapexNotFound, "no capex request")
	}
	if c.Version != in.Version {
		return store.CapexRequest{}, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersionConflict, "stale")
	}
	next, err := in.Decision.Apply(domain.CapexStatus(c.Status))
	if err != nil {
		return store.CapexRequest{}, err
	}
	p, _ := tenant.FromContext(ctx)
	if in.Decision.IsApproverDecision() && c.RequesterID == p.UserID {
		return store.CapexRequest{}, httpx.NewProblem(http.StatusForbidden, store.CodeCapexOwnDecision, "own request")
	}
	if in.Decision == domain.DecisionApprove {
		c.ApprovedAmount = c.RequestedAmount
	}
	c.Status, c.Version = string(next), c.Version+1
	c.Decisions = append(c.Decisions, store.CapexDecisionRecord{Decision: string(in.Decision), Comment: in.Comment, DecidedBy: p.UserID})
	f.capex[id] = c
	return c, nil
}

func (f *fakeStore) MidQuarterTest(_ context.Context, in store.MidQuarterFilter) (store.MidQuarterReport, error) {
	f.lastMidQuarter = in
	return store.MidQuarterReport{LegalEntityID: in.LegalEntityID, BookCode: in.BookCode, Review: []store.MidQuarterBook{},
		MidQuarterResult: domain.MidQuarterTest(in.Year, nil)}, nil
}

const approverDuties = "CapexRequestApprove"

// inApproval stores a submitted 320,000 request (requester "plant.manager").
func inApproval(f *fakeStore) string {
	amount, level, variant := decimal.RequireFromString("320000"), "CFO", ids.New()
	id := ids.New()
	f.capex[id] = store.CapexRequest{ID: id, LegalEntityID: ids.New(), Number: "CR-1", RequesterID: "plant.manager", Status: string(domain.CapexInApproval),
		Currency: "USD", SelectedVariantID: &variant, ApprovalLevel: &level, RequestedAmount: &amount, Version: 2}
	return id
}

func decide(h http.Handler, id, action, body, duties, user, etag string) *httptest.ResponseRecorder {
	headers := map[string]string{"X-Passage-User": user}
	if etag != "" {
		headers["If-Match"] = etag
	}
	return do(h, req{method: http.MethodPost, path: "/v1/assets/capex-requests/" + id + "/" + action, body: body, duties: duties, headers: headers})
}

func TestCapexDecision_ApproveReleaseClose(t *testing.T) {
	f := newFake()
	h := server(f)
	id := inApproval(f)

	rec := decide(h, id, "approve", "", approverDuties, "cfo", `"2"`)
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"3"` {
		t.Fatalf("approve status = %d etag %q body %s", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
	}
	var approved store.CapexRequest
	if err := json.Unmarshal(rec.Body.Bytes(), &approved); err != nil {
		t.Fatal(err)
	}
	if approved.Status != "Approved" || approved.ApprovedAmount == nil || approved.ApprovedAmount.StringFixed(2) != "320000.00" {
		t.Fatalf("approved = %+v", approved)
	}
	if f.lastDecision.Decision != domain.DecisionApprove || f.lastDecision.Version != 2 || f.lastDecision.Comment != nil {
		t.Fatalf("store got %+v", f.lastDecision)
	}
	// Reject after approval is refused by the state machine.
	if rec := decide(h, id, "reject", `{"comment":"too late"}`, approverDuties, "cfo", `"3"`); rec.Code != http.StatusUnprocessableEntity || problem(t, rec).Code != domain.CodeCapexState {
		t.Fatalf("reject after approve status = %d body %s", rec.Code, rec.Body.String())
	}
	if rec := decide(h, id, "release", `{"comment":"PO raised"}`, approverDuties, "cfo", `"3"`); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"Released"`) {
		t.Fatalf("release status = %d body %s", rec.Code, rec.Body.String())
	}
	if *f.lastDecision.Comment != "PO raised" {
		t.Fatalf("comment = %q", *f.lastDecision.Comment)
	}
	if rec := decide(h, id, "close", "{}", approverDuties, "cfo", `"4"`); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"Closed"`) {
		t.Fatalf("close status = %d body %s", rec.Code, rec.Body.String())
	}
}

func TestCapexDecision_PostponeNeedsReasonAndReopenIsTheRequesters(t *testing.T) {
	f := newFake()
	h := server(f)
	id := inApproval(f)
	for _, action := range []string{"postpone", "reject"} {
		rec := decide(h, id, action, `{"comment":"  "}`, approverDuties, "cfo", `"2"`)
		if rec.Code != http.StatusBadRequest || problem(t, rec).Errors["comment"] == "" {
			t.Fatalf("%s without a reason status = %d body %s", action, rec.Code, rec.Body.String())
		}
	}
	if rec := decide(h, id, "postpone", `{"comment":"`+strings.Repeat("x", 1001)+`"}`, approverDuties, "cfo", `"2"`); rec.Code != http.StatusBadRequest {
		t.Fatalf("1001-character comment status = %d", rec.Code)
	}
	if rec := decide(h, id, "postpone", `{"comment":"Next budget year"}`, approverDuties, "cfo", `"2"`); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"Postponed"`) {
		t.Fatalf("postpone status = %d body %s", rec.Code, rec.Body.String())
	}
	// Approve from Postponed is not a legal move.
	if rec := decide(h, id, "approve", "", approverDuties, "cfo", `"3"`); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("approve a postponed request status = %d", rec.Code)
	}
	// Reopen is the requester's action: CapexRequestCreate, not the approver duty.
	if rec := decide(h, id, "reopen", "", approverDuties, "cfo", `"3"`); rec.Code != http.StatusForbidden {
		t.Fatalf("reopen with the approver duty status = %d", rec.Code)
	}
	if rec := decide(h, id, "reopen", "", capexDuties, "plant.manager", `"3"`); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"Draft"`) {
		t.Fatalf("reopen status = %d body %s", rec.Code, rec.Body.String())
	}
}

func TestCapexDecision_GuardsDutyUserVersionAndSoD(t *testing.T) {
	f := newFake()
	h := server(f)
	id := inApproval(f)
	cases := []struct {
		name, duties, user, etag string
		status                   int
		code                     string
	}{
		{"requester duty cannot approve", capexDuties, "cfo", `"2"`, http.StatusForbidden, ""},
		{"no user", approverDuties, "", `"2"`, http.StatusUnauthorized, "AUTH-0001"},
		{"no If-Match", approverDuties, "cfo", "", http.StatusPreconditionRequired, store.CodeVersionConflict},
		{"stale If-Match", approverDuties, "cfo", `"1"`, http.StatusPreconditionFailed, store.CodeVersionConflict},
		{"approver is the requester", approverDuties, "plant.manager", `"2"`, http.StatusForbidden, store.CodeCapexOwnDecision},
	}
	for _, tc := range cases {
		rec := decide(h, id, "approve", "", tc.duties, tc.user, tc.etag)
		if rec.Code != tc.status || (tc.code != "" && problem(t, rec).Code != tc.code) {
			t.Errorf("%s: status %d body %s", tc.name, rec.Code, rec.Body.String())
		}
	}
	if rec := decide(h, id, "approve", `{"note":"x"}`, approverDuties, "cfo", `"2"`); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown body field status = %d", rec.Code)
	}
	if rec := decide(h, ids.New(), "approve", "", approverDuties, "cfo", `"1"`); rec.Code != http.StatusNotFound {
		t.Errorf("unknown request status = %d", rec.Code)
	}
	if rec := decide(h, "not-a-uuid", "approve", "", approverDuties, "cfo", `"1"`); rec.Code != http.StatusNotFound {
		t.Errorf("malformed id status = %d", rec.Code)
	}
	if f.capex[id].Status != "InApproval" {
		t.Fatalf("a refused decision changed the request: %+v", f.capex[id])
	}
	// The approver can read the requests it decides.
	if rec := call(h, http.MethodGet, "/v1/assets/capex-requests/"+id, "", approverDuties, false); rec.Code != http.StatusOK {
		t.Errorf("approver read status = %d", rec.Code)
	}
}

func TestMidQuarterTest_Validation(t *testing.T) {
	f := newFake()
	h := server(f)
	le := testLE
	path := "/v1/assets/mid-quarter-test?legalEntityId=" + le + "&bookCode=TAX&year=2026"
	if rec := call(h, http.MethodGet, path, "", "AssetMaintain", false); rec.Code != http.StatusForbidden {
		t.Fatalf("without AssetView or AssetDepreciate status = %d", rec.Code)
	}
	rec := call(h, http.MethodGet, path, "", "AssetDepreciate", false)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"applies":false`) || !strings.Contains(rec.Body.String(), `"quarterBasis":["0","0","0","0"]`) {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	if f.lastMidQuarter != (store.MidQuarterFilter{LegalEntityID: le, BookCode: "TAX", Year: 2026}) {
		t.Fatalf("store got %+v", f.lastMidQuarter)
	}
	bad := call(h, http.MethodGet, "/v1/assets/mid-quarter-test?legalEntityId=x&bookCode=tax&year=26", "", "AssetView", false)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("invalid query status = %d", bad.Code)
	}
	if errs := problem(t, bad).Errors; errs["legalEntityId"] == "" || errs["bookCode"] == "" || errs["year"] == "" {
		t.Fatalf("errors = %v", errs)
	}
}
