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

func (f *fakeStore) EditPurchaseOrder(_ context.Context, id string, ifMatch int, next store.PurchaseOrder) (store.PurchaseOrder, error) {
	f.lastEdit.id, f.lastEdit.ifMatch, f.lastEdit.next = id, ifMatch, next
	next.ID, next.RowVersion, next.ApprovalStatus = id, ifMatch+1, domain.POStatusDraft
	return next, nil
}

func (f *fakeStore) ActOnPurchaseOrder(_ context.Context, id string, ifMatch int, action string, idem store.Idem) (store.PurchaseOrder, error) {
	f.lastAction.id, f.lastAction.action, f.lastAction.ifMatch, f.lastAction.idem = id, action, ifMatch, idem
	if f.pol.actErr != nil {
		return store.PurchaseOrder{}, f.pol.actErr
	}
	po, ok := f.poByID[id]
	if !ok {
		return store.PurchaseOrder{}, httpx.NewProblem(http.StatusNotFound, store.CodePurchaseOrderNotFound, "purchase order not found")
	}
	next, err := domain.NextApproval(po.ApprovalStatus, action)
	if action == domain.POActionCancel {
		next, err = domain.POStatusCancelled, nil
	}
	if err != nil {
		return store.PurchaseOrder{}, err
	}
	po.ApprovalStatus, po.RowVersion = next, ifMatch+1
	return po, nil
}

func (f *fakeStore) ListPurchaseOrderVersions(_ context.Context, poID string) ([]store.PurchaseOrderVersion, error) {
	if _, ok := f.poByID[poID]; !ok {
		return nil, httpx.NewProblem(http.StatusNotFound, store.CodePurchaseOrderNotFound, "purchase order not found")
	}
	return f.versions[poID], nil
}

func (f *fakeStore) GetPurchaseOrderVersion(_ context.Context, poID string, no int) (store.PurchaseOrderVersion, error) {
	for _, v := range f.versions[poID] {
		if v.VersionNo == no {
			return v, nil
		}
	}
	return store.PurchaseOrderVersion{}, httpx.NewProblem(http.StatusNotFound, store.CodePOVersionNotFound, "version not found")
}

const (
	vendorA  = "00000000-0000-7000-8000-0000000000c1"
	productA = "00000000-0000-7000-8000-0000000000d1"
	reqLineA = "00000000-0000-7000-8000-0000000000e1"
)

// storedPO is a confirmed PO with a trade-agreement priced line 1 ordering a
// requisition line, a cancelled line 2 and a manual line 3.
func storedPO(h *harness, status string) store.PurchaseOrder {
	d := decimal.RequireFromString
	po := store.PurchaseOrder{ID: ids.New(), Number: "PO-00000007", LegalEntityID: leA, VendorID: vendorA, Currency: "USD",
		ApprovalStatus: status, VersionNo: 1, RowVersion: 5, Lines: []store.PurchaseOrderLine{
			{LineNo: 1, ProductID: productA, Qty: d("10"), Unit: "EA", UnitPrice: d("9.5"), PriceUnit: d("1"), DiscountPct: d("0"),
				PriceSource: "TRADE_AGREEMENT:" + ids.New(), RequisitionLineID: reqLineA, Status: domain.POLineOpen},
			{LineNo: 2, Description: "Old", Qty: d("1"), Unit: "EA", UnitPrice: d("1"), PriceUnit: d("1"), PriceSource: "MANUAL", Status: domain.POLineCancelled},
			{LineNo: 3, Description: "Freight box", Qty: d("2"), Unit: "EA", UnitPrice: d("4"), PriceUnit: d("1"), PriceSource: "MANUAL", Status: domain.POLineOpen},
		}}
	h.fake.poByID[po.ID] = po
	return po
}

func poPath(id string) string { return "/v1/procurement/purchase-orders/" + id }

func ifMatchIdem(version, key string) map[string]string {
	return map[string]string{"If-Match": `"` + version + `"`, "Idempotency-Key": key}
}

func TestOrderAction_EnforcesTheDutyOfEachAction(t *testing.T) {
	maintain, confirm, vendorConfirm := []string{dutyPurchaseOrderMaintain}, []string{dutyPurchaseOrderConfirm}, []string{dutyPurchaseOrderVendorConfirm}
	tests := []struct {
		action, status string
		duties         []string
		want           int
		wantStatus     string
	}{
		{"submit", domain.POStatusDraft, maintain, 200, domain.POStatusInReview},
		{"cancel", domain.POStatusConfirmed, maintain, 200, domain.POStatusCancelled},
		{"approve", domain.POStatusInReview, confirm, 200, domain.POStatusApproved},
		{"reject", domain.POStatusInReview, confirm, 200, domain.POStatusRejected},
		{"confirm", domain.POStatusApproved, confirm, 200, domain.POStatusConfirmed},
		{"confirm", domain.POStatusApproved, vendorConfirm, 200, domain.POStatusConfirmed},
		{"approve", domain.POStatusInReview, vendorConfirm, 403, ""},
		{"reject", domain.POStatusInReview, vendorConfirm, 403, ""},
		{"submit", domain.POStatusDraft, vendorConfirm, 403, ""},
		{"cancel", domain.POStatusConfirmed, vendorConfirm, 403, ""},
		{"approve", domain.POStatusInReview, maintain, 403, ""},
		{"confirm", domain.POStatusApproved, maintain, 403, ""},
		{"submit", domain.POStatusDraft, confirm, 403, ""},
		{"cancel", domain.POStatusDraft, confirm, 403, ""},
	}
	for _, tc := range tests {
		t.Run(tc.action+" with "+tc.duties[0], func(t *testing.T) {
			h := newHarness(t)
			po := storedPO(h, tc.status)
			rec := h.do(call{method: "POST", path: poPath(po.ID) + ":" + tc.action, user: userA, duties: tc.duties, headers: ifMatchIdem("5", "k1")})
			if rec.Code != tc.want {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.want, rec.Body)
			}
			if tc.want == 403 {
				if problemCode(t, rec) != codeScope || h.fake.lastAction.action != "" {
					t.Fatalf("want AUTH-0003 and no store call, got %s and %q", problemCode(t, rec), h.fake.lastAction.action)
				}
				return
			}
			var out store.PurchaseOrder
			_ = json.Unmarshal(rec.Body.Bytes(), &out)
			if out.ApprovalStatus != tc.wantStatus || rec.Header().Get("ETag") != `"6"` || h.fake.lastAction.ifMatch != 5 {
				t.Fatalf("status %s etag %s ifMatch %d", out.ApprovalStatus, rec.Header().Get("ETag"), h.fake.lastAction.ifMatch)
			}
		})
	}
}

func TestOrderAction_RefusesBeforeTheStore(t *testing.T) {
	both := []string{dutyPurchaseOrderMaintain, dutyPurchaseOrderConfirm}
	id := ids.New()
	tests := []struct {
		name string
		c    call
		want int
		code string
	}{
		{"no tenant", call{method: "POST", path: poPath(id) + ":submit", noTenant: true, headers: ifMatchIdem("1", "k")}, 401, codeAuth},
		{"missing If-Match", call{method: "POST", path: poPath(id) + ":approve", duties: both, headers: idem("k")}, 428, codeIfMatchMissing},
		{"bad If-Match", call{method: "POST", path: poPath(id) + ":approve", duties: both, headers: map[string]string{"If-Match": "W/x", "Idempotency-Key": "k"}}, 400, codeIfMatchBad},
		{"missing key", call{method: "POST", path: poPath(id) + ":approve", duties: both, headers: map[string]string{"If-Match": `"1"`}}, 400, codeIdemRequired},
		{"unknown action", call{method: "POST", path: poPath(id) + ":explode", duties: both, headers: ifMatchIdem("1", "k")}, 404, codeUnknownAction},
		{"no action", call{method: "POST", path: poPath(id), duties: both, headers: ifMatchIdem("1", "k")}, 404, codeUnknownAction},
		{"bad id", call{method: "POST", path: poPath("nope") + ":approve", duties: both, headers: ifMatchIdem("1", "k")}, 404, codeUnknownAction},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			rec := h.do(tc.c)
			if rec.Code != tc.want || problemCode(t, rec) != tc.code || h.fake.lastAction.action != "" {
				t.Fatalf("got %d %s (store action %q), want %d %s", rec.Code, problemCode(t, rec), h.fake.lastAction.action, tc.want, tc.code)
			}
		})
	}
}

func TestOrderAction_StateErrorsAre422WithTheirCode(t *testing.T) {
	h := newHarness(t)
	po := storedPO(h, domain.POStatusDraft)
	rec := h.do(call{method: "POST", path: poPath(po.ID) + ":confirm", duties: []string{dutyPurchaseOrderConfirm}, headers: ifMatchIdem("5", "k")})
	if rec.Code != 422 || problemCode(t, rec) != domain.CodeConfirmPrecondition {
		t.Fatalf("confirm DRAFT: %d %s, want 422 PRC-PO-008", rec.Code, problemCode(t, rec))
	}
	rec = h.do(call{method: "POST", path: poPath(po.ID) + ":approve", duties: []string{dutyPurchaseOrderConfirm}, headers: ifMatchIdem("5", "k2")})
	if rec.Code != 422 || problemCode(t, rec) != domain.CodePOState {
		t.Fatalf("approve DRAFT: %d %s, want 422 PRC-PO-STATE", rec.Code, problemCode(t, rec))
	}
}

func TestOrderAction_IdempotencyHashIsScopedToActionAndVersion(t *testing.T) {
	h := newHarness(t)
	po := storedPO(h, domain.POStatusInReview)
	duties := []string{dutyPurchaseOrderConfirm}
	h.do(call{method: "POST", path: poPath(po.ID) + ":approve", duties: duties, headers: ifMatchIdem("5", "same")})
	approve := h.fake.lastAction.idem
	h.do(call{method: "POST", path: poPath(po.ID) + ":reject", duties: duties, headers: ifMatchIdem("5", "same")})
	reject := h.fake.lastAction.idem
	h.do(call{method: "POST", path: poPath(po.ID) + ":approve", duties: duties, headers: ifMatchIdem("6", "same")})
	later := h.fake.lastAction.idem
	if approve.Key != "same" || approve.Hash == reject.Hash || approve.Hash == later.Hash {
		t.Fatalf("hashes must differ by action and If-Match: %+v %+v %+v", approve, reject, later)
	}
}

const editBody = `{"legalEntityId":"` + leA + `","vendorId":"` + vendorA + `","currency":"USD","lines":[
 {"lineNo":1,"productId":"` + productA + `","qty":"12","unit":"EA","deliveryDate":"2026-04-01"},
 {"description":"Pallet","qty":"1","unit":"EA","unitPrice":"25","deliveryDate":"2026-04-02"}],
 "charges":[{"calc":"FIXED","value":"5"}]}`

// TestEditOrder_KeepsPriceAndLinksAndNumbersNewLines: line 1 keeps its trade
// agreement price, source and requisition link; line 3 is left out (the store
// cancels it); the new line is numbered after the cancelled line 2's highest
// neighbour, 4; totals are repriced.
func TestEditOrder_KeepsPriceAndLinksAndNumbersNewLines(t *testing.T) {
	h := newHarness(t)
	po := storedPO(h, domain.POStatusConfirmed)
	rec := h.do(call{method: "PATCH", path: poPath(po.ID), duties: []string{dutyPurchaseOrderMaintain}, body: editBody,
		headers: map[string]string{"If-Match": `"5"`}})
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if rec.Header().Get("ETag") != `"6"` || h.fake.lastEdit.ifMatch != 5 || h.fake.lastEdit.id != po.ID {
		t.Fatalf("etag %s, edit %+v", rec.Header().Get("ETag"), h.fake.lastEdit)
	}
	next := h.fake.lastEdit.next
	if len(next.Lines) != 2 {
		t.Fatalf("lines = %+v", next.Lines)
	}
	l1, l4 := next.Lines[0], next.Lines[1]
	if l1.LineNo != 1 || !l1.UnitPrice.Equal(decimal.RequireFromString("9.5")) || l1.PriceSource != po.Lines[0].PriceSource ||
		l1.RequisitionLineID != reqLineA || !l1.LineAmount.Equal(decimal.RequireFromString("114")) {
		t.Fatalf("line 1 = %+v", l1)
	}
	if l4.LineNo != 4 || l4.PriceSource != "MANUAL" || !l4.LineAmount.Equal(decimal.RequireFromString("25")) {
		t.Fatalf("new line = %+v", l4)
	}
	if !next.Total.Equal(decimal.RequireFromString("144")) || len(next.Charges) != 1 {
		t.Fatalf("total %s charges %+v, want 114 + 25 + 5 = 144", next.Total, next.Charges)
	}
}

func TestEditOrder_Rejections(t *testing.T) {
	maintain := []string{dutyPurchaseOrderMaintain}
	line := func(extra string) string {
		return `{"legalEntityId":"` + leA + `","vendorId":"` + vendorA + `","currency":"USD","lines":[` + extra + `]}`
	}
	tests := []struct {
		name   string
		body   string
		hdr    map[string]string
		scope  string
		status int
		code   string
		field  string
	}{
		{"missing If-Match", editBody, nil, "", 428, codeIfMatchMissing, ""},
		{"unknown line", line(`{"lineNo":9,"description":"x","qty":"1","unit":"EA","unitPrice":"1","deliveryDate":"2026-04-01"}`), nil, "", 422, codeInvalid, "lines[0].lineNo"},
		{"cancelled line", line(`{"lineNo":2,"description":"x","qty":"1","unit":"EA","unitPrice":"1","deliveryDate":"2026-04-01"}`), nil, "", 422, codeInvalid, "lines[0].lineNo"},
		{"duplicate line", line(`{"lineNo":3,"description":"x","qty":"1","unit":"EA","unitPrice":"1","deliveryDate":"2026-04-01"},
			{"lineNo":3,"description":"y","qty":"1","unit":"EA","unitPrice":"1","deliveryDate":"2026-04-01"}`), nil, "", 422, codeInvalid, "lines[1].lineNo"},
		// A different unit is not the stored line any more, so it is repriced
		// from trade agreements, of which the fake has none.
		{"changed unit reprices", line(`{"lineNo":1,"productId":"` + productA + `","qty":"1","unit":"BOX","deliveryDate":"2026-04-01"}`), nil, "", 422, codeNoPrice, ""},
		{"outside scope", editBody, nil, "00000000-0000-7000-8000-0000000001e9", 403, codeScope, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			po := storedPO(h, domain.POStatusConfirmed)
			hdr := tc.hdr
			if hdr == nil && tc.code != codeIfMatchMissing {
				hdr = map[string]string{"If-Match": `"5"`}
			}
			rec := h.do(call{method: "PATCH", path: poPath(po.ID), duties: maintain, scope: tc.scope, body: tc.body, headers: hdr})
			if rec.Code != tc.status || problemCode(t, rec) != tc.code {
				t.Fatalf("got %d %s, want %d %s: %s", rec.Code, problemCode(t, rec), tc.status, tc.code, rec.Body)
			}
			if tc.field != "" {
				var p httpx.Problem
				_ = json.Unmarshal(rec.Body.Bytes(), &p)
				if _, ok := p.Errors[tc.field]; !ok {
					t.Fatalf("errors %v, want %s", p.Errors, tc.field)
				}
			}
			if h.fake.lastEdit.id != "" {
				t.Fatal("the store must not be called")
			}
		})
	}
	h := newHarness(t)
	rec := h.do(call{method: "PATCH", path: poPath(ids.New()), duties: maintain, body: editBody, headers: map[string]string{"If-Match": `"1"`}})
	if rec.Code != 404 || problemCode(t, rec) != store.CodePurchaseOrderNotFound {
		t.Fatalf("missing PO: %d %s", rec.Code, problemCode(t, rec))
	}
}

func TestOrderVersions_ListAndDiff(t *testing.T) {
	h := newHarness(t)
	po := storedPO(h, domain.POStatusInReview)
	patch := []domain.PatchOp{{Op: "replace", Path: "/lines/0/qty", Value: "110"}}
	h.fake.versions[po.ID] = []store.PurchaseOrderVersion{{VersionNo: 1, Patch: []domain.PatchOp{}}, {VersionNo: 2, ReapprovalRequired: true, Patch: patch}}
	inquire := []string{dutyPurchaseOrderInquire}

	rec := h.do(call{method: "GET", path: poPath(po.ID) + "/versions", duties: inquire})
	var list struct{ Items []store.PurchaseOrderVersion }
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || rec.Code != 200 || len(list.Items) != 2 {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}

	rec = h.do(call{method: "GET", path: poPath(po.ID) + "/versions/2/diff", duties: inquire})
	var diff versionDiff
	if err := json.Unmarshal(rec.Body.Bytes(), &diff); err != nil || rec.Code != 200 {
		t.Fatalf("diff: %d %s", rec.Code, rec.Body)
	}
	if diff.FromVersion != 1 || diff.ToVersion != 2 || diff.Confirmed || !diff.ReapprovalRequired || len(diff.Patch) != 1 || diff.Patch[0].Path != "/lines/0/qty" {
		t.Fatalf("diff = %+v", diff)
	}

	for path, want := range map[string]struct {
		status int
		code   string
	}{
		poPath(po.ID) + "/versions/0/diff":   {422, codeInvalid},
		poPath(po.ID) + "/versions/two/diff": {422, codeInvalid},
		poPath(po.ID) + "/versions/9/diff":   {404, store.CodePOVersionNotFound},
		poPath(ids.New()) + "/versions":      {404, store.CodePurchaseOrderNotFound},
	} {
		rec := h.do(call{method: "GET", path: path, duties: inquire})
		if rec.Code != want.status || problemCode(t, rec) != want.code {
			t.Errorf("%s: %d %s, want %d %s", path, rec.Code, problemCode(t, rec), want.status, want.code)
		}
	}
	if rec := h.do(call{method: "GET", path: poPath(po.ID) + "/versions", duties: []string{dutyPurchaseOrderMaintain}}); rec.Code != 403 {
		t.Fatalf("versions without procurement.purchase-order.inquire: %d", rec.Code)
	}
}
