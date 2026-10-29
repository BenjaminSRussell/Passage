package consumer

import (
	"context"
	"errors"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/payables/internal/store"
	"github.com/BenjaminSRussell/passage/services/payables/migrations"
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
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: "*"})
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
