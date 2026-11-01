package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

// signingLog is what the signing limit methods of the fake hold and received.
type signingLog struct {
	limits   []domain.SigningLimit
	requests []domain.SigningLimitRequest
	created  *domain.SigningLimitRequest
	decided  struct {
		id, action, note string
		ifMatch          int
		today            domain.Day
	}
	recertified string
	err         error
}

func (f *fakeStore) CreateSigningLimitRequest(_ context.Context, in domain.SigningLimitRequest, _ store.Idem) (domain.SigningLimitRequest, error) {
	in.ID, in.Version, in.Status = ids.New(), 1, domain.SigningPending
	f.signing.created = &in
	return in, f.signing.err
}

func (f *fakeStore) GetSigningLimitRequest(_ context.Context, id string) (domain.SigningLimitRequest, error) {
	for _, r := range f.signing.requests {
		if r.ID == id {
			return r, nil
		}
	}
	return domain.SigningLimitRequest{}, httpx.NewProblem(http.StatusNotFound, store.CodeSigningRequestNotFound, "not found")
}

func (f *fakeStore) ListSigningLimitRequests(_ context.Context, fl store.SigningRequestFilter, _ string, _ int) (store.Page[domain.SigningLimitRequest], error) {
	out := []domain.SigningLimitRequest{}
	for _, r := range f.signing.requests {
		if fl.Status == "" || r.Status == fl.Status {
			out = append(out, r)
		}
	}
	return store.Page[domain.SigningLimitRequest]{Items: out}, nil
}

func (f *fakeStore) DecideSigningLimitRequest(_ context.Context, id string, ifMatch int, action, note string, today domain.Day, _ store.Idem) (domain.SigningLimitRequest, error) {
	d := &f.signing.decided
	d.id, d.ifMatch, d.action, d.note, d.today = id, ifMatch, action, note, today
	r, err := f.GetSigningLimitRequest(context.Background(), id)
	r.Version++
	return r, err
}

func (f *fakeStore) GetSigningLimit(_ context.Context, id string) (domain.SigningLimit, error) {
	for _, l := range f.signing.limits {
		if l.ID == id {
			return l, nil
		}
	}
	return domain.SigningLimit{}, httpx.NewProblem(http.StatusNotFound, store.CodeSigningLimitNotFound, "not found")
}

func (f *fakeStore) ListSigningLimits(context.Context, store.SigningLimitFilter, string, int) (store.Page[domain.SigningLimit], error) {
	return store.Page[domain.SigningLimit]{Items: f.signing.limits}, nil
}

func (f *fakeStore) SigningLimitsFor(_ context.Context, _, worker, docType string) ([]domain.SigningLimit, error) {
	out := []domain.SigningLimit{}
	for _, l := range f.signing.limits {
		if l.WorkerID == worker && l.DocumentType == docType {
			out = append(out, l)
		}
	}
	return out, f.signing.err
}

func (f *fakeStore) RecertifySigningLimit(_ context.Context, id string, _ int, _ domain.Day, _ store.Idem) (domain.SigningLimit, error) {
	f.signing.recertified = id
	l, err := f.GetSigningLimit(context.Background(), id)
	l.Version++
	return l, err
}

const signingReqPath = "/v1/procurement/signing-limit-requests"

func signingLimit(h *harness, worker, docType, spending, approval, by string) domain.SigningLimit {
	l := domain.SigningLimit{ID: ids.New(), LegalEntityID: leA, WorkerID: worker, DocumentType: docType, Currency: "USD",
		SpendingLimit: decimal.RequireFromString(spending), ApprovalLimit: decimal.RequireFromString(approval), RecertifyBy: mustDay(h.t, by), Version: 2}
	h.fake.signing.limits = append(h.fake.signing.limits, l)
	return l
}

func TestSigningRequest_CreateValidatesAndNeedsTheDuty(t *testing.T) {
	h := newHarness(t)
	worker := ids.New()
	good := `{"legalEntityId":"` + leA + `","workerId":"` + worker + `","documentType":"PURCHASE_ORDER","currency":"USD","spendingLimit":"1000","approvalLimit":"5000.50","reason":" new buyer "}`
	req := []string{dutyRequisitionCreate}
	for _, tc := range []struct {
		name   string
		c      call
		status int
		code   string
	}{
		{"inquire cannot request", call{method: "POST", path: signingReqPath, user: userA, duties: []string{dutyPurchaseOrderInquire}, body: good, headers: idem("k")}, 403, "AUTH-0003"},
		{"no Idempotency-Key", call{method: "POST", path: signingReqPath, user: userA, duties: req, body: good}, 400, codeIdemRequired},
		{"bad document type", call{method: "POST", path: signingReqPath, user: userA, duties: req, headers: idem("k"), body: `{"legalEntityId":"` + leA + `","workerId":"` + worker + `","documentType":"X","currency":"USD","spendingLimit":"1","approvalLimit":"1"}`}, 422, codeInvalid},
		{"negative limit", call{method: "POST", path: signingReqPath, user: userA, duties: req, headers: idem("k"), body: `{"legalEntityId":"` + leA + `","workerId":"` + worker + `","documentType":"REQUISITION","currency":"USD","spendingLimit":"-1","approvalLimit":"1"}`}, 422, codeInvalid},
		{"missing approval limit", call{method: "POST", path: signingReqPath, user: userA, duties: req, headers: idem("k"), body: `{"legalEntityId":"` + leA + `","workerId":"` + worker + `","documentType":"REQUISITION","currency":"USD","spendingLimit":"1"}`}, 422, codeInvalid},
		{"bad currency", call{method: "POST", path: signingReqPath, user: userA, duties: req, headers: idem("k"), body: `{"legalEntityId":"` + leA + `","workerId":"` + worker + `","documentType":"REQUISITION","currency":"usd","spendingLimit":"1","approvalLimit":"1"}`}, 422, codeInvalid},
		{"legal entity outside scope", call{method: "POST", path: signingReqPath, user: userA, duties: req, headers: idem("k"), scope: leA,
			body: `{"legalEntityId":"00000000-0000-7000-8000-0000000001e2","workerId":"` + worker + `","documentType":"REQUISITION","currency":"USD","spendingLimit":"1","approvalLimit":"1"}`}, 403, codeScope},
	} {
		rec := h.do(tc.c)
		if rec.Code != tc.status || problemCode(t, rec) != tc.code {
			t.Errorf("%s: status %d code %s: %s", tc.name, rec.Code, problemCode(t, rec), rec.Body)
		}
	}
	rec := h.do(call{method: "POST", path: signingReqPath, user: userA, duties: req, body: good, headers: idem("ok")})
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("create: %d: %s", rec.Code, rec.Body)
	}
	if c := h.fake.signing.created; c == nil || c.Reason != "new buyer" || !c.ApprovalLimit.Equal(decimal.RequireFromString("5000.5")) || c.WorkerID != worker {
		t.Errorf("store got %+v", c)
	}
}

func TestSigningRequest_DecisionNeedsTheDutyIfMatchAndScope(t *testing.T) {
	h := newHarness(t)
	id := ids.New()
	h.fake.signing.requests = []domain.SigningLimitRequest{{ID: id, LegalEntityID: leA, WorkerID: ids.New(), Status: domain.SigningPending, Version: 3}}
	path := signingReqPath + "/" + id
	hdr := map[string]string{"Idempotency-Key": "d1", "If-Match": `"3"`}
	if rec := h.do(call{method: "POST", path: path + ":approve", user: userA, duties: []string{dutyPurchaseOrderConfirm}, headers: hdr}); rec.Code != 403 {
		t.Errorf("without SigningLimitApprove: %d", rec.Code)
	}
	if rec := h.do(call{method: "POST", path: path + ":approve", user: userA, duties: []string{dutySigningLimitApprove}, headers: idem("d1")}); rec.Code != 428 {
		t.Errorf("without If-Match: %d", rec.Code)
	}
	if rec := h.do(call{method: "POST", path: path + ":archive", user: userA, duties: []string{dutySigningLimitApprove}, headers: hdr}); rec.Code != 404 {
		t.Errorf("unknown action: %d", rec.Code)
	}
	if rec := h.do(call{method: "POST", path: path + ":approve", user: userA, duties: []string{dutySigningLimitApprove}, headers: hdr, body: `{"note":1}`}); rec.Code != 422 {
		t.Errorf("bad body: %d", rec.Code)
	}
	rec := h.do(call{method: "POST", path: path + ":reject", user: userA, duties: []string{dutySigningLimitApprove}, headers: hdr, body: `{"note":" over budget "}`})
	if rec.Code != 200 || rec.Header().Get("ETag") != `"4"` {
		t.Fatalf("reject: %d %s", rec.Code, rec.Body)
	}
	if d := h.fake.signing.decided; d.id != id || d.action != "reject" || d.note != "over budget" || d.ifMatch != 3 || d.today.String() != "2026-03-01" {
		t.Errorf("store got %+v", d)
	}
	// An empty body is allowed.
	if rec := h.do(call{method: "POST", path: path + ":approve", user: userA, duties: []string{dutySigningLimitApprove}, headers: hdr}); rec.Code != 200 {
		t.Errorf("approve with no body: %d %s", rec.Code, rec.Body)
	}
}

func TestSigningLimit_ReadsRecertifyAndEvaluation(t *testing.T) {
	h := newHarness(t)
	worker := ids.New()
	l := signingLimit(h, worker, domain.DocPurchaseOrder, "1000", "5000", "2026-12-31")
	if rec := h.do(call{method: "GET", path: "/v1/procurement/signing-limits/" + l.ID, user: userA, duties: []string{dutyPurchaseOrderInquire}}); rec.Code != 200 || rec.Header().Get("ETag") != `"2"` {
		t.Errorf("get: %d %s", rec.Code, rec.Body)
	}
	if rec := h.do(call{method: "GET", path: "/v1/procurement/signing-limits", user: userA, duties: []string{dutyRequisitionCreate}}); rec.Code != 403 {
		t.Errorf("a requester cannot list limits: %d", rec.Code)
	}
	rc := call{method: "POST", path: "/v1/procurement/signing-limits/" + l.ID + ":recertify", user: userA, duties: []string{dutySigningLimitApprove}, headers: ifMatchIdem("2", "r1")}
	if rec := h.do(rc); rec.Code != 200 || h.fake.signing.recertified != l.ID {
		t.Errorf("recertify: %d %s", rec.Code, rec.Body)
	}
	rc.duties = []string{dutyPurchaseOrderInquire}
	if rec := h.do(rc); rec.Code != 403 {
		t.Errorf("recertify without the duty: %d", rec.Code)
	}
	ev := func(amount string) signingEvaluation {
		rec := h.do(call{method: "GET", user: userA, duties: []string{dutyPurchaseOrderInquire}, path: "/v1/procurement/signing-limits/evaluation?legalEntityId=" + leA +
			"&workerId=" + worker + "&documentType=PURCHASE_ORDER&currency=USD&amount=" + amount})
		if rec.Code != 200 {
			t.Fatalf("evaluation: %d %s", rec.Code, rec.Body)
		}
		var out signingEvaluation
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out
	}
	if e := ev("800"); e.RequesterEscalates || !e.ApproverAllowed || e.Limit == nil {
		t.Errorf("800: %+v", e)
	}
	if e := ev("7000"); !e.RequesterEscalates || e.ApproverAllowed || e.Code != domain.CodeSigningApprovalLimit {
		t.Errorf("7000: %+v", e)
	}
	worker = ids.New()
	if e := ev("1"); e.RequesterEscalates || e.ApproverAllowed || e.Code != domain.CodeSigningApprovalLimit || e.Limit != nil {
		t.Errorf("a worker with no limit cannot approve: %+v", e)
	}
}

// AC-17.12 (limit half): a requester with a spending limit of 5,000 USD who submits 7,000 USD
// gets one more approval level on the chain.
func TestSubmitRequisition_SpendingLimitAddsAnApprovalLevel(t *testing.T) {
	h := newHarness(t)
	signingLimit(h, userA, domain.DocRequisition, "5000", "5000", "2026-12-31")
	id := draftRequisition(h, ids.New())
	rec := submitRequisition(h, id)
	if rec.Code != 200 {
		t.Fatalf("submit: %d %s", rec.Code, rec.Body)
	}
	// 7,000 USD routes to the manager band; the limit adds a level above it.
	if got := h.fake.lastSubmit.chain; len(got) != 2 || got[0] != domain.RoleManager || got[1] != domain.RoleNextApprover {
		t.Errorf("chain = %v, want [MANAGER NEXT_APPROVER]", got)
	}

	// Within the limit nothing is added.
	h2 := newHarness(t)
	signingLimit(h2, userA, domain.DocRequisition, "9000", "9000", "2026-12-31")
	rec = submitRequisition(h2, draftRequisition(h2, ids.New()))
	if got := h2.fake.lastSubmit.chain; rec.Code != 200 || len(got) != 1 {
		t.Errorf("within the limit: %d chain %v", rec.Code, got)
	}

	// A limit only in another currency cannot be compared (no conversion yet): one more level.
	h3 := newHarness(t)
	signingLimit(h3, userA, domain.DocRequisition, "99999", "99999", "2026-12-31")
	h3.fake.signing.limits[0].Currency = "EUR"
	rec = submitRequisition(h3, draftRequisition(h3, ids.New()))
	if got := h3.fake.lastSubmit.chain; rec.Code != 200 || len(got) != 2 || got[1] != domain.RoleNextApprover {
		t.Errorf("limit in EUR for a USD requisition: %d chain %v", rec.Code, got)
	}
}

func TestSubmitRequisition_ExpiredLimitEscalatesWithAWarning(t *testing.T) {
	h := newHarness(t)
	signingLimit(h, userA, domain.DocRequisition, "99999", "99999", "2026-02-01")
	rec := submitRequisition(h, draftRequisition(h, ids.New()))
	var out struct {
		Warnings []store.Warning `json:"warnings"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != 200 || len(h.fake.lastSubmit.chain) != 2 || len(out.Warnings) != 1 || out.Warnings[0].Code != domain.CodeSigningExpired {
		t.Errorf("expired: %d chain %v warnings %+v", rec.Code, h.fake.lastSubmit.chain, out.Warnings)
	}
}

func TestOrderApprove_ApproverLimitSkipsTheApprover(t *testing.T) {
	h := newHarness(t)
	po := storedPO(h, domain.POStatusInReview)
	po.Total = decimal.NewFromInt(7000)
	h.fake.poByID[po.ID] = po
	approve := call{method: "POST", path: poPath(po.ID) + ":approve", user: userA, duties: []string{dutyPurchaseOrderConfirm}, headers: ifMatchIdem("5", "a1")}

	l := signingLimit(h, userA, domain.DocPurchaseOrder, "1000", "5000", "2026-12-31")
	rec := h.do(approve)
	if rec.Code != http.StatusForbidden || problemCode(t, rec) != domain.CodeSigningApprovalLimit {
		t.Fatalf("above the approval limit: %d %s", rec.Code, rec.Body)
	}
	if h.fake.lastAction.action != "" {
		t.Errorf("the store must not be asked to approve: %+v", h.fake.lastAction)
	}

	h.fake.signing.limits[0].ApprovalLimit = decimal.NewFromInt(7000)
	if rec := h.do(approve); rec.Code != 200 {
		t.Fatalf("within the approval limit: %d %s", rec.Code, rec.Body)
	}

	h.fake.signing.limits[0] = l
	h.fake.signing.limits[0].ApprovalLimit = decimal.NewFromInt(99999)
	h.fake.signing.limits[0].RecertifyBy = mustDay(t, "2026-02-28")
	approve.headers = ifMatchIdem("5", "a2")
	rec = h.do(approve)
	if rec.Code != http.StatusForbidden || problemCode(t, rec) != domain.CodeSigningExpired {
		t.Errorf("expired limit: %d %s", rec.Code, rec.Body)
	}
	// The rule fails closed: no limit, or a limit only in another currency, is an approval
	// limit of zero, and so is a caller that is not a named user.
	h.fake.signing.limits = nil
	approve.headers = ifMatchIdem("5", "a3")
	if rec := h.do(approve); rec.Code != http.StatusForbidden || problemCode(t, rec) != domain.CodeSigningApprovalLimit {
		t.Errorf("no limit: %d %s", rec.Code, rec.Body)
	}
	eur := signingLimit(h, userA, domain.DocPurchaseOrder, "999999", "999999", "2026-12-31")
	h.fake.signing.limits[0].Currency = "EUR"
	approve.headers = ifMatchIdem("5", "a4")
	if rec := h.do(approve); rec.Code != http.StatusForbidden || problemCode(t, rec) != domain.CodeSigningApprovalLimit {
		t.Errorf("a limit in EUR for a USD PO: %d %s", rec.Code, rec.Body)
	}
	h.fake.signing.limits = []domain.SigningLimit{eur}
	approve.user, approve.headers = "", ifMatchIdem("5", "a5")
	if rec := h.do(approve); rec.Code != http.StatusForbidden || problemCode(t, rec) != domain.CodeSigningApprovalLimit {
		t.Errorf("no named user: %d %s", rec.Code, rec.Body)
	}

	// A zero total needs no limit.
	po.Total = decimal.Zero
	h.fake.poByID[po.ID] = po
	approve.user, approve.headers = userA, ifMatchIdem("5", "a6")
	if rec := h.do(approve); rec.Code != 200 {
		t.Errorf("zero total: %d %s", rec.Code, rec.Body)
	}

	// An If-Match behind the PO is left to the store: a replay of an earlier approval is
	// answered from its key, anything else is 412 there. The total checked is never one the
	// store would not act on.
	po.Total, po.RowVersion = decimal.NewFromInt(7000), 6
	h.fake.poByID[po.ID] = po
	h.fake.lastAction.action = ""
	approve.headers = ifMatchIdem("5", "a1")
	if rec := h.do(approve); h.fake.lastAction.action != "approve" || h.fake.lastAction.idem.Key != "a1" {
		t.Errorf("an older If-Match must reach the store (replay or 412): %d %+v", rec.Code, h.fake.lastAction)
	}
}
