package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/eventing"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/revrec/internal/store"
)

const invoiceCustomer = "018f0000-0000-7000-8000-0000000000c1"

type invoiceLine struct {
	order, product, net string
}

func invoicePosted(t *testing.T, date string, lines ...invoiceLine) events.Event {
	t.Helper()
	type line struct {
		LineNo  int    `json:"line_no"`
		Net     string `json:"net"`
		OrderID string `json:"order_id,omitempty"`
		Product string `json:"product,omitempty"`
	}
	var out []line
	for i, l := range lines {
		out = append(out, line{LineNo: i + 1, Net: l.net, OrderID: l.order, Product: l.product})
	}
	id := ids.New()
	evt, err := events.New(InvoicePostedType, BillingSource, id, testkit.TenantID(1), map[string]any{
		"invoice_id": id, "number": "INV-2027-0000001", "type": "SHIPMENT", "legal_entity_id": entityID, "customer": invoiceCustomer,
		"currency": "USD", "posting_date": date, "lines": out})
	if err != nil {
		t.Fatal(err)
	}
	return evt
}

func (g *ledgerRig) consumeInvoice(t *testing.T, evt events.Event) error {
	t.Helper()
	actor, err := tenant.ServicePrincipal(CreditNoteConsumerName, evt.TenantID, "")
	if err != nil {
		t.Fatal(err)
	}
	return InvoiceConsumerHandlers(g.st)[evt.Type].Handle(tenant.With(context.Background(), actor), nil, evt)
}

func (g *ledgerRig) contractsOf(t *testing.T, order string) []store.Contract {
	t.Helper()
	actor, _ := tenant.ServicePrincipal(CreditNoteConsumerName, testkit.TenantID(1), entityID)
	page, err := g.st.ListContracts(tenant.With(context.Background(), actor), store.ContractFilter{LegalEntityID: entityID, OrderID: order, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	out := make([]store.Contract, len(page.Items))
	for i, h := range page.Items {
		if out[i], err = g.st.GetContract(tenant.With(context.Background(), actor), h.ID); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func TestInvoiceConsumer_OpensOneContractPerOrderFromTheInvoicedLines(t *testing.T) {
	g := newLedgerRig(t)
	if rec := call(t, g.handler, http.MethodPut, "/v1/revrec/recognition-templates/"+bundleSupport, `{"method":"RATABLE","termMonths":12}`, nil); rec.Code != http.StatusOK {
		t.Fatalf("put template: %d %s", rec.Code, rec.Body)
	}
	evt := invoicePosted(t, "2027-03-01",
		invoiceLine{bundleOrder, bundleHardware, "8000.00"}, invoiceLine{bundleOrder, bundleSupport, "1200.00"}, invoiceLine{bundleOrder, bundleSupport, "1200.00"},
		invoiceLine{"", "", "50.00"})
	if err := g.consumeInvoice(t, evt); err != nil {
		t.Fatalf("consume: %v", err)
	}
	cs := g.contractsOf(t, bundleOrder)
	if len(cs) != 1 {
		t.Fatalf("contracts: %+v", cs)
	}
	c := cs[0]
	if !c.TransactionPrice.Equal(decimal.RequireFromString("10400.00")) || c.CustomerID != invoiceCustomer || c.ContractDate.Format("2006-01-02") != "2027-03-01" || len(c.Obligations) != 2 {
		t.Fatalf("contract: %+v", c)
	}
	got := map[string]store.Obligation{}
	for _, o := range c.Obligations {
		got[o.ProductID] = o
	}
	hw, sup := got[bundleHardware], got[bundleSupport]
	if hw.Method != "POINT_IN_TIME" || !hw.Allocated.Equal(decimal.RequireFromString("8000.00")) || hw.SatisfiedOn == nil {
		t.Fatalf("hardware obligation: %+v", hw)
	}
	if sup.Method != "RATABLE" || !sup.Allocated.Equal(decimal.RequireFromString("2400.00")) || sup.TermStart == nil || sup.TermEnd == nil ||
		sup.TermEnd.Format("2006-01-02") != "2028-02-29" || len(sup.Schedule) == 0 {
		t.Fatalf("support obligation: %+v", sup)
	}
	// A redelivery and a later invoice of the same order open nothing more.
	if err := g.consumeInvoice(t, evt); err != nil {
		t.Fatalf("redelivery: %v", err)
	}
	if err := g.consumeInvoice(t, invoicePosted(t, "2027-04-01", invoiceLine{bundleOrder, bundleInstall, "500.00"})); err != nil {
		t.Fatalf("second invoice: %v", err)
	}
	if cs := g.contractsOf(t, bundleOrder); len(cs) != 1 || len(cs[0].Obligations) != 2 {
		t.Fatalf("contracts after replay: %+v", cs)
	}
}

func TestInvoiceConsumer_LeavesAnOperatorContractAlone(t *testing.T) {
	g := newLedgerRig(t)
	body := `{"legalEntityId":"` + entityID + `","orderId":"` + bundleOrder + `","customerId":"` + invoiceCustomer + `","currency":"USD","transactionPrice":"9000.00","contractDate":"2027-03-01",
		"obligations":[{"productId":"` + bundleHardware + `","ssp":"9000.00","sspSource":"EXPLICIT","method":"POINT_IN_TIME","satisfiedOn":"2027-03-01"}]}`
	if rec := call(t, g.handler, http.MethodPost, "/v1/revrec/contracts", body, map[string]string{"Idempotency-Key": "op-1"}); rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	if err := g.consumeInvoice(t, invoicePosted(t, "2027-03-01", invoiceLine{bundleOrder, bundleHardware, "8000.00"})); err != nil {
		t.Fatalf("consume: %v", err)
	}
	if cs := g.contractsOf(t, bundleOrder); len(cs) != 1 || !cs[0].TransactionPrice.Equal(decimal.RequireFromString("9000.00")) {
		t.Fatalf("operator contract changed: %+v", cs)
	}
}

func TestInvoiceConsumer_RefusesAPrincipalWithoutItsDutyAndSkipsOrderlessInvoices(t *testing.T) {
	g := newLedgerRig(t)
	evt := invoicePosted(t, "2027-03-01", invoiceLine{bundleOrder, bundleHardware, "8000.00"})
	plain := tenant.With(context.Background(), tenant.Principal{TenantID: evt.TenantID, LegalEntityID: entityID, UserID: "someone"})
	err := InvoiceConsumerHandlers(g.st)[evt.Type].Handle(plain, nil, evt)
	if err == nil || !eventing.IsPermanent(err) {
		t.Fatalf("want a permanent refusal, got %v", err)
	}
	if err := g.consumeInvoice(t, invoicePosted(t, "2027-03-01", invoiceLine{"", "", "50.00"})); err != nil {
		t.Fatalf("free-text invoice: %v", err)
	}
	if cs := g.contractsOf(t, bundleOrder); len(cs) != 0 {
		t.Fatalf("contracts: %+v", cs)
	}
}
