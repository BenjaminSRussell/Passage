package store_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
)

var shipDay = time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)

func transferOrder(ctx context.Context, product, qty string, transit bool, extra ...string) domain.TransferOrder {
	o := domain.TransferOrder{LegalEntityID: legalEntity(ctx), FromWarehouse: "W1", ToWarehouse: "W2", UseTransit: transit,
		ShipDate: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		Lines: []domain.TransferLine{{LineNo: 1, ProductID: product, Dims: dims(extra...), Unit: "EA", Qty: dec(qty),
			UnderPct: dec("10"), OverPct: dec("10")}}}
	if transit {
		o.TransitWarehouse = "T1"
	}
	return o
}

// stockAt returns physical quantity and posted value of a product at one warehouse.
func stockAt(t *testing.T, s *store.Postgres, ctx context.Context, product, warehouse string) (decimal.Decimal, decimal.Decimal) {
	t.Helper()
	res, err := s.ListOnHand(ctx, store.OnHandFilter{ProductID: product, WarehouseID: warehouse, GroupBy: []string{"product"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) == 0 {
		return decimal.Zero, decimal.Zero
	}
	return res.Rows[0].PhysicalInvent, res.Rows[0].PostedValue
}

func wantStock(t *testing.T, s *store.Postgres, ctx context.Context, product, warehouse, qty, value string) {
	t.Helper()
	q, v := stockAt(t, s, ctx, product, warehouse)
	if !q.Equal(dec(qty)) || !v.Equal(dec(value)) {
		t.Fatalf("%s: %s units worth %s, want %s worth %s", warehouse, q, v, qty, value)
	}
}

func transferTxnCount(t *testing.T, admin *pgxpool.Pool, orderID string) int {
	t.Helper()
	var n int
	if err := admin.QueryRow(context.Background(), `select count(*) from inventory_transaction t
		join inventory_transaction_origin o on o.tenant_id = t.tenant_id and o.id = t.origin_id
		where o.source_type = 'TRANSFER_LINE' and o.reference_number = $1`, orderID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// AC-16.11: 100 EA from W1 to W2 through T1; 95 received with close-short at a
// 10 % tolerance. W1 -100, T1 +5 left in transit, W2 +95, the line closed, the
// value moves with the stock (BR-16.3) and there is no GL entry.
func TestTransferThroughTransitWithShortClose(t *testing.T) {
	s, _, admin := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "100", "2", dims()))

	o, replayed, err := s.CreateTransfer(ctx, "to-1", transferOrder(ctx, product, "100", true))
	if err != nil || replayed {
		t.Fatalf("create: %v replayed=%v", err, replayed)
	}
	if o.Status != domain.TransferCreated || o.Version != 1 || len(o.Lines) != 1 || o.Lines[0].Dims[domain.DimWarehouse] != "W1" || len(o.Number) != 15 {
		t.Fatalf("created %+v", o)
	}
	wantStock(t, s, ctx, product, "W1", "100", "200")

	shipped, err := s.ShipTransfer(ctx, o.ID, o.Version, shipDay, domain.NegativePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if shipped.Status != domain.TransferShipped || shipped.Version != 2 || !shipped.ShipDate.Equal(shipDay) || !shipped.Lines[0].InTransit().Equal(dec("100")) {
		t.Fatalf("shipped %+v", shipped)
	}
	wantStock(t, s, ctx, product, "W1", "0", "0")
	wantStock(t, s, ctx, product, "T1", "100", "200")
	if _, err := s.ShipTransfer(ctx, o.ID, shipped.Version, shipDay, domain.NegativePolicy{}); problemCode(err) != domain.CodeTransferState {
		t.Fatalf("ship twice: %v, want %s", err, domain.CodeTransferState)
	}
	if _, err := s.CancelTransfer(ctx, o.ID, shipped.Version); problemCode(err) != domain.CodeTransferCancel {
		t.Fatalf("cancel in transit: %v, want %s", err, domain.CodeTransferCancel)
	}

	lineID := shipped.Lines[0].ID
	if _, err := s.ReceiveTransfer(ctx, o.ID, shipped.Version, []store.TransferReceipt{{LineID: lineID, Qty: dec("80"), CloseShort: true}},
		shipDay, domain.NegativePolicy{}); problemCode(err) != domain.CodeTransferShortClose {
		t.Fatalf("close short beyond tolerance: %v, want %s", err, domain.CodeTransferShortClose)
	}
	if _, err := s.ReceiveTransfer(ctx, o.ID, shipped.Version, []store.TransferReceipt{{LineID: ids.New(), Qty: dec("1")}},
		shipDay, domain.NegativePolicy{}); problemCode(err) != domain.CodeTransferNotFound {
		t.Fatalf("unknown line: %v, want %s", err, domain.CodeTransferNotFound)
	}
	receiptDay := shipDay.AddDate(0, 0, 3)
	received, err := s.ReceiveTransfer(ctx, o.ID, shipped.Version, []store.TransferReceipt{{LineID: lineID, Qty: dec("95"), CloseShort: true}},
		receiptDay, domain.NegativePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	l := received.Lines[0]
	if received.Status != domain.TransferReceived || !received.ReceiptDate.Equal(receiptDay) || !l.Closed || !l.ReceivedQty.Equal(dec("95")) ||
		!l.InTransit().Equal(dec("5")) {
		t.Fatalf("received %+v", received)
	}
	wantStock(t, s, ctx, product, "W1", "0", "0")
	wantStock(t, s, ctx, product, "T1", "5", "10")
	wantStock(t, s, ctx, product, "W2", "95", "190")
	if q, v := onHandFor(t, s, ctx, product); !q.Equal(dec("100")) || !v.Equal(dec("200")) {
		t.Fatalf("product total %s worth %s, want 100 worth 200 (a transfer moves stock and value, it creates none)", q, v)
	}

	transit, err := s.ListInTransit(ctx)
	if err != nil || len(transit) != 1 || !transit[0].InTransit.Equal(dec("5")) || !transit[0].Closed || transit[0].TransitWarehouse != "T1" {
		t.Fatalf("in transit: %v %+v", err, transit)
	}
	if n := transferTxnCount(t, admin, o.ID); n != 4 {
		t.Fatalf("%d transfer transactions, want 4 (ship pair and receipt pair)", n)
	}
	var journals int
	if err := admin.QueryRow(context.Background(), `select count(*) from inventory_journal where journal_type = 'OPENING_BALANCE'`).Scan(&journals); err != nil || journals != 1 {
		t.Fatalf("journals %d (%v): a transfer must not create a journal or GL entry", journals, err)
	}
	if outboxCount(t, admin, "dev.passage.inventory.transfer.shipped.v1") != 1 || outboxCount(t, admin, "dev.passage.inventory.transfer.received.v1") != 1 {
		t.Fatal("want one transfer.shipped and one transfer.received event")
	}
	var raw []byte
	if err := admin.QueryRow(context.Background(), `select payload from outbox where type = 'dev.passage.inventory.transfer.received.v1'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var evt struct {
		Data struct {
			ReceiptDate *string `json:"receiptDate"`
			Lines       []struct {
				Qty    string `json:"qty"`
				Closed bool   `json:"closed"`
			} `json:"lines"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &evt); err != nil {
		t.Fatal(err)
	}
	if evt.Data.ReceiptDate == nil || *evt.Data.ReceiptDate != "2026-09-05" || len(evt.Data.Lines) != 1 || evt.Data.Lines[0].Qty != "95" || !evt.Data.Lines[0].Closed {
		t.Fatalf("received event %s", raw)
	}
	reconcile(t, admin)
}

// A one-step transfer receives at shipment; an allowed over-delivery on a
// two-step order is issued from the shipping warehouse, never from transit.
func TestOneStepTransferAndOverDelivery(t *testing.T) {
	s, _, admin := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "30", "3", dims("batch", "B1")))

	one, _, err := s.CreateTransfer(ctx, "one", transferOrder(ctx, product, "10", false, "batch", "B1"))
	if err != nil {
		t.Fatal(err)
	}
	one, err = s.ShipTransfer(ctx, one.ID, one.Version, shipDay, domain.NegativePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if one.Status != domain.TransferReceived || !one.ReceiptDate.Equal(shipDay) || !one.Lines[0].Closed {
		t.Fatalf("one-step %+v", one)
	}
	wantStock(t, s, ctx, product, "W1", "20", "60")
	wantStock(t, s, ctx, product, "W2", "10", "30")
	if res, _ := s.ListOnHand(ctx, store.OnHandFilter{ProductID: product, WarehouseID: "W2", GroupBy: []string{"batch"}}); len(res.Rows) != 1 || res.Rows[0].Group["batch"] != "B1" {
		t.Fatalf("the batch must travel with the stock: %+v", res.Rows)
	}

	two, _, err := s.CreateTransfer(ctx, "two", transferOrder(ctx, product, "10", true, "batch", "B1"))
	if err != nil {
		t.Fatal(err)
	}
	two, err = s.ShipTransfer(ctx, two.ID, two.Version, shipDay, domain.NegativePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	lineID := two.Lines[0].ID
	if _, err := s.ReceiveTransfer(ctx, two.ID, two.Version, []store.TransferReceipt{{LineID: lineID, Qty: dec("12")}}, shipDay, domain.NegativePolicy{}); problemCode(err) != domain.CodeTransferOverReceipt {
		t.Fatalf("over tolerance: %v, want %s", err, domain.CodeTransferOverReceipt)
	}
	partial, err := s.ReceiveTransfer(ctx, two.ID, two.Version, []store.TransferReceipt{{LineID: lineID, Qty: dec("4")}}, shipDay, domain.NegativePolicy{})
	if err != nil || partial.Status != domain.TransferPartial || partial.Lines[0].Closed {
		t.Fatalf("partial: %v %+v", err, partial)
	}
	if _, err := s.ReceiveTransfer(ctx, two.ID, two.Version, []store.TransferReceipt{{LineID: lineID, Qty: dec("1")}}, shipDay, domain.NegativePolicy{}); problemCode(err) != store.CodeConflictVersion {
		t.Fatalf("stale version: %v, want %s", err, store.CodeConflictVersion)
	}
	done, err := s.ReceiveTransfer(ctx, two.ID, partial.Version, []store.TransferReceipt{{LineID: lineID, Qty: dec("7")}}, shipDay, domain.NegativePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != domain.TransferReceived || !done.Lines[0].ShippedQty.Equal(dec("11")) || !done.Lines[0].ReceivedQty.Equal(dec("11")) {
		t.Fatalf("over-delivered %+v", done.Lines[0])
	}
	wantStock(t, s, ctx, product, "W1", "9", "27")
	wantStock(t, s, ctx, product, "T1", "0", "0")
	wantStock(t, s, ctx, product, "W2", "21", "63")
	if rows, err := s.ListInTransit(ctx); err != nil || len(rows) != 0 {
		t.Fatalf("nothing should be in transit: %v %+v", err, rows)
	}
	reconcile(t, admin)
}

// Shipping more than is available is INV-NEG-001 and leaves nothing behind;
// reserved stock is not available to ship.
func TestTransferShipRespectsAvailability(t *testing.T) {
	s, _, admin := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "10", "1", dims()))
	if _, err := reserve(ctx, s, product, "4", domain.PolicyManual, dims(), "r"); err != nil {
		t.Fatal(err)
	}
	o, _, err := s.CreateTransfer(ctx, "short", transferOrder(ctx, product, "7", true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ShipTransfer(ctx, o.ID, o.Version, shipDay, domain.NegativePolicy{}); problemCode(err) != domain.CodeNegativePhysical {
		t.Fatalf("ship beyond available: %v, want %s", err, domain.CodeNegativePhysical)
	}
	again, err := s.GetTransfer(ctx, o.ID)
	if err != nil || again.Status != domain.TransferCreated || again.Version != o.Version || !again.Lines[0].ShippedQty.IsZero() {
		t.Fatalf("a refused shipment changed the order: %v %+v", err, again)
	}
	wantStock(t, s, ctx, product, "W1", "10", "10")
	if n := transferTxnCount(t, admin, o.ID); n != 0 {
		t.Fatalf("%d transactions written by a refused shipment", n)
	}
	if outboxCount(t, admin, "dev.passage.inventory.transfer.shipped.v1") != 0 {
		t.Fatal("a refused shipment emitted an event")
	}
	if _, err := s.ShipTransfer(ctx, o.ID, o.Version, shipDay, domain.NegativePolicy{PhysicalAllowed: true, FinancialAllowed: true}); err != nil {
		t.Fatalf("ship with negative stock allowed: %v", err)
	}
	wantStock(t, s, ctx, product, "W1", "3", "3")
}

func TestTransferIdempotencyCancelAndScope(t *testing.T) {
	s, _, _ := setup(t)
	le := ids.New()
	ctx := entityCtx(1, le)
	product := ids.New()
	in := transferOrder(ctx, product, "5", true)
	first, replayed, err := s.CreateTransfer(ctx, "k", in)
	if err != nil || replayed {
		t.Fatalf("create: %v %v", err, replayed)
	}
	again, replayed, err := s.CreateTransfer(ctx, "k", in)
	if err != nil || !replayed || again.ID != first.ID {
		t.Fatalf("replay: %v replayed=%v id %s, want %s", err, replayed, again.ID, first.ID)
	}
	changed := transferOrder(ctx, product, "6", true)
	if _, _, err := s.CreateTransfer(ctx, "k", changed); problemCode(err) != store.CodeIdempotencyReuse {
		t.Fatalf("same key, other body: %v, want %s", err, store.CodeIdempotencyReuse)
	}
	noSite := transferOrder(ctx, product, "5", true)
	noSite.Lines[0].Dims = domain.Dimensions{}
	if _, _, err := s.CreateTransfer(ctx, "k2", noSite); problemCode(err) != domain.CodeDimensionMandatory {
		t.Fatalf("no site: %v, want %s", err, domain.CodeDimensionMandatory)
	}
	foreign := transferOrder(ctx, product, "5", true)
	foreign.LegalEntityID = ids.New()
	if _, _, err := s.CreateTransfer(ctx, "k3", foreign); problemCode(err) != store.CodeOutOfScope {
		t.Fatalf("order for another legal entity: %v, want %s", err, store.CodeOutOfScope)
	}
	if _, err := s.CancelTransfer(ctx, first.ID, first.Version+1); problemCode(err) != store.CodeConflictVersion {
		t.Fatalf("stale cancel: %v, want %s", err, store.CodeConflictVersion)
	}
	cancelled, err := s.CancelTransfer(ctx, first.ID, first.Version)
	if err != nil || cancelled.Status != domain.TransferCancelled {
		t.Fatalf("cancel: %v %+v", err, cancelled)
	}
	if _, err := s.ShipTransfer(ctx, first.ID, cancelled.Version, shipDay, domain.NegativePolicy{}); problemCode(err) != domain.CodeTransferState {
		t.Fatalf("ship cancelled: %v, want %s", err, domain.CodeTransferState)
	}
	if _, err := s.CancelTransfer(ctx, first.ID, cancelled.Version); problemCode(err) != domain.CodeTransferState {
		t.Fatalf("cancel twice: %v, want %s", err, domain.CodeTransferState)
	}
	list, err := s.ListTransfers(ctx, domain.TransferCancelled)
	if err != nil || len(list) != 1 || list[0].ID != first.ID || len(list[0].Lines) != 1 {
		t.Fatalf("list cancelled: %v %+v", err, list)
	}
	if list, err := s.ListTransfers(ctx, domain.TransferCreated); err != nil || len(list) != 0 {
		t.Fatalf("list created: %v %+v", err, list)
	}
	other := entityCtx(1, ids.New())
	if _, err := s.GetTransfer(other, first.ID); problemCode(err) != domain.CodeTransferNotFound {
		t.Fatalf("another legal entity reads the order: %v", err)
	}
	if _, err := s.GetTransfer(tenantWideCtx(2), first.ID); problemCode(err) != domain.CodeTransferNotFound {
		t.Fatalf("another tenant reads the order: %v", err)
	}
}

// AC-16.14 at small scale: classes by cumulative value, zero-value products in
// the zero class, runs replayable by key and listed with class counts.
func TestABCRunClassifiesByValue(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	big, mid, small, empty := ids.New(), ids.New(), ids.New(), ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(big, "80", "10", dims()), line(mid, "15", "10", dims()),
		line(small, "5", "10", dims()), line(small, "1", "10", dims("site", "S2")), line(empty, "1", "0", dims()))
	in := store.AbcRunInput{IdempotencyKey: "abc", LegalEntityID: legalEntity(ctx), Criterion: domain.AbcCriterionValue,
		Thresholds: domain.AbcThresholds{A: dec("70"), B: dec("20"), C: dec("10")}, ZeroClass: domain.ABCN}
	run, replayed, err := s.RunABC(ctx, in)
	if err != nil || replayed {
		t.Fatalf("run: %v %v", err, replayed)
	}
	classes := map[string]string{}
	for _, it := range run.Items {
		classes[it.ProductID+"@"+it.Site] = it.Class
	}
	want := map[string]string{big + "@S1": "A", mid + "@S1": "B", small + "@S1": "C", small + "@S2": "C", empty + "@S1": "N"}
	for k, c := range want {
		if classes[k] != c {
			t.Fatalf("%s: class %q, want %q (all %v)", k, classes[k], c, classes)
		}
	}
	if len(run.Items) != 5 || run.Items[0].ProductID != big || !run.Items[0].Value.Equal(dec("800")) {
		t.Fatalf("items %+v", run.Items)
	}
	again, replayed, err := s.RunABC(ctx, in)
	if err != nil || !replayed || again.ID != run.ID {
		t.Fatalf("replay: %v %v", err, replayed)
	}
	in.ZeroClass = domain.ABCC
	if _, _, err := s.RunABC(ctx, in); problemCode(err) != store.CodeIdempotencyReuse {
		t.Fatalf("same key, other body: %v, want %s", err, store.CodeIdempotencyReuse)
	}
	in.IdempotencyKey, in.Thresholds = "bad", domain.AbcThresholds{A: dec("80"), B: dec("15"), C: dec("4")}
	if _, _, err := s.RunABC(ctx, in); problemCode(err) != domain.CodeABCThresholds {
		t.Fatalf("thresholds not summing to 100: %v, want %s", err, domain.CodeABCThresholds)
	}
	runs, err := s.ListABCRuns(ctx)
	if err != nil || len(runs) != 1 || runs[0].Counts["A"] != 1 || runs[0].Counts["C"] != 2 || runs[0].Counts["N"] != 1 {
		t.Fatalf("list: %v %+v", err, runs)
	}
	if _, err := s.GetABCRun(tenantWideCtx(2), run.ID); problemCode(err) != domain.CodeABCNotFound {
		t.Fatalf("another tenant reads the run: %v", err)
	}
}

// reconcile checks every on-hand row of tenant 1 against its transactions:
// posted quantity and value equal their sums (AC2 after transfers).
func reconcile(t *testing.T, admin *pgxpool.Pool) {
	t.Helper()
	rows, err := admin.Query(context.Background(), `select o.posted_qty::text, o.posted_value::text,
		(select coalesce(sum(t.qty), 0) from inventory_transaction t where t.tenant_id = o.tenant_id and t.legal_entity_id = o.legal_entity_id
		  and t.product_id = o.product_id and t.dimension_set_hash = o.dimension_set_hash)::text,
		(select coalesce(sum(t.cost_amount_posted), 0) from inventory_transaction t where t.tenant_id = o.tenant_id and t.legal_entity_id = o.legal_entity_id
		  and t.product_id = o.product_id and t.dimension_set_hash = o.dimension_set_hash)::text
		from on_hand o where o.tenant_id = $1`, testkit.TenantID(1))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var pq, pv, sq, sv string
		if err := rows.Scan(&pq, &pv, &sq, &sv); err != nil {
			t.Fatal(err)
		}
		if !dec(pq).Equal(dec(sq)) || !dec(pv).Equal(dec(sv)) {
			t.Fatalf("projection %s / %s != transactions %s / %s", pq, pv, sq, sv)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}
