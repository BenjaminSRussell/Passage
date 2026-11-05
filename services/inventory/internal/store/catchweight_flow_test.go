package store_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/eventing"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/api"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
)

// The catch-weight flow fixtures are the receiving and sales events of one
// catch-weight product: receipt.posted of 10 cases at 102 kg and 2.00/kg, and
// the DELIVERED orderprogress.recorded of 5 cases weighing 51 kg. Receiving's
// TestCatchWeightReceipt_PostedEventAndGRIRMatchTheInventoryContract and sales'
// TestPostgres_CatchWeightDeliveryMatchesTheInventoryContract check that the
// producers write exactly these members and values, so this test follows the
// line from the receipt to the sales posting across the service boundaries.
const flowFixtures = "../../testdata/catchweight-flow/"

// flowData reads a fixture as a JSON object, edited by change.
func flowData(t *testing.T, name string, change func(map[string]any, map[string]any)) json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile(flowFixtures + name)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if change != nil {
		change(m, m["lines"].([]any)[0].(map[string]any))
	}
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func flowEvent(t *testing.T, typ, source string, data json.RawMessage) events.Event {
	t.Helper()
	evt, err := events.New(typ, source, "subject", testkit.TenantID(1), data)
	if err != nil {
		t.Fatal(err)
	}
	return evt
}

func flowConsumerCtx(t *testing.T) context.Context {
	t.Helper()
	p, err := tenant.ServicePrincipal(api.PostingsConsumerName, testkit.TenantID(1), "")
	if err != nil {
		t.Fatal(err)
	}
	return tenant.With(context.Background(), p)
}

func flowTransactions(t *testing.T, s *store.Postgres, ctx context.Context, product, source string) []store.TransactionRow {
	t.Helper()
	rows, err := s.ListTransactions(ctx, store.TransactionFilter{ProductID: product, SourceType: source, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// TestCatchWeightFlow_ReceiptToSalesDelivery is Wave 24 AC4 end to end at the
// inventory boundary (#480): the postings consumer books the receipt as qty
// 102 (kg) with cw_qty 10 (cases) at 204, and the delivery as qty -51 (kg)
// with cw_qty 5 at the running average, 102, leaving 51 kg on hand. Replays
// book nothing twice; an invoice report, a plain line and a line without a
// site are not booked, and an issue beyond the stock parks the event.
func TestCatchWeightFlow_ReceiptToSalesDelivery(t *testing.T) {
	s, _, _ := setup(t)
	handlers := api.PostingsConsumerHandlers(s, domain.NegativePolicy{})
	consumer := flowConsumerCtx(t)
	handle := func(evt events.Event) error {
		return handlers[evt.Type].Handle(consumer, nil, evt)
	}
	receipt := flowEvent(t, api.ReceiptPostedType, api.ReceiptSource, flowData(t, "receipt-posted.json", nil))
	delivery := flowEvent(t, api.OrderProgressRecordedType, api.SalesSource, flowData(t, "orderprogress-delivered.json", nil))
	var fixture struct {
		LegalEntityID string `json:"legal_entity_id"`
		Lines         []struct {
			ProductID   string `json:"product_id"`
			SiteID      string `json:"site_id"`
			WarehouseID string `json:"warehouse_id"`
		} `json:"lines"`
	}
	if err := json.Unmarshal(receipt.Data, &fixture); err != nil {
		t.Fatal(err)
	}
	product, read := fixture.Lines[0].ProductID, entityCtx(1, fixture.LegalEntityID)

	if err := handle(receipt); err != nil {
		t.Fatal(err)
	}
	if err := handle(receipt); err != nil {
		t.Fatalf("replay: %v", err)
	}
	got := flowTransactions(t, s, read, product, "RECEIPT_LINE")
	if len(got) != 1 {
		t.Fatalf("%d receipt transactions, want 1 (a replay books nothing)", len(got))
	}
	r := got[0]
	if !r.Qty.Equal(dec("102")) || r.CWQty == nil || *r.CWQty != 10 || r.EnteredUnit != "KG" || r.ReceiptStatus != string(domain.StatusReceived) ||
		!r.CostAmountPhysical.Equal(dec("204")) || r.Currency != "USD" || r.Reference != "RCV-00000001" || r.PhysicalDate != "2026-10-25" ||
		r.Dims[domain.DimSite] != fixture.Lines[0].SiteID || r.Dims[domain.DimWarehouse] != fixture.Lines[0].WarehouseID || len(r.Dims) != 2 {
		t.Fatalf("receipt %+v, want qty 102 kg, cw_qty 10, RECEIVED, 204 USD at the PO line's site and warehouse", r)
	}
	if phys, _ := onHandFor(t, s, read, product); !phys.Equal(dec("102")) {
		t.Fatalf("on hand %s after the receipt, want 102", phys)
	}

	if err := handle(delivery); err != nil {
		t.Fatal(err)
	}
	if err := handle(delivery); err != nil {
		t.Fatalf("replay: %v", err)
	}
	issued := flowTransactions(t, s, read, product, "SALES_LINE")
	if len(issued) != 1 {
		t.Fatalf("%d sales transactions, want 1", len(issued))
	}
	i := issued[0]
	if !i.Qty.Equal(dec("-51")) || i.CWQty == nil || *i.CWQty != 5 || !i.EnteredQty.Equal(dec("-5")) || i.EnteredUnit != "CS" ||
		i.IssueStatus != string(domain.StatusDeducted) || !i.CostAmountPhysical.Equal(dec("-102")) || i.Reference != "PS-W" {
		t.Fatalf("issue %+v, want qty -51 kg, cw_qty 5 (entered -5 CS), DEDUCTED, cost -102 (51 kg x 2.00, not 5 cases x 2.00)", i)
	}
	if phys, _ := onHandFor(t, s, read, product); !phys.Equal(dec("51")) {
		t.Fatalf("on hand %s after the delivery, want 51", phys)
	}

	// An invoice report moves no stock; a plain (not catch-weight) line is not booked.
	invoiced := flowEvent(t, api.OrderProgressRecordedType, api.SalesSource, flowData(t, "orderprogress-delivered.json", func(m, l map[string]any) {
		m["kind"], m["progress_id"] = "INVOICED", "0199f480-0000-7000-8000-0000000000a1"
		delete(l, "reported_weight")
	}))
	plain := flowEvent(t, api.ReceiptPostedType, api.ReceiptSource, flowData(t, "receipt-posted.json", func(m, l map[string]any) {
		m["receipt_id"] = "0199f480-0000-7000-8000-0000000000a2"
		for _, k := range []string{"cw_qty", "cw_nominal", "cw_min", "cw_max"} {
			delete(l, k)
		}
	}))
	for _, evt := range []events.Event{invoiced, plain} {
		if err := handle(evt); err != nil {
			t.Fatalf("%s: %v", evt.Type, err)
		}
	}
	if n, m := len(flowTransactions(t, s, read, product, "RECEIPT_LINE")), len(flowTransactions(t, s, read, product, "SALES_LINE")); n != 1 || m != 1 {
		t.Fatalf("%d receipt and %d sales transactions after the invoice and the plain line, want 1 and 1", n, m)
	}

	// Parked (permanent): a receipt line without a delivery site, an event from
	// another source, and a delivery beyond the stock on hand.
	noSite := flowEvent(t, api.ReceiptPostedType, api.ReceiptSource, flowData(t, "receipt-posted.json", func(m, l map[string]any) {
		m["receipt_id"] = "0199f480-0000-7000-8000-0000000000a3"
		delete(l, "site_id")
	}))
	foreign := flowEvent(t, api.ReceiptPostedType, "/passage/purchasing/purchasing", flowData(t, "receipt-posted.json", nil))
	tooMuch := flowEvent(t, api.OrderProgressRecordedType, api.SalesSource, flowData(t, "orderprogress-delivered.json", func(m, l map[string]any) {
		m["progress_id"] = "0199f480-0000-7000-8000-0000000000a4"
		l["quantity"], l["reported_weight"] = "6", "61"
	}))
	for name, evt := range map[string]events.Event{"no site": noSite, "foreign source": foreign, "beyond stock": tooMuch} {
		err := handle(evt)
		if err == nil || !eventing.IsPermanent(err) {
			t.Fatalf("%s: %v, want a permanent error that parks the event", name, err)
		}
		if name == "beyond stock" && !strings.Contains(err.Error(), "line 1") {
			t.Fatalf("beyond stock: %v does not name the line", err)
		}
	}
	if phys, _ := onHandFor(t, s, read, product); !phys.Equal(dec("51")) {
		t.Fatalf("on hand %s after the refused events, want 51", phys)
	}
}
