package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

// approvalFake records what ActOnRequisition and ListRequisitionApprovals received.
type approvalFake struct {
	act     store.RequisitionAct
	version int
	idem    store.Idem
	calls   int
	filter  store.ApprovalQueueFilter
	err     error
	// keys holds the request hash of each Idempotency-Key the fake has applied, so a retry replays as the store does.
	keys map[string]string
}

func (f *fakeStore) ActOnRequisition(_ context.Context, id string, version int, act store.RequisitionAct, idem store.Idem) (store.Requisition, error) {
	f.approval.act, f.approval.version, f.approval.idem = act, version, idem
	f.approval.calls++
	r := f.requisitions[id]
	if f.approval.err != nil {
		return store.Requisition{}, f.approval.err
	}
	if f.approval.keys == nil {
		f.approval.keys = map[string]string{}
	}
	if hash, seen := f.approval.keys[idem.Key]; seen && hash == idem.Hash {
		return r, nil
	}
	f.approval.keys[idem.Key] = idem.Hash
	next, steps, err := domain.RequisitionTransition(r.Status, act.Action, r.ApprovedSteps, len(r.ApproverChain))
	if err == nil {
		r.Status, r.ApprovedSteps, r.Version = next, steps, r.Version+1
	}
	f.requisitions[id] = r
	return r, nil
}

func (f *fakeStore) ListRequisitionApprovals(_ context.Context, rf store.ApprovalQueueFilter, _ string, _ int) (store.Page[store.RequisitionApprovalItem], error) {
	f.approval.filter = rf
	return store.Page[store.RequisitionApprovalItem]{Items: []store.RequisitionApprovalItem{}}, nil
}

const (
	userApprover = "00000000-0000-7000-8000-0000000000c1"
	userOther    = "00000000-0000-7000-8000-0000000000c2"
)

func reviewRequisition(h *harness, chain []string, steps int, category string) string {
	id := ids.New()
	h.fake.requisitions[id] = store.Requisition{ID: id, LegalEntityID: leA, RequesterID: userA, Status: store.RequisitionInReview, Version: 4,
		Currency: "USD", Total: decimal.NewFromInt(7000), ApproverChain: chain, ApprovedSteps: steps,
		Lines: []store.RequisitionLine{{Category: category, Status: "OPEN"}}}
	return id
}

func decide(h *harness, id, action, user string, duties []string, body string) int {
	rec := h.do(call{method: "POST", path: "/v1/procurement/requisitions/" + id + ":" + action, user: user, duties: duties, body: body,
		headers: map[string]string{"Idempotency-Key": "k-" + action, "If-Match": `"4"`}})
	return rec.Code
}

func TestApproveRequisition_NeedsTheDutyAnApprovalLimitAndAnotherPerson(t *testing.T) {
	approve := []string{dutyRequisitionApprove}
	h := newHarness(t)
	id := reviewRequisition(h, []string{domain.RoleManager, domain.RoleCFO}, 0, "MRO")

	if code := decide(h, id, "approve", userApprover, []string{dutyRequisitionCreate}, ""); code != http.StatusForbidden {
		t.Fatalf("create duty only: %d, want 403", code)
	}
	// Fails closed with no signing limit on file (BR-17.15).
	rec := h.do(call{method: "POST", path: "/v1/procurement/requisitions/" + id + ":approve", user: userApprover, duties: approve,
		headers: map[string]string{"Idempotency-Key": "k1", "If-Match": `"4"`}})
	if rec.Code != http.StatusForbidden || problemCode(t, rec) != domain.CodeSigningApprovalLimit {
		t.Fatalf("no limit: %d %s", rec.Code, rec.Body)
	}
	signingLimit(h, userApprover, domain.DocRequisition, "1000", "5000", "2026-12-31")
	if rec := h.do(call{method: "POST", path: "/v1/procurement/requisitions/" + id + ":approve", user: userApprover, duties: approve,
		headers: map[string]string{"Idempotency-Key": "k1", "If-Match": `"4"`}}); rec.Code != http.StatusForbidden || problemCode(t, rec) != domain.CodeSigningApprovalLimit {
		t.Fatalf("limit below the 7,000 total: %d %s", rec.Code, rec.Body)
	}
	if h.fake.approval.calls != 0 {
		t.Fatal("a refused approval must not reach the store")
	}
	h.fake.signing.limits[0].ApprovalLimit = decimal.NewFromInt(10000)

	// The requester cannot approve their own requisition.
	signingLimit(h, userA, domain.DocRequisition, "1000", "10000", "2026-12-31")
	rec = h.do(call{method: "POST", path: "/v1/procurement/requisitions/" + id + ":approve", user: userA, duties: approve,
		headers: map[string]string{"Idempotency-Key": "k2", "If-Match": `"4"`}})
	if rec.Code != http.StatusForbidden || problemCode(t, rec) != store.CodeRequisitionSelfApproval {
		t.Fatalf("requester: %d %s", rec.Code, rec.Body)
	}

	rec = h.do(call{method: "POST", path: "/v1/procurement/requisitions/" + id + ":approve", user: userApprover, duties: approve, body: `{"comment":" ok for Q1 "}`,
		headers: map[string]string{"Idempotency-Key": "k3", "If-Match": `"4"`}})
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"5"` {
		t.Fatalf("approve: %d %s etag %s", rec.Code, rec.Body, rec.Header().Get("ETag"))
	}
	got := h.fake.approval
	if got.act.Action != "approve" || got.act.ActorID != userApprover || got.act.Comment != "ok for Q1" || got.version != 4 || got.idem.Key != "k3" || got.idem.Hash == "" {
		t.Fatalf("store got %+v", got)
	}
	if !strings.Contains(rec.Body.String(), `"status":"IN_REVIEW"`) || !strings.Contains(rec.Body.String(), `"approvedSteps":1`) {
		t.Fatalf("the CFO level is still open: %s", rec.Body)
	}
}

func TestApproveRequisition_ValidatesTheRequestAndScope(t *testing.T) {
	approve := []string{dutyRequisitionApprove}
	h := newHarness(t)
	id := reviewRequisition(h, []string{domain.RoleManager}, 0, "MRO")
	signingLimit(h, userApprover, domain.DocRequisition, "1000", "10000", "2026-12-31")
	path := "/v1/procurement/requisitions/" + id + ":approve"
	base := func(c call) call {
		c.method, c.path, c.user, c.duties = "POST", path, userApprover, approve
		if c.headers == nil {
			c.headers = map[string]string{"Idempotency-Key": "k", "If-Match": `"4"`}
		}
		return c
	}
	for _, tc := range []struct {
		name   string
		c      call
		status int
		code   string
	}{
		{"no Idempotency-Key", base(call{headers: map[string]string{"If-Match": `"4"`}}), 400, codeIdemRequired},
		{"no If-Match", base(call{headers: idem("k")}), 428, codeIfMatchMissing},
		{"unknown body field", base(call{body: `{"bogus":1}`}), 422, codeInvalid},
		{"comment too long", base(call{body: `{"comment":"` + strings.Repeat("x", 1001) + `"}`}), 422, codeInvalid},
		{"legal entity outside scope", base(call{scope: "00000000-0000-7000-8000-0000000001e2"}), 403, codeScope},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := h.do(tc.c)
			if rec.Code != tc.status || problemCode(t, rec) != tc.code {
				t.Fatalf("got %d %s, want %d %s: %s", rec.Code, problemCode(t, rec), tc.status, tc.code, rec.Body)
			}
		})
	}
	if h.fake.approval.calls != 0 {
		t.Fatalf("%d refused requests reached the store", h.fake.approval.calls)
	}
}

func TestApproveRequisition_CategoryOwnerLevelNeedsACategoryManager(t *testing.T) {
	approve := []string{dutyRequisitionApprove}
	chain := []string{domain.RoleManager, domain.RoleCategoryOwner}
	h := newHarness(t)
	id := reviewRequisition(h, chain, 1, "IT")
	signingLimit(h, userApprover, domain.DocRequisition, "1000", "10000", "2026-12-31")
	signingLimit(h, userOther, domain.DocRequisition, "1000", "10000", "2026-12-31")

	// Nobody manages IT yet: any approver may decide the level.
	if code := decide(h, id, "approve", userApprover, approve, ""); code != http.StatusOK {
		t.Fatalf("no managers assigned: %d", code)
	}

	h = newHarness(t)
	id = reviewRequisition(h, chain, 1, "IT")
	signingLimit(h, userApprover, domain.DocRequisition, "1000", "10000", "2026-12-31")
	signingLimit(h, userOther, domain.DocRequisition, "1000", "10000", "2026-12-31")
	h.fake.categories.managers = []domain.CategoryManager{{ID: ids.New(), LegalEntityID: leA, Category: "IT", WorkerID: userOther}}
	rec := h.do(call{method: "POST", path: "/v1/procurement/requisitions/" + id + ":approve", user: userApprover, duties: approve,
		headers: map[string]string{"Idempotency-Key": "k", "If-Match": `"4"`}})
	if rec.Code != http.StatusForbidden || problemCode(t, rec) != codeNotCategoryManager {
		t.Fatalf("not the category manager: %d %s", rec.Code, rec.Body)
	}
	if code := decide(h, id, "approve", userOther, approve, ""); code != http.StatusOK {
		t.Fatalf("the category manager: %d", code)
	}
	if h.fake.requisitions[id].Status != store.RequisitionApproved {
		t.Fatalf("status = %s, want APPROVED after the last level", h.fake.requisitions[id].Status)
	}
}

// A retry of an approval whose first attempt was applied must get the original answer, not the next level's rules:
// here the retry arrives once the requisition has moved on to a CATEGORY_OWNER level the approver does not manage,
// and its approval limit has since been lowered.
func TestApproveRequisition_RetryAfterTheLevelMovedReplays(t *testing.T) {
	approve := []string{dutyRequisitionApprove}
	h := newHarness(t)
	id := reviewRequisition(h, []string{domain.RoleManager, domain.RoleCategoryOwner}, 0, "IT")
	signingLimit(h, userApprover, domain.DocRequisition, "1000", "10000", "2026-12-31")
	h.fake.categories.managers = []domain.CategoryManager{{ID: ids.New(), LegalEntityID: leA, Category: "IT", WorkerID: userOther}}
	approveOnce := func(key string) *httptest.ResponseRecorder {
		return h.do(call{method: "POST", path: "/v1/procurement/requisitions/" + id + ":approve", user: userApprover, duties: approve,
			headers: map[string]string{"Idempotency-Key": key, "If-Match": `"4"`}})
	}
	if rec := approveOnce("retry-1"); rec.Code != http.StatusOK || h.fake.requisitions[id].ApprovedSteps != 1 {
		t.Fatalf("first attempt: %d %s", rec.Code, rec.Body)
	}
	h.fake.signing.limits[0].ApprovalLimit = decimal.NewFromInt(10)
	rec := approveOnce("retry-1")
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"5"` {
		t.Fatalf("retry with the same key: %d %s, want 200 with the requisition at version 5", rec.Code, rec.Body)
	}
	if got := h.fake.requisitions[id]; got.ApprovedSteps != 1 || got.Version != 5 {
		t.Fatalf("the retry applied again: steps %d version %d", got.ApprovedSteps, got.Version)
	}
	// A new request at the old version is not a replay: it reaches the store, which refuses the stale version.
	h.fake.approval.err = httpErr409()
	if rec := approveOnce("retry-2"); rec.Code != http.StatusConflict {
		t.Fatalf("new key at the old version: %d %s, want 409", rec.Code, rec.Body)
	}
	h.fake.approval.err = nil
	// At the current version the level rules apply again: the lowered limit, then the CATEGORY_OWNER rule.
	if rec := h.do(call{method: "POST", path: "/v1/procurement/requisitions/" + id + ":approve", user: userApprover, duties: approve,
		headers: map[string]string{"Idempotency-Key": "retry-3", "If-Match": `"5"`}}); rec.Code != http.StatusForbidden || problemCode(t, rec) != domain.CodeSigningApprovalLimit {
		t.Fatalf("lowered limit at the current version: %d %s", rec.Code, rec.Body)
	}
	h.fake.signing.limits[0].ApprovalLimit = decimal.NewFromInt(10000)
	if rec := h.do(call{method: "POST", path: "/v1/procurement/requisitions/" + id + ":approve", user: userApprover, duties: approve,
		headers: map[string]string{"Idempotency-Key": "retry-3", "If-Match": `"5"`}}); rec.Code != http.StatusForbidden || problemCode(t, rec) != codeNotCategoryManager {
		t.Fatalf("next level at the current version: %d %s", rec.Code, rec.Body)
	}
}

func httpErr409() error {
	return httpx.NewProblem(http.StatusConflict, store.CodeVersionConflict, "requisition is at version 5, not 4; reload and retry")
}

func TestRejectRequisition_NeedsAReason(t *testing.T) {
	approve := []string{dutyRequisitionApprove}
	h := newHarness(t)
	id := reviewRequisition(h, []string{domain.RoleManager}, 0, "MRO")
	if code := decide(h, id, "reject", userApprover, approve, ""); code != http.StatusUnprocessableEntity {
		t.Fatalf("no reason: %d, want 422", code)
	}
	if code := decide(h, id, "reject", userA, approve, `{"comment":"no"}`); code != http.StatusForbidden {
		t.Fatalf("requester rejecting: %d, want 403", code)
	}
	// A rejection does not check the approval limit: an approver may always send a request back.
	if code := decide(h, id, "reject", userApprover, approve, `{"comment":"Use the framework agreement"}`); code != http.StatusOK {
		t.Fatalf("reject: %d", code)
	}
	if got := h.fake.approval.act; got.Action != "reject" || got.Comment != "Use the framework agreement" {
		t.Fatalf("store got %+v", got)
	}
	if h.fake.requisitions[id].Status != store.RequisitionRejected {
		t.Fatalf("status = %s", h.fake.requisitions[id].Status)
	}
}

func TestReopenAndCancelRequisition_AreTheRequestersOwn(t *testing.T) {
	create := []string{dutyRequisitionCreate}
	h := newHarness(t)
	id := reviewRequisition(h, []string{domain.RoleManager}, 0, "MRO")
	r := h.fake.requisitions[id]
	r.Status = store.RequisitionRejected
	h.fake.requisitions[id] = r

	if code := decide(h, id, "reopen", userOther, create, ""); code != http.StatusForbidden {
		t.Fatalf("someone else reopening: %d, want 403", code)
	}
	if code := decide(h, id, "reopen", userA, []string{dutyRequisitionApprove}, ""); code != http.StatusForbidden {
		t.Fatalf("reopen without the create duty: %d, want 403", code)
	}
	if code := decide(h, id, "reopen", userA, create, ""); code != http.StatusOK || h.fake.requisitions[id].Status != store.RequisitionDraft {
		t.Fatalf("reopen: %d, status %s", code, h.fake.requisitions[id].Status)
	}
	if code := decide(h, id, "cancel", userOther, create, ""); code != http.StatusForbidden {
		t.Fatalf("someone else cancelling: %d, want 403", code)
	}
	if code := decide(h, id, "cancel", userA, create, `{"comment":"not needed"}`); code != http.StatusOK || h.fake.requisitions[id].Status != store.RequisitionCancelled {
		t.Fatalf("cancel: %d, status %s", code, h.fake.requisitions[id].Status)
	}
	if got := h.fake.approval.act; got.Action != "cancel" || got.Comment != "not needed" || got.ActorID != userA {
		t.Fatalf("store got %+v", got)
	}
}

func TestListRequisitionApprovals_FiltersAndHidesTheCallersOwn(t *testing.T) {
	h := newHarness(t)
	list := func(query string, duties ...string) int {
		return h.do(call{method: "GET", path: "/v1/procurement/requisition-approvals" + query, user: userApprover, duties: duties}).Code
	}
	if code := list("", dutyRequisitionCreate); code != http.StatusForbidden {
		t.Fatalf("create duty only: %d, want 403", code)
	}
	if code := list("?role=BOSS", dutyRequisitionApprove); code != http.StatusUnprocessableEntity {
		t.Fatalf("bad role: %d, want 422", code)
	}
	if code := list("?legalEntityId=00000000-0000-7000-8000-0000000001e2", dutyRequisitionApprove); code != http.StatusForbidden {
		t.Fatalf("legal entity outside scope: %d, want 403", code)
	}
	if code := list("?role=CFO&legalEntityId="+leA, dutyRequisitionApprove); code != http.StatusOK {
		t.Fatalf("approve duty: %d", code)
	}
	if got := h.fake.approval.filter; got.Role != "CFO" || got.LegalEntityID != leA || got.ExcludeRequesterID != userApprover {
		t.Fatalf("filter = %+v", got)
	}
	if code := list("", dutyPurchaseOrderInquire); code != http.StatusOK {
		t.Fatalf("inquire duty: %d", code)
	}
	if code := list("", dutyInboxRead); code != http.StatusOK {
		t.Fatalf("inbox duty: %d", code)
	}
	if got := h.fake.approval.filter; got.ExcludeRequesterID != userApprover {
		t.Fatalf("inbox read must still hide the caller's own requisitions: %+v", got)
	}
}

func TestGetRequisition_ApproversMayRead(t *testing.T) {
	h := newHarness(t)
	id := reviewRequisition(h, []string{domain.RoleManager}, 0, "MRO")
	for duty, want := range map[string]int{dutyRequisitionApprove: 200, dutyRequisitionCreate: 200, dutyPurchaseOrderMaintain: 403} {
		if rec := h.do(call{method: "GET", path: "/v1/procurement/requisitions/" + id, duties: []string{duty}}); rec.Code != want {
			t.Errorf("duty %s: %d, want %d", duty, rec.Code, want)
		}
	}
}
