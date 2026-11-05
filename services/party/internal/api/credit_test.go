package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/party/internal/api"
	"github.com/BenjaminSRussell/passage/services/party/internal/domain"
	"github.com/BenjaminSRussell/passage/services/party/internal/store"
)

type fakeCredit struct {
	limit      string
	requested  string
	decided    string
	approve    bool
	replayed   bool
	configured string
	err        error
}

func TestCreditRoutes_ProtectedFields(t *testing.T) {
	f := &fakeCredit{}
	r := creditRouter(f)
	path := "/v1/party/legal-entities/" + knownID + "/protected-fields"
	rec := do(r, http.MethodGet, path, "", "party.read", true)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"fields":["credit_limit"]`) || f.configured != knownID {
		t.Fatalf("get: %d %s", rec.Code, rec.Body)
	}
	rec = do(r, http.MethodPut, path, `{"fields":[]}`, "party.approval-policy.write", true)
	if rec.Code != http.StatusOK || f.configured != knownID+"/" {
		t.Fatalf("put: %d %s configured=%q", rec.Code, rec.Body, f.configured)
	}
	f.configured = ""
	for _, tc := range []struct {
		name, method, body, duties string
		tenant                     bool
		want                       int
	}{
		{"put needs party.approval-policy.write", http.MethodPut, `{"fields":[]}`, "party.write", true, http.StatusForbidden},
		{"party.approve cannot reconfigure", http.MethodPut, `{"fields":[]}`, "party.approve", true, http.StatusForbidden},
		{"empty object", http.MethodPut, `{}`, "party.approval-policy.write", true, http.StatusUnprocessableEntity},
		{"null fields", http.MethodPut, `{"fields":null}`, "party.approval-policy.write", true, http.StatusUnprocessableEntity},
		{"get needs party.read", http.MethodGet, "", "party.write", true, http.StatusForbidden},
		{"put without principal", http.MethodPut, `{"fields":[]}`, "", false, http.StatusUnauthorized},
		{"unknown body field", http.MethodPut, `{"field":[]}`, "party.approval-policy.write", true, http.StatusBadRequest},
	} {
		if rec := do(r, tc.method, path, tc.body, tc.duties, tc.tenant); rec.Code != tc.want || f.configured != "" {
			t.Fatalf("%s: %d %s configured=%q", tc.name, rec.Code, rec.Body, f.configured)
		}
	}
	f.err = httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeFieldsInvalid, "no")
	if rec := do(r, http.MethodPut, path, `{"fields":["x"]}`, "party.approval-policy.write", true); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), store.CodeFieldsInvalid) {
		t.Fatalf("store problem: %d %s", rec.Code, rec.Body)
	}
}

func (f *fakeCredit) RequestCreditLimit(_ context.Context, customerID, limit string) (domain.CreditLimitResult, error) {
	f.requested = customerID + "/" + limit
	if f.err != nil {
		return domain.CreditLimitResult{}, f.err
	}
	current := f.limit
	if len(limit) > len(current) {
		return domain.CreditLimitResult{CreditLimit: &current, Pending: &domain.PendingChange{ID: knownID, Status: domain.ChangePending, RequestedValue: limit}}, nil
	}
	return domain.CreditLimitResult{CreditLimit: &limit}, nil
}

func (f *fakeCredit) GetPendingChange(_ context.Context, changeID string) (domain.PendingChange, error) {
	if f.err != nil {
		return domain.PendingChange{}, f.err
	}
	return domain.PendingChange{ID: changeID, Status: domain.ChangePending}, nil
}

func (f *fakeCredit) DecidePendingChange(_ context.Context, changeID string, approve bool, _ string) (domain.PendingChange, error) {
	f.decided, f.approve = changeID, approve
	if f.err != nil {
		return domain.PendingChange{}, f.err
	}
	status := domain.ChangeRejected
	if approve {
		status = domain.ChangeApproved
	}
	return domain.PendingChange{ID: changeID, Status: status, Replayed: f.replayed}, nil
}

func (f *fakeCredit) ProtectedFields(_ context.Context, legalEntityID string) ([]string, error) {
	f.configured = legalEntityID
	return []string{domain.FieldCreditLimit}, f.err
}

func (f *fakeCredit) SetProtectedFields(_ context.Context, legalEntityID string, fields []string) ([]string, error) {
	f.configured = legalEntityID + "/" + strings.Join(fields, ",")
	return fields, f.err
}

func creditRouter(f *fakeCredit) http.Handler {
	r := httpx.NewRouter()
	api.New(&stubChecker{}, stubPinger{}, &fakeParties{}, &fakeSequencer{}).WithCredit(f).Register(r)
	return r
}

func TestCreditRoutes_IncreaseIs202AndDecreaseIs200(t *testing.T) {
	f := &fakeCredit{limit: "50"}
	r := creditRouter(f)
	path := "/v1/party/customers/" + knownID + "/credit-limit"
	rec := do(r, http.MethodPost, path, `{"creditLimit":"120"}`, "party.write", true)
	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"creditLimit":"50"`) || !strings.Contains(rec.Body.String(), `"status":"pending"`) {
		t.Fatalf("increase: %d %s", rec.Code, rec.Body)
	}
	rec = do(r, http.MethodPost, path, `{"creditLimit":"10"}`, "party.write", true)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"creditLimit":"10"`) {
		t.Fatalf("decrease: %d %s", rec.Code, rec.Body)
	}
}

func TestCreditRoutes_ApproveAndReject(t *testing.T) {
	for action, wantApprove := range map[string]bool{"approve": true, "reject": false} {
		f := &fakeCredit{}
		rec := do(creditRouter(f), http.MethodPost, "/v1/party/pending-changes/"+knownID+":"+action, "", "party.approve", true)
		if rec.Code != http.StatusOK || f.decided != knownID || f.approve != wantApprove {
			t.Fatalf("%s: %d %s decided=%q approve=%v", action, rec.Code, rec.Body, f.decided, f.approve)
		}
	}
}

func TestCreditRoutes_GetPendingChangeNeedsPartyRead(t *testing.T) {
	f := &fakeCredit{}
	r := creditRouter(f)
	path := "/v1/party/pending-changes/" + knownID
	rec := do(r, http.MethodGet, path, "", "party.read", true)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"id":"`+knownID+`"`) {
		t.Fatalf("get pending change: %d %s", rec.Code, rec.Body)
	}
	rec = do(r, http.MethodGet, path, "", "party.write", true)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("get without party.read: %d %s", rec.Code, rec.Body)
	}
}

func TestCreditRoutes_DecisionReplayDoesNotIncrementMetrics(t *testing.T) {
	f := &fakeCredit{}
	a := api.New(&stubChecker{}, stubPinger{}, &fakeParties{}, &fakeSequencer{}).WithCredit(f)
	r := httpx.NewRouter()
	a.Register(r)
	path := "/v1/party/pending-changes/" + knownID + ":approve"
	for _, replayed := range []bool{false, true} {
		f.replayed = replayed
		if rec := do(r, http.MethodPost, path, "", "party.approve", true); rec.Code != http.StatusOK {
			t.Fatalf("approve (replayed=%v): %d %s", replayed, rec.Code, rec.Body)
		}
	}
	rec := httptest.NewRecorder()
	a.Metrics().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(rec.Body.String(), `outcome="approved"} 1`+"\n") {
		t.Fatalf("decision counter after one fresh decision and one replay is not exactly 1: %s", rec.Body)
	}
}

func TestCreditRoutes_GetPendingChangeUnknownIs404(t *testing.T) {
	f := &fakeCredit{err: httpx.NewProblem(http.StatusNotFound, store.CodeChangeNotFound, "pending change not found")}
	rec := do(creditRouter(f), http.MethodGet, "/v1/party/pending-changes/"+knownID, "", "party.read", true)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), store.CodeChangeNotFound) {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}

func TestCreditRoutes_Authorization(t *testing.T) {
	f := &fakeCredit{}
	r := creditRouter(f)
	tests := []struct {
		name, path, duties, body string
		tenant                   bool
		want                     int
	}{
		{"request needs party.write", "/v1/party/customers/" + knownID + "/credit-limit", "party.read", `{"creditLimit":"1"}`, true, http.StatusForbidden},
		{"approve needs party.approve", "/v1/party/pending-changes/" + knownID + ":approve", "party.write", "", true, http.StatusForbidden},
		{"reject needs party.approve", "/v1/party/pending-changes/" + knownID + ":reject", "party.write", "", true, http.StatusForbidden},
		{"no principal", "/v1/party/pending-changes/" + knownID + ":approve", "", "", false, http.StatusUnauthorized},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(r, http.MethodPost, tc.path, tc.body, tc.duties, tc.tenant)
			if rec.Code != tc.want || f.decided != "" || f.requested != "" {
				t.Fatalf("got %d %s decided=%q requested=%q", rec.Code, rec.Body, f.decided, f.requested)
			}
		})
	}
}

func TestCreditRoutes_StoreProblemsPassThrough(t *testing.T) {
	f := &fakeCredit{err: httpx.NewProblem(http.StatusForbidden, store.CodeSelfApproval, "no")}
	rec := do(creditRouter(f), http.MethodPost, "/v1/party/pending-changes/"+knownID+":approve", "", "party.approve", true)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), store.CodeSelfApproval) {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	rec = do(creditRouter(f), http.MethodPost, "/v1/party/customers/"+knownID+"/credit-limit", `{"creditLimit":1}`, "party.write", true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("a JSON number must be rejected: %d %s", rec.Code, rec.Body)
	}
}
