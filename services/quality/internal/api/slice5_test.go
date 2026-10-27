package api

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/quality/internal/domain"
)

func seedStudy(f *fakeStore) string {
	id := ids.New()
	start := time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC)
	pulls, _ := domain.PullSchedule(start, []int{0, 3, 6}, 7)
	pulls[0].Status = domain.PullOrdered
	f.studies[id] = domain.Study{ID: id, Status: domain.StudyPlanned, Version: 1, Pulls: pulls,
		StudySpec: domain.StudySpec{ItemID: ids.New(), BatchID: "B-1", StartDate: start, PullMonths: []int{0, 3, 6}}}
	return id
}

func TestStudyTransition_IfMatchLifecycleAndTermination(t *testing.T) {
	f := newFake()
	h := newHandler(f)
	id := seedStudy(f)
	path := "/v1/quality/stability-studies/" + id + ":transition"
	move := func(body, ifMatch, duties string) (int, string) {
		rec := call(h, reqSpec{method: http.MethodPost, path: path, body: body, duties: duties, ifMatch: ifMatch, user: "stab-1"})
		return rec.Code, rec.Body.String() + "|etag=" + rec.Header().Get("ETag")
	}
	if code, _ := move(`{"to":"ACTIVE"}`, `"1"`, "QM_VIEW"); code != http.StatusForbidden {
		t.Fatalf("QM_VIEW cannot change a study: %d", code)
	}
	if code, _ := move(`{"to":"ACTIVE"}`, "", dutyStability); code != http.StatusPreconditionRequired {
		t.Fatalf("no If-Match: %d", code)
	}
	if code, _ := move(`{"to":"ACTIVE"}`, `"4"`, dutyStability); code != http.StatusPreconditionFailed {
		t.Fatalf("stale If-Match: %d", code)
	}
	if code, body := move(`{"to":"COMPLETED"}`, `"1"`, dutyStability); code != http.StatusConflict || !strings.Contains(body, domain.CodeStudyStatus) {
		t.Fatalf("PLANNED cannot complete: %d %s", code, body)
	}
	if code, body := move(`{"to":"ACTIVE"}`, `"1"`, dutyStability); code != http.StatusOK || !strings.Contains(body, `"status":"ACTIVE"`) ||
		!strings.Contains(body, `etag="2"`) || !strings.Contains(body, `"statusChangedBy":"stab-1"`) {
		t.Fatalf("PLANNED -> ACTIVE: %d %s", code, body)
	}
	if code, body := move(`{"to":"PLANNED"}`, `"2"`, dutyStability); code != http.StatusBadRequest {
		t.Fatalf("back to PLANNED is not a target: %d %s", code, body)
	}
	if code, body := move(`{"to":"TERMINATED"}`, `"2"`, dutyStability); code != http.StatusBadRequest || !strings.Contains(body, "reason") {
		t.Fatalf("termination without a reason: %d %s", code, body)
	}
	code, body := move(`{"to":"TERMINATED","reason":"chamber 3 failed"}`, `"2"`, dutyStability)
	if code != http.StatusOK || !strings.Contains(body, `"statusReason":"chamber 3 failed"`) {
		t.Fatalf("ACTIVE -> TERMINATED: %d %s", code, body)
	}
	st := f.studies[id]
	if st.Pulls[0].Status != domain.PullOrdered || st.Pulls[1].Status != domain.PullCancelled || st.Pulls[2].Status != domain.PullCancelled {
		t.Fatalf("termination cancels the PLANNED pulls only: %+v", st.Pulls)
	}
	if code, _ := move(`{"to":"COMPLETED"}`, `"3"`, dutyStability); code != http.StatusConflict {
		t.Fatalf("TERMINATED is final: %d", code)
	}
	if rec := call(h, reqSpec{method: http.MethodPost, path: "/v1/quality/stability-studies/" + ids.New() + ":transition", body: `{"to":"ACTIVE"}`,
		duties: dutyStability, ifMatch: `"1"`}); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown study: %d", rec.Code)
	}
}

// seedSourceInspection closes a SOURCE_INSPECTION order of a PO line and batch
// whose decision accepted acceptQty (and deviationQty with a deviation).
func seedSourceInspection(f *fakeStore, poLine, batch string, acceptQty, deviationQty int64) {
	id := ids.New()
	lot := acceptQty + deviationQty
	f.orders[id] = domain.Order{ID: id, Trigger: domain.TriggerSourceInspect, SourceRef: domain.SourceRef{Type: "po_line", ID: poLine},
		Dims: map[string]string{"batch": batch}, LotQty: decimal.NewFromInt(lot), Status: domain.StatusClosed, Version: 3}
	f.decisions[ids.New()] = domain.Decision{ID: ids.New(), OrderID: id,
		Split: domain.Split{Accept: decimal.NewFromInt(acceptQty), Reject: decimal.Zero, Deviation: decimal.NewFromInt(deviationQty)}}
}

// TestReceiptCheck_AC3314OverHTTP is AC-33.14 through the API: a receipt of the
// source-inspected batch is skipped (QM-0128), another batch is not, and the
// accepted quantity is used up across receipts.
func TestReceiptCheck_AC3314OverHTTP(t *testing.T) {
	f := newFake()
	h := newHandler(f)
	po := ids.New()
	seedSourceInspection(f, po, "B", 900, 100)
	seedSourceInspection(f, ids.New(), "B", 5000, 0)
	pending := ids.New()
	f.orders[pending] = domain.Order{ID: pending, Trigger: domain.TriggerSourceInspect, SourceRef: domain.SourceRef{ID: po},
		Dims: map[string]string{"batch": "B"}, Status: domain.StatusPending}
	line1, line2, line3 := ids.New(), ids.New(), ids.New()
	body := func(line, batch, qty string) string {
		return `{"poLineId":"` + po + `","batch":"` + batch + `","receiptLineId":"` + line + `","receivedQty":"` + qty + `"}`
	}
	check := func(b, key, duties string) (int, string) {
		rec := call(h, reqSpec{method: http.MethodPost, path: "/v1/quality/receipt-checks", body: b, duties: duties, key: key, user: "rcv-1"})
		return rec.Code, rec.Body.String()
	}
	if code, _ := check(body(line1, "B", "600"), "k1", "QM_VIEW"); code != http.StatusForbidden {
		t.Fatalf("a check uses up cover, so it needs QM_ORDER_MAINTAIN: %d", code)
	}
	if code, b := check(body(line1, "B", "600"), "", allDuties); code != http.StatusBadRequest || !strings.Contains(b, "Idempotency-Key") {
		t.Fatalf("no key: %d %s", code, b)
	}
	code, b := check(body(line1, " B ", "600"), "k1", allDuties)
	if code != http.StatusCreated || !strings.Contains(b, `"skip":true`) || !strings.Contains(b, `"code":"QM-0128"`) || !strings.Contains(b, `"acceptedQty":"1000"`) {
		t.Fatalf("first receipt of B is covered by the 1,000 accepted (deviation included): %d %s", code, b)
	}
	if code, b := check(body(line1, "B", "600"), "k1", allDuties); code != http.StatusOK || len(f.s5.receiptChecks) != 1 {
		t.Fatalf("a replayed key answers the stored check: %d %s", code, b)
	}
	if code, _ := check(body(line1, "B", "600"), "k1b", allDuties); code != http.StatusOK || len(f.s5.receiptChecks) != 1 {
		t.Fatalf("the same receipt line with the same values answers the stored check: %d", code)
	}
	if code, b := check(body(line1, "B", "700"), "k1c", allDuties); code != http.StatusConflict || !strings.Contains(b, domain.CodeReceiptCheckDupe) {
		t.Fatalf("the same receipt line with another quantity: %d %s", code, b)
	}
	if code, b := check(body(line2, "B", "600"), "k1", allDuties); code != http.StatusUnprocessableEntity || !strings.Contains(b, codeKeyReuse) {
		t.Fatalf("a key reused for another receipt line: %d %s", code, b)
	}
	code, b = check(body(line2, "B", "600"), "k2", allDuties)
	if code != http.StatusCreated || !strings.Contains(b, `"skip":false`) || !strings.Contains(b, `"coveredQty":"400"`) || !strings.Contains(b, `"normalQty":"200"`) {
		t.Fatalf("the second receipt sees only the 400 left: %d %s", code, b)
	}
	code, b = check(body(line3, "C", "1000"), "k3", allDuties)
	if code != http.StatusCreated || !strings.Contains(b, `"skip":false`) || !strings.Contains(b, `"normalQty":"1000"`) || strings.Contains(b, "QM-0128") {
		t.Fatalf("AC-33.14 batch C gets a normal order: %d %s", code, b)
	}
	if code, b := check(`{"poLineId":"x","batch":"","receiptLineId":"y","receivedQty":"0"}`, "k4", allDuties); code != http.StatusBadRequest ||
		!strings.Contains(b, "poLineId") || !strings.Contains(b, "receivedQty") {
		t.Fatalf("validation: %d %s", code, b)
	}
	rec := call(h, reqSpec{method: http.MethodGet, path: "/v1/quality/receipt-checks?poLineId=" + po + "&batch=B", duties: "QM_VIEW"})
	if rec.Code != http.StatusOK || strings.Count(rec.Body.String(), `"receiptLineId"`) != 2 {
		t.Fatalf("list by PO line and batch: %d %s", rec.Code, rec.Body.String())
	}
	if rec := call(h, reqSpec{method: http.MethodGet, path: "/v1/quality/receipt-checks?poLineId=nope", duties: "QM_VIEW"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("a bad filter: %d", rec.Code)
	}
}

// seedClosed puts a closed order with a passing result and its decision in the fake.
func seedClosed(t *testing.T, f *fakeStore, h http.Handler, lines []domain.DecisionLine, split domain.Split) string {
	t.Helper()
	groupID, testID, outcomeID := createGroup(t, h, true)
	id := ids.New()
	decided := time.Date(2026, 4, 2, 9, 0, 0, 0, time.UTC)
	f.orders[id] = domain.Order{ID: id, ItemID: ids.New(), LotQty: decimal.NewFromInt(100), SampleQty: decimal.NewFromInt(1), UOM: "KG",
		Dims: map[string]string{"batch": "B-7"}, TestGroupID: groupID, Status: domain.StatusClosed, Version: 3}
	f.results[id] = []domain.Result{{TestID: testID, SampleNo: 1, OutcomeID: outcomeID, Passed: true, RecordedBy: "lab-1", RecordedAt: decided.Add(-time.Hour)}}
	f.decisions["dec-"+id] = domain.Decision{ID: ids.New(), OrderID: id, Lines: lines, Split: split, DecidedBy: "qa-release", DecidedAt: decided}
	return id
}

func TestCoA_RevisionChainsReplayAndRefusals(t *testing.T) {
	f := newFake()
	h := newHandler(f)
	accept := []domain.DecisionLine{{Decision: domain.DecisionAccept, Quantity: decimal.NewFromInt(100)}}
	id := seedClosed(t, f, h, accept, domain.Split{Accept: decimal.NewFromInt(100), Reject: decimal.Zero, Deviation: decimal.Zero})
	gen := func(body, key, duties string) (int, string) {
		rec := call(h, reqSpec{method: http.MethodPost, path: "/v1/quality/coa:generate", body: body, duties: duties, key: key, user: "clerk-1"})
		return rec.Code, rec.Body.String()
	}
	batch := `{"qualityOrderId":"` + id + `"}`
	if code, _ := gen(batch, "c1", allDuties); code != http.StatusForbidden {
		t.Fatalf("issuing a CoA needs QM_COA: %d", code)
	}
	code, b := gen(batch, "c1", dutyCoA)
	if code != http.StatusCreated || !strings.Contains(b, `"revision":1`) || !strings.Contains(b, `"signedBy":"qa-release"`) ||
		!strings.Contains(b, `"issuedBy":"clerk-1"`) || !strings.Contains(b, `"batch":"B-7"`) || !strings.Contains(b, `"testName":"appearance"`) {
		t.Fatalf("first CoA: %d %s", code, b)
	}
	if code, _ := gen(batch, "c1", dutyCoA); code != http.StatusOK || len(f.s5.coas) != 1 {
		t.Fatalf("a replayed key answers the stored revision: %d, %d revisions", code, len(f.s5.coas))
	}
	if code, b := gen(batch, "c2", dutyCoA); code != http.StatusUnprocessableEntity || !strings.Contains(b, domain.CodeCoAReasonRequired) {
		t.Fatalf("a second revision without a reason: %d %s", code, b)
	}
	code, b = gen(`{"qualityOrderId":"`+id+`","reason":"customer PO number corrected"}`, "c3", dutyCoA)
	if code != http.StatusCreated || !strings.Contains(b, `"revision":2`) || !strings.Contains(b, `"supersedes":"`+f.s5.coas[0].ID+`"`) {
		t.Fatalf("revision 2 supersedes revision 1: %d %s", code, b)
	}
	shipment, customer := ids.New(), ids.New()
	code, b = gen(`{"qualityOrderId":"`+id+`","shipmentId":"`+shipment+`","customerId":"`+customer+`"}`, "c4", dutyCoA)
	if code != http.StatusCreated || !strings.Contains(b, `"revision":1`) || !strings.Contains(b, `"shipmentId":"`+shipment+`"`) {
		t.Fatalf("a shipment has its own chain: %d %s", code, b)
	}
	other := seedPending(t, f, h, "10", true)
	if code, b := gen(`{"qualityOrderId":"`+other+`"}`, "c1", dutyCoA); code != http.StatusUnprocessableEntity || !strings.Contains(b, codeKeyReuse) {
		t.Fatalf("a key reused on another order: %d %s", code, b)
	}
	if code, b := gen(`{"qualityOrderId":"`+other+`"}`, "c5", dutyCoA); code != http.StatusUnprocessableEntity || !strings.Contains(b, domain.CodeCoANotReleasable) {
		t.Fatalf("an order still pending: %d %s", code, b)
	}
	reject := []domain.DecisionLine{{Decision: domain.DecisionReject, Quantity: decimal.NewFromInt(100)}}
	rejected := seedClosed(t, f, h, reject, domain.Split{Accept: decimal.Zero, Reject: decimal.NewFromInt(100), Deviation: decimal.Zero})
	if code, b := gen(`{"qualityOrderId":"`+rejected+`"}`, "c6", dutyCoA); code != http.StatusUnprocessableEntity || !strings.Contains(b, domain.CodeCoANotReleasable) {
		t.Fatalf("a rejected lot: %d %s", code, b)
	}
	if code, _ := gen(`{"qualityOrderId":"`+ids.New()+`"}`, "c7", dutyCoA); code != http.StatusNotFound {
		t.Fatalf("an unknown order: %d", code)
	}
	if code, b := gen(`{"qualityOrderId":"x","shipmentId":"y"}`, "c8", dutyCoA); code != http.StatusBadRequest || !strings.Contains(b, "shipmentId") {
		t.Fatalf("validation: %d %s", code, b)
	}
	rec := call(h, reqSpec{method: http.MethodGet, path: "/v1/quality/quality-orders/" + id + "/coas", duties: "QM_VIEW"})
	if rec.Code != http.StatusOK || strings.Count(rec.Body.String(), `"revision"`) != 3 {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	if rec := call(h, reqSpec{method: http.MethodGet, path: "/v1/quality/quality-orders/" + ids.New() + "/coas", duties: "QM_VIEW"}); rec.Code != http.StatusNotFound {
		t.Fatalf("CoAs of an unknown order: %d", rec.Code)
	}
}

func TestInstruments_DueWithinDays(t *testing.T) {
	f := newFake()
	now := time.Now().UTC()
	f.instruments["a"] = domain.Instrument{ID: "a", Code: "OVERDUE", DueAt: now.AddDate(0, 0, -1)}
	f.instruments["b"] = domain.Instrument{ID: "b", Code: "SOON", DueAt: now.AddDate(0, 0, 10)}
	f.instruments["c"] = domain.Instrument{ID: "c", Code: "LATER", DueAt: now.AddDate(0, 0, 40)}
	h := newHandler(f)
	rec := call(h, reqSpec{method: http.MethodGet, path: "/v1/quality/instruments?dueWithinDays=30", duties: "QM_VIEW"})
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "OVERDUE") || !strings.Contains(body, "SOON") || strings.Contains(body, "LATER") {
		t.Fatalf("due within 30 days, overdue included: %d %s", rec.Code, body)
	}
	if !strings.Contains(body, `"calibrationStatus":"OVERDUE"`) || !strings.Contains(body, `"calibrationStatus":"DUE_SOON"`) {
		t.Fatalf("the due list carries the calibration status: %s", body)
	}
	for _, bad := range []string{"-1", "x", "3651"} {
		if rec := call(h, reqSpec{method: http.MethodGet, path: "/v1/quality/instruments?dueWithinDays=" + bad, duties: "QM_VIEW"}); rec.Code != http.StatusBadRequest {
			t.Fatalf("dueWithinDays=%s: %d", bad, rec.Code)
		}
	}
	if rec := call(h, reqSpec{method: http.MethodGet, path: "/v1/quality/instruments", duties: "QM_VIEW"}); strings.Count(rec.Body.String(), `"code"`) != 3 {
		t.Fatalf("without the filter every instrument is listed: %s", rec.Body.String())
	}
}
