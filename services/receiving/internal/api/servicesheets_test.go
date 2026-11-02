package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/api"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store/memstore"
)

const sheetsPath = "/v1/receiving/service-entry-sheets"

type sheetJSON struct {
	ID           string `json:"id"`
	Status       string `json:"status"`
	Total        string `json:"total"`
	VendorID     string `json:"vendorId"`
	DecidedBy    string `json:"decidedBy"`
	RejectReason string `json:"rejectReason"`
	Lines        []struct {
		POLineNo int    `json:"poLineNo"`
		Amount   string `json:"amount"`
	} `json:"lines"`
}

func newSheetRig(po domain.PurchaseOrder) *rig {
	rg := &rig{st: memstore.New(), ledger: &domain.FakeLedger{}}
	a := api.New(rg.st, api.Ports{
		Periods: domain.FakePeriods{Status: domain.PeriodOpen}, Ledger: rg.ledger,
		PurchaseOrders: domain.FakePurchaseOrders{Orders: map[string]domain.PurchaseOrder{po.ID: po}},
	})
	router := httpx.NewRouter()
	a.Register(router)
	rg.h, rg.api = router, a
	return rg
}

func servicePO() domain.PurchaseOrder {
	dec := decimal.RequireFromString
	return domain.PurchaseOrder{
		ID: poID, VendorID: vendorID, Currency: "USD", LegalEntityID: legalEntityID, ApprovalStatus: "APPROVED",
		Lines: []domain.PurchaseOrderLine{
			{LineNo: 1, Unit: "H", Qty: dec("10"), UnitPrice: dec("2.5"), PriceUnit: dec("1"), Status: "OPEN"},
			{LineNo: 2, Unit: "H", Qty: dec("1"), UnitPrice: dec("9"), PriceUnit: dec("1"), Status: "CANCELLED"},
			{LineNo: 3, Unit: "EA", Qty: dec("1"), UnitPrice: dec("1000"), PriceUnit: dec("1"), Status: "OPEN"},
		},
	}
}

func sheetBodyFor(lines ...map[string]any) string {
	raw, _ := json.Marshal(map[string]any{"legalEntityId": legalEntityID, "purchaseOrderId": poID, "lines": lines})
	return string(raw)
}

func sheetLine(no int, qty, price string) map[string]any {
	return map[string]any{"poLineNo": no, "description": "Consulting", "unit": "H", "qty": qty, "unitPrice": price}
}

func (rg *rig) sheetAs(user, body string) *httpResult {
	rec := rg.call(http.MethodPost, sheetsPath, body, duty("ServiceEntryMaintain", rg.key(), "X-Passage-User", user))
	return &httpResult{rec.Code, rec.Body.String()}
}

func (rg *rig) decideSheet(id, action, user, body string) *httpResult {
	rec := rg.call(http.MethodPost, sheetsPath+"/"+id+":"+action, body, duty("ServiceEntryAccept", rg.key(), "X-Passage-User", user))
	return &httpResult{rec.Code, rec.Body.String()}
}

func parseSheet(t *testing.T, r *httpResult, want int) sheetJSON {
	t.Helper()
	if r.code != want {
		t.Fatalf("got %d %s, want %d", r.code, r.body, want)
	}
	var out sheetJSON
	if err := json.Unmarshal([]byte(r.body), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestServiceSheet_CreateAcceptEmitsOneDecisionEvent(t *testing.T) {
	rg := newSheetRig(servicePO())
	created := parseSheet(t, rg.sheetAs("alice", sheetBodyFor(sheetLine(1, "3", "2.505"))), http.StatusCreated)
	if created.Status != "PENDING" || created.VendorID != vendorID || created.Total != "7.52" || created.Lines[0].Amount != "7.52" {
		t.Fatalf("created %+v", created)
	}
	if r := rg.decideSheet(created.ID, "accept", "alice", ""); r.code != http.StatusForbidden || !strings.Contains(r.body, "AP-SES-002") {
		t.Fatalf("the author accepted their own sheet: %d %s", r.code, r.body)
	}
	if len(rg.st.Events()) != 0 {
		t.Fatal("a refused decision emitted an event")
	}
	accepted := parseSheet(t, rg.decideSheet(created.ID, "accept", "bob", ""), http.StatusOK)
	if accepted.Status != "ACCEPTED" || accepted.DecidedBy != "bob" {
		t.Fatalf("accepted %+v", accepted)
	}
	evts := rg.st.Events()
	if len(evts) != 1 || evts[0].Type != "dev.passage.receiving.serviceentrysheet.decided.v1" {
		t.Fatalf("events %+v", evts)
	}
	if r := rg.decideSheet(created.ID, "reject", "carol", `{"reason":"too late"}`); r.code != http.StatusConflict || !strings.Contains(r.body, "AP-SES-003") {
		t.Fatalf("a decided sheet was decided again: %d %s", r.code, r.body)
	}
	read := rg.call(http.MethodGet, sheetsPath+"/"+created.ID, "", duty("ServiceEntryMaintain", "", "X-Passage-User", "alice"))
	if got := decode[sheetJSON](t, read, http.StatusOK); got.Status != "ACCEPTED" {
		t.Fatalf("read %+v", got)
	}
}

func TestServiceSheet_LineValueCapCountsPendingAndAcceptedButNotRejected(t *testing.T) {
	rg := newSheetRig(servicePO())
	first := parseSheet(t, rg.sheetAs("alice", sheetBodyFor(sheetLine(1, "8", "2.5"))), http.StatusCreated)
	if r := rg.sheetAs("alice", sheetBodyFor(sheetLine(1, "2", "2.5"), sheetLine(1, "1", "2.5"))); r.code != http.StatusUnprocessableEntity || !strings.Contains(r.body, "AP-SES-001") {
		t.Fatalf("over the line value: %d %s", r.code, r.body)
	}
	exact := parseSheet(t, rg.sheetAs("alice", sheetBodyFor(sheetLine(1, "2", "2.5"))), http.StatusCreated)
	if exact.Total != "5" {
		t.Fatalf("exact fill %+v", exact)
	}
	parseSheet(t, rg.decideSheet(first.ID, "reject", "bob", `{"reason":"not performed"}`), http.StatusOK)
	parseSheet(t, rg.sheetAs("alice", sheetBodyFor(sheetLine(1, "8", "2.5"))), http.StatusCreated)
}

func TestServiceSheet_RefusesBadInputs(t *testing.T) {
	rg := newSheetRig(servicePO())
	cases := map[string]struct {
		body   string
		status int
		code   string
	}{
		"cancelled line": {sheetBodyFor(sheetLine(2, "1", "9")), http.StatusUnprocessableEntity, "RCV-PO-002"},
		"unknown line":   {sheetBodyFor(sheetLine(9, "1", "9")), http.StatusBadRequest, "poLineNo"},
		"zero quantity":  {sheetBodyFor(sheetLine(1, "0", "9")), http.StatusBadRequest, "qty"},
		"no lines":       {sheetBodyFor(), http.StatusBadRequest, "lines"},
		"unknown po":     {strings.Replace(sheetBodyFor(sheetLine(1, "1", "1")), poID, "44444444-4444-4444-8444-444444444444", 1), http.StatusNotFound, "RCV-NF-004"},
	}
	for name, tc := range cases {
		if r := rg.sheetAs("alice", tc.body); r.code != tc.status || !strings.Contains(r.body, tc.code) {
			t.Errorf("%s: %d %s", name, r.code, r.body)
		}
	}
	other := servicePO()
	other.LegalEntityID = "99999999-9999-4999-8999-999999999990"
	if r := newSheetRig(other).sheetAs("alice", sheetBodyFor(sheetLine(1, "1", "1"))); r.code != http.StatusUnprocessableEntity || !strings.Contains(r.body, "RCV-PO-003") {
		t.Errorf("other legal entity: %d %s", r.code, r.body)
	}
	draft := servicePO()
	draft.ApprovalStatus = "DRAFT"
	if r := newSheetRig(draft).sheetAs("alice", sheetBodyFor(sheetLine(1, "1", "1"))); r.code != http.StatusUnprocessableEntity || !strings.Contains(r.body, "RCV-PO-001") {
		t.Errorf("unapproved order: %d %s", r.code, r.body)
	}
	if rec := rg.call(http.MethodPost, sheetsPath, sheetBodyFor(sheetLine(1, "1", "1")), duty("VendorInvoicePost", rg.key())); rec.Code != http.StatusForbidden {
		t.Errorf("wrong duty: %d", rec.Code)
	}
	noUser := duty("ServiceEntryMaintain", rg.key())
	delete(noUser, "X-Passage-User")
	if rec := rg.call(http.MethodPost, sheetsPath, sheetBodyFor(sheetLine(1, "1", "1")), noUser); rec.Code != http.StatusUnauthorized {
		t.Errorf("no user: %d", rec.Code)
	}
}

func TestServiceSheet_RejectNeedsAReasonAndReadsAreScoped(t *testing.T) {
	rg := newSheetRig(servicePO())
	created := parseSheet(t, rg.sheetAs("alice", sheetBodyFor(sheetLine(3, "1", "1000"))), http.StatusCreated)
	if r := rg.decideSheet(created.ID, "reject", "bob", `{"reason":" "}`); r.code != http.StatusBadRequest {
		t.Fatalf("blank reason: %d %s", r.code, r.body)
	}
	rejected := parseSheet(t, rg.decideSheet(created.ID, "reject", "bob", `{"reason":"not performed"}`), http.StatusOK)
	if rejected.Status != "REJECTED" || rejected.RejectReason != "not performed" {
		t.Fatalf("rejected %+v", rejected)
	}
	foreign := duty("ServiceEntryMaintain", "", "X-Passage-User", "alice", "X-Passage-Legal-Entity", "55555555-5555-4555-8555-555555555555")
	if rec := rg.call(http.MethodGet, sheetsPath+"/"+created.ID, "", foreign); rec.Code != http.StatusForbidden {
		t.Errorf("foreign read: %d", rec.Code)
	}
	missing := fmt.Sprintf("%s/%s", sheetsPath, "66666666-6666-4666-8666-666666666666")
	if rec := rg.call(http.MethodGet, missing, "", duty("ServiceEntryMaintain", "", "X-Passage-User", "alice")); rec.Code != http.StatusNotFound {
		t.Errorf("unknown sheet: %d", rec.Code)
	}
}
