package store

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	salesevents "github.com/BenjaminSRussell/passage/services/sales/internal/events"
)

// TestPostgres_FulfillmentFollowsCutsAndCancels: a quantity cut on a confirmed
// line, a raise after it and a whole-order cancel each queue
// order.remaindercancelled.v1, the event fulfillment follows, with the kept
// quantity per line; a price change and a DRAFT order's cancel queue none.
func TestPostgres_FulfillmentFollowsCutsAndCancels(t *testing.T) {
	s, pool, ctx := migrated(t)
	le, customer := ids.New(), ids.New()
	order := changeOrderFixture(t, s, ctx, le, customer, 10, 7) // 5 x 10, 5 x 7
	now := time.Date(2027, 3, 1, 9, 0, 0, 0, time.UTC)
	confirmed, err := s.ConfirmOrder(ctx, order.ID, order.Version, "sales-rep", now)
	if err != nil || !confirmed.Confirmed {
		t.Fatalf("confirm: %v %v", confirmed.Confirmed, err)
	}
	cut, err := s.ChangeOrder(ctx, order.ID, confirmed.Order.Version, ChangeRequest{Reason: "customer needs fewer",
		Lines: []LineChange{{LineNo: 1, Quantity: dp("3")}}}, "sales-rep", now)
	if err != nil {
		t.Fatalf("cut: %v", err)
	}
	var data salesevents.OrderRemainderCancelledData
	if err := json.Unmarshal(outboxData(t, pool, order.ID, salesevents.OrderRemainderCancelled), &data); err != nil {
		t.Fatal(err)
	}
	if data.Version != cut.Version || data.Status != domain.OrderConfirmed || len(data.Lines) != 1 || data.Lines[0].LineNo != 1 ||
		data.Lines[0].CancelledQty != "2" || data.Lines[0].KeptQty != "3" || data.Lines[0].LineStatus == domain.LineCancelled {
		t.Fatalf("quantity cut event = %+v", data)
	}
	// A raise after the cut lifts fulfillment's stored ceiling again: kept is
	// the new quantity and nothing is given up; a price-only change queues none.
	raised, err := s.ChangeOrder(ctx, order.ID, cut.Version, ChangeRequest{Lines: []LineChange{{LineNo: 1, Quantity: dp("4")}}}, "sales-rep", now)
	if err != nil {
		t.Fatal(err)
	}
	if n := countWhere(t, pool, `select count(*) from outbox where aggregate_id = $1 and type = $2 and (payload->'data'->>'version')::int = $3
		and payload->'data'->>'status' = 'CONFIRMED' and jsonb_array_length(payload->'data'->'lines') = 1
		and payload->'data'->'lines'->0->>'line_no' = '1' and payload->'data'->'lines'->0->>'kept_qty' = '4'
		and payload->'data'->'lines'->0->>'cancelled_qty' = '0' and payload->'data'->'lines'->0->>'line_status' <> 'CANCELLED'`,
		order.ID, salesevents.OrderRemainderCancelled, raised.Version); n != 1 {
		t.Fatalf("a raise after a cut must restate the kept quantity 4")
	}
	repriced, err := s.ChangeOrder(ctx, order.ID, raised.Version, ChangeRequest{Lines: []LineChange{{LineNo: 2, UnitPrice: dp("8")}}}, "sales-rep", now)
	if err != nil {
		t.Fatal(err)
	}
	if n := countWhere(t, pool, `select count(*) from outbox where aggregate_id = $1 and type = $2`, order.ID, salesevents.OrderRemainderCancelled); n != 2 {
		t.Fatalf("a price change moves no quantity and queues no remainder event, got %d events", n)
	}
	raised = repriced

	cancelled, err := s.CancelOrder(ctx, order.ID, raised.Version, "customer withdrew", "sales-rep", now)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if n := countWhere(t, pool, `select count(*) from outbox where aggregate_id = $1 and type = $2 and (payload->'data'->>'version')::int = $3
		and payload->'data'->>'status' = 'CANCELLED' and jsonb_array_length(payload->'data'->'lines') = 2
		and not exists (select 1 from jsonb_array_elements(payload->'data'->'lines') x where x->>'kept_qty' <> '0')`,
		order.ID, salesevents.OrderRemainderCancelled, cancelled.Version); n != 1 {
		t.Fatalf("whole-order cancel must queue one remainder event with both lines kept at 0")
	}

	// An order that was never confirmed has no shipment to cut.
	draft := changeOrderFixture(t, s, ctx, le, customer, 10)
	if _, err := s.CancelOrder(ctx, draft.ID, draft.Version, "typo", "sales-rep", now); err != nil {
		t.Fatal(err)
	}
	if n := countWhere(t, pool, `select count(*) from outbox where aggregate_id = $1 and type = $2`, draft.ID, salesevents.OrderRemainderCancelled); n != 0 {
		t.Fatalf("a draft cancel must queue no remainder event, got %d", n)
	}
}

// TestPostgres_FulfillmentFollowsSupplementaryReopen: on a confirmed order a
// cut that takes the parent below a MANDATORY rule's base cancels the
// supplementary line (kept 0 for fulfillment); raising the parent again
// reopens it, and the same event lifts its kept quantity back so fulfillment
// does not keep the reopened line at 0.
func TestPostgres_FulfillmentFollowsSupplementaryReopen(t *testing.T) {
	s, pool, ctx := migrated(t)
	le, customer, fg, manual := ids.New(), ids.New(), ids.New(), ids.New()
	if _, err := s.CreateSupplementaryRule(ctx, domain.SupplementaryRule{LegalEntityID: le, ParentProductID: fg, SupplementaryProduct: manual, Unit: "EA",
		Requirement: domain.SuppMandatory, BaseQuantity: d(10), QuantityPerBase: d(1), Pricing: domain.SuppFree, UnitPrice: d(0)}, "product-admin"); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2027, 3, 1, 9, 0, 0, 0, time.UTC)
	in := oneLineOrder(le, customer, fg, now)
	in.Lines[0].Quantity, in.Lines[0].LineNet = d(25), d(250)
	order, _, err := s.CreateOrder(ctx, in)
	if err != nil || len(order.Lines) != 2 {
		t.Fatalf("create: %v %+v", err, order.Lines)
	}
	confirmed, err := s.ConfirmOrder(ctx, order.ID, order.Version, "sales-rep", now)
	if err != nil || !confirmed.Confirmed {
		t.Fatalf("confirm: %v %v", confirmed.Confirmed, err)
	}
	cut, err := s.ChangeOrder(ctx, order.ID, confirmed.Order.Version, ChangeRequest{Reason: "cut", Lines: []LineChange{{LineNo: 1, Quantity: dp("9")}}}, "sales-rep", now)
	if err != nil || cut.Lines[1].LineStatus != domain.LineCancelled {
		t.Fatalf("cut: %v %+v", err, cut.Lines)
	}
	lineOf := func(version, lineNo int) (kept, cancelled, status string) {
		t.Helper()
		if err := pool.QueryRow(ctx, `select x->>'kept_qty', x->>'cancelled_qty', x->>'line_status' from outbox o, jsonb_array_elements(o.payload->'data'->'lines') x
			where o.aggregate_id = $1 and o.type = $2 and (o.payload->'data'->>'version')::int = $3 and (x->>'line_no')::int = $4`,
			order.ID, salesevents.OrderRemainderCancelled, version, lineNo).Scan(&kept, &cancelled, &status); err != nil {
			t.Fatalf("version %d line %d: %v", version, lineNo, err)
		}
		return
	}
	if k, c, st := lineOf(cut.Version, 1); k != "9" || c != "16" || st == domain.LineCancelled {
		t.Fatalf("cut parent = %s %s %s", k, c, st)
	}
	if k, c, st := lineOf(cut.Version, 2); k != "0" || c != "2" || st != domain.LineCancelled {
		t.Fatalf("cancelled supplementary line = %s %s %s", k, c, st)
	}
	back, err := s.ChangeOrder(ctx, order.ID, cut.Version, ChangeRequest{Reason: "back", Lines: []LineChange{{LineNo: 1, Quantity: dp("20")}}}, "sales-rep", now)
	if err != nil || back.Lines[1].LineStatus == domain.LineCancelled {
		t.Fatalf("raise: %v %+v", err, back.Lines)
	}
	if k, c, st := lineOf(back.Version, 1); k != "20" || c != "0" || st == domain.LineCancelled {
		t.Fatalf("raised parent = %s %s %s", k, c, st)
	}
	if k, c, st := lineOf(back.Version, 2); k != "2" || c != "0" || st == domain.LineCancelled {
		t.Fatalf("reopened supplementary line = %s %s %s", k, c, st)
	}
}
