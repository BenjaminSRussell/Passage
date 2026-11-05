package store_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/eventing"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/api"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/productunits"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
)

// flowTotals sums the quantity, cases (signed) and physical value of every
// transaction of a product.
func flowTotals(t *testing.T, s *store.Postgres, ctx context.Context, product string) (decimal.Decimal, int, decimal.Decimal, int) {
	t.Helper()
	rows, err := s.ListTransactions(ctx, store.TransactionFilter{ProductID: product, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	qty, value, cases := decimal.Zero, decimal.Zero, 0
	for _, r := range rows {
		qty, value = qty.Add(r.Qty), value.Add(r.CostAmountPhysical)
		if r.CWQty != nil {
			if r.Qty.IsNegative() {
				cases -= *r.CWQty
			} else {
				cases += *r.CWQty
			}
		}
	}
	return qty, cases, value, len(rows)
}

// TestCatchWeightFlow_CorrectionsAndCancellationsOffsetTheBookings follows
// the catch-weight line after its receipt and delivery (#480), from the shared
// fixtures that receiving's and fulfillment's producer tests check member for
// member: the receipt correction (1 case, 10 kg short) books -10 kg, 1 case,
// -20 at the booked 2.00/kg; the cancelled packing slip books the exact mirror
// of the delivery, +51 kg, 5 cases, +102; the receipt cancellation offsets
// what the line holds after its correction, -92 kg, 9 cases, -184. Nothing is
// left on hand and the physical value nets to zero. Replays book nothing
// twice; a cancelled slip read before its delivery is retried, and a
// cancellation of another quantity than booked is parked.
func TestCatchWeightFlow_CorrectionsAndCancellationsOffsetTheBookings(t *testing.T) {
	s, _, _ := setup(t)
	handlers := api.PostingsConsumerHandlers(s, nil, domain.NegativePolicy{})
	consumer := flowConsumerCtx(t)
	handle := func(evt events.Event) error {
		return handlers[evt.Type].Handle(consumer, nil, evt)
	}
	receipt := flowEvent(t, api.ReceiptPostedType, api.ReceiptSource, flowData(t, "receipt-posted.json", nil))
	delivery := flowEvent(t, api.OrderProgressRecordedType, api.SalesSource, flowData(t, "orderprogress-delivered.json", nil))
	corrected := flowEvent(t, api.ReceiptCorrectedType, api.ReceiptSource, flowData(t, "receipt-corrected.json", nil))
	slipCancelled := flowEvent(t, api.PackingSlipCancelledType, api.FulfillmentSource, flowData(t, "packingslip-cancelled.json", nil))
	receiptCancelled := flowEvent(t, api.ReceiptCorrectedType, api.ReceiptSource, flowData(t, "receipt-cancelled.json", nil))
	var fixture struct {
		LegalEntityID string `json:"legal_entity_id"`
		Lines         []struct {
			ProductID string `json:"product_id"`
		} `json:"lines"`
	}
	if err := json.Unmarshal(receipt.Data, &fixture); err != nil {
		t.Fatal(err)
	}
	product, read := fixture.Lines[0].ProductID, entityCtx(1, fixture.LegalEntityID)

	if err := handle(receipt); err != nil {
		t.Fatal(err)
	}
	// The cancelled slip arrives on fulfillment's topic and may be read before
	// sales' delivery is booked: retried, not parked.
	if err := handle(slipCancelled); err == nil || eventing.IsPermanent(err) || !strings.Contains(err.Error(), api.CodePostingOriginalMissing) {
		t.Fatalf("slip cancelled before its delivery: %v, want a retryable %s", err, api.CodePostingOriginalMissing)
	}
	for _, evt := range []events.Event{delivery, corrected, corrected, slipCancelled, slipCancelled} {
		if err := handle(evt); err != nil {
			t.Fatalf("%s: %v", evt.Type, err)
		}
	}
	corr := flowTransactions(t, s, read, product, "RECEIPT_LINE")
	if len(corr) != 2 {
		t.Fatalf("%d receipt transactions after the correction and its replay, want 2", len(corr))
	}
	c := corr[0] // newest first
	if !c.Qty.Equal(dec("-10")) || c.CWQty == nil || *c.CWQty != 1 || !c.EnteredQty.Equal(dec("-10")) || c.IssueStatus != string(domain.StatusDeducted) ||
		!c.CostAmountPhysical.Equal(dec("-20")) || c.PhysicalDate != "2026-10-27" || c.Reference != "RCV-00000001" || len(c.Dims) != 2 {
		t.Fatalf("correction %+v, want -10 kg, 1 case, -20 DEDUCTED on 2026-10-27 in the receipt's dimensions", c)
	}
	back := flowTransactions(t, s, read, product, "SALES_LINE")
	if len(back) != 2 {
		t.Fatalf("%d sales transactions after the slip cancellation and its replay, want 2", len(back))
	}
	if b := back[0]; !b.Qty.Equal(dec("51")) || b.CWQty == nil || *b.CWQty != 5 || !b.EnteredQty.Equal(dec("5")) || b.EnteredUnit != "CS" ||
		b.ReceiptStatus != string(domain.StatusReceived) || !b.CostAmountPhysical.Equal(dec("102")) || b.Reference != "PS-W" || b.PhysicalDate != "2026-10-28" {
		t.Fatalf("slip reversal %+v, want +51 kg, 5 cases (5 CS), +102 RECEIVED", b)
	}
	if phys, _ := onHandFor(t, s, read, product); !phys.Equal(dec("92")) {
		t.Fatalf("on hand %s, want 92", phys)
	}

	// A cancellation of another quantity than the line holds after its
	// booked correction is parked; the right one offsets it exactly.
	wrong := flowEvent(t, api.ReceiptCorrectedType, api.ReceiptSource, flowData(t, "receipt-cancelled.json", func(_, l map[string]any) {
		l["qty_delta"] = "-102"
	}))
	if err := handle(wrong); !eventing.IsPermanent(err) || !strings.Contains(err.Error(), api.CodePostingMismatch) {
		t.Fatalf("cancellation of 102 after a correction to 92: %v, want permanent %s", err, api.CodePostingMismatch)
	}
	for _, evt := range []events.Event{receiptCancelled, receiptCancelled} {
		if err := handle(evt); err != nil {
			t.Fatalf("receipt cancellation: %v", err)
		}
	}
	qty, cases, value, n := flowTotals(t, s, read, product)
	if !qty.IsZero() || cases != 0 || !value.IsZero() || n != 5 {
		t.Fatalf("after the cancellation: qty %s, cases %d, value %s over %d transactions, want 0, 0, 0 over 5", qty, cases, value, n)
	}
	if last := flowTransactions(t, s, read, product, "RECEIPT_LINE")[0]; !last.Qty.Equal(dec("-92")) || *last.CWQty != 9 || !last.CostAmountPhysical.Equal(dec("-184")) {
		t.Fatalf("receipt cancellation %+v, want -92 kg, 9 cases, -184", last)
	}

	// A correction of a line that was never booked is parked.
	unbooked := flowEvent(t, api.ReceiptCorrectedType, api.ReceiptSource, flowData(t, "receipt-corrected.json", func(m, _ map[string]any) {
		m["receipt_id"] = "0199f480-0000-7000-8000-0000000000b1"
	}))
	if err := handle(unbooked); !eventing.IsPermanent(err) || !strings.Contains(err.Error(), api.CodePostingOriginalMissing) {
		t.Fatalf("correction of an unbooked receipt: %v, want permanent %s", err, api.CodePostingOriginalMissing)
	}
}

// flowUnits converts BOX into 12 EA, the inventory unit, and knows no other unit.
type flowUnits struct{}

func (flowUnits) ToInventoryUnit(_ context.Context, _, _, unit string, qty decimal.Decimal) (decimal.Decimal, string, error) {
	switch unit {
	case "EA":
		return qty, "EA", nil
	case "BOX":
		return qty.Mul(decimal.NewFromInt(12)), "EA", nil
	}
	return decimal.Zero, "", fmt.Errorf("%w: %s", productunits.ErrNoConversion, unit)
}

// TestPostingsFlow_PlainLinesInTheInventoryUnit: a plain receipt of 4 BOX for
// 10.00 books 48 EA (entered 4 BOX) at 10.00, a plain delivery of 1 BOX
// issues 12 EA at the running average (2.50), and a correction of -1 BOX
// books -12 EA at the booked 0.208333/EA. A unit without a conversion parks
// the line with INV-PST-001.
func TestPostingsFlow_PlainLinesInTheInventoryUnit(t *testing.T) {
	s, _, _ := setup(t)
	handlers := api.PostingsConsumerHandlers(s, flowUnits{}, domain.NegativePolicy{})
	consumer := flowConsumerCtx(t)
	handle := func(evt events.Event) error {
		return handlers[evt.Type].Handle(consumer, nil, evt)
	}
	const product = "0199f480-0000-7000-8000-0000000000c5"
	plainReceipt := func(receiptID, unit string) events.Event {
		return flowEvent(t, api.ReceiptPostedType, api.ReceiptSource, flowData(t, "receipt-posted.json", func(m, l map[string]any) {
			m["receipt_id"] = receiptID
			for _, k := range []string{"cw_qty", "cw_nominal", "cw_min", "cw_max"} {
				delete(l, k)
			}
			l["product_id"], l["unit"], l["qty"], l["unit_price"], l["amount"], l["amount_acct"] = product, unit, "4", "2.5", "10", "10"
		}))
	}
	receipt := plainReceipt("0199f480-0000-7000-8000-0000000000c1", "BOX")
	delivery := flowEvent(t, api.OrderProgressRecordedType, api.SalesSource, flowData(t, "orderprogress-delivered.json", func(m, l map[string]any) {
		m["progress_id"], m["source_ref"] = "0199f480-0000-7000-8000-0000000000c2", "PS-P"
		delete(l, "reported_weight")
		delete(l, "delivered_weight")
		l["product_id"], l["unit"], l["quantity"] = product, "BOX", "1"
	}))
	correction := flowEvent(t, api.ReceiptCorrectedType, api.ReceiptSource, flowData(t, "receipt-corrected.json", func(m, l map[string]any) {
		m["receipt_id"] = "0199f480-0000-7000-8000-0000000000c1"
		delete(l, "cw_qty_delta")
		l["product_id"], l["qty_delta"], l["amount_delta"], l["amount_acct_delta"] = product, "-1", "-2.5", "-2.5"
	}))
	var fixture struct {
		LegalEntityID string `json:"legal_entity_id"`
	}
	if err := json.Unmarshal(receipt.Data, &fixture); err != nil {
		t.Fatal(err)
	}
	read := entityCtx(1, fixture.LegalEntityID)
	for _, evt := range []events.Event{receipt, receipt, delivery, delivery, correction, correction} {
		if err := handle(evt); err != nil {
			t.Fatalf("%s: %v", evt.Type, err)
		}
	}
	in := flowTransactions(t, s, read, product, "RECEIPT_LINE")
	out := flowTransactions(t, s, read, product, "SALES_LINE")
	if len(in) != 2 || len(out) != 1 {
		t.Fatalf("%d receipt and %d sales transactions, want 2 and 1", len(in), len(out))
	}
	r, c, i := in[1], in[0], out[0]
	if !r.Qty.Equal(dec("48")) || !r.EnteredQty.Equal(dec("4")) || r.EnteredUnit != "BOX" || r.CWQty != nil || !r.CostAmountPhysical.Equal(dec("10")) {
		t.Fatalf("receipt %+v, want 48 EA entered as 4 BOX at 10", r)
	}
	if !i.Qty.Equal(dec("-12")) || !i.EnteredQty.Equal(dec("-1")) || i.EnteredUnit != "BOX" || !i.CostAmountPhysical.Equal(dec("-2.5")) {
		t.Fatalf("delivery %+v, want -12 EA entered as -1 BOX at -2.50", i)
	}
	if !c.Qty.Equal(dec("-12")) || !c.EnteredQty.Equal(dec("-1")) || !c.CostAmountPhysical.Equal(dec("-2.5")) {
		t.Fatalf("correction %+v, want -12 EA entered as -1 BOX at -2.50", c)
	}
	if phys, _ := onHandFor(t, s, read, product); !phys.Equal(dec("24")) {
		t.Fatalf("on hand %s, want 24 EA", phys)
	}
	if err := handle(plainReceipt("0199f480-0000-7000-8000-0000000000c3", "LB")); !eventing.IsPermanent(err) || !strings.Contains(err.Error(), api.CodePostingNoConversion) {
		t.Fatalf("unit without a conversion: %v, want permanent %s", err, api.CodePostingNoConversion)
	}

	// A compensation must name a transaction of its own product and
	// dimensions, and its own value must carry the quantity's sign.
	value := dec("1")
	base := store.CreateTransactionInput{LegalEntityID: fixture.LegalEntityID, SourceType: "RECEIPT_LINE", SourceID: "0199f480-0000-7000-8000-0000000000c4",
		ProductID: "0199f480-0000-7000-8000-0000000000c6", Dims: r.Dims, Qty: dec("1"), EnteredQty: dec("1"), EnteredUnit: "EA",
		Status: domain.StatusReceived, Currency: "USD", IdempotencyKey: "corrects-check", Corrects: r.ID, Value: &value}
	if _, _, err := s.CreateTransaction(read, base); problemCode(err) != domain.CodeInvalid {
		t.Fatalf("compensation of another product's transaction: %v, want %s", err, domain.CodeInvalid)
	}
	negative := dec("-1")
	base.ProductID, base.Value, base.IdempotencyKey = product, &negative, "value-sign-check"
	if _, _, err := s.CreateTransaction(read, base); problemCode(err) != domain.CodeInvalid {
		t.Fatalf("receipt with a negative own value: %v, want %s", err, domain.CodeInvalid)
	}
}
