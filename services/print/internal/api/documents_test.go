package api_test

import (
	"net/http"
	"strings"
	"testing"
)

const poDoc = `{"documentType":"PURCHASE_ORDER","number":"PO-1","legalEntity":{"name":"Cedar Ridge Pumps","address":["1 Main St"]},"columns":[{"header":"Item"},{"header":"Qty","align":"R"}],"lines":[["Casting","4"]]}`

func TestRenderDocument_ReturnsPDF(t *testing.T) {
	h := router(newMemStore(), &fakeSender{})
	rec := call(t, h, http.MethodPost, "/print/v1/documents:render", poDoc, "print.document.render", "")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/pdf" || !strings.HasPrefix(rec.Body.String(), "%PDF-") {
		t.Fatalf("got %d %s %q", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String()[:10])
	}
	again := call(t, h, http.MethodPost, "/print/v1/documents:render", poDoc, "print.document.render", "")
	if again.Body.String() != rec.Body.String() {
		t.Fatal("a repeated call returned different bytes")
	}
}

// A request as the first release of the route accepted it still renders: no counterparty
// address, no additional parties.
func TestRenderDocument_ReportsPagesAndNoUnprintableCharactersForPlainText(t *testing.T) {
	h := router(newMemStore(), &fakeSender{})
	rec := call(t, h, http.MethodPost, "/print/v1/documents:render", poDoc, "print.document.render", "")
	if rec.Header().Get("X-Passage-Document-Pages") != "1" || rec.Header().Get("X-Passage-Unprintable-Characters") != "" {
		t.Fatalf("headers = %v", rec.Header())
	}
}

const unicodeDoc = `{"documentType":"CUSTOMER_INVOICE","number":"INV-7","legalEntity":{"name":"Łódzkie Zakłady Pomp","address":["ul. Piotrkowska 104","90-926 Łódź"]},` +
	`"counterparty":{"label":"Bill to","name":"Žluťoučký kůň s.r.o.","address":["Náměstí Míru 15","120 00 Praha 2"]},` +
	`"additionalParties":[{"label":"Ship to","name":"Αθηναϊκή Εμπορική Α.Ε.","address":["Οδός Ερμού 25"]}],` +
	`"columns":[{"header":"Description"},{"header":"Net","align":"R"}],"lines":[["Сервисное обслуживание","900,00"],["東京支店","1,00"]]}`

func TestRenderDocument_AcceptsUnicodeAndCountsWhatTheFontsLack(t *testing.T) {
	h := router(newMemStore(), &fakeSender{})
	rec := call(t, h, http.MethodPost, "/print/v1/documents:render", unicodeDoc, "print.document.render", "")
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Body.String(), "%PDF-") {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String()[:min(200, rec.Body.Len())])
	}
	// Polish, Czech, Greek and Cyrillic are drawn; the four CJK characters are not.
	if got := rec.Header().Get("X-Passage-Unprintable-Characters"); got != "4" {
		t.Fatalf("unprintable characters = %q, want 4", got)
	}
	if !strings.Contains(rec.Body.String(), "/Encoding /Identity-H") || !strings.Contains(rec.Body.String(), "/ToUnicode") {
		t.Fatal("the PDF does not embed a Unicode font")
	}
}

// The remittance advice exactly as the payables payment run page sends it (#528).
const remittanceDoc = `{"documentType":"REMITTANCE_ADVICE","number":"E2E-1","legalEntity":{"name":"Cedar Ridge Pumps","address":[]},` +
	`"counterparty":{"label":"Vendor","name":"Contoso Ltd (V-1)","address":[]},` +
	`"fields":[{"label":"Payment date","value":"2026-11-20"},{"label":"Payment method","value":"ACH credit"},{"label":"Currency","value":"USD"}],` +
	`"columns":[{"header":"Invoice"},{"header":"Invoice date"},{"header":"Amount open","align":"R"},{"header":"Discount","align":"R"},{"header":"Tax withheld","align":"R"},{"header":"Paid","align":"R"}],` +
	`"lines":[["INV-1001","2026-09-01","600.10","20.00","12.00","568.10"],["INV-1002","2026-09-03","400.00","0.00","0.00","400.00"]],` +
	`"totals":[{"label":"Discount taken","value":"20.00"},{"label":"Tax withheld","value":"12.00"},{"label":"Total paid USD","value":"968.10"}]}`

func TestRenderDocument_RendersARemittanceAdvice(t *testing.T) {
	h := router(newMemStore(), &fakeSender{})
	rec := call(t, h, http.MethodPost, "/print/v1/documents:render", remittanceDoc, "print.document.render", "")
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Body.String(), "%PDF-") {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String()[:min(200, rec.Body.Len())])
	}
	if rec.Header().Get("X-Passage-Document-Pages") != "1" {
		t.Fatalf("pages = %q", rec.Header().Get("X-Passage-Document-Pages"))
	}
}

func TestRenderDocument_TooManyLinesHasItsOwnCode(t *testing.T) {
	h := router(newMemStore(), &fakeSender{})
	lines := strings.TrimSuffix(strings.Repeat(`["Casting","4"],`, 501), ",")
	big := strings.Replace(poDoc, `[["Casting","4"]]`, "["+lines+"]", 1)
	rec := call(t, h, http.MethodPost, "/print/v1/documents:render", big, "print.document.render", "")
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "PRN-0009") {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}

func TestRenderDocument_UnrenderableDocumentIsRefusedWithTheField(t *testing.T) {
	h := router(newMemStore(), &fakeSender{})
	bad := strings.Replace(poDoc, `"PO-1"`, `""`, 1)
	rec := call(t, h, http.MethodPost, "/print/v1/documents:render", bad, "print.document.render", "")
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "PRN-0008") || !strings.Contains(rec.Body.String(), "number") {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}

func TestRenderDocument_NeedsItsOwnDuty(t *testing.T) {
	h := router(newMemStore(), &fakeSender{})
	if rec := call(t, h, http.MethodPost, "/print/v1/documents:render", poDoc, "print.job.submit", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("status %d, want 403", rec.Code)
	}
}
