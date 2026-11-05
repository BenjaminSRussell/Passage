package api_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/print/internal/domain"
)

// pdfBody builds a PDF job request for queue with the given base64 document.
func pdfBody(t *testing.T, queue, doc string) string {
	t.Helper()
	raw, err := json.Marshal(map[string]string{"format": "PDF", "queueCode": queue, "documentBase64": doc})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func pdfDoc(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func registerOffice(t *testing.T, h http.Handler, code string, rank int) {
	t.Helper()
	body := fmt.Sprintf(`{"code":%q,"name":"Office","deviceType":"PDF_IPP","dpi":300,"mediaSize":"LETTER","host":"10.0.7.%d","port":631,"queueCode":"OFFICE","failoverRank":%d}`, code, rank, rank)
	if rec := call(t, h, http.MethodPost, "/print/v1/printers", body, "print.admin", "office-"+code); rec.Code != http.StatusCreated {
		t.Fatalf("register %s: %d %s", code, rec.Code, rec.Body)
	}
}

func TestCreateJob_PDFDocumentPrintsOnPDFDevice(t *testing.T) {
	sender := &fakeSender{down: map[string]bool{}}
	h := router(newMemStore(), sender)
	seedDock(t, h) // ZPL printers on queue DOCK; must be skipped for PDF
	registerOffice(t, h, "OFFICE-1", 1)
	body := pdfBody(t, "OFFICE", pdfDoc("%PDF-1.4\n%%EOF"))
	rec := call(t, h, http.MethodPost, "/print/v1/jobs", body, "print.job.submit", "pdf-1")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	j := decodeJob(t, rec)
	if j.Status != domain.JobPrinted || j.PrinterCode != "OFFICE-1" || j.Format != domain.FormatPDF || j.TemplateCode != domain.PDFDocumentTemplate {
		t.Fatalf("job = %+v", j)
	}
	if strings.Contains(rec.Body.String(), "%PDF") || strings.Contains(rec.Body.String(), "JVBERi") {
		t.Fatal("document bytes leaked in the response body")
	}
	if len(sender.sent) != 1 || sender.sent[0] != "OFFICE-1" {
		t.Fatalf("sent %v, want only OFFICE-1", sender.sent)
	}
	if sender.payloads[0] != "%PDF-1.4\n%%EOF" {
		t.Fatalf("printer got %q, want the decoded document", sender.payloads[0])
	}
}

func TestCreateJob_PDFWithoutPDFDeviceFailsWithMOB0112(t *testing.T) {
	sender := &fakeSender{down: map[string]bool{}}
	h := router(newMemStore(), sender)
	seedDock(t, h) // only ZPL devices on the queue
	j := decodeJob(t, call(t, h, http.MethodPost, "/print/v1/jobs", pdfBody(t, "DOCK", pdfDoc("%PDF-1.4")), "print.job.submit", "pdf-2"))
	if j.Status != domain.JobFailed || j.LastError != domain.CodeNoPrinter || len(sender.sent) != 0 {
		t.Fatalf("job = %+v sent %v", j, sender.sent)
	}
}

func TestCreateJob_PDFFailsOverToPeer(t *testing.T) {
	sender := &fakeSender{down: map[string]bool{"OFFICE-1": true}}
	h := router(newMemStore(), sender)
	registerOffice(t, h, "OFFICE-1", 1)
	registerOffice(t, h, "OFFICE-2", 2)
	j := decodeJob(t, call(t, h, http.MethodPost, "/print/v1/jobs", pdfBody(t, "OFFICE", pdfDoc("%PDF-1.4")), "print.job.submit", "pdf-3"))
	if j.Status != domain.JobPrinted || j.PrinterCode != "OFFICE-2" || j.Attempts != 3 {
		t.Fatalf("failover job = %+v", j)
	}
}

func TestCreateJob_PDFValidation(t *testing.T) {
	h := router(newMemStore(), &fakeSender{down: map[string]bool{}})
	registerOffice(t, h, "OFFICE-1", 1)
	cases := []struct {
		name string
		body string
	}{
		{"not base64", `{"format":"PDF","queueCode":"OFFICE","documentBase64":"%%%"}`},
		{"not a pdf", `{"format":"PDF","queueCode":"OFFICE","documentBase64":"` + pdfDoc("hello") + `"}`},
		{"missing document", `{"format":"PDF","queueCode":"OFFICE"}`},
		{"template with pdf", `{"format":"PDF","queueCode":"OFFICE","templateCode":"LP_LABEL","documentBase64":"` + pdfDoc("%PDF-1.4") + `"}`},
		{"unknown format", `{"format":"PNG","queueCode":"OFFICE","documentBase64":"` + pdfDoc("%PDF-1.4") + `"}`},
		{"document without pdf format", `{"queueCode":"OFFICE","templateCode":"LP_LABEL","data":{},"documentBase64":"` + pdfDoc("%PDF-1.4") + `"}`},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if rec := call(t, h, http.MethodPost, "/print/v1/jobs", c.body, "print.job.submit", fmt.Sprintf("pdfv-%d", i)); rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400: %s", rec.Code, rec.Body)
			}
		})
	}
}

func TestCreateJob_ReprintOfPDFReusesDocument(t *testing.T) {
	sender := &fakeSender{down: map[string]bool{}}
	h := router(newMemStore(), sender)
	registerOffice(t, h, "OFFICE-1", 1)
	orig := decodeJob(t, call(t, h, http.MethodPost, "/print/v1/jobs", pdfBody(t, "OFFICE", pdfDoc("%PDF-1.4 coa")), "print.job.submit", "pdf-r1"))
	re := decodeJob(t, call(t, h, http.MethodPost, "/print/v1/jobs", fmt.Sprintf(`{"queueCode":"OFFICE","reprintOf":%q}`, orig.ID), "print.job.submit", "pdf-r2"))
	if re.Format != domain.FormatPDF || re.TemplateCode != domain.PDFDocumentTemplate || re.ReprintOf == nil || *re.ReprintOf != orig.ID {
		t.Fatalf("reprint = %+v", re)
	}
	if len(sender.sent) != 2 || sender.payloads[1] != "%PDF-1.4 coa" {
		t.Fatalf("reprint not sent with the original document: %v %q", sender.sent, sender.payloads)
	}
}

func TestCreateJob_PDFRefusesOversizeDocumentAndFormatOnReprint(t *testing.T) {
	sender := &fakeSender{down: map[string]bool{}}
	h := router(newMemStore(), sender)
	registerOffice(t, h, "OFFICE-1", 1)
	big := "%PDF-" + strings.Repeat("x", domain.MaxDocumentBytes)
	rec := call(t, h, http.MethodPost, "/print/v1/jobs", pdfBody(t, "OFFICE", pdfDoc(big)), "print.job.submit", "pdf-big")
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "documentBase64") {
		t.Fatalf("oversize document: status %d %s", rec.Code, rec.Body)
	}
	orig := decodeJob(t, call(t, h, http.MethodPost, "/print/v1/jobs", pdfBody(t, "OFFICE", pdfDoc("%PDF-1.4")), "print.job.submit", "pdf-o"))
	body := fmt.Sprintf(`{"format":"PDF","queueCode":"OFFICE","reprintOf":%q}`, orig.ID)
	if rec := call(t, h, http.MethodPost, "/print/v1/jobs", body, "print.job.submit", "pdf-o2"); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"format"`) {
		t.Fatalf("format on reprint: status %d %s", rec.Code, rec.Body)
	}
	if len(sender.sent) != 1 {
		t.Fatalf("refused requests reached a printer: %v", sender.sent)
	}
}

func TestCreateJob_ZPLLabelSkipsPDFDevices(t *testing.T) {
	sender := &fakeSender{down: map[string]bool{}}
	h := router(newMemStore(), sender)
	registerOffice(t, h, "OFFICE-1", 1)
	rec := call(t, h, http.MethodPost, "/print/v1/jobs", `{"templateCode":"LOCATION_LABEL","queueCode":"OFFICE","data":{"location":"A-01-02"}}`, "print.job.submit", "zpl-office")
	j := decodeJob(t, rec)
	if j.Format != domain.FormatZPL || j.Status != domain.JobFailed || j.LastError != domain.CodeNoPrinter || len(sender.sent) != 0 {
		t.Fatalf("label on a PDF-only queue: %+v sent %v", j, sender.sent)
	}
}

func TestPrinterRegistration_MediaSize(t *testing.T) {
	h := router(newMemStore(), &fakeSender{down: map[string]bool{}})
	bad := `{"code":"LBL-1","name":"Pack","deviceType":"ZPL_II","dpi":203,"mediaSize":"A4","host":"10.0.8.1","queueCode":"PACK","failoverRank":1}`
	if rec := call(t, h, http.MethodPost, "/print/v1/printers", bad, "print.admin", "m-bad"); rec.Code != http.StatusBadRequest {
		t.Fatalf("A4 on ZPL: status %d, want 400", rec.Code)
	}
	defaulted := `{"code":"LBL-2","name":"Pack","deviceType":"ZPL_II","dpi":203,"host":"10.0.8.2","queueCode":"PACK","failoverRank":1}`
	rec := call(t, h, http.MethodPost, "/print/v1/printers", defaulted, "print.admin", "m-default")
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"mediaSize":"4X6_IN"`) {
		t.Fatalf("default media: status %d body %s", rec.Code, rec.Body)
	}
	explicit := `{"code":"LBL-3","name":"Pack","deviceType":"ZPL_II","dpi":203,"mediaSize":"3X1_IN","host":"10.0.8.3","queueCode":"PACK","failoverRank":2}`
	if rec := call(t, h, http.MethodPost, "/print/v1/printers", explicit, "print.admin", "m-3x1"); rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"mediaSize":"3X1_IN"`) {
		t.Fatalf("explicit media: status %d body %s", rec.Code, rec.Body)
	}
}
