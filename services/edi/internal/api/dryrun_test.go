package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/edi/internal/domain"
)

func TestPunchoutParse_CXMLReturnsLinesAndIssues(t *testing.T) {
	doc := `<cXML payloadID="p" timestamp="t"><Message><PunchOutOrderMessage>
	<PunchOutOrderMessageHeader><Total><Money currency="USD">20.00</Money></Total></PunchOutOrderMessageHeader>
	<ItemIn quantity="2"><ItemID><SupplierPartID>S1</SupplierPartID></ItemID>
	<ItemDetail><UnitPrice><Money currency="USD">10.00</Money></UnitPrice><Description>Bolt</Description>
	<UnitOfMeasure>EA</UnitOfMeasure></ItemDetail></ItemIn>
	</PunchOutOrderMessage></Message></cXML>`
	body, _ := json.Marshal(map[string]any{
		"format": "CXML", "document": doc,
		"supplier": map[string]any{"currencies": []string{"USD"}, "tolerancePct": "5"},
	})
	rec := call(t, http.MethodPost, "/edi/v1/punchout/returns:parse", string(body), "PUR_REQUISITION_CREATE", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Lines []struct {
			SupplierPartID string `json:"supplierPartId"`
			Catalog        bool   `json:"catalog"`
			UnitCode       string `json:"unitCode"`
		} `json:"lines"`
		CartRejected bool `json:"cartRejected"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.CartRejected || len(got.Lines) != 1 || got.Lines[0].SupplierPartID != "S1" || !got.Lines[0].Catalog || got.Lines[0].UnitCode != "C62" {
		t.Fatalf("unexpected cart: %s", rec.Body.String())
	}
}

func TestPunchoutParse_OCIForm(t *testing.T) {
	body := `{"format":"oci","fields":{"NEW_ITEM-VENDORMAT[1]":"X","NEW_ITEM-QUANTITY[1]":"1",` +
		`"NEW_ITEM-PRICE[1]":"3.00","NEW_ITEM-CURRENCY[1]":"USD","NEW_ITEM-UNIT[1]":"EA"},` +
		`"supplier":{"currencies":["USD"]}}`
	rec := call(t, http.MethodPost, "/edi/v1/punchout/returns:parse", body, "PUR_REQUISITION_CREATE", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"supplierPartId":"X"`) {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
}

func TestPunchoutParse_Validation(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"unknown format", `{"format":"EDIFACT","document":"x"}`},
		{"cxml without document", `{"format":"CXML"}`},
		{"bad tolerance", `{"format":"OCI","fields":{"NEW_ITEM-QUANTITY[1]":"1"},"supplier":{"tolerancePct":"25"}}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := call(t, http.MethodPost, "/edi/v1/punchout/returns:parse", tc.body, "PUR_REQUISITION_CREATE", nil)
			if rec.Code != http.StatusBadRequest || problemCode(t, rec) != CodeValidation {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestPunchoutParse_RequiresDuty(t *testing.T) {
	rec := call(t, http.MethodPost, "/edi/v1/punchout/returns:parse", `{"format":"OCI"}`, "EDI_OPERATE", nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status %d, want 403", rec.Code)
	}
}

func TestSelfBillingMatch_ReportsDifferenceAndUnbilled(t *testing.T) {
	body := `{"asOf":"2026-10-31","waitDays":30,
	  "shipments":[
	    {"id":"S1","deliveryNote":"DN1","poLine":"1","item":"I","quantity":"2","unitPrice":"10.00","taxRatePct":"21","shippedOn":"2026-10-01"},
	    {"id":"S2","deliveryNote":"DN2","poLine":"1","item":"I","quantity":"1","unitPrice":"5.00","taxRatePct":"21","shippedOn":"2026-09-01"}],
	  "lines":[{"line":"1","deliveryNote":"DN1","poLine":"1","item":"I","quantity":"2","unitPrice":"10.05","tax":"4.20"}]}`
	rec := call(t, http.MethodPost, "/edi/v1/self-billing:match", body, "SELF_BILLING_RECONCILE", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var got domain.SBResult
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Lines) != 1 || got.Lines[0].Outcome != domain.SelfBillDifference {
		t.Fatalf("lines = %+v", got.Lines)
	}
	// S1 is claimed by the DIFFERENCE line; S2 was shipped 60 days ago and is unbilled.
	if len(got.Unbilled) != 1 || got.Unbilled[0] != "S2" {
		t.Fatalf("unbilled = %v, want [S2]", got.Unbilled)
	}
}

func TestSelfBillingMatch_BadInput(t *testing.T) {
	body := `{"asOf":"31/10/2026","shipments":[{"id":"S1","quantity":"x"}]}`
	rec := call(t, http.MethodPost, "/edi/v1/self-billing:match", body, "SELF_BILLING_RECONCILE", nil)
	if rec.Code != http.StatusBadRequest || problemCode(t, rec) != CodeValidation {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"asOf"`) || !strings.Contains(rec.Body.String(), `"shipments[0].quantity"`) {
		t.Fatalf("field errors missing: %s", rec.Body.String())
	}
}

func TestSelfBillingMatch_ParameterRanges(t *testing.T) {
	for _, tc := range []struct{ body, field string }{
		{`{"waitDays":500}`, "waitDays"},
		{`{"waitDays":-1}`, "waitDays"},
		{`{"tolerance":{"price":"-0.01"}}`, "tolerance.price"},
		{`{"shipments":[{"quantity":"1","shippedOn":"2026-10-01"}]}`, "shipments[0].id"},
		{`{"lines":[{"quantity":"1"}]}`, "lines[0].line"},
	} {
		rec := call(t, http.MethodPost, "/edi/v1/self-billing:match", tc.body, "SELF_BILLING_RECONCILE", nil)
		if rec.Code != http.StatusBadRequest || problemCode(t, rec) != CodeValidation || !strings.Contains(rec.Body.String(), `"`+tc.field+`"`) {
			t.Errorf("%s: status %d: %s", tc.body, rec.Code, rec.Body.String())
		}
	}
}

func TestSelfBillingMatch_RequiresDuty(t *testing.T) {
	rec := call(t, http.MethodPost, "/edi/v1/self-billing:match", `{}`, "EDI_OPERATE", nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status %d, want 403", rec.Code)
	}
}

func TestPunchoutParse_UnreadableDocumentIsSyntax(t *testing.T) {
	body := `{"format":"CXML","document":"<cXML payloadID=\"p\"><Request><OrderRequest/></Request></cXML>","supplier":{"currencies":["USD"]}}`
	rec := call(t, http.MethodPost, "/edi/v1/punchout/returns:parse", body, "PUR_REQUISITION_CREATE", nil)
	if rec.Code != http.StatusBadRequest || problemCode(t, rec) != CodeSyntax {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
}

const ublBody = `{"id":"INV-1","issueDate":"2026-10-01","dueDate":"2026-10-31","currency":"EUR",
 "buyerReference":"PO-9","iban":"DE89370400440532013000",
 "supplier":{"name":"Maker GmbH","endpointScheme":"0088","endpointId":"4006403333333","countryCode":"DE","vatId":"DE123456789"},
 "customer":{"name":"Buyer SAS","endpointScheme":"0088","endpointId":"4000001234567","countryCode":"FR"},
 "lines":[{"id":"1","name":"Widget","unitCode":"C62","quantity":"3","unitPrice":"10.00","category":"S","percent":"21"}]}`

func TestUBLBuild_ValidInvoiceReturnsXML(t *testing.T) {
	rec := call(t, http.MethodPost, "/edi/v1/peppol/invoices:build", ublBody, "EINV_OPERATE", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		XML   string `json:"xml"`
		Valid bool   `json:"valid"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Valid || !strings.Contains(got.XML, "<cbc:PayableAmount currencyID=\"EUR\">36.30</cbc:PayableAmount>") {
		t.Fatalf("unexpected: valid=%v xml=%s", got.Valid, got.XML)
	}
}

func TestUBLBuild_MissingIBANIsBlocked(t *testing.T) {
	body := strings.Replace(ublBody, `"iban":"DE89370400440532013000",`, "", 1)
	rec := call(t, http.MethodPost, "/edi/v1/peppol/invoices:build", body, "EINV_OPERATE", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		XML    string         `json:"xml"`
		Valid  bool           `json:"valid"`
		Issues []domain.Issue `json:"issues"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Valid || got.XML != "" || len(got.Issues) != 1 || got.Issues[0].Code != domain.CodeRequired {
		t.Fatalf("expected one EINV-0110 and no XML: %s", rec.Body.String())
	}
}

func TestUBLBuild_DocumentAllowanceLowersVAT(t *testing.T) {
	body := strings.Replace(ublBody, `"lines":[`, `"allowanceCharges":[{"reason":"Discount","amount":"10.00","category":"S","percent":"21"}],"lines":[`, 1)
	rec := call(t, http.MethodPost, "/edi/v1/peppol/invoices:build", body, "EINV_OPERATE", nil)
	// 30.00 - 10.00 = 20.00 taxable, 4.20 VAT, 24.20 payable.
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `PayableAmount currencyID=\"EUR\"\u003e24.20`) {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
}

func TestUBLBuild_RequiresDuty(t *testing.T) {
	rec := call(t, http.MethodPost, "/edi/v1/peppol/invoices:build", ublBody, "EDI_OPERATE", nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status %d, want 403", rec.Code)
	}
}

func TestUBLBuild_BadDecimalIsValidation(t *testing.T) {
	body := strings.Replace(ublBody, `"quantity":"3"`, `"quantity":"three"`, 1)
	rec := call(t, http.MethodPost, "/edi/v1/peppol/invoices:build", body, "EINV_OPERATE", nil)
	if rec.Code != http.StatusBadRequest || problemCode(t, rec) != CodeValidation {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
}
