package store

import (
	"context"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	salesevents "github.com/BenjaminSRussell/passage/services/sales/internal/events"
)

// TestPostgres_RepriceAgreementOrders covers BR-27.9: a revised agreement price
// reaches the DRAFT orders already released only through the reprice run, at the
// agreement's ETag. The run recomputes line net and total with the order's
// discount, records an order version and order.changed, leaves a confirmed or
// cancelled order alone (the confirmed one is reported), keeps the commitment
// drawn, adds a REPRICED history entry with agreement.repriced, and a second run
// finds nothing to do and keeps the version.
func TestPostgres_RepriceAgreementOrders(t *testing.T) {
	s, pool, ctx := migrated(t)
	le := ids.New()
	now := time.Date(2027, 3, 10, 9, 0, 0, 0, time.UTC)
	a := agreementFixture(t, s, ctx, le, 1000, nil, nil)
	r1, _, err := s.ReleaseAgreement(ctx, a.ID, release(100, "r1"), now)
	if err != nil {
		t.Fatal(err)
	}
	r2, _, err := s.ReleaseAgreement(ctx, a.ID, release(50, "r2"), now)
	if err != nil {
		t.Fatal(err)
	}
	r3, _, err := s.ReleaseAgreement(ctx, a.ID, release(20, "r3"), now)
	if err != nil {
		t.Fatal(err)
	}
	r4, _, err := s.ReleaseAgreement(ctx, a.ID, release(10, "r4"), now)
	if err != nil {
		t.Fatal(err)
	}
	// r1 takes a 10% discount by hand; r3 is cancelled; r2 stays DRAFT; r4 is forced to CONFIRMED
	disc := d(10)
	if _, err := s.ChangeOrder(ctx, r1.Order.ID, r1.Order.Version, ChangeRequest{Reason: "deal", Lines: []LineChange{{LineNo: 1, DiscPct: &disc}}}, "rep", now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CancelOrder(ctx, r3.Order.ID, r3.Order.Version, "no", "rep", now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `update sales_order set status = 'CONFIRMED' where id = $1`, r4.Order.ID); err != nil {
		t.Fatal(err)
	}

	a, _ = s.GetAgreement(ctx, a.ID)
	// nothing to do while the agreement price is the release price
	idle, err := s.RepriceAgreementOrders(ctx, a.ID, a.Version, "", "ka", now)
	if err != nil || len(idle.Repriced) != 0 || idle.Agreement.Version != a.Version {
		t.Fatalf("idle run: %+v %v", idle, err)
	}

	price := d(15)
	rev, err := s.ReviseAgreement(ctx, a.ID, a.Version, AgreementRevision{UnitPrice: &price}, "ka", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RepriceAgreementOrders(ctx, a.ID, rev.Version-1, "", "ka", now); problemCode(t, err) != CodeVersionConflict {
		t.Fatalf("stale: %v", err)
	}
	stranger := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: ids.New(), UserID: "x", Duties: testkit.ScopeDuties("x")})
	if _, err := s.RepriceAgreementOrders(stranger, a.ID, rev.Version, "", "x", now); problemCode(t, err) != CodeAgreementNotFound {
		t.Fatalf("other legal entity: %v", err)
	}
	res, err := s.RepriceAgreementOrders(ctx, a.ID, rev.Version, "price list 2027", "ka", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Repriced) != 2 || res.More || res.Agreement.Version != rev.Version+1 {
		t.Fatalf("repriced %+v", res)
	}
	if len(res.Skipped) != 1 || res.Skipped[0].OrderID != r4.Order.ID {
		t.Fatalf("skipped %+v", res.Skipped)
	}
	byID := map[string]RepricedOrder{}
	for _, r := range res.Repriced {
		byID[r.OrderID] = r
	}
	// 100 x 15 less 10% = 1350; 50 x 15 = 750
	if !byID[r1.Order.ID].NetTotal.Equal(d(1350)) || !byID[r2.Order.ID].NetTotal.Equal(d(750)) || !byID[r1.Order.ID].OldPrice.Equal(d(12)) {
		t.Fatalf("totals %+v", byID)
	}
	o1, _ := s.GetOrder(ctx, r1.Order.ID)
	if !o1.Lines[0].UnitPrice.Equal(d(15)) || !o1.Lines[0].DiscPct.Equal(d(10)) || !o1.NetTotal.Equal(d(1350)) || o1.Version != 3 {
		t.Fatalf("order 1 %+v", o1)
	}
	o3, _ := s.GetOrder(ctx, r3.Order.ID)
	o4, _ := s.GetOrder(ctx, r4.Order.ID)
	if !o3.Lines[0].UnitPrice.Equal(d(12)) || !o4.Lines[0].UnitPrice.Equal(d(12)) {
		t.Fatalf("cancelled or confirmed order repriced: %+v %+v", o3.Lines, o4.Lines)
	}
	vs, _ := s.ListOrderVersions(ctx, r2.Order.ID)
	if len(vs) == 0 || vs[0].Diff[0].Field != "unitPrice" || vs[0].Diff[0].New != "15" {
		t.Fatalf("order history %+v", vs)
	}
	cur, _ := s.GetAgreement(ctx, a.ID)
	if !cur.Released.Equal(d(160)) {
		t.Fatalf("commitment moved: %s", cur.Released)
	}
	hist, _ := s.ListAgreementVersions(ctx, a.ID)
	if len(hist) == 0 || hist[0].Kind != "REPRICED" || hist[0].Reason != "price list 2027" || len(hist[0].Diff) != 2 {
		t.Fatalf("agreement history %+v", hist)
	}
	got := outboxTypes(t, pool, a.ID)
	if got[len(got)-1] != salesevents.AgreementRepriced {
		t.Fatalf("outbox %v", got)
	}
	if got := outboxTypes(t, pool, r2.Order.ID); got[len(got)-1] != salesevents.OrderChanged {
		t.Fatalf("order outbox %v", got)
	}
	again, err := s.RepriceAgreementOrders(ctx, a.ID, res.Agreement.Version, "", "ka", now)
	if err != nil || len(again.Repriced) != 0 || again.Agreement.Version != res.Agreement.Version {
		t.Fatalf("second run: %+v %v", again, err)
	}

	closed, err := s.CloseAgreement(ctx, a.ID, again.Agreement.Version, "done", "ka", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RepriceAgreementOrders(ctx, a.ID, closed.Version, "", "ka", now); problemCode(t, err) != CodeAgreementClosed {
		t.Fatalf("closed: %v", err)
	}
}

// TestPostgres_RepriceAgreementOrdersPages covers the per-run limit: each run
// takes only orders that still need the price, so settled, cancelled and
// confirmed orders never fill a run, every repeat at the new ETag moves on, and
// the run ends with every draft repriced exactly once.
func TestPostgres_RepriceAgreementOrdersPages(t *testing.T) {
	s, pool, ctx := migrated(t)
	now := time.Date(2027, 3, 10, 9, 0, 0, 0, time.UTC)
	a := agreementFixture(t, s, ctx, ids.New(), 1000, nil, nil)
	var drafts []string
	for i, key := range []string{"p1", "p2", "p3", "p4", "p5"} {
		r, _, err := s.ReleaseAgreement(ctx, a.ID, release(10, key+ids.New()), now)
		if err != nil {
			t.Fatal(err)
		}
		switch i {
		case 0:
			if _, err := s.CancelOrder(ctx, r.Order.ID, r.Order.Version, "no", "rep", now); err != nil {
				t.Fatal(err)
			}
		case 1:
			if _, err := pool.Exec(ctx, `update sales_order set status = 'CONFIRMED' where id = $1`, r.Order.ID); err != nil {
				t.Fatal(err)
			}
		default:
			drafts = append(drafts, r.Order.ID)
		}
	}
	a, _ = s.GetAgreement(ctx, a.ID)
	price := d(14)
	rev, err := s.ReviseAgreement(ctx, a.ID, a.Version, AgreementRevision{UnitPrice: &price}, "ka", now)
	if err != nil {
		t.Fatal(err)
	}
	version, seen, runs := rev.Version, map[string]int{}, 0
	for more := true; more; runs++ {
		if runs == len(drafts)+1 {
			t.Fatalf("the run does not end: %v", seen)
		}
		res, err := s.repriceAgreementOrders(ctx, a.ID, version, "", "ka", now, 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Repriced) != 1 || len(res.Skipped) != 1 || res.Skipped[0].OrderNumber == "" {
			t.Fatalf("run %d: %+v", runs, res)
		}
		if _, err := s.repriceAgreementOrders(ctx, a.ID, version, "", "ka", now, 1); problemCode(t, err) != CodeVersionConflict {
			t.Fatalf("the old ETag after a run: %v", err)
		}
		seen[res.Repriced[0].OrderID]++
		version, more = res.Agreement.Version, res.More
	}
	if runs != len(drafts) || len(seen) != len(drafts) {
		t.Fatalf("runs %d repriced %v", runs, seen)
	}
	for _, id := range drafts {
		if o, _ := s.GetOrder(ctx, id); seen[id] != 1 || !o.Lines[0].UnitPrice.Equal(d(14)) {
			t.Fatalf("draft %s repriced %d times, price %s", id, seen[id], o.Lines[0].UnitPrice)
		}
	}
}
