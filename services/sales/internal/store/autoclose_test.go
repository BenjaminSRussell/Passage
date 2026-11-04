package store

import (
	"context"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	salesevents "github.com/BenjaminSRussell/passage/services/sales/internal/events"
)

// invoicedAt takes a new order of qty units through delivery and invoice, the
// invoice recorded at at.
func invoicedAt(t *testing.T, s *Postgres, ctx context.Context, le string, at time.Time) Order {
	t.Helper()
	o := confirmed(t, s, ctx, orderOf(le, ids.New(), ids.New(), "2027-03-01", 2))
	for i, kind := range []string{domain.ProgressDelivered, domain.ProgressInvoiced} {
		in := progressIn(o.ID, kind, "REF-"+o.ID[:8], ids.New(), ids.New(), ProgressLineInput{LineNo: 1, Quantity: d(2)})
		if _, _, err := s.RecordProgress(ctx, in, at.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.GetOrder(ctx, o.ID)
	if err != nil || got.Status != domain.OrderInvoiced {
		t.Fatalf("not invoiced: %v %+v", err, got)
	}
	return got
}

// TestPostgres_AutoCloseOrders covers the nightly order auto-close (PRD
// 5.27.16, BR-27.5): only an INVOICED order unchanged for the grace period and
// without an active hold is closed, oldest first, once; the close writes a
// version and order.closed; a closed order takes no further progress, change or
// cancel; a dry run closes nothing; the legal entity filter and the limit hold.
func TestPostgres_AutoCloseOrders(t *testing.T) {
	s, pool, ctx := migrated(t)
	le, other := ids.New(), ids.New()
	t0 := time.Date(2027, 3, 1, 9, 0, 0, 0, time.UTC)
	old := invoicedAt(t, s, ctx, le, t0)
	recent := invoicedAt(t, s, ctx, le, t0.Add(20*24*time.Hour))
	held := invoicedAt(t, s, ctx, le, t0)
	if _, err := pool.Exec(ctx, `insert into order_hold (tenant_id, id, legal_entity_id, order_id, hold_type, reason_code, status, order_amount, applied_by)
		select tenant_id, $2, legal_entity_id, id, 'MANUAL', 'R', 'ACTIVE', 1, 'x' from sales_order where id = $1`, held.ID, ids.New()); err != nil {
		t.Fatal(err)
	}
	foreign := invoicedAt(t, s, ctx, other, t0)
	shipped := confirmed(t, s, ctx, orderOf(le, ids.New(), ids.New(), "2027-03-01", 2))
	if _, _, err := s.RecordProgress(ctx, progressIn(shipped.ID, domain.ProgressDelivered, "PS", ids.New(), ids.New(), ProgressLineInput{LineNo: 1, Quantity: d(1)}), t0); err != nil {
		t.Fatal(err)
	}
	now := t0.Add(31 * 24 * time.Hour)
	f := AutoCloseFilter{LegalEntityID: le, GraceDays: 30, Limit: 50}

	// 10 days after the invoice nothing is old enough.
	if res, err := s.AutoCloseOrders(ctx, f, "job", t0.Add(10*24*time.Hour)); err != nil || res.Closed != 0 || len(res.Items) != 0 {
		t.Fatalf("too young: %v %+v", err, res)
	}
	dry := f
	dry.DryRun = true
	res, err := s.AutoCloseOrders(ctx, dry, "job", now)
	if err != nil || res.Closed != 0 || len(res.Items) != 1 || res.Items[0].OrderID != old.ID || res.Items[0].Result != "CANDIDATE" {
		t.Fatalf("dry run lists only the old order: %v %+v", err, res)
	}
	if got, _ := s.GetOrder(ctx, old.ID); got.Status != domain.OrderInvoiced {
		t.Fatalf("a dry run closes nothing: %s", got.Status)
	}

	res, err = s.AutoCloseOrders(ctx, f, "job", now)
	if err != nil || res.Closed != 1 || res.Skipped != 0 || res.More || res.Items[0].OrderID != old.ID || res.Items[0].Result != "CLOSED" || res.Items[0].Version != old.Version+1 {
		t.Fatalf("close: %v %+v", err, res)
	}
	got, _ := s.GetOrder(ctx, old.ID)
	if got.Status != domain.OrderClosed || got.Version != old.Version+1 {
		t.Fatalf("closed order: %+v", got)
	}
	versions, err := s.ListOrderVersions(ctx, old.ID)
	if err != nil || len(versions) == 0 || versions[0].Version != got.Version || versions[0].Actor != "job" || len(versions[0].Diff) != 1 || versions[0].Diff[0].New != "CLOSED" {
		t.Fatalf("version: %v %+v", err, versions)
	}
	types := outboxTypes(t, pool, old.ID)
	if types[len(types)-1] != salesevents.OrderClosed {
		t.Fatalf("events: %v", types)
	}
	for _, id := range []string{recent.ID, held.ID, foreign.ID, shipped.ID} {
		if o, _ := s.GetOrder(ctx, id); o.Status == domain.OrderClosed {
			t.Fatalf("order %s must stay open: %s", id, o.Status)
		}
	}

	// A repeat finds nothing; a later run closes the recent one, and only
	// the caller's legal entity.
	if again, err := s.AutoCloseOrders(ctx, f, "job", now); err != nil || again.Closed != 0 || len(again.Items) != 0 {
		t.Fatalf("repeat: %v %+v", err, again)
	}
	later := t0.Add(60 * 24 * time.Hour)
	res, err = s.AutoCloseOrders(ctx, f, "job", later)
	if err != nil || res.Closed != 1 || res.Items[0].OrderID != recent.ID {
		t.Fatalf("recent order: %v %+v", err, res)
	}
	if o, _ := s.GetOrder(ctx, foreign.ID); o.Status != domain.OrderInvoiced {
		t.Fatalf("another legal entity is not touched: %s", o.Status)
	}
	all := AutoCloseFilter{GraceDays: 30, Limit: 50}
	if res, err = s.AutoCloseOrders(ctx, all, "job", later); err != nil || res.Closed != 1 || res.Items[0].OrderID != foreign.ID {
		t.Fatalf("every legal entity: %v %+v", err, res)
	}

	// A closed order is final.
	now2 := later.Add(time.Hour)
	if _, _, err := s.RecordProgress(ctx, progressIn(old.ID, domain.ProgressInvoiced, "INV-X", ids.New(), ids.New(), ProgressLineInput{LineNo: 1, Quantity: d(1)}), now2); problemCode(t, err) != CodeNotProgressable {
		t.Fatalf("progress on a closed order: %v", err)
	}
	if _, err := s.CancelOrderAs(ctx, old.ID, got.Version, "x", "rep", true, now2); problemCode(t, err) != CodeNotCancellable && problemCode(t, err) != CodeNotChangeable {
		t.Fatalf("cancel a closed order: %v", err)
	}

	// The held order closes once its hold is released; the limit pages the run.
	if _, err := pool.Exec(ctx, `update order_hold set status = 'RELEASED' where order_id = $1`, held.ID); err != nil {
		t.Fatal(err)
	}
	third := invoicedAt(t, s, ctx, le, t0)
	res, err = s.AutoCloseOrders(ctx, AutoCloseFilter{LegalEntityID: le, GraceDays: 30, Limit: 1}, "job", later)
	if err != nil || res.Closed != 1 || !res.More {
		t.Fatalf("limit: %v %+v", err, res)
	}
	res, err = s.AutoCloseOrders(ctx, AutoCloseFilter{LegalEntityID: le, GraceDays: 30, Limit: 1}, "job", later)
	if err != nil || res.Closed != 1 || res.More {
		t.Fatalf("second page: %v %+v", err, res)
	}
	for _, id := range []string{held.ID, third.ID} {
		if o, _ := s.GetOrder(ctx, id); o.Status != domain.OrderClosed {
			t.Fatalf("order %s: %s", id, o.Status)
		}
	}
}
