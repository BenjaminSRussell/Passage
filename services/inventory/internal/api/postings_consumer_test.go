package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/eventing"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/productunits"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
)

const (
	postTenant  = "11111111-1111-4111-8111-111111111111"
	postEntity  = "22222222-2222-4222-8222-222222222222"
	postSite    = "33333333-3333-4333-8333-333333333333"
	postProd    = "44444444-4444-4444-8444-444444444444"
	postReceipt = "55555555-5555-4555-8555-555555555555"
	postOrder   = "77777777-7777-4777-8777-777777777777"
	postLine    = "88888888-8888-4888-8888-888888888888"
)

type fakePostings struct {
	got     []store.CreateTransactionInput
	errs    []error
	cost    decimal.Decimal
	booked  map[string]store.TransactionRow
	entries map[string][]string
}

func (f *fakePostings) CreateTransaction(ctx context.Context, in store.CreateTransactionInput) (store.StatusChange, bool, error) {
	if p, _ := tenant.FromContext(ctx); p.LegalEntityID != postEntity || p.UserID != tenant.ServiceActor(PostingsConsumerName) {
		return store.StatusChange{}, false, errors.New("booking outside the document's legal entity")
	}
	f.got = append(f.got, in)
	if len(f.errs) > 0 {
		err := f.errs[0]
		f.errs = f.errs[1:]
		return store.StatusChange{}, false, err
	}
	return store.StatusChange{TransactionID: "t"}, true, nil
}

func (f *fakePostings) IssueBasis(context.Context, string, string, domain.Dimensions) (string, decimal.Decimal, error) {
	return "EUR", f.cost, nil
}

func (f *fakePostings) LineBookings(ctx context.Context, le, sourceType string, lineIDs []string) (map[string][]store.TransactionRow, error) {
	if p, _ := tenant.FromContext(ctx); p.LegalEntityID != postEntity || le != postEntity {
		return nil, errors.New("read outside the document's legal entity")
	}
	out := map[string][]store.TransactionRow{}
	for _, id := range lineIDs {
		if r, ok := f.booked[sourceType+"|"+id]; ok {
			out[id] = append(out[id], r)
		}
		for _, k := range f.entries[sourceType+"|"+id] {
			out[id] = append(out[id], f.booked[k])
		}
	}
	return out, nil
}

func (f *fakePostings) book(sourceType, sourceID string, r store.TransactionRow) {
	if f.booked == nil {
		f.booked = map[string]store.TransactionRow{}
	}
	r.SourceType, r.SourceID, r.LegalEntityID = sourceType, sourceID, postEntity
	f.booked[sourceType+"|"+sourceID] = r
}

// bookEntry books r as an entry (a serial or batch) of the document line lineID.
func (f *fakePostings) bookEntry(sourceType, lineID, sourceID string, r store.TransactionRow) {
	if f.booked == nil {
		f.booked = map[string]store.TransactionRow{}
	}
	if f.entries == nil {
		f.entries = map[string][]string{}
	}
	r.SourceType, r.SourceID, r.LegalEntityID = sourceType, sourceID, postEntity
	k := sourceType + "|" + sourceID
	f.booked[k] = r
	f.entries[sourceType+"|"+lineID] = append(f.entries[sourceType+"|"+lineID], k)
}

// fakeUnits converts BOX into 12 EA (EA is the inventory unit) and knows no other unit.
type fakeUnits struct {
	err   error
	calls int
}

func (u *fakeUnits) ToInventoryUnit(_ context.Context, _, _, unit string, qty decimal.Decimal) (decimal.Decimal, string, error) {
	u.calls++
	switch {
	case u.err != nil:
		return decimal.Zero, "", u.err
	case unit == "EA":
		return qty, "EA", nil
	case unit == "BOX":
		return qty.Mul(decimal.NewFromInt(12)), "EA", nil
	}
	return decimal.Zero, "", fmt.Errorf("%w: %s", productunits.ErrNoConversion, unit)
}

func postingsCtx(t *testing.T) context.Context {
	t.Helper()
	p, err := tenant.ServicePrincipal(PostingsConsumerName, postTenant, "")
	if err != nil {
		t.Fatal(err)
	}
	return tenant.With(context.Background(), p)
}

func postingEvent(t *testing.T, typ, source, data string) events.Event {
	t.Helper()
	evt, err := events.New(typ, source, "s", postTenant, json.RawMessage(data))
	if err != nil {
		t.Fatal(err)
	}
	return evt
}

func cwPtr(n int) *int { return &n }

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

const twoReceiptLines = `{"receipt_id":"` + postReceipt + `","receipt_number":"RCV-1","legal_entity_id":"` + postEntity + `","posting_date":"2026-10-25","currency":"USD","lines":[` +
	`{"line_no":1,"product_id":"` + postProd + `","unit":"kg","qty":"102","serials":[],"inventory_status":"QUARANTINE","amount":"204","unit_price":"2","site_id":"` + postSite + `","cw_qty":10},` +
	`{"line_no":2,"product_id":"` + postProd + `","unit":"BOX","qty":"4","serials":[],"inventory_status":"AVAILABLE","amount":"10","unit_price":"2.5","site_id":"` + postSite + `"}]}`

// A receipt books its catch-weight line at the weight with the cases, in the
// document's legal entity, under a quarantine status dimension. Without a unit
// conversion the plain line is parked (INV-PST-002) after the other is booked.
func TestPostingsConsumer_BooksCatchWeightReceiptLines(t *testing.T) {
	f := &fakePostings{}
	h := PostingsConsumerHandlers(f, nil, domain.NegativePolicy{})
	err := h[ReceiptPostedType].Handle(postingsCtx(t), nil, postingEvent(t, ReceiptPostedType, ReceiptSource, twoReceiptLines))
	if !eventing.IsPermanent(err) || !strings.Contains(err.Error(), "line 2: "+CodePostingNoConverter) {
		t.Fatalf("%v, want line 2 parked with %s", err, CodePostingNoConverter)
	}
	if len(f.got) != 1 {
		t.Fatalf("%d bookings, want 1", len(f.got))
	}
	in := f.got[0]
	if in.SourceType != "RECEIPT_LINE" || !in.Qty.Equal(decimal.NewFromInt(102)) || in.CWQty != 10 || in.EnteredUnit != "KG" || in.Status != domain.StatusReceived ||
		!in.CostPrice.Equal(decimal.NewFromInt(2)) || in.Value != nil || in.Dims[domain.DimStatus] != "QUARANTINE" || in.Dims[domain.DimSite] != postSite || in.LegalEntityID != postEntity {
		t.Fatalf("booking %+v", in)
	}
	if in.SourceID != postingSourceID(postTenant, "receipt-line", postReceipt, "1") || in.IdempotencyKey == "" {
		t.Fatalf("origin %s key %s are not derived from the receipt line", in.SourceID, in.IdempotencyKey)
	}
}

// A plain line is converted into the product's inventory unit (4 BOX = 48 EA)
// and valued at the receipt amount; the catch-weight line asks no conversion.
// A unit product cannot convert parks the line (INV-PST-001); a conversion
// that may pass is retried.
func TestPostingsConsumer_ConvertsPlainReceiptLines(t *testing.T) {
	f, u := &fakePostings{}, &fakeUnits{}
	evt := postingEvent(t, ReceiptPostedType, ReceiptSource, twoReceiptLines)
	if err := PostingsConsumerHandlers(f, u, domain.NegativePolicy{})[ReceiptPostedType].Handle(postingsCtx(t), nil, evt); err != nil {
		t.Fatal(err)
	}
	if len(f.got) != 2 || u.calls != 1 {
		t.Fatalf("%d bookings, %d conversions, want 2 and 1", len(f.got), u.calls)
	}
	in := f.got[1]
	if !in.Qty.Equal(d("48")) || !in.EnteredQty.Equal(d("4")) || in.EnteredUnit != "BOX" || in.Value == nil || !in.Value.Equal(d("10")) ||
		!in.CostPrice.Equal(d("0.208333")) || in.CWQty != 0 || len(in.Dims) != 1 {
		t.Fatalf("plain booking %+v", in)
	}
	pound := strings.Replace(twoReceiptLines, `"unit":"BOX"`, `"unit":"LB"`, 1)
	err := PostingsConsumerHandlers(&fakePostings{}, &fakeUnits{}, domain.NegativePolicy{})[ReceiptPostedType].Handle(postingsCtx(t), nil,
		postingEvent(t, ReceiptPostedType, ReceiptSource, pound))
	if !eventing.IsPermanent(err) || !strings.Contains(err.Error(), CodePostingNoConversion) {
		t.Fatalf("no conversion: %v, want permanent %s", err, CodePostingNoConversion)
	}
	down := &fakeUnits{err: errors.New("product unavailable")}
	if err := PostingsConsumerHandlers(&fakePostings{}, down, domain.NegativePolicy{})[ReceiptPostedType].Handle(postingsCtx(t), nil, evt); err == nil || eventing.IsPermanent(err) {
		t.Fatalf("product unavailable: %v, want a retryable error", err)
	}
}

const deliveryData = `{"progress_id":"66666666-6666-4666-8666-666666666666","order_id":"` + postOrder + `","legal_entity_id":"` + postEntity +
	`","currency":"USD","kind":"%s","source_ref":"PS-1","lines":[{"line_id":"` + postLine + `","line_no":1,"quantity":"5","product_id":"` + postProd +
	`","unit":"%s","site_id":"` + postSite + `"%s}]}`

// A delivery issues the reported weight with the cases at the row's average in
// the row's currency, a plain line its quantity converted into the inventory
// unit; an invoice report books nothing.
func TestPostingsConsumer_IssuesTheDeliveredWeight(t *testing.T) {
	f := &fakePostings{cost: d("2.5")}
	h := PostingsConsumerHandlers(f, &fakeUnits{}, domain.NegativePolicy{PhysicalAllowed: true})
	weighed := `,"reported_weight":"51"`
	if err := h[OrderProgressRecordedType].Handle(postingsCtx(t), nil, postingEvent(t, OrderProgressRecordedType, SalesSource, fmt.Sprintf(deliveryData, "INVOICED", "CS", weighed))); err != nil || len(f.got) != 0 {
		t.Fatalf("invoice report: %v, %d bookings", err, len(f.got))
	}
	if err := h[OrderProgressRecordedType].Handle(postingsCtx(t), nil, postingEvent(t, OrderProgressRecordedType, SalesSource, fmt.Sprintf(deliveryData, "DELIVERED", "CS", weighed))); err != nil {
		t.Fatal(err)
	}
	in := f.got[0]
	if in.SourceType != "SALES_LINE" || !in.Qty.Equal(decimal.NewFromInt(-51)) || in.CWQty != 5 || !in.EnteredQty.Equal(decimal.NewFromInt(-5)) || in.EnteredUnit != "CS" ||
		in.Status != domain.StatusDeducted || in.Currency != "EUR" || !in.CostPrice.Equal(d("2.5")) || !in.Negative.PhysicalAllowed ||
		in.SourceID != deliverySourceID(postTenant, postOrder, "PS-1", postLine) || in.IssueRef != "SALES_LINE:"+strings.ToLower(postLine) {
		t.Fatalf("booking %+v", in)
	}
	if err := h[OrderProgressRecordedType].Handle(postingsCtx(t), nil, postingEvent(t, OrderProgressRecordedType, SalesSource, fmt.Sprintf(deliveryData, "DELIVERED", "BOX", ""))); err != nil {
		t.Fatal(err)
	}
	if in := f.got[1]; !in.Qty.Equal(d("-60")) || !in.EnteredQty.Equal(d("-5")) || in.EnteredUnit != "BOX" || in.CWQty != 0 {
		t.Fatalf("plain delivery %+v, want -60 EA entered as -5 BOX", in)
	}
}

// A store failure that may pass is retried (not permanent); a rule of
// inventory, a foreign source, malformed data or a missing principal parks.
func TestPostingsConsumer_RetriesTransientAndParksPermanentFailures(t *testing.T) {
	ctx := postingsCtx(t)
	evt := postingEvent(t, ReceiptPostedType, ReceiptSource, twoReceiptLines)
	units := &fakeUnits{}
	transient := &fakePostings{errs: []error{errors.New("connection reset")}}
	if err := PostingsConsumerHandlers(transient, units, domain.NegativePolicy{})[ReceiptPostedType].Handle(ctx, nil, evt); err == nil || eventing.IsPermanent(err) {
		t.Fatalf("transient store error: %v, want a retryable error", err)
	}
	rule := &fakePostings{errs: []error{httpx.NewProblem(422, "INV-NEG-001", "negative")}}
	if err := PostingsConsumerHandlers(rule, units, domain.NegativePolicy{})[ReceiptPostedType].Handle(ctx, nil, evt); !eventing.IsPermanent(err) {
		t.Fatalf("business rule: %v, want permanent", err)
	}
	inFlight := &fakePostings{errs: []error{httpx.NewProblem(409, store.CodeKeyInFlight, "in flight")}}
	if err := PostingsConsumerHandlers(inFlight, units, domain.NegativePolicy{})[ReceiptPostedType].Handle(ctx, nil, evt); err == nil || eventing.IsPermanent(err) {
		t.Fatalf("key in flight: %v, want a retryable error", err)
	}
	h := PostingsConsumerHandlers(&fakePostings{}, units, domain.NegativePolicy{})[ReceiptPostedType]
	for name, tc := range map[string]struct {
		ctx context.Context
		evt events.Event
	}{
		"foreign source": {ctx, postingEvent(t, ReceiptPostedType, "/passage/other/other", twoReceiptLines)},
		"malformed":      {ctx, postingEvent(t, ReceiptPostedType, ReceiptSource, `{"lines":"x"}`)},
		"no principal":   {context.Background(), evt},
	} {
		if err := h.Handle(tc.ctx, nil, tc.evt); !eventing.IsPermanent(err) {
			t.Errorf("%s: %v, want permanent", name, err)
		}
	}
}

func correctedData(kind string, seq int, lines string) string {
	return fmt.Sprintf(`{"receipt_id":%q,"receipt_number":"RCV-1","legal_entity_id":%q,"posting_date":"2026-10-27","kind":%q,"correction_seq":%d,"currency":"USD","lines":[%s]}`,
		postReceipt, postEntity, kind, seq, lines)
}

// A correction books the delta at the booked line's unit factor and cost, in
// its dimensions, linked to it; a cancellation offsets the booked net of the
// line and its corrections exactly. An unbooked line, another product or a
// cancellation of another quantity than booked parks.
func TestPostingsConsumer_BooksReceiptCorrectionsAgainstTheBookedLine(t *testing.T) {
	f := &fakePostings{}
	dims := domain.Dimensions{domain.DimSite: postSite, domain.DimStatus: "QUARANTINE"}
	// Line 1: 4 BOX booked as 48 EA at 10.00; line 2: 102 kg, 10 cases at 204.
	f.book(sourceReceiptLine, receiptLineSourceID(postTenant, postReceipt, 1), store.TransactionRow{ID: "orig-1", ProductID: postProd, Dims: dims,
		Qty: d("48"), EnteredQty: d("4"), EnteredUnit: "BOX", Currency: "USD", CostAmountPhysical: d("10")})
	f.book(sourceReceiptLine, receiptLineSourceID(postTenant, postReceipt, 2), store.TransactionRow{ID: "orig-2", ProductID: postProd, Dims: dims,
		Qty: d("102"), EnteredQty: d("102"), EnteredUnit: "KG", CWQty: cwPtr(10), Currency: "USD", CostAmountPhysical: d("204")})
	h := PostingsConsumerHandlers(f, nil, domain.NegativePolicy{})[ReceiptCorrectedType]
	ctx := postingsCtx(t)
	lines := `{"line_no":1,"product_id":"` + postProd + `","qty_delta":"-1"},{"line_no":2,"product_id":"` + postProd + `","qty_delta":"-2","cw_qty_delta":-1}`
	if err := h.Handle(ctx, nil, postingEvent(t, ReceiptCorrectedType, ReceiptSource, correctedData("CORRECTION", 1, lines))); err != nil {
		t.Fatal(err)
	}
	if len(f.got) != 2 {
		t.Fatalf("%d bookings, want 2", len(f.got))
	}
	box, kg := f.got[0], f.got[1]
	if !box.Qty.Equal(d("-12")) || !box.EnteredQty.Equal(d("-1")) || box.EnteredUnit != "BOX" || box.Status != domain.StatusDeducted || box.Corrects != "orig-1" ||
		box.Value == nil || !box.Value.Equal(d("-2.5")) || box.Dims[domain.DimStatus] != "QUARANTINE" || box.SourceID != receiptCorrectionSourceID(postTenant, postReceipt, 1, 1) {
		t.Fatalf("BOX correction %+v, want -12 EA (entered -1 BOX) at -2.50 linked to orig-1", box)
	}
	if !kg.Qty.Equal(d("-2")) || kg.CWQty != 1 || kg.Value == nil || !kg.Value.Equal(d("-4")) || kg.Corrects != "orig-2" {
		t.Fatalf("catch-weight correction %+v, want -2 kg, 1 case, -4", kg)
	}

	// The cancellation offsets what is left after correction 1: 36 EA at 7.50, 100 kg and 9 cases at 200.
	f.book(sourceReceiptLine, receiptCorrectionSourceID(postTenant, postReceipt, 1, 1), store.TransactionRow{ID: "c-1", ProductID: postProd, Dims: dims,
		Qty: d("-12"), EnteredQty: d("-1"), EnteredUnit: "BOX", Currency: "USD", CostAmountPhysical: d("-2.5")})
	f.book(sourceReceiptLine, receiptCorrectionSourceID(postTenant, postReceipt, 1, 2), store.TransactionRow{ID: "c-2", ProductID: postProd, Dims: dims,
		Qty: d("-2"), EnteredQty: d("-2"), EnteredUnit: "KG", CWQty: cwPtr(1), Currency: "USD", CostAmountPhysical: d("-4")})
	f.got = nil
	cancel := `{"line_no":1,"product_id":"` + postProd + `","qty_delta":"-3"},{"line_no":2,"product_id":"` + postProd + `","qty_delta":"-100","cw_qty_delta":-9}`
	if err := h.Handle(ctx, nil, postingEvent(t, ReceiptCorrectedType, ReceiptSource, correctedData("CANCELLATION", 1, cancel))); err != nil {
		t.Fatal(err)
	}
	if box, kg := f.got[0], f.got[1]; !box.Qty.Equal(d("-36")) || !box.Value.Equal(d("-7.5")) || !kg.Qty.Equal(d("-100")) || kg.CWQty != 9 || !kg.Value.Equal(d("-200")) {
		t.Fatalf("cancellation %+v / %+v, want -36 EA at -7.50 and -100 kg, 9 cases at -200", box, kg)
	}

	for name, tc := range map[string]struct{ kind, lines, code string }{
		"line not booked": {"CORRECTION", `{"line_no":3,"product_id":"` + postProd + `","qty_delta":"1"}`, CodePostingOriginalMissing},
		"other product":   {"CORRECTION", `{"line_no":1,"product_id":"` + postOrder + `","qty_delta":"1"}`, CodePostingMismatch},
		"cases apart":     {"CORRECTION", `{"line_no":2,"product_id":"` + postProd + `","qty_delta":"1","cw_qty_delta":-1}`, CodePostingMismatch},
		"net differs":     {"CANCELLATION", `{"line_no":1,"product_id":"` + postProd + `","qty_delta":"-4"}`, CodePostingMismatch},
	} {
		err := h.Handle(ctx, nil, postingEvent(t, ReceiptCorrectedType, ReceiptSource, correctedData(tc.kind, 1, tc.lines)))
		if !eventing.IsPermanent(err) || !strings.Contains(err.Error(), tc.code) {
			t.Errorf("%s: %v, want permanent %s", name, err, tc.code)
		}
	}
	old := `{"receipt_id":"` + postReceipt + `","receipt_number":"RCV-1","kind":"CORRECTION","correction_seq":1,"currency":"USD","lines":[]}`
	if err := h.Handle(ctx, nil, postingEvent(t, ReceiptCorrectedType, ReceiptSource, old)); !eventing.IsPermanent(err) {
		t.Fatalf("event without legal_entity_id: %v, want permanent", err)
	}
}

func cancelledData(qty string) string {
	return `{"packing_slip_id":"99999999-9999-4999-8999-999999999999","packing_slip_number":"PS-1","reversal_id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","order_id":"` + postOrder +
		`","legal_entity_id":"` + postEntity + `","posting_date":"2026-10-28","currency":"USD","lines":[{"order_line_id":"` + postLine + `","qty":"` + qty + `","cost_amount":"-1"}]}`
}

// A cancelled slip books the mirror of the booked delivery: the weight, cases
// and value back at RECEIVED, linked to it. A delivery not booked yet is
// retried (it comes on another topic); a slip of another quantity parks.
func TestPostingsConsumer_ReversesTheBookedDeliveryOfACancelledSlip(t *testing.T) {
	f := &fakePostings{}
	h := PostingsConsumerHandlers(f, nil, domain.NegativePolicy{})[PackingSlipCancelledType]
	ctx := postingsCtx(t)
	evt := postingEvent(t, PackingSlipCancelledType, FulfillmentSource, cancelledData("-5"))
	err := h.Handle(ctx, nil, evt)
	if err == nil || eventing.IsPermanent(err) || !strings.Contains(err.Error(), CodePostingOriginalMissing) {
		t.Fatalf("delivery not booked: %v, want a retryable %s", err, CodePostingOriginalMissing)
	}
	f.book(sourceSalesLine, deliverySourceID(postTenant, postOrder, "PS-1", postLine), store.TransactionRow{ID: "issue-1", ProductID: postProd,
		Dims: domain.Dimensions{domain.DimSite: postSite}, Qty: d("-51"), EnteredQty: d("-5"), EnteredUnit: "CS", CWQty: cwPtr(5), Currency: "USD", CostAmountPhysical: d("-102")})
	if err := h.Handle(ctx, nil, evt); err != nil {
		t.Fatal(err)
	}
	in := f.got[0]
	if in.SourceType != "SALES_LINE" || !in.Qty.Equal(d("51")) || !in.EnteredQty.Equal(d("5")) || in.EnteredUnit != "CS" || in.CWQty != 5 || in.Status != domain.StatusReceived ||
		in.Value == nil || !in.Value.Equal(d("102")) || in.Corrects != "issue-1" || in.Currency != "USD" || in.Reference != "PS-1" || in.Date.Format("2006-01-02") != "2026-10-28" {
		t.Fatalf("reversal %+v, want +51 kg, 5 cases, 102 back at RECEIVED", in)
	}
	err = h.Handle(ctx, nil, postingEvent(t, PackingSlipCancelledType, FulfillmentSource, cancelledData("-4")))
	if !eventing.IsPermanent(err) || !strings.Contains(err.Error(), CodePostingMismatch) {
		t.Fatalf("other quantity: %v, want permanent %s", err, CodePostingMismatch)
	}
	if err := h.Handle(ctx, nil, postingEvent(t, PackingSlipCancelledType, "sales", cancelledData("-5"))); !eventing.IsPermanent(err) {
		t.Fatalf("foreign source: %v, want permanent", err)
	}
}
