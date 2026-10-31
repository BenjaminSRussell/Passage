package api_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/api"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store/memstore"
)

const (
	receiptsPath = "/v1/receiving/receipts"
	grirPath     = "/v1/receiving/grir"
	invoicesPath = "/v1/receiving/invoices:post"
	locationID   = "77777777-7777-4777-8777-777777777777"
	ledgerID     = "88888888-8888-4888-8888-888888888888"
	periodID     = "99999999-9999-4999-8999-999999999999"
)

type rig struct {
	h      http.Handler
	api    *api.API
	st     *memstore.Store
	ledger *domain.FakeLedger
	keys   int

	confirmErsPrice string
}

func newRig(periods domain.PeriodStatus) *rig { return newRigOn(memstore.New(), periods) }

func newRigOn(st *memstore.Store, periods domain.PeriodStatus) *rig {
	rg := &rig{st: st, ledger: &domain.FakeLedger{}}
	a := api.New(rg.st, api.Ports{
		Periods: domain.FakePeriods{Status: periods}, Ledger: rg.ledger,
		Profile:       domain.PostingProfile{LedgerID: ledgerID, Inventory: "1400", GRIR: "2100", PPV: "5200", VendorControl: "2000"},
		ResolveMinAge: time.Nanosecond,
	})
	router := httpx.NewRouter()
	a.Register(router)
	rg.h, rg.api = router, a
	return rg
}

func (rg *rig) call(method, path, body string, hdr headers) *httptest.ResponseRecorder {
	return doWith(rg.h, method, path, body, hdr)
}

func (rg *rig) key() string {
	rg.keys++
	return "key-" + strconv.Itoa(rg.keys)
}

func duty(name, key string, extra ...string) headers {
	h := headers{"X-Passage-Tenant": testkit.TenantID(1), "X-Passage-Legal-Entity": legalEntityID, "X-Passage-Duties": name, "Idempotency-Key": key, "X-Passage-User": "test-user"}
	for i := 0; i+1 < len(extra); i += 2 {
		h[extra[i]] = extra[i+1]
	}
	return h
}

func (rg *rig) maintain(extra ...string) headers {
	return duty("ReceivingMaintain", rg.key(), extra...)
}
func (rg *rig) poster(version int) headers {
	return duty("ReceivingPost", rg.key(), "If-Match", `"`+strconv.Itoa(version)+`"`)
}

type receiptJSON struct {
	ID            string `json:"id"`
	State         string `json:"state"`
	Version       int    `json:"version"`
	LedgerEntryID string `json:"ledgerEntryId"`
	Lines         []struct {
		LineNo  int      `json:"lineNo"`
		Qty     string   `json:"qty"`
		Serials []string `json:"serials"`
		Amount  *string  `json:"amount"`
	} `json:"lines"`
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder, want int) T {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("got %d %s, want %d", rec.Code, rec.Body.String(), want)
	}
	var out T
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func directBody(slip string, ordered, qty int, lineExtra string) string {
	return fmt.Sprintf(`{"vendorId":%q,"purchaseOrderId":%q,"legalEntityId":%q,"packingSlip":%q,"receiptDate":"2026-10-25","currency":"USD","lines":[{"poLineId":%q,"productId":%q,"orderedRemaining":%d,"overDeliveryPct":5,"unitPrice":2.5,"unit":"EA","qty":%d,"locationId":%q%s}]}`,
		vendorID, poID, legalEntityID, slip, poLineID, productID, ordered, qty, locationID, lineExtra)
}

func (rg *rig) direct(t *testing.T, slip string, ordered, qty int) receiptJSON {
	t.Helper()
	return decode[receiptJSON](t, rg.call(http.MethodPost, receiptsPath, directBody(slip, ordered, qty, ""), rg.maintain()), http.StatusCreated)
}

func (rg *rig) posted(t *testing.T, slip string, ordered, qty int) receiptJSON {
	t.Helper()
	r := rg.direct(t, slip, ordered, qty)
	posted := decode[receiptJSON](t, rg.call(http.MethodPost, receiptsPath+"/"+r.ID+":post", "", rg.poster(r.Version)), http.StatusOK)
	if rg.confirmErsPrice != "" {
		for _, l := range posted.Lines {
			rg.confirmPrice(t, posted.ID, l.LineNo, rg.confirmErsPrice, http.StatusOK)
		}
	}
	return posted
}

func (rg *rig) confirmPrice(t *testing.T, receiptID string, lineNo int, price string, want int) {
	t.Helper()
	body := fmt.Sprintf(`{"legalEntityId":%q,"unitPrice":%s}`, legalEntityID, price)
	rec := rg.call(http.MethodPut, fmt.Sprintf("%s/%s/lines/%d/ers-price-confirmation", receiptsPath, receiptID, lineNo), body, userDuty("ErsPriceConfirm", rg.key(), "buyer"))
	if rec.Code != want {
		t.Fatalf("confirm price: %d %s, want %d", rec.Code, rec.Body.String(), want)
	}
}

func problemCode(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if rec.Code != status || !strings.Contains(rec.Body.String(), `"code":"`+code+`"`) {
		t.Fatalf("got %d %s, want %d %s", rec.Code, rec.Body.String(), status, code)
	}
}

func TestDirectReceipt_RegistersAndReturnsVersionedResource(t *testing.T) {
	rg := newRig(domain.PeriodOpen)
	rec := rg.call(http.MethodPost, receiptsPath, directBody("PS-1", 10, 4, `,"batch":"B1"`), rg.maintain())
	got := decode[receiptJSON](t, rec, http.StatusCreated)
	if got.State != "REGISTERED" || got.Version != 2 || got.Lines[0].Qty != "4" || rec.Header().Get("ETag") != `"2"` || rec.Header().Get("Location") != receiptsPath+"/"+got.ID {
		t.Fatalf("unexpected receipt %+v headers %v", got, rec.Header())
	}
}

func TestDirectReceipt_ValidationAndAuth(t *testing.T) {
	rg := newRig(domain.PeriodOpen)
	tests := []struct {
		name  string
		body  string
		field string
	}{
		{"missing qty", strings.Replace(directBody("P", 10, 4, ""), `"qty":4,`, "", 1), `"lines[0].qty"`},
		{"zero qty", directBody("P", 10, 0, ""), `"lines[0].qty"`},
		{"bad location", strings.Replace(directBody("P", 10, 4, ""), locationID, "nope", 1), `"lines[0].locationId"`},
		{"bad inventory status", directBody("P", 10, 4, `,"inventoryStatus":"LOST"`), `"lines[0].inventoryStatus"`},
		{"empty serial", directBody("P", 10, 4, `,"serials":[""]`), `"lines[0].serials[0]"`},
		{"long batch", directBody("P", 10, 4, `,"batch":"`+strings.Repeat("b", 129)+`"`), `"lines[0].batch"`},
		{"arrival field", strings.Replace(directBody("P", 10, 4, ""), `"packingSlip":"P"`, `"packingSlip":""`, 1), `"packingSlip"`},
		{"unknown field", directBody("P", 10, 4, `,"extra":1`), "COM-0001"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := rg.call(http.MethodPost, receiptsPath, tc.body, rg.maintain())
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tc.field) {
				t.Fatalf("got %d %s, want 400 with %s", rec.Code, rec.Body.String(), tc.field)
			}
		})
	}
	problemCode(t, rg.call(http.MethodPost, receiptsPath, directBody("P", 10, 4, ""), duty("APInquire", "k")), http.StatusForbidden, "AUTH-0003")
	problemCode(t, rg.call(http.MethodPost, receiptsPath, directBody("P", 10, 4, ""), headers{"Idempotency-Key": "k"}), http.StatusUnauthorized, "AUTH-0001")
	problemCode(t, rg.call(http.MethodPost, receiptsPath, directBody("P", 10, 4, ""), duty("ReceivingMaintain", "k", "X-Passage-Legal-Entity", ledgerID)), http.StatusForbidden, "AUTH-0003")
	problemCode(t, rg.call(http.MethodPost, receiptsPath, directBody("P", 10, 4, ""), headers{"X-Passage-Tenant": testkit.TenantID(1), "X-Passage-Duties": "ReceivingMaintain"}), http.StatusBadRequest, "COM-0001")
}

func TestDirectReceipt_CallerWithoutEntityScopeIsForbidden(t *testing.T) {
	rg := newRig(domain.PeriodOpen)
	problemCode(t, rg.call(http.MethodPost, receiptsPath, directBody("NS-1", 10, 4, ""), rg.maintain(entityHeader, "")), http.StatusForbidden, "AUTH-0003")
	problemCode(t, rg.call(http.MethodPost, receiptsPath, directBody("NS-1", 10, 4, ""), rg.maintain(entityHeader, ledgerID)), http.StatusForbidden, "AUTH-0003")
	if rec := rg.call(http.MethodPost, receiptsPath, directBody("NS-1", 10, 4, ""), rg.maintain()); rec.Code != http.StatusCreated {
		t.Fatalf("scoped caller: %d %s", rec.Code, rec.Body)
	}
}

func TestDirectReceipt_DomainConflicts(t *testing.T) {
	rg := newRig(domain.PeriodOpen)
	rg.direct(t, "PS-1", 10, 4)
	problemCode(t, rg.call(http.MethodPost, receiptsPath, directBody("PS-1", 10, 4, ""), rg.maintain()), http.StatusConflict, "RCV-DUP-001")
	problemCode(t, rg.call(http.MethodPost, receiptsPath, directBody("PS-2", 10, 11, ""), rg.maintain()), http.StatusUnprocessableEntity, "RCV-OVR-001")
	serial := directBody("PS-3", 10, 1, `,"serialTracked":true,"serials":["S1"]`)
	rg.direct(t, "PS-S", 10, 1)
	decode[receiptJSON](t, rg.call(http.MethodPost, receiptsPath, serial, rg.maintain()), http.StatusCreated)
	problemCode(t, rg.call(http.MethodPost, receiptsPath, strings.Replace(serial, `"PS-3"`, `"PS-4"`, 1), rg.maintain()), http.StatusUnprocessableEntity, "INV-SER-001")
}

func TestDirectReceipt_IdempotencyReplayAndMismatch(t *testing.T) {
	rg := newRig(domain.PeriodOpen)
	hdr := duty("ReceivingMaintain", "same-key")
	first := rg.call(http.MethodPost, receiptsPath, directBody("PS-1", 10, 4, ""), hdr)
	replay := rg.call(http.MethodPost, receiptsPath, directBody("PS-1", 10, 4, ""), hdr)
	if first.Code != http.StatusCreated || replay.Code != http.StatusCreated || replay.Header().Get("Idempotent-Replayed") != "true" || first.Body.String() != replay.Body.String() {
		t.Fatalf("first %d %s replay %d %s", first.Code, first.Body.String(), replay.Code, replay.Body.String())
	}
	problemCode(t, rg.call(http.MethodPost, receiptsPath, directBody("PS-2", 10, 4, ""), hdr), http.StatusUnprocessableEntity, "COM-0409")
	list := decode[struct{ Items []receiptJSON }](t, rg.call(http.MethodGet, receiptsPath, "", duty("ReceivingMaintain", "")), http.StatusOK)
	if len(list.Items) != 1 {
		t.Fatalf("replay created %d receipts, want 1", len(list.Items))
	}
}

func registerBody(qty int, extra string) string {
	return fmt.Sprintf(`{"lines":[{"lineNo":1,"qty":%d,"locationId":%q%s}]}`, qty, locationID, extra)
}

func (rg *rig) arrived(t *testing.T, slip string) receiptJSON {
	t.Helper()
	return decode[receiptJSON](t, rg.call(http.MethodPost, "/v1/receiving/arrivals", arrivalJSON(slip, ""), rg.maintain()), http.StatusCreated)
}

func TestRegister_HappyPathAndPreconditions(t *testing.T) {
	rg := newRig(domain.PeriodOpen)
	r := rg.arrived(t, "PS-1")
	path := receiptsPath + "/" + r.ID + ":register"
	problemCode(t, rg.call(http.MethodPost, path, registerBody(4, ""), rg.maintain()), http.StatusPreconditionRequired, "COM-0428")
	problemCode(t, rg.call(http.MethodPost, path, registerBody(4, ""), rg.maintain("If-Match", `"7"`)), http.StatusPreconditionFailed, "COM-0412")
	problemCode(t, rg.call(http.MethodPost, path, registerBody(4, ""), rg.maintain("If-Match", "abc")), http.StatusBadRequest, "COM-0001")
	got := decode[receiptJSON](t, rg.call(http.MethodPost, path, registerBody(4, `,"serials":[]`), rg.maintain("If-Match", `"1"`)), http.StatusOK)
	if got.State != "REGISTERED" || got.Version != 2 || got.Lines[0].Qty != "4" {
		t.Fatalf("unexpected %+v", got)
	}
	problemCode(t, rg.call(http.MethodPost, path, registerBody(4, ""), rg.maintain("If-Match", `"2"`)), http.StatusConflict, "RCV-STA-001")
}

func TestRegister_ValidationAndLookup(t *testing.T) {
	rg := newRig(domain.PeriodOpen)
	r := rg.arrived(t, "PS-1")
	path := receiptsPath + "/" + r.ID + ":register"
	for name, body := range map[string]string{
		"no lines":       `{"lines":[]}`,
		"zero lineNo":    `{"lines":[{"lineNo":0,"qty":1,"locationId":"` + locationID + `"}]}`,
		"duplicate line": `{"lines":[{"lineNo":1,"qty":1,"locationId":"` + locationID + `"},{"lineNo":1,"qty":1,"locationId":"` + locationID + `"}]}`,
		"missing qty":    `{"lines":[{"lineNo":1,"locationId":"` + locationID + `"}]}`,
	} {
		rec := rg.call(http.MethodPost, path, body, rg.maintain("If-Match", `"1"`))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: got %d %s", name, rec.Code, rec.Body.String())
		}
	}
	problemCode(t, rg.call(http.MethodPost, path, registerBody(11, ""), rg.maintain("If-Match", `"1"`)), http.StatusUnprocessableEntity, "RCV-OVR-001")
	problemCode(t, rg.call(http.MethodPost, path, `{"lines":[{"lineNo":2,"qty":1,"locationId":"`+locationID+`"}]}`, rg.maintain("If-Match", `"1"`)), http.StatusUnprocessableEntity, "RCV-VAL-001")
	problemCode(t, rg.call(http.MethodPost, receiptsPath+"/not-a-uuid:register", registerBody(1, ""), rg.maintain("If-Match", `"1"`)), http.StatusBadRequest, "COM-0001")
	problemCode(t, rg.call(http.MethodPost, receiptsPath+"/"+productID+":register", registerBody(1, ""), rg.maintain("If-Match", `"1"`)), http.StatusNotFound, "RCV-NF-001")
}

func TestPost_LedgerEventAndGrirInOneStep(t *testing.T) {
	rg := newRig(domain.PeriodOpen)
	got := rg.posted(t, "PS-1", 10, 4)
	if got.State != "POSTED" || got.Version != 3 || got.LedgerEntryID != "fake-entry-1" || got.Lines[0].Amount == nil || *got.Lines[0].Amount != "10" {
		t.Fatalf("unexpected %+v", got)
	}
	if len(rg.ledger.Entries) != 1 || rg.ledger.Entries[0].IdempotencyKey != "post:receiving:productreceipt:"+got.ID+":receipt" {
		t.Fatalf("ledger entries %+v", rg.ledger.Entries)
	}
	if evts := rg.st.Events(); len(evts) != 1 || evts[0].Type != "dev.passage.receiving.receipt.posted.v1" || !strings.Contains(string(evts[0].Data), `"ledger_entry_id":"fake-entry-1"`) {
		t.Fatalf("events %+v", evts)
	}
	page := decode[struct {
		Items []struct{ OpenAmount, Status, UnitPrice string }
	}](t, rg.call(http.MethodGet, grirPath, "", duty("APInquire", "")), http.StatusOK)
	if len(page.Items) != 1 || page.Items[0].OpenAmount != "10.00" || page.Items[0].Status != "OPEN" || page.Items[0].UnitPrice != "2.5" {
		t.Fatalf("grir %+v", page)
	}
}

func TestPost_Failures(t *testing.T) {
	rg := newRig(domain.PeriodOpen)
	r := rg.direct(t, "PS-1", 10, 4)
	path := receiptsPath + "/" + r.ID + ":post"
	problemCode(t, rg.call(http.MethodPost, path, "", duty("ReceivingMaintain", "k")), http.StatusForbidden, "AUTH-0003")
	problemCode(t, rg.call(http.MethodPost, path, "", duty("ReceivingPost", "k2")), http.StatusPreconditionRequired, "COM-0428")
	problemCode(t, rg.call(http.MethodPost, path, "", rg.poster(1)), http.StatusPreconditionFailed, "COM-0412")
	rg.ledger.Err = errors.New("ledger down 10.0.0.9")
	rec := rg.call(http.MethodPost, path, "", rg.poster(2))
	problemCode(t, rec, http.StatusInternalServerError, "COM-0500")
	if strings.Contains(rec.Body.String(), "10.0.0.9") {
		t.Fatalf("500 leaks ledger error: %s", rec.Body.String())
	}
	rg.ledger.Err = domain.ErrPeriodClosed
	problemCode(t, rg.call(http.MethodPost, path, "", rg.poster(2)), http.StatusUnprocessableEntity, "PER-AP-001")
	still := decode[receiptJSON](t, rg.call(http.MethodGet, receiptsPath+"/"+r.ID, "", duty("ReceivingMaintain", "")), http.StatusOK)
	if still.State != "REGISTERED" || len(rg.st.Events()) != 0 {
		t.Fatalf("failed post changed state to %s with %d events", still.State, len(rg.st.Events()))
	}
	rg.ledger.Err = nil
	decode[receiptJSON](t, rg.call(http.MethodPost, path, "", rg.poster(2)), http.StatusOK)
	problemCode(t, rg.call(http.MethodPost, path, "", rg.poster(3)), http.StatusConflict, "RCV-STA-001")
}

func TestPost_ReplayDoesNotPostTwice(t *testing.T) {
	rg := newRig(domain.PeriodOpen)
	r := rg.direct(t, "PS-1", 10, 4)
	hdr := duty("ReceivingPost", "post-key", "If-Match", `"2"`)
	path := receiptsPath + "/" + r.ID + ":post"
	rg.call(http.MethodPost, path, "", hdr)
	replay := rg.call(http.MethodPost, path, "", hdr)
	if replay.Code != http.StatusOK || replay.Header().Get("Idempotent-Replayed") != "true" || len(rg.ledger.Entries) != 1 || len(rg.st.Events()) != 1 {
		t.Fatalf("replay %d, ledger entries %d, events %d", replay.Code, len(rg.ledger.Entries), len(rg.st.Events()))
	}
}

func invoiceBody(number, receiptID string, qty int, extra string) string {
	return fmt.Sprintf(`{"vendorId":%q,"legalEntityId":%q,"invoiceNumber":%q,"invoiceDate":"2026-10-26","postingDate":"2026-10-26","currency":"USD","ledgerId":%q,"fiscalPeriodId":%q,"lines":[{"receiptId":%q,"receiptLineNo":1,"qty":%d,"unitPrice":2.5%s}]}`,
		vendorID, legalEntityID, number, ledgerID, periodID, receiptID, qty, extra)
}

func (rg *rig) apPoster() headers { return duty("VendorInvoicePost", rg.key()) }

type invoiceJSON struct {
	ID            string `json:"id"`
	Total         string `json:"total"`
	LedgerEntryID string `json:"ledgerEntryId"`
	LedgerID      string `json:"ledgerId"`
}

func TestCorrect_CreditsNeverBelowInvoicedQuantity(t *testing.T) {
	rg := newRig(domain.PeriodOpen)
	r := rg.posted(t, "PS-1", 100, 100)
	decode[invoiceJSON](t, rg.call(http.MethodPost, invoicesPath, invoiceBody("INV-1", r.ID, 40, ""), rg.apPoster()), http.StatusCreated)
	path := receiptsPath + "/" + r.ID + ":correct"
	body := func(delta int) string { return fmt.Sprintf(`{"lines":[{"lineNo":1,"qtyDelta":%d}]}`, delta) }

	problemCode(t, rg.call(http.MethodPost, path, body(-70), rg.poster(r.Version)), http.StatusConflict, "RCV-COR-001")
	got := decode[receiptJSON](t, rg.call(http.MethodPost, path, body(-60), rg.poster(r.Version)), http.StatusOK)
	if got.State != "POSTED" || got.Version != r.Version+1 || got.Lines[0].Qty != "40" || *got.Lines[0].Amount != "100" {
		t.Fatalf("unexpected %+v amount %s", got, *got.Lines[0].Amount)
	}
	last := rg.ledger.Entries[len(rg.ledger.Entries)-1]
	if last.IdempotencyKey != "post:receiving:productreceipt:"+r.ID+":correction:1" {
		t.Fatalf("correction key %q", last.IdempotencyKey)
	}
	if len(last.Lines) != 2 {
		t.Fatalf("correction posted %d lines, want 2", len(last.Lines))
	}
	if grir := last.Lines[1].Amount.Value.String(); grir != "150" {
		t.Fatalf("correction moved %s on GR/IR, want 150", grir)
	}
	if balance := rg.ledger.Balance("2100").String(); balance != "0" {
		t.Fatalf("GR/IR balance %s after receipt, invoice and correction, want 0 (everything received is invoiced)", balance)
	}
	evts := rg.st.Events()
	if evts[len(evts)-1].Type != "dev.passage.receiving.receipt.corrected.v1" || !strings.Contains(string(evts[len(evts)-1].Data), `"qty_delta":"-60"`) {
		t.Fatalf("events %+v", evts)
	}
	page := decode[struct {
		Items []struct{ ReceivedQty, InvoicedQty, Status string }
	}](t, rg.call(http.MethodGet, grirPath, "", duty("APInquire", "")), http.StatusOK)
	if page.Items[0].ReceivedQty != "40" || page.Items[0].InvoicedQty != "40" || page.Items[0].Status != "CLEARED" {
		t.Fatalf("grir after correction %+v", page.Items)
	}
}

func TestCorrect_ValidationAndRules(t *testing.T) {
	rg := newRig(domain.PeriodOpen)
	r := rg.posted(t, "PS-1", 10, 4)
	path := receiptsPath + "/" + r.ID + ":correct"
	for name, body := range map[string]string{
		"no lines":      `{"lines":[]}`,
		"zero delta":    `{"lines":[{"lineNo":1,"qtyDelta":0}]}`,
		"missing delta": `{"lines":[{"lineNo":1}]}`,
		"duplicate":     `{"lines":[{"lineNo":1,"qtyDelta":1},{"lineNo":1,"qtyDelta":1}]}`,
		"bad line no":   `{"lines":[{"lineNo":0,"qtyDelta":1}]}`,
	} {
		if rec := rg.call(http.MethodPost, path, body, rg.poster(r.Version)); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: got %d %s", name, rec.Code, rec.Body.String())
		}
	}
	problemCode(t, rg.call(http.MethodPost, path, `{"lines":[{"lineNo":1,"qtyDelta":-4}]}`, rg.poster(r.Version)), http.StatusUnprocessableEntity, "RCV-VAL-001")
	problemCode(t, rg.call(http.MethodPost, path, `{"lines":[{"lineNo":9,"qtyDelta":1}]}`, rg.poster(r.Version)), http.StatusUnprocessableEntity, "RCV-VAL-001")
	problemCode(t, rg.call(http.MethodPost, path, `{"lines":[{"lineNo":1,"qtyDelta":7}]}`, rg.poster(r.Version)), http.StatusUnprocessableEntity, "RCV-OVR-001")
	problemCode(t, rg.call(http.MethodPost, path, `{"lines":[{"lineNo":1,"qtyDelta":1}]}`, rg.poster(1)), http.StatusPreconditionFailed, "COM-0412")
	up := decode[receiptJSON](t, rg.call(http.MethodPost, path, `{"lines":[{"lineNo":1,"qtyDelta":2}]}`, rg.poster(r.Version)), http.StatusOK)
	again := decode[receiptJSON](t, rg.call(http.MethodPost, path, `{"lines":[{"lineNo":1,"qtyDelta":1}]}`, rg.poster(up.Version)), http.StatusOK)
	if again.Lines[0].Qty != "7" || !strings.HasSuffix(rg.ledger.Entries[len(rg.ledger.Entries)-1].IdempotencyKey, ":correction:2") {
		t.Fatalf("second correction %+v", again)
	}
	arrived := rg.arrived(t, "PS-2")
	problemCode(t, rg.call(http.MethodPost, receiptsPath+"/"+arrived.ID+":correct", `{"lines":[{"lineNo":1,"qtyDelta":1}]}`, rg.poster(1)), http.StatusConflict, "RCV-STA-001")
}

func TestCancel_ByState(t *testing.T) {
	rg := newRig(domain.PeriodOpen)
	arrived := rg.arrived(t, "PS-A")
	got := decode[receiptJSON](t, rg.call(http.MethodPost, receiptsPath+"/"+arrived.ID+":cancel", "", rg.poster(1)), http.StatusOK)
	if got.State != "CANCELLED" || got.Version != 2 || len(rg.ledger.Entries) != 0 || len(rg.st.Events()) != 0 {
		t.Fatalf("cancelling an arrival touched the ledger or events: %+v", got)
	}
	problemCode(t, rg.call(http.MethodPost, receiptsPath+"/"+arrived.ID+":cancel", "", rg.poster(2)), http.StatusConflict, "RCV-STA-001")

	serial := directBody("PS-S", 10, 1, `,"serialTracked":true,"serials":["S1"]`)
	reg := decode[receiptJSON](t, rg.call(http.MethodPost, receiptsPath, serial, rg.maintain()), http.StatusCreated)
	decode[receiptJSON](t, rg.call(http.MethodPost, receiptsPath+"/"+reg.ID+":cancel", "", rg.poster(reg.Version)), http.StatusOK)
	decode[receiptJSON](t, rg.call(http.MethodPost, receiptsPath, strings.Replace(serial, `"PS-S"`, `"PS-T"`, 1), rg.maintain()), http.StatusCreated)

	posted := rg.posted(t, "PS-P", 10, 4)
	got = decode[receiptJSON](t, rg.call(http.MethodPost, receiptsPath+"/"+posted.ID+":cancel", "", rg.poster(posted.Version)), http.StatusOK)
	reversal := rg.ledger.Entries[len(rg.ledger.Entries)-1]
	if got.State != "CANCELLED" || !strings.HasSuffix(reversal.IdempotencyKey, ":cancel") || reversal.Lines[0].Amount.Value.String() != "-10" || reversal.Lines[1].Amount.Value.String() != "10" {
		t.Fatalf("reversal %+v receipt %+v", reversal, got)
	}
	cleared := decode[struct {
		Items []struct{ Status, OpenAmount string }
	}](t, rg.call(http.MethodGet, grirPath+"?status=CLEARED", "", duty("APInquire", "")), http.StatusOK)
	if len(cleared.Items) != 1 || cleared.Items[0].OpenAmount != "0.00" {
		t.Fatalf("cancelled grir %+v", cleared)
	}
	evts := rg.st.Events()
	if evts[len(evts)-1].Type != "dev.passage.receiving.receipt.corrected.v1" || !strings.Contains(string(evts[len(evts)-1].Data), `"kind":"CANCELLATION"`) {
		t.Fatalf("events %+v", evts)
	}
}

func TestCancel_RejectedOnceInvoiced(t *testing.T) {
	rg := newRig(domain.PeriodOpen)
	r := rg.posted(t, "PS-1", 10, 4)
	decode[invoiceJSON](t, rg.call(http.MethodPost, invoicesPath, invoiceBody("INV-1", r.ID, 1, ""), rg.apPoster()), http.StatusCreated)
	before := len(rg.ledger.Entries)
	problemCode(t, rg.call(http.MethodPost, receiptsPath+"/"+r.ID+":cancel", "", rg.poster(r.Version)), http.StatusConflict, "RCV-STA-001")
	if len(rg.ledger.Entries) != before {
		t.Fatalf("rejected cancel posted to the ledger")
	}
}

func TestInvoice_PostsClearsGrirAndEmits(t *testing.T) {
	rg := newRig(domain.PeriodOpen)
	r := rg.posted(t, "PS-1", 10, 4)
	rec := rg.call(http.MethodPost, invoicesPath, invoiceBody("INV-1", r.ID, 4, ""), rg.apPoster())
	got := decode[invoiceJSON](t, rec, http.StatusCreated)
	if got.Total != "10.00" || got.LedgerEntryID == "" || got.ID == "" {
		t.Fatalf("unexpected invoice %+v", got)
	}
	entry := rg.ledger.Entries[len(rg.ledger.Entries)-1]
	if entry.IdempotencyKey != "post:receiving:vendorinvoice:"+got.ID+":invoice" || rg.ledger.Balance("2100").String() != "0" || rg.ledger.Balance("2000").String() != "-10" {
		t.Fatalf("invoice entry %+v grir %s ap %s", entry, rg.ledger.Balance("2100"), rg.ledger.Balance("2000"))
	}
	evts := rg.st.Events()
	if evts[len(evts)-1].Type != "dev.passage.payables.invoice.posted.v1" || !strings.Contains(string(evts[len(evts)-1].Data), `"invoice_number":"INV-1"`) {
		t.Fatalf("events %+v", evts)
	}
	page := decode[struct {
		Items []struct{ Status, OpenAmount string }
	}](t, rg.call(http.MethodGet, grirPath, "", duty("APInquire", "")), http.StatusOK)
	if page.Items[0].Status != "CLEARED" || page.Items[0].OpenAmount != "0.00" {
		t.Fatalf("grir %+v", page.Items)
	}
}

func TestInvoice_Rejections(t *testing.T) {
	rg := newRig(domain.PeriodOpen)
	r := rg.posted(t, "PS-1", 10, 4)
	decode[invoiceJSON](t, rg.call(http.MethodPost, invoicesPath, invoiceBody("INV-1", r.ID, 2, ""), rg.apPoster()), http.StatusCreated)
	entries := len(rg.ledger.Entries)

	problemCode(t, rg.call(http.MethodPost, invoicesPath, invoiceBody(" inv-1 ", r.ID, 1, ""), rg.apPoster()), http.StatusConflict, "AP-DUP-001")
	problemCode(t, rg.call(http.MethodPost, invoicesPath, invoiceBody("INV-2", r.ID, 3, ""), rg.apPoster()), http.StatusUnprocessableEntity, "AP-QTY-001")
	problemCode(t, rg.call(http.MethodPost, invoicesPath, invoiceBody("INV-3", productID, 1, ""), rg.apPoster()), http.StatusNotFound, "RCV-NF-002")
	other := strings.Replace(invoiceBody("INV-4", r.ID, 1, ""), vendorID, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", 1)
	problemCode(t, rg.call(http.MethodPost, invoicesPath, other, rg.apPoster()), http.StatusUnprocessableEntity, "RCV-VAL-001")
	problemCode(t, rg.call(http.MethodPost, invoicesPath, invoiceBody("INV-5", r.ID, 1, ""), duty("ReceivingPost", "k")), http.StatusForbidden, "AUTH-0003")
	problemCode(t, rg.call(http.MethodPost, invoicesPath, invoiceBody("INV-5", r.ID, 1, ""), duty("VendorInvoicePost", "k", "X-Passage-Legal-Entity", ledgerID)), http.StatusForbidden, "AUTH-0003")
	if len(rg.ledger.Entries) != entries {
		t.Fatalf("rejected invoices reached the ledger: %d entries, want %d", len(rg.ledger.Entries), entries)
	}
}

func TestInvoice_LedgerIDMustMatchTheConfiguredLedgerAndMayBeOmitted(t *testing.T) {
	rg := newRig(domain.PeriodOpen)
	r := rg.posted(t, "PS-1", 10, 4)
	entries := len(rg.ledger.Entries)

	foreign := strings.Replace(invoiceBody("INV-1", r.ID, 1, ""), ledgerID, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", 1)
	problemCode(t, rg.call(http.MethodPost, invoicesPath, foreign, rg.apPoster()), http.StatusUnprocessableEntity, "RCV-VAL-001")
	if len(rg.ledger.Entries) != entries {
		t.Fatalf("an invoice for a foreign ledger reached the ledger: %d entries, want %d", len(rg.ledger.Entries), entries)
	}

	problemCode(t, rg.call(http.MethodPost, invoicesPath, foreign, duty("VendorInvoicePost", "k", "X-Passage-Legal-Entity", ledgerID)), http.StatusForbidden, "AUTH-0003")

	omitted := strings.Replace(invoiceBody("INV-2", r.ID, 1, ""), `"ledgerId":"`+ledgerID+`",`, "", 1)
	got := decode[invoiceJSON](t, rg.call(http.MethodPost, invoicesPath, omitted, rg.apPoster()), http.StatusCreated)
	if got.LedgerID != ledgerID || len(rg.ledger.Entries) != entries+1 || rg.ledger.Entries[entries].LedgerID != ledgerID {
		t.Fatalf("invoice ledger %q, entries %d: want the configured ledger %s", got.LedgerID, len(rg.ledger.Entries), ledgerID)
	}
}

func TestInvoice_ValidationAndIdempotency(t *testing.T) {
	rg := newRig(domain.PeriodOpen)
	r := rg.posted(t, "PS-1", 10, 4)
	for name, body := range map[string]string{
		"no number":      strings.Replace(invoiceBody("INV", r.ID, 1, ""), `"invoiceNumber":"INV"`, `"invoiceNumber":" "`, 1),
		"bad date":       strings.Replace(invoiceBody("INV", r.ID, 1, ""), "2026-10-26", "26/10/2026", 1),
		"bad currency":   strings.Replace(invoiceBody("INV", r.ID, 1, ""), `"USD"`, `"usd"`, 1),
		"bad ledger":     strings.Replace(invoiceBody("INV", r.ID, 1, ""), ledgerID, "x", 1),
		"zero qty":       invoiceBody("INV", r.ID, 0, ""),
		"negative price": strings.Replace(invoiceBody("INV", r.ID, 1, ""), `"unitPrice":2.5`, `"unitPrice":-1`, 1),
		"bad receipt":    invoiceBody("INV", "x", 1, ""),
		"no lines":       strings.Replace(invoiceBody("INV", r.ID, 1, ""), `"lines":[`, `"lines":[],"x":[`, 1),
		"unknown field":  invoiceBody("INV", r.ID, 1, `,"extra":1`),
	} {
		if rec := rg.call(http.MethodPost, invoicesPath, body, rg.apPoster()); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: got %d %s", name, rec.Code, rec.Body.String())
		}
	}
	hdr := duty("VendorInvoicePost", "inv-key")
	first := rg.call(http.MethodPost, invoicesPath, invoiceBody("INV-1", r.ID, 1, ""), hdr)
	replay := rg.call(http.MethodPost, invoicesPath, invoiceBody("INV-1", r.ID, 1, ""), hdr)
	if first.Code != http.StatusCreated || replay.Header().Get("Idempotent-Replayed") != "true" || first.Body.String() != replay.Body.String() {
		t.Fatalf("first %d replay %d %s", first.Code, replay.Code, replay.Body.String())
	}
	problemCode(t, rg.call(http.MethodPost, invoicesPath, invoiceBody("INV-9", r.ID, 1, ""), hdr), http.StatusUnprocessableEntity, "COM-0409")
	problemCode(t, rg.call(http.MethodPost, invoicesPath, invoiceBody("INV-1", r.ID, 1, ""), duty("VendorInvoicePost", "")), http.StatusBadRequest, "COM-0001")
}

func TestInvoice_ClosedPeriodPostsNothing(t *testing.T) {
	open := newRig(domain.PeriodOpen)
	posted := open.posted(t, "PS-1", 10, 4)
	closed := newRigOn(open.st, domain.PeriodClosed)
	closed.keys = 100

	problemCode(t, closed.call(http.MethodPost, invoicesPath, invoiceBody("INV-1", posted.ID, 1, ""), closed.apPoster()), http.StatusUnprocessableEntity, "PER-AP-001")
	if len(closed.ledger.Entries) != 0 {
		t.Fatalf("closed period reached the ledger")
	}
	page := decode[struct {
		Items []struct{ InvoicedQty string }
	}](t, closed.call(http.MethodGet, grirPath, "", duty("APInquire", "")), http.StatusOK)
	if page.Items[0].InvoicedQty != "0" {
		t.Fatalf("closed-period invoice cleared quantity: %+v", page.Items)
	}
	if invoiced := decode[invoiceJSON](t, open.call(http.MethodPost, invoicesPath, invoiceBody("INV-1", posted.ID, 1, ""), open.apPoster()), http.StatusCreated); invoiced.ID == "" {
		t.Fatalf("number stays usable after a rejected posting")
	}
}

func TestGetReceipt_AndList(t *testing.T) {
	rg := newRig(domain.PeriodOpen)
	a := rg.direct(t, "PS-1", 10, 1)
	b := rg.direct(t, "PS-2", 10, 2)
	c := rg.direct(t, "PS-3", 10, 3)
	viewer := duty("ReceivingMaintain", "")

	rec := rg.call(http.MethodGet, receiptsPath+"/"+b.ID, "", viewer)
	if got := decode[receiptJSON](t, rec, http.StatusOK); got.ID != b.ID || rec.Header().Get("ETag") != `"2"` {
		t.Fatalf("get %+v etag %s", got, rec.Header().Get("ETag"))
	}
	problemCode(t, rg.call(http.MethodGet, receiptsPath+"/"+productID, "", viewer), http.StatusNotFound, "RCV-NF-001")
	problemCode(t, rg.call(http.MethodGet, receiptsPath+"/zzz", "", viewer), http.StatusBadRequest, "COM-0001")
	problemCode(t, rg.call(http.MethodGet, receiptsPath+"/"+b.ID, "", duty("APInquire", "")), http.StatusForbidden, "AUTH-0003")

	type page struct {
		Items      []receiptJSON `json:"items"`
		NextCursor string        `json:"nextCursor"`
	}
	first := decode[page](t, rg.call(http.MethodGet, receiptsPath+"?limit=2", "", viewer), http.StatusOK)
	if len(first.Items) != 2 || first.Items[0].ID != c.ID || first.Items[1].ID != b.ID || first.NextCursor == "" {
		t.Fatalf("first page %+v", first)
	}
	second := decode[page](t, rg.call(http.MethodGet, receiptsPath+"?limit=2&cursor="+first.NextCursor, "", viewer), http.StatusOK)
	if len(second.Items) != 1 || second.Items[0].ID != a.ID || second.NextCursor != "" {
		t.Fatalf("second page %+v", second)
	}
	if none := decode[page](t, rg.call(http.MethodGet, receiptsPath+"?state=POSTED", "", viewer), http.StatusOK); len(none.Items) != 0 {
		t.Fatalf("state filter returned %+v", none.Items)
	}
	for _, q := range []string{"?state=BOGUS", "?limit=0", "?limit=201", "?cursor=junk", "?vendorId=x", "?from=2026-99-99"} {
		if rec := rg.call(http.MethodGet, receiptsPath+q, "", viewer); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: got %d %s", q, rec.Code, rec.Body.String())
		}
	}
}

func TestListGrir_FiltersTotalsAndPaging(t *testing.T) {
	rg := newRig(domain.PeriodOpen)
	rg.posted(t, "PS-1", 10, 4)
	rg.posted(t, "PS-2", 10, 2)
	viewer := duty("APInquire", "")
	type page struct {
		Items  []struct{ ReceiptNumber, OpenAmount string }
		Totals struct {
			Count      int
			OpenAmount []struct{ Amount, Currency string }
		}
		NextCursor string
	}
	first := decode[page](t, rg.call(http.MethodGet, grirPath+"?limit=1", "", viewer), http.StatusOK)
	if len(first.Items) != 1 || first.Items[0].ReceiptNumber != "RCV-00000002" || first.NextCursor == "" || first.Totals.Count != 2 || first.Totals.OpenAmount[0].Amount != "15.00" {
		t.Fatalf("first %+v", first)
	}
	second := decode[page](t, rg.call(http.MethodGet, grirPath+"?limit=1&cursor="+first.NextCursor, "", viewer), http.StatusOK)
	if len(second.Items) != 1 || second.Items[0].ReceiptNumber != "RCV-00000001" || second.NextCursor != "" {
		t.Fatalf("second %+v", second)
	}
	if got := decode[page](t, rg.call(http.MethodGet, grirPath+"?ageGt=1", "", viewer), http.StatusOK); len(got.Items) != 0 {
		t.Fatalf("ageGt=1 returned %+v", got.Items)
	}
	if got := decode[page](t, rg.call(http.MethodGet, grirPath+"?vendorId="+vendorID+"&purchaseOrderId="+poID+"&status=OPEN", "", viewer), http.StatusOK); len(got.Items) != 2 {
		t.Fatalf("filters returned %+v", got.Items)
	}
	for _, q := range []string{"?status=NOPE", "?ageGt=-1", "?ageGt=x", "?limit=x", "?purchaseOrderId=1"} {
		if rec := rg.call(http.MethodGet, grirPath+q, "", viewer); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: got %d %s", q, rec.Code, rec.Body.String())
		}
	}
	problemCode(t, rg.call(http.MethodGet, grirPath, "", duty("ReceivingMaintain", "")), http.StatusForbidden, "AUTH-0003")
	problemCode(t, rg.call(http.MethodGet, grirPath, "", headers{}), http.StatusUnauthorized, "AUTH-0001")
}

func TestMetrics_CountReceiptLifecycle(t *testing.T) {
	rg := newRig(domain.PeriodOpen)
	r := rg.posted(t, "PS-1", 10, 4)
	rg.call(http.MethodPost, invoicesPath, invoiceBody("INV-1", r.ID, 1, ""), rg.apPoster())
	rg.call(http.MethodPost, receiptsPath+"/"+r.ID+":correct", `{"lines":[{"lineNo":1,"qtyDelta":1}]}`, rg.poster(r.Version))
	rec := httptest.NewRecorder()
	rg.api.Metrics().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, want := range []string{"receiving_receipts_registered_total 1", "receiving_receipts_posted_total 1", "receiving_receipts_adjusted_total 1", "receiving_invoices_posted_total 1"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("metrics missing %q in %s", want, rec.Body.String())
		}
	}
}

func TestAsyncAPIDocumentsEveryEmittedEventAndField(t *testing.T) {
	spec, err := os.ReadFile("../../events/asyncapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	rg := newRig(domain.PeriodOpen)
	r := rg.posted(t, "PS-1", 10, 4)
	rg.call(http.MethodPost, invoicesPath, invoiceBody("INV-1", r.ID, 1, ""), rg.apPoster())
	rg.call(http.MethodPost, receiptsPath+"/"+r.ID+":correct", `{"lines":[{"lineNo":1,"qtyDelta":1}]}`, rg.poster(r.Version))
	rg.call(http.MethodPost, receiptsPath+"/"+r.ID+":cancel", "", rg.poster(r.Version+1))
	rg.call(http.MethodPost, invoicesPath, invoiceBody("INV-2", r.ID, 1, ""), rg.apPoster())
	seen := map[string]bool{}
	for _, evt := range rg.st.Events() {
		seen[evt.Type] = true
		if !strings.Contains(string(spec), "name: "+evt.Type) {
			t.Errorf("asyncapi.yaml does not document %s", evt.Type)
		}
		var data map[string]any
		if err := json.Unmarshal(evt.Data, &data); err != nil {
			t.Fatal(err)
		}
		for key := range data {
			if !strings.Contains(string(spec), "        "+key+":") {
				t.Errorf("asyncapi.yaml does not document %s.%s", evt.Type, key)
			}
		}
	}
	if len(seen) != 3 {
		t.Fatalf("lifecycle emitted %d distinct event types, want 3: %v", len(seen), seen)
	}
}

func TestDirectReceipt_AcceptsDecimalStrings(t *testing.T) {
	rg := newRig(domain.PeriodOpen)
	body := `{"vendorId":"` + vendorID + `","purchaseOrderId":"` + poID + `","legalEntityId":"` + legalEntityID + `","packingSlip":"PS-D","receiptDate":"2026-10-25","currency":"USD","lines":[{"poLineId":"` + poLineID + `","productId":"` + productID + `","orderedRemaining":"10.5","unitPrice":"2.50","unit":"EA","qty":"4.25","locationId":"` + locationID + `"}]}`
	got := decode[receiptJSON](t, rg.call(http.MethodPost, receiptsPath, body, rg.maintain()), http.StatusCreated)
	if got.Lines[0].Qty != "4.25" {
		t.Fatalf("qty %s, want 4.25", got.Lines[0].Qty)
	}
	bad := strings.Replace(body, `"qty":"4.25"`, `"qty":"4,25"`, 1)
	if rec := rg.call(http.MethodPost, receiptsPath, strings.Replace(bad, "PS-D", "PS-E", 1), rg.maintain()); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"lines[0].qty"`) {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}
