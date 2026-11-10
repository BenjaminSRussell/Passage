package main

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/eventing"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/api"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/store"
	"github.com/BenjaminSRussell/passage/services/receivables/migrations"
)

const (
	legalEntityA = "00000000-0000-0000-0000-0000000000a1"
	legalEntityB = "00000000-0000-0000-0000-0000000000a2"
	customerID   = "00000000-0000-0000-0000-0000000000c1"
	invoiceID    = "00000000-0000-0000-0000-00000000b001"
	creditID     = "00000000-0000-0000-0000-00000000b002"
)

type fakeSource struct {
	mu        sync.Mutex
	pending   []eventing.Record
	committed []int64
	done      chan struct{}
}

func (f *fakeSource) Poll(ctx context.Context) ([]eventing.Record, error) {
	f.mu.Lock()
	batch := f.pending
	f.pending = nil
	f.mu.Unlock()
	if len(batch) > 0 {
		return batch, nil
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func (f *fakeSource) Commit(_ context.Context, r eventing.Record) error {
	f.mu.Lock()
	f.committed = append(f.committed, r.Offset)
	n := len(f.committed)
	f.mu.Unlock()
	if n == cap(f.done) {
		close(f.done)
	}
	return nil
}

type deadLetters struct {
	mu      sync.Mutex
	offsets map[int64]string
}

func (d *deadLetters) Send(_ context.Context, topic string, r eventing.Record, reason string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if topic != billingDeadLetterTopic {
		return errors.New("dead-lettered to " + topic)
	}
	d.offsets[r.Offset] = reason
	return nil
}

type flakyStore struct {
	inner    api.BillingStore
	failures atomic.Int32
}

func (f *flakyStore) ApplyBillingDocument(ctx context.Context, d store.BillingDocument) (int, error) {
	if f.failures.Add(-1) >= 0 {
		return 0, errors.New("connection reset")
	}
	return f.inner.ApplyBillingDocument(ctx, d)
}

func record(t *testing.T, offset int64, source, eventType, id string, data any) eventing.Record {
	t.Helper()
	evt, err := events.New(eventType, source, id, testkit.TenantID(60), data)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(evt)
	if err != nil {
		t.Fatal(err)
	}
	return eventing.Record{Topic: api.BillingInvoiceTopic, Offset: offset, Value: raw}
}

func invoiceData(le, number string, gross string, installments ...map[string]any) map[string]any {
	return map[string]any{"invoice_id": invoiceID, "number": number, "legal_entity_id": le, "customer": customerID, "currency": "USD",
		"invoice_date": "2027-03-31", "due_date": "2027-04-30", "totals": map[string]any{"gross": gross}, "installments": installments}
}

func creditData(le, number string) map[string]any {
	return map[string]any{"credit_note_id": creditID, "number": number, "original_invoice_id": invoiceID, "legal_entity_id": le,
		"customer": customerID, "currency": "USD", "credit_date": "2027-04-10", "posting_date": "2027-04-10", "totals": map[string]any{"gross": "150.00"}}
}

func installment(no int, amount, due string) map[string]any {
	return map[string]any{"no": no, "amount": amount, "due_date": due}
}

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

type countingWriter struct{ strings.Builder }

func (c *countingWriter) has(line string) bool { return strings.Contains(c.String(), line) }

type openRow struct {
	number, amount, due, entity string
}

func openRows(t *testing.T, st *store.Postgres, le string) []openRow {
	t.Helper()
	p, err := tenant.ServicePrincipal(api.BillingConsumerName, testkit.TenantID(60), le)
	if err != nil {
		t.Fatal(err)
	}
	items, err := st.ListOpenItems(tenant.With(context.Background(), p), store.OpenItemFilter{}, 50)
	if err != nil {
		t.Fatal(err)
	}
	var out []openRow
	for _, it := range items {
		out = append(out, openRow{it.DocumentNumber, it.Amount.StringFixed(2), it.DueDate, it.LegalEntityID})
	}
	slices.SortFunc(out, func(a, b openRow) int { return strings.Compare(a.number, b.number) })
	return out
}

func runConsumer(t *testing.T, st api.BillingStore, inbox eventing.Inbox, records ...eventing.Record) (*deadLetters, *eventing.Consumer) {
	t.Helper()
	src := &fakeSource{pending: records, done: make(chan struct{}, len(records))}
	dlq := &deadLetters{offsets: map[int64]string{}}
	cfg := billingConfig(st)
	cfg.Backoff = func(int) time.Duration { return time.Millisecond }
	c, err := eventing.NewConsumer(src, dlq, inbox, cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() { finished <- c.Run(ctx) }()
	select {
	case <-src.done:
	case err := <-finished:
		t.Fatalf("consumer stopped early: %v", err)
	case <-time.After(20 * time.Second):
		t.Fatalf("consumer committed %v of %d records", src.committed, len(records))
	}
	cancel()
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Fatalf("consumer ended with %v", err)
	}
	return dlq, c
}

func setupStore(t *testing.T) (*store.Postgres, eventing.Inbox) {
	t.Helper()
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return store.New(app), eventing.NewPgInbox(app)
}

func TestBillingConsumer_InvoiceOpensInstallmentsCreditNoteOpensCreditAndRedeliveryChangesNothing(t *testing.T) {
	st, inbox := setupStore(t)
	invoice := invoiceData(legalEntityA, "INV-2027-0000001", "1000.00",
		installment(1, "333.34", "2027-04-30"), installment(2, "333.33", "2027-05-30"), installment(3, "333.33", "2027-06-30"))
	inv := record(t, 1, "billing", api.InvoicePostedType, invoiceID, invoice)
	credit := record(t, 3, "billing", api.CreditNotePostedType, creditID, creditData(legalEntityA, "CN-2027-0000001"))
	redelivered := record(t, 2, "billing", api.InvoicePostedType, invoiceID, invoice)
	redelivered.Value = inv.Value

	dlq, c := runConsumer(t, st, inbox, inv, redelivered, credit)
	if len(dlq.offsets) != 0 {
		t.Fatalf("dead-lettered %v", dlq.offsets)
	}
	got := openRows(t, st, legalEntityA)
	want := []openRow{{"CN-2027-0000001", "-150.00", "2027-04-10", legalEntityA}, {"INV-2027-0000001/1", "333.34", "2027-04-30", legalEntityA},
		{"INV-2027-0000001/2", "333.33", "2027-05-30", legalEntityA}, {"INV-2027-0000001/3", "333.33", "2027-06-30", legalEntityA}}
	if len(got) != len(want) {
		t.Fatalf("open items %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("open item %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if got := openRows(t, st, legalEntityB); len(got) != 0 {
		t.Fatalf("legal entity B sees %+v", got)
	}
	var metrics countingWriter
	c.Metrics().Write(&metrics, api.BillingConsumerName)
	if !metrics.has(`event_consumer_applied_total{consumer="receivables-billing"} 2`) || !metrics.has(`event_consumer_duplicates_total{consumer="receivables-billing"} 1`) {
		t.Fatalf("metrics:\n%s", metrics.String())
	}
}

func TestBillingConsumer_ADatabaseFailureIsRetriedAndOpensTheItemsOnce(t *testing.T) {
	st, inbox := setupStore(t)
	flaky := &flakyStore{inner: st}
	flaky.failures.Store(2)
	rec := record(t, 1, "billing", api.InvoicePostedType, invoiceID, invoiceData(legalEntityA, "INV-2027-0000002", "400.00",
		installment(1, "250.00", "2027-04-30"), installment(2, "150.00", "2027-05-30")))
	dlq, c := runConsumer(t, flaky, inbox, rec)
	if len(dlq.offsets) != 0 {
		t.Fatalf("dead-lettered %v", dlq.offsets)
	}
	if got := openRows(t, st, legalEntityA); len(got) != 2 {
		t.Fatalf("open items %+v", got)
	}
	var metrics countingWriter
	c.Metrics().Write(&metrics, api.BillingConsumerName)
	if !metrics.has(`event_consumer_retries_total{consumer="receivables-billing"} 2`) {
		t.Fatalf("metrics:\n%s", metrics.String())
	}
}

func TestBillingConsumer_AnEventItCannotApplyIsParkedAndOpensNothing(t *testing.T) {
	st, inbox := setupStore(t)
	if _, err := st.CreateOpenItem(func() context.Context {
		return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(60), Duties: map[string]struct{}{tenant.DutyAllScope: {}}})
	}(), store.OpenItem{LegalEntityID: legalEntityA, CustomerID: customerID, SourceType: "INVOICE", DocumentNumber: "INV-2027-0000009",
		DocumentDate: "2027-03-31", DueDate: "2027-04-30", Currency: "USD", Amount: dec("10.00"), OpenAmount: dec("10.00")}); err != nil {
		t.Fatal(err)
	}
	notAnID := invoiceData(legalEntityA, "INV-2027-0000003", "100.00")
	notAnID["customer"] = "someone"
	wrongCurrency := invoiceData(legalEntityA, "INV-2027-0000004", "100.00")
	wrongCurrency["currency"] = "JPY"
	cases := []struct {
		name string
		rec  eventing.Record
	}{
		{"installments short of the gross", record(t, 1, "billing", api.InvoicePostedType, invoiceID, invoiceData(legalEntityA, "INV-2027-0000005", "1000.00",
			installment(1, "500.00", "2027-04-30"), installment(2, "499.99", "2027-05-30")))},
		{"customer is not an id", record(t, 2, "billing", api.InvoicePostedType, invoiceID, notAnID)},
		{"amount finer than the currency", record(t, 3, "billing", api.InvoicePostedType, invoiceID, wrongCurrency)},
		{"from another source", record(t, 4, "sales", api.InvoicePostedType, invoiceID, invoiceData(legalEntityA, "INV-2027-0000006", "100.00"))},
		{"no legal entity", record(t, 5, "billing", api.InvoicePostedType, invoiceID, invoiceData("", "INV-2027-0000007", "100.00"))},
		{"number taken by a hand-entered item", record(t, 6, "billing", api.InvoicePostedType, invoiceID, invoiceData(legalEntityA, "INV-2027-0000009", "100.00"))},
		{"credit note of zero", record(t, 7, "billing", api.CreditNotePostedType, creditID, func() map[string]any {
			d := creditData(legalEntityA, "CN-2027-0000002")
			d["totals"] = map[string]any{"gross": "0.00"}
			return d
		}())},
	}
	records := make([]eventing.Record, len(cases))
	for i, c := range cases {
		records[i] = c.rec
	}
	dlq, _ := runConsumer(t, st, inbox, records...)
	for _, c := range cases {
		if dlq.offsets[c.rec.Offset] != "permanent" {
			t.Errorf("%s: not parked (%v)", c.name, dlq.offsets)
		}
	}
	got := openRows(t, st, legalEntityA)
	if len(got) != 1 || got[0].number != "INV-2027-0000009" {
		t.Fatalf("a parked event left open items: %+v", got)
	}
}

func TestBillingConsumer_AnEventForAnotherLegalEntityStaysInThatEntity(t *testing.T) {
	st, inbox := setupStore(t)
	a := record(t, 1, "billing", api.InvoicePostedType, invoiceID, invoiceData(legalEntityA, "INV-2027-0000010", "100.00"))
	bData := invoiceData(legalEntityB, "INV-2027-0000010", "70.00")
	bData["invoice_id"] = "00000000-0000-0000-0000-00000000b003"
	b := record(t, 2, "billing", api.InvoicePostedType, "00000000-0000-0000-0000-00000000b003", bData)
	runConsumer(t, st, inbox, a, b)
	if got := openRows(t, st, legalEntityA); len(got) != 1 || got[0].amount != "100.00" {
		t.Fatalf("entity A %+v", got)
	}
	if got := openRows(t, st, legalEntityB); len(got) != 1 || got[0].amount != "70.00" {
		t.Fatalf("entity B %+v", got)
	}
}

func TestCommand_ConsumeStartsTheBillingConsumerAndNoArgumentServes(t *testing.T) {
	start, err := command([]string{"consume"})
	if err != nil || start == nil {
		t.Fatalf("consume: %v", err)
	}
	err = start(context.Background(), func(string) string { return "" })
	if err == nil || err.Error() != "DATABASE_URL is required" {
		t.Fatalf("consume without configuration ran: %v", err)
	}
	err = start(context.Background(), func(k string) string { return map[string]string{"DATABASE_URL": "postgres://x"}[k] })
	if err == nil || err.Error() != "KAFKA_BROKERS is required" {
		t.Fatalf("consume without brokers ran: %v", err)
	}
	if serve, err := command(nil); err != nil || serve == nil {
		t.Fatalf("serve: %v", err)
	}
	for _, args := range [][]string{{"consume", "extra"}, {"serve"}, {"-h"}} {
		if _, err := command(args); err == nil {
			t.Fatalf("%v accepted", args)
		}
	}
}

func TestBillingConsumer_StartsOnlyWhileItsPrincipalIsSeededAndSubscribesToBillingsInvoiceTopic(t *testing.T) {
	if billingSeeded(map[string][]string{}) || billingSeeded(map[string][]string{api.BillingConsumerName: {"other.duty"}}) {
		t.Fatal("unseeded consumer reported seeded")
	}
	if !billingSeeded(tenant.ServicePrincipalTable()) {
		t.Fatal("platform table does not seed service:" + api.BillingConsumerName)
	}
	cfg, err := billingKafkaConfig(func(k string) string { return map[string]string{"KAFKA_BROKERS": "kafka:9092"}[k] })
	if err != nil || cfg.Group != api.BillingConsumerName || len(cfg.Topics) != 1 || cfg.Topics[0] != "passage.billing.invoice" {
		t.Fatalf("kafka config %+v %v", cfg, err)
	}
	if _, err := billingKafkaConfig(func(string) string { return "" }); err == nil {
		t.Fatal("kafka config without KAFKA_BROKERS accepted")
	}
	for _, typ := range []string{"dev.passage.billing.invoice.posted.v1", "dev.passage.billing.creditnote.posted.v1"} {
		if _, ok := billingConfig(nil).Handlers[typ]; !ok {
			t.Fatalf("no handler for %s", typ)
		}
	}
}
