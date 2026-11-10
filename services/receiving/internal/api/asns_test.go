package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
)

const asnsPath = "/v1/receiving/asns"

type asnJSON struct {
	ID             string `json:"id"`
	Status         string `json:"status"`
	VendorID       string `json:"vendorId"`
	TrackingNumber string `json:"trackingNumber"`
	ReceiptID      string `json:"receiptId"`
	Lines          []struct {
		POLineNo   int      `json:"poLineNo"`
		Qty        string   `json:"qty"`
		Batch      string   `json:"batch"`
		Serials    []string `json:"serials"`
		ExpiryDate string   `json:"expiryDate"`
	} `json:"lines"`
}

type asnReceiptJSON struct {
	Asn     asnJSON     `json:"asn"`
	Receipt receiptJSON `json:"receipt"`
}

func asnPO() domain.PurchaseOrder {
	dec := decimal.RequireFromString
	return domain.PurchaseOrder{
		ID: poID, VendorID: vendorID, Currency: "USD", LegalEntityID: legalEntityID, ApprovalStatus: "APPROVED",
		Lines: []domain.PurchaseOrderLine{
			{LineNo: 1, ProductID: productID, Unit: "EA", Qty: dec("100"), UnitPrice: dec("2.5"), PriceUnit: dec("1"), Status: "OPEN"},
			{LineNo: 2, ProductID: productID, Unit: "EA", Qty: dec("50"), UnitPrice: dec("4"), PriceUnit: dec("1"), Status: "OPEN"},
		},
	}
}

func asnBodyFor(tracking string, lines ...map[string]any) string {
	raw, _ := json.Marshal(map[string]any{
		"legalEntityId": legalEntityID, "purchaseOrderId": poID, "carrier": "DHL", "trackingNumber": tracking,
		"shipDate": "2026-11-01", "expectedArrival": "2026-11-05", "lines": lines,
	})
	return string(raw)
}

func asnLine(no int, qty, batch, expiry string) map[string]any {
	return map[string]any{"poLineNo": no, "qty": qty, "batch": batch, "expiryDate": expiry}
}

func (rg *rig) vendorAsn(body string) *httpResult {
	rec := rg.call(http.MethodPost, asnsPath, body, duty("receiving.asn.maintain", rg.key(), "X-Passage-User", "vendor-user"))
	return &httpResult{rec.Code, rec.Body.String()}
}

func (rg *rig) createAsn(t *testing.T, tracking string, lines ...map[string]any) asnJSON {
	t.Helper()
	r := rg.vendorAsn(asnBodyFor(tracking, lines...))
	if r.code != http.StatusCreated {
		t.Fatalf("create ASN got %d %s", r.code, r.body)
	}
	var out asnJSON
	if err := json.Unmarshal([]byte(r.body), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestAsn_QuantityBoundRefusesMoreThanTheOpenPurchaseOrderQuantity(t *testing.T) {
	rg := newSheetRig(asnPO())
	rg.createAsn(t, "TRK-1", asnLine(1, "60", "", ""))

	over := rg.vendorAsn(asnBodyFor("TRK-2", asnLine(1, "41", "", "")))
	if over.code != http.StatusUnprocessableEntity || !strings.Contains(over.body, "SUP-ASN-001") || !strings.Contains(over.body, "remaining 40") {
		t.Fatalf("over the open quantity: %d %s", over.code, over.body)
	}
	rg.createAsn(t, "TRK-2", asnLine(1, "40", "", ""))
	if r := rg.vendorAsn(asnBodyFor("TRK-3", asnLine(1, "0.000001", "", ""))); r.code != http.StatusUnprocessableEntity || !strings.Contains(r.body, "SUP-ASN-001") {
		t.Fatalf("line fully shipped: %d %s", r.code, r.body)
	}
	rg.createAsn(t, "TRK-4", asnLine(2, "50", "", ""))
}

func TestAsn_RefusesDuplicateTrackingNumberAndBadInput(t *testing.T) {
	rg := newSheetRig(asnPO())
	first := rg.createAsn(t, "TRK-1", asnLine(1, "5", "", ""))
	dup := rg.vendorAsn(asnBodyFor("TRK-1", asnLine(2, "5", "", "")))
	if dup.code != http.StatusConflict || !strings.Contains(dup.body, "SUP-ASN-002") || !strings.Contains(dup.body, first.ID) {
		t.Fatalf("duplicate tracking: %d %s", dup.code, dup.body)
	}
	for name, body := range map[string]string{
		"no lines":             asnBodyFor("T"),
		"zero quantity":        asnBodyFor("T", asnLine(1, "0", "", "")),
		"duplicate line":       asnBodyFor("T", asnLine(1, "1", "", ""), asnLine(1, "1", "", "")),
		"unknown line":         asnBodyFor("T", asnLine(9, "1", "", "")),
		"bad expiry":           asnBodyFor("T", asnLine(1, "1", "B", "11/2026")),
		"serials not per unit": asnBodyFor("T", map[string]any{"poLineNo": 1, "qty": "2", "serials": []string{"S1"}}),
	} {
		if r := rg.vendorAsn(body); r.code != http.StatusBadRequest {
			t.Errorf("%s: got %d %s, want 400", name, r.code, r.body)
		}
	}
	rec := rg.call(http.MethodPost, asnsPath, asnBodyFor("T", asnLine(1, "1", "", "")), rg.maintain())
	if rec.Code != http.StatusForbidden {
		t.Fatalf("without the ASN duty got %d", rec.Code)
	}
}

func TestAsn_ReplayedCreateWithTheSameKeyIsIdempotent(t *testing.T) {
	rg := newSheetRig(asnPO())
	body := asnBodyFor("TRK-1", asnLine(1, "60", "", ""))
	hdr := func() headers { return duty("receiving.asn.maintain", "asn-key", "X-Passage-User", "vendor-user") }

	first := decode[asnJSON](t, rg.call(http.MethodPost, asnsPath, body, hdr()), http.StatusCreated)
	replay := decode[asnJSON](t, rg.call(http.MethodPost, asnsPath, body, hdr()), http.StatusCreated)
	if first.ID != replay.ID {
		t.Fatalf("replay created %s after %s", replay.ID, first.ID)
	}
	list := decode[struct {
		Items []asnJSON `json:"items"`
	}](t, rg.call(http.MethodGet, asnsPath+"?purchaseOrderId="+poID, "", duty("receiving.asn.maintain", "")), http.StatusOK)
	if len(list.Items) != 1 {
		t.Fatalf("%d ASNs after a replayed create, want 1", len(list.Items))
	}
	if r := rg.call(http.MethodPost, asnsPath, asnBodyFor("TRK-1", asnLine(1, "61", "", "")), hdr()); r.Code != http.StatusUnprocessableEntity || !strings.Contains(r.Body.String(), "COM-0409") {
		t.Fatalf("same key, different body: %d %s", r.Code, r.Body)
	}
}

func TestAsn_InvisibleToAnotherLegalEntity(t *testing.T) {
	rg := newSheetRig(asnPO())
	asn := rg.createAsn(t, "TRK-1", asnLine(1, "5", "", ""))
	other := func(extra ...string) headers {
		return duty("receiving.asn.receive", rg.key(), append([]string{entityHeader, otherEntityID}, extra...)...)
	}

	if rec := rg.call(http.MethodGet, asnsPath+"/"+asn.ID, "", other()); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "RCV-NF-010") {
		t.Fatalf("read from another entity: %d %s", rec.Code, rec.Body)
	}
	list := decode[struct {
		Items []asnJSON `json:"items"`
	}](t, rg.call(http.MethodGet, asnsPath, "", other()), http.StatusOK)
	if len(list.Items) != 0 {
		t.Fatalf("another entity lists %+v", list.Items)
	}
	if rec := rg.call(http.MethodPost, asnsPath+"/"+asn.ID+":start-receipt", "", other()); rec.Code != http.StatusNotFound {
		t.Fatalf("start from another entity: %d %s", rec.Code, rec.Body)
	}
	own := decode[asnJSON](t, rg.call(http.MethodGet, asnsPath+"/"+asn.ID, "", duty("receiving.asn.receive", "")), http.StatusOK)
	if own.ID != asn.ID || own.Status != "OPEN" || own.VendorID != vendorID {
		t.Fatalf("own read %+v", own)
	}
}

func TestAsn_StartReceiptPrefillsRegistrationAndRecordsTheReceipt(t *testing.T) {
	rg := newSheetRig(asnPO())
	asn := rg.createAsn(t, "TRK-1", asnLine(1, "30", "LOT-A", "2027-01-31"), asnLine(2, "12", "LOT-B", "2027-03-15"))
	path := asnsPath + "/" + asn.ID + ":start-receipt"

	rec := rg.call(http.MethodPost, path, fmt.Sprintf(`{"receiptDate":"2026-11-05","locationId":%q}`, locationID), duty("receiving.asn.receive", "start-1"))
	got := decode[asnReceiptJSON](t, rec, http.StatusCreated)
	if got.Receipt.State != "ARRIVED" || len(got.Receipt.Lines) != 2 || got.Asn.Status != "RECEIVING" || got.Asn.ReceiptID != got.Receipt.ID {
		t.Fatalf("started %+v", got)
	}

	registered := decode[receiptJSON](t, rg.call(http.MethodPost, receiptsPath+"/"+got.Receipt.ID+":register", string(registrationOf(t, rec.Body.Bytes())), rg.maintain("If-Match", `"1"`)), http.StatusOK)
	if registered.State != "REGISTERED" || registered.Lines[0].Qty != "30" || registered.Lines[1].Qty != "12" {
		t.Fatalf("registered from the pre-filled body %+v", registered)
	}
	if registered.Lines[0].Expiry != "2027-01-31" || registered.Lines[1].Expiry != "2027-03-15" || registered.Lines[0].Batch != "LOT-A" || registered.Lines[1].Batch != "LOT-B" {
		t.Fatalf("one confirmation must store batch and expiry for both lines %+v", registered.Lines)
	}
	read := decode[receiptJSON](t, rg.call(http.MethodGet, receiptsPath+"/"+got.Receipt.ID, "", rg.maintain()), http.StatusOK)
	if read.Lines[0].Expiry != "2027-01-31" || read.Lines[1].Expiry != "2027-03-15" {
		t.Fatalf("expiry not stored on the receipt: %+v", read.Lines)
	}

	replay := rg.call(http.MethodPost, path, fmt.Sprintf(`{"receiptDate":"2026-11-05","locationId":%q}`, locationID), duty("receiving.asn.receive", "start-1"))
	if again := decode[asnReceiptJSON](t, replay, http.StatusCreated); again.Receipt.ID != got.Receipt.ID {
		t.Fatalf("replayed start created %s after %s", again.Receipt.ID, got.Receipt.ID)
	}
	second := rg.call(http.MethodPost, path, "", duty("receiving.asn.receive", "start-2"))
	if second.Code != http.StatusConflict || !strings.Contains(second.Body.String(), "SUP-ASN-003") {
		t.Fatalf("second start: %d %s", second.Code, second.Body)
	}
	after := decode[asnJSON](t, rg.call(http.MethodGet, asnsPath+"/"+asn.ID, "", duty("receiving.asn.maintain", "")), http.StatusOK)
	if after.Status != "RECEIVING" || after.ReceiptID != got.Receipt.ID {
		t.Fatalf("ASN after start %+v", after)
	}
}

func registrationOf(t *testing.T, body []byte) []byte {
	t.Helper()
	var envelope struct {
		Registration json.RawMessage `json:"registration"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.Registration
}

func TestAsn_TrackingNumberIsTrimmedAndCaseFolded(t *testing.T) {
	rg := newSheetRig(asnPO())
	rg.createAsn(t, "TRK-1", asnLine(1, "5", "", ""))
	if r := rg.vendorAsn(asnBodyFor("  trk-1 ", asnLine(2, "5", "", ""))); r.code != http.StatusConflict || !strings.Contains(r.body, "SUP-ASN-002") {
		t.Fatalf("padded lower-case duplicate: %d %s", r.code, r.body)
	}
}

func TestAsn_StartReceiptNeedsTheReceiveDutyAndAPurchaseOrderInTheEntity(t *testing.T) {
	rg := newSheetRig(asnPO())
	asn := rg.createAsn(t, "TRK-1", asnLine(1, "5", "", ""))
	path := asnsPath + "/" + asn.ID + ":start-receipt"
	if rec := rg.call(http.MethodPost, path, "", duty("receiving.asn.maintain", rg.key())); rec.Code != http.StatusForbidden {
		t.Fatalf("the vendor duty started a receipt: %d %s", rec.Code, rec.Body)
	}
	if rec := rg.call(http.MethodPost, asnsPath, asnBodyFor("TRK-9", asnLine(1, "1", "", "")), duty("receiving.asn.receive", rg.key())); rec.Code != http.StatusForbidden {
		t.Fatalf("the receive duty created a notice: %d %s", rec.Code, rec.Body)
	}

	foreign := asnPO()
	foreign.LegalEntityID = otherEntityID
	if r := newSheetRig(foreign).vendorAsn(asnBodyFor("TRK-1", asnLine(1, "1", "", ""))); r.code != http.StatusUnprocessableEntity || !strings.Contains(r.body, "RCV-PO-003") {
		t.Fatalf("purchase order of another legal entity: %d %s", r.code, r.body)
	}
}

func TestAsn_ManualPackingSlipsCannotUseTheReservedPrefix(t *testing.T) {
	rg := newRig(domain.PeriodOpen)
	for _, slip := range []string{"ASN-1", "asn-2"} {
		if rec := rg.call(http.MethodPost, "/v1/receiving/arrivals", arrivalJSON(slip, ""), rg.maintain()); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: got %d %s", slip, rec.Code, rec.Body)
		}
	}
}
