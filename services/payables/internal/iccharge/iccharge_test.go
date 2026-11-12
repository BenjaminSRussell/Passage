package iccharge_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/payables/internal/iccharge"
	"github.com/BenjaminSRussell/passage/services/payables/internal/store"
	"github.com/BenjaminSRussell/passage/services/payables/migrations"
)

func event(t *testing.T, data map[string]any, source string) events.Event {
	t.Helper()
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	return events.Event{SpecVersion: "1.0", ID: ids.New(), Type: iccharge.ChargeInvoiceRequestedType, Source: source, TenantID: testkit.TenantID(1), Data: raw}
}

func consumerCtx(t *testing.T) context.Context {
	t.Helper()
	sp, err := tenant.ServicePrincipal(iccharge.ConsumerName, testkit.TenantID(1), "")
	if err != nil {
		t.Fatal(err)
	}
	return tenant.With(context.Background(), sp)
}

// #40: an IC charge in Invoice mode becomes one intake row in the borrower's legal entity for the pairing's vendor, once.
func TestHandle_TakesTheBorrowersVendorInvoiceInOnce(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(app)
	le, vendor, charge := ids.New(), ids.New(), ids.New()
	data := map[string]any{"id": charge, "sourceId": "TS-2", "version": 1, "lender": "A", "borrower": "B", "category": "Hour", "transactionDate": "2026-11-05",
		"currency": "USD", "price": "550.00", "tax": "104.50", "borrowerLegalEntityId": le, "vendorAccount": vendor}
	first, err := iccharge.Handle(consumerCtx(t), s, event(t, data, iccharge.Source))
	if err != nil {
		t.Fatal(err)
	}
	if first.LegalEntityID != le || first.VendorID != vendor || first.Source != "INTERCOMPANY" || first.InvoiceNumber != "IC-TS-2-v1" ||
		first.Net.String() != "550" || first.Gross.String() != "654.5" || first.ReceivedBy != "service:payables-iccharge" {
		t.Fatalf("intake %+v", first)
	}
	again, err := iccharge.Handle(consumerCtx(t), s, event(t, data, iccharge.Source))
	if err != nil || again.ID != first.ID {
		t.Fatalf("redelivery = %s, %v; want %s", again.ID, err, first.ID)
	}
	data["price"] = "600.00"
	if _, err := iccharge.Handle(consumerCtx(t), s, event(t, data, iccharge.Source)); err == nil {
		t.Fatal("the same charge version with another price is refused")
	}
}

func TestHandle_RefusesWhatCannotBeTakenIn(t *testing.T) {
	good := map[string]any{"id": ids.New(), "version": 1, "lender": "A", "borrower": "B", "transactionDate": "2026-11-05", "currency": "USD", "price": "10",
		"borrowerLegalEntityId": ids.New(), "vendorAccount": ids.New()}
	for name, mut := range map[string]func(map[string]any){
		"no legal entity": func(d map[string]any) { delete(d, "borrowerLegalEntityId") },
		"no vendor":       func(d map[string]any) { d["vendorAccount"] = "V-1" },
		"bad tax":         func(d map[string]any) { d["tax"] = "-1" },
	} {
		d := map[string]any{}
		for k, v := range good {
			d[k] = v
		}
		mut(d)
		if _, err := iccharge.Handle(consumerCtx(t), nil, event(t, d, iccharge.Source)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := iccharge.Handle(consumerCtx(t), nil, event(t, good, "billing")); err == nil {
		t.Error("another source is refused")
	}
}
