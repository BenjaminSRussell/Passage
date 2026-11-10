package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

type ackLog struct {
	acks         []store.POAcknowledgement
	vendorID     string
	poID         string
	in           domain.AckInput
	idem         store.Idem
	decided      string
	approve      bool
	note         string
	listVendorID string
	po           store.PurchaseOrder
	err          error
}

func (f *fakeStore) AcknowledgePurchaseOrder(_ context.Context, vendorID, poID string, in domain.AckInput, idem store.Idem) (store.POAcknowledgement, error) {
	f.ack.vendorID, f.ack.poID, f.ack.in, f.ack.idem = vendorID, poID, in, idem
	return store.POAcknowledgement{ID: ids.New(), PurchaseOrderID: poID, VendorID: vendorID, VersionNo: in.VersionNo, Outcome: in.Outcome,
		Changes: []domain.LineChange{}}, f.ack.err
}

func (f *fakeStore) DecideAcknowledgement(_ context.Context, id string, approve bool, note string, _ store.Idem) (store.POAcknowledgement, error) {
	f.ack.decided, f.ack.approve, f.ack.note = id, approve, note
	return store.POAcknowledgement{ID: id, Outcome: domain.AckChangesProposed, Changes: []domain.LineChange{}}, f.ack.err
}

func (f *fakeStore) GetAcknowledgement(_ context.Context, id string) (store.POAcknowledgement, error) {
	for _, a := range f.ack.acks {
		if a.ID == id {
			return a, nil
		}
	}
	return store.POAcknowledgement{}, httpx.NewProblem(http.StatusNotFound, store.CodeAckNotFound, "acknowledgement not found")
}

func (f *fakeStore) ListAcknowledgements(_ context.Context, _ store.AckFilter, _ string, _ int) (store.Page[store.POAcknowledgement], error) {
	return store.Page[store.POAcknowledgement]{Items: f.ack.acks}, nil
}

func (f *fakeStore) ListAwaitingAcknowledgement(context.Context, string, int) (store.Page[store.PurchaseOrderAckSummary], error) {
	return store.Page[store.PurchaseOrderAckSummary]{Items: []store.PurchaseOrderAckSummary{}}, nil
}

func (f *fakeStore) ListVendorPurchaseOrders(_ context.Context, vendorID, _ string, _ int) (store.Page[store.PurchaseOrderAckSummary], error) {
	f.ack.listVendorID = vendorID
	return store.Page[store.PurchaseOrderAckSummary]{Items: []store.PurchaseOrderAckSummary{}}, nil
}

func (f *fakeStore) GetVendorPurchaseOrder(_ context.Context, vendorID, id string) (store.PurchaseOrder, error) {
	if f.ack.po.ID != id || f.ack.po.VendorID != vendorID {
		return store.PurchaseOrder{}, httpx.NewProblem(http.StatusNotFound, store.CodePurchaseOrderNotFound, "purchase order not found")
	}
	return f.ack.po, nil
}

func vendorHarness(t *testing.T, vendorID string) (*harness, string) {
	h := newHarness(t)
	h.api.WithVendorPrincipal(vendorFor(vendorID))
	return h, vendorID
}

// vendorFor stands in for a verified vendor credential in tests.
type vendorFor string

func (v vendorFor) VendorID(context.Context) (string, bool) { return string(v), v != "" }

const ackBody = `{"versionNo":2,"outcome":"ACCEPTED"}`

func TestVendorRoutes_RefuseACallerWithoutAVendor(t *testing.T) {
	h := newHarness(t)
	po := ids.New()
	for _, c := range []call{
		{method: "GET", path: "/v1/procurement/vendor/purchase-orders", user: userA, duties: []string{dutyVendorPortalRead}},
		{method: "GET", path: "/v1/procurement/vendor/purchase-orders/" + po, user: userA, duties: []string{dutyVendorPortalRead}},
		{method: "POST", path: "/v1/procurement/vendor/purchase-orders/" + po + "/acknowledgements", user: userA, duties: []string{dutyVendorPortalAcknowledge},
			body: ackBody, headers: idem("k1")},
	} {
		if rec := h.do(c); rec.Code != http.StatusForbidden || problemCode(t, rec) != codeScope {
			t.Errorf("%s %s with the duty but no vendor: %d %s", c.method, c.path, rec.Code, rec.Body)
		}
	}
	if h.fake.ack.vendorID != "" || h.fake.ack.listVendorID != "" {
		t.Fatal("the store was reached without a vendor")
	}
	for _, c := range []call{
		{method: "GET", path: "/v1/procurement/vendor/purchase-orders", user: userA, duties: []string{dutyPurchaseOrderInquire}},
		{method: "POST", path: "/v1/procurement/vendor/purchase-orders/" + po + "/acknowledgements", user: userA, duties: []string{dutyVendorPortalRead},
			body: ackBody, headers: idem("k1")},
	} {
		if rec := h.do(c); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s without the vendor duty: %d", c.method, c.path, rec.Code)
		}
	}
}

func TestVendorAcknowledge_UsesTheVerifiedVendorOnly(t *testing.T) {
	h, vendor := vendorHarness(t, ids.New())
	po := ids.New()
	path := "/v1/procurement/vendor/purchase-orders/" + po + "/acknowledgements?vendorId=" + ids.New()
	duties := []string{dutyVendorPortalAcknowledge}

	rec := h.do(call{method: "POST", path: path, user: userA, duties: duties, body: ackBody, headers: map[string]string{"Idempotency-Key": "k1", "X-Vendor-Id": ids.New()}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("accept: %d %s", rec.Code, rec.Body)
	}
	if h.fake.ack.vendorID != vendor || h.fake.ack.poID != po || h.fake.ack.idem.Key != "k1" || h.fake.ack.in.Outcome != domain.AckAccepted {
		t.Fatalf("store got vendor %s po %s key %s outcome %s", h.fake.ack.vendorID, h.fake.ack.poID, h.fake.ack.idem.Key, h.fake.ack.in.Outcome)
	}

	for name, tc := range map[string]struct {
		body    string
		headers map[string]string
		status  int
	}{
		"no idempotency key":        {ackBody, nil, http.StatusBadRequest},
		"reject needs a reason":     {`{"versionNo":2,"outcome":"REJECTED"}`, idem("k2"), http.StatusUnprocessableEntity},
		"proposal needs changes":    {`{"versionNo":2,"outcome":"CHANGES_PROPOSED"}`, idem("k3"), http.StatusUnprocessableEntity},
		"proposal with empty line":  {`{"versionNo":2,"outcome":"CHANGES_PROPOSED","changes":[{"lineNo":1}]}`, idem("k4"), http.StatusUnprocessableEntity},
		"unknown outcome":           {`{"versionNo":2,"outcome":"MAYBE"}`, idem("k5"), http.StatusUnprocessableEntity},
		"unknown member is refused": {`{"versionNo":2,"outcome":"ACCEPTED","vendorId":"` + ids.New() + `"}`, idem("k6"), http.StatusBadRequest},
	} {
		rec := h.do(call{method: "POST", path: "/v1/procurement/vendor/purchase-orders/" + po + "/acknowledgements", user: userA, duties: duties, body: tc.body, headers: tc.headers})
		if rec.Code != tc.status {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}

	good := `{"versionNo":2,"outcome":"CHANGES_PROPOSED","reason":"mill delay","changes":[{"lineNo":1,"qty":"90","unitPrice":"10.50","deliveryDate":"2027-02-01"}]}`
	rec = h.do(call{method: "POST", path: "/v1/procurement/vendor/purchase-orders/" + po + "/acknowledgements", user: userA, duties: duties, body: good, headers: idem("k7")})
	got := h.fake.ack.in
	if rec.Code != http.StatusCreated || len(got.Changes) != 1 || !got.Changes[0].Qty.Equal(decimal.NewFromInt(90)) || got.Changes[0].DeliveryDate.String() != "2027-02-01" {
		t.Fatalf("proposal: %d %s -> %+v", rec.Code, rec.Body, got)
	}
}

func TestVendorReads_ScopedToTheVerifiedVendor(t *testing.T) {
	h, vendor := vendorHarness(t, ids.New())
	po := store.PurchaseOrder{ID: ids.New(), Number: "PO-1", VendorID: vendor, Currency: "USD", ApprovalStatus: domain.POStatusConfirmed, VersionNo: 1,
		CreatedBy: userB, ChangedBy: userB, Lines: []store.PurchaseOrderLine{{LineNo: 1, Qty: decimal.NewFromInt(5), Unit: "EA", PriceSource: "TRADE_AGREEMENT:x",
			RequisitionLineID: ids.New(), Status: domain.POLineOpen}}, Charges: []store.PurchaseOrderCharge{{Calc: "FIXED", DebitAccountID: ids.New()}}}
	h.fake.ack.po = po
	read := []string{dutyVendorPortalRead}

	rec := h.do(call{method: "GET", path: "/v1/procurement/vendor/purchase-orders?vendorId=" + ids.New(), user: userA, duties: read})
	if rec.Code != http.StatusOK || h.fake.ack.listVendorID != vendor {
		t.Fatalf("list: %d %s listed vendor %s", rec.Code, rec.Body, h.fake.ack.listVendorID)
	}
	rec = h.do(call{method: "GET", path: "/v1/procurement/vendor/purchase-orders/" + po.ID, user: userA, duties: read})
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, `"number":"PO-1"`) {
		t.Fatalf("detail: %d %s", rec.Code, body)
	}
	for _, internal := range []string{"createdBy", "changedBy", "priceSource", "requisitionLineId", "debitAccountId", "legalEntityId"} {
		if strings.Contains(body, internal) {
			t.Errorf("vendor detail leaks %s: %s", internal, body)
		}
	}
	other := newHarness(t)
	other.api.WithVendorPrincipal(vendorFor(ids.New()))
	other.fake.ack.po = po
	if rec := other.do(call{method: "GET", path: "/v1/procurement/vendor/purchase-orders/" + po.ID, user: userA, duties: read}); rec.Code != http.StatusNotFound {
		t.Fatalf("another vendor read the order: %d %s", rec.Code, rec.Body)
	}
}

func TestAcknowledgementDecision_NeedsConfirmDutyAndScope(t *testing.T) {
	h := newHarness(t)
	ack := store.POAcknowledgement{ID: ids.New(), LegalEntityID: leA, Outcome: domain.AckChangesProposed}
	foreign := store.POAcknowledgement{ID: ids.New(), LegalEntityID: ids.New(), Outcome: domain.AckChangesProposed}
	h.fake.ack.acks = []store.POAcknowledgement{ack, foreign}
	confirm := []string{dutyPurchaseOrderConfirm}
	path := func(id, action string) string { return "/v1/procurement/acknowledgements/" + id + ":" + action }

	for name, tc := range map[string]struct {
		c      call
		status int
	}{
		"inquire alone cannot decide": {call{method: "POST", path: path(ack.ID, "approve"), user: userA, duties: []string{dutyPurchaseOrderInquire}, headers: idem("a")}, 403},
		"maintain alone cannot":       {call{method: "POST", path: path(ack.ID, "approve"), user: userA, duties: []string{dutyPurchaseOrderMaintain}, headers: idem("a")}, 403},
		"no key":                      {call{method: "POST", path: path(ack.ID, "approve"), user: userA, duties: confirm}, 400},
		"unknown action":              {call{method: "POST", path: path(ack.ID, "delete"), user: userA, duties: confirm, headers: idem("a")}, 404},
		"unknown proposal":            {call{method: "POST", path: path(ids.New(), "approve"), user: userA, duties: confirm, headers: idem("a")}, 404},
		"other legal entity":          {call{method: "POST", path: path(foreign.ID, "approve"), user: userA, duties: confirm, headers: idem("a")}, 403},
	} {
		if rec := h.do(tc.c); rec.Code != tc.status {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if h.fake.ack.decided != "" {
		t.Fatal("a refused decision reached the store")
	}

	if rec := h.do(call{method: "POST", path: path(ack.ID, "decline"), user: userA, duties: confirm, body: `{"note":"too late"}`, headers: idem("d")}); rec.Code != http.StatusOK ||
		h.fake.ack.decided != ack.ID || h.fake.ack.approve || h.fake.ack.note != "too late" {
		t.Fatalf("decline: %d %s %+v", rec.Code, rec.Body, h.fake.ack)
	}
	if rec := h.do(call{method: "POST", path: path(ack.ID, "approve"), user: userA, duties: confirm, headers: idem("e")}); rec.Code != http.StatusOK || !h.fake.ack.approve {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body)
	}
}

func TestAcknowledgementReads_NeedInquireAndValidateFilters(t *testing.T) {
	h := newHarness(t)
	for _, p := range []string{"/v1/procurement/acknowledgements", "/v1/procurement/acknowledgements/awaiting", "/v1/procurement/purchase-orders/" + ids.New() + "/acknowledgements"} {
		if rec := h.do(call{method: "GET", path: p, user: userA, duties: []string{dutyPurchaseOrderMaintain}}); rec.Code != http.StatusForbidden {
			t.Errorf("%s without inquire: %d", p, rec.Code)
		}
		if rec := h.do(call{method: "GET", path: p, user: userA, duties: []string{dutyPurchaseOrderInquire}}); rec.Code != http.StatusOK {
			t.Errorf("%s with inquire: %d %s", p, rec.Code, rec.Body)
		}
	}
	for _, q := range []string{"outcome=DONE", "purchaseOrderId=x"} {
		if rec := h.do(call{method: "GET", path: "/v1/procurement/acknowledgements?" + q, user: userA, duties: []string{dutyPurchaseOrderInquire}}); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("?%s: %d", q, rec.Code)
		}
	}
}
