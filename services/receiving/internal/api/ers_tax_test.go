package api_test

import (
	"bytes"
	"net/http"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/receiving/internal/api"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store/memstore"
)

func TestErs_DebitsInputTaxIssuesSelfBillAndRecordsTheTransaction(t *testing.T) {
	tax := domain.FakeTaxCalculator{Result: domain.TaxResult{Code: "VAT19", Amount: decimal.RequireFromString("1.98")}}
	recorder := &domain.FakeTaxRecorder{}
	rg := newRigPorts(memstore.New(), domain.PeriodOpen, func(p *api.Ports) {
		p.PurchaseOrders = domain.FakePurchaseOrders{Orders: map[string]domain.PurchaseOrder{poID: ersOrder("2.60", nil)}}
		p.AuditSampler = testSampler("seed-one-0123456789")
		p.Tax, p.TaxRecorder = tax, recorder
	})
	rg.confirmErsPrice = "2.60"
	rg.enableErs(t, true)
	r := rg.posted(t, "PS-TAX", 10, 4)
	run := rg.ersRun(t, http.StatusOK)
	if len(run.Settled) != 1 || run.Settled[0].Total != "12.38" || run.Settled[0].ReceiptID != r.ID {
		t.Fatalf("settled %+v", run)
	}
	entry := lastEntry(t, rg)
	wantLines(t, entry, "2100=10", "1400=0.4", "1570=1.98", "2000=-12.38")
	if got := rg.ledger.Balance("1570"); !got.Equal(decimal.RequireFromString("1.98")) {
		t.Fatalf("input tax %s", got)
	}
	if len(recorder.Txns) != 1 || recorder.Txns[0].Code != "VAT19" || !recorder.Txns[0].Amount.Equal(decimal.RequireFromString("1.98")) {
		t.Fatalf("recorded %+v", recorder.Txns)
	}
	doc := rg.call(http.MethodGet, "/v1/receiving/ers-self-bills/"+run.Settled[0].InvoiceID, "", duty("APInquire", "", "X-Passage-User", "alice"))
	if doc.Code != http.StatusOK || !bytes.Contains(doc.Body.Bytes(), []byte("VAT19")) || !bytes.Contains(doc.Body.Bytes(), []byte("12.38")) {
		t.Fatalf("self-bill %d %s", doc.Code, doc.Body.String())
	}
}
