package store_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	purchasingevents "github.com/BenjaminSRussell/passage/services/purchasing/internal/events"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

// approvedBulk is an approved requisition with one line of 100 EA at 10.00 (1,000.00) and one of 1 EA at 5.00.
func approvedBulk(t *testing.T, s *store.Postgres) store.Requisition {
	t.Helper()
	ctx := principalContext(31)
	in := requisition(t, ids.New(), "5")
	in.Lines = append([]store.RequisitionLine{{LineNo: 1, Description: "Bolts", Category: "MRO", Qty: dec("100"), Unit: "EA",
		EstimatedUnitPrice: dec("10"), Amount: dec("1000"), RequestedDate: day(t, "2026-11-02"), PreferredVendorID: ids.New()}}, in.Lines...)
	in.Lines[1].LineNo = 2
	in.Total = dec("1005")
	r, err := s.CreateRequisition(ctx, in, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	r, err = s.SubmitRequisition(ctx, r.ID, 1, nil, "builtin-1", store.Idem{})
	if err != nil || r.Status != store.RequisitionApproved {
		t.Fatalf("approve = %v, %v", r.Status, err)
	}
	return r
}

func qtys(vals ...string) []decimal.Decimal {
	out := make([]decimal.Decimal, len(vals))
	for i, v := range vals {
		out[i] = dec(v)
	}
	return out
}

// FR-17.3: a split cuts new OPEN lines out of an OPEN line, the total and the line sums are unchanged, the
// parts go to different POs, and the requisition closes only when every part is ordered.
func TestSplitRequisitionLine_PartsGoToDifferentPOs(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(31)
	req := approvedBulk(t, s)
	orig := req.Lines[0]

	out, err := s.SplitRequisitionLine(ctx, orig.ID, req.Version, qtys("40", "35"), 2, "buyer-1", store.Idem{Key: "split-1", Hash: "h"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Version != req.Version+1 || !out.Total.Equal(dec("1005")) || len(out.Lines) != 4 || out.Status != store.RequisitionApproved {
		t.Fatalf("requisition after split = version %d total %s lines %d status %s", out.Version, out.Total, len(out.Lines), out.Status)
	}
	byNo := map[int]store.RequisitionLine{}
	sumQty, sumAmount := decimal.Zero, decimal.Zero
	for _, l := range out.Lines {
		byNo[l.LineNo] = l
		if l.LineNo != 2 { // line 2 is the other line, untouched
			sumQty, sumAmount = sumQty.Add(l.Qty), sumAmount.Add(l.Amount)
		}
	}
	if !sumQty.Equal(dec("100")) || !sumAmount.Equal(dec("1000")) {
		t.Fatalf("parts add to %s EA / %s, want 100 / 1000.00", sumQty, sumAmount)
	}
	a, b := byNo[3], byNo[4]
	if !byNo[1].Qty.Equal(dec("25")) || !byNo[1].Amount.Equal(dec("250")) || !a.Qty.Equal(dec("40")) || !a.Amount.Equal(dec("400")) ||
		!b.Qty.Equal(dec("35")) || !b.Amount.Equal(dec("350")) {
		t.Fatalf("lines = %+v", byNo)
	}
	for _, n := range []int{3, 4} {
		l := byNo[n]
		if l.SplitFromLineID != orig.ID || l.Status != "OPEN" || l.Description != "Bolts" || l.Category != "MRO" || l.Unit != "EA" ||
			l.PreferredVendorID != orig.PreferredVendorID || !l.EstimatedUnitPrice.Equal(dec("10")) || l.RequestedDate.String() != orig.RequestedDate.String() {
			t.Fatalf("new line %d = %+v; want the original's attributes and its origin", n, l)
		}
	}
	if byNo[1].SplitFromLineID != "" {
		t.Errorf("the original line claims to be split from %s", byNo[1].SplitFromLineID)
	}
	var evt map[string]any
	for _, row := range outbox(t, ctx, pool) {
		if row.Type == purchasingevents.RequisitionSplit {
			evt = row.Data
		}
	}
	if evt == nil || evt["split_by"] != "buyer-1" || len(evt["new_lines"].([]any)) != 2 ||
		evt["original"].(map[string]any)["qty"] != "25" || int(evt["version"].(float64)) != out.Version {
		t.Fatalf("requisition.split.v1 = %+v", evt)
	}

	// Replaying the key returns the requisition as it is, without a second split.
	again, err := s.SplitRequisitionLine(ctx, orig.ID, req.Version, qtys("40", "35"), 2, "buyer-1", store.Idem{Key: "split-1", Hash: "h"})
	if err != nil || len(again.Lines) != 4 || again.Version != out.Version {
		t.Fatalf("replay = %d lines, version %d, %v", len(again.Lines), again.Version, err)
	}

	// The parts are ordinary lines: two vendors, two POs; the requisition closes when the last part is ordered.
	po1, err := s.CreatePurchaseOrder(ctx, orderFor(t, ids.New(), byNo[1], a), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	po2, err := s.CreatePurchaseOrder(ctx, orderFor(t, ids.New(), b), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetRequisition(ctx, req.ID)
	open := 0
	for _, l := range got.Lines {
		if l.Status == "OPEN" {
			open++
		}
	}
	if open != 1 || got.Status != store.RequisitionApproved || got.Lines[3].PurchaseOrderID != po2.ID || got.Lines[0].PurchaseOrderID != po1.ID {
		t.Fatalf("after ordering the parts: %d lines open, status %s, %+v", open, got.Status, got.Lines)
	}
	// A consolidated line cannot be split.
	if _, err := s.SplitRequisitionLine(ctx, b.ID, got.Version, qtys("1"), 2, "", store.Idem{}); problemCode(err) != store.CodeRequisitionState {
		t.Fatalf("splitting an ordered line = %v", err)
	}
}

// FR-17.3 and slice 20's hold: a line a running RFQ holds cannot be split; once the RFQ is cancelled it can, and
// a part can then be sourced on its own RFQ while the rest is ordered.
func TestSplitRequisitionLine_RefusalsAndRFQHold(t *testing.T) {
	s := store.New(migrated(t))
	ctx := principalContext(31)
	req := approvedBulk(t, s)
	l := req.Lines[0]

	if _, err := s.SplitRequisitionLine(ctx, l.ID, req.Version+7, qtys("10"), 2, "", store.Idem{Key: "stale", Hash: "h"}); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("stale version = %v", err)
	}
	for name, parts := range map[string][]decimal.Decimal{"all of the line": qtys("100"), "more than the line": qtys("60", "50"), "zero": qtys("0"), "none": nil} {
		if _, err := s.SplitRequisitionLine(ctx, l.ID, req.Version, parts, 2, "", store.Idem{}); problemCode(err) != store.CodeRequisitionSplit {
			t.Errorf("%s = %v, want %s", name, err, store.CodeRequisitionSplit)
		}
	}
	if _, err := s.SplitRequisitionLine(ctx, ids.New(), req.Version, qtys("1"), 2, "", store.Idem{}); problemCode(err) != store.CodeRequisitionLineNotFound {
		t.Fatalf("unknown line = %v", err)
	}
	if got, _ := s.GetRequisition(ctx, req.ID); len(got.Lines) != 2 || got.Version != req.Version {
		t.Fatalf("a refused split changed the requisition: %d lines, version %d", len(got.Lines), got.Version)
	}

	held, err := s.CreateRFQ(ctx, rfqFor(l), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SplitRequisitionLine(ctx, l.ID, req.Version, qtys("10"), 2, "", store.Idem{}); problemCode(err) != store.CodeRequisitionOnRFQ {
		t.Fatalf("split of a line on a running RFQ = %v", err)
	}
	if _, err := s.ActOnRFQ(ctx, held.ID, held.Version, domain.RFQActionCancel, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	out, err := s.SplitRequisitionLine(ctx, l.ID, req.Version, qtys("10"), 2, "", store.Idem{})
	if err != nil || len(out.Lines) != 3 {
		t.Fatalf("split after the RFQ was cancelled = %v, %v", out.Lines, err)
	}
	// The part goes to an RFQ; the original line stays in the queue for a PO.
	if _, err := s.CreateRFQ(ctx, rfqFor(out.Lines[2]), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	queue, _ := s.ListOpenRequisitionLines(ctx, store.SourcingFilter{}, "", 0)
	if len(queue.Items) != 2 {
		t.Fatalf("queue = %d lines, want the original and the other line (the part is on the RFQ)", len(queue.Items))
	}
	for _, q := range queue.Items {
		if q.RequisitionVersion != out.Version {
			t.Errorf("queue line carries requisition version %d, want %d", q.RequisitionVersion, out.Version)
		}
	}
}

// A DRAFT requisition's lines are not for splitting: only an APPROVED requisition is in the buyer's hands.
func TestSplitRequisitionLine_DraftRequisition(t *testing.T) {
	s := store.New(migrated(t))
	ctx := principalContext(31)
	in := requisition(t, ids.New(), "5")
	in.Lines[0].Qty, in.Lines[0].Amount = dec("4"), dec("20")
	draft, err := s.CreateRequisition(ctx, in, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SplitRequisitionLine(ctx, draft.Lines[0].ID, draft.Version, qtys("1"), 2, "", store.Idem{}); problemCode(err) != store.CodeRequisitionState {
		t.Fatalf("split on a DRAFT requisition = %v", err)
	}
}

// A consolidation or an RFQ built from a line read before a split would order the whole old quantity while the parts
// cut from it stay OPEN to be ordered again: both are refused under the line lock, and the fresh read goes through.
func TestSplitRequisitionLine_StaleConsolidationAndRFQAreRefused(t *testing.T) {
	s := store.New(migrated(t))
	ctx := principalContext(31)
	req := approvedBulk(t, s)
	before := req.Lines[0]
	stalePO, staleRFQ := orderFor(t, ids.New(), before), rfqFor(before)

	out, err := s.SplitRequisitionLine(ctx, before.ID, req.Version, qtys("40"), 2, "", store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConsolidateRequisitionLines(ctx, []store.PurchaseOrder{stalePO}, []string{before.ID}, store.Idem{Key: "stale-c", Hash: "h"}); problemCode(err) != store.CodeRequisitionState {
		t.Fatalf("consolidating the line as it was before the split = %v, want %s", err, store.CodeRequisitionState)
	}
	if _, err := s.CreateRFQ(ctx, staleRFQ, store.Idem{}); problemCode(err) != store.CodeRequisitionState {
		t.Fatalf("an RFQ for the line as it was before the split = %v, want %s", err, store.CodeRequisitionState)
	}
	if got, _ := s.GetRequisition(ctx, req.ID); got.Lines[0].Status != "OPEN" || got.Version != out.Version {
		t.Fatalf("a refused consolidation changed the requisition: %+v", got.Lines[0])
	}
	if _, err := s.ConsolidateRequisitionLines(ctx, []store.PurchaseOrder{orderFor(t, ids.New(), out.Lines[0])}, []string{before.ID}, store.Idem{Key: "fresh-c", Hash: "h"}); err != nil {
		t.Fatalf("consolidating the line as it is now = %v", err)
	}
}

// Every writer of requisition lines locks the requisition first and its lines after, in id order. A writer that
// already holds the requisition and then takes its lines (the order below) therefore queues a consolidation and a
// split on the requisition instead of deadlocking with them line by line.
func TestRequisitionLineWriters_RequisitionFirstThenLines(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(31)
	req := approvedBulk(t, s)
	first, second := req.Lines[0], req.Lines[1]
	if second.ID < first.ID {
		first, second = second, first
	}
	admin, _ := adminPools.Load(pool)
	tx, err := admin.(*pgxpool.Pool).Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	tenantID := testkit.TenantID(31)
	if _, err := tx.Exec(context.Background(), `select 1 from purchase_requisition where tenant_id = $1 and id = $2 for update`, tenantID, req.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(context.Background(), `select 1 from purchase_requisition_line where tenant_id = $1 and id = $2 for update`, tenantID, first.ID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 2)
	go func() {
		_, err := s.SplitRequisitionLine(ctx, second.ID, req.Version, qtys("0.5"), 2, "", store.Idem{})
		done <- err
	}()
	go func() {
		_, err := s.ConsolidateRequisitionLines(ctx, []store.PurchaseOrder{orderFor(t, ids.New(), first, second)}, []string{first.ID, second.ID}, store.Idem{Key: "q-c", Hash: "h"})
		done <- err
	}()
	time.Sleep(300 * time.Millisecond) // both are queued on the requisition, which the open transaction holds
	select {
	case err := <-done:
		t.Fatalf("a writer did not wait for the requisition: %v", err)
	default:
	}
	if _, err := tx.Exec(context.Background(), `select 1 from purchase_requisition_line where tenant_id = $1 and id = $2 for update`, tenantID, second.ID); err != nil {
		t.Fatalf("taking the second line after the requisition was aborted: %v", err)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The writer that goes second finds the requisition moved on (a stale version or quantity, a line already
	// ordered); neither may be the deadlock backstop, which a split taking its line before the requisition hits.
	for i := 0; i < 2; i++ {
		err := <-done
		if err != nil && problemCode(err) != store.CodeRequisitionState && problemCode(err) != store.CodeVersionConflict {
			t.Fatalf("writer %d = %v", i, err)
		}
		if err != nil && strings.Contains(err.Error(), "at the same time") {
			t.Fatalf("writer %d deadlocked: %v", i, err)
		}
	}
}

// Cancelling a PO reopens its requisition lines and their CLOSED requisition. It takes the requisition first, like
// every other writer of requisition lines: a writer that holds the requisition and then takes the line (a split or
// a consolidation) makes the cancel wait instead of deadlocking with it.
func TestPurchaseOrderCancel_LocksTheRequisitionBeforeItsLine(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(31)
	req := approved(t, s, ids.New(), "10")
	line := req.Lines[0]
	po, err := s.CreatePurchaseOrder(ctx, orderFor(t, ids.New(), line), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetRequisition(ctx, req.ID); got.Status != "CLOSED" {
		t.Fatalf("requisition after its only line was ordered = %s", got.Status)
	}
	admin, _ := adminPools.Load(pool)
	tx, err := admin.(*pgxpool.Pool).Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	tenantID := testkit.TenantID(31)
	if _, err := tx.Exec(context.Background(), `select 1 from purchase_requisition where tenant_id = $1 and id = $2 for update`, tenantID, req.ID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := s.ActOnPurchaseOrder(ctx, po.ID, po.RowVersion, domain.POActionCancel, store.Idem{})
		done <- err
	}()
	time.Sleep(300 * time.Millisecond) // the cancel holds its PO and waits for the requisition
	select {
	case err := <-done:
		t.Fatalf("the cancel did not wait for the requisition: %v", err)
	default:
	}
	if _, err := tx.Exec(context.Background(), `select 1 from purchase_requisition_line where tenant_id = $1 and id = $2 for update`, tenantID, line.ID); err != nil {
		t.Fatalf("taking the line after the requisition was aborted: %v", err)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("cancel = %v", err)
	}
	got, err := s.GetRequisition(ctx, req.ID)
	if err != nil || got.Status != store.RequisitionApproved || got.Lines[0].Status != "OPEN" {
		t.Fatalf("requisition after cancel = %s, line %+v, %v", got.Status, got.Lines[0], err)
	}
}

// Splits, consolidations, direct POs and RFQs of lines of the same requisitions, all at once, never deadlock: the
// only refusals are the stale-read ones (a version or quantity that moved, a line already ordered).
func TestRequisitionLineWriters_ConcurrentWritersDoNotDeadlock(t *testing.T) {
	s := store.New(migrated(t))
	ctx := principalContext(31)
	for round := 0; round < 12; round++ {
		req := approvedBulk(t, s)
		a, b := req.Lines[0], req.Lines[1]
		var wg sync.WaitGroup
		errs := make(chan error, 8)
		run := func(name string, f func() error) {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := f(); err != nil {
					errs <- fmt.Errorf("%s: %w", name, err)
				}
			}()
		}
		run("split a", func() error {
			_, err := s.SplitRequisitionLine(ctx, a.ID, req.Version, qtys("10"), 2, "", store.Idem{})
			return err
		})
		run("split b", func() error {
			_, err := s.SplitRequisitionLine(ctx, b.ID, req.Version, qtys("0.5"), 2, "", store.Idem{})
			return err
		})
		run("consolidate a+b", func() error {
			_, err := s.ConsolidateRequisitionLines(ctx, []store.PurchaseOrder{orderFor(t, ids.New(), a, b)}, []string{a.ID, b.ID}, store.Idem{Key: fmt.Sprintf("cc-%d", round), Hash: "h"})
			return err
		})
		run("direct PO b+a", func() error {
			_, err := s.CreatePurchaseOrder(ctx, orderFor(t, ids.New(), b, a), store.Idem{})
			return err
		})
		run("rfq a+b", func() error {
			_, err := s.CreateRFQ(ctx, rfqFor(a, b), store.Idem{})
			return err
		})
		wg.Wait()
		close(errs)
		for err := range errs {
			if code := problemCode(err); code != store.CodeRequisitionState && code != store.CodeVersionConflict && code != store.CodeRequisitionOnRFQ && code != store.CodeRequisitionQty {
				t.Fatalf("round %d: %v", round, err)
			}
			if strings.Contains(err.Error(), "at the same time") {
				t.Fatalf("round %d: deadlock: %v", round, err)
			}
		}
	}
}

// A direct PO that names a requisition line orders that line's quantity in its unit: ordering less would drop the
// rest of the need, more would over-order it, and another unit cannot be compared (PRC-REQ-008); the line is
// untouched by the refusal, and the whole quantity (at any decimal scale) or a split part goes through.
func TestDirectPO_MustOrderTheRequisitionLineQuantity(t *testing.T) {
	s := store.New(migrated(t))
	ctx := principalContext(31)
	req := approvedBulk(t, s)
	line := req.Lines[0]
	for _, c := range []struct{ qty, unit string }{{"99", "EA"}, {"101", "EA"}, {"100", "BOX"}} {
		po := orderFor(t, ids.New(), line)
		po.Lines[0].Qty, po.Lines[0].Unit = dec(c.qty), c.unit
		if _, err := s.CreatePurchaseOrder(ctx, po, store.Idem{}); problemCode(err) != store.CodeRequisitionQty {
			t.Fatalf("a PO line of %s %s against a requisition line of 100 EA = %v, want %s", c.qty, c.unit, err, store.CodeRequisitionQty)
		}
	}
	if got, _ := s.GetRequisition(ctx, req.ID); got.Lines[0].Status != "OPEN" {
		t.Fatalf("a refused PO consumed the line: %+v", got.Lines[0])
	}
	out, err := s.SplitRequisitionLine(ctx, line.ID, req.Version, qtys("40"), 2, "", store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	part := out.Lines[len(out.Lines)-1]
	if _, err := s.CreatePurchaseOrder(ctx, orderFor(t, ids.New(), part), store.Idem{}); err != nil {
		t.Fatalf("a PO for the split part's whole quantity = %v", err)
	}
	rest := orderFor(t, ids.New(), out.Lines[0])
	rest.Lines[0].Qty = dec("60.000000")
	if _, err := s.CreatePurchaseOrder(ctx, rest, store.Idem{}); err != nil {
		t.Fatalf("a PO for the remainder's whole quantity = %v", err)
	}
}
