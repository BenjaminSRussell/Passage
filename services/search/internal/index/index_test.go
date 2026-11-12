package index

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/eventing"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/search/internal/store"
)

const (
	tenantID = "00000000-0000-0000-0000-000000000001"
	entityID = "00000000-0000-0000-0000-0000000000a1"
	recordID = "00000000-0000-0000-0000-0000000000c1"
	otherID  = "00000000-0000-0000-0000-0000000000c2"
)

var eventTime = time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)

type applied struct {
	rec       store.Record
	principal tenant.Principal
}

type fakeStore struct {
	got []applied
	err error
}

func (f *fakeStore) Apply(ctx context.Context, r store.Record) (bool, error) {
	p, _ := tenant.FromContext(ctx)
	f.got = append(f.got, applied{r, p})
	return true, f.err
}

func event(t *testing.T, typ, source string, data any) events.Event {
	t.Helper()
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	return events.Event{ID: "evt-1", Type: typ, Source: source, TenantID: tenantID, Time: eventTime, Data: raw}
}

func handle(t *testing.T, s Applier, evt events.Event) error {
	t.Helper()
	h, ok := Handlers(s)[evt.Type]
	if !ok {
		t.Fatalf("no handler for %s", evt.Type)
	}
	return h.Handle(context.Background(), nil, evt)
}

func TestHandlersCoverEverySubscribedEventType(t *testing.T) {
	for _, typ := range []string{PartyCreatedType, PartyUpdatedType, PartyMergedType, ProductCreatedType, ProductUpdatedType, PurchaseOrderConfirmed, PurchaseOrderCancelled,
		ReceiptPostedType, ReceiptCorrectedType, VendorInvoicePostedType, SalesOrderConfirmedType, PackingSlipPostedType, PackingSlipCancelledType, CustomerInvoicePostedType, CreditNotePostedType,
		ProductionOrderCreatedType, ProductionOrderStatusChangedType, WorkerCreatedType} {
		if _, ok := Handlers(&fakeStore{})[typ]; !ok {
			t.Errorf("no handler for %s", typ)
		}
	}
}

func TestIndexesEachRecordKindFromItsEvent(t *testing.T) {
	for _, tc := range []struct {
		name string
		evt  events.Event
		want store.Record
		le   string
	}{
		{"party created", event(t, PartyCreatedType, partySource, map[string]any{"party_id": recordID, "party_number": "P-1", "party_type": "organization", "name": "Contoso", "status": "active", "version": 3}),
			store.Record{Type: store.TypeParty, ID: recordID, Number: "P-1", Title: "Contoso", Subtitle: "organization", Status: "active", Version: 3, At: eventTime}, ""},
		{"product updated", event(t, ProductUpdatedType, productSource, map[string]any{"productId": recordID, "productNumber": "SKU-9", "name": "Widget", "productType": "finished", "lifecycleState": "active", "version": 4}),
			store.Record{Type: store.TypeProduct, ID: recordID, Number: "SKU-9", Title: "Widget", Subtitle: "finished", Status: "active", Version: 4, At: eventTime}, ""},
		{"po confirmed", event(t, PurchaseOrderConfirmed, purchasingSource, map[string]any{"po_id": recordID, "number": "PO-5", "legal_entity_id": entityID, "currency": "USD", "total": "12.50", "version_no": 2}),
			store.Record{Type: store.TypePurchaseOrder, ID: recordID, LegalEntityID: entityID, Number: "PO-5", Title: "PO-5", Subtitle: "USD 12.50", Status: "CONFIRMED", Version: 2, At: eventTime}, entityID},
		{"po cancelled", event(t, PurchaseOrderCancelled, purchasingSource, map[string]any{"po_id": recordID, "number": "PO-5", "legal_entity_id": entityID, "version_no": 2, "prior_status": "CONFIRMED"}),
			store.Record{Type: store.TypePurchaseOrder, ID: recordID, LegalEntityID: entityID, Number: "PO-5", Title: "PO-5", Status: "CANCELLED", Version: 2, At: eventTime}, entityID},
		{"po draft cancelled", event(t, PurchaseOrderCancelled, purchasingSource, map[string]any{"po_id": recordID, "number": "PO-5", "legal_entity_id": entityID, "version_no": 1, "prior_status": "DRAFT"}),
			store.Record{Type: store.TypePurchaseOrder, ID: recordID, LegalEntityID: entityID, Number: "PO-5", Title: "PO-5", Status: "CANCELLED", Version: 1, UpdateOnly: true, At: eventTime}, entityID},
		{"receipt posted", event(t, ReceiptPostedType, receivingSource, map[string]any{"receipt_id": recordID, "receipt_number": "RC-7", "legal_entity_id": entityID, "posting_date": "2026-03-01"}),
			store.Record{Type: store.TypeReceipt, ID: recordID, LegalEntityID: entityID, Number: "RC-7", Title: "RC-7", Subtitle: "2026-03-01", Status: "POSTED", At: eventTime}, entityID},
		{"receipt cancelled", event(t, ReceiptCorrectedType, receivingSource, map[string]any{"receipt_id": recordID, "receipt_number": "RC-7", "legal_entity_id": entityID, "kind": "CANCELLATION"}),
			store.Record{Type: store.TypeReceipt, ID: recordID, LegalEntityID: entityID, Number: "RC-7", Status: "CANCELLED", UpdateOnly: true, At: eventTime}, entityID},
		{"vendor invoice posted", event(t, VendorInvoicePostedType, receivingSource, map[string]any{"invoice_id": recordID, "invoice_number": "VI-3", "legal_entity_id": entityID, "currency": "EUR", "total": "99.00"}),
			store.Record{Type: store.TypeVendorInvoice, ID: recordID, LegalEntityID: entityID, Number: "VI-3", Title: "VI-3", Subtitle: "EUR 99.00", Status: "POSTED", At: eventTime}, entityID},
		{"sales order confirmed", event(t, SalesOrderConfirmedType, salesSource, map[string]any{"order_id": recordID, "order_number": "SO-11", "legal_entity_id": entityID, "currency": "USD", "net_total": "5.00", "version": 2}),
			store.Record{Type: store.TypeSalesOrder, ID: recordID, LegalEntityID: entityID, Number: "SO-11", Title: "SO-11", Subtitle: "USD 5.00", Status: "CONFIRMED", Version: 2, At: eventTime}, entityID},
		{"packing slip posted", event(t, PackingSlipPostedType, fulfillmentSource, map[string]any{"packing_slip_id": recordID, "packing_slip_number": "PS-2", "legal_entity_id": entityID, "posting_date": "2026-03-02"}),
			store.Record{Type: store.TypePackingSlip, ID: recordID, LegalEntityID: entityID, Number: "PS-2", Title: "PS-2", Subtitle: "2026-03-02", Status: "POSTED", At: eventTime}, entityID},
		{"packing slip cancelled", event(t, PackingSlipCancelledType, fulfillmentSource, map[string]any{"packing_slip_id": recordID, "packing_slip_number": "PS-2", "legal_entity_id": entityID}),
			store.Record{Type: store.TypePackingSlip, ID: recordID, LegalEntityID: entityID, Number: "PS-2", Status: "CANCELLED", UpdateOnly: true, At: eventTime}, entityID},
		{"customer invoice posted", event(t, CustomerInvoicePostedType, billingSource, map[string]any{"invoice_id": recordID, "number": "INV-8", "legal_entity_id": entityID, "currency": "USD", "totals": map[string]any{"gross": "10.00"}}),
			store.Record{Type: store.TypeCustomerInvoice, ID: recordID, LegalEntityID: entityID, Number: "INV-8", Title: "INV-8", Subtitle: "USD 10.00", Status: "POSTED", At: eventTime}, entityID},
		{"credit note posted", event(t, CreditNotePostedType, billingSource, map[string]any{"credit_note_id": recordID, "number": "CN-1", "legal_entity_id": entityID, "currency": "USD", "totals": map[string]any{"gross": "1.00"}}),
			store.Record{Type: store.TypeCustomerInvoice, ID: recordID, LegalEntityID: entityID, Number: "CN-1", Title: "CN-1", Subtitle: "USD 1.00", Status: "POSTED", At: eventTime}, entityID},
		{"production order created", event(t, ProductionOrderCreatedType, productionSource, map[string]any{"orderId": recordID, "orderNumber": "PO-000004", "legalEntityId": entityID, "status": "Created", "deliveryDate": "2026-11-30"}),
			store.Record{Type: store.TypeProductionOrder, ID: recordID, LegalEntityID: entityID, Number: "PO-000004", Title: "PO-000004", Status: "CREATED", At: eventTime}, entityID},
		{"production order status changed", event(t, ProductionOrderStatusChangedType, productionSource, map[string]any{"orderId": recordID, "orderNumber": "PO-000004", "legalEntityId": entityID, "from": "Started", "to": "ReportedAsFinished"}),
			store.Record{Type: store.TypeProductionOrder, ID: recordID, LegalEntityID: entityID, Number: "PO-000004", Title: "PO-000004", Status: "REPORTED_AS_FINISHED", At: eventTime}, entityID},
		{"worker created", event(t, WorkerCreatedType, hrSource, map[string]any{"id": recordID, "personnel_number": "WRK-000012", "worker_type": "EMPLOYEE", "party_person_id": otherID}),
			store.Record{Type: store.TypeWorker, ID: recordID, Number: "WRK-000012", Title: "WRK-000012", Subtitle: "employee", PartyID: otherID, At: eventTime}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeStore{}
			if err := handle(t, f, tc.evt); err != nil {
				t.Fatal(err)
			}
			if len(f.got) != 1 || f.got[0].rec != tc.want {
				t.Fatalf("applied %+v, want %+v", f.got, tc.want)
			}
			if tc.le != "" && (f.got[0].principal.TenantID != tenantID || f.got[0].principal.LegalEntityID != tc.le) {
				t.Fatalf("principal %+v, want tenant %s scoped to %s", f.got[0].principal, tenantID, tc.le)
			}
		})
	}
}

const survivorID = "00000000-0000-0000-0000-0000000000c3"

func mergeData(losers ...string) map[string]any {
	return map[string]any{"survivorPartyId": survivorID, "mergedPartyIds": losers,
		"survivor": map[string]any{"partyNumber": "P-1", "partyType": "organization", "name": "Contoso Group", "status": "active", "version": 5}}
}

func TestMergeRemovesLosersAndReindexesTheSurvivor(t *testing.T) {
	f := &fakeStore{}
	if err := handle(t, f, event(t, PartyMergedType, partySource, mergeData(recordID, otherID))); err != nil {
		t.Fatal(err)
	}
	if len(f.got) != 3 {
		t.Fatalf("applied %d records, want 2 removals and the survivor", len(f.got))
	}
	// The survivor first, so the merged persons' workers take its name as they move to it.
	want := store.Record{Type: store.TypeParty, ID: survivorID, Number: "P-1", Title: "Contoso Group", Subtitle: "organization", Status: "active", Version: 5, At: eventTime}
	if f.got[0].rec != want {
		t.Errorf("survivor = %+v, want %+v", f.got[0].rec, want)
	}
	for i, id := range []string{recordID, otherID} {
		if r := f.got[i+1].rec; r.ID != id || !r.Removed || r.Status != "MERGED" || r.MergedInto != survivorID || r.At != eventTime {
			t.Errorf("record %d = %+v", i+1, r)
		}
	}
}

func TestMergeWithoutSurvivorDataStillRemovesTheLosers(t *testing.T) {
	f := &fakeStore{}
	data := map[string]any{"mergedPartyIds": []string{recordID, otherID}}
	if err := handle(t, f, event(t, PartyMergedType, partySource, data)); err != nil {
		t.Fatal(err)
	}
	if len(f.got) != 2 || !f.got[0].rec.Removed || !f.got[1].rec.Removed {
		t.Fatalf("applied %+v, want only the 2 removals", f.got)
	}
}

func TestEverySubscribedTopicMatchesTheSourceOutboxAggregate(t *testing.T) {
	for _, tc := range []struct{ topic, file, pattern string }{
		{PartyTopic, "../../../party/internal/domain/merge.go", `AggregateParty\s*=\s*"([a-z_]+)"`},
		{ProductTopic, "../../../product/internal/store/store.go", `emit\(ctx, tx, pevents\.ProductCreated, "([a-z_]+)"`},
		{ProductTopic, "../../../product/internal/store/store.go", `emit\(ctx, tx, pevents\.ProductUpdated, "([a-z_]+)"`},
		{PurchaseOrderTopic, "../../../purchasing/internal/store/po_lifecycle.go", `enqueue\(ctx, tx, "([a-z_]+)", po\.ID, purchasingevents\.POConfirmed`},
		{PurchaseOrderTopic, "../../../purchasing/internal/store/po_lifecycle.go", `enqueue\(ctx, tx, "([a-z_]+)", cur\.ID, purchasingevents\.POCancelled`},
		{ReceiptTopic, "../../../receiving/internal/store/receipts.go", `receiptAggregate\s*=\s*"([a-z_]+)"`},
		{VendorInvoiceTopic, "../../../receiving/internal/store/invoices.go", `invoiceAggregate\s*=\s*"([a-z_]+)"`},
		{SalesOrderTopic, "../../../sales/internal/store/store.go", `enqueue\(ctx, tx, "([a-z_]+)", o\.ID, salesevents\.OrderConfirmed`},
		{ShipmentTopic, "../../../fulfillment/internal/store/store.go", `enqueueFor\(ctx, tx, "([a-z_]+)", aggregateID`},
		{BillingInvoiceTopic, "../../../billing/internal/store/store.go", `outbox\.Enqueue\(ctx, tx, "([a-z_]+)", inv\.ID, evt\)`},
		{WorkerTopic, "../../../hr/internal/store/store.go", `enqueue\(ctx, tx, "([a-z_]+)", w\.ID, hrevents\.WorkerCreated`},
	} {
		src, err := os.ReadFile(tc.file)
		if err != nil {
			t.Fatal(err)
		}
		m := regexp.MustCompile(tc.pattern).FindSubmatch(src)
		if m == nil {
			t.Fatalf("%s: no outbox call matches %s", tc.file, tc.pattern)
		}
		if got := string(m[1]); !strings.HasSuffix(tc.topic, "."+got) {
			t.Errorf("topic %s does not end in the outbox aggregate type %q of %s", tc.topic, got, tc.file)
		}
	}
}

// Production writes every event under one outbox aggregate type and names its
// topics after the event type's entity (its AsyncAPI channels; scheduling reads
// the same order channel), so the order topic is tied to the channel address
// and the event type. HR's worker channel is checked the same way.
func TestProductionAndWorkerTopicsMatchTheSourceChannels(t *testing.T) {
	for _, tc := range []struct{ topic, eventType, file string }{
		{ProductionOrderTopic, ProductionOrderCreatedType, "../../../production/events/asyncapi.yaml"},
		{ProductionOrderTopic, ProductionOrderStatusChangedType, "../../../production/events/asyncapi.yaml"},
		{WorkerTopic, WorkerCreatedType, "../../../hr/events/asyncapi.yaml"},
	} {
		if got := events.Topic(tc.eventType); got != tc.topic {
			t.Errorf("%s is published on %s, the consumer reads %s", tc.eventType, got, tc.topic)
		}
		spec, err := os.ReadFile(tc.file)
		if err != nil {
			t.Fatal(err)
		}
		if !regexp.MustCompile(`(?m)^\s+address: ` + regexp.QuoteMeta(tc.topic) + `\s*$`).Match(spec) {
			t.Errorf("%s has no channel with address %s", tc.file, tc.topic)
		}
	}
}

// The production and HR payloads are read by JSON name; a renamed or dropped
// field in the producer would leave the index empty without an error.
func TestSourcePayloadsCarryTheFieldsTheIndexReads(t *testing.T) {
	for _, tc := range []struct {
		file, payload string
		tags          []string
	}{
		{"../../../production/internal/events/events.go", "OrderCreatedData", []string{"orderId", "orderNumber", "legalEntityId", "status"}},
		{"../../../production/internal/events/events.go", "OrderStatusChangedData", []string{"orderId", "orderNumber", "legalEntityId", "to"}},
		{"../../../hr/internal/events/events.go", "WorkerCreatedData", []string{"id", "personnel_number", "worker_type", "party_person_id"}},
	} {
		src, err := os.ReadFile(tc.file)
		if err != nil {
			t.Fatal(err)
		}
		m := regexp.MustCompile(`(?s)type ` + tc.payload + ` struct \{(.*?)\n\}`).FindSubmatch(src)
		if m == nil {
			t.Fatalf("%s: no struct %s", tc.file, tc.payload)
		}
		for _, tag := range tc.tags {
			if !strings.Contains(string(m[1]), `json:"`+tag+`"`) {
				t.Errorf("%s.%s has no field %q", tc.file, tc.payload, tag)
			}
		}
	}
}

func TestMalformedEventsAreDeadLettered(t *testing.T) {
	good := map[string]any{"party_id": recordID, "party_number": "P-1"}
	noTime := event(t, PartyCreatedType, partySource, good)
	noTime.Time = time.Time{}
	for name, evt := range map[string]events.Event{
		"wrong source":                  event(t, PartyCreatedType, "/passage/other", good),
		"no time":                       noTime,
		"bad json":                      {Type: PartyCreatedType, Source: partySource, Time: eventTime, Data: []byte("{")},
		"party no number":               event(t, PartyUpdatedType, partySource, map[string]any{"party_id": recordID}),
		"party bad id":                  event(t, PartyUpdatedType, partySource, map[string]any{"party_id": "x", "party_number": "P"}),
		"product no number":             event(t, ProductCreatedType, productSource, map[string]any{"productId": recordID}),
		"merge empty":                   event(t, PartyMergedType, partySource, mergeData()),
		"merge bad survivor":            event(t, PartyMergedType, partySource, map[string]any{"survivorPartyId": "x", "mergedPartyIds": []string{recordID}}),
		"merge bad id":                  event(t, PartyMergedType, partySource, mergeData("x")),
		"po no entity":                  event(t, PurchaseOrderConfirmed, purchasingSource, map[string]any{"po_id": recordID, "number": "PO-1"}),
		"receipt no entity":             event(t, ReceiptPostedType, receivingSource, map[string]any{"receipt_id": recordID, "receipt_number": "RC-1"}),
		"slip wrong source":             event(t, PackingSlipPostedType, "billing", map[string]any{"packing_slip_id": recordID, "packing_slip_number": "PS", "legal_entity_id": entityID}),
		"po no number":                  event(t, PurchaseOrderCancelled, purchasingSource, map[string]any{"po_id": recordID, "legal_entity_id": entityID}),
		"production order no entity":    event(t, ProductionOrderStatusChangedType, productionSource, map[string]any{"orderId": recordID, "orderNumber": "PO-000001", "to": "Released"}),
		"production order wrong source": event(t, ProductionOrderCreatedType, "planning", map[string]any{"orderId": recordID, "orderNumber": "PO-000001", "legalEntityId": entityID}),
		"worker no number":              event(t, WorkerCreatedType, hrSource, map[string]any{"id": recordID}),
		"worker bad id":                 event(t, WorkerCreatedType, hrSource, map[string]any{"id": "x", "personnel_number": "WRK-1"}),
		"worker bad party person":       event(t, WorkerCreatedType, hrSource, map[string]any{"id": recordID, "personnel_number": "WRK-1", "party_person_id": "x"}),
	} {
		f := &fakeStore{}
		err := handle(t, f, evt)
		if err == nil || !eventing.IsPermanent(err) {
			t.Errorf("%s: err %v, want a permanent error", name, err)
		}
		if len(f.got) != 0 {
			t.Errorf("%s: wrote %d records", name, len(f.got))
		}
	}
}

func TestStoreFailureIsRetryable(t *testing.T) {
	f := &fakeStore{err: errors.New("db down")}
	evt := event(t, ProductCreatedType, productSource, map[string]any{"productId": recordID, "productNumber": "S-1"})
	err := handle(t, f, evt)
	if err == nil || eventing.IsPermanent(err) {
		t.Fatalf("err %v, want a retryable error", err)
	}
}
