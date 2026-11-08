package store

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	salesevents "github.com/BenjaminSRussell/passage/services/sales/internal/events"
)

func deliverIn(orderID, ref string, qty int64) NewProgress {
	return progressIn(orderID, domain.ProgressDelivered, ref, "k-"+ref, "h-"+ref, ProgressLineInput{LineNo: 1, Quantity: d(qty)})
}

// finalIn is deliverIn for the last delivery of the line (close for delivery).
func finalIn(orderID, ref string, qty int64) NewProgress {
	return progressIn(orderID, domain.ProgressDelivered, ref, "k-"+ref, "h-"+ref, ProgressLineInput{LineNo: 1, Quantity: d(qty), Final: true})
}

// TestPostgres_RecordProgressOverDeliveryWithinTolerance covers BR-27.9 and
// BR-28.7 on the sales side: with a 5 percent over-delivery rule a 100-unit line
// accepts 105 and follows it (quantity, net, total, version diff, event), 106
// is refused with nothing stored, a customer's own rule replaces the
// all-customers rule, and a customer without a rule keeps the exact quantity.
func TestPostgres_RecordProgressOverDeliveryWithinTolerance(t *testing.T) {
	s, pool, ctx := migrated(t)
	le, product, customer, other := ids.New(), ids.New(), ids.New(), ids.New()
	now := time.Now().UTC().Truncate(time.Microsecond)
	if _, _, err := s.CreateDeliveryTolerance(ctx, tolIn(le, product, nil, "2", "0", domain.PartialAllowed)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateDeliveryTolerance(ctx, tolIn(le, product, &customer, "5", "0", domain.PartialAllowed)); err != nil {
		t.Fatal(err)
	}

	o := confirmed(t, s, ctx, orderOf(le, customer, product, "2027-03-01", 100))
	if _, _, err := s.RecordProgress(ctx, deliverIn(o.ID, "PS-1", 106), now); problemCode(t, err) != CodeProgressQuantity {
		t.Fatalf("106 of 100 at 5%%: %v", err)
	}
	if got, _ := s.GetOrder(ctx, o.ID); got.Version != o.Version || !got.Lines[0].DeliveredQty.IsZero() || !got.Lines[0].Quantity.Equal(d(100)) {
		t.Fatalf("a refused report stores nothing: %+v", got)
	}
	rep, created, err := s.RecordProgress(ctx, deliverIn(o.ID, "PS-2", 105), now)
	if err != nil || !created {
		t.Fatalf("105 of 100 at 5%%: %v %v", err, created)
	}
	line := rep.Lines[0]
	if rep.OrderStatus != domain.OrderShipped || line.LineStatus != domain.LineDelivered || line.Tolerance != domain.ToleranceOver ||
		line.LineQuantity == nil || !line.LineQuantity.Equal(d(105)) || !line.DeliveredQty.Equal(d(105)) {
		t.Fatalf("report: %+v", rep)
	}
	got, _ := s.GetOrder(ctx, o.ID)
	if !got.Lines[0].Quantity.Equal(d(105)) || !got.Lines[0].LineNet.Equal(d(1050)) || !got.NetTotal.Equal(d(1050)) || got.Status != domain.OrderShipped {
		t.Fatalf("the line follows the delivery: %+v", got)
	}
	versions, _ := s.ListOrderVersions(ctx, o.ID)
	last := versions[len(versions)-1]
	seen := map[string]bool{}
	for _, c := range last.Diff {
		seen[c.Field] = true
	}
	if !seen["quantity"] || !seen["lineNet"] || !seen["deliveredQty"] || !seen["netTotal"] {
		t.Fatalf("the version names the quantity change: %+v", last.Diff)
	}
	var ev salesevents.OrderProgressData
	if err := json.Unmarshal(outboxData(t, pool, o.ID, salesevents.OrderProgressRecorded), &ev); err != nil ||
		ev.Lines[0].LineQuantity != "105" || ev.Lines[0].Tolerance != domain.ToleranceOver || ev.Lines[0].DeliveredQty != "105" {
		t.Fatalf("event: %+v %v", ev, err)
	}
	// The delivered goods are invoiced in full: the invoice rule is quantity = delivered.
	inv, _, err := s.RecordProgress(ctx, progressIn(o.ID, domain.ProgressInvoiced, "INV-1", "k-inv", "h-inv", ProgressLineInput{LineNo: 1, Quantity: d(105)}), now.Add(time.Second))
	if err != nil || inv.OrderStatus != domain.OrderInvoiced || inv.Lines[0].LineStatus != domain.LineInvoiced {
		t.Fatalf("invoice of 105: %+v %v", inv, err)
	}

	// Another customer gets the all-customers rule (2 percent), a customer
	// the rules do not reach nothing beyond 2: 102 is accepted, 103 is not.
	o2 := confirmed(t, s, ctx, orderOf(le, other, product, "2027-03-01", 100))
	if _, _, err := s.RecordProgress(ctx, deliverIn(o2.ID, "PS-3", 103), now); problemCode(t, err) != CodeProgressQuantity {
		t.Fatalf("103 of 100 at 2%%: %v", err)
	}
	if rep, _, err := s.RecordProgress(ctx, deliverIn(o2.ID, "PS-4", 102), now); err != nil || rep.Lines[0].Tolerance != domain.ToleranceOver {
		t.Fatalf("102 of 100 at 2%%: %+v %v", rep, err)
	}
	o3 := confirmed(t, s, ctx, orderOf(le, other, ids.New(), "2027-03-01", 100))
	if _, _, err := s.RecordProgress(ctx, deliverIn(o3.ID, "PS-5", 101), now); problemCode(t, err) != CodeProgressQuantity {
		t.Fatalf("a product without a rule keeps the exact quantity: %v", err)
	}
}

// TestPostgres_RecordProgressUnderDeliveryClosesRemainder covers the under-delivery
// side: a short delivery marked final within the tolerance completes the line
// and closes the remainder like :cancel-remainder (net, total, quota, schedule,
// order.remaindercancelled); without final a short slip within the tolerance
// stays partial so a split shipment is not cut; a final delivery short by more
// than the tolerance is refused and stores nothing.
func TestPostgres_RecordProgressUnderDeliveryClosesRemainder(t *testing.T) {
	s, pool, ctx := migrated(t)
	le, product, customer := ids.New(), ids.New(), ids.New()
	now := time.Now().UTC().Truncate(time.Microsecond)
	if _, _, err := s.CreateDeliveryTolerance(ctx, tolIn(le, product, nil, "0", "5", domain.PartialAllowed)); err != nil {
		t.Fatal(err)
	}
	quota, _, err := s.CreateAllocationQuota(ctx, quotaIn(le, product, nil, "2027-03-01", "2027-03-31", 500))
	if err != nil {
		t.Fatal(err)
	}

	short := confirmed(t, s, ctx, orderOf(le, customer, product, "2027-03-01", 100))
	if q, _ := s.GetAllocationQuota(ctx, quota.ID); !q.ConsumedQty.Equal(d(100)) {
		t.Fatalf("quota drawn at confirm: %+v", q)
	}
	rep, _, err := s.RecordProgress(ctx, deliverIn(short.ID, "PS-1", 94), now)
	if err != nil || rep.Lines[0].Tolerance != "" || rep.Lines[0].LineStatus != domain.LinePicked || rep.OrderStatus != domain.OrderPartiallyShipped {
		t.Fatalf("94 of 100 at 5%% is a partial delivery: %+v %v", rep, err)
	}
	rep, _, err = s.RecordProgress(ctx, finalIn(short.ID, "PS-2", 2), now.Add(time.Second))
	if err != nil || rep.Lines[0].Tolerance != domain.ToleranceUnder || rep.Lines[0].LineStatus != domain.LineDelivered ||
		!rep.Lines[0].LineQuantity.Equal(d(96)) || rep.OrderStatus != domain.OrderShipped {
		t.Fatalf("a final 96 of 100 at 5%% completes the line: %+v %v", rep, err)
	}
	got, _ := s.GetOrder(ctx, short.ID)
	if !got.Lines[0].Quantity.Equal(d(96)) || !got.Lines[0].LineNet.Equal(d(960)) || !got.NetTotal.Equal(d(960)) {
		t.Fatalf("net and total follow the closed remainder: %+v", got)
	}
	if q, _ := s.GetAllocationQuota(ctx, quota.ID); !q.ConsumedQty.Equal(d(96)) {
		t.Fatalf("the quota gets 4 back: %+v", q)
	}
	var open int
	if err := pool.QueryRow(ctx, `select count(*) from order_schedule_line where line_id = $1 and status in ('CONFIRMED', 'BACKORDER', 'UNPROMISED')`, got.Lines[0].ID).Scan(&open); err != nil || open != 0 {
		t.Fatalf("open schedule lines: %d %v", open, err)
	}
	var rc salesevents.OrderRemainderCancelledData
	if err := json.Unmarshal(outboxData(t, pool, short.ID, salesevents.OrderRemainderCancelled), &rc); err != nil || len(rc.Lines) != 1 ||
		rc.Lines[0].CancelledQty != "4" || rc.Lines[0].KeptQty != "96" || rc.Status != domain.OrderShipped || rc.Version != got.Version {
		t.Fatalf("remainder event: %+v %v", rc, err)
	}
	// A replay of the final slip under its key changes nothing more.
	if again, created, err := s.RecordProgress(ctx, finalIn(short.ID, "PS-2", 2), now.Add(2*time.Second)); err != nil || created || again.ID != rep.ID {
		t.Fatalf("replay: %+v %v %v", again, created, err)
	}
	if q, _ := s.GetAllocationQuota(ctx, quota.ID); !q.ConsumedQty.Equal(d(96)) {
		t.Fatalf("a replay gives nothing back twice: %+v", q)
	}
	if _, _, err := s.RecordProgress(ctx, deliverIn(short.ID, "PS-3", 4), now.Add(2*time.Second)); problemCode(t, err) != CodeNotProgressable {
		t.Fatalf("nothing is left to deliver: %v", err)
	}

	// A split shipment: 96 not marked final stays partial (the warehouse still
	// has 4 on the way) and the last 4 complete the line exactly.
	split := confirmed(t, s, ctx, orderOf(le, customer, product, "2027-03-01", 100))
	rep, _, err = s.RecordProgress(ctx, deliverIn(split.ID, "PS-10", 96), now)
	if err != nil || rep.Lines[0].Tolerance != "" || rep.Lines[0].LineStatus != domain.LinePicked || !rep.Lines[0].LineQuantity.Equal(d(100)) {
		t.Fatalf("96 of 100 not final: %+v %v", rep, err)
	}
	rep, _, err = s.RecordProgress(ctx, deliverIn(split.ID, "PS-11", 4), now.Add(time.Second))
	if err != nil || rep.Lines[0].Tolerance != "" || rep.OrderStatus != domain.OrderShipped {
		t.Fatalf("the last 4: %+v %v", rep, err)
	}
	var remainders int
	if err := pool.QueryRow(ctx, `select count(*) from outbox where aggregate_id = $1 and type = $2`, split.ID, salesevents.OrderRemainderCancelled).Scan(&remainders); err != nil || remainders != 0 {
		t.Fatalf("no remainder was cancelled: %d %v", remainders, err)
	}

	// A final delivery below the bound is refused and stores nothing; the same
	// slip without final is a partial delivery that can be closed by hand.
	low := confirmed(t, s, ctx, orderOf(le, customer, product, "2027-03-01", 100))
	if _, _, err := s.RecordProgress(ctx, finalIn(low.ID, "PS-4", 94), now); problemCode(t, err) != CodeProgressQuantity {
		t.Fatalf("a final 94 of 100 at 5%%: %v", err)
	}
	if got, _ := s.GetOrder(ctx, low.ID); got.Version != low.Version || !got.Lines[0].DeliveredQty.IsZero() {
		t.Fatalf("a refused report stores nothing: %+v", got)
	}
	rep, _, err = s.RecordProgress(ctx, deliverIn(low.ID, "PS-5", 94), now)
	if err != nil || rep.Lines[0].Tolerance != "" || !rep.Lines[0].LineQuantity.Equal(d(100)) {
		t.Fatalf("94 of 100: %+v %v", rep, err)
	}
}

// TestPostgres_RecordProgressConcurrentFinalSlips covers two final slips for the
// same line at once: the order row lock serialises them, one closes the
// remainder and the other is refused, so the quota is given back once and one
// order.remaindercancelled is queued.
func TestPostgres_RecordProgressConcurrentFinalSlips(t *testing.T) {
	s, pool, ctx := migrated(t)
	le, product, customer := ids.New(), ids.New(), ids.New()
	now := time.Now().UTC().Truncate(time.Microsecond)
	if _, _, err := s.CreateDeliveryTolerance(ctx, tolIn(le, product, nil, "0", "5", domain.PartialAllowed)); err != nil {
		t.Fatal(err)
	}
	quota, _, err := s.CreateAllocationQuota(ctx, quotaIn(le, product, nil, "2027-03-01", "2027-03-31", 500))
	if err != nil {
		t.Fatal(err)
	}
	o := confirmed(t, s, ctx, orderOf(le, customer, product, "2027-03-01", 100))
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, ref := range []string{"PS-A", "PS-B"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, errs[i] = s.RecordProgress(ctx, finalIn(o.ID, ref, 96), now)
		}()
	}
	wg.Wait()
	ok, refused := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case problemCode(t, err) == CodeNotProgressable:
			refused++
		default:
			t.Fatalf("unexpected: %v", err)
		}
	}
	if ok != 1 || refused != 1 {
		t.Fatalf("one slip closes the line, the other is refused: %v", errs)
	}
	if q, _ := s.GetAllocationQuota(ctx, quota.ID); !q.ConsumedQty.Equal(d(96)) {
		t.Fatalf("the quota gets 4 back once: %+v", q)
	}
	var remainders int
	if err := pool.QueryRow(ctx, `select count(*) from outbox where aggregate_id = $1 and type = $2`, o.ID, salesevents.OrderRemainderCancelled).Scan(&remainders); err != nil || remainders != 1 {
		t.Fatalf("remainder events: %d %v", remainders, err)
	}
}

// TestPostgres_RecordProgressToleranceFollowsAgreement covers BR-27.9 for the
// release line of an agreement order: an over-delivery draws the extra quantity
// under the agreement (refused beyond the ceiling, nothing stored) and an
// under-delivery gives the closed remainder back.
func TestPostgres_RecordProgressToleranceFollowsAgreement(t *testing.T) {
	s, _, ctx := migrated(t)
	le := ids.New()
	now := time.Date(2027, 3, 10, 9, 0, 0, 0, time.UTC)
	a := toleranceAgreement(t, s, ctx, le, 100, "0")
	if _, _, err := s.CreateDeliveryTolerance(ctx, tolIn(le, a.ProductID, nil, "10", "10", domain.PartialAllowed)); err != nil {
		t.Fatal(err)
	}
	r1, _, err := s.ReleaseAgreement(ctx, a.ID, release(60, "r1"), now)
	if err != nil {
		t.Fatal(err)
	}
	over, err := s.ConfirmOrder(ctx, r1.Order.ID, r1.Order.Version, "rep", now)
	if err != nil || !over.Confirmed {
		t.Fatalf("confirm: %v", err)
	}
	if rep, _, err := s.RecordProgress(ctx, deliverIn(over.Order.ID, "PS-1", 66), now); err != nil || rep.Lines[0].Tolerance != domain.ToleranceOver {
		t.Fatalf("66 of 60: %+v %v", rep, err)
	}
	if got, _ := s.GetAgreement(ctx, a.ID); !got.Released.Equal(d(66)) || !got.Remaining.Equal(d(34)) {
		t.Fatalf("the extra 6 is drawn: %+v", got)
	}
	// A replay of the same Idempotency-Key draws nothing again.
	if _, created, err := s.RecordProgress(ctx, deliverIn(over.Order.ID, "PS-1", 66), now.Add(time.Second)); err != nil || created {
		t.Fatalf("replay: %v %v", created, err)
	}
	if got, _ := s.GetAgreement(ctx, a.ID); !got.Released.Equal(d(66)) {
		t.Fatalf("a replay draws once: %+v", got)
	}

	// The last 34 are released; 37 would be inside the 10 percent (37.4) but
	// draws 3 beyond the ceiling, so the whole report is refused.
	r2, _, err := s.ReleaseAgreement(ctx, a.ID, release(34, "r2"), now)
	if err != nil {
		t.Fatal(err)
	}
	c2, err := s.ConfirmOrder(ctx, r2.Order.ID, r2.Order.Version, "rep", now)
	if err != nil || !c2.Confirmed {
		t.Fatalf("confirm: %v", err)
	}
	if _, _, err := s.RecordProgress(ctx, deliverIn(c2.Order.ID, "PS-2", 37), now); problemCode(t, err) != CodeAgreementExhausted {
		t.Fatalf("beyond the agreement ceiling: %v", err)
	}
	if got, _ := s.GetOrder(ctx, c2.Order.ID); !got.Lines[0].Quantity.Equal(d(34)) || !got.Lines[0].DeliveredQty.IsZero() {
		t.Fatalf("nothing stored by the refused report: %+v", got.Lines[0])
	}
	if got, _ := s.GetAgreement(ctx, a.ID); !got.Released.Equal(d(100)) || got.Status != domain.AgreementExhausted {
		t.Fatalf("agreement unchanged: %+v", got)
	}
	// 30 of 34 is below the 30.6 bound (a partial delivery); 1 more marked final
	// is within it and gives the 3 closed units back to the agreement.
	if rep, _, err := s.RecordProgress(ctx, deliverIn(c2.Order.ID, "PS-3", 30), now); err != nil || rep.Lines[0].Tolerance != "" {
		t.Fatalf("30 of 34: %+v %v", rep, err)
	}
	if rep, _, err := s.RecordProgress(ctx, finalIn(c2.Order.ID, "PS-4", 1), now.Add(time.Second)); err != nil || rep.Lines[0].Tolerance != domain.ToleranceUnder {
		t.Fatalf("31 of 34: %+v %v", rep, err)
	}
	if got, _ := s.GetAgreement(ctx, a.ID); !got.Released.Equal(d(97)) || got.Status != domain.AgreementActive {
		t.Fatalf("the closed remainder goes back: %+v", got)
	}
}
