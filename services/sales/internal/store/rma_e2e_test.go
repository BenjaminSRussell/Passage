package store

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	salesevents "github.com/BenjaminSRussell/passage/services/sales/internal/events"
)

type recordingInventory struct {
	receipts []ReturnReceipt
	failNext bool
}

func (r *recordingInventory) ReceiveReturn(_ context.Context, in ReturnReceipt) (string, error) {
	if r.failNext {
		r.failNext = false
		return "", errors.New("inventory down")
	}
	r.receipts = append(r.receipts, in)
	return "rcpt-" + in.LineID, nil
}

type recordingAR struct {
	credits  []ARCredit
	failNext bool
}

func (r *recordingAR) PostRMACredit(_ context.Context, in ARCredit) (CreditNote, error) {
	if r.failNext {
		r.failNext = false
		return CreditNote{}, errors.New("receivables down")
	}
	r.credits = append(r.credits, in)
	return CreditNote{OpenItemID: "oi-" + in.RMAID, DocumentNumber: "CN-" + in.RMANumber, JournalPostingID: "je-" + in.RMAID}, nil
}

type fakeSources struct {
	invoice  Invoice
	original *decimal.Decimal
	current  *decimal.Decimal
}

func (f *fakeSources) ReadInvoice(_ context.Context, _, id string) (Invoice, error) {
	if id != f.invoice.ID {
		return Invoice{}, ErrInvoiceNotFound
	}
	return f.invoice, nil
}

func (f *fakeSources) OriginalUnitCost(context.Context, string, string, []string) (decimal.Decimal, bool, error) {
	if f.original == nil {
		return decimal.Zero, false, nil
	}
	return *f.original, true, nil
}

func (f *fakeSources) CurrentUnitCost(context.Context, string, string, string, string) (decimal.Decimal, bool, error) {
	if f.current == nil {
		return decimal.Zero, false, nil
	}
	return *f.current, true, nil
}

type rmaEnv struct {
	s                     *Postgres
	ctx                   context.Context
	pool                  *pgxpool.Pool
	inv                   *recordingInventory
	ar                    *recordingAR
	src                   *fakeSources
	le, customer          string
	product, site, wh     string
	now                   time.Time
	createdBy, authorizer string
}

func newRMAEnv(t *testing.T) *rmaEnv {
	t.Helper()
	s, pool, ctx := migrated(t)
	e := &rmaEnv{s: s, pool: pool, ctx: ctx, inv: &recordingInventory{}, ar: &recordingAR{}, le: ids.New(), customer: ids.New(),
		product: ids.New(), site: ids.New(), wh: ids.New(), now: time.Date(2027, 3, 1, 12, 0, 0, 0, time.UTC), createdBy: "cs-rep", authorizer: "returns-mgr"}
	cost := decimal.RequireFromString("60.00")
	e.src = &fakeSources{original: &cost, invoice: Invoice{ID: ids.New(), Number: "INV-2027-0000042", LegalEntityID: e.le, CustomerID: e.customer,
		Currency: "USD", Returnable: true, Lines: []InvoiceLine{{LineNo: 3, ProductID: e.product, OrderLineID: ids.New(), PackingSlipID: ids.New(),
			Qty: d(10), Net: decimal.RequireFromString("1000.00")}}}}
	s.WithInventory(e.inv).WithAR(e.ar).WithReturnSources(e.src, e.src)
	return e
}

func (e *rmaEnv) newOrder(disposition string, qty int64) NewReturnOrder {
	return NewReturnOrder{LegalEntityID: e.le, CustomerID: e.customer, OriginalInvoiceID: e.src.invoice.ID, ReturnReasonCode: "DAMAGED",
		IdempotencyKey: ids.New(), RequestHash: ids.New(), Actor: e.createdBy,
		Lines: []NewReturnLine{{LineNo: 1, ProductID: e.product, SiteID: e.site, WarehouseID: e.wh, Quantity: d(qty), Unit: "EA", Disposition: disposition}}}
}

func (e *rmaEnv) create(t *testing.T, in NewReturnOrder) ReturnOrder {
	t.Helper()
	rma, created, err := e.s.CreateReturnOrder(e.ctx, in)
	if err != nil || !created {
		t.Fatalf("create: %+v created=%v err=%v", rma, created, err)
	}
	return rma
}

func (e *rmaEnv) toReceived(t *testing.T, rma ReturnOrder, qty int64) ReturnOrder {
	t.Helper()
	auth, err := e.s.AuthorizeReturnOrder(e.ctx, rma.ID, rma.Version, e.authorizer, e.now)
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	recv, err := e.s.ReceiveReturnOrder(e.ctx, auth.ID, auth.Version, []ReceiveLine{{LineNo: 1, Quantity: d(qty)}}, "wh-clerk", e.now)
	if err != nil {
		t.Fatalf("receive: %v", err)
	}
	return recv
}

func (e *rmaEnv) toInspected(t *testing.T, rma ReturnOrder, received, accepted int64, disposition string) ReturnOrder {
	t.Helper()
	recv := e.toReceived(t, rma, received)
	insp, err := e.s.InspectReturnOrder(e.ctx, recv.ID, recv.Version, []InspectLine{{LineNo: 1, Accepted: d(accepted), Disposition: disposition}}, "qc", e.now)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	return insp
}

func stateCode(t *testing.T, err error) string {
	t.Helper()
	return problemCode(t, err)
}

// TestRMA_ReturnToStockCreditsInventoryAndAR is AC-30.5 / issue #30 AC3: a 4 x 100.00 USD
// RETURN_TO_STOCK return priced from the invoice, received, inspected, restocked at the
// shipment's original cost of 60.00 and credited 400 / 60 / 340.
func TestRMA_ReturnToStockCreditsInventoryAndAR(t *testing.T) {
	e := newRMAEnv(t)
	rma := e.create(t, e.newOrder(domain.DispositionReturnToStock, 4))
	if rma.Status != domain.RMARequested || !rma.Lines[0].UnitPrice.Equal(decimal.RequireFromString("100")) || rma.Currency != "USD" ||
		rma.CustomerID != e.customer || rma.OriginalInvoiceNo != "INV-2027-0000042" {
		t.Fatalf("create priced from the invoice: %+v", rma)
	}
	insp := e.toInspected(t, rma, 4, 4, domain.DispositionReturnToStock)
	if len(e.inv.receipts) != 0 {
		t.Fatalf("nothing is restocked before disposition: %+v", e.inv.receipts)
	}
	disp, err := e.s.DispositionReturnOrder(e.ctx, insp.ID, insp.Version, "returns", e.now)
	if err != nil || disp.Status != domain.RMADispositioned || disp.Lines[0].InventoryReceiptID == nil {
		t.Fatalf("disposition: %+v %v", disp, err)
	}
	r := e.inv.receipts
	if len(r) != 1 || !r[0].Quantity.Equal(d(4)) || !r[0].UnitCost.Equal(decimal.RequireFromString("60")) || r[0].Key != "rma-receive:"+disp.Lines[0].ID ||
		r[0].ProductID != e.product || r[0].PostingDate != "2027-03-01" || r[0].LegalEntityID != e.le {
		t.Fatalf("inventory receipt sent: %+v", r)
	}
	credited, err := e.s.CreditReturnOrder(e.ctx, disp.ID, disp.Version, "ar-clerk", e.now)
	if err != nil || credited.Status != domain.RMAClosed || credited.CreditNoteID == nil || credited.JournalPostingID == nil {
		t.Fatalf("credit: %+v %v", credited, err)
	}
	c := e.ar.credits
	if len(c) != 1 || !c[0].Gross.Equal(d(400)) || !c[0].Restocking.Equal(d(60)) || !c[0].Net.Equal(d(340)) || c[0].InvoiceNumber != "INV-2027-0000042" ||
		c[0].PostingDate != "2027-03-01" || c[0].RMAID != rma.ID || c[0].LegalEntityID != e.le ||
		len(c[0].InvoiceLines) != 1 || c[0].InvoiceLines[0].LineNo != 3 || !c[0].InvoiceLines[0].Qty.Equal(d(4)) {
		t.Fatalf("AR credit sent: %+v", c)
	}
	postings := domain.CreditNoteAmounts{Gross: credited.Gross, Restocking: credited.Restocking, Net: credited.Net}.Postings()
	if len(postings) != 3 || postings[0].Account != domain.AccountSalesReturns || !postings[0].Debit.Equal(d(400)) ||
		postings[1].Account != domain.AccountRestocking || !postings[1].Credit.Equal(d(60)) || postings[2].Account != domain.AccountReceivable || !postings[2].Credit.Equal(d(340)) {
		t.Fatalf("GL postings %v", postings)
	}
	got := outboxTypes(t, e.pool, rma.ID)
	want := []string{salesevents.ReturnOrderAuthorized, salesevents.ReturnOrderReceived, salesevents.ReturnOrderInspected,
		salesevents.ReturnOrderDispositioned, salesevents.ReturnOrderCredited}
	if len(got) != len(want) {
		t.Fatalf("events %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("events %v", got)
		}
	}
}

func TestRMA_CreateIsLimitedByTheInvoice(t *testing.T) {
	e := newRMAEnv(t)
	if _, _, err := e.s.CreateReturnOrder(e.ctx, e.newOrder(domain.DispositionReturnToStock, 11)); stateCode(t, err) != CodeRMAOverReturn {
		t.Fatalf("11 of 10 invoiced: %v", err)
	}
	e.create(t, e.newOrder(domain.DispositionReturnToStock, 6))
	if _, _, err := e.s.CreateReturnOrder(e.ctx, e.newOrder(domain.DispositionReturnToStock, 5)); stateCode(t, err) != CodeRMAOverReturn {
		t.Fatalf("6 already returned, 5 more of 10: %v", err)
	}
	e.create(t, e.newOrder(domain.DispositionReturnToStock, 4))

	other := e.newOrder(domain.DispositionReturnToStock, 1)
	other.Lines[0].ProductID = ids.New()
	if _, _, err := e.s.CreateReturnOrder(e.ctx, other); stateCode(t, err) != CodeRMAInvoice {
		t.Fatalf("product not on the invoice: %v", err)
	}
	wrongCustomer := e.newOrder(domain.DispositionReturnToStock, 1)
	wrongCustomer.CustomerID = ids.New()
	if _, _, err := e.s.CreateReturnOrder(e.ctx, wrongCustomer); stateCode(t, err) != CodeRMAInvoice {
		t.Fatalf("customer that is not the invoice's: %v", err)
	}
	unknown := e.newOrder(domain.DispositionReturnToStock, 1)
	unknown.OriginalInvoiceID = ids.New()
	if _, _, err := e.s.CreateReturnOrder(e.ctx, unknown); stateCode(t, err) != CodeRMAInvoice {
		t.Fatalf("unknown invoice: %v", err)
	}
	e.src.original = nil
	if _, _, err := e.s.CreateReturnOrder(e.ctx, e.newOrder(domain.DispositionReturnToStock, 1)); stateCode(t, err) != CodeRMAInvoice {
		t.Fatalf("unknown original cost: %v", err)
	}
}

func TestRMA_CreateReplayReturnsStoredAndMismatchIs422(t *testing.T) {
	e := newRMAEnv(t)
	in := e.newOrder(domain.DispositionReturnToStock, 2)
	first := e.create(t, in)
	again, created, err := e.s.CreateReturnOrder(e.ctx, in)
	if err != nil || created || again.ID != first.ID {
		t.Fatalf("replay: %+v created=%v %v", again, created, err)
	}
	in.RequestHash = ids.New()
	if _, _, err := e.s.CreateReturnOrder(e.ctx, in); stateCode(t, err) != CodeIdempotencyReuse {
		t.Fatalf("other body under the same key: %v", err)
	}
}

func TestRMA_CreatorCannotAuthorizeOwnReturn(t *testing.T) {
	e := newRMAEnv(t)
	rma := e.create(t, e.newOrder(domain.DispositionReturnToStock, 1))
	_, err := e.s.AuthorizeReturnOrder(e.ctx, rma.ID, rma.Version, e.createdBy, e.now)
	if problemStatus(err) != http.StatusForbidden {
		t.Fatalf("creator authorizing: %v", err)
	}
	if _, err := e.s.AuthorizeReturnOrder(e.ctx, rma.ID, rma.Version, e.authorizer, e.now); err != nil {
		t.Fatalf("another user authorizing: %v", err)
	}
}

func TestRMA_StepsCannotBeSkipped(t *testing.T) {
	e := newRMAEnv(t)
	rma := e.create(t, e.newOrder(domain.DispositionReturnToStock, 4))
	recv := e.toReceived(t, rma, 4)
	if _, err := e.s.DispositionReturnOrder(e.ctx, recv.ID, recv.Version, "returns", e.now); stateCode(t, err) != CodeRMAState {
		t.Fatalf("disposition before inspect: %v", err)
	}
	if _, err := e.s.CreditReturnOrder(e.ctx, recv.ID, recv.Version, "ar", e.now); stateCode(t, err) != CodeRMAState {
		t.Fatalf("credit before inspect: %v", err)
	}
	if _, err := e.s.ReceiveReturnOrder(e.ctx, recv.ID, recv.Version, nil, "wh", e.now); stateCode(t, err) != CodeRMAState {
		t.Fatalf("second receive: %v", err)
	}
	if len(e.inv.receipts) != 0 || len(e.ar.credits) != 0 {
		t.Fatalf("no side effect may run: %+v %+v", e.inv.receipts, e.ar.credits)
	}
}

func TestRMA_PartialReceiptAndInspectionDecideTheCredit(t *testing.T) {
	e := newRMAEnv(t)
	rma := e.create(t, e.newOrder(domain.DispositionReturnToStock, 4))
	insp := e.toInspected(t, rma, 3, 2, domain.DispositionReturnToStock)
	disp, err := e.s.DispositionReturnOrder(e.ctx, insp.ID, insp.Version, "returns", e.now)
	if err != nil || len(e.inv.receipts) != 1 || !e.inv.receipts[0].Quantity.Equal(d(2)) {
		t.Fatalf("only the 2 accepted units restock: %+v %v", e.inv.receipts, err)
	}
	credited, err := e.s.CreditReturnOrder(e.ctx, disp.ID, disp.Version, "ar", e.now)
	if err != nil || !credited.Gross.Equal(d(200)) || !credited.Restocking.Equal(d(30)) || !credited.Net.Equal(d(170)) {
		t.Fatalf("credit for 2 accepted of 4 expected: %+v %v", credited, err)
	}
}

func TestRMA_OmittedReceiveLinesReceiveNothingAndNeedNoInspection(t *testing.T) {
	e := newRMAEnv(t)
	in := e.newOrder(domain.DispositionReturnToStock, 2)
	in.Lines = append(in.Lines, NewReturnLine{LineNo: 2, ProductID: e.product, SiteID: e.site, WarehouseID: e.wh, Quantity: d(3), Unit: "EA", Disposition: domain.DispositionReturnToStock})
	rma := e.create(t, in)
	auth, _ := e.s.AuthorizeReturnOrder(e.ctx, rma.ID, rma.Version, e.authorizer, e.now)
	recv, err := e.s.ReceiveReturnOrder(e.ctx, auth.ID, auth.Version, []ReceiveLine{{LineNo: 1, Quantity: d(2)}}, "wh", e.now)
	if err != nil || !recv.Lines[1].QuantityReceived.IsZero() {
		t.Fatalf("omitted line defaults to 0: %+v %v", recv.Lines, err)
	}
	if _, err := e.s.InspectReturnOrder(e.ctx, recv.ID, recv.Version, []InspectLine{{LineNo: 1, Accepted: d(2), Disposition: domain.DispositionReturnToStock},
		{LineNo: 2, Accepted: d(1), Disposition: domain.DispositionReturnToStock}}, "qc", e.now); stateCode(t, err) != CodeRMAState {
		t.Fatalf("inspecting a line that received nothing: %v", err)
	}
	insp, err := e.s.InspectReturnOrder(e.ctx, recv.ID, recv.Version, []InspectLine{{LineNo: 1, Accepted: d(2), Disposition: domain.DispositionReturnToStock}}, "qc", e.now)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	disp, err := e.s.DispositionReturnOrder(e.ctx, insp.ID, insp.Version, "returns", e.now)
	if err != nil || len(e.inv.receipts) != 1 {
		t.Fatalf("disposition: %+v %v", e.inv.receipts, err)
	}
	credited, err := e.s.CreditReturnOrder(e.ctx, disp.ID, disp.Version, "ar", e.now)
	if err != nil || !credited.Gross.Equal(d(200)) {
		t.Fatalf("only line 1 is credited: %+v %v", credited, err)
	}
}

func TestRMA_ReceivedLineMustBeInspectedAndRangesHold(t *testing.T) {
	e := newRMAEnv(t)
	rma := e.create(t, e.newOrder(domain.DispositionReturnToStock, 4))
	auth, _ := e.s.AuthorizeReturnOrder(e.ctx, rma.ID, rma.Version, e.authorizer, e.now)
	if _, err := e.s.ReceiveReturnOrder(e.ctx, auth.ID, auth.Version, []ReceiveLine{{LineNo: 1, Quantity: d(5)}}, "wh", e.now); stateCode(t, err) != CodeRMAState {
		t.Fatalf("receive above expected: %v", err)
	}
	recv, err := e.s.ReceiveReturnOrder(e.ctx, auth.ID, auth.Version, []ReceiveLine{{LineNo: 1, Quantity: d(4)}}, "wh", e.now)
	if err != nil {
		t.Fatalf("receive: %v", err)
	}
	if _, err := e.s.InspectReturnOrder(e.ctx, recv.ID, recv.Version, nil, "qc", e.now); stateCode(t, err) != CodeRMAState {
		t.Fatalf("a received line left uninspected: %v", err)
	}
	if _, err := e.s.InspectReturnOrder(e.ctx, recv.ID, recv.Version, []InspectLine{{LineNo: 1, Accepted: d(5), Disposition: domain.DispositionReturnToStock}}, "qc", e.now); stateCode(t, err) != CodeRMAState {
		t.Fatalf("accepting more than received: %v", err)
	}
}

func TestRMA_ExpiryIsCommittedWhenReceiveIsRefused(t *testing.T) {
	e := newRMAEnv(t)
	rma := e.create(t, e.newOrder(domain.DispositionReturnToStock, 1))
	auth, _ := e.s.AuthorizeReturnOrder(e.ctx, rma.ID, rma.Version, e.authorizer, e.now)
	late := e.now.AddDate(0, 0, 31)
	if _, err := e.s.ReceiveReturnOrder(e.ctx, auth.ID, auth.Version, nil, "wh", late); stateCode(t, err) != CodeRMAExpired {
		t.Fatalf("receive after expiry: %v", err)
	}
	got, err := e.s.GetReturnOrder(e.ctx, rma.ID)
	if err != nil || got.Status != domain.RMAExpired {
		t.Fatalf("expiry must be stored, not rolled back: %+v %v", got, err)
	}
	expired, _, err := e.s.CreateReturnOrder(e.ctx, e.newOrder(domain.DispositionReturnToStock, 10))
	if err != nil || expired.ID == "" {
		t.Fatalf("an expired RMA frees its quantity: %v", err)
	}
}

func TestRMA_FailedRestockIsRetriedUnderTheSameKeyBeforeCredit(t *testing.T) {
	e := newRMAEnv(t)
	insp := e.toInspected(t, e.create(t, e.newOrder(domain.DispositionReturnToStock, 4)), 4, 4, domain.DispositionReturnToStock)
	e.inv.failNext = true
	if _, err := e.s.DispositionReturnOrder(e.ctx, insp.ID, insp.Version, "returns", e.now); stateCode(t, err) != CodeRMAUpstream {
		t.Fatalf("inventory down: %v", err)
	}
	got, _ := e.s.GetReturnOrder(e.ctx, insp.ID)
	if got.Status != domain.RMADispositioned || got.Lines[0].InventoryReceiptID != nil {
		t.Fatalf("state is committed before the call: %+v", got)
	}
	if _, err := e.s.CreditReturnOrder(e.ctx, got.ID, got.Version, "ar", e.now); stateCode(t, err) != CodeRMAState || len(e.ar.credits) != 0 {
		t.Fatalf("credit with a pending restock: %v", err)
	}
	disp, err := e.s.DispositionReturnOrder(e.ctx, got.ID, got.Version, "returns", e.now.Add(48*time.Hour))
	if err != nil || disp.Lines[0].InventoryReceiptID == nil || len(e.inv.receipts) != 1 || e.inv.receipts[0].Key != "rma-receive:"+got.Lines[0].ID ||
		e.inv.receipts[0].PostingDate != "2027-03-01" {
		t.Fatalf("retry: %+v receipts=%+v %v", disp, e.inv.receipts, err)
	}
	if _, err := e.s.DispositionReturnOrder(e.ctx, disp.ID, disp.Version, "returns", e.now); stateCode(t, err) != CodeRMAState {
		t.Fatalf("a completed disposition does not repeat: %v", err)
	}
}

func TestRMA_FailedCreditResumesWithStoredAmountsAndDate(t *testing.T) {
	e := newRMAEnv(t)
	insp := e.toInspected(t, e.create(t, e.newOrder(domain.DispositionReturnToStock, 4)), 4, 4, domain.DispositionReturnToStock)
	disp, err := e.s.DispositionReturnOrder(e.ctx, insp.ID, insp.Version, "returns", e.now)
	if err != nil {
		t.Fatalf("disposition: %v", err)
	}
	e.ar.failNext = true
	if _, err := e.s.CreditReturnOrder(e.ctx, disp.ID, disp.Version, "ar", e.now); stateCode(t, err) != CodeRMAUpstream {
		t.Fatalf("receivables down: %v", err)
	}
	got, _ := e.s.GetReturnOrder(e.ctx, disp.ID)
	if got.Status != domain.RMACredited || !got.Net.Equal(d(340)) {
		t.Fatalf("credit is stored before the call: %+v", got)
	}
	closed, err := e.s.CreditReturnOrder(e.ctx, got.ID, got.Version, "ar", e.now.AddDate(0, 0, 3))
	if err != nil || closed.Status != domain.RMAClosed || len(e.ar.credits) != 1 || e.ar.credits[0].PostingDate != "2027-03-01" ||
		e.ar.credits[0].IdempotencyKey != "rma-credit-"+got.ID {
		t.Fatalf("resume: %+v credits=%+v %v", closed, e.ar.credits, err)
	}
}

func TestRMA_CreditOnlyLineCreditsExpectedQuantityWithoutStock(t *testing.T) {
	e := newRMAEnv(t)
	rma := e.create(t, e.newOrder(domain.DispositionCreditOnly, 3))
	auth, _ := e.s.AuthorizeReturnOrder(e.ctx, rma.ID, rma.Version, e.authorizer, e.now)
	recv, err := e.s.ReceiveReturnOrder(e.ctx, auth.ID, auth.Version, nil, "wh", e.now)
	if err != nil {
		t.Fatalf("receive: %v", err)
	}
	insp, err := e.s.InspectReturnOrder(e.ctx, recv.ID, recv.Version, []InspectLine{}, "qc", e.now)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	disp, err := e.s.DispositionReturnOrder(e.ctx, insp.ID, insp.Version, "returns", e.now)
	if err != nil || len(e.inv.receipts) != 0 {
		t.Fatalf("credit-only touches no stock: %+v %v", e.inv.receipts, err)
	}
	credited, err := e.s.CreditReturnOrder(e.ctx, disp.ID, disp.Version, "ar", e.now)
	if err != nil || !credited.Gross.Equal(d(300)) || !credited.Restocking.IsZero() || !credited.Net.Equal(d(300)) {
		t.Fatalf("credit-only: %+v %v", credited, err)
	}
}

func TestRMA_NothingAcceptedMeansNothingToCredit(t *testing.T) {
	e := newRMAEnv(t)
	insp := e.toInspected(t, e.create(t, e.newOrder(domain.DispositionReturnToStock, 2)), 2, 0, domain.DispositionReturnToStock)
	disp, err := e.s.DispositionReturnOrder(e.ctx, insp.ID, insp.Version, "returns", e.now)
	if err != nil || len(e.inv.receipts) != 0 {
		t.Fatalf("disposition: %v", err)
	}
	// Nothing to credit closes the RMA without asking receivables (which is not even needed to be configured).
	e.s.ar = nil
	closed, err := e.s.CreditReturnOrder(e.ctx, disp.ID, disp.Version, "ar", e.now)
	if err != nil || closed.Status != domain.RMAClosed || !closed.Net.IsZero() || closed.CreditNoteID != nil || len(e.ar.credits) != 0 {
		t.Fatalf("credit of nothing: %+v %v", closed, err)
	}
}

func TestRMA_RestockingFloorAppliesOncePerNote(t *testing.T) {
	e := newRMAEnv(t)
	second := ids.New()
	e.src.invoice.Lines[0].Net = decimal.RequireFromString("200.00")
	e.src.invoice.Lines = append(e.src.invoice.Lines, InvoiceLine{LineNo: 2, ProductID: second, OrderLineID: ids.New(), PackingSlipID: e.src.invoice.Lines[0].PackingSlipID,
		Qty: d(10), Net: decimal.RequireFromString("200.00")})
	in := e.newOrder(domain.DispositionReturnToStock, 1)
	in.Lines = append(in.Lines, NewReturnLine{LineNo: 2, ProductID: second, SiteID: e.site, WarehouseID: e.wh, Quantity: d(1), Unit: "EA", Disposition: domain.DispositionReturnToStock})
	rma := e.create(t, in)
	auth, _ := e.s.AuthorizeReturnOrder(e.ctx, rma.ID, rma.Version, e.authorizer, e.now)
	recv, err := e.s.ReceiveReturnOrder(e.ctx, auth.ID, auth.Version, []ReceiveLine{{LineNo: 1, Quantity: d(1)}, {LineNo: 2, Quantity: d(1)}}, "wh", e.now)
	if err != nil {
		t.Fatalf("receive: %v", err)
	}
	insp, err := e.s.InspectReturnOrder(e.ctx, recv.ID, recv.Version, []InspectLine{{LineNo: 1, Accepted: d(1), Disposition: domain.DispositionReturnToStock},
		{LineNo: 2, Accepted: d(1), Disposition: domain.DispositionReturnToStock}}, "qc", e.now)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	disp, err := e.s.DispositionReturnOrder(e.ctx, insp.ID, insp.Version, "returns", e.now)
	if err != nil {
		t.Fatalf("disposition: %v", err)
	}
	// 2 x 20.00: 3.00 + 3.00 is below the 25.00 floor, which applies once to the note (25.00, net 15.00); applied per
	// line it charged 20.00 + 20.00 and left nothing to credit.
	credited, err := e.s.CreditReturnOrder(e.ctx, disp.ID, disp.Version, "ar", e.now)
	if err != nil || !credited.Gross.Equal(d(40)) || !credited.Restocking.Equal(d(25)) || !credited.Net.Equal(d(15)) {
		t.Fatalf("two-line note: %+v %v", credited, err)
	}
	sum := decimal.Zero
	for _, l := range credited.Lines {
		sum = sum.Add(l.RestockingAmount)
	}
	if !sum.Equal(credited.Restocking) || len(e.ar.credits) != 1 || !e.ar.credits[0].Restocking.Equal(d(25)) {
		t.Fatalf("line charges %s vs note %s, AR %+v", sum, credited.Restocking, e.ar.credits)
	}
}

func TestRMA_InspectRefusesRestockingALineWithNoStockDestinationOrCost(t *testing.T) {
	e := newRMAEnv(t)
	in := e.newOrder(domain.DispositionCreditOnly, 2)
	in.Lines[0].SiteID, in.Lines[0].WarehouseID = "", ""
	recv := e.toReceived(t, e.create(t, in), 2)
	if _, err := e.s.InspectReturnOrder(e.ctx, recv.ID, recv.Version, []InspectLine{{LineNo: 1, Accepted: d(2), Disposition: domain.DispositionReturnToStock}}, "qc", e.now); stateCode(t, err) != CodeRMAState {
		t.Fatalf("RETURN_TO_STOCK on a credit-only line without site or warehouse: %v", err)
	}
	// With site and warehouse but created CREDIT_ONLY at ORIGINAL_COST, no cost was resolved: refused too.
	in = e.newOrder(domain.DispositionCreditOnly, 2)
	e.src.invoice.Lines[0].Qty = d(20)
	recv = e.toReceived(t, e.create(t, in), 2)
	if _, err := e.s.InspectReturnOrder(e.ctx, recv.ID, recv.Version, []InspectLine{{LineNo: 1, Accepted: d(2), Disposition: domain.DispositionReturnToStock}}, "qc", e.now); stateCode(t, err) != CodeRMAState {
		t.Fatalf("RETURN_TO_STOCK on a credit-only line without a cost: %v", err)
	}
	insp, err := e.s.InspectReturnOrder(e.ctx, recv.ID, recv.Version, []InspectLine{{LineNo: 1, Accepted: d(2), Disposition: domain.DispositionCreditOnly}}, "qc", e.now)
	if err != nil || insp.Status != domain.RMAInspected {
		t.Fatalf("the same line inspected CREDIT_ONLY: %+v %v", insp, err)
	}
}

func TestRMA_CurrentCostIsResolvedAtInspection(t *testing.T) {
	e := newRMAEnv(t)
	in := e.newOrder(domain.DispositionReturnToStock, 1)
	in.Lines[0].ReturnCostMethod = domain.CostCurrent
	recv := e.toReceived(t, e.create(t, in), 1)
	if _, err := e.s.InspectReturnOrder(e.ctx, recv.ID, recv.Version, []InspectLine{{LineNo: 1, Accepted: d(1), Disposition: domain.DispositionReturnToStock}}, "qc", e.now); stateCode(t, err) != CodeRMAInvoice {
		t.Fatalf("no on-hand average is refused at inspection: %v", err)
	}
	if got, _ := e.s.GetReturnOrder(e.ctx, recv.ID); got.Status != domain.RMAReceived {
		t.Fatalf("the refused inspection keeps RECEIVED: %s", got.Status)
	}
	cur := decimal.RequireFromString("75.00")
	e.src.current = &cur
	insp, err := e.s.InspectReturnOrder(e.ctx, recv.ID, recv.Version, []InspectLine{{LineNo: 1, Accepted: d(1), Disposition: domain.DispositionReturnToStock}}, "qc", e.now)
	if err != nil || insp.Lines[0].UnitCost == nil || !insp.Lines[0].UnitCost.Equal(cur) {
		t.Fatalf("inspection stores the current cost: %+v %v", insp.Lines, err)
	}
	e.src.current = nil
	if _, err := e.s.DispositionReturnOrder(e.ctx, insp.ID, insp.Version, "returns", e.now); err != nil || len(e.inv.receipts) != 1 || !e.inv.receipts[0].UnitCost.Equal(cur) {
		t.Fatalf("restock at the inspected cost: %+v %v", e.inv.receipts, err)
	}
}

func TestRMA_BillingCreditNotesReduceTheReturnableQuantity(t *testing.T) {
	e := newRMAEnv(t)
	e.src.invoice.Lines[0].CreditedQty = d(7)
	if _, _, err := e.s.CreateReturnOrder(e.ctx, e.newOrder(domain.DispositionReturnToStock, 4)); stateCode(t, err) != CodeRMAOverReturn {
		t.Fatalf("10 invoiced, 7 credited by billing, 4 asked: %v", err)
	}
	e.create(t, e.newOrder(domain.DispositionReturnToStock, 3))
}

func TestRMA_RejectFreesTheQuantityAndOnlyBeforeReceipt(t *testing.T) {
	e := newRMAEnv(t)
	rma := e.create(t, e.newOrder(domain.DispositionReturnToStock, 10))
	if _, _, err := e.s.CreateReturnOrder(e.ctx, e.newOrder(domain.DispositionReturnToStock, 1)); stateCode(t, err) != CodeRMAOverReturn {
		t.Fatalf("the open RMA holds the whole invoice: %v", err)
	}
	rejected, err := e.s.RejectReturnOrder(e.ctx, rma.ID, rma.Version, e.authorizer, e.now)
	if err != nil || rejected.Status != domain.RMARejected {
		t.Fatalf("reject: %+v %v", rejected, err)
	}
	again := e.create(t, e.newOrder(domain.DispositionReturnToStock, 10))
	recv := e.toReceived(t, again, 10)
	if _, err := e.s.RejectReturnOrder(e.ctx, recv.ID, recv.Version, e.authorizer, e.now); stateCode(t, err) != CodeRMAState {
		t.Fatalf("a received RMA cannot be rejected: %v", err)
	}
}

func TestRMA_ListFiltersCustomerByUUIDValue(t *testing.T) {
	e := newRMAEnv(t)
	e.create(t, e.newOrder(domain.DispositionReturnToStock, 1))
	for _, customer := range []string{e.customer, strings.ToUpper(e.customer)} {
		got, err := e.s.ListReturnOrders(e.ctx, ReturnOrderFilter{CustomerID: customer, LegalEntityID: strings.ToUpper(e.le), Limit: 10})
		if err != nil || len(got) != 1 {
			t.Fatalf("customer %s: %d %v", customer, len(got), err)
		}
	}
}

func TestRMA_ReturnCostMethods(t *testing.T) {
	cases := []struct {
		method string
		want   string
	}{{domain.CostZero, "0"}, {domain.CostCurrent, "75"}, {domain.CostOriginal, "60"}}
	for _, tc := range cases {
		t.Run(tc.method, func(t *testing.T) {
			e := newRMAEnv(t)
			cur := decimal.RequireFromString("75.00")
			e.src.current = &cur
			in := e.newOrder(domain.DispositionReturnToStock, 1)
			in.Lines[0].ReturnCostMethod = tc.method
			insp := e.toInspected(t, e.create(t, in), 1, 1, domain.DispositionReturnToStock)
			if _, err := e.s.DispositionReturnOrder(e.ctx, insp.ID, insp.Version, "returns", e.now); err != nil {
				t.Fatalf("disposition: %v", err)
			}
			if len(e.inv.receipts) != 1 || !e.inv.receipts[0].UnitCost.Equal(decimal.RequireFromString(tc.want)) {
				t.Fatalf("restock cost = %+v, want %s", e.inv.receipts, tc.want)
			}
		})
	}
}
