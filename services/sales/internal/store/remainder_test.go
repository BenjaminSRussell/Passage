package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	salesevents "github.com/BenjaminSRussell/passage/services/sales/internal/events"
)

func deliver(t *testing.T, s *Postgres, ctx context.Context, orderID, ref string, lineNo int, qty int64) {
	t.Helper()
	if _, _, err := s.RecordProgress(ctx, progressIn(orderID, domain.ProgressDelivered, ref, "k-"+ref, "h-"+ref, ProgressLineInput{LineNo: lineNo, Quantity: d(qty)}), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
}

// TestPostgres_CancelRemainder covers BR-27.5: a partly shipped two-line order
// has its undelivered line cancelled and its half-delivered line closed at the
// delivered quantity; the total, the quota, the schedule and the version
// history follow, one event is queued, and the order cannot be closed twice.
func TestPostgres_CancelRemainder(t *testing.T) {
	s, pool, ctx := migrated(t)
	le, customer, a, b := ids.New(), ids.New(), ids.New(), ids.New()
	q, _, err := s.CreateAllocationQuota(ctx, quotaIn(le, a, nil, "2027-03-01", "2027-03-31", 100))
	if err != nil {
		t.Fatal(err)
	}
	in := orderOf(le, customer, a, "2027-03-10", 10)
	line2 := in.Lines[0]
	line2.LineNo, line2.ProductID = 2, b
	in.Lines = append(in.Lines, line2)
	o := confirmed(t, s, ctx, in)
	now := time.Now().UTC().Truncate(time.Microsecond)

	if _, err := s.CancelRemainder(ctx, o.ID, o.Version, "early", "rep", now); problemCode(t, err) != CodeNotCancellable {
		t.Fatalf("a confirmed order has no remainder: %v", err)
	}
	deliver(t, s, ctx, o.ID, "PS-1", 1, 4)
	got, _ := s.GetOrder(ctx, o.ID)
	if got.Status != domain.OrderPartiallyShipped || !got.NetTotal.Equal(d(200)) {
		t.Fatalf("before: %+v", got)
	}
	if _, err := s.CancelRemainder(ctx, o.ID, o.Version, "stale", "rep", now); problemCode(t, err) != CodeVersionConflict {
		t.Fatalf("stale version: %v", err)
	}

	res, err := s.CancelRemainder(ctx, o.ID, got.Version, "customer takes 4", "rep", now)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != domain.OrderShipped || res.Version != got.Version+1 || !res.NetTotal.Equal(d(40)) {
		t.Fatalf("after: status %s version %d total %s", res.Status, res.Version, res.NetTotal)
	}
	l1, l2 := res.Lines[0], res.Lines[1]
	if !l1.Quantity.Equal(d(4)) || !l1.LineNet.Equal(d(40)) || l1.LineStatus != domain.LineDelivered || l2.LineStatus != domain.LineCancelled {
		t.Fatalf("lines: %+v %+v", l1, l2)
	}
	qa, _ := s.GetAllocationQuota(ctx, q.ID)
	if !qa.ConsumedQty.Equal(d(4)) {
		t.Fatalf("quota keeps only what was delivered: %s", qa.ConsumedQty)
	}
	if n := countWhere(t, pool, `select count(*) from order_schedule_line where order_id = $1 and status in ('CONFIRMED','BACKORDER','UNPROMISED')`, o.ID); n != 0 {
		t.Fatalf("%d schedule lines still open", n)
	}
	versions, _ := s.ListOrderVersions(ctx, o.ID)
	if len(versions) == 0 || versions[0].Version != res.Version || versions[0].Reason != "customer takes 4" {
		t.Fatalf("history: %+v", versions)
	}
	var data salesevents.OrderRemainderCancelledData
	if err := json.Unmarshal(outboxData(t, pool, o.ID, salesevents.OrderRemainderCancelled), &data); err != nil {
		t.Fatal(err)
	}
	if data.Status != domain.OrderShipped || len(data.Lines) != 2 || data.Lines[0].CancelledQty != "6" || data.Lines[0].KeptQty != "4" || data.Lines[1].CancelledQty != "10" || data.Lines[1].KeptQty != "0" {
		t.Fatalf("event: %+v", data)
	}
	if _, err := s.CancelRemainder(ctx, o.ID, res.Version, "again", "rep", now); problemCode(t, err) != CodeNotCancellable {
		t.Fatalf("closing twice: %v", err)
	}
	// Shipped goods can still be invoiced for what was delivered.
	if _, _, err := s.RecordProgress(ctx, progressIn(o.ID, domain.ProgressInvoiced, "INV-1", "k-inv", "h-inv", ProgressLineInput{LineNo: 1, Quantity: d(4)}), now); err != nil {
		t.Fatal(err)
	}
	if end, _ := s.GetOrder(ctx, o.ID); end.Status != domain.OrderInvoiced {
		t.Fatalf("invoiced after closing: %s", end.Status)
	}
}

// TestPostgres_CancelRemainderReturnsAgreement checks that an order released
// from an agreement gives back only the undelivered quantity (BR-27.9), once,
// with agreement.returned.
func TestPostgres_CancelRemainderReturnsAgreement(t *testing.T) {
	s, pool, ctx := migrated(t)
	le := ids.New()
	now := time.Date(2027, 3, 10, 9, 0, 0, 0, time.UTC)
	a := agreementFixture(t, s, ctx, le, 100, nil, nil)
	r, _, err := s.ReleaseAgreement(ctx, a.ID, release(60, "r1"), now)
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.ConfirmOrder(ctx, r.Order.ID, r.Order.Version, "rep", now)
	if err != nil || !res.Confirmed {
		t.Fatalf("confirm: %v %+v", err, res.Evaluation)
	}
	deliver(t, s, ctx, r.Order.ID, "PS-1", 1, 45)
	cur, _ := s.GetOrder(ctx, r.Order.ID)
	if _, err := s.CancelRemainder(ctx, r.Order.ID, cur.Version, "short", "rep", now); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetAgreement(ctx, a.ID)
	if !got.Released.Equal(d(45)) || got.Status != domain.AgreementActive {
		t.Fatalf("agreement after: %+v", got)
	}
	var ret salesevents.AgreementLifecycleData
	if err := json.Unmarshal(outboxData(t, pool, a.ID, salesevents.AgreementReturned), &ret); err != nil || ret.ReturnedQty != "15" {
		t.Fatalf("returned event: %+v %v", ret, err)
	}
}

// TestPostgres_CancelRemainderHoldsDiscountAndLateDelivery checks the review
// rules of the close: a cut line's net is recomputed in decimals at its price
// and discount, an ACTIVE hold is left for its owner to release (it is not
// cleared by the order write duty), a delivery reported after the close is
// refused with SLS-0058 and the reason, and the delivered part can be invoiced.
func TestPostgres_CancelRemainderHoldsDiscountAndLateDelivery(t *testing.T) {
	s, pool, ctx := migrated(t)
	le := ids.New()
	in := orderOf(le, ids.New(), ids.New(), "2027-03-10", 7)
	in.Lines[0].UnitPrice, in.Lines[0].DiscPct = decimal.RequireFromString("12.34"), decimal.RequireFromString("12.5")
	in.Lines[0].LineNet = decimal.RequireFromString("75.5825")
	o := confirmed(t, s, ctx, in)
	held, err := s.ApplyHold(ctx, o.ID, o.Version, HoldRequest{HoldType: domain.HoldManual, ReasonCode: "CHECK"}, "manager")
	if err != nil {
		t.Fatal(err)
	}
	deliver(t, s, ctx, o.ID, "PS-1", 1, 3)
	cur, _ := s.GetOrder(ctx, o.ID)
	if cur.Version <= held.Version {
		t.Fatalf("delivery did not move the version: %d", cur.Version)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	res, err := s.CancelRemainder(ctx, o.ID, cur.Version, "", "rep", now)
	if err != nil {
		t.Fatal(err)
	}
	// 3 x 12.34 x 87.5% = 32.3925
	if l := res.Lines[0]; !l.Quantity.Equal(d(3)) || !l.LineNet.Equal(decimal.RequireFromString("32.3925")) || !res.NetTotal.Equal(l.LineNet) {
		t.Fatalf("cut line: qty %s net %s total %s", l.Quantity, l.LineNet, res.NetTotal)
	}
	if len(res.Holds) != 1 || res.Holds[0].Status != domain.HoldActive {
		t.Fatalf("the hold must stay active: %+v", res.Holds)
	}
	if n := countWhere(t, pool, `select count(*) from order_hold where order_id = $1 and status = 'CLEARED'`, o.ID); n != 0 {
		t.Fatalf("%d holds cleared", n)
	}
	_, _, err = s.RecordProgress(ctx, progressIn(o.ID, domain.ProgressDelivered, "PS-2", "k-late", "h-late", ProgressLineInput{LineNo: 1, Quantity: d(4)}), now)
	if problemCode(t, err) != CodeNotProgressable || !strings.Contains(err.Error(), "remainder was cancelled") {
		t.Fatalf("late delivery: %v", err)
	}
	if _, _, err := s.RecordProgress(ctx, progressIn(o.ID, domain.ProgressInvoiced, "INV-1", "k-inv", "h-inv", ProgressLineInput{LineNo: 1, Quantity: d(3)}), now); err != nil {
		t.Fatal(err)
	}
	var data salesevents.OrderRemainderCancelledData
	if err := json.Unmarshal(outboxData(t, pool, o.ID, salesevents.OrderRemainderCancelled), &data); err != nil || data.LegalEntityID != le {
		t.Fatalf("event legal entity: %+v %v", data, err)
	}
}
