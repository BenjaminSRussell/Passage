package store_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/eventing"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/issueclaim"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/api"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
)

// TestPostingsFlow_BatchesAndSerials follows tracked stock through the
// postings consumer (#480). A receipt of three serials of batch LOT-7 for
// 10.00 books each serial as one EA under batch and serial (3.33, 3.33,
// 3.34), one serial of LOT-8 at 10.00, and 5 EA of batch LOT-9 as one booking
// under the batch. The tracked delivery of the shared fixture
// (testdata/tracking-flow, which sales' producer test checks member for
// member) issues SN-0002, SN-0003 and SN-0005 at their own cost. The
// cancelled slip mirrors each issued serial, and the receipt cancellation
// offsets every serial of the cancelled lines, leaving the LOT-9 stock.
// Replays book nothing twice; serials that are not one inventory unit park.
func TestPostingsFlow_BatchesAndSerials(t *testing.T) {
	s, _, _ := setup(t)
	handlers := api.PostingsConsumerHandlers(s, flowUnits{}, domain.NegativePolicy{})
	consumer := flowConsumerCtx(t)
	handle := func(evt events.Event) error { return handlers[evt.Type].Handle(consumer, nil, evt) }

	const receiptID = "0199f480-0000-7000-8000-0000000000d1"
	delivery := flowEvent(t, api.OrderProgressRecordedType, api.SalesSource, flowData(t, "../tracking-flow/orderprogress-delivered-tracked.json", nil))
	var fx struct {
		OrderID       string `json:"order_id"`
		LegalEntityID string `json:"legal_entity_id"`
		Lines         []struct {
			LineID      string `json:"line_id"`
			ProductID   string `json:"product_id"`
			SiteID      string `json:"site_id"`
			WarehouseID string `json:"warehouse_id"`
		} `json:"lines"`
	}
	if err := json.Unmarshal(delivery.Data, &fx); err != nil {
		t.Fatal(err)
	}
	product, read := fx.Lines[0].ProductID, entityCtx(1, fx.LegalEntityID)
	line := func(no int, batch string, serials []string, qty, price, amount string) map[string]any {
		return map[string]any{"line_no": no, "product_id": product, "unit": "EA", "qty": qty, "batch": batch, "serials": serials,
			"inventory_status": "AVAILABLE", "amount": amount, "amount_acct": amount, "unit_price": price,
			"site_id": fx.Lines[0].SiteID, "warehouse_id": fx.Lines[0].WarehouseID}
	}
	receiptData := func(id string, lines ...map[string]any) json.RawMessage {
		raw, err := json.Marshal(map[string]any{"receipt_id": id, "receipt_number": "RCV-T", "legal_entity_id": fx.LegalEntityID,
			"posting_date": "2026-10-25", "currency": "USD", "lines": lines})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	receipt := flowEvent(t, api.ReceiptPostedType, api.ReceiptSource, receiptData(receiptID,
		line(1, "LOT-7", []string{"SN-0002", "SN-0003", "SN-0004"}, "3", "3.333333", "10"),
		line(2, "LOT-8", []string{"SN-0005"}, "1", "10", "10"),
		line(3, "LOT-9", []string{}, "5", "1", "5")))
	for _, evt := range []events.Event{receipt, receipt, delivery, delivery} {
		if err := handle(evt); err != nil {
			t.Fatalf("%s: %v", evt.Type, err)
		}
	}
	in := flowTransactions(t, s, read, product, "RECEIPT_LINE")
	out := flowTransactions(t, s, read, product, "SALES_LINE")
	if len(in) != 5 || len(out) != 3 {
		t.Fatalf("%d receipt and %d sales transactions, want 5 and 3", len(in), len(out))
	}
	cost := map[string]string{}
	for _, r := range in {
		key := r.Dims[domain.DimBatch] + "/" + r.Dims[domain.DimSerial]
		cost[key] = r.CostAmountPhysical.String() + " " + r.Qty.String()
	}
	want := map[string]string{"LOT-7/SN-0002": "3.33 1", "LOT-7/SN-0003": "3.33 1", "LOT-7/SN-0004": "3.34 1", "LOT-8/SN-0005": "10 1", "LOT-9/": "5 5"}
	for k, v := range want {
		if cost[k] != v {
			t.Errorf("receipt %s = %q, want %q (all %v)", k, cost[k], v, cost)
		}
	}
	issued := map[string]string{}
	for _, r := range out {
		issued[r.Dims[domain.DimSerial]] = r.CostAmountPhysical.String() + " " + r.Qty.String() + " " + r.EnteredQty.String()
	}
	if issued["SN-0002"] != "-3.33 -1 -1" || issued["SN-0003"] != "-3.33 -1 -1" || issued["SN-0005"] != "-10 -1 -1" {
		t.Fatalf("issues %v", issued)
	}
	if phys, _ := onHandFor(t, s, read, product); !phys.Equal(dec("6")) {
		t.Fatalf("on hand %s, want 6 (SN-0004 and LOT-9)", phys)
	}
	if _, _, value, _ := flowTotals(t, s, read, product); !value.Equal(dec("8.34")) {
		t.Fatalf("physical value %s, want 8.34 (SN-0004 at 3.34 and LOT-9 at 5)", value)
	}

	slip := flowEvent(t, api.PackingSlipCancelledType, api.FulfillmentSource, flowData(t, "packingslip-cancelled.json", func(m, l map[string]any) {
		m["packing_slip_number"], m["order_id"], m["legal_entity_id"] = "PS-T", fx.OrderID, fx.LegalEntityID
		l["order_line_id"], l["qty"], l["cost_amount"] = fx.Lines[0].LineID, "-3", "-16.66"
		delete(l, "weight")
	}))
	cancel := flowEvent(t, api.ReceiptCorrectedType, api.ReceiptSource, func() json.RawMessage {
		raw, err := json.Marshal(map[string]any{"receipt_id": receiptID, "receipt_number": "RCV-T", "legal_entity_id": fx.LegalEntityID,
			"posting_date": "2026-10-29", "kind": "CANCELLATION", "correction_seq": 0, "currency": "USD", "lines": []map[string]any{
				{"line_no": 1, "product_id": product, "qty_delta": "-3", "amount_delta": "-10", "amount_acct_delta": "-10"},
				{"line_no": 2, "product_id": product, "qty_delta": "-1", "amount_delta": "-10", "amount_acct_delta": "-10"},
			}})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}())
	for _, evt := range []events.Event{slip, slip, cancel, cancel} {
		if err := handle(evt); err != nil {
			t.Fatalf("%s: %v", evt.Type, err)
		}
	}
	if phys, _ := onHandFor(t, s, read, product); !phys.Equal(dec("5")) {
		t.Fatalf("on hand %s after the cancellations, want 5 (LOT-9)", phys)
	}
	if _, _, value, _ := flowTotals(t, s, read, product); !value.Equal(dec("5")) {
		t.Fatalf("physical value %s after the cancellations, want 5 (LOT-9)", value)
	}
	if n := len(flowTransactions(t, s, read, product, "SALES_LINE")); n != 6 {
		t.Fatalf("%d sales transactions, want 3 issues and 3 mirrors", n)
	}
	for _, r := range flowTransactions(t, s, read, product, "RECEIPT_LINE") {
		if r.Dims[domain.DimSerial] == "" && r.Dims[domain.DimBatch] != "LOT-9" {
			t.Fatalf("a booking outside batch and serial: %+v", r)
		}
	}

	// A line whose serials are not one inventory unit each parks.
	box := line(1, "LOT-X", []string{"SN-B1"}, "1", "12", "12")
	box["unit"] = "BOX"
	err := handle(flowEvent(t, api.ReceiptPostedType, api.ReceiptSource, receiptData("0199f480-0000-7000-8000-0000000000d2", box)))
	if !eventing.IsPermanent(err) || !strings.Contains(err.Error(), api.CodePostingTracking) {
		t.Fatalf("one serial for 12 EA: %v, want permanent %s", err, api.CodePostingTracking)
	}
}

// TestPostingsFlow_ThePostedSlipsDeliveryMovesStockAndSettlesItsClaims follows
// a packing slip post (#496) through inventory: the slip's ship claim (under
// the order line) stands in for the 3 serials until sales' DELIVERED report of
// the slip, the shared fixture, is booked. The booking issues the serials and
// settles the claim, and a replay of the event changes nothing.
func TestPostingsFlow_ThePostedSlipsDeliveryMovesStockAndSettlesItsClaims(t *testing.T) {
	s, _, _ := setup(t)
	handlers := api.PostingsConsumerHandlers(s, flowUnits{}, domain.NegativePolicy{})
	consumer := flowConsumerCtx(t)
	handle := func(evt events.Event) error { return handlers[evt.Type].Handle(consumer, nil, evt) }
	delivery := flowEvent(t, api.OrderProgressRecordedType, api.SalesSource, flowData(t, "../tracking-flow/orderprogress-delivered-tracked.json", nil))
	var fx struct {
		LegalEntityID string `json:"legal_entity_id"`
		Lines         []struct {
			LineID      string `json:"line_id"`
			ProductID   string `json:"product_id"`
			SiteID      string `json:"site_id"`
			WarehouseID string `json:"warehouse_id"`
		} `json:"lines"`
	}
	if err := json.Unmarshal(delivery.Data, &fx); err != nil {
		t.Fatal(err)
	}
	l := fx.Lines[0]
	read := entityCtx(1, fx.LegalEntityID)
	receipt, err := json.Marshal(map[string]any{"receipt_id": "0199f480-0000-7000-8000-0000000000e1", "receipt_number": "RCV-S", "legal_entity_id": fx.LegalEntityID,
		"posting_date": "2026-10-25", "currency": "USD", "lines": []map[string]any{{"line_no": 1, "product_id": l.ProductID, "unit": "EA", "qty": "3", "batch": "LOT-7",
			"serials": []string{"SN-0002", "SN-0003", "SN-0004"}, "inventory_status": "AVAILABLE", "amount": "9", "amount_acct": "9", "unit_price": "3",
			"site_id": l.SiteID, "warehouse_id": l.WarehouseID}, {"line_no": 2, "product_id": l.ProductID, "unit": "EA", "qty": "1", "batch": "LOT-8",
			"serials": []string{"SN-0005"}, "inventory_status": "AVAILABLE", "amount": "3", "amount_acct": "3", "unit_price": "3",
			"site_id": l.SiteID, "warehouse_id": l.WarehouseID}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := handle(flowEvent(t, api.ReceiptPostedType, api.ReceiptSource, receipt)); err != nil {
		t.Fatal(err)
	}
	claim := func(key, kind, qty, ref string) {
		t.Helper()
		if _, err := s.ClaimIssue(read, store.IssueClaimInput{IdempotencyKey: key, LegalEntityID: fx.LegalEntityID, ProductID: l.ProductID, Warehouse: l.WarehouseID,
			Kind: kind, Qty: dec(qty), IssueRef: ref}); err != nil {
			t.Fatalf("%s claim: %v", kind, err)
		}
	}
	claim("warehouse-pick", "PICK", "3", issueclaim.SalesLineRef(l.LineID))
	claim("fulfillment-ship", "SHIP", "3", issueclaim.SalesLineRef(l.LineID))
	claim("adhoc-pick", "PICK", "1", "")
	room := func() store.IssueRoom {
		r, err := s.GetIssueRoom(read, fx.LegalEntityID, l.ProductID, l.WarehouseID, "")
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	if r := room(); !r.Physical.Equal(dec("4")) || !r.Outstanding.Equal(dec("4")) {
		t.Fatalf("before the delivery: %s physical, %s claimed, want 4 and 4 (the line's pick and ship once, the ad hoc pick)", r.Physical, r.Outstanding)
	}
	for i := 0; i < 2; i++ {
		if err := handle(delivery); err != nil {
			t.Fatalf("delivery %d: %v", i+1, err)
		}
	}
	if n := len(flowTransactions(t, s, read, l.ProductID, "SALES_LINE")); n != 3 {
		t.Fatalf("%d sales transactions, want the 3 serials issued once", n)
	}
	if r := room(); !r.Physical.Equal(dec("1")) || !r.Held.IsZero() || !r.Outstanding.Equal(dec("1")) {
		t.Fatalf("after the delivery: %s physical, %s held, %s claimed, want 1, 0 and the ad hoc pick's 1", r.Physical, r.Held, r.Outstanding)
	}
	if err := s.ReleaseIssueClaim(read, "adhoc-pick"); err != nil {
		t.Fatal(err)
	}
	if r := room(); !r.Outstanding.IsZero() {
		t.Fatalf("after releasing the ad hoc pick: %s claimed, want 0", r.Outstanding)
	}
}
