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
	"github.com/BenjaminSRussell/passage/services/billing/internal/api"
	"github.com/BenjaminSRussell/passage/services/billing/internal/iccharge"
	"github.com/BenjaminSRussell/passage/services/billing/internal/store"
	"github.com/BenjaminSRussell/passage/services/billing/migrations"
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

// #40: an IC charge in Invoice mode becomes one FREE_TEXT proposal in the lender's legal entity for the pairing's customer, once.
func TestHandle_RaisesTheLendersProposalOnce(t *testing.T) {
	admin, pool := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(pool)
	tax, err := api.ParseTaxRates("IC19=19")
	if err != nil {
		t.Fatal(err)
	}
	cfg := iccharge.Config{RevenueAccount: "4900", TaxCode: "IC19", PaymentTermDays: 30}
	le, customer, charge := ids.New(), ids.New(), ids.New()
	data := map[string]any{"id": charge, "sourceId": "TS-2", "version": 1, "lender": "A", "borrower": "B", "category": "Hour", "transactionDate": "2026-11-05",
		"currency": "USD", "price": "550.00", "lenderLegalEntityId": le, "customerAccount": customer}
	first, err := iccharge.Handle(consumerCtx(t), s, tax, cfg, event(t, data, iccharge.Source))
	if err != nil {
		t.Fatal(err)
	}
	if first.LegalEntityID != le || first.CustomerID != customer || first.Type != "FREE_TEXT" || first.Status != "PROPOSAL" || first.Totals.Gross.String() != "654.5" ||
		len(first.Lines) != 1 || first.Lines[0].RevenueAccount != "4900" || first.DueDate.Format("2006-01-02") != "2026-12-05" {
		t.Fatalf("proposal %+v", first)
	}
	again, err := iccharge.Handle(consumerCtx(t), s, tax, cfg, event(t, data, iccharge.Source))
	if err != nil || again.ID != first.ID {
		t.Fatalf("redelivery = %s, %v; want %s", again.ID, err, first.ID)
	}
}

func TestHandle_RefusesWhatCannotBeInvoiced(t *testing.T) {
	cfg := iccharge.Config{RevenueAccount: "4900", TaxCode: "IC19", PaymentTermDays: 30}
	tax, _ := api.ParseTaxRates("IC19=19")
	good := map[string]any{"id": ids.New(), "version": 1, "lender": "A", "borrower": "B", "category": "Hour", "transactionDate": "2026-11-05",
		"currency": "USD", "price": "10", "lenderLegalEntityId": ids.New(), "customerAccount": ids.New()}
	for name, mut := range map[string]func(map[string]any){
		"no legal entity": func(d map[string]any) { delete(d, "lenderLegalEntityId") },
		"no customer":     func(d map[string]any) { d["customerAccount"] = "C-100" },
		"no date":         func(d map[string]any) { d["transactionDate"] = "" },
	} {
		d := map[string]any{}
		for k, v := range good {
			d[k] = v
		}
		mut(d)
		if _, err := iccharge.Handle(consumerCtx(t), nil, tax, cfg, event(t, d, iccharge.Source)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := iccharge.Handle(consumerCtx(t), nil, tax, cfg, event(t, good, "sales")); err == nil {
		t.Error("another source is refused")
	}
	if _, err := iccharge.Handle(context.Background(), nil, tax, cfg, event(t, good, iccharge.Source)); err == nil {
		t.Error("no principal is refused")
	}
}
