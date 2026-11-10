package store

import (
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
)

type taxEnv struct {
	*rmaEnv
	productB string
}

func newTaxEnv(t *testing.T) *taxEnv {
	t.Helper()
	e := &taxEnv{rmaEnv: newRMAEnv(t), productB: ids.New()}
	e.src.invoice.Lines = []InvoiceLine{
		{LineNo: 1, ProductID: e.product, OrderLineID: ids.New(), Qty: d(10), Net: dec("1000.00"), TaxCode: "DE19", Tax: dec("190.00")},
		{LineNo: 2, ProductID: e.productB, OrderLineID: ids.New(), Qty: d(3), Net: dec("99.99"), TaxCode: "DE7", Tax: dec("7.00")},
	}
	return e
}

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func (e *taxEnv) creditOnly(t *testing.T, qtyA, qtyB int64) ReturnOrder {
	t.Helper()
	in := NewReturnOrder{LegalEntityID: e.le, CustomerID: e.customer, OriginalInvoiceID: e.src.invoice.ID, ReturnReasonCode: "DAMAGED",
		IdempotencyKey: ids.New(), RequestHash: ids.New(), Actor: e.createdBy}
	var received []ReceiveLine
	var inspected []InspectLine
	for _, l := range []struct {
		product string
		qty     int64
	}{{e.product, qtyA}, {e.productB, qtyB}} {
		if l.qty == 0 {
			continue
		}
		no := len(in.Lines) + 1
		in.Lines = append(in.Lines, NewReturnLine{LineNo: no, ProductID: l.product, Quantity: d(l.qty), Unit: "EA", Disposition: domain.DispositionCreditOnly})
		received = append(received, ReceiveLine{LineNo: no, Quantity: d(l.qty)})
		inspected = append(inspected, InspectLine{LineNo: no, Accepted: d(l.qty), Disposition: domain.DispositionCreditOnly})
	}
	rma := e.create(t, in)
	auth, err := e.s.AuthorizeReturnOrder(e.ctx, rma.ID, rma.Version, e.authorizer, e.now)
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	recv, err := e.s.ReceiveReturnOrder(e.ctx, auth.ID, auth.Version, received, "wh-clerk", e.now)
	if err != nil {
		t.Fatalf("receive: %v", err)
	}
	insp, err := e.s.InspectReturnOrder(e.ctx, recv.ID, recv.Version, inspected, "qc", e.now)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	disp, err := e.s.DispositionReturnOrder(e.ctx, insp.ID, insp.Version, "returns", e.now)
	if err != nil {
		t.Fatalf("disposition: %v", err)
	}
	credited, err := e.s.CreditReturnOrder(e.ctx, disp.ID, disp.Version, "ar-clerk", e.now)
	if err != nil {
		t.Fatalf("credit: %v", err)
	}
	return credited
}

func taxByCode(c ARCredit) map[string]string {
	out := map[string]string{}
	for _, l := range c.TaxLines {
		out[l.Code] = l.Amount.StringFixed(2)
	}
	return out
}

func TestRMA_CreditReversesTheStoredInvoiceTaxPerCodeAndSendsItToAR(t *testing.T) {
	e := newTaxEnv(t)
	credited := e.creditOnly(t, 3, 1)
	if !credited.Gross.Equal(dec("333.33")) || !credited.Net.Equal(dec("333.33")) || !credited.Tax.Equal(dec("59.33")) {
		t.Fatalf("3 x 100.00 + 1 x 33.33 with 57.00 and 2.33 tax: %+v", credited)
	}
	if len(e.ar.credits) != 1 {
		t.Fatalf("credits sent: %+v", e.ar.credits)
	}
	got := taxByCode(e.ar.credits[0])
	if len(got) != 2 || got["DE19"] != "57.00" || got["DE7"] != "2.33" {
		t.Fatalf("tax lines sent to AR: %+v", e.ar.credits[0].TaxLines)
	}
}

func TestRMA_SuccessivePartialReturnsReverseExactlyTheBilledTax(t *testing.T) {
	e := newTaxEnv(t)
	wantA := []string{"57.00", "57.00", "76.00"}
	wantB := []string{"2.33", "2.34", "2.33"}
	sumA, sumB := decimal.Zero, decimal.Zero
	for i, qtyA := range []int64{3, 3, 4} {
		e.creditOnly(t, qtyA, 1)
		got := taxByCode(e.ar.credits[i])
		if got["DE19"] != wantA[i] || got["DE7"] != wantB[i] {
			t.Fatalf("return %d: want %s / %s got %+v", i+1, wantA[i], wantB[i], e.ar.credits[i].TaxLines)
		}
		sumA, sumB = sumA.Add(dec(got["DE19"])), sumB.Add(dec(got["DE7"]))
	}
	if !sumA.Equal(dec("190.00")) || !sumB.Equal(dec("7.00")) {
		t.Fatalf("totals reversed %s and %s of 190.00 and 7.00", sumA, sumB)
	}
}

func TestRMA_TaxAlreadyCreditedByBillingIsNotReversedAgain(t *testing.T) {
	e := newTaxEnv(t)
	e.src.invoice.Lines[0].CreditedQty, e.src.invoice.Lines[0].CreditedTax = d(4), dec("76.00")
	e.creditOnly(t, 6, 0)
	if got := taxByCode(e.ar.credits[0]); len(got) != 1 || got["DE19"] != "114.00" {
		t.Fatalf("6 left of 10 after 76.00 credited: %+v", e.ar.credits[0].TaxLines)
	}
}

func TestRMA_CreditRetryResendsTheStoredTaxWithoutAllocatingAgain(t *testing.T) {
	e := newTaxEnv(t)
	e.ar.failNext = true
	in := e.creditOnlyUntilCredit(t, 3, 0)
	if _, err := e.s.CreditReturnOrder(e.ctx, in.ID, in.Version, "ar-clerk", e.now); err == nil {
		t.Fatal("receivables is down")
	}
	stuck, err := e.s.GetReturnOrder(e.ctx, in.ID)
	if err != nil || stuck.Status != domain.RMACredited || !stuck.Tax.Equal(dec("57.00")) {
		t.Fatalf("credited and stored, not closed: %+v %v", stuck, err)
	}
	if _, err := e.s.CreditReturnOrder(e.ctx, stuck.ID, stuck.Version, "ar-clerk", e.now); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if len(e.ar.credits) != 1 || taxByCode(e.ar.credits[0])["DE19"] != "57.00" {
		t.Fatalf("retry sends the stored tax: %+v", e.ar.credits)
	}
	e.creditOnly(t, 7, 0)
	if got := taxByCode(e.ar.credits[1]); got["DE19"] != "133.00" {
		t.Fatalf("the retry did not take the line twice: %+v", e.ar.credits[1].TaxLines)
	}
}

func TestRMA_UntaxedInvoiceSendsNoTaxLines(t *testing.T) {
	e := newRMAEnv(t)
	rma := e.create(t, e.newOrder(domain.DispositionCreditOnly, 2))
	insp := e.toInspected(t, rma, 2, 2, domain.DispositionCreditOnly)
	disp, err := e.s.DispositionReturnOrder(e.ctx, insp.ID, insp.Version, "returns", e.now)
	if err != nil {
		t.Fatalf("disposition: %v", err)
	}
	credited, err := e.s.CreditReturnOrder(e.ctx, disp.ID, disp.Version, "ar-clerk", e.now)
	if err != nil || !credited.Tax.IsZero() || len(e.ar.credits[0].TaxLines) != 0 {
		t.Fatalf("no tax on the invoice: %+v %v %+v", credited, err, e.ar.credits)
	}
}

func TestRMA_CreditNeedsBillingToReadTheInvoiceTax(t *testing.T) {
	e := newTaxEnv(t)
	in := e.creditOnlyUntilCredit(t, 3, 0)
	e.s.WithReturnSources(nil, e.src)
	if _, err := e.s.CreditReturnOrder(e.ctx, in.ID, in.Version, "ar-clerk", e.now); stateCode(t, err) != CodeRMAUpstream {
		t.Fatalf("no billing: %v", err)
	}
	if again, err := e.s.GetReturnOrder(e.ctx, in.ID); err != nil || again.Status != domain.RMADispositioned {
		t.Fatalf("the return keeps its state: %+v %v", again, err)
	}
}

func (e *taxEnv) creditOnlyUntilCredit(t *testing.T, qtyA, qtyB int64) ReturnOrder {
	t.Helper()
	in := NewReturnOrder{LegalEntityID: e.le, CustomerID: e.customer, OriginalInvoiceID: e.src.invoice.ID, ReturnReasonCode: "DAMAGED",
		IdempotencyKey: ids.New(), RequestHash: ids.New(), Actor: e.createdBy,
		Lines: []NewReturnLine{{LineNo: 1, ProductID: e.product, Quantity: d(qtyA), Unit: "EA", Disposition: domain.DispositionCreditOnly}}}
	rma := e.create(t, in)
	insp := e.toInspected(t, rma, qtyA, qtyA, domain.DispositionCreditOnly)
	disp, err := e.s.DispositionReturnOrder(e.ctx, insp.ID, insp.Version, "returns", e.now)
	if err != nil {
		t.Fatalf("disposition: %v", err)
	}
	return disp
}

func TestRMA_TaxedInvoiceLineWithoutATaxCodeIsRefusedNotDropped(t *testing.T) {
	e := newTaxEnv(t)
	e.src.invoice.Lines[0].TaxCode = ""
	in := e.creditOnlyUntilCredit(t, 3, 0)
	if _, err := e.s.CreditReturnOrder(e.ctx, in.ID, in.Version, "ar-clerk", e.now); stateCode(t, err) != CodeRMAInvoice {
		t.Fatalf("tax 190.00 on a line without a code: %v", err)
	}
	if len(e.ar.credits) != 0 {
		t.Fatalf("nothing is sent to AR: %+v", e.ar.credits)
	}
}

func invoiceLinesSent(t *testing.T, e *taxEnv) map[int]string {
	t.Helper()
	if len(e.ar.credits) != 1 {
		t.Fatalf("credits sent: %+v", e.ar.credits)
	}
	out := map[int]string{}
	for _, l := range e.ar.credits[0].InvoiceLines {
		out[l.LineNo] = l.Qty.String()
	}
	return out
}

func TestRMA_InvoiceLinesSentAreTheInvoicesNotTheReturnOrders(t *testing.T) {
	e := newTaxEnv(t)
	e.src.invoice.Lines = []InvoiceLine{{LineNo: 7, ProductID: e.product, OrderLineID: ids.New(), Qty: d(10), Net: dec("1000.00")}}
	e.creditOnly(t, 4, 0)
	if got := invoiceLinesSent(t, e); len(got) != 1 || got[7] != "4" {
		t.Fatalf("an untaxed product on invoice line 7 must reserve line 7: %v", got)
	}
}

func TestRMA_AProductOnTwoInvoiceLinesSplitsTheReturnedQuantity(t *testing.T) {
	e := newTaxEnv(t)
	e.src.invoice.Lines = []InvoiceLine{
		{LineNo: 2, ProductID: e.product, OrderLineID: ids.New(), Qty: d(3), Net: dec("300.00")},
		{LineNo: 5, ProductID: e.product, OrderLineID: ids.New(), Qty: d(10), Net: dec("1000.00")},
	}
	e.creditOnly(t, 5, 0)
	if got := invoiceLinesSent(t, e); len(got) != 2 || got[2] != "3" || got[5] != "2" {
		t.Fatalf("5 units over lines 2 (3) and 5 (10): %v", got)
	}
}

func TestRMA_TwoReturnLinesOfOneProductAreOneInvoiceAllocation(t *testing.T) {
	e := newTaxEnv(t)
	e.src.invoice.Lines = []InvoiceLine{{LineNo: 4, ProductID: e.product, OrderLineID: ids.New(), Qty: d(10), Net: dec("1000.00")}}
	in := NewReturnOrder{LegalEntityID: e.le, CustomerID: e.customer, OriginalInvoiceID: e.src.invoice.ID, ReturnReasonCode: "DAMAGED",
		IdempotencyKey: ids.New(), RequestHash: ids.New(), Actor: e.createdBy,
		Lines: []NewReturnLine{
			{LineNo: 1, ProductID: e.product, Quantity: d(2), Unit: "EA", Disposition: domain.DispositionCreditOnly},
			{LineNo: 2, ProductID: e.product, Quantity: d(3), Unit: "EA", Disposition: domain.DispositionCreditOnly}}}
	rma := e.create(t, in)
	auth, err := e.s.AuthorizeReturnOrder(e.ctx, rma.ID, rma.Version, e.authorizer, e.now)
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	recv, err := e.s.ReceiveReturnOrder(e.ctx, auth.ID, auth.Version, []ReceiveLine{{LineNo: 1, Quantity: d(2)}, {LineNo: 2, Quantity: d(3)}}, "wh", e.now)
	if err != nil {
		t.Fatalf("receive: %v", err)
	}
	insp, err := e.s.InspectReturnOrder(e.ctx, recv.ID, recv.Version, []InspectLine{
		{LineNo: 1, Accepted: d(2), Disposition: domain.DispositionCreditOnly}, {LineNo: 2, Accepted: d(3), Disposition: domain.DispositionCreditOnly}}, "qc", e.now)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	disp, err := e.s.DispositionReturnOrder(e.ctx, insp.ID, insp.Version, "returns", e.now)
	if err != nil {
		t.Fatalf("disposition: %v", err)
	}
	if _, err := e.s.CreditReturnOrder(e.ctx, disp.ID, disp.Version, "ar", e.now); err != nil {
		t.Fatalf("credit: %v", err)
	}
	if got := invoiceLinesSent(t, e); len(got) != 1 || got[4] != "5" {
		t.Fatalf("2 + 3 of one product on invoice line 4: %v", got)
	}
}

func TestRMA_AnEarlierReturnsUntaxedQuantityMovesTheNextToTheNextInvoiceLine(t *testing.T) {
	e := newTaxEnv(t)
	e.src.invoice.Lines = []InvoiceLine{
		{LineNo: 2, ProductID: e.product, OrderLineID: ids.New(), Qty: d(3), Net: dec("300.00")},
		{LineNo: 5, ProductID: e.product, OrderLineID: ids.New(), Qty: d(10), Net: dec("1000.00")},
	}
	e.creditOnly(t, 3, 0)
	e.ar.credits = nil
	e.creditOnly(t, 2, 0)
	if got := invoiceLinesSent(t, e); len(got) != 1 || got[5] != "2" {
		t.Fatalf("line 2 is used up by the first return: %v", got)
	}
}
