package api_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
)

const (
	listInvoicesPath = "/v1/receiving/invoices"
	otherEntityID    = "44444444-4444-4444-8444-444444444444"
	otherPOID        = "55555555-5555-4555-8555-555555555555"
)

type invoiceListJSON struct {
	Items []struct {
		ID            string `json:"id"`
		LedgerEntryID string `json:"ledgerEntryId"`
		Status        string `json:"status"`
		Lines         []struct {
			ReceiptID string `json:"receiptId"`
		} `json:"lines"`
	} `json:"items"`
	NextCursor string `json:"nextCursor"`
}

func (rg *rig) inquirer(extra ...string) headers {
	return duty("receiving.ap.inquire", rg.key(), extra...)
}

func TestListInvoices_ReturnsPostedInvoicesWithLedgerEntryByReceiptAndPurchaseOrder(t *testing.T) {
	rg := newRig(domain.PeriodOpen)
	r, inv := rg.invoiced(t, "PS-1", "INV-1", 40, "2.5", "")
	rg.invoiced(t, "PS-2", "INV-2", 10, "2.5", "")

	byReceipt := decode[invoiceListJSON](t, rg.call(http.MethodGet, listInvoicesPath+"?receiptId="+r.ID, "", rg.inquirer()), http.StatusOK)
	if len(byReceipt.Items) != 1 || byReceipt.Items[0].ID != inv.ID || byReceipt.Items[0].LedgerEntryID == "" || byReceipt.Items[0].Lines[0].ReceiptID != r.ID {
		t.Fatalf("by receipt %+v, want only %s with its ledger entry", byReceipt, inv.ID)
	}
	byOrder := decode[invoiceListJSON](t, rg.call(http.MethodGet, listInvoicesPath+"?purchaseOrderId="+poID+"&status=POSTED", "", rg.inquirer()), http.StatusOK)
	if len(byOrder.Items) != 2 {
		t.Fatalf("by purchase order %+v, want both invoices", byOrder)
	}
	otherOrder := decode[invoiceListJSON](t, rg.call(http.MethodGet, listInvoicesPath+"?purchaseOrderId="+otherPOID, "", rg.inquirer()), http.StatusOK)
	if len(otherOrder.Items) != 0 {
		t.Fatalf("other purchase order %+v, want none", otherOrder)
	}
	paged := decode[invoiceListJSON](t, rg.call(http.MethodGet, listInvoicesPath+"?limit=1", "", rg.inquirer()), http.StatusOK)
	if len(paged.Items) != 1 || paged.NextCursor == "" {
		t.Fatalf("first page %+v, want one item and a cursor", paged)
	}
	next := decode[invoiceListJSON](t, rg.call(http.MethodGet, listInvoicesPath+"?limit=1&cursor="+paged.NextCursor, "", rg.inquirer()), http.StatusOK)
	if len(next.Items) != 1 || next.Items[0].ID == paged.Items[0].ID || next.NextCursor != "" {
		t.Fatalf("second page %+v, want the other invoice and no cursor", next)
	}
}

func TestListInvoices_StaysInsideTheCallersLegalEntity(t *testing.T) {
	rg := newRig(domain.PeriodOpen)
	rg.invoiced(t, "PS-1", "INV-1", 40, "2.5", "")

	got := decode[invoiceListJSON](t, rg.call(http.MethodGet, listInvoicesPath, "", rg.inquirer(entityHeader, otherEntityID)), http.StatusOK)
	if len(got.Items) != 0 {
		t.Fatalf("another legal entity sees %+v", got)
	}
}

func TestListInvoices_RefusesBadFiltersAndMissingDuty(t *testing.T) {
	rg := newRig(domain.PeriodOpen)
	for _, query := range []string{"?receiptId=nope", "?purchaseOrderId=nope", "?vendorId=nope", "?status=DONE", "?limit=0", "?cursor=junk"} {
		rec := rg.call(http.MethodGet, listInvoicesPath+query, "", rg.inquirer())
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "COM-0001") {
			t.Fatalf("%s: got %d %s, want 400 COM-0001", query, rec.Code, rec.Body)
		}
	}
	if rec := rg.call(http.MethodGet, listInvoicesPath, "", rg.maintain()); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "AUTH-0003") {
		t.Fatalf("without the inquire duty got %d %s", rec.Code, rec.Body)
	}
	if rec := doWith(rg.h, http.MethodGet, listInvoicesPath, "", headers{}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("without a tenant got %d", rec.Code)
	}
}

func TestGetInvoice_ReturnsTheInvoiceWithLinesAndLedgerEntry(t *testing.T) {
	rg := newRig(domain.PeriodOpen)
	r, inv := rg.invoiced(t, "PS-1", "INV-1", 40, "2.5", "")

	got := decode[invoiceListJSON](t, rg.call(http.MethodGet, listInvoicesPath, "", rg.inquirer()), http.StatusOK).Items[0]
	one := decode[struct {
		ID            string `json:"id"`
		LedgerEntryID string `json:"ledgerEntryId"`
		Status        string `json:"status"`
		Lines         []struct {
			ReceiptID string `json:"receiptId"`
		} `json:"lines"`
	}](t, rg.call(http.MethodGet, listInvoicesPath+"/"+inv.ID, "", rg.inquirer()), http.StatusOK)
	if one.ID != inv.ID || one.LedgerEntryID != got.LedgerEntryID || one.LedgerEntryID == "" || one.Status != "POSTED" || len(one.Lines) != 1 || one.Lines[0].ReceiptID != r.ID {
		t.Fatalf("got %+v, want invoice %s with its ledger entry and line", one, inv.ID)
	}
}

func TestGetInvoice_IsNotFoundOutsideTheCallersLegalEntityOrForAnUnknownId(t *testing.T) {
	rg := newRig(domain.PeriodOpen)
	_, inv := rg.invoiced(t, "PS-1", "INV-1", 40, "2.5", "")

	for name, rec := range map[string]*httptest.ResponseRecorder{
		"other legal entity": rg.call(http.MethodGet, listInvoicesPath+"/"+inv.ID, "", rg.inquirer(entityHeader, otherEntityID)),
		"unknown id":         rg.call(http.MethodGet, listInvoicesPath+"/"+otherPOID, "", rg.inquirer()),
	} {
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "RCV-NF-005") {
			t.Fatalf("%s: got %d %s, want 404 RCV-NF-005", name, rec.Code, rec.Body)
		}
	}
	if rec := rg.call(http.MethodGet, listInvoicesPath+"/nope", "", rg.inquirer()); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad id got %d, want 400", rec.Code)
	}
}

func TestGetInvoice_RefusesMissingDutyAndTenant(t *testing.T) {
	rg := newRig(domain.PeriodOpen)
	_, inv := rg.invoiced(t, "PS-1", "INV-1", 40, "2.5", "")

	if rec := rg.call(http.MethodGet, listInvoicesPath+"/"+inv.ID, "", rg.maintain()); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "AUTH-0003") {
		t.Fatalf("without the inquire duty got %d %s", rec.Code, rec.Body)
	}
	if rec := doWith(rg.h, http.MethodGet, listInvoicesPath+"/"+inv.ID, "", headers{}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("without a tenant got %d", rec.Code)
	}
}

func TestListReceipts_FiltersByPurchaseOrder(t *testing.T) {
	rg := newRig(domain.PeriodOpen)
	r := rg.posted(t, "PS-1", 100, 10)

	got := decode[struct {
		Items []receiptJSON `json:"items"`
	}](t, rg.call(http.MethodGet, receiptsPath+"?purchaseOrderId="+poID, "", rg.maintain()), http.StatusOK)
	if len(got.Items) != 1 || got.Items[0].ID != r.ID {
		t.Fatalf("by purchase order %+v, want %s", got, r.ID)
	}
	none := decode[struct {
		Items []receiptJSON `json:"items"`
	}](t, rg.call(http.MethodGet, receiptsPath+"?purchaseOrderId="+otherPOID, "", rg.maintain()), http.StatusOK)
	if len(none.Items) != 0 {
		t.Fatalf("other purchase order %+v, want none", none)
	}
}
