package consumer

import (
	"context"
	"errors"
	"github.com/shopspring/decimal"
	"testing"

	"github.com/BenjaminSRussell/depot/platform/dbx"
	"github.com/BenjaminSRussell/depot/platform/events"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/platform/testkit"
	"github.com/BenjaminSRussell/depot/services/payables/internal/party"
	"github.com/BenjaminSRussell/depot/services/payables/internal/store"
	"github.com/BenjaminSRussell/depot/services/payables/migrations"
)

const (
	le     = "00000000-0000-0000-0000-0000000000a1"
	vendor = "00000000-0000-0000-0000-0000000000c1"
	inv    = "00000000-0000-0000-0000-0000000000f1"
)

func setup(t *testing.T) *store.Postgres {
	t.Helper()
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return store.New(app)
}

func event(t *testing.T, typ string, data map[string]any) events.Event {
	t.Helper()
	evt, err := events.New(typ, "receiving", inv, testkit.TenantID(1), data)
	if err != nil {
		t.Fatal(err)
	}
	return evt
}

func payload(overrides map[string]any) map[string]any {
	d := map[string]any{"invoice_id": inv, "legal_entity_id": le, "vendor_id": vendor, "invoice_number": "INV-1",
		"invoice_date": "2027-03-01", "posting_date": "2027-03-02", "currency": "USD", "total": "1250.50", "ledger_entry_id": "le-1", "lines": []any{}}
	for k, v := range overrides {
		d[k] = v
	}
	return d
}

func TestHandleInvoicePosted_CreatesOpenItemOnceAcrossRedelivery(t *testing.T) {
	s := setup(t)
	evt := event(t, InvoicePostedType, payload(map[string]any{"due_date": "2027-03-31", "discount_date": "2027-03-11", "discount_amount": "25.01"}))
	for range 2 {
		if err := HandleInvoicePosted(context.Background(), s, evt); err != nil {
			t.Fatal(err)
		}
	}
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: le})
	items, err := s.List(ctx, store.Filter{VendorID: vendor}, 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("items = %+v err=%v", items, err)
	}
	got := items[0]
	if got.SourceInvoiceID != inv || got.Amount.String() != "1250.5" || got.DueDate != "2027-03-31" || got.DueDateSource != store.DueDateEvent || got.DiscountAmount.String() != "25.01" || *got.DiscountDate != "2027-03-11" {
		t.Fatalf("open item = %+v", got)
	}
}

func TestHandleInvoicePosted_DueDateDefaultsToInvoiceDate(t *testing.T) {
	s := setup(t)
	if err := HandleInvoicePosted(context.Background(), s, event(t, InvoicePostedType, payload(nil))); err != nil {
		t.Fatal(err)
	}
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), Duties: testkit.ScopeDuties("")})
	items, _ := s.List(ctx, store.Filter{}, 10)
	if len(items) != 1 || items[0].DueDate != "2027-03-01" || items[0].DueDateSource != store.DueDateDefaulted {
		t.Fatalf("items = %+v", items)
	}
}

func TestHandleInvoicePosted_RejectsInvalidEvents(t *testing.T) {
	s := setup(t)
	cases := map[string]events.Event{
		"wrong type":              event(t, events.Type("payables", "invoice", "voided", 1), payload(nil)),
		"zero total":              event(t, InvoicePostedType, payload(map[string]any{"total": "0"})),
		"bad total":               event(t, InvoicePostedType, payload(map[string]any{"total": "abc"})),
		"discount > total":        event(t, InvoicePostedType, payload(map[string]any{"discount_amount": "9999"})),
		"bad invoice date":        event(t, InvoicePostedType, payload(map[string]any{"invoice_date": "03/01/2027"})),
		"missing vendor":          event(t, InvoicePostedType, payload(map[string]any{"vendor_id": ""})),
		"lowercase currency":      event(t, InvoicePostedType, payload(map[string]any{"currency": "usd"})),
		"four letter currency":    event(t, InvoicePostedType, payload(map[string]any{"currency": "USDX"})),
		"non-uuid invoice id":     event(t, InvoicePostedType, payload(map[string]any{"invoice_id": "inv-1"})),
		"non-uuid vendor id":      event(t, InvoicePostedType, payload(map[string]any{"vendor_id": "v1"})),
		"non-uuid legal entity":   event(t, InvoicePostedType, payload(map[string]any{"legal_entity_id": "le"})),
		"malformed discount date": event(t, InvoicePostedType, payload(map[string]any{"discount_date": "11/03/2027", "discount_amount": "5"})),
		"discount date alone":     event(t, InvoicePostedType, payload(map[string]any{"discount_date": "2027-03-11"})),
		"discount amount alone":   event(t, InvoicePostedType, payload(map[string]any{"discount_amount": "5"})),
		"seven decimal total":     event(t, InvoicePostedType, payload(map[string]any{"total": "1.0000001"})),
		"seven decimal discount":  event(t, InvoicePostedType, payload(map[string]any{"discount_date": "2027-03-11", "discount_amount": "1.0000001"})),
		"non-uuid tenant":         {Type: InvoicePostedType, TenantID: "t1", Data: event(t, InvoicePostedType, payload(nil)).Data},
		"missing tenant id":       {Type: InvoicePostedType, Data: event(t, InvoicePostedType, payload(nil)).Data},
	}
	for name, evt := range cases {
		if err := HandleInvoicePosted(context.Background(), s, evt); !errors.Is(err, ErrInvalidEvent) {
			t.Errorf("%s: err = %v, want ErrInvalidEvent", name, err)
		}
	}
}

func TestHandleInvoicePosted_StoresVendorGroupAndBackfillsOnRedelivery(t *testing.T) {
	const group = "00000000-0000-0000-0000-0000000000b1"
	s := setup(t)
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: le})
	if err := HandleInvoicePosted(context.Background(), s, event(t, InvoicePostedType, payload(nil))); err != nil {
		t.Fatal(err)
	}
	if items, _ := s.List(ctx, store.Filter{}, 10); len(items) != 1 || items[0].VendorGroupID != "" {
		t.Fatalf("legacy event must leave the group empty: %+v", items)
	}
	if err := HandleInvoicePosted(context.Background(), s, event(t, InvoicePostedType, payload(map[string]any{"vendor_group_id": group}))); err != nil {
		t.Fatal(err)
	}
	if items, _ := s.List(ctx, store.Filter{}, 10); len(items) != 1 || items[0].VendorGroupID != group {
		t.Fatalf("redelivery with a group must backfill: %+v", items)
	}
	err := HandleInvoicePosted(context.Background(), s, event(t, InvoicePostedType, payload(map[string]any{"vendor_group_id": "nope"})))
	if !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("bad vendor_group_id err = %v, want ErrInvalidEvent", err)
	}
	if items, _ := s.List(ctx, store.Filter{}, 10); items[0].VendorGroupCode != "" {
		t.Fatalf("no code was sent: %+v", items)
	}
	if err := HandleInvoicePosted(context.Background(), s, event(t, InvoicePostedType, payload(map[string]any{"vendor_group_id": group, "vendor_group_code": "DOM"}))); err != nil {
		t.Fatal(err)
	}
	if items, _ := s.List(ctx, store.Filter{}, 10); items[0].VendorGroupCode != "DOM" {
		t.Fatalf("redelivery with a code must fill it: %+v", items)
	}
	for _, bad := range []string{"dom", "TOO-LONG-CODE", "A B"} {
		if err := HandleInvoicePosted(context.Background(), s, event(t, InvoicePostedType, payload(map[string]any{"vendor_group_code": bad}))); !errors.Is(err, ErrInvalidEvent) {
			t.Fatalf("vendor_group_code %q err = %v, want ErrInvalidEvent", bad, err)
		}
	}
}

type fakeTerms struct {
	dates  party.TermDates
	err    error
	called int
	gotLE  string
	gotDay string
}

func (f *fakeTerms) TermDates(_ context.Context, legalEntityID, _, documentDate string) (party.TermDates, error) {
	f.called++
	f.gotLE, f.gotDay = legalEntityID, documentDate
	return f.dates, f.err
}

const termID = "00000000-0000-0000-0000-0000000000d1"

func twoTenNetThirty() *fakeTerms {
	return &fakeTerms{dates: party.TermDates{DueDate: "2027-03-31", Discounts: []party.DiscountDate{{Percent: decimal.NewFromInt(2), Date: "2027-03-11"}}}}
}

func openItems(t *testing.T, s *store.Postgres) []store.OpenItem {
	t.Helper()
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: le})
	items, err := s.List(ctx, store.Filter{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	return items
}

func TestHandleInvoicePosted_DatesFromTerm(t *testing.T) {
	s := setup(t)
	f := twoTenNetThirty()
	if err := HandleInvoicePostedWithTerms(context.Background(), s, f, event(t, InvoicePostedType, payload(map[string]any{"payment_term_id": termID}))); err != nil {
		t.Fatal(err)
	}
	items := openItems(t, s)
	if len(items) != 1 || f.gotLE != le || f.gotDay != "2027-03-01" {
		t.Fatalf("items = %+v resolver saw %s %s", items, f.gotLE, f.gotDay)
	}
	got := items[0]
	if got.DueDate != "2027-03-31" || got.DueDateSource != store.DueDateTerms || got.DiscountDate == nil || *got.DiscountDate != "2027-03-11" || got.DiscountAmount.String() != "25.01" {
		t.Fatalf("2/10 net 30 on 1250.50 must give due 2027-03-31, discount 2027-03-11 of 25.01: %+v", got)
	}
}

func TestHandleInvoicePosted_ShiftedDueDateFromPartyIsStored(t *testing.T) {
	s := setup(t)
	f := &fakeTerms{dates: party.TermDates{DueDate: "2027-04-01"}}
	if err := HandleInvoicePostedWithTerms(context.Background(), s, f, event(t, InvoicePostedType, payload(map[string]any{"payment_term_id": termID}))); err != nil {
		t.Fatal(err)
	}
	if items := openItems(t, s); len(items) != 1 || items[0].DueDate != "2027-04-01" || items[0].DiscountDate != nil || !items[0].DiscountAmount.IsZero() {
		t.Fatalf("items = %+v", items)
	}
}

func TestHandleInvoicePosted_TermLookupFailureIsRetriedNotDefaulted(t *testing.T) {
	s := setup(t)
	for name, resolveErr := range map[string]error{"party down": errors.New("party: 503"), "unknown term": party.ErrTermNotFound} {
		f := &fakeTerms{err: resolveErr}
		err := HandleInvoicePostedWithTerms(context.Background(), s, f, event(t, InvoicePostedType, payload(map[string]any{"payment_term_id": termID})))
		if !errors.Is(err, resolveErr) || errors.Is(err, ErrInvalidEvent) {
			t.Fatalf("%s: err = %v, want the resolver error so the event is retried", name, err)
		}
	}
	if items := openItems(t, s); len(items) != 0 {
		t.Fatalf("no open item may be created from a failed lookup: %+v", items)
	}
}

func TestHandleInvoicePosted_ExplicitDueDateBeatsTerm(t *testing.T) {
	s := setup(t)
	f := twoTenNetThirty()
	evt := event(t, InvoicePostedType, payload(map[string]any{"payment_term_id": termID, "due_date": "2027-05-05"}))
	if err := HandleInvoicePostedWithTerms(context.Background(), s, f, evt); err != nil {
		t.Fatal(err)
	}
	if items := openItems(t, s); f.called != 0 || len(items) != 1 || items[0].DueDate != "2027-05-05" || items[0].DueDateSource != store.DueDateEvent {
		t.Fatalf("resolver called %d times, items %+v", f.called, items)
	}
}

func TestHandleInvoicePosted_TermIdIsValidated(t *testing.T) {
	s := setup(t)
	f := twoTenNetThirty()
	for name, evt := range map[string]events.Event{
		"non-uuid term":   event(t, InvoicePostedType, payload(map[string]any{"payment_term_id": "net30"})),
		"no invoice date": event(t, InvoicePostedType, payload(map[string]any{"payment_term_id": termID, "invoice_date": ""})),
	} {
		if err := HandleInvoicePostedWithTerms(context.Background(), s, f, evt); !errors.Is(err, ErrInvalidEvent) {
			t.Errorf("%s: err = %v, want ErrInvalidEvent", name, err)
		}
	}
	if f.called != 0 {
		t.Fatalf("an invalid event must not reach party: %d calls", f.called)
	}
}
