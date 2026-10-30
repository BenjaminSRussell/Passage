package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

// policyLog records what the policy methods received; saved is what GetPolicy
// answers when set, err what every policy method answers, and actErr what
// ActOnPurchaseOrder answers when set.
type policyLog struct {
	put     *domain.Policy
	ifMatch int
	saved   *domain.Policy
	err     error
	actErr  error
}

func (f *fakeStore) GetPolicy(_ context.Context, le string) (domain.Policy, error) {
	if f.pol.saved != nil {
		return *f.pol.saved, f.pol.err
	}
	return domain.DefaultPolicy(le), f.pol.err
}

func (f *fakeStore) ListPolicies(context.Context) ([]domain.Policy, error) {
	if f.pol.saved == nil {
		return []domain.Policy{}, f.pol.err
	}
	return []domain.Policy{*f.pol.saved}, f.pol.err
}

func (f *fakeStore) PutPolicy(_ context.Context, in domain.Policy, ifMatch int) (domain.Policy, error) {
	f.pol.put, f.pol.ifMatch = &in, ifMatch
	in.Version = ifMatch + 1
	return in, f.pol.err
}

const policyPath = "/v1/procurement/purchasing-policies/" + leA

func TestPolicy_GetReturnsDefaultsAtVersionZero(t *testing.T) {
	h := newHarness(t)
	rec := h.do(call{method: "GET", path: policyPath, duties: []string{dutyPurchaseOrderInquire}})
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"0"` {
		t.Fatalf("status %d ETag %q: %s", rec.Code, rec.Header().Get("ETag"), rec.Body)
	}
	var got domain.Policy
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if !got.ChangeManagementDefault || got.POFourEyes || got.AllowInvoiceBeforeReceipt || got.LegalEntityID != leA {
		t.Errorf("policy = %+v", got)
	}
}

func TestPolicy_PutNeedsDutyIfMatchAndAllFlags(t *testing.T) {
	h := newHarness(t)
	full := `{"changeManagementDefault":false,"poFourEyes":true,"allowInvoiceBeforeReceipt":true}`
	maintain := []string{dutyPolicyMaintain}
	for _, tc := range []struct {
		name   string
		c      call
		status int
		code   string
	}{
		{"inquire duty cannot save", call{method: "PUT", path: policyPath, user: userA, duties: []string{dutyPurchaseOrderInquire}, body: full, headers: map[string]string{"If-Match": "0"}}, 403, "AUTH-0003"},
		{"If-Match missing", call{method: "PUT", path: policyPath, user: userA, duties: maintain, body: full}, 428, codeIfMatchMissing},
		{"flag missing", call{method: "PUT", path: policyPath, user: userA, duties: maintain, body: `{"poFourEyes":true}`, headers: map[string]string{"If-Match": "0"}}, 422, codeInvalid},
		{"entity outside scope", call{method: "PUT", path: "/v1/procurement/purchasing-policies/00000000-0000-7000-8000-0000000001e2", user: userA, duties: maintain, body: full, headers: map[string]string{"If-Match": "0"}}, 403, codeScope},
		{"bad entity id", call{method: "GET", path: "/v1/procurement/purchasing-policies/nope", duties: maintain}, 422, codeInvalid},
	} {
		rec := h.do(tc.c)
		if rec.Code != tc.status || problemCode(t, rec) != tc.code {
			t.Errorf("%s: status %d code %s: %s", tc.name, rec.Code, problemCode(t, rec), rec.Body)
		}
	}
	rec := h.do(call{method: "PUT", path: policyPath, user: userA, duties: maintain, body: full, headers: map[string]string{"If-Match": "0"}})
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("save: status %d ETag %q: %s", rec.Code, rec.Header().Get("ETag"), rec.Body)
	}
	if got := h.fake.pol.put; got == nil || got.ChangeManagementDefault || !got.POFourEyes || !got.AllowInvoiceBeforeReceipt || got.LegalEntityID != leA {
		t.Errorf("store got %+v", got)
	}
}

func TestPolicy_StalePutIs412(t *testing.T) {
	h := newHarness(t)
	h.fake.pol.err = httpx.NewProblem(http.StatusPreconditionFailed, store.CodePolicyStale, "stale")
	rec := h.do(call{method: "PUT", path: policyPath, user: userA, duties: []string{dutyPolicyMaintain},
		body: `{"changeManagementDefault":true,"poFourEyes":false,"allowInvoiceBeforeReceipt":false}`, headers: map[string]string{"If-Match": "3"}})
	if rec.Code != http.StatusPreconditionFailed || problemCode(t, rec) != store.CodePolicyStale {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
}

// The store's four-eyes refusal reaches the caller as 403 PRC-POL-002 on
// :approve and :confirm, not as the 422 other domain rules get.
func TestOrderAction_FourEyesRefusalIs403(t *testing.T) {
	h := newHarness(t)
	po := storedPO(h, domain.POStatusInReview)
	h.fake.pol.actErr = domain.CheckFourEyes(domain.Policy{POFourEyes: true}, userA, userA)
	for i, action := range []string{":approve", ":confirm"} {
		rec := h.do(call{method: "POST", path: poPath(po.ID) + action, user: userA, duties: []string{dutyPurchaseOrderConfirm},
			headers: ifMatchIdem("5", "fe"+strconv.Itoa(i))})
		if rec.Code != http.StatusForbidden || problemCode(t, rec) != domain.CodeFourEyes {
			t.Fatalf("%s: status %d: %s", action, rec.Code, rec.Body)
		}
	}
}

func TestCreateOrder_ChangeManagementDefaultComesFromPolicy(t *testing.T) {
	h := newHarness(t)
	h.fake.pol.saved = &domain.Policy{LegalEntityID: leA, ChangeManagementDefault: false, Version: 2}
	body := `{"legalEntityId":"` + leA + `","vendorId":"` + userB + `","currency":"USD","lines":[
	 {"description":"Bolts","qty":"10","unit":"EA","unitPrice":"2","deliveryDate":"2026-03-10"}]}`
	rec := h.do(call{method: "POST", path: "/v1/procurement/purchase-orders", user: userA, duties: []string{dutyPurchaseOrderMaintain}, body: body, headers: idem("po-pol")})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var got store.PurchaseOrder
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.ChangeManagement {
		t.Error("changeManagement should follow the policy default (false)")
	}
	explicit := `{"changeManagement":true,` + body[1:]
	rec = h.do(call{method: "POST", path: "/v1/procurement/purchase-orders", user: userA, duties: []string{dutyPurchaseOrderMaintain}, body: explicit, headers: idem("po-pol2")})
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if rec.Code != http.StatusCreated || !got.ChangeManagement {
		t.Errorf("an explicit changeManagement wins: status %d, got %v", rec.Code, got.ChangeManagement)
	}
}
