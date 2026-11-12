package iccharge_test

import (
	"context"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/billing/internal/iccharge"
	"github.com/BenjaminSRussell/passage/services/billing/internal/store"
	"github.com/BenjaminSRussell/passage/services/billing/migrations"
)

// publishedSource is what intercompany's enqueue passes to events.New (services/intercompany/internal/events.Source).
const publishedSource = "/passage/intercompany"

// published builds the event the way intercompany's store enqueues it: events.New with the publisher's source.
func published(t *testing.T, data map[string]any) events.Event {
	t.Helper()
	evt, err := events.New(iccharge.ChargeInvoiceRequestedType, publishedSource, ids.New(), testkit.TenantID(1), data)
	if err != nil {
		t.Fatal(err)
	}
	return evt
}

func consumerCtx(t *testing.T) context.Context {
	t.Helper()
	sp, err := tenant.ServicePrincipal(iccharge.ConsumerName, testkit.TenantID(1), "")
	if err != nil {
		t.Fatal(err)
	}
	return tenant.With(context.Background(), sp)
}

func charge(le, customer string) map[string]any {
	return map[string]any{"id": ids.New(), "sourceId": "TS-2", "version": 1, "lender": "A", "borrower": "B", "category": "Hour", "transactionDate": "2026-11-05",
		"currency": "USD", "price": "550.00", "tax": "104.50", "taxCode": "IC19", "lenderLegalEntityId": le, "customerAccount": customer}
}

// #40: an IC charge in Invoice mode, as intercompany publishes it, becomes one FREE_TEXT proposal in the lender's legal entity for
// the pairing's customer, carrying the charge's own tax, once.
func TestHandle_RaisesTheLendersProposalOnceWithTheChargesTax(t *testing.T) {
	admin, pool := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(pool)
	cfg := iccharge.Config{RevenueAccount: "4900", TaxCode: "STD", PaymentTermDays: 30}
	le, customer := ids.New(), ids.New()
	data := charge(le, customer)
	first, err := iccharge.Handle(consumerCtx(t), s, cfg, published(t, data))
	if err != nil {
		t.Fatal(err)
	}
	if first.LegalEntityID != le || first.CustomerID != customer || first.Type != "FREE_TEXT" || first.Status != "PROPOSAL" ||
		first.Totals.Tax.String() != "104.5" || first.Totals.Gross.String() != "654.5" || len(first.Lines) != 1 ||
		first.Lines[0].RevenueAccount != "4900" || first.Lines[0].TaxCode != "IC19" || first.DueDate.Format("2006-01-02") != "2026-12-05" {
		t.Fatalf("proposal %+v", first)
	}
	again, err := iccharge.Handle(consumerCtx(t), s, cfg, published(t, data))
	if err != nil || again.ID != first.ID {
		t.Fatalf("redelivery = %s, %v; want %s", again.ID, err, first.ID)
	}
}

func TestHandle_RefusesWhatCannotBeInvoiced(t *testing.T) {
	cfg := iccharge.Config{RevenueAccount: "4900", TaxCode: "IC19", PaymentTermDays: 30}
	good := charge(ids.New(), ids.New())
	for name, mut := range map[string]func(map[string]any){
		"no legal entity": func(d map[string]any) { delete(d, "lenderLegalEntityId") },
		"no customer":     func(d map[string]any) { d["customerAccount"] = "C-100" },
		"no date":         func(d map[string]any) { d["transactionDate"] = "" },
		"bad tax":         func(d map[string]any) { d["tax"] = "-1" },
	} {
		d := map[string]any{}
		for k, v := range good {
			d[k] = v
		}
		mut(d)
		if _, err := iccharge.Handle(consumerCtx(t), nil, cfg, published(t, d)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	other, err := events.New(iccharge.ChargeInvoiceRequestedType, "/passage/sales", ids.New(), testkit.TenantID(1), good)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := iccharge.Handle(consumerCtx(t), nil, cfg, other); err == nil {
		t.Error("another source is refused")
	}
	if _, err := iccharge.Handle(context.Background(), nil, cfg, published(t, good)); err == nil {
		t.Error("no principal is refused")
	}
}
