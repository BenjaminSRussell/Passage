package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/quality/internal/domain"
)

func decode[T any](t *testing.T, body []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return v
}

// TestCertificateGate_AC3312OverHTTP is AC-33.12: an association requiring an EN
// 10204 3.1 certificate gives its receipt order that requirement; ACCEPT is
// rejected with QM-0125 until an in-specification certificate is recorded.
func TestCertificateGate_AC3312OverHTTP(t *testing.T) {
	f := newFake()
	h := newHandler(f)
	groupID, testID, outcomeID := createGroup(t, h, true)
	assoc := `{"trigger":"PURCHASE_RECEIPT","itemScope":"ALL","partyScope":"ALL","siteScope":"ALL","sequence":1,"testGroupId":"` + groupID +
		`","samplingPlan":{"method":"FIXED","fixedQty":"1"},"certificateRequired":"EN10204_31"}`
	if rec := call(h, reqSpec{method: http.MethodPost, path: "/v1/quality/associations", body: assoc, duties: allDuties, key: ids.New()}); rec.Code != http.StatusCreated {
		t.Fatalf("association: %d %s", rec.Code, rec.Body.String())
	}
	receipt := `{"trigger":"PURCHASE_RECEIPT","sourceRef":{"type":"receipt_line","id":"` + ids.New() + `"},"itemId":"` + ids.New() +
		`","siteId":"` + ids.New() + `","warehouseId":"` + ids.New() + `","lotQty":"40","uom":"KG","certificateRequired":"NONE"}`
	rec := call(h, reqSpec{method: http.MethodPost, path: "/v1/quality/quality-orders", body: receipt, duties: allDuties, key: ids.New()})
	order := decode[domain.Order](t, rec.Body.Bytes())
	if rec.Code != http.StatusCreated || order.CertificateRequired != domain.CertEN10204_31 {
		t.Fatalf("a receipt order takes its association's requirement, not the request's: %d %s", rec.Code, rec.Body.String())
	}
	results := `{"results":[{"testId":"` + testID + `","sampleNo":1,"outcomeId":"` + outcomeID + `"}]}`
	if rec := call(h, reqSpec{method: http.MethodPost, path: "/v1/quality/quality-orders/" + order.ID + "/results", body: results, duties: allDuties, key: ids.New()}); rec.Code != http.StatusCreated {
		t.Fatalf("results: %d %s", rec.Code, rec.Body.String())
	}
	view := call(h, reqSpec{method: http.MethodGet, path: "/v1/quality/quality-orders/" + order.ID, duties: "QM_VIEW"})
	if !strings.Contains(view.Body.String(), `"certificateStatus":"MISSING"`) {
		t.Fatalf("the order view shows the gate: %s", view.Body.String())
	}
	decide := func(key string) *httpResponse {
		r := call(h, reqSpec{method: http.MethodPost, path: "/v1/quality/quality-orders/" + order.ID + ":usage-decision",
			body: `{"lines":[{"decision":"ACCEPT","quantity":"40"}]}`, duties: allDuties, key: key, ifMatch: `"2"`})
		return &httpResponse{code: r.Code, body: r.Body.String()}
	}
	if got := decide("d-1"); got.code != http.StatusUnprocessableEntity || !strings.Contains(got.body, "QM-0125") {
		t.Fatalf("ACCEPT without the certificate: %d %s", got.code, got.body)
	}

	path := "/v1/quality/quality-orders/" + order.ID + "/certificates"
	cert := `{"type":"EN10204_31","documentRef":"EDI-4711","values":{"` + testID + `":{"outcome":"` + outcomeID + `"}}}`
	if rec := call(h, reqSpec{method: http.MethodPost, path: path, body: cert, duties: "QM_VIEW", key: "c-1"}); rec.Code != http.StatusForbidden {
		t.Fatalf("recording a certificate needs QM_RESULTS_RECORD: %d", rec.Code)
	}
	rec = call(h, reqSpec{method: http.MethodPost, path: path, body: cert, duties: allDuties, key: "c-1"})
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"inSpec":true`) || !strings.Contains(rec.Body.String(), `"certificateStatus":"RECEIVED_OK"`) {
		t.Fatalf("in-specification certificate: %d %s", rec.Code, rec.Body.String())
	}
	if again := call(h, reqSpec{method: http.MethodPost, path: path, body: cert, duties: allDuties, key: "c-1"}); again.Code != http.StatusOK || len(f.certs) != 1 {
		t.Fatalf("a replayed key answers 200 and records nothing: %d, %d records", again.Code, len(f.certs))
	}
	if got := decide("d-2"); got.code != http.StatusCreated {
		t.Fatalf("ACCEPT after the certificate: %d %s", got.code, got.body)
	}
}

type httpResponse struct {
	code int
	body string
}

// TestCertificate_OutOfSpecRaisesVendorNCR is BR-33.13's NOK leg: a certificate
// whose outcome is not a passing one is RECEIVED_NOK and hands a VENDOR NCR to
// the store with the receipt.
func TestCertificate_OutOfSpecRaisesVendorNCR(t *testing.T) {
	f := newFake()
	h := newHandler(f)
	orderID, testID, _ := orderWithGroup(t, f, h, domain.StatusPending, 2)
	o := f.orders[orderID]
	o.CertificateRequired, o.ItemID, o.UOM = domain.CertCoA, ids.New(), "EA"
	f.orders[orderID] = o
	path := "/v1/quality/quality-orders/" + orderID + "/certificates"
	body := `{"type":"COA","values":{"` + testID + `":{"outcome":"` + ids.New() + `"}}}`
	rec := call(h, reqSpec{method: http.MethodPost, path: path, body: body, duties: allDuties, key: "nok-1", user: "qa-1"})
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"certificateStatus":"RECEIVED_NOK"`) || !strings.Contains(rec.Body.String(), `"deviations":["appearance"]`) {
		t.Fatalf("NOK certificate: %d %s", rec.Code, rec.Body.String())
	}
	if len(f.certNCRs) != 1 || f.certNCRs[0].Source != domain.SourceVendor || f.certNCRs[0].QualityOrderID != orderID ||
		f.certNCRs[0].ProblemType != domain.ProblemCertificateNOK || !f.certNCRs[0].Qty.Equal(decimal.NewFromInt(5)) || f.certNCRs[0].CreatedBy != "qa-1" {
		t.Fatalf("the VENDOR NCR for the lot: %+v", f.certNCRs)
	}
	listed := call(h, reqSpec{method: http.MethodGet, path: path, duties: "QM_VIEW"})
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"required":"COA"`) || !strings.Contains(listed.Body.String(), `"certificateStatus":"RECEIVED_NOK"`) {
		t.Fatalf("certificate list: %d %s", listed.Code, listed.Body.String())
	}

	bad := call(h, reqSpec{method: http.MethodPost, path: path, body: `{"type":"COA","values":{"` + ids.New() + `":{"value":"1"}}}`, duties: allDuties, key: "bad-1"})
	if bad.Code != http.StatusBadRequest || !strings.Contains(bad.Body.String(), "is not a test of this order") {
		t.Fatalf("a value for another test: %d %s", bad.Code, bad.Body.String())
	}
	other, _, _ := orderWithGroup(t, f, h, domain.StatusPending, 2)
	reuse := call(h, reqSpec{method: http.MethodPost, path: "/v1/quality/quality-orders/" + other + "/certificates", body: `{"type":"COC"}`, duties: allDuties, key: "nok-1"})
	if reuse.Code != http.StatusUnprocessableEntity || !strings.Contains(reuse.Body.String(), "idempotency_key_reuse") {
		t.Fatalf("a key from another order: %d %s", reuse.Code, reuse.Body.String())
	}
	if rec := call(h, reqSpec{method: http.MethodGet, path: "/v1/quality/quality-orders/" + ids.New() + "/certificates", duties: "QM_VIEW"}); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown order: %d", rec.Code)
	}
}

// TestCertificateWaiver covers the waiver: it needs QM_USAGE_DECISION and a
// reason, an order with nothing to waive answers 409 QM-0180, and a waived order
// can be accepted.
func TestCertificateWaiver(t *testing.T) {
	f := newFake()
	h := newHandler(f)
	orderID := seedPending(t, f, h, "10", true)
	path := "/v1/quality/quality-orders/" + orderID + "/certificates:waive"
	if rec := call(h, reqSpec{method: http.MethodPost, path: path, body: `{"reason":"audit"}`, duties: "QM_RESULTS_RECORD,QM_VIEW", key: "w-0"}); rec.Code != http.StatusForbidden {
		t.Fatalf("a waiver needs QM_USAGE_DECISION: %d", rec.Code)
	}
	if rec := call(h, reqSpec{method: http.MethodPost, path: path, body: `{"reason":"audit"}`, duties: allDuties, key: "w-1"}); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "QM-0180") {
		t.Fatalf("nothing to waive: %d %s", rec.Code, rec.Body.String())
	}
	o := f.orders[orderID]
	o.CertificateRequired = domain.CertCoC
	f.orders[orderID] = o
	if rec := call(h, reqSpec{method: http.MethodPost, path: path, body: `{"reason":"  "}`, duties: allDuties, key: "w-2"}); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "reason") {
		t.Fatalf("a blank reason: %d %s", rec.Code, rec.Body.String())
	}
	rec := call(h, reqSpec{method: http.MethodPost, path: path, body: `{"reason":"supplier audit on file"}`, duties: allDuties, key: "w-3"})
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"certificateStatus":"WAIVED"`) || !strings.Contains(rec.Body.String(), `"type":"COC"`) {
		t.Fatalf("waiver: %d %s", rec.Code, rec.Body.String())
	}
	accept := call(h, reqSpec{method: http.MethodPost, path: "/v1/quality/quality-orders/" + orderID + ":usage-decision",
		body: `{"lines":[{"decision":"ACCEPT","quantity":"10"}]}`, duties: allDuties, key: "d-w", ifMatch: `"3"`})
	if accept.Code != http.StatusCreated {
		t.Fatalf("ACCEPT after a waiver: %d %s", accept.Code, accept.Body.String())
	}
	if rec := call(h, reqSpec{method: http.MethodPost, path: path, body: `{"reason":"again"}`, duties: allDuties, key: "w-4"}); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "QM-0109") {
		t.Fatalf("a closed order takes no waiver: %d %s", rec.Code, rec.Body.String())
	}
}

func TestManualOrder_CertificateRequirement(t *testing.T) {
	h := newHandler(newFake())
	groupID, _, _ := createGroup(t, h, true)
	body := func(req string) string {
		return `{"trigger":"MANUAL","sourceRef":{"type":"po_line","id":"` + ids.New() + `"},"itemId":"` + ids.New() + `","siteId":"` + ids.New() +
			`","warehouseId":"` + ids.New() + `","lotQty":"5","uom":"EA","testGroupId":"` + groupID + `","samplingPlan":{"method":"FIXED","fixedQty":"1"}` + req + `}`
	}
	rec := call(h, reqSpec{method: http.MethodPost, path: "/v1/quality/quality-orders", body: body(`,"certificateRequired":"COA"`), duties: allDuties, key: ids.New()})
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"certificateRequired":"COA"`) {
		t.Fatalf("manual order with a certificate: %d %s", rec.Code, rec.Body.String())
	}
	rec = call(h, reqSpec{method: http.MethodPost, path: "/v1/quality/quality-orders", body: body(""), duties: allDuties, key: ids.New()})
	if !strings.Contains(rec.Body.String(), `"certificateRequired":"NONE"`) {
		t.Fatalf("no requirement is NONE: %s", rec.Body.String())
	}
	rec = call(h, reqSpec{method: http.MethodPost, path: "/v1/quality/quality-orders", body: body(`,"certificateRequired":"MILL"`), duties: allDuties, key: ids.New()})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "certificateRequired") {
		t.Fatalf("unknown requirement: %d %s", rec.Code, rec.Body.String())
	}
}

// TestSamples_RegisterAndList covers FR-33.20 over HTTP: a retention sample gets
// its expiry and the order's unit, a pooled sample must name primary samples of
// the order (QM-0181), and the list filters by status as of today (AC-33.18).
func TestSamples_RegisterAndList(t *testing.T) {
	f := newFake()
	h := newHandler(f)
	orderID, _, _ := orderWithGroup(t, f, h, domain.StatusOpen, 1)
	o := f.orders[orderID]
	o.UOM = "KG"
	f.orders[orderID] = o
	post := func(body, key, duties string) (int, string) {
		rec := call(h, reqSpec{method: http.MethodPost, path: "/v1/quality/samples", body: body, duties: duties, key: key, user: "qa-1"})
		return rec.Code, rec.Body.String()
	}
	if code, _ := post(`{"qualityOrderId":"`+orderID+`","kind":"PRIMARY","quantity":"1"}`, "s-0", "QM_VIEW"); code != http.StatusForbidden {
		t.Fatalf("registering a sample needs QM_RESULTS_RECORD: %d", code)
	}
	code, body := post(`{"qualityOrderId":"`+orderID+`","kind":"RESERVE","quantity":"0.5"}`, "s-1", allDuties)
	if code != http.StatusBadRequest || !strings.Contains(body, "batchExpiresOn") {
		t.Fatalf("a retention sample needs the batch expiry: %d %s", code, body)
	}
	code, body = post(`{"qualityOrderId":"`+orderID+`","kind":"RESERVE","quantity":"0.5","batchExpiresOn":"2020-01-15","storageLocation":"RET-01"}`, "s-2", allDuties)
	reserve := decode[domain.Sample](t, []byte(body))
	if code != http.StatusCreated || reserve.RetentionExpiresOn != "2021-01-15" || reserve.Status != domain.SampleDueForDisposal || reserve.UOM != "KG" || reserve.RecordedBy != "qa-1" {
		t.Fatalf("an expired retention sample is due for disposal: %d %s", code, body)
	}
	code, body = post(`{"qualityOrderId":"`+orderID+`","kind":"PRIMARY","quantity":"1","container":"jar"}`, "s-3", allDuties)
	primary := decode[domain.Sample](t, []byte(body))
	if code != http.StatusCreated || primary.Status != domain.SampleActive {
		t.Fatalf("primary sample: %d %s", code, body)
	}
	if code, body := post(`{"qualityOrderId":"`+orderID+`","kind":"POOLED","quantity":"1","primaryIds":["`+reserve.ID+`"]}`, "s-4", allDuties); code != http.StatusUnprocessableEntity || !strings.Contains(body, "QM-0181") {
		t.Fatalf("a pooled sample of a reserve sample: %d %s", code, body)
	}
	if code, body := post(`{"qualityOrderId":"`+orderID+`","kind":"PRIMARY","quantity":"4"}`, "s-5", allDuties); code != http.StatusUnprocessableEntity || !strings.Contains(body, "QM-0182") {
		t.Fatalf("0.5 + 1 + 4 from a lot of 5: %d %s", code, body)
	}
	if code, _ := post(`{"qualityOrderId":"`+ids.New()+`","kind":"PRIMARY","quantity":"1"}`, "s-6", allDuties); code != http.StatusNotFound {
		t.Fatalf("unknown order: %d", code)
	}

	due := call(h, reqSpec{method: http.MethodGet, path: "/v1/quality/samples?status=DUE_FOR_DISPOSAL", duties: "QM_VIEW"})
	items := decode[list[domain.Sample]](t, due.Body.Bytes()).Items
	if due.Code != http.StatusOK || len(items) != 1 || items[0].ID != reserve.ID || items[0].Status != domain.SampleDueForDisposal {
		t.Fatalf("due for disposal: %d %s", due.Code, due.Body.String())
	}
	all := call(h, reqSpec{method: http.MethodGet, path: "/v1/quality/samples?qualityOrderId=" + orderID, duties: "QM_VIEW"})
	if n := len(decode[list[domain.Sample]](t, all.Body.Bytes()).Items); n != 2 {
		t.Fatalf("the order's samples: %d", n)
	}
	if bad := call(h, reqSpec{method: http.MethodGet, path: "/v1/quality/samples?status=EXPIRED&qualityOrderId=x", duties: "QM_VIEW"}); bad.Code != http.StatusBadRequest ||
		!strings.Contains(bad.Body.String(), "status") || !strings.Contains(bad.Body.String(), "qualityOrderId") {
		t.Fatalf("bad filters: %d %s", bad.Code, bad.Body.String())
	}
}
