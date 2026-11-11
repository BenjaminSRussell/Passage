package api

import (
	"context"
	"net/http"
	"strconv"
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
	// A redelivery, and a later invoice that bills a product the contract has, open nothing more.
	if err := g.consumeInvoice(t, evt); err != nil {
		t.Fatalf("redelivery: %v", err)
	}
	if err := g.consumeInvoice(t, invoicePosted(t, "2027-04-01", invoiceLine{bundleOrder, bundleHardware, "500.00"})); err != nil {
		t.Fatalf("second invoice: %v", err)
	}
	if cs := g.contractsOf(t, bundleOrder); len(cs) != 1 || len(cs[0].Obligations) != 2 || !cs[0].TransactionPrice.Equal(decimal.RequireFromString("10400.00")) {
		t.Fatalf("contracts after replay: %+v", cs)
	}
}

// #29: a product first invoiced on a later invoice becomes a new obligation at its invoiced net (a separate contract, ASC 606-10-25-12):
// the price grows by it, the obligations and schedules already there are untouched, its template sets how it is recognized, and
// redelivering the invoice adds nothing twice.
func TestInvoiceConsumer_AProductFirstInvoicedLaterIsAddedToTheContract(t *testing.T) {
	g := newLedgerRig(t)
	if rec := call(t, g.handler, http.MethodPut, "/v1/revrec/recognition-templates/"+bundleInstall, `{"method":"MILESTONE"}`, nil); rec.Code != http.StatusOK {
		t.Fatalf("put template: %d %s", rec.Code, rec.Body)
	}
	if err := g.consumeInvoice(t, invoicePosted(t, "2027-03-01", invoiceLine{bundleOrder, bundleHardware, "8000.00"})); err != nil {
		t.Fatal(err)
	}
	before := g.contractsOf(t, bundleOrder)[0]
	if before.Status != "COMPLETE" {
		t.Fatalf("a contract with only recognized revenue is %s, want COMPLETE", before.Status)
	}
	later := invoicePosted(t, "2027-04-01", invoiceLine{bundleOrder, bundleHardware, "100.00"}, invoiceLine{bundleOrder, bundleInstall, "1350.00"})
	for range 2 {
		if err := g.consumeInvoice(t, later); err != nil {
			t.Fatalf("later invoice: %v", err)
		}
	}
	cs := g.contractsOf(t, bundleOrder)
	if len(cs) != 1 {
		t.Fatalf("contracts: %+v", cs)
	}
	c := cs[0]
	if len(c.Obligations) != 2 || !c.TransactionPrice.Equal(decimal.RequireFromString("9350.00")) || c.Status != "OPEN" || c.Version != before.Version+1 {
		t.Fatalf("modified contract: price %s status %s version %d obligations %d", c.TransactionPrice, c.Status, c.Version, len(c.Obligations))
	}
	var installed, hardware store.Obligation
	for _, o := range c.Obligations {
		if o.ProductID == bundleInstall {
			installed = o
		} else {
			hardware = o
		}
	}
	if installed.LineNo != 2 || installed.Method != "MILESTONE" || !installed.Allocated.Equal(decimal.RequireFromString("1350.00")) ||
		len(installed.Schedule) != 1 || installed.Schedule[0].Status != "SCHEDULED" {
		t.Fatalf("added obligation: %+v", installed)
	}
	if !hardware.Allocated.Equal(decimal.RequireFromString("8000.00")) || len(hardware.Schedule) != 1 || hardware.Schedule[0].Status != "RECOGNIZED" {
		t.Fatalf("the obligation that was there changed: %+v", hardware)
	}
}

// #29: a USAGE template gives an obligation with no schedule that keeps the contract open.
func TestInvoiceConsumer_UsageTemplateOpensAnObligationWithoutSchedule(t *testing.T) {
	g := newLedgerRig(t)
	if rec := call(t, g.handler, http.MethodPut, "/v1/revrec/recognition-templates/"+bundleSupport, `{"method":"USAGE"}`, nil); rec.Code != http.StatusOK {
		t.Fatalf("put template: %d %s", rec.Code, rec.Body)
	}
	if rec := call(t, g.handler, http.MethodPut, "/v1/revrec/recognition-templates/"+bundleInstall, `{"method":"USAGE","termMonths":3}`, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("a USAGE template with a term: %d", rec.Code)
	}
	if err := g.consumeInvoice(t, invoicePosted(t, "2027-03-01", invoiceLine{bundleOrder, bundleSupport, "900.00"})); err != nil {
		t.Fatal(err)
	}
	c := g.contractsOf(t, bundleOrder)[0]
	if c.Status != "OPEN" || len(c.Obligations) != 1 || c.Obligations[0].Method != "USAGE" || len(c.Obligations[0].Schedule) != 0 {
		t.Fatalf("usage contract: %+v", c)
	}
}

func TestAllocateRouteAddsObligationsAndRefusesDuplicatesAndCancelled(t *testing.T) {
	g := newLedgerRig(t)
	if err := g.consumeInvoice(t, invoicePosted(t, "2027-03-01", invoiceLine{bundleOrder, bundleHardware, "8000.00"})); err != nil {
		t.Fatal(err)
	}
	c := g.contractsOf(t, bundleOrder)[0]
	path := "/v1/revrec/contracts/" + c.ID + ":allocate"
	body := func(product, price, ssp string) string {
		return `{"transactionPriceIncrease":"` + price + `","obligations":[{"productId":"` + product + `","ssp":"` + ssp + `","sspSource":"EXPLICIT","method":"RATABLE","termStart":"2027-04-01","termEnd":"2028-03-31"}]}`
	}
	if rec := call(t, g.handler, http.MethodPost, path, body(bundleSupport, "1200.00", "1200.00"), map[string]string{"If-Match": `"99"`}); rec.Code != http.StatusPreconditionFailed {
		t.Errorf("a stale version: %d %s", rec.Code, rec.Body)
	}
	if rec := call(t, g.handler, http.MethodPost, path, body(bundleHardware, "10.00", "10.00"), nil); rec.Code != http.StatusConflict {
		t.Errorf("a product the contract has: %d %s", rec.Code, rec.Body)
	}
	if rec := call(t, g.handler, http.MethodPost, path, `{"transactionPriceIncrease":"x","obligations":[]}`, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("a bad price: %d", rec.Code)
	}
	rec := call(t, g.handler, http.MethodPost, path, body(bundleSupport, "1200.00", "1200.00"), map[string]string{"If-Match": `"` + strconv.Itoa(c.Version) + `"`})
	if rec.Code != http.StatusOK {
		t.Fatalf("allocate: %d %s", rec.Code, rec.Body)
	}
	got := g.contractsOf(t, bundleOrder)[0]
	if len(got.Obligations) != 2 || !got.TransactionPrice.Equal(decimal.RequireFromString("9200.00")) || got.Status != "OPEN" {
		t.Fatalf("contract after :allocate: %+v", got)
	}
	var support store.Obligation
	for _, o := range got.Obligations {
		if o.ProductID == bundleSupport {
			support = o
		}
	}
	sum := decimal.Zero
	for _, s := range support.Schedule {
		sum = sum.Add(s.Amount)
	}
	if !support.Allocated.Equal(decimal.RequireFromString("1200.00")) || !sum.Equal(support.Allocated) || len(support.Schedule) != 12 {
		t.Fatalf("support obligation: allocated %s over %d lines summing %s", support.Allocated, len(support.Schedule), sum)
	}
	var queued int
	if err := g.admin.QueryRow(context.Background(), `select count(*) from outbox where aggregate_id = $1`, c.ID).Scan(&queued); err != nil || queued < 2 {
		t.Errorf("contract.allocated events for the contract = %d (%v), want one at creation and one for the modification", queued, err)
	}
	if rec := call(t, g.handler, http.MethodPost, "/v1/revrec/contracts/"+ids.New()+":allocate", body(bundleSupport, "1.00", "1.00"), nil); rec.Code != http.StatusNotFound {
		t.Errorf("an unknown contract: %d", rec.Code)
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

// racingStore runs another invoice of the same order to completion just before the first AddObligations call, as a second consumer
// would when the two invoices are consumed at the same time.
type racingStore struct {
	InvoiceContractStore
	race func()
	done bool
}

func (r *racingStore) AddObligations(ctx context.Context, contractID string, increase decimal.Decimal, add []store.Obligation, expectedVersion int) (store.Contract, error) {
	if !r.done {
		r.done = true
		r.race()
	}
	return r.InvoiceContractStore.AddObligations(ctx, contractID, increase, add, expectedVersion)
}

// Two invoices of one order that both bill a product the contract lacks: the one that loses the race reads the contract again and
// still adds its own new product, instead of failing with REV-0105 and being dead-lettered.
func TestInvoiceConsumer_ConcurrentInvoicesOfOneOrderBothAddTheirProducts(t *testing.T) {
	g := newLedgerRig(t)
	if err := g.consumeInvoice(t, invoicePosted(t, "2027-03-01", invoiceLine{bundleOrder, bundleHardware, "8000.00"})); err != nil {
		t.Fatal(err)
	}
	other := invoicePosted(t, "2027-04-01", invoiceLine{bundleOrder, bundleInstall, "1350.00"})
	mine := invoicePosted(t, "2027-04-01", invoiceLine{bundleOrder, bundleInstall, "1350.00"}, invoiceLine{bundleOrder, bundleSupport, "1200.00"})
	actor, err := tenant.ServicePrincipal(CreditNoteConsumerName, mine.TenantID, "")
	if err != nil {
		t.Fatal(err)
	}
	ctx := tenant.With(context.Background(), actor)
	racing := &racingStore{InvoiceContractStore: g.st}
	racing.race = func() {
		if err := InvoiceConsumerHandlers(g.st)[other.Type].Handle(ctx, nil, other); err != nil {
			t.Errorf("other invoice: %v", err)
		}
	}
	if err := InvoiceConsumerHandlers(racing)[mine.Type].Handle(ctx, nil, mine); err != nil {
		t.Fatalf("invoice that lost the race: %v", err)
	}
	cs := g.contractsOf(t, bundleOrder)
	if len(cs) != 1 || len(cs[0].Obligations) != 3 || !cs[0].TransactionPrice.Equal(decimal.RequireFromString("10550.00")) {
		t.Fatalf("contract after the race: %+v", cs)
	}
}
