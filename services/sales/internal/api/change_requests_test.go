package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/sales/internal/store"
)

type requestStore struct {
	store.Store
	pending     bool
	approver    bool
	decidedID   string
	decidedVer  int
	comment     string
	actor       string
	rejected    bool
	withdrawn   bool
	listed      store.ChangeRequestFilter
	policy      store.ChangePolicy
	policyVer   int
	policyActor string
	storeCalls  int
}

func (s *requestStore) ChangeOrder(_ context.Context, _ string, version int, req store.ChangeRequest, _ string, _ time.Time) (store.Order, error) {
	s.approver = req.Approver
	o := store.Order{Version: version, Lines: []store.Line{}, Holds: []store.Hold{}}
	if s.pending {
		o.PendingChange = &store.OrderChangeRequest{ID: ids.New(), Status: store.ChangePending, Version: 1}
	}
	return o, nil
}

func (s *requestStore) ApproveChangeRequest(_ context.Context, id string, version int, comment, actor string, _ time.Time) (store.ChangeDecision, error) {
	s.decidedID, s.decidedVer, s.comment, s.actor = id, version, comment, actor
	return store.ChangeDecision{Request: store.OrderChangeRequest{ID: id, Status: store.ChangeApproved, Version: version + 1}, Order: store.Order{Lines: []store.Line{}, Holds: []store.Hold{}}}, nil
}

func (s *requestStore) RejectChangeRequest(_ context.Context, id string, version int, comment, actor string, _ time.Time) (store.OrderChangeRequest, error) {
	s.rejected, s.decidedVer, s.comment, s.actor = true, version, comment, actor
	return store.OrderChangeRequest{ID: id, Status: store.ChangeRejected, Version: version + 1}, nil
}

func (s *requestStore) WithdrawChangeRequest(_ context.Context, id string, version int, comment, actor string, _ time.Time) (store.OrderChangeRequest, error) {
	s.withdrawn, s.decidedVer, s.comment, s.actor = true, version, comment, actor
	return store.OrderChangeRequest{ID: id, Kind: store.KindChange, Status: store.ChangeWithdrawn, Version: version + 1}, nil
}

func (s *requestStore) ListChangeRequests(_ context.Context, f store.ChangeRequestFilter) ([]store.OrderChangeRequest, error) {
	s.listed = f
	return []store.OrderChangeRequest{}, nil
}

func (s *requestStore) GetChangePolicy(_ context.Context, le, customer string) (store.ChangePolicy, error) {
	return store.ChangePolicy{LegalEntityID: le, CustomerID: customer}, nil
}

func (s *requestStore) PutChangePolicy(_ context.Context, in store.ChangePolicy, version int, actor string) (store.ChangePolicy, error) {
	s.policy, s.policyVer, s.policyActor = in, version, actor
	in.Version = version + 1
	return in, nil
}

func TestPatchOrder_PendingChangeIs202AndApproverIsPassed(t *testing.T) {
	body := `{"lines":[{"lineNo":1,"quantity":"8"}]}`
	h := map[string]string{"If-Match": `"3"`}
	s := &requestStore{pending: true}
	rec := serveBody(t, s, http.MethodPatch, orders+"/"+ids.New(), body, principal(dutyWrite), h)
	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"pendingChange"`) || s.approver || rec.Header().Get("ETag") != `"3"` {
		t.Fatalf("status %d approver=%v etag=%s: %s", rec.Code, s.approver, rec.Header().Get("ETag"), rec.Body.String())
	}
	s = &requestStore{}
	rec = serveBody(t, s, http.MethodPatch, orders+"/"+ids.New(), body, principal(dutyWrite, dutyOrderApprove), h)
	if rec.Code != http.StatusOK || !s.approver {
		t.Fatalf("an approver: status %d approver=%v", rec.Code, s.approver)
	}
}

func TestChangeRequests_DecisionsNeedDutyVersionAndComment(t *testing.T) {
	id := ids.New()
	path := changeRequests + "/" + id
	h := map[string]string{"If-Match": `"2"`}
	s := &requestStore{}
	if rec := serveBody(t, s, http.MethodPost, path+":approve", "", principal(dutyRead, dutyWrite), h); rec.Code != http.StatusForbidden || s.decidedID != "" {
		t.Fatalf("approve without the duty: %d", rec.Code)
	}
	if rec := serveBody(t, s, http.MethodPost, path+":approve", "", principal(dutyOrderApprove), nil); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("approve without If-Match: %d", rec.Code)
	}
	rec := serveBody(t, s, http.MethodPost, path+":approve", `{"comment":"fine"}`, principal(dutyOrderApprove), h)
	if rec.Code != http.StatusOK || s.decidedID != id || s.decidedVer != 2 || s.comment != "fine" || s.actor != "u1" || rec.Header().Get("ETag") != `"3"` {
		t.Fatalf("approve: %d id=%s ver=%d comment=%q: %s", rec.Code, s.decidedID, s.decidedVer, s.comment, rec.Body.String())
	}
	if rec := serveBody(t, s, http.MethodPost, path+":reject", `{}`, principal(dutyOrderApprove), h); rec.Code != http.StatusBadRequest || s.rejected {
		t.Fatalf("reject needs a comment: %d", rec.Code)
	}
	if rec := serveBody(t, s, http.MethodPost, path+":reject", `{"comment":"`+strings.Repeat("x", 501)+`"}`, principal(dutyOrderApprove), h); rec.Code != http.StatusBadRequest || s.rejected {
		t.Fatalf("long comment: %d", rec.Code)
	}
	if rec := serveBody(t, s, http.MethodPost, path+":reject", `{"comment":"no"}`, principal(dutyOrderApprove), h); rec.Code != http.StatusOK || !s.rejected || s.comment != "no" {
		t.Fatalf("reject: %d", rec.Code)
	}
	if rec := serveBody(t, s, http.MethodPost, changeRequests+"/nope:approve", "", principal(dutyOrderApprove), h); rec.Code != http.StatusNotFound {
		t.Fatalf("bad id: %d", rec.Code)
	}
}

func TestChangeRequests_ListFilterValidation(t *testing.T) {
	s := &requestStore{}
	order := ids.New()
	rec := serve(t, s, http.MethodGet, changeRequests+"?status=PENDING&orderId="+order+"&limit=10", principal(dutyRead), nil)
	if rec.Code != http.StatusOK || s.listed.Status != "PENDING" || s.listed.OrderID != order || s.listed.Limit != 10 || s.listed.LegalEntityID != testLE {
		t.Fatalf("list: %d %+v", rec.Code, s.listed)
	}
	for _, q := range []string{"?status=DONE", "?orderId=x", "?limit=0"} {
		if rec := serve(t, s, http.MethodGet, changeRequests+q, principal(dutyRead), nil); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d", q, rec.Code)
		}
	}
}

func TestChangePolicy_GetAndPut(t *testing.T) {
	customer := ids.New()
	path := "/v1/sales/customers/" + customer + "/change-policy?legalEntityId=" + testLE
	s := &requestStore{}
	rec := serve(t, s, http.MethodGet, path, principal(dutyRead), nil)
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"0"` || !strings.Contains(rec.Body.String(), `"approvalRequired":false`) {
		t.Fatalf("get: %d %s", rec.Code, rec.Body.String())
	}
	h := map[string]string{"If-Match": `"0"`}
	if rec := serveBody(t, s, http.MethodPut, path, `{"approvalRequired":true}`, principal(dutyRead, dutyWrite), h); rec.Code != http.StatusForbidden {
		t.Fatalf("put without the duty: %d", rec.Code)
	}
	if rec := serveBody(t, s, http.MethodPut, path, `{}`, principal(dutyOrderApprove), h); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing flag: %d", rec.Code)
	}
	rec = serveBody(t, s, http.MethodPut, path, `{"approvalRequired":true}`, principal(dutyOrderApprove), h)
	if rec.Code != http.StatusOK || !s.policy.ApprovalRequired || s.policy.CustomerID != customer || s.policy.LegalEntityID != testLE || s.policyVer != 0 || s.policyActor != "u1" {
		t.Fatalf("put: %d %+v", rec.Code, s.policy)
	}
	if rec := serve(t, s, http.MethodGet, "/v1/sales/customers/x/change-policy?legalEntityId="+testLE, principal(dutyRead), nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad customer: %d", rec.Code)
	}
}

func TestWithdrawChangeRequest_DutyVersionAndOptionalComment(t *testing.T) {
	id := ids.New()
	path := changeRequests + "/" + id + ":withdraw"
	h := map[string]string{"If-Match": `"2"`}
	s := &requestStore{}
	if rec := serveBody(t, s, http.MethodPost, path, "", principal(dutyRead), h); rec.Code != http.StatusForbidden || s.withdrawn {
		t.Fatalf("withdraw without the write duty: %d", rec.Code)
	}
	if rec := serveBody(t, s, http.MethodPost, path, "", principal(dutyWrite), nil); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("withdraw without If-Match: %d", rec.Code)
	}
	rec := serveBody(t, s, http.MethodPost, path, `{"comment":"typo"}`, principal(dutyWrite), h)
	if rec.Code != http.StatusOK || !s.withdrawn || s.decidedVer != 2 || s.comment != "typo" || s.actor != "u1" || rec.Header().Get("ETag") != `"3"` || !strings.Contains(rec.Body.String(), `"status":"WITHDRAWN"`) {
		t.Fatalf("withdraw: %d ver=%d comment=%q: %s", rec.Code, s.decidedVer, s.comment, rec.Body.String())
	}
	s = &requestStore{}
	if rec := serveBody(t, s, http.MethodPost, path, "", principal(dutyWrite), h); rec.Code != http.StatusOK || !s.withdrawn {
		t.Fatalf("withdraw without a body: %d", rec.Code)
	}
}

func TestCancelOrder_PendingCancelIs202AndApproverIsPassed(t *testing.T) {
	id := ids.New()
	h := map[string]string{"If-Match": `"5"`}
	c := &changeStore{cancelPending: true}
	rec := serveBody(t, c, http.MethodPost, orders+"/"+id+":cancel", `{"reason":"duplicate"}`, principal(dutyConfirm), h)
	if rec.Code != http.StatusAccepted || c.cancelApprover || !strings.Contains(rec.Body.String(), `"kind":"CANCEL"`) || rec.Header().Get("ETag") != `"5"` {
		t.Fatalf("status %d approver=%v etag=%s: %s", rec.Code, c.cancelApprover, rec.Header().Get("ETag"), rec.Body.String())
	}
	c = &changeStore{}
	if rec := serveBody(t, c, http.MethodPost, orders+"/"+id+":cancel", "", principal(dutyConfirm, dutyOrderApprove), h); rec.Code != http.StatusOK || !c.cancelApprover {
		t.Fatalf("an approver: %d approver=%v", rec.Code, c.cancelApprover)
	}
}
