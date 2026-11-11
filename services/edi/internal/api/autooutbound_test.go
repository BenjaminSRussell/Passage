package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"

	"github.com/BenjaminSRussell/passage/services/edi/internal/autosend"
	"github.com/BenjaminSRussell/passage/services/edi/internal/store"
	"github.com/BenjaminSRussell/passage/services/edi/migrations"
)

// autoSetup is a migrated database, the API over fake sources, a partner with a trade profile and the consumer's handlers.
func autoSetup(t *testing.T, f *fakeSources) (*API, http.Handler, *pgxpool.Pool, string, map[string]func(context.Context, events.Event) error) {
	t.Helper()
	return autoSetupWith(t, f, distributor)
}

// autoSetupWith is autoSetup for a partner created from the given body (for example one with an EDIFACT identity).
func autoSetupWith(t *testing.T, f *fakeSources, partnerBody string) (*API, http.Handler, *pgxpool.Pool, string, map[string]func(context.Context, events.Event) error) {
	t.Helper()
	pool := testkit.DB(t)
	if err := dbx.Migrate(context.Background(), pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	st := store.New(pool, 30)
	a := New(st)
	a.Sources = f
	h := a.Routes()
	rec := do(t, h, http.MethodPost, "/edi/v1/partners", partnerBody, "edi.admin", map[string]string{"Idempotency-Key": "p"})
	var p store.Partner
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil || rec.Code != http.StatusCreated {
		t.Fatalf("partner: %d %s", rec.Code, rec.Body.String())
	}
	putProfile(t, h, p.ID, profileBody)
	handlers := map[string]func(context.Context, events.Event) error{}
	for typ, hd := range autosend.Handlers(st, a) {
		hd := hd
		handlers[typ] = func(ctx context.Context, evt events.Event) error { return hd.Handle(ctx, nil, evt) }
	}
	return a, h, pool, p.ID, handlers
}

// consumerCtx is the context the consumer's handlers run in: the principal edi-outbound of the tenant, as the Kafka consumer sets it up.
func consumerCtx(t *testing.T) context.Context {
	t.Helper()
	sp, err := tenant.ServicePrincipal(autosend.ConsumerName, testkit.TenantID(7), "")
	if err != nil {
		t.Fatal(err)
	}
	return tenant.With(context.Background(), sp)
}

func event(t *testing.T, typ, source string, data any) events.Event {
	t.Helper()
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	return events.Event{SpecVersion: "1.0", ID: "evt-" + typ, Source: source, Type: typ, TenantID: testkit.TenantID(7), Data: raw}
}

func countInterchanges(t *testing.T, h http.Handler, direction string) int {
	t.Helper()
	rec := do(t, h, http.MethodGet, "/edi/v1/interchanges?direction="+direction, "", "edi.operate", srcScope)
	var out struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return len(out.Items)
}

func TestAutoOutbound_ConfigRoutes(t *testing.T) {
	_, h, _, partner, _ := autoSetup(t, newFake())
	path := "/edi/v1/partners/" + partner + "/auto-outbound"
	if rec := do(t, h, http.MethodPut, path, `{"transactionSets":["856","999"]}`, "edi.admin", srcScope); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown set: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodPut, path, `{"transactionSets":["856","856"]}`, "edi.admin", srcScope); rec.Code != http.StatusBadRequest {
		t.Fatalf("repeated set: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodPut, path, `{"transactionSets":["856","810"]}`, "edi.admin", srcScope); rec.Code != http.StatusOK {
		t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
	}
	rec := do(t, h, http.MethodGet, path, "", "edi.admin", srcScope)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"856"`) || !strings.Contains(rec.Body.String(), `"810"`) {
		t.Fatalf("get: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodPut, path, `{"transactionSets":[]}`, "edi.admin", srcScope); rec.Code != http.StatusOK {
		t.Fatalf("clear: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodGet, path, "", "edi.admin", srcScope); !strings.Contains(rec.Body.String(), `"transactionSets":[]`) {
		t.Fatalf("cleared: %s", rec.Body.String())
	}
	if rec := do(t, h, http.MethodPut, path, `{"transactionSets":["856"]}`, "edi.operate", srcScope); rec.Code != http.StatusForbidden {
		t.Fatalf("duty: %d", rec.Code)
	}
}

func TestAutoOutbound_NoProfileIsConflict(t *testing.T) {
	pool := testkit.DB(t)
	if err := dbx.Migrate(context.Background(), pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	h := New(store.New(pool, 30)).Routes()
	rec := do(t, h, http.MethodPost, "/edi/v1/partners", distributor, "edi.admin", map[string]string{"Idempotency-Key": "p"})
	var p store.Partner
	_ = json.Unmarshal(rec.Body.Bytes(), &p)
	rec = do(t, h, http.MethodPut, "/edi/v1/partners/"+p.ID+"/auto-outbound", `{"transactionSets":["856"]}`, "edi.admin", srcScope)
	if rec.Code != http.StatusConflict || problemCode(t, rec) != CodeNoTradeProfile {
		t.Fatalf("no profile: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAutoOutbound_ShipmentEventSendsThe856Once(t *testing.T) {
	f := newFake()
	_, h, pool, partner, handlers := autoSetup(t, f)
	if rec := do(t, h, http.MethodPut, "/edi/v1/partners/"+partner+"/auto-outbound", `{"transactionSets":["856"]}`, "edi.admin", srcScope); rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	evt := event(t, autosend.ASNIssuedType, autosend.FulfillmentSource, map[string]string{"shipment_id": srcShipment, "legal_entity_id": srcLE})
	for i := 0; i < 2; i++ { // the second is a redelivery
		if err := handlers[autosend.ASNIssuedType](consumerCtx(t), evt); err != nil {
			t.Fatalf("delivery %d: %v", i, err)
		}
	}
	if n := countInterchanges(t, h, "OUT"); n != 1 {
		t.Fatalf("one 856 expected, got %d", n)
	}
	// The shipment is also registered for self-billing (35-5).
	var rows int
	if err := pool.QueryRow(context.Background(), `select count(*) from self_billing_shipment`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("self-billing shipments: %d %v", rows, err)
	}
	// An invoice event sends nothing: the partner asked only for the 856.
	inv := event(t, autosend.InvoicePostedType, autosend.BillingSource, map[string]string{"invoice_id": srcInvoice, "legal_entity_id": srcLE})
	if err := handlers[autosend.InvoicePostedType](consumerCtx(t), inv); err != nil {
		t.Fatal(err)
	}
	if n := countInterchanges(t, h, "OUT"); n != 1 {
		t.Fatalf("no 810 expected, got %d interchanges", n)
	}
}

func TestAutoOutbound_InvoiceEventSendsThe810(t *testing.T) {
	f := newFake()
	_, h, _, partner, handlers := autoSetup(t, f)
	do(t, h, http.MethodPut, "/edi/v1/partners/"+partner+"/auto-outbound", `{"transactionSets":["810"]}`, "edi.admin", srcScope)
	inv := event(t, autosend.InvoicePostedType, autosend.BillingSource, map[string]string{"invoice_id": srcInvoice, "legal_entity_id": srcLE})
	if err := handlers[autosend.InvoicePostedType](consumerCtx(t), inv); err != nil {
		t.Fatal(err)
	}
	rec := do(t, h, http.MethodGet, "/edi/v1/messages?direction=OUT&transactionSet=810", "", "edi.operate", srcScope)
	if !strings.Contains(rec.Body.String(), `"transactionSet":"810"`) {
		t.Fatalf("810 not recorded: %s", rec.Body.String())
	}
}

func TestAutoOutbound_OtherCustomersDocumentIsSkippedAndSourceOutageIsRetried(t *testing.T) {
	f := newFake()
	_, h, _, partner, handlers := autoSetup(t, f)
	do(t, h, http.MethodPut, "/edi/v1/partners/"+partner+"/auto-outbound", `{"transactionSets":["856"]}`, "edi.admin", srcScope)
	evt := event(t, autosend.ASNIssuedType, autosend.FulfillmentSource, map[string]string{"shipment_id": srcShipment, "legal_entity_id": srcLE})

	// The shipment belongs to another customer than the partner's: nothing is sent and the event is consumed.
	f.shipment.CustomerID = "00000000-0000-0000-0000-0000000000c2"
	if err := handlers[autosend.ASNIssuedType](consumerCtx(t), evt); err != nil {
		t.Fatalf("a document of another customer must be skipped, got %v", err)
	}
	if n := countInterchanges(t, h, "OUT"); n != 0 {
		t.Fatalf("nothing expected, got %d", n)
	}
	// A source service outage is returned, so the consumer retries and then dead-letters.
	f.shipment.CustomerID = srcCustomer
	f.err = errors.New("fulfillment unreachable")
	if err := handlers[autosend.ASNIssuedType](consumerCtx(t), evt); err == nil {
		t.Fatal("an outage must be retried")
	}
	f.err = nil
	if err := handlers[autosend.ASNIssuedType](consumerCtx(t), evt); err != nil {
		t.Fatal(err)
	}
	if n := countInterchanges(t, h, "OUT"); n != 1 {
		t.Fatalf("the retry must send once, got %d", n)
	}
}

func TestAutoOutbound_RejectsEventsItCannotTrust(t *testing.T) {
	_, _, _, _, handlers := autoSetup(t, newFake())
	data := map[string]string{"shipment_id": srcShipment, "legal_entity_id": srcLE}
	if err := handlers[autosend.ASNIssuedType](consumerCtx(t), event(t, autosend.ASNIssuedType, "billing", data)); err == nil || !strings.Contains(err.Error(), "want") {
		t.Fatalf("wrong source: %v", err)
	}
	if err := handlers[autosend.ASNIssuedType](consumerCtx(t), event(t, autosend.ASNIssuedType, autosend.FulfillmentSource, map[string]string{"shipment_id": srcShipment})); err == nil {
		t.Fatal("an event without a legal entity must be refused")
	}
	// Without the consumer's principal nothing runs.
	if err := handlers[autosend.ASNIssuedType](context.Background(), event(t, autosend.ASNIssuedType, autosend.FulfillmentSource, data)); err == nil {
		t.Fatal("no principal must be refused")
	}
}
