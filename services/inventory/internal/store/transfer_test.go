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

	shipped, err := s.ShipTransfer(ctx, o.ID, o.Version, nil, shipDay, domain.NegativePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if shipped.Status != domain.TransferShipped || shipped.Version != 2 || !shipped.ShipDate.Equal(shipDay) || !shipped.Lines[0].InTransit().Equal(dec("100")) {
		t.Fatalf("shipped %+v", shipped)
	}
	wantStock(t, s, ctx, product, "W1", "0", "0")
	wantStock(t, s, ctx, product, "T1", "100", "200")
	if _, err := s.ShipTransfer(ctx, o.ID, shipped.Version, nil, shipDay, domain.NegativePolicy{}); problemCode(err) != domain.CodeTransferShipQty {
		t.Fatalf("ship twice: %v, want %s", err, domain.CodeTransferShipQty)
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
	one, err = s.ShipTransfer(ctx, one.ID, one.Version, nil, shipDay, domain.NegativePolicy{})
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
	two, err = s.ShipTransfer(ctx, two.ID, two.Version, nil, shipDay, domain.NegativePolicy{})
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
	if _, err := s.ShipTransfer(ctx, o.ID, o.Version, nil, shipDay, domain.NegativePolicy{}); problemCode(err) != domain.CodeNegativePhysical {
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
	if _, err := s.ShipTransfer(ctx, o.ID, o.Version, nil, shipDay, domain.NegativePolicy{PhysicalAllowed: true, FinancialAllowed: true}); err != nil {
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
	if _, err := s.ShipTransfer(ctx, first.ID, cancelled.Version, nil, shipDay, domain.NegativePolicy{}); problemCode(err) != domain.CodeTransferState {
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

// The line inquiry spans orders, filters by legal entity, status, warehouse,
// product and ship date, finds late shipments and late receipts with the same
// boundary as domain.TransferLineLateness, and never shows another legal
// entity's or tenant's lines.
func TestListTransferLinesFiltersAndLateness(t *testing.T) {
	s, _, _ := setup(t)
	leA, leB := ids.New(), ids.New()
	ctx, ctxB := entityCtx(1, leA), entityCtx(1, leB)
	product, other, productB := ids.New(), ids.New(), ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "100", "2", dims()), line(other, "50", "1", dims()))
	post(t, ctxB, s, domain.JournalOpeningBalance, line(productB, "10", "1", dims()))

	planned, _, err := s.CreateTransfer(ctx, "to-a", transferOrder(ctx, product, "10", false)) // planned 2026-09-01, never shipped
	if err != nil {
		t.Fatal(err)
	}
	toShip, _, err := s.CreateTransfer(ctx, "to-b", transferOrder(ctx, other, "20", true))
	if err != nil {
		t.Fatal(err)
	}
	shipped, err := s.ShipTransfer(ctx, toShip.ID, toShip.Version, nil, shipDay, domain.NegativePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateTransfer(ctxB, "to-c", transferOrder(ctxB, productB, "5", false)); err != nil {
		t.Fatal(err)
	}

	list := func(c context.Context, f store.TransferLineFilter) []store.TransferLineRow {
		t.Helper()
		if f.AsOf.IsZero() {
			f.AsOf = shipDay.AddDate(0, 0, 10)
		}
		if f.LateReceiptDays == 0 {
			f.LateReceiptDays = 7
		}
		rows, truncated, err := s.ListTransferLines(c, f)
		if err != nil || truncated {
			t.Fatalf("list %+v: truncated %v, %v", f, truncated, err)
		}
		return rows
	}
	numbers := func(rows []store.TransferLineRow) []string {
		out := []string{}
		for _, r := range rows {
			out = append(out, r.Number)
		}
		return out
	}

	all := list(ctx, store.TransferLineFilter{})
	if len(all) != 2 || all[0].Number != planned.Number || all[1].Number != shipped.Number || all[0].LegalEntityID != leA {
		t.Fatalf("legal entity A sees %v", numbers(all))
	}
	if l := all[1].Line; !l.ShippedQty.Equal(dec("20")) || !l.InTransit().Equal(dec("20")) || all[1].Status != domain.TransferShipped ||
		l.Dims[domain.DimWarehouse] != "W1" || !all[1].ShipDate.Equal(shipDay) {
		t.Fatalf("shipped line %+v", all[1])
	}
	if rows := list(tenantWideCtx(1), store.TransferLineFilter{}); len(rows) != 3 {
		t.Fatalf("tenant-wide sees %v, want all three orders", numbers(rows))
	}
	if rows := list(tenantWideCtx(1), store.TransferLineFilter{LegalEntityID: leB}); len(rows) != 1 || rows[0].LegalEntityID != leB {
		t.Fatalf("legal entity filter: %v", numbers(rows))
	}

	asOf := shipDay.AddDate(0, 0, 8) // eight days in transit: one beyond the 7-day allowance
	late := list(ctx, store.TransferLineFilter{LateOnly: true, AsOf: asOf})
	if len(late) != 2 {
		t.Fatalf("late lines %v", numbers(late))
	}
	for _, r := range late {
		want := domain.LateShipment
		if r.Status == domain.TransferShipped {
			want = domain.LateReceipt
		}
		if got, days := domain.TransferLineLateness(r.Status, r.Line, r.ShipDate, r.PlannedReceipt, asOf, 7); got != want || days < 1 {
			t.Errorf("%s: lateness %q %d days, want %q", r.Number, got, days, want)
		}
	}
	if due := list(ctx, store.TransferLineFilter{LateOnly: true, AsOf: shipDay.AddDate(0, 0, 7)}); len(due) != 1 || due[0].Status != domain.TransferCreated {
		t.Fatalf("a shipment due today is not late yet: %v", numbers(due))
	}
	if rows := list(ctx, store.TransferLineFilter{LateOnly: true, AsOf: shipDay.AddDate(0, 0, 8), LateReceiptDays: 30}); len(rows) != 1 {
		t.Fatalf("a longer allowance keeps the shipment on time: %v", numbers(rows))
	}

	from, to := shipDay, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	if rows := list(ctx, store.TransferLineFilter{From: &from}); len(rows) != 1 || rows[0].OrderID != shipped.ID {
		t.Fatalf("from filter: %v", numbers(rows))
	}
	if rows := list(ctx, store.TransferLineFilter{To: &to}); len(rows) != 1 || rows[0].OrderID != planned.ID {
		t.Fatalf("to filter: %v", numbers(rows))
	}
	if rows := list(ctx, store.TransferLineFilter{Status: domain.TransferShipped}); len(rows) != 1 || rows[0].OrderID != shipped.ID {
		t.Fatalf("status filter: %v", numbers(rows))
	}
	if rows := list(ctx, store.TransferLineFilter{ProductID: product}); len(rows) != 1 || rows[0].OrderID != planned.ID {
		t.Fatalf("product filter: %v", numbers(rows))
	}
	if rows := list(ctx, store.TransferLineFilter{FromWarehouse: "W9"}); len(rows) != 0 {
		t.Fatalf("from warehouse filter: %v", numbers(rows))
	}
	if rows := list(ctx, store.TransferLineFilter{ToWarehouse: "W2"}); len(rows) != 2 {
		t.Fatalf("to warehouse filter: %v", numbers(rows))
	}
	if rows := list(entityCtx(2, ids.New()), store.TransferLineFilter{}); len(rows) != 0 {
		t.Fatalf("another tenant sees lines: %v", numbers(rows))
	}
}

// Partial shipments: 100 EA ordered, 60 shipped, then 40. Each shipment moves its own
// quantity and value; the line stays open until it is fully shipped and received; the
// planned receipt date is stored and drives lateness; a replay of the create with another
// planned date is an idempotency reuse.
func TestTransferPartialShipmentsAndPlannedReceipt(t *testing.T) {
	s, _, admin := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "100", "2", dims()))

	in := transferOrder(ctx, product, "100", true)
	in.PlannedReceipt = time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	o, _, err := s.CreateTransfer(ctx, "to-partial", in)
	if err != nil || !o.PlannedReceipt.Equal(in.PlannedReceipt) {
		t.Fatalf("create: %v planned %v", err, o.PlannedReceipt)
	}
	other := in
	other.PlannedReceipt = time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)
	if _, _, err := s.CreateTransfer(ctx, "to-partial", other); problemCode(err) != store.CodeIdempotencyReuse {
		t.Fatalf("replay with another planned date: %v, want %s", err, store.CodeIdempotencyReuse)
	}
	lineID := o.Lines[0].ID
	ship := func(version int, qty string) (domain.TransferOrder, error) {
		return s.ShipTransfer(ctx, o.ID, version, []store.TransferShipment{{LineID: lineID, Qty: dec(qty)}}, shipDay, domain.NegativePolicy{})
	}
	if _, err := ship(o.Version, "1.1234567"); problemCode(err) != domain.CodeUOMPrecision {
		t.Fatalf("ship beyond the quantity precision: %v, want %s", err, domain.CodeUOMPrecision)
	}
	if _, err := ship(o.Version, "101"); problemCode(err) != domain.CodeTransferShipQty {
		t.Fatalf("over-ship: %v, want %s", err, domain.CodeTransferShipQty)
	}
	first, err := ship(o.Version, "60")
	if err != nil || first.Status != domain.TransferShipped || !first.Lines[0].ShippedQty.Equal(dec("60")) || !first.Lines[0].UnshippedQty().Equal(dec("40")) {
		t.Fatalf("first shipment: %v %+v", err, first)
	}
	wantStock(t, s, ctx, product, "W1", "40", "80")
	wantStock(t, s, ctx, product, "T1", "60", "120")

	recv, err := s.ReceiveTransfer(ctx, o.ID, first.Version, []store.TransferReceipt{{LineID: lineID, Qty: dec("60")}}, shipDay, domain.NegativePolicy{})
	if err != nil || recv.Status != domain.TransferPartial || recv.Lines[0].Closed {
		t.Fatalf("receive the shipped 60: %v status %s closed %v", err, recv.Status, recv.Lines[0].Closed)
	}
	laterDay := time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)
	second, err := s.ShipTransfer(ctx, o.ID, recv.Version, []store.TransferShipment{{LineID: lineID, Qty: dec("40")}}, laterDay, domain.NegativePolicy{})
	if err != nil || !second.Lines[0].ShippedQty.Equal(dec("100")) || second.Status != domain.TransferPartial {
		t.Fatalf("second shipment: %v %+v", err, second)
	}
	if !second.ShipDate.Equal(shipDay) {
		t.Fatalf("order ship date %v after a later partial shipment, want the first shipment's %v", second.ShipDate, shipDay)
	}
	if n := outboxCount(t, admin, "dev.passage.inventory.transfer.shipped.v1"); n != 2 {
		t.Fatalf("transfer.shipped events %d, want one per shipment", n)
	}
	var evt struct {
		Lines []struct {
			Qty        string `json:"qty"`
			ShippedQty string `json:"shippedQty"`
		} `json:"lines"`
	}
	eventData(t, admin, "dev.passage.inventory.transfer.shipped.v1", "'shipDate'", "2026-09-07", &evt)
	if len(evt.Lines) != 1 || evt.Lines[0].Qty != "40" || evt.Lines[0].ShippedQty != "100" {
		t.Fatalf("second transfer.shipped lines %+v, want qty 40 of shipped 100", evt.Lines)
	}
	if _, err := s.ShipTransfer(ctx, o.ID, second.Version, nil, shipDay, domain.NegativePolicy{}); problemCode(err) != domain.CodeTransferShipQty {
		t.Fatalf("nothing left to ship: %v, want %s", err, domain.CodeTransferShipQty)
	}
	wantStock(t, s, ctx, product, "W1", "0", "0")
	done, err := s.ReceiveTransfer(ctx, o.ID, second.Version, []store.TransferReceipt{{LineID: lineID, Qty: dec("40")}}, shipDay, domain.NegativePolicy{})
	if err != nil || done.Status != domain.TransferReceived || !done.Lines[0].Closed {
		t.Fatalf("receive the rest: %v %+v", err, done)
	}
	wantStock(t, s, ctx, product, "W2", "100", "200")
	wantStock(t, s, ctx, product, "T1", "0", "0")
	if n := transferTxnCount(t, admin, o.ID); n != 8 {
		t.Fatalf("transactions %d, want 8 (two shipments and two receipts, a pair each)", n)
	}
}

// With a planned receipt date, an open shipment is late once that date has passed, whatever
// the allowance; without one the allowance after the ship date applies.
func TestPlannedReceiptDateDrivesLateReceipt(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "20", "1", dims()))
	in := transferOrder(ctx, product, "10", true)
	in.PlannedReceipt = time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	o, _, err := s.CreateTransfer(ctx, "to-late-plan", in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ShipTransfer(ctx, o.ID, o.Version, nil, shipDay, domain.NegativePolicy{}); err != nil {
		t.Fatal(err)
	}
	late := func(asOf time.Time) int {
		rows, _, err := s.ListTransferLines(ctx, store.TransferLineFilter{LateOnly: true, AsOf: asOf, LateReceiptDays: 7})
		if err != nil {
			t.Fatal(err)
		}
		return len(rows)
	}
	if n := late(time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)); n != 0 {
		t.Fatalf("due on the planned date: %d late lines, want 0 (the 7-day allowance has passed)", n)
	}
	if n := late(time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC)); n != 1 {
		t.Fatalf("a day after the planned date: %d late lines, want 1", n)
	}
}

func TestEditTransferChangesLinesAndEmitsEvent(t *testing.T) {
	s, _, admin := setup(t)
	le := ids.New()
	ctx := entityCtx(1, le)
	p1, p2 := ids.New(), ids.New()
	o, _, err := s.CreateTransfer(ctx, "edit-1", transferOrder(ctx, p1, "5", true))
	if err != nil {
		t.Fatal(err)
	}
	newShip := time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC)
	site := "S2"
	q := dec("8")
	planned := newShip.AddDate(0, 0, 3)
	e := domain.TransferEdit{ShipDate: &newShip, PlannedReceipt: &planned, ToSite: &site, Update: []domain.LineEdit{{LineID: o.Lines[0].ID, Qty: &q}},
		Add: []domain.TransferLine{{ProductID: p2, Dims: dims(), Unit: "EA", Qty: dec("3")}}}
	// Another legal entity, or another tenant, cannot see the order to edit it.
	for name, other := range map[string]context.Context{"legal entity": entityCtx(1, ids.New()), "tenant": entityCtx(2, le)} {
		if _, err := s.EditTransfer(other, o.ID, o.Version, e); problemCode(err) != domain.CodeTransferNotFound {
			t.Fatalf("edit from another %s: %v, want %s", name, err, domain.CodeTransferNotFound)
		}
	}
	if _, err := s.EditTransfer(ctx, o.ID, o.Version+1, e); problemCode(err) != store.CodeConflictVersion {
		t.Fatalf("stale edit: %v, want %s", err, store.CodeConflictVersion)
	}
	got, err := s.EditTransfer(ctx, o.ID, o.Version, e)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != o.Version+1 || !got.ShipDate.Equal(newShip) || got.ToSite != "S2" || len(got.Lines) != 2 ||
		!got.Lines[0].Qty.Equal(q) || got.Lines[1].LineNo != 2 || got.Lines[1].ProductID != p2 || got.Lines[1].ID == "" {
		t.Fatalf("edited order %+v", got)
	}
	// Remove the first line, clear the planned receipt.
	got2, err := s.EditTransfer(ctx, o.ID, got.Version, domain.TransferEdit{Remove: []string{got.Lines[0].ID}, ClearPlannedReceipt: true})
	if err != nil || len(got2.Lines) != 1 || got2.Lines[0].ProductID != p2 {
		t.Fatalf("remove: %v %+v", err, got2)
	}
	// Remove the highest line (2) and add one in the same edit: the new line is 3,
	// not a reuse of the removed number.
	got3, err := s.EditTransfer(ctx, o.ID, got2.Version, domain.TransferEdit{Remove: []string{got2.Lines[0].ID},
		Add: []domain.TransferLine{{ProductID: p1, Dims: dims(), Unit: "EA", Qty: dec("1")}}})
	if err != nil || len(got3.Lines) != 1 || got3.Lines[0].LineNo != 3 || got3.Lines[0].ProductID != p1 {
		t.Fatalf("replace the highest line: %v %+v", err, got3)
	}
	if _, err := s.EditTransfer(ctx, o.ID, got3.Version, domain.TransferEdit{Remove: []string{got3.Lines[0].ID}}); problemCode(err) != domain.CodeInvalid {
		t.Fatalf("remove last line: %v, want %s", err, domain.CodeInvalid)
	}
	var n int
	if err := admin.QueryRow(context.Background(), `select count(*) from outbox where type = 'dev.passage.inventory.transfer.changed.v1'
		and aggregate_id = $1`, o.ID).Scan(&n); err != nil || n != 3 {
		t.Fatalf("transfer.changed events %d (%v), want 3", n, err)
	}
	// The event carries the changed header: receiving site and planned receipt date.
	if err := admin.QueryRow(context.Background(), `select count(*) from outbox where type = 'dev.passage.inventory.transfer.changed.v1'
		and aggregate_id = $1 and payload->'data'->>'toSiteId' = 'S2' and payload->'data'->>'plannedReceiptDate' = '2026-09-07'
		and jsonb_array_length(payload->'data'->'lines') = 2`, o.ID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("transfer.changed with the new site and planned receipt: %d (%v), want 1", n, err)
	}
	// A cancelled order cannot be edited, and cancelling emits the event.
	o2, _, err := s.CreateTransfer(ctx, "edit-2", transferOrder(ctx, p1, "5", true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CancelTransfer(ctx, o2.ID, o2.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EditTransfer(ctx, o2.ID, o2.Version+1, domain.TransferEdit{ClearPlannedReceipt: true}); problemCode(err) != domain.CodeTransferState {
		t.Fatalf("edit cancelled: %v, want %s", err, domain.CodeTransferState)
	}
	if err := admin.QueryRow(context.Background(), `select count(*) from outbox where type = 'dev.passage.inventory.transfer.changed.v1'
		and aggregate_id = $1`, o2.ID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("cancel events %d (%v), want 1", n, err)
	}
}
