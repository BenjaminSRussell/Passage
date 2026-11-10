package api_test

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/api"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/ocr"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store/memstore"
)

const (
	capturesPath = "/v1/receiving/invoice-captures"
	ublDocument  = `<Invoice xmlns:cbc="urn:x"><cbc:ID>INV-9</cbc:ID><cbc:IssueDate>2026-10-20</cbc:IssueDate><cbc:DocumentCurrencyCode>EUR</cbc:DocumentCurrencyCode>` +
		`<AccountingSupplierParty><Party><PartyTaxScheme><CompanyID>DE1</CompanyID></PartyTaxScheme></Party></AccountingSupplierParty>` +
		`<LegalMonetaryTotal><TaxExclusiveAmount>25.00</TaxExclusiveAmount></LegalMonetaryTotal>` +
		`<InvoiceLine><InvoicedQuantity unitCode="EA">10</InvoicedQuantity><LineExtensionAmount>25.00</LineExtensionAmount><Item><Name>Bolt</Name></Item><Price><PriceAmount>2.50</PriceAmount></Price></InvoiceLine></Invoice>`
)

type captureJSON struct {
	ID            string   `json:"id"`
	Source        string   `json:"source"`
	Status        string   `json:"status"`
	ReviewReasons []string `json:"reviewReasons"`
	InvoiceNumber string   `json:"invoiceNumber"`
	Total         string   `json:"total"`
	Confidence    string   `json:"confidence"`
	Lines         []struct {
		Description string `json:"description"`
		Qty         string `json:"qty"`
	} `json:"lines"`
}

func captureRequest(format, mediaType, content string) string {
	body := map[string]any{"legalEntityId": legalEntityID, "format": format, "content": base64.StdEncoding.EncodeToString([]byte(content))}
	if mediaType != "" {
		body["mediaType"] = mediaType
	}
	raw, _ := json.Marshal(body)
	return string(raw)
}

func newCaptureRig(ex domain.DocumentExtractor) *rig {
	rg := &rig{st: memstore.New(), ledger: &domain.FakeLedger{}}
	a := api.New(rg.st, api.Ports{
		Periods: domain.FakePeriods{Status: domain.PeriodOpen}, Ledger: rg.ledger, Extractor: ex,
		Profile: domain.PostingProfile{LedgerID: ledgerID, Inventory: "1400", GRIR: "2100", PPV: "5200", VendorControl: "2000"},
	})
	router := httpx.NewRouter()
	a.Register(router)
	rg.h, rg.api = router, a
	return rg
}

func scannedInvoice(confidence string) domain.ExtractedInvoice {
	return domain.ExtractedInvoice{
		VendorTaxID: "DE1", InvoiceNumber: "SCAN-1", InvoiceDate: time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC), Currency: "USD",
		Total: decimal.NewFromInt(40), Confidence: decimal.RequireFromString(confidence),
		Lines: []domain.CaptureLine{{Description: "Widget", Unit: "EA", Qty: decimal.NewFromInt(4), UnitPrice: decimal.NewFromInt(10), Amount: decimal.NewFromInt(40)}},
	}
}

func TestInvoiceCapture_UBLIsStoredReadableAndDeduplicated(t *testing.T) {
	rg := newCaptureRig(nil)
	rec := rg.call(http.MethodPost, capturesPath, captureRequest("UBL", "", ublDocument), duty("receiving.vendor-invoice.post", rg.key()))
	got := decode[captureJSON](t, rec, http.StatusCreated)
	if got.Status != "CAPTURED" || got.Source != "UBL" || got.InvoiceNumber != "INV-9" || got.Total != "25" || len(got.Lines) != 1 || got.Lines[0].Qty != "10" || len(got.ReviewReasons) != 0 {
		t.Fatalf("capture %+v", got)
	}
	if rec.Header().Get("Location") != capturesPath+"/"+got.ID {
		t.Fatalf("location %q", rec.Header().Get("Location"))
	}
	again := decode[captureJSON](t, rg.call(http.MethodPost, capturesPath, captureRequest("UBL", "", ublDocument), duty("receiving.vendor-invoice.post", rg.key())), http.StatusOK)
	if again.ID != got.ID {
		t.Fatalf("same document captured twice: %s and %s", got.ID, again.ID)
	}
	read := decode[captureJSON](t, rg.call(http.MethodGet, capturesPath+"/"+got.ID, "", duty("receiving.ap.inquire", "")), http.StatusOK)
	if read.ID != got.ID || read.InvoiceNumber != "INV-9" {
		t.Fatalf("read %+v", read)
	}
}

func TestInvoiceCapture_OCRUsesTheExtractorAndFlagsLowConfidence(t *testing.T) {
	ex := &domain.FakeExtractor{Result: scannedInvoice("0.55")}
	rg := newCaptureRig(ex)
	got := decode[captureJSON](t, rg.call(http.MethodPost, capturesPath, captureRequest("OCR", "application/pdf", "%PDF-1.7 scan"), duty("receiving.vendor-invoice.post", rg.key())), http.StatusCreated)
	if ex.Calls != 1 || got.Source != "OCR" || got.Status != "NEEDS_REVIEW" || len(got.ReviewReasons) != 1 || got.ReviewReasons[0] != domain.ReviewLowConfidence || got.Confidence != "0.55" {
		t.Fatalf("calls %d capture %+v", ex.Calls, got)
	}
}

func TestInvoiceCapture_LocalOCRProviderReadsAPDFTextLayer(t *testing.T) {
	rg := newCaptureRig(ocr.Local{})
	pdf := "%PDF-1.7\n1 0 obj << /Length 0 >>\nstream\nBT (Invoice Number: LOC-7) Tj (Invoice Date: 2026-11-01) Tj (Currency: USD) Tj " +
		"(Vendor Tax ID: US99) Tj (Line: Field service | 3 | HUR | 90 | 270) Tj (Total: 270.00) Tj ET\nendstream\nendobj\n"
	got := decode[captureJSON](t, rg.call(http.MethodPost, capturesPath, captureRequest("OCR", "application/pdf", pdf), duty("receiving.vendor-invoice.post", rg.key())), http.StatusCreated)
	if got.Source != "OCR" || got.Status != "CAPTURED" || got.InvoiceNumber != "LOC-7" || got.Total != "270" || got.Confidence != "0.85" || len(got.Lines) != 1 || got.Lines[0].Qty != "3" {
		t.Fatalf("capture %+v", got)
	}
	problem := decodeProblem(t, rg.call(http.MethodPost, capturesPath, captureRequest("OCR", "image/png", "\x89PNG scan"), duty("receiving.vendor-invoice.post", rg.key())))
	if problem.Status != http.StatusUnprocessableEntity || problem.Code != domain.CodeCaptureExtraction {
		t.Fatalf("image without a text layer %+v", problem)
	}
}

func TestInvoiceCapture_OCRFailureAndMissingProvider(t *testing.T) {
	failing := newCaptureRig(&domain.FakeExtractor{Err: domain.ErrExtraction})
	problem := decodeProblem(t, failing.call(http.MethodPost, capturesPath, captureRequest("OCR", "image/png", "png"), duty("receiving.vendor-invoice.post", failing.key())))
	if problem.Status != http.StatusUnprocessableEntity || problem.Code != domain.CodeCaptureExtraction {
		t.Fatalf("failure %+v", problem)
	}
	unconfigured := newCaptureRig(nil)
	rec := unconfigured.call(http.MethodPost, capturesPath, captureRequest("OCR", "image/png", "png"), duty("receiving.vendor-invoice.post", unconfigured.key()))
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "COM-0503") {
		t.Fatalf("unconfigured %d %s", rec.Code, rec.Body)
	}
}

func TestInvoiceCapture_RefusesBadRequestsAndForeignScope(t *testing.T) {
	rg := newCaptureRig(&domain.FakeExtractor{Result: scannedInvoice("0.9")})
	post := func(body string, hdr headers) int { return rg.call(http.MethodPost, capturesPath, body, hdr).Code }
	if got := post(captureRequest("UBL", "", `<Invoice><ID>1</ID></Invoice>`), duty("receiving.vendor-invoice.post", rg.key())); got != http.StatusUnprocessableEntity {
		t.Errorf("incomplete UBL: %d", got)
	}
	if got := post(captureRequest("PDF", "", "x"), duty("receiving.vendor-invoice.post", rg.key())); got != http.StatusBadRequest {
		t.Errorf("unknown format: %d", got)
	}
	if got := post(captureRequest("OCR", "text/html", "x"), duty("receiving.vendor-invoice.post", rg.key())); got != http.StatusBadRequest {
		t.Errorf("html media type: %d", got)
	}
	if got := post(captureRequest("UBL", "", ""), duty("receiving.vendor-invoice.post", rg.key())); got != http.StatusBadRequest {
		t.Errorf("empty content: %d", got)
	}
	if got := post(captureRequest("UBL", "", ublDocument), duty("receiving.receipt.maintain", rg.key())); got != http.StatusForbidden {
		t.Errorf("wrong duty: %d", got)
	}
	other := duty("receiving.vendor-invoice.post", rg.key(), "X-Passage-Legal-Entity", "22222222-2222-4222-8222-222222222222")
	if got := post(captureRequest("UBL", "", ublDocument), other); got != http.StatusForbidden {
		t.Errorf("foreign entity: %d", got)
	}
	created := decode[captureJSON](t, rg.call(http.MethodPost, capturesPath, captureRequest("UBL", "", ublDocument), duty("receiving.vendor-invoice.post", rg.key())), http.StatusCreated)
	foreign := duty("receiving.ap.inquire", "", "X-Passage-Legal-Entity", "22222222-2222-4222-8222-222222222222")
	if got := rg.call(http.MethodGet, capturesPath+"/"+created.ID, "", foreign).Code; got != http.StatusForbidden {
		t.Errorf("foreign read: %d", got)
	}
	if got := rg.call(http.MethodGet, capturesPath+"/not-a-uuid", "", duty("receiving.ap.inquire", "")).Code; got != http.StatusBadRequest {
		t.Errorf("bad id: %d", got)
	}
	if got := rg.call(http.MethodGet, capturesPath+"/33333333-3333-4333-8333-333333333333", "", duty("receiving.ap.inquire", "")).Code; got != http.StatusNotFound {
		t.Errorf("unknown id: %d", got)
	}
}

func TestInvoiceCapture_OutOfRangeNumbersAreRefusedOnBothPaths(t *testing.T) {
	for _, exp := range []string{"1e999999999", "1e-999999999", "1e50000000"} {
		doc := strings.Replace(ublDocument, "<TaxExclusiveAmount>25.00</TaxExclusiveAmount>", "<TaxExclusiveAmount>"+exp+"</TaxExclusiveAmount>", 1)
		rg := newCaptureRig(nil)
		rec := rg.call(http.MethodPost, capturesPath, captureRequest("UBL", "", doc), duty("receiving.vendor-invoice.post", rg.key()))
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), domain.CodeInvalidCapture) {
			t.Errorf("UBL %s: %d %s", exp, rec.Code, rec.Body)
		}
		big, _ := decimal.NewFromString(exp)
		bad := scannedInvoice("0.9")
		bad.Total = big
		ocr := newCaptureRig(&domain.FakeExtractor{Result: bad})
		rec = ocr.call(http.MethodPost, capturesPath, captureRequest("OCR", "image/png", "png"), duty("receiving.vendor-invoice.post", ocr.key()))
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), domain.CodeInvalidCapture) {
			t.Errorf("OCR %s: %d %s", exp, rec.Code, rec.Body)
		}
	}
}

func TestInvoiceCapture_RepeatedDocumentSkipsTheExtractor(t *testing.T) {
	ex := &domain.FakeExtractor{Result: scannedInvoice("0.9")}
	rg := newCaptureRig(ex)
	decode[captureJSON](t, rg.call(http.MethodPost, capturesPath, captureRequest("OCR", "image/png", "png"), duty("receiving.vendor-invoice.post", rg.key())), http.StatusCreated)
	decode[captureJSON](t, rg.call(http.MethodPost, capturesPath, captureRequest("OCR", "image/png", "png"), duty("receiving.vendor-invoice.post", rg.key())), http.StatusOK)
	if ex.Calls != 1 {
		t.Fatalf("extractor ran %d times", ex.Calls)
	}
}

func (rg *rig) captureBoth(t *testing.T) (ubl, scan captureJSON) {
	t.Helper()
	ubl = decode[captureJSON](t, rg.call(http.MethodPost, capturesPath, captureRequest("UBL", "", ublDocument), duty("receiving.vendor-invoice.post", rg.key())), http.StatusCreated)
	time.Sleep(2 * time.Millisecond)
	scan = decode[captureJSON](t, rg.call(http.MethodPost, capturesPath, captureRequest("OCR", "application/pdf", "%PDF-1.7 scan"), duty("receiving.vendor-invoice.post", rg.key())), http.StatusCreated)
	return ubl, scan
}

func TestInvoiceCaptureList_ShowsCapturesNewestFirstFiltersByStatusAndPages(t *testing.T) {
	rg := newCaptureRig(&domain.FakeExtractor{Result: scannedInvoice("0.55")})
	ubl, scan := rg.captureBoth(t)
	type page struct {
		Items      []captureJSON `json:"items"`
		NextCursor string        `json:"nextCursor"`
	}
	all := decode[page](t, rg.call(http.MethodGet, capturesPath, "", rg.inquirer()), http.StatusOK)
	if len(all.Items) != 2 || all.Items[0].ID != scan.ID || all.Items[1].ID != ubl.ID || all.Items[0].Status != "NEEDS_REVIEW" || len(all.Items[0].ReviewReasons) != 1 {
		t.Fatalf("all %+v, want the scan (needs review) before the UBL document", all)
	}
	review := decode[page](t, rg.call(http.MethodGet, capturesPath+"?status=NEEDS_REVIEW", "", rg.inquirer()), http.StatusOK)
	if len(review.Items) != 1 || review.Items[0].ID != scan.ID {
		t.Fatalf("needs review %+v", review)
	}
	first := decode[page](t, rg.call(http.MethodGet, capturesPath+"?limit=1", "", rg.inquirer()), http.StatusOK)
	if len(first.Items) != 1 || first.NextCursor == "" {
		t.Fatalf("first page %+v", first)
	}
	next := decode[page](t, rg.call(http.MethodGet, capturesPath+"?limit=1&cursor="+first.NextCursor, "", rg.inquirer()), http.StatusOK)
	if len(next.Items) != 1 || next.Items[0].ID != ubl.ID || next.NextCursor != "" {
		t.Fatalf("second page %+v", next)
	}
	empty := decode[page](t, rg.call(http.MethodGet, capturesPath, "", rg.inquirer(entityHeader, otherEntityID)), http.StatusOK)
	if len(empty.Items) != 0 {
		t.Fatalf("another legal entity sees %+v", empty)
	}
}

func TestInvoiceCaptureList_RefusesBadFiltersAndMissingDuty(t *testing.T) {
	rg := newCaptureRig(nil)
	for _, query := range []string{"?status=POSTED", "?limit=0", "?cursor=junk"} {
		if rec := rg.call(http.MethodGet, capturesPath+query, "", rg.inquirer()); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "COM-0001") {
			t.Fatalf("%s: got %d %s, want 400 COM-0001", query, rec.Code, rec.Body)
		}
	}
	if rec := rg.call(http.MethodGet, capturesPath, "", duty("receiving.vendor-invoice.post", "")); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "AUTH-0003") {
		t.Fatalf("without the inquire duty got %d %s", rec.Code, rec.Body)
	}
	if rec := doWith(rg.h, http.MethodGet, capturesPath, "", headers{}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("without a tenant got %d", rec.Code)
	}
}

func TestInvoiceCaptureList_DropsADocumentOnceItsInvoiceIsPosted(t *testing.T) {
	extracted := scannedInvoice("0.95")
	extracted.InvoiceNumber, extracted.InvoiceDate, extracted.Currency = "INV-1", time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC), "USD"
	rg := newCaptureRig(&domain.FakeExtractor{Result: extracted})
	decode[captureJSON](t, rg.call(http.MethodPost, capturesPath, captureRequest("OCR", "application/pdf", "%PDF-1.7 scan"), duty("receiving.vendor-invoice.post", rg.key())), http.StatusCreated)
	type page struct {
		Items []captureJSON `json:"items"`
	}
	if before := decode[page](t, rg.call(http.MethodGet, capturesPath, "", rg.inquirer()), http.StatusOK); len(before.Items) != 1 {
		t.Fatalf("before posting %+v", before)
	}
	rg.invoiced(t, "PS-1", "inv-1", 40, "2.5", "")
	if after := decode[page](t, rg.call(http.MethodGet, capturesPath, "", rg.inquirer()), http.StatusOK); len(after.Items) != 0 {
		t.Fatalf("after posting %+v, want the captured document gone", after)
	}
}
