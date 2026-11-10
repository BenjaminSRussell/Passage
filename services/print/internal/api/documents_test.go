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
