package api_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
)

const relatedPath = "/v1/receiving/related-documents"

type relatedJSON struct {
	PurchaseOrderID string `json:"purchaseOrderId"`
	Receipts        []struct {
		ID            string `json:"id"`
		ReceiptNumber string `json:"receiptNumber"`
		State         string `json:"state"`
	} `json:"receipts"`
	Invoices []struct {
		ID            string `json:"id"`
		InvoiceNumber string `json:"invoiceNumber"`
		LedgerEntryID string `json:"ledgerEntryId"`
	} `json:"invoices"`
	Truncated bool `json:"truncated"`
}

func (rg *rig) flowReader() headers {
	return duty("receiving.document-flow.read", rg.key())
}

func TestRelatedDocuments_TracesAPurchaseOrderFromAnyOfItsDocuments(t *testing.T) {
	rg := newRig(domain.PeriodOpen)
	r, inv := rg.invoiced(t, "PS-1", "INV-1", 40, "2.5", "")

	for name, query := range map[string]string{
		"purchase order": "?purchaseOrderId=" + poID,
		"receipt":        "?receiptId=" + r.ID,
		"invoice":        "?invoiceId=" + inv.ID,
	} {
		got := decode[relatedJSON](t, rg.call(http.MethodGet, relatedPath+query, "", rg.flowReader()), http.StatusOK)
		if got.PurchaseOrderID != poID || len(got.Receipts) != 1 || got.Receipts[0].ID != r.ID || got.Receipts[0].State != "POSTED" {
			t.Fatalf("%s: receipts %+v for order %s, want receipt %s", name, got.Receipts, got.PurchaseOrderID, r.ID)
		}
		if len(got.Invoices) != 1 || got.Invoices[0].ID != inv.ID || got.Invoices[0].InvoiceNumber != "INV-1" || got.Invoices[0].LedgerEntryID == "" {
			t.Fatalf("%s: invoices %+v, want %s with its ledger entry", name, got.Invoices, inv.ID)
		}
	}
}

func TestRelatedDocuments_OtherOrderHasNothing(t *testing.T) {
	rg := newRig(domain.PeriodOpen)
	rg.invoiced(t, "PS-1", "INV-1", 40, "2.5", "")
	got := decode[relatedJSON](t, rg.call(http.MethodGet, relatedPath+"?purchaseOrderId="+otherPOID, "", rg.flowReader()), http.StatusOK)
	if len(got.Receipts) != 0 || len(got.Invoices) != 0 {
		t.Fatalf("got %+v, want nothing", got)
	}
}

func TestRelatedDocuments_RefusesBadQueriesAndMissingDuty(t *testing.T) {
	rg := newRig(domain.PeriodOpen)
	for _, query := range []string{"", "?purchaseOrderId=nope", "?purchaseOrderId=" + poID + "&receiptId=" + poID} {
		rec := rg.call(http.MethodGet, relatedPath+query, "", rg.flowReader())
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "COM-0001") {
			t.Fatalf("%q: got %d %s, want 400 COM-0001", query, rec.Code, rec.Body)
		}
	}
	if rec := rg.call(http.MethodGet, relatedPath+"?purchaseOrderId="+poID, "", rg.inquirer()); rec.Code != http.StatusForbidden {
		t.Fatalf("without the document flow duty got %d", rec.Code)
	}
	if rec := rg.call(http.MethodGet, relatedPath+"?invoiceId="+otherPOID, "", rg.flowReader()); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown invoice got %d, want 404", rec.Code)
	}
}
