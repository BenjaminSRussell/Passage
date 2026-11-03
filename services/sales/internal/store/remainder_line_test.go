package store

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	salesevents "github.com/BenjaminSRussell/passage/services/sales/internal/events"
)

// TestPostgres_PartlyShippedExposureIsUndelivered covers BR-27.3: a partly
// delivered order counts only the undelivered part of its lines as open
// exposure, on the profile read and on the profile list, and nothing once a
// line is delivered in full or cancelled.
func TestPostgres_PartlyShippedExposureIsUndelivered(t *testing.T) {
	s, _, ctx := migrated(t)
	le, customer, a, b := ids.New(), ids.New(), ids.New(), ids.New()
	if _, err := s.PutCreditProfile(ctx, CreditProfile{LegalEntityID: le, CustomerID: customer, CreditProfile: domain.CreditProfile{
		Limit: d(100000), Currency: "USD", LimitType: domain.LimitBalanceAll, OldestItemDaysMax: 30, MaxDunningLevel: 3}}, 0); err != nil {
		t.Fatal(err)
	}
	in := orderOf(le, customer, a, "2027-03-10", 10)
	line2 := in.Lines[0]
	line2.LineNo, line2.ProductID = 2, b
	in.Lines = append(in.Lines, line2)
	o := confirmed(t, s, ctx, in)
	open := func(want string) {
		t.Helper()
		w := decimal.RequireFromString(want)
		if p, err := s.GetCreditProfile(ctx, le, customer); err != nil || !p.Exposure.OpenOrders.Equal(w) {
			t.Fatalf("open orders want %s: %+v %v", want, p.Exposure, err)
		}
		if l, err := s.ListCreditProfiles(ctx, le, 50); err != nil || len(l) != 1 || !l[0].Exposure.OpenOrders.Equal(w) {
			t.Fatalf("list want %s: %+v %v", want, l, err)
		}
	}
	open("200")
	deliver(t, s, ctx, o.ID, "PS-1", 1, 4)
	open("160") // 200 less 4 x 10 delivered
	deliver(t, s, ctx, o.ID, "PS-2", 1, 6)
	open("100") // line 1 delivered in full
	deliver(t, s, ctx, o.ID, "PS-3", 2, 10)
	open("0") // SHIPPED

	// A discounted line counts the line net less the net of the delivered
	// quantity at the same price and discount: 7 x 12.34 less 12.5% is 75.5825,
	// 3 delivered are 32.3925, so 43.19 is open.
	disc := orderOf(le, customer, ids.New(), "2027-03-10", 7)
	disc.Lines[0].UnitPrice, disc.Lines[0].DiscPct = decimal.RequireFromString("12.34"), decimal.RequireFromString("12.5")
	disc.Lines[0].LineNet = decimal.RequireFromString("75.5825")
	o2 := confirmed(t, s, ctx, disc)
	open("75.5825")
	deliver(t, s, ctx, o2.ID, "PS-4", 1, 3)
	open("43.19")
	// Closing the line's remainder leaves nothing open.
	cur, _ := s.GetOrder(ctx, o2.ID)
	if _, err := s.CancelLineRemainder(ctx, o2.ID, 1, cur.Version, "", "rep", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	open("0")
}

// TestPostgres_CancelLineRemainder covers BR-27.5 for one line: the line
// delivered in part is closed at the delivered quantity, the other line is not
// touched, the order stays partly shipped, and a line with nothing left to
// cancel or an unknown line is refused.
func TestPostgres_CancelLineRemainder(t *testing.T) {
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

	if _, err := s.CancelLineRemainder(ctx, o.ID, 1, o.Version, "early", "rep", now); problemCode(t, err) != CodeNotCancellable {
		t.Fatalf("a confirmed order has no remainder: %v", err)
	}
	deliver(t, s, ctx, o.ID, "PS-1", 1, 4)
	got, _ := s.GetOrder(ctx, o.ID)
	if _, err := s.CancelLineRemainder(ctx, o.ID, 9, got.Version, "x", "rep", now); problemCode(t, err) != CodeLineNotFound {
		t.Fatalf("unknown line: %v", err)
	}
	// Line 0 is not "every line": it is refused and nothing is closed.
	if _, err := s.CancelLineRemainder(ctx, o.ID, 0, got.Version, "x", "rep", now); problemCode(t, err) != CodeLineNotFound {
		t.Fatalf("line 0: %v", err)
	}
	if _, err := s.CancelLineRemainder(ctx, o.ID, 1, got.Version-1, "stale", "rep", now); problemCode(t, err) != CodeVersionConflict {
		t.Fatalf("stale version: %v", err)
	}
	res, err := s.CancelLineRemainder(ctx, o.ID, 1, got.Version, "customer takes 4 of line 1", "rep", now)
	if err != nil {
		t.Fatal(err)
	}
	l1, l2 := res.Lines[0], res.Lines[1]
	if res.Status != domain.OrderPartiallyShipped || res.Version != got.Version+1 || !res.NetTotal.Equal(d(140)) ||
		!l1.Quantity.Equal(d(4)) || !l1.LineNet.Equal(d(40)) || l1.LineStatus != domain.LineDelivered ||
		!l2.Quantity.Equal(d(10)) || l2.LineStatus == domain.LineCancelled || l2.LineStatus == domain.LineDelivered {
		t.Fatalf("after: %s v%d total %s lines %+v %+v", res.Status, res.Version, res.NetTotal, l1, l2)
	}
	qa, _ := s.GetAllocationQuota(ctx, q.ID)
	if !qa.ConsumedQty.Equal(d(4)) {
		t.Fatalf("quota of line 1 keeps what was delivered: %s", qa.ConsumedQty)
	}
	var data salesevents.OrderRemainderCancelledData
	if err := json.Unmarshal(outboxData(t, pool, o.ID, salesevents.OrderRemainderCancelled), &data); err != nil {
		t.Fatal(err)
	}
	if data.Status != domain.OrderPartiallyShipped || data.Version != res.Version ||
		len(data.Lines) != 1 || data.Lines[0].LineNo != 1 || data.Lines[0].CancelledQty != "6" || data.Lines[0].KeptQty != "4" {
		t.Fatalf("event: %+v", data)
	}
	if _, err := s.CancelLineRemainder(ctx, o.ID, 1, res.Version, "again", "rep", now); problemCode(t, err) != CodeNotCancellable {
		t.Fatalf("a line closed already: %v", err)
	}
	// Closing line 2 (nothing delivered) cancels it and ends the order SHIPPED.
	end, err := s.CancelLineRemainder(ctx, o.ID, 2, res.Version, "", "rep", now)
	if err != nil || end.Status != domain.OrderShipped || !end.NetTotal.Equal(d(40)) || end.Lines[1].LineStatus != domain.LineCancelled {
		t.Fatalf("closing line 2: %v %+v", err, end)
	}
}
