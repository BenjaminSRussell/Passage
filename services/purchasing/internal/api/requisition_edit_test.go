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

// editFake records what UpdateRequisition and CancelRequisitionLine received.
type editFake struct {
	edit      store.RequisitionEdit
	version   int
	cancelled string
	reason    string
	idem      store.Idem
	calls     int
	// replayKey is an Idempotency-Key the fake answers as a replay: the requisition as it is, before any check.
	replayKey string
}

// UpdateRequisition answers like the store: a replay first, then the version, then the status.
func (f *fakeStore) UpdateRequisition(_ context.Context, id string, version int, edit store.RequisitionEdit, idem store.Idem) (store.Requisition, error) {
	f.editing.edit, f.editing.version, f.editing.idem = edit, version, idem
	f.editing.calls++
	r := f.requisitions[id]
	if idem.Key != "" && idem.Key == f.editing.replayKey {
		return r, nil
	}
	if r.Version != version {
		return store.Requisition{}, httpx.NewProblem(http.StatusConflict, store.CodeVersionConflict, "stale")
	}
	if r.Status != store.RequisitionDraft {
		return store.Requisition{}, httpx.NewProblem(http.StatusConflict, store.CodeRequisitionState, "not a draft")
	}
	r.Version++
	if edit.Lines != nil {
		r.Lines, r.Total = edit.Lines, edit.Total
	}
	f.requisitions[id] = r
	return r, nil
}

func (f *fakeStore) CancelRequisitionLine(_ context.Context, id string, version int, lineID, _, reason string, idem store.Idem) (store.Requisition, error) {
	f.editing.version, f.editing.cancelled, f.editing.reason, f.editing.idem = version, lineID, reason, idem
	f.editing.calls++
	r := f.requisitions[id]
	r.Version++
	f.requisitions[id] = r
	return r, nil
}

func statusRequisition(h *harness, status string) string {
	id := ids.New()
	h.fake.requisitions[id] = store.Requisition{ID: id, LegalEntityID: leA, RequesterID: userA, Status: status, Version: 4, Currency: "USD",
		Total: decimal.NewFromInt(7000), Lines: []store.RequisitionLine{{ID: ids.New(), LineNo: 1, Category: "MRO", Status: "OPEN"}}}
	return id
}

const patchLines = `{"lines":[{"productId":"00000000-0000-7000-8000-0000000000f1","qty":"4","unit":"EA","estimatedUnitPrice":"2.50","requestedDate":"2026-11-01"}]}`

func patch(h *harness, id, user, body string, version string) *httptest.ResponseRecorder {
	return h.do(call{method: "PATCH", path: "/v1/procurement/requisitions/" + id, user: user, duties: []string{dutyRequisitionCreate}, body: body,
		headers: map[string]string{"Idempotency-Key": "p1", "If-Match": version}})
}

func TestPatchRequisition_RequesterEditsADraft(t *testing.T) {
	h := newHarness(t)
	id := statusRequisition(h, store.RequisitionDraft)
	rec := patch(h, id, userA, patchLines, `"4"`)
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"5"` {
		t.Fatalf("edit: %d %s", rec.Code, rec.Body)
	}
	got := h.fake.editing
	if got.version != 4 || len(got.edit.Lines) != 1 || got.edit.Lines[0].LineNo != 1 || !got.edit.Total.Equal(decimal.RequireFromString("10")) ||
		got.edit.Currency != "USD" || got.edit.ActorID != userA || got.idem.Key != "p1" || got.idem.Hash == "" {
		t.Fatalf("store got %+v", got)
	}

	// Only the operating unit: the lines stay.
	rec = patch(h, id, userA, `{"operatingUnitId":"`+ids.New()+`"}`, `"5"`)
	if rec.Code != http.StatusOK || h.fake.editing.edit.Lines != nil || h.fake.editing.edit.OperatingUnitID == "" {
		t.Fatalf("header only: %d %s %+v", rec.Code, rec.Body, h.fake.editing.edit)
	}
}

func TestPatchRequisition_RefusesWhatTheStateMachineDoes(t *testing.T) {
	h := newHarness(t)
	draft := statusRequisition(h, store.RequisitionDraft)
	for _, tc := range []struct {
		name         string
		id, user     string
		body, ifm    string
		status       int
		code         string
		duties       []string
		wantNoStore  bool
		customHeader map[string]string
	}{
		{"someone else", draft, userOther, patchLines, `"4"`, 403, codeNotRequester, nil, true, nil},
		{"in review", statusRequisition(h, store.RequisitionInReview), userA, patchLines, `"4"`, 409, store.CodeRequisitionState, nil, false, nil},
		{"approved", statusRequisition(h, store.RequisitionApproved), userA, patchLines, `"4"`, 409, store.CodeRequisitionState, nil, false, nil},
		{"stale version", draft, userA, patchLines, `"3"`, 409, store.CodeVersionConflict, nil, false, nil},
		{"a version not reached yet", draft, userA, patchLines, `"9"`, 409, store.CodeVersionConflict, nil, true, nil},
		{"empty body", draft, userA, `{}`, `"4"`, 422, "", nil, true, nil},
		{"no lines", draft, userA, `{"lines":[]}`, `"4"`, 422, "", nil, true, nil},
		{"unknown field", draft, userA, `{"legalEntityId":"` + leA + `"}`, `"4"`, 400, "", nil, true, nil},
		{"currency without lines", draft, userA, `{"currency":"EUR"}`, `"4"`, 422, "", nil, true, nil},
		{"bad line", draft, userA, `{"lines":[{"qty":"0","unit":"EA","requestedDate":"2026-11-01"}]}`, `"4"`, 422, "", nil, true, nil},
		{"no create duty", draft, userA, patchLines, `"4"`, 403, "AUTH-0003", []string{}, true, nil},
		{"no If-Match", draft, userA, patchLines, ``, 428, "", nil, true, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := h.fake.editing.calls
			duties := tc.duties
			if duties == nil {
				duties = []string{dutyRequisitionCreate}
			}
			hdr := map[string]string{"Idempotency-Key": "p2"}
			if tc.ifm != "" {
				hdr["If-Match"] = tc.ifm
			}
			rec := h.do(call{method: "PATCH", path: "/v1/procurement/requisitions/" + tc.id, user: tc.user, duties: duties, body: tc.body, headers: hdr})
			if rec.Code != tc.status || (tc.code != "" && problemCode(t, rec) != tc.code) {
				t.Fatalf("%d %s, want %d %s", rec.Code, rec.Body, tc.status, tc.code)
			}
			if tc.wantNoStore && h.fake.editing.calls != before {
				t.Fatal("a refused edit must not reach the store")
			}
		})
	}
}

// A retried PATCH of a requisition that was submitted since is answered from the Idempotency-Key with the
// requisition as it is, not refused for its status; the lines are not priced again for it.
func TestPatchRequisition_ReplayBeforeTheStateCheck(t *testing.T) {
	h := newHarness(t)
	id := statusRequisition(h, store.RequisitionInReview)
	h.fake.editing.replayKey = "p1"
	rec := patch(h, id, userA, patchLines, `"3"`)
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"4"` {
		t.Fatalf("replay: %d %s", rec.Code, rec.Body)
	}
	if got := h.fake.editing; got.calls != 1 || got.edit.Lines != nil || got.version != 3 || got.idem.Key != "p1" {
		t.Fatalf("store got %+v; want the edit unpriced at the caller's version", got)
	}
}

func TestWithdrawRequisition_IsTheRequestersAction(t *testing.T) {
	h := newHarness(t)
	id := reviewRequisition(h, []string{domain.RoleManager}, 0, "MRO")
	if code := decide(h, id, "withdraw", userApprover, []string{dutyRequisitionCreate}, ""); code != http.StatusForbidden {
		t.Fatalf("another user: %d, want 403", code)
	}
	if code := decide(h, id, "withdraw", userA, []string{dutyRequisitionApprove}, ""); code != http.StatusForbidden {
		t.Fatalf("approve duty only: %d, want 403", code)
	}
	if code := decide(h, id, "withdraw", userA, []string{dutyRequisitionCreate}, `{"comment":" wrong cost centre "}`); code != http.StatusOK {
		t.Fatalf("withdraw: %d", code)
	}
	got := h.fake.approval
	if got.act.Action != "withdraw" || got.act.ActorID != userA || got.act.Comment != "wrong cost centre" || got.version != 4 {
		t.Fatalf("store got %+v", got)
	}
	if h.api.requisitionDecisions.Load() != 0 || h.api.requisitionsCancelled.Load() != 0 {
		t.Fatal("a withdrawal is neither a decision nor a cancellation")
	}
}

func TestCancelRequisitionLine_IsTheRequestersAction(t *testing.T) {
	h := newHarness(t)
	id := statusRequisition(h, store.RequisitionApproved)
	line := h.fake.requisitions[id].Lines[0].ID
	post := func(user, body string, duties ...string) *httptest.ResponseRecorder {
		return h.do(call{method: "POST", path: "/v1/procurement/requisitions/" + id + ":cancel-line", user: user, duties: duties, body: body,
			headers: map[string]string{"Idempotency-Key": "c1", "If-Match": `"4"`}})
	}
	create := []string{dutyRequisitionCreate}
	for _, tc := range []struct {
		name   string
		rec    *httptest.ResponseRecorder
		status int
		code   string
	}{
		{"another user", post(userOther, `{"lineId":"`+line+`"}`, create...), 403, codeNotRequester},
		{"approve duty only", post(userA, `{"lineId":"`+line+`"}`, dutyRequisitionApprove), 403, ""},
		{"no line", post(userA, `{}`, create...), 422, ""},
		{"bad line id", post(userA, `{"lineId":"x"}`, create...), 422, ""},
		{"long comment", post(userA, `{"lineId":"`+line+`","comment":"`+strings.Repeat("x", 1001)+`"}`, create...), 422, ""},
	} {
		if tc.rec.Code != tc.status || (tc.code != "" && problemCode(t, tc.rec) != tc.code) {
			t.Fatalf("%s: %d %s, want %d %s", tc.name, tc.rec.Code, tc.rec.Body, tc.status, tc.code)
		}
	}
	if h.fake.editing.calls != 0 {
		t.Fatal("a refused cancel must not reach the store")
	}
	rec := post(userA, `{"lineId":"`+line+`","comment":" not needed "}`, create...)
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"5"` {
		t.Fatalf("cancel line: %d %s", rec.Code, rec.Body)
	}
	if got := h.fake.editing; got.cancelled != line || got.reason != "not needed" || got.version != 4 || got.idem.Key != "c1" {
		t.Fatalf("store got %+v", got)
	}
}
