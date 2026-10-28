package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/party/internal/api"
	"github.com/BenjaminSRussell/passage/services/party/internal/domain"
	"github.com/BenjaminSRussell/passage/services/party/internal/store"
)

type fakeCredit struct {
	limit     string
	requested string
	decided   string
	approve   bool
	err       error
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

func (f *fakeCredit) DecidePendingChange(_ context.Context, changeID string, approve bool) (domain.PendingChange, error) {
	f.decided, f.approve = changeID, approve
	if f.err != nil {
		return domain.PendingChange{}, f.err
	}
	status := domain.ChangeRejected
	if approve {
		status = domain.ChangeApproved
	}
	return domain.PendingChange{ID: changeID, Status: status}, nil
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
