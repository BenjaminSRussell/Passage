package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/intercompany/internal/api"
)

const (
	chainPath    = "/v1/interco/chains:preview"
	evidencePath = "/v1/interco/transfer-pricing/evidence:export"
	reversalPath = "/v1/interco/centralized-payments/reversals:preview"
)

// chainBody is AC-40.3: customer order in A, IC PO from A to B and IC SO in B
// for 20 units, B shipping straight to A's customer.
const chainBody = `{
  "chainId":"CH-1","partner":{"sellerEntity":"B","buyerEntity":"A","active":true},
  "customerOrder":{"docId":"SO-A-1","entity":"A","quantity":"20"},
  "icPurchase":{"docId":"PO-A-1","entity":"A","quantity":"20"},
  "icSales":{"docId":"SO-B-1","entity":"B","quantity":"20"}
}`

type chainOut struct {
	ChainID  string `json:"chainId"`
	Postings []struct{ Kind, Entity, DocID, Quantity string }
}

func TestPreviewChain_AC403(t *testing.T) {
	a := api.New()
	rec := do(router(a), chainPath, tenantID, api.DutyView, chainBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	var got chainOut
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.ChainID != "CH-1" || len(got.Postings) != 2 ||
		got.Postings[0] != (struct{ Kind, Entity, DocID, Quantity string }{"ProductReceipt", "A", "PO-A-1", "20"}) ||
		got.Postings[1] != (struct{ Kind, Entity, DocID, Quantity string }{"CustomerPackingSlip", "A", "SO-A-1", "20"}) {
		t.Fatalf("response = %+v", got)
	}

	partial := strings.Replace(chainBody, `"chainId":"CH-1"`, `"chainId":"CH-1","shippedQuantity":"5"`, 1)
	rec = do(router(a), chainPath, tenantID, api.DutyView, partial)
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK || got.Postings[0].Quantity != "5" || got.Postings[1].Quantity != "5" {
		t.Fatalf("partial shipment: status %d body %s", rec.Code, rec.Body.String())
	}

	mrec := httptest.NewRecorder()
	a.Metrics().ServeHTTP(mrec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(mrec.Body.String(), "intercompany_chain_previews_total 2") {
		t.Fatalf("metrics = %s", mrec.Body.String())
	}
}

func TestPreviewChain_Errors(t *testing.T) {
	cases := []struct {
		name, body, duties string
		status             int
		code, field        string
	}{
		{"inactive pairing", strings.Replace(chainBody, `"active":true`, `"active":false`, 1), api.DutyView, http.StatusUnprocessableEntity, "IC-0801", ""},
		{"sales order not in seller", strings.Replace(chainBody, `"docId":"SO-B-1","entity":"B"`, `"docId":"SO-B-1","entity":"C"`, 1), api.DutyView, http.StatusUnprocessableEntity, "IC-0801", ""},
		{"quantity mismatch", strings.Replace(chainBody, `"docId":"SO-B-1","entity":"B","quantity":"20"`, `"docId":"SO-B-1","entity":"B","quantity":"19"`, 1), api.DutyView, http.StatusBadRequest, "COM-0001", ""},
		{"over-shipment", strings.Replace(chainBody, `"chainId":"CH-1"`, `"chainId":"CH-1","shippedQuantity":"21"`, 1), api.DutyView, http.StatusBadRequest, "COM-0001", ""},
		{"zero shipment", strings.Replace(chainBody, `"chainId":"CH-1"`, `"chainId":"CH-1","shippedQuantity":"0"`, 1), api.DutyView, http.StatusBadRequest, "COM-0001", "shippedQuantity"},
		{"missing document", strings.Replace(chainBody, `"docId":"PO-A-1"`, `"docId":""`, 1), api.DutyView, http.StatusBadRequest, "COM-0001", "icPurchase.docId"},
		{"bad quantity", strings.Replace(chainBody, `"docId":"SO-A-1","entity":"A","quantity":"20"`, `"docId":"SO-A-1","entity":"A","quantity":"x"`, 1), api.DutyView, http.StatusBadRequest, "COM-0001", "customerOrder.quantity"},
		{"same partner entities", strings.Replace(chainBody, `"buyerEntity":"A"`, `"buyerEntity":"B"`, 1), api.DutyView, http.StatusBadRequest, "COM-0001", "partner.buyerEntity"},
		{"no duty", chainBody, "", http.StatusForbidden, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := do(router(api.New()), chainPath, tenantID, c.duties, c.body)
			p := problem(t, rec)
			if rec.Code != c.status || (c.code != "" && p.Code != c.code) || (c.field != "" && p.Errors[c.field] == "") {
				t.Fatalf("status %d body %s; want %d %s on %q", rec.Code, rec.Body.String(), c.status, c.code, c.field)
			}
		})
	}
}

const evidenceBody = `{
  "sellerEntity":"P","buyerEntity":"S","period":"2026-11","currency":"EUR",
  "rows":[{"item":"CONSULT","quantity":"8","price":"528.00","cost":"480.00","basis":"CostPlus"},
          {"item":"PUMP-100","quantity":"10","price":"1500","cost":"1500","basis":"Cost"}]
}`

func TestExportEvidence_CSV(t *testing.T) {
	rec := do(router(api.New()), evidencePath, tenantID, api.DutyView, evidenceBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/csv; charset=utf-8" {
		t.Fatalf("content type = %q", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); cd != `attachment; filename="tp-evidence-P-S-2026-11.csv"` {
		t.Fatalf("content disposition = %q", cd)
	}
	want := "seller_entity,buyer_entity,period,item,quantity,currency,price,cost,margin,margin_pct,basis\n" +
		"P,S,2026-11,CONSULT,8,EUR,528.00,480.00,48.00,9.09,CostPlus\n" +
		"P,S,2026-11,PUMP-100,10,EUR,1500.00,1500.00,0.00,0.00,Cost\n"
	if rec.Body.String() != want {
		t.Fatalf("csv =\n%s\nwant\n%s", rec.Body.String(), want)
	}
}

func TestExportEvidence_FileNameIsSanitised(t *testing.T) {
	body := strings.Replace(evidenceBody, `"sellerEntity":"P"`, `"sellerEntity":"P\"; x=\r\ny"`, 1)
	rec := do(router(api.New()), evidencePath, tenantID, api.DutyView, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	if cd := rec.Header().Get("Content-Disposition"); cd != `attachment; filename="tp-evidence-P___x___y-S-2026-11.csv"` {
		t.Fatalf("content disposition = %q", cd)
	}
}

func TestExportEvidence_Errors(t *testing.T) {
	cases := []struct {
		name, body, duties string
		status             int
		field              string
	}{
		{"bad period", strings.Replace(evidenceBody, `"2026-11"`, `"2026-13"`, 1), api.DutyView, http.StatusBadRequest, ""},
		{"period format", strings.Replace(evidenceBody, `"2026-11"`, `"11/2026"`, 1), api.DutyView, http.StatusBadRequest, "period"},
		{"same entities", strings.Replace(evidenceBody, `"buyerEntity":"S"`, `"buyerEntity":"P"`, 1), api.DutyView, http.StatusBadRequest, "buyerEntity"},
		{"bad currency", strings.Replace(evidenceBody, `"EUR"`, `"eur"`, 1), api.DutyView, http.StatusBadRequest, "currency"},
		{"no rows", `{"sellerEntity":"P","buyerEntity":"S","period":"2026-11","currency":"EUR","rows":[]}`, api.DutyView, http.StatusBadRequest, "rows"},
		{"negative cost", strings.Replace(evidenceBody, `"480.00"`, `"-1"`, 1), api.DutyView, http.StatusBadRequest, "rows[0].cost"},
		{"zero quantity", strings.Replace(evidenceBody, `"quantity":"8"`, `"quantity":"0"`, 1), api.DutyView, http.StatusBadRequest, "rows[0].quantity"},
		{"no basis", strings.Replace(evidenceBody, `"basis":"Cost"`, `"basis":" "`, 1), api.DutyView, http.StatusBadRequest, "rows[1].basis"},
		{"no duty", evidenceBody, api.DutyPost, http.StatusForbidden, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := do(router(api.New()), evidencePath, tenantID, c.duties, c.body)
			p := problem(t, rec)
			if rec.Code != c.status || (c.status == http.StatusBadRequest && p.Code != "COM-0001") || (c.field != "" && p.Errors[c.field] == "") {
				t.Fatalf("status %d body %s; want %d on %q", rec.Code, rec.Body.String(), c.status, c.field)
			}
		})
	}
}

const pairID = "01890a5d-ac96-774b-bcce-b302099a8099"

// TestPreviewReversal_BR4011 reverses the AC-40.10 payment after a bank return:
// both entities' legs flip side under the original pair id.
func TestPreviewReversal_BR4011(t *testing.T) {
	body := strings.Replace(paymentBody, "{", `{"pairId":"`+pairID+`",`, 1)
	rec := do(router(api.New()), reversalPath, tenantID, api.DutyConfigure, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	var got journalBody
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := []legBody{
		{"P", "1510-DUE-FROM-S", "credit", "10000.00", "EUR"},
		{"P", "1100-BANK", "debit", "10000.00", "EUR"},
		{"S", "2000-VENDOR", "credit", "10000.00", "EUR"},
		{"S", "2510-DUE-TO-P", "debit", "10000.00", "EUR"},
	}
	if got.PairID != pairID || !got.Balanced || len(got.Legs) != len(want) {
		t.Fatalf("response = %+v", got)
	}
	for i := range want {
		if got.Legs[i] != want[i] {
			t.Fatalf("leg %d = %+v; want %+v", i, got.Legs[i], want[i])
		}
	}
}

func TestPreviewReversal_Errors(t *testing.T) {
	withPair := func(id string) string { return strings.Replace(paymentBody, "{", `{"pairId":"`+id+`",`, 1) }
	cases := []struct {
		name, body, duties string
		status             int
		code, field        string
	}{
		{"no pair id", paymentBody, api.DutyConfigure, http.StatusBadRequest, "COM-0001", "pairId"},
		{"malformed pair id", withPair("pair-1"), api.DutyConfigure, http.StatusBadRequest, "COM-0001", "pairId"},
		{"bad direction", strings.Replace(withPair(pairID), `"Pay"`, `"Both"`, 1), api.DutyConfigure, http.StatusBadRequest, "COM-0001", "direction"},
		{"missing matrix row", strings.Replace(withPair(pairID), `"owningEntity":"S"`, `"owningEntity":"T"`, 1), api.DutyConfigure, http.StatusUnprocessableEntity, "IC-0805", ""},
		{"view duty only", withPair(pairID), api.DutyView, http.StatusForbidden, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := do(router(api.New()), reversalPath, tenantID, c.duties, c.body)
			p := problem(t, rec)
			if rec.Code != c.status || (c.code != "" && p.Code != c.code) || (c.field != "" && p.Errors[c.field] == "") {
				t.Fatalf("status %d body %s; want %d %s on %q", rec.Code, rec.Body.String(), c.status, c.code, c.field)
			}
		})
	}
}
