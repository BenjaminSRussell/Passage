package index

import (
	"context"
	"encoding/json"
	"errors"
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
	for _, typ := range []string{PartyCreatedType, PartyUpdatedType, PartyMergedType, ProductCreatedType, ProductUpdatedType, PurchaseOrderConfirmed, PurchaseOrderCancelled} {
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
		{"party created", event(t, PartyCreatedType, partySource, map[string]any{"party_id": recordID, "party_number": "P-1", "party_type": "organization", "name": "Contoso", "status": "active"}),
			store.Record{Type: store.TypeParty, ID: recordID, Number: "P-1", Title: "Contoso", Subtitle: "organization", Status: "active", At: eventTime}, ""},
		{"product updated", event(t, ProductUpdatedType, productSource, map[string]any{"productId": recordID, "productNumber": "SKU-9", "name": "Widget", "productType": "finished", "lifecycleState": "active"}),
			store.Record{Type: store.TypeProduct, ID: recordID, Number: "SKU-9", Title: "Widget", Subtitle: "finished", Status: "active", At: eventTime}, ""},
		{"po confirmed", event(t, PurchaseOrderConfirmed, purchasingSource, map[string]any{"po_id": recordID, "number": "PO-5", "legal_entity_id": entityID, "currency": "USD", "total": "12.50"}),
			store.Record{Type: store.TypePurchaseOrder, ID: recordID, LegalEntityID: entityID, Number: "PO-5", Title: "PO-5", Subtitle: "USD 12.50", Status: "CONFIRMED", At: eventTime}, entityID},
		{"po cancelled", event(t, PurchaseOrderCancelled, purchasingSource, map[string]any{"po_id": recordID, "number": "PO-5", "legal_entity_id": entityID}),
			store.Record{Type: store.TypePurchaseOrder, ID: recordID, LegalEntityID: entityID, Number: "PO-5", Title: "PO-5", Status: "CANCELLED", At: eventTime}, entityID},
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

func TestMergeRemovesEveryMergedParty(t *testing.T) {
	f := &fakeStore{}
	evt := event(t, PartyMergedType, partySource, map[string]any{"mergedPartyIds": []string{recordID, otherID}})
	if err := handle(t, f, evt); err != nil {
		t.Fatal(err)
	}
	if len(f.got) != 2 {
		t.Fatalf("applied %d records, want 2", len(f.got))
	}
	for i, id := range []string{recordID, otherID} {
		if r := f.got[i].rec; r.ID != id || !r.Removed || r.Status != "MERGED" || r.At != eventTime {
			t.Errorf("record %d = %+v", i, r)
		}
	}
}

func TestMalformedEventsAreDeadLettered(t *testing.T) {
	good := map[string]any{"party_id": recordID, "party_number": "P-1"}
	noTime := event(t, PartyCreatedType, partySource, good)
	noTime.Time = time.Time{}
	for name, evt := range map[string]events.Event{
		"wrong source":      event(t, PartyCreatedType, "/passage/other", good),
		"no time":           noTime,
		"bad json":          {Type: PartyCreatedType, Source: partySource, Time: eventTime, Data: []byte("{")},
		"party no number":   event(t, PartyUpdatedType, partySource, map[string]any{"party_id": recordID}),
		"party bad id":      event(t, PartyUpdatedType, partySource, map[string]any{"party_id": "x", "party_number": "P"}),
		"product no number": event(t, ProductCreatedType, productSource, map[string]any{"productId": recordID}),
		"merge empty":       event(t, PartyMergedType, partySource, map[string]any{"mergedPartyIds": []string{}}),
		"merge bad id":      event(t, PartyMergedType, partySource, map[string]any{"mergedPartyIds": []string{"x"}}),
		"po no entity":      event(t, PurchaseOrderConfirmed, purchasingSource, map[string]any{"po_id": recordID, "number": "PO-1"}),
		"po no number":      event(t, PurchaseOrderCancelled, purchasingSource, map[string]any{"po_id": recordID, "legal_entity_id": entityID}),
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
