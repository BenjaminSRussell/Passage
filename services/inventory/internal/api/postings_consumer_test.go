package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/eventing"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
)

const (
	postTenant = "11111111-1111-4111-8111-111111111111"
	postEntity = "22222222-2222-4222-8222-222222222222"
	postSite   = "33333333-3333-4333-8333-333333333333"
	postProd   = "44444444-4444-4444-8444-444444444444"
)

type fakePostings struct {
	got  []store.CreateTransactionInput
	errs []error
	cost decimal.Decimal
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

const twoReceiptLines = `{"receipt_id":"55555555-5555-4555-8555-555555555555","receipt_number":"RCV-1","legal_entity_id":"` + postEntity + `","posting_date":"2026-10-25","currency":"USD","lines":[` +
	`{"line_no":1,"product_id":"` + postProd + `","unit":"kg","qty":"102","serials":[],"inventory_status":"QUARANTINE","unit_price":"2","site_id":"` + postSite + `","cw_qty":10},` +
	`{"line_no":2,"product_id":"` + postProd + `","unit":"EA","qty":"4","serials":[],"inventory_status":"AVAILABLE","unit_price":"3","site_id":"` + postSite + `"}]}`

// A receipt books its catch-weight line only, at the weight with the cases, in
// the document's legal entity, under a quarantine status dimension; the plain
// line is left alone (no unit conversion yet).
func TestPostingsConsumer_BooksCatchWeightReceiptLines(t *testing.T) {
	f := &fakePostings{}
	h := PostingsConsumerHandlers(f, domain.NegativePolicy{})
	if err := h[ReceiptPostedType].Handle(postingsCtx(t), nil, postingEvent(t, ReceiptPostedType, ReceiptSource, twoReceiptLines)); err != nil {
		t.Fatal(err)
	}
	if len(f.got) != 1 {
		t.Fatalf("%d bookings, want 1", len(f.got))
	}
	in := f.got[0]
	if in.SourceType != "RECEIPT_LINE" || !in.Qty.Equal(decimal.NewFromInt(102)) || in.CWQty != 10 || in.EnteredUnit != "KG" || in.Status != domain.StatusReceived ||
		!in.CostPrice.Equal(decimal.NewFromInt(2)) || in.Dims[domain.DimStatus] != "QUARANTINE" || in.Dims[domain.DimSite] != postSite || in.LegalEntityID != postEntity {
		t.Fatalf("booking %+v", in)
	}
	if in.SourceID != postingSourceID(postTenant, "receipt-line", "55555555-5555-4555-8555-555555555555", "1") || in.IdempotencyKey == "" {
		t.Fatalf("origin %s key %s are not derived from the receipt line", in.SourceID, in.IdempotencyKey)
	}
}

// A delivery issues the reported weight with the cases at the row's average in
// the row's currency; an invoice report books nothing.
func TestPostingsConsumer_IssuesTheDeliveredWeight(t *testing.T) {
	f := &fakePostings{cost: decimal.RequireFromString("2.5")}
	h := PostingsConsumerHandlers(f, domain.NegativePolicy{PhysicalAllowed: true})
	data := `{"progress_id":"66666666-6666-4666-8666-666666666666","order_id":"77777777-7777-4777-8777-777777777777","legal_entity_id":"` + postEntity +
		`","currency":"USD","kind":"%s","source_ref":"PS-1","lines":[{"line_id":"88888888-8888-4888-8888-888888888888","line_no":1,"quantity":"5","product_id":"` + postProd +
		`","unit":"CS","site_id":"` + postSite + `","reported_weight":"51"}]}`
	if err := h[OrderProgressRecordedType].Handle(postingsCtx(t), nil, postingEvent(t, OrderProgressRecordedType, SalesSource, fmt.Sprintf(data, "INVOICED"))); err != nil || len(f.got) != 0 {
		t.Fatalf("invoice report: %v, %d bookings", err, len(f.got))
	}
	if err := h[OrderProgressRecordedType].Handle(postingsCtx(t), nil, postingEvent(t, OrderProgressRecordedType, SalesSource, fmt.Sprintf(data, "DELIVERED"))); err != nil {
		t.Fatal(err)
	}
	in := f.got[0]
	if in.SourceType != "SALES_LINE" || !in.Qty.Equal(decimal.NewFromInt(-51)) || in.CWQty != 5 || !in.EnteredQty.Equal(decimal.NewFromInt(-5)) || in.EnteredUnit != "CS" ||
		in.Status != domain.StatusDeducted || in.Currency != "EUR" || !in.CostPrice.Equal(decimal.RequireFromString("2.5")) || !in.Negative.PhysicalAllowed {
		t.Fatalf("booking %+v", in)
	}
}

// A store failure that may pass is retried (not permanent); a rule of
// inventory, a foreign source, malformed data or a missing principal parks.
func TestPostingsConsumer_RetriesTransientAndParksPermanentFailures(t *testing.T) {
	ctx := postingsCtx(t)
	evt := postingEvent(t, ReceiptPostedType, ReceiptSource, twoReceiptLines)
	transient := &fakePostings{errs: []error{errors.New("connection reset")}}
	if err := PostingsConsumerHandlers(transient, domain.NegativePolicy{})[ReceiptPostedType].Handle(ctx, nil, evt); err == nil || eventing.IsPermanent(err) {
		t.Fatalf("transient store error: %v, want a retryable error", err)
	}
	rule := &fakePostings{errs: []error{httpx.NewProblem(422, "INV-NEG-001", "negative")}}
	if err := PostingsConsumerHandlers(rule, domain.NegativePolicy{})[ReceiptPostedType].Handle(ctx, nil, evt); !eventing.IsPermanent(err) {
		t.Fatalf("business rule: %v, want permanent", err)
	}
	inFlight := &fakePostings{errs: []error{httpx.NewProblem(409, store.CodeKeyInFlight, "in flight")}}
	if err := PostingsConsumerHandlers(inFlight, domain.NegativePolicy{})[ReceiptPostedType].Handle(ctx, nil, evt); err == nil || eventing.IsPermanent(err) {
		t.Fatalf("key in flight: %v, want a retryable error", err)
	}
	h := PostingsConsumerHandlers(&fakePostings{}, domain.NegativePolicy{})[ReceiptPostedType]
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
