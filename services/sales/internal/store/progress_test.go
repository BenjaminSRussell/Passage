package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	salesevents "github.com/BenjaminSRussell/passage/services/sales/internal/events"
)

func progressIn(orderID, kind, ref, key, hash string, lines ...ProgressLineInput) NewProgress {
	return NewProgress{OrderID: orderID, Kind: kind, SourceRef: ref, Lines: lines, IdempotencyKey: key, RequestHash: hash, Actor: "fulfillment"}
}

// TestPostgres_RecordProgress covers BR-27.5: partial and full deliveries move
// the line and the order, an invoice follows delivery, a repeat is answered
// from the stored record, nothing past the ordered or delivered quantity is
// accepted, and every report writes a version and one event.
func TestPostgres_RecordProgress(t *testing.T) {
	s, pool, ctx := migrated(t)
	le, product := ids.New(), ids.New()
	o := confirmed(t, s, ctx, orderOf(le, ids.New(), product, "2027-03-01", 10))
	now := time.Now().UTC().Truncate(time.Microsecond)

	if _, _, err := s.RecordProgress(ctx, progressIn(o.ID, domain.ProgressInvoiced, "INV-0", "k0", "h0", ProgressLineInput{LineNo: 1, Quantity: d(1)}), now); problemCode(t, err) != CodeProgressQuantity {
		t.Fatalf("invoice before delivery: %v", err)
	}
	if _, _, err := s.RecordProgress(ctx, progressIn(o.ID, domain.ProgressDelivered, "PS-0", "k0b", "h0", ProgressLineInput{LineNo: 9, Quantity: d(1)}), now); problemCode(t, err) != CodeLineNotFound {
		t.Fatalf("unknown line: %v", err)
	}

	first, created, err := s.RecordProgress(ctx, progressIn(o.ID, domain.ProgressDelivered, "PS-1", "k1", "h1", ProgressLineInput{LineNo: 1, Quantity: d(4)}), now)
	if err != nil || !created {
		t.Fatalf("first delivery: %v %v", err, created)
	}
	if first.OrderStatus != domain.OrderPartiallyShipped || first.OrderVersion != o.Version+1 || first.Lines[0].LineStatus != domain.LinePicked || !first.Lines[0].DeliveredQty.Equal(d(4)) {
		t.Fatalf("after the first delivery: %+v", first)
	}
	got, _ := s.GetOrder(ctx, o.ID)
	if got.Status != domain.OrderPartiallyShipped || !got.Lines[0].DeliveredQty.Equal(d(4)) || got.Lines[0].LineStatus != domain.LinePicked {
		t.Fatalf("order: %+v", got)
	}
	// A partly delivered line can no longer be changed or cancelled.
	if _, err := s.CancelOrderAs(ctx, o.ID, got.Version, "x", "rep", true, now); problemCode(t, err) != CodeNotCancellable && problemCode(t, err) != CodeNotChangeable {
		t.Fatalf("cancel after delivery: %v", err)
	}

	replay, created, err := s.RecordProgress(ctx, progressIn(o.ID, domain.ProgressDelivered, "PS-1", "k1", "h1", ProgressLineInput{LineNo: 1, Quantity: d(4)}), now)
	if err != nil || created || replay.ID != first.ID {
		t.Fatalf("replay by key: %v %v", err, created)
	}
	replay, created, err = s.RecordProgress(ctx, progressIn(o.ID, domain.ProgressDelivered, "PS-1", "k-new", "h1", ProgressLineInput{LineNo: 1, Quantity: d(4)}), now)
	if err != nil || created || replay.ID != first.ID {
		t.Fatalf("replay of the same source: %v %v", err, created)
	}
	if _, _, err := s.RecordProgress(ctx, progressIn(o.ID, domain.ProgressDelivered, "PS-1", "k-new2", "other", ProgressLineInput{LineNo: 1, Quantity: d(5)}), now); problemCode(t, err) != CodeProgressRepeated {
		t.Fatalf("same source, other body: %v", err)
	}
	if _, _, err := s.RecordProgress(ctx, progressIn(o.ID, domain.ProgressDelivered, "PS-2", "k1", "different", ProgressLineInput{LineNo: 1, Quantity: d(1)}), now); problemCode(t, err) != CodeIdempotencyReuse {
		t.Fatalf("same key, other body: %v", err)
	}
	if _, _, err := s.RecordProgress(ctx, progressIn(o.ID, domain.ProgressDelivered, "PS-3", "k3", "h3", ProgressLineInput{LineNo: 1, Quantity: d(7)}), now); problemCode(t, err) != CodeProgressQuantity {
		t.Fatalf("over-delivery: %v", err)
	}

	// Invoice what shipped, then the rest of the goods and the rest of the invoice.
	if _, _, err := s.RecordProgress(ctx, progressIn(o.ID, domain.ProgressInvoiced, "INV-1", "k4", "h4", ProgressLineInput{LineNo: 1, Quantity: d(4)}), now); err != nil {
		t.Fatal(err)
	}
	del, _, err := s.RecordProgress(ctx, progressIn(o.ID, domain.ProgressDelivered, "PS-4", "k5", "h5", ProgressLineInput{LineNo: 1, Quantity: d(6)}), now)
	if err != nil || del.OrderStatus != domain.OrderShipped || del.Lines[0].LineStatus != domain.LineDelivered {
		t.Fatalf("all delivered: %+v %v", del, err)
	}
	inv, _, err := s.RecordProgress(ctx, progressIn(o.ID, domain.ProgressInvoiced, "INV-2", "k6", "h6", ProgressLineInput{LineNo: 1, Quantity: d(6)}), now.Add(time.Second))
	if err != nil || inv.OrderStatus != domain.OrderInvoiced || inv.Lines[0].LineStatus != domain.LineInvoiced {
		t.Fatalf("all invoiced: %+v %v", inv, err)
	}
	if _, _, err := s.RecordProgress(ctx, progressIn(o.ID, domain.ProgressInvoiced, "INV-3", "k7", "h7", ProgressLineInput{LineNo: 1, Quantity: d(1)}), now); problemCode(t, err) != CodeNotProgressable {
		t.Fatalf("an invoiced order takes nothing: %v", err)
	}

	list, err := s.ListProgress(ctx, ProgressFilter{LegalEntityID: le, OrderID: o.ID, Limit: 10})
	if err != nil || len(list) != 4 || list[0].SourceRef != "INV-2" {
		t.Fatalf("list: %v %+v", err, list)
	}
	if byKind, _ := s.ListProgress(ctx, ProgressFilter{OrderID: o.ID, Kind: domain.ProgressDelivered, Limit: 10}); len(byKind) != 2 {
		t.Fatalf("filter by kind: %+v", byKind)
	}
	versions, _ := s.ListOrderVersions(ctx, o.ID)
	if len(versions) != 4 {
		t.Fatalf("a version per report: %d", len(versions))
	}
	n := 0
	for _, typ := range outboxTypes(t, pool, o.ID) {
		if typ == salesevents.OrderProgressRecorded {
			n++
		}
	}
	if n != 4 {
		t.Fatalf("one event per stored report: %d", n)
	}
	var conf salesevents.OrderConfirmedData
	if err := json.Unmarshal(outboxData(t, pool, o.ID, salesevents.OrderConfirmed), &conf); err != nil {
		t.Fatal(err)
	}
	if len(conf.Lines) != 1 || conf.Lines[0].AtpQty != "10" || conf.OrderType != "ORDER" {
		t.Fatalf("order.confirmed carries what a release call needs: %+v", conf)
	}
}

func TestPostgres_RecordProgressRefusals(t *testing.T) {
	s, _, ctx := migrated(t)
	le := ids.New()
	now := time.Now().UTC().Truncate(time.Microsecond)
	draft, _, err := s.CreateOrder(ctx, orderOf(le, ids.New(), ids.New(), "2027-03-01", 3))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.RecordProgress(ctx, progressIn(draft.ID, domain.ProgressDelivered, "PS-1", "a", "h", ProgressLineInput{LineNo: 1, Quantity: d(1)}), now); problemCode(t, err) != CodeNotProgressable {
		t.Fatalf("a draft order cannot ship: %v", err)
	}
	if _, _, err := s.RecordProgress(ctx, progressIn(ids.New(), domain.ProgressDelivered, "PS-1", "b", "h", ProgressLineInput{LineNo: 1, Quantity: d(1)}), now); problemCode(t, err) != CodeOrderNotFound {
		t.Fatalf("unknown order: %v", err)
	}
	j := orderOf(le, ids.New(), ids.New(), "2027-03-01", 3)
	j.OrderType = domain.OrderTypeJournal
	jo := confirmed(t, s, ctx, j)
	if _, _, err := s.RecordProgress(ctx, progressIn(jo.ID, domain.ProgressDelivered, "PS-2", "c", "h", ProgressLineInput{LineNo: 1, Quantity: d(1)}), now); problemCode(t, err) != CodeNotProgressable {
		t.Fatalf("a JOURNAL order never ships: %v", err)
	}
}

// TestPostgres_RecordProgressOrderTypes: a RETURNED order takes no report
// (returns post their own receipts and credit notes), a SUBSCRIPTION order
// ships against its lines but is invoiced per period by billing, never
// against the line quantity.
func TestPostgres_RecordProgressOrderTypes(t *testing.T) {
	s, _, ctx := migrated(t)
	le := ids.New()
	now := time.Now().UTC().Truncate(time.Microsecond)
	ret := orderOf(le, ids.New(), ids.New(), "2027-03-01", 3)
	ret.OrderType = domain.OrderTypeReturned
	ro := confirmed(t, s, ctx, ret)
	if _, _, err := s.RecordProgress(ctx, progressIn(ro.ID, domain.ProgressDelivered, "PS-1", "r1", "h", ProgressLineInput{LineNo: 1, Quantity: d(1)}), now); problemCode(t, err) != CodeNotProgressable {
		t.Fatalf("a RETURNED order is not delivered: %v", err)
	}
	sub := orderOf(le, ids.New(), ids.New(), "2027-03-01", 3)
	sub.OrderType = domain.OrderTypeSubscription
	so := confirmed(t, s, ctx, sub)
	if _, _, err := s.RecordProgress(ctx, progressIn(so.ID, domain.ProgressDelivered, "PS-1", "s1", "h1", ProgressLineInput{LineNo: 1, Quantity: d(3)}), now); err != nil {
		t.Fatalf("a SUBSCRIPTION order ships: %v", err)
	}
	if _, _, err := s.RecordProgress(ctx, progressIn(so.ID, domain.ProgressInvoiced, "INV-1", "s2", "h2", ProgressLineInput{LineNo: 1, Quantity: d(3)}), now); problemCode(t, err) != CodeNotProgressable {
		t.Fatalf("a SUBSCRIPTION order is not invoiced against its lines: %v", err)
	}
}

// TestPostgres_PartlyShippedOrderStaysPromisable: once one line has shipped
// the order is PARTIALLY_SHIPPED, and its lines that have not shipped can
// still be promised (FR-28.8), while the delivered line cannot.
func TestPostgres_PartlyShippedOrderStaysPromisable(t *testing.T) {
	s, _, ctx := migrated(t)
	in := orderOf(ids.New(), ids.New(), ids.New(), "2027-03-01", 10)
	second := in.Lines[0]
	second.LineNo, second.ProductID = 2, ids.New()
	in.Lines = append(in.Lines, second)
	o := confirmed(t, s, ctx, in)
	now := time.Now().UTC().Truncate(time.Microsecond)
	rec, _, err := s.RecordProgress(ctx, progressIn(o.ID, domain.ProgressDelivered, "PS-1", "p1", "h1", ProgressLineInput{LineNo: 1, Quantity: d(10)}), now)
	if err != nil || rec.OrderStatus != domain.OrderPartiallyShipped {
		t.Fatalf("first line shipped: %+v %v", rec, err)
	}
	if _, err := s.PromiseLine(ctx, o.ID, rec.OrderVersion, PromiseRequest{LineNo: 1, Periods: atpBuckets()}, "planner", now); problemCode(t, err) != CodeNotPromisable {
		t.Fatalf("a delivered line cannot be promised: %v", err)
	}
	res, err := s.PromiseLine(ctx, o.ID, rec.OrderVersion, PromiseRequest{LineNo: 2, Periods: atpBuckets()}, "planner", now)
	if err != nil {
		t.Fatalf("the open line of a partly shipped order is promised: %v", err)
	}
	if res.Order.Status != domain.OrderPartiallyShipped {
		t.Fatalf("a promise keeps the order status: %s", res.Order.Status)
	}
	runOf := func(l Line, key string) NewBackorderRun {
		return NewBackorderRun{LegalEntityID: in.LegalEntityID, ProductID: l.ProductID, SiteID: l.SiteID, WarehouseID: l.WarehouseID, Strategy: domain.StrategyFill,
			Supply: d(1), SupplyDate: "2027-03-10", LoseThreshold: 3, DefaultPriority: 5, IdempotencyKey: key, RequestHash: key, Actor: "planner"}
	}
	run, _, err := s.CreateBackorderRun(ctx, runOf(second, "bo-open"), now)
	if err != nil || len(run.Lines) != 1 || run.Lines[0].OrderID != o.ID || run.Lines[0].LineNo != 2 {
		t.Fatalf("a backorder run takes the open line of a partly shipped order: %+v %v", run, err)
	}
	if _, _, err := s.CreateBackorderRun(ctx, runOf(in.Lines[0], "bo-shipped"), now); problemCode(t, err) != CodeBackorderRunRefused {
		t.Fatalf("a delivered line is not in a backorder run: %v", err)
	}
}

// TestPostgres_ProgressLegalEntityScope: a caller scoped to another legal
// entity neither sees an order's reports nor records one on it.
func TestPostgres_ProgressLegalEntityScope(t *testing.T) {
	s, _, ctx := migrated(t)
	le := ids.New()
	o := confirmed(t, s, ctx, orderOf(le, ids.New(), ids.New(), "2027-03-01", 5))
	now := time.Now().UTC().Truncate(time.Microsecond)
	if _, _, err := s.RecordProgress(ctx, progressIn(o.ID, domain.ProgressDelivered, "PS-1", "l1", "h1", ProgressLineInput{LineNo: 1, Quantity: d(2)}), now); err != nil {
		t.Fatal(err)
	}
	other := ids.New()
	stranger := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: other, UserID: "x", Duties: testkit.ScopeDuties(other)})
	if list, err := s.ListProgress(stranger, ProgressFilter{OrderID: o.ID, Limit: 10}); err != nil || len(list) != 0 {
		t.Fatalf("another legal entity sees %d reports: %v", len(list), err)
	}
	if _, _, err := s.RecordProgress(stranger, progressIn(o.ID, domain.ProgressDelivered, "PS-2", "l2", "h2", ProgressLineInput{LineNo: 1, Quantity: d(1)}), now); problemCode(t, err) != CodeOrderNotFound {
		t.Fatalf("another legal entity records on the order: %v", err)
	}
}
