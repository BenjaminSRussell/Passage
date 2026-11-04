package store_test

import (
	"context"
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

// A consolidation holds its lines and a share lock on their requisition, line by line in id order. When it holds an
// earlier line of the requisition and then waits for the line being split, while the split holds that line and waits
// for the requisition, Postgres aborts the split (it waited first): the caller gets a 409 to reload, not a 500.
func TestSplitRequisitionLine_DeadlockWithAConsolidationIsAConflict(t *testing.T) {
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
	const lock = `select l.id from purchase_requisition_line l join purchase_requisition r on r.tenant_id = l.tenant_id and r.id = l.requisition_id
		where l.tenant_id = $1 and l.id = $2 for update of l for share of r`
	tenantID := testkit.TenantID(31)
	if _, err := tx.Exec(context.Background(), lock, tenantID, first.ID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := s.SplitRequisitionLine(ctx, second.ID, req.Version, []decimal.Decimal{second.Qty.Div(dec("2"))}, 2, "", store.Idem{})
		done <- err
	}()
	time.Sleep(300 * time.Millisecond) // the split now holds its line and waits for the requisition
	if _, err := tx.Exec(context.Background(), lock, tenantID, second.ID); err != nil {
		t.Fatalf("the consolidation was aborted instead of the split: %v", err)
	}
	if err := <-done; problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("split in a deadlock = %v, want %s", err, store.CodeVersionConflict)
	}
}
