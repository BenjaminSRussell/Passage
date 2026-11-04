package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/api"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store/memstore"
)

const sheetsPath = "/v1/receiving/service-entry-sheets"

type sheetJSON struct {
	ID            string `json:"id"`
	Status        string `json:"status"`
	Total         string `json:"total"`
	VendorID      string `json:"vendorId"`
	DecidedBy     string `json:"decidedBy"`
	RejectReason  string `json:"rejectReason"`
	ReceiptID     string `json:"receiptId"`
	LedgerEntryID string `json:"ledgerEntryId"`
	Lines         []struct {
		POLineNo int    `json:"poLineNo"`
		Amount   string `json:"amount"`
	} `json:"lines"`
}

func newSheetRig(po domain.PurchaseOrder) *rig {
	rg := &rig{st: memstore.New(), ledger: &domain.FakeLedger{}, orders: map[string]domain.PurchaseOrder{po.ID: po}}
	a := api.New(rg.st, api.Ports{
		Periods: domain.FakePeriods{Status: domain.PeriodOpen}, Ledger: rg.ledger,
		Profile:        domain.PostingProfile{LedgerID: ledgerID, Inventory: "1400", GRIR: "2100", PPV: "5200", VendorControl: "2000", Expense: "6000", InputTax: "1570"},
		PurchaseOrders: domain.FakePurchaseOrders{Orders: rg.orders},
		AuditSampler:   testSampler("seed-one-0123456789"),
		ResolveMinAge:  time.Nanosecond,
		Tax:            testTax, SelfBillParties: testParties,
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
	if accepted.Status != "ACCEPTED" || accepted.DecidedBy != "bob" || accepted.ReceiptID == "" || accepted.LedgerEntryID == "" {
		t.Fatalf("accepted %+v", accepted)
	}
	if len(rg.ledger.Entries) != 1 {
		t.Fatalf("ledger entries %d, want the service accrual", len(rg.ledger.Entries))
	}
	wantLines(t, rg.ledger.Entries[0], "6000=7.52", "2100=-7.52")
	if rg.ledger.Balance("6000").String() != "7.52" || rg.ledger.Balance("2100").String() != "-7.52" {
		t.Fatalf("expense %s GR/IR %s", rg.ledger.Balance("6000"), rg.ledger.Balance("2100"))
	}
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: legalEntityID})
	page, err := rg.st.ListGrir(ctx, store.GrirFilter{Limit: 10})
	if err != nil || len(page.Items) != 1 || page.Items[0].ReceiptID != accepted.ReceiptID || page.Items[0].Status != "OPEN" {
		t.Fatalf("GR/IR %+v err %v", page.Items, err)
	}
	rec, err := rg.st.GetReceipt(ctx, accepted.ReceiptID)
	if err != nil || rec.Source != domain.ReceiptSourceService || rec.PackingSlip != "SES-"+created.ID || rec.State != domain.Posted {
		t.Fatalf("service receipt %+v err %v", rec, err)
	}
	evts := rg.st.Events()
	if len(evts) != 2 {
		t.Fatalf("events %+v", evts)
	}
	types := []string{evts[0].Type, evts[1].Type}
	if types[0] != "dev.passage.receiving.serviceentrysheet.decided.v1" || types[1] != "dev.passage.receiving.receipt.posted.v1" {
		t.Fatalf("events %v", types)
	}
	assertDocumented(t, evts[0])
	assertDocumented(t, evts[1])
	byDecider := rg.call(http.MethodGet, sheetsPath+"/"+created.ID, "", duty("ServiceEntryAccept", "", "X-Passage-User", "bob"))
	if got := decode[sheetJSON](t, byDecider, http.StatusOK); got.ID != created.ID {
		t.Fatalf("decider read %+v", got)
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

func TestServiceSheet_AcceptClosedPeriodPostsNothing(t *testing.T) {
	po := servicePO()
	rg := &rig{st: memstore.New(), ledger: &domain.FakeLedger{}}
	a := api.New(rg.st, api.Ports{
		Periods: domain.FakePeriods{Status: domain.PeriodClosed}, Ledger: rg.ledger,
		Profile:        domain.PostingProfile{LedgerID: ledgerID, Inventory: "1400", GRIR: "2100", PPV: "5200", VendorControl: "2000", Expense: "6000", InputTax: "1570"},
		PurchaseOrders: domain.FakePurchaseOrders{Orders: map[string]domain.PurchaseOrder{po.ID: po}},
		ResolveMinAge:  time.Nanosecond,
	})
	router := httpx.NewRouter()
	a.Register(router)
	rg.h, rg.api = router, a
	created := parseSheet(t, rg.sheetAs("alice", sheetBodyFor(sheetLine(1, "1", "2.5"))), http.StatusCreated)
	if r := rg.decideSheet(created.ID, "accept", "bob", ""); r.code != http.StatusUnprocessableEntity || !strings.Contains(r.body, "PER-AP-001") {
		t.Fatalf("closed period: %d %s", r.code, r.body)
	}
	if len(rg.ledger.Entries) != 0 {
		t.Fatalf("ledger %+v", rg.ledger.Entries)
	}
	read := decode[sheetJSON](t, rg.call(http.MethodGet, sheetsPath+"/"+created.ID, "", duty("ServiceEntryMaintain", "", "X-Passage-User", "alice")), http.StatusOK)
	if read.Status != "PENDING" {
		t.Fatalf("sheet moved off PENDING: %+v", read)
	}
}

func TestServiceSheet_AcceptedSheetSettlesByERS(t *testing.T) {
	rg := newSheetRig(servicePO())
	created := parseSheet(t, rg.sheetAs("alice", sheetBodyFor(sheetLine(1, "3", "2.5"))), http.StatusCreated)
	accepted := parseSheet(t, rg.decideSheet(created.ID, "accept", "bob", ""), http.StatusOK)
	rg.enableErs(t, true)
	run := rg.ersRun(t, http.StatusOK)
	if len(run.Settled) != 1 || run.Settled[0].InvoiceNumber != "ERS-"+created.ID || run.Settled[0].ReceiptID != accepted.ReceiptID {
		t.Fatalf("settled %+v", run)
	}
	entry := lastEntry(t, rg)
	wantLines(t, entry, "2100=7.5", "2000=-7.5")
}

func TestServiceSheet_ERSPriceDifferenceGoesToExpenseNotInventory(t *testing.T) {
	rg := newSheetRig(servicePO())
	created := parseSheet(t, rg.sheetAs("alice", sheetBodyFor(sheetLine(1, "4", "2.5"))), http.StatusCreated)
	accepted := parseSheet(t, rg.decideSheet(created.ID, "accept", "bob", ""), http.StatusOK)
	repriced := servicePO()
	repriced.Lines[0].UnitPrice = decimal.RequireFromString("3")
	rg.orders[poID] = repriced
	rg.enableErs(t, true)
	rg.confirmPrice(t, accepted.ReceiptID, 1, "3", http.StatusOK)
	run := rg.ersRun(t, http.StatusOK)
	if len(run.Settled) != 1 {
		t.Fatalf("run %+v", run)
	}
	wantLines(t, lastEntry(t, rg), "2100=10", "6000=2", "2000=-12")
	if rg.ledger.Balance("1400").Sign() != 0 {
		t.Fatalf("a service price difference reached Inventory: %s", rg.ledger.Balance("1400"))
	}
}

func TestServiceSheet_ManualInvoiceDifferenceGoesToExpenseToo(t *testing.T) {
	rg := newSheetRig(servicePO())
	created := parseSheet(t, rg.sheetAs("alice", sheetBodyFor(sheetLine(1, "4", "2.5"))), http.StatusCreated)
	accepted := parseSheet(t, rg.decideSheet(created.ID, "accept", "bob", ""), http.StatusOK)
	rec := rg.call(http.MethodPost, invoicesPath, invoiceBody("INV-SES-1", accepted.ReceiptID, 4, ""), userDuty("VendorInvoicePost", rg.key(), "ap-clerk"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("manual invoice %d %s", rec.Code, rec.Body.String())
	}
	if rg.ledger.Balance("1400").Sign() != 0 {
		t.Fatalf("Inventory %s", rg.ledger.Balance("1400"))
	}
}

func TestServiceSheet_ReceiptCannotBeCorrectedOrCancelled(t *testing.T) {
	rg := newSheetRig(servicePO())
	created := parseSheet(t, rg.sheetAs("alice", sheetBodyFor(sheetLine(1, "4", "2.5"))), http.StatusCreated)
	accepted := parseSheet(t, rg.decideSheet(created.ID, "accept", "bob", ""), http.StatusOK)
	entries := len(rg.ledger.Entries)
	path := receiptsPath + "/" + accepted.ReceiptID
	problemCode(t, rg.call(http.MethodPost, path+":correct", `{"lines":[{"lineNo":1,"qtyDelta":1}]}`, rg.poster(1)), http.StatusConflict, "RCV-SVC-001")
	problemCode(t, rg.call(http.MethodPost, path+":cancel", "", rg.poster(1)), http.StatusConflict, "RCV-SVC-001")
	if len(rg.ledger.Entries) != entries {
		t.Fatalf("a refused command posted to the ledger: %+v", rg.ledger.Entries[entries:])
	}
	read := decode[sheetJSON](t, rg.call(http.MethodGet, sheetsPath+"/"+created.ID, "", duty("ServiceEntryMaintain", "", "X-Passage-User", "alice")), http.StatusOK)
	if read.Status != "ACCEPTED" {
		t.Fatalf("sheet %+v", read)
	}
}

func TestServiceSheet_AcceptFailsWhenThePurchaseOrderCannotBeRead(t *testing.T) {
	rg := newSheetRig(servicePO())
	created := parseSheet(t, rg.sheetAs("alice", sheetBodyFor(sheetLine(1, "4", "2.5"))), http.StatusCreated)
	delete(rg.orders, poID)
	if r := rg.decideSheet(created.ID, "accept", "bob", ""); r.code == http.StatusOK {
		t.Fatalf("accepted without reading the purchase order: %s", r.body)
	}
	if len(rg.ledger.Entries) != 0 {
		t.Fatalf("ledger %+v", rg.ledger.Entries)
	}
}
