package api

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/BenjaminSRussell/passage/services/edi/internal/domain"
	"github.com/BenjaminSRussell/passage/services/edi/internal/sources"
)

// exPayables is payables' vendor-invoice intake.
type exPayables struct {
	mu        sync.Mutex
	got       []sources.VendorInvoiceRequest
	keys      []string
	refuse    *sources.DownstreamRefusedError
	principal string
}

func (p *exPayables) ReceiveVendorInvoice(ctx context.Context, _, key string, body sources.VendorInvoiceRequest) (sources.VendorInvoiceRef, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	d := &exDownstream{}
	d.note(ctx)
	p.principal = d.principal
	if p.refuse != nil {
		return sources.VendorInvoiceRef{}, p.refuse
	}
	p.got, p.keys = append(p.got, body), append(p.keys, key)
	return sources.VendorInvoiceRef{ID: "00000000-0000-0000-0000-0000000000b7", Status: "RECEIVED"}, nil
}

// #35 (35-4): an inbound InvoiceDetailRequest of one purchase order lands in payables' intake for the order's vendor, as edi-cxml.
func TestCXMLInbound_InvoiceLandsInPayablesIntake(t *testing.T) {
	h, pub, pool, _, path, down, wire := exInbound(t, true)
	if code, body := postCXML(pub, path, sample(t, "invoice-detail.xml")); code != http.StatusOK {
		t.Fatalf("receipt: %d %s", code, body)
	}
	pay := &exPayables{}
	down.payables = pay
	wire()
	id := messageID(t, pool, "InvoiceDetailRequest")
	rec := do(t, h, http.MethodPost, "/edi/v1/messages/"+id+":post", "", "edi.operate", srcScope)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"VENDOR_INVOICE"`) {
		t.Fatalf("post: %d %s", rec.Code, rec.Body.String())
	}
	if len(pay.got) != 1 || pay.keys[0] != id || pay.principal != "service:edi-cxml" {
		t.Fatalf("payables got %d calls, key %v, as %q", len(pay.got), pay.keys, pay.principal)
	}
	got := pay.got[0]
	if got.VendorID != exVendor || got.PurchaseOrderID != exPO || got.PurchaseOrderNumber != "PO-1001" || got.InvoiceNumber != "DIST-INV-7001" ||
		got.InvoiceDate != "2026-11-05" || got.Currency != "USD" || got.Gross != "86.80" || got.SourceRef != id || got.Source != "CXML" || got.LegalEntityID != srcLE ||
		len(got.Lines) != 1 || got.Lines[0].Quantity != "10" || got.Lines[0].UnitPrice != "8" {
		t.Fatalf("intake request %+v", got)
	}
	// Posting again is a replay: payables is not called twice.
	if rec := do(t, h, http.MethodPost, "/edi/v1/messages/"+id+":post", "", "edi.operate", srcScope); rec.Code != http.StatusOK || len(pay.got) != 1 {
		t.Fatalf("repeat: %d %d", rec.Code, len(pay.got))
	}
}

func TestCXMLInbound_InvoiceRefusedByPayablesIsParked(t *testing.T) {
	h, pub, pool, _, path, down, wire := exInbound(t, true)
	postCXML(pub, path, sample(t, "invoice-detail.xml"))
	pay := &exPayables{refuse: &sources.DownstreamRefusedError{Service: "payables", Status: 409, Code: "AP-VII-002", Detail: "already received"}}
	down.payables = pay
	wire()
	id := messageID(t, pool, "InvoiceDetailRequest")
	rec := do(t, h, http.MethodPost, "/edi/v1/messages/"+id+":post", "", "edi.operate", srcScope)
	if rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != domain.CodeCXMLDownstream || !strings.Contains(rec.Body.String(), "AP-VII-002") {
		t.Fatalf("refused: %d %s", rec.Code, rec.Body.String())
	}
	// An invoice of two orders is not put into payables.
	h, pub, pool, _, path, down, wire = exInbound(t, true)
	two := strings.Replace(sample(t, "invoice-detail.xml"), `</InvoiceDetailOrder>`, `</InvoiceDetailOrder><InvoiceDetailOrder><InvoiceDetailOrderInfo><OrderReference orderID="PO-1002"/></InvoiceDetailOrderInfo>
		<InvoiceDetailItem invoiceLineNumber="2" quantity="1"><UnitOfMeasure>EA</UnitOfMeasure><UnitPrice><Money currency="USD">1.00</Money></UnitPrice></InvoiceDetailItem></InvoiceDetailOrder>`, 1)
	postCXML(pub, path, two)
	pay = &exPayables{}
	down.payables = pay
	wire()
	rec = do(t, h, http.MethodPost, "/edi/v1/messages/"+messageID(t, pool, "InvoiceDetailRequest")+":post", "", "edi.operate", srcScope)
	if rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != domain.CodeCXMLInvoiceTarget || len(pay.got) != 0 {
		t.Fatalf("two orders: %d %s", rec.Code, rec.Body.String())
	}
}

func TestPlanInvoice(t *testing.T) {
	base := domain.CXMLCanonical{DocType: "InvoiceDetailRequest", Number: "INV-1", Date: "2026-11-05T00:00:00-05:00", Currency: "USD", OrderIDs: []string{"PO-1"},
		Lines: []domain.CXMLLine{{Line: "1", Quantity: "2", UnitPrice: "3.50"}}}
	inv, park := domain.PlanInvoice(base)
	if park != nil || inv.Date != "2026-11-05" || inv.Purpose != "standard" || inv.Lines[0].UnitPrice != "3.5" {
		t.Fatalf("plan %+v %+v", inv, park)
	}
	for name, mut := range map[string]func(*domain.CXMLCanonical){
		"no order":      func(c *domain.CXMLCanonical) { c.OrderIDs = nil },
		"bad date":      func(c *domain.CXMLCanonical) { c.Date = "05/11/2026" },
		"no currency":   func(c *domain.CXMLCanonical) { c.Currency = "" },
		"zero quantity": func(c *domain.CXMLCanonical) { c.Lines = []domain.CXMLLine{{Line: "1", Quantity: "0", UnitPrice: "1"}} },
		"no price":      func(c *domain.CXMLCanonical) { c.Lines = []domain.CXMLLine{{Line: "1", Quantity: "1"}} },
		"odd purpose":   func(c *domain.CXMLCanonical) { c.Purpose = "refund" },
	} {
		c := base
		c.Lines = append([]domain.CXMLLine(nil), base.Lines...)
		mut(&c)
		if _, park := domain.PlanInvoice(c); park == nil {
			t.Errorf("%s: not parked", name)
		}
	}
}
