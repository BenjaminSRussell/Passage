package store_test

import (
	"context"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
)

// The overlap quantity (BR-23.5) lets the next operation start once that share
// of the first one is through; a Gantt move keeps the rule, a patch can clear
// it, and the database refuses an overlap above the quantity.
func TestOperationOverlap_RunMovePatchAndChecks(t *testing.T) {
	s, _, admin := setup(t)
	ctx := tenantCtx(1)
	m1, m2 := mustResource(t, ctx, s, "M1", true), mustResource(t, ctx, s, "M2", true)
	order := ids.New()
	q, o := "100", "25"
	op1, _, err := s.CreateOperation(ctx, store.Operation{OrderID: order, OperNumber: 10, Minutes: 240, Priority: 100, ResourceIDs: []string{m1.ID}, Quantity: &q, OverlapQuantity: &o}, store.Idem{})
	if err != nil || op1.Quantity == nil || *op1.Quantity != "100.0000" || op1.OverlapQuantity == nil || *op1.OverlapQuantity != "25.0000" {
		t.Fatalf("op1 = %+v err=%v, want quantity 100 and overlap 25", op1, err)
	}
	op2, _, err := s.CreateOperation(ctx, store.Operation{OrderID: order, OperNumber: 20, Minutes: 240, Priority: 100, ResourceIDs: []string{m2.ID}}, store.Idem{})
	if err != nil || op2.Quantity != nil || op2.OverlapQuantity != nil {
		t.Fatalf("op2 = %+v err=%v, want no quantities", op2, err)
	}
	if _, _, err := s.RunSchedule(ctx, siteRun(at(0, 6, 0)), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	bars := barsOf(t, ctx, s)
	// o1 08:00-12:00, 25% of 240 minutes = 60: o2 starts at 09:00 and ends 13:00.
	if !bars[op1.ID].End.Equal(at(0, 12, 0)) || !bars[op2.ID].Start.Equal(at(0, 9, 0)) || !bars[op2.ID].End.Equal(at(0, 13, 0)) {
		t.Fatalf("o1 %v-%v o2 %v-%v, want o1 to 12:00 and o2 09:00-13:00", bars[op1.ID].Start, bars[op1.ID].End, bars[op2.ID].Start, bars[op2.ID].End)
	}

	// A move of o1 an hour later pushes o2 by an hour, not to o1's end.
	out, _, err := s.MoveJob(ctx, store.MoveRequest{JobID: op1.ID, Start: at(0, 9, 0), HorizonDays: 60}, store.Idem{})
	if err != nil || out.Conflict != nil {
		t.Fatalf("move: %+v err=%v", out, err)
	}
	bars = barsOf(t, ctx, s)
	if !bars[op1.ID].Start.Equal(at(0, 9, 0)) || !bars[op2.ID].Start.Equal(at(0, 10, 0)) {
		t.Fatalf("after the move o1 starts %v, o2 starts %v; want 09:00 and 10:00", bars[op1.ID].Start, bars[op2.ID].Start)
	}

	// An overlap above the quantity is refused, and so is one without quantity.
	big := decimal.NewFromInt(150)
	if _, _, err := s.UpdateOperation(ctx, op1.ID, 1, store.OperationPatch{OverlapQuantity: &big}, idem("o1")); problemCode(err) != store.CodeOverlapQuantity {
		t.Fatalf("overlap above the quantity: %v, want SCHED-0142", err)
	}
	if _, _, err := s.UpdateOperation(ctx, op2.ID, 1, store.OperationPatch{OverlapQuantity: &big}, idem("o2")); problemCode(err) != store.CodeOverlapQuantity {
		t.Fatalf("overlap without a quantity: %v, want SCHED-0142", err)
	}
	small := decimal.NewFromInt(20)
	if _, _, err := s.UpdateOperation(ctx, op1.ID, 1, store.OperationPatch{Quantity: &small}, idem("o3")); problemCode(err) != store.CodeOverlapQuantity {
		t.Fatalf("quantity below the overlap: %v, want SCHED-0142", err)
	}

	// Clearing the overlap (zero) takes the jobs off the schedule and is published.
	zero := decimal.Zero
	upd, _, err := s.UpdateOperation(ctx, op1.ID, 1, store.OperationPatch{OverlapQuantity: &zero}, idem("o4"))
	if err != nil || !upd.JobsRemoved || upd.OverlapQuantity != nil || upd.Quantity == nil {
		t.Fatalf("clear = %+v err=%v, want jobs removed, no overlap, quantity kept", upd, err)
	}
	if got := outboxCount(t, admin, "dev.passage.scheduling.operation.updated.v1"); got != 1 {
		t.Fatalf("operation.updated events = %d, want 1", got)
	}
	if _, _, err := s.RunSchedule(ctx, siteRun(at(0, 6, 0)), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	bars = barsOf(t, ctx, s)
	if !bars[op2.ID].Start.Equal(bars[op1.ID].End) {
		t.Fatalf("without the overlap o2 starts %v, want o1's end %v", bars[op2.ID].Start, bars[op1.ID].End)
	}

	// The checks hold in the database too.
	if _, err := admin.Exec(context.Background(), `update scheduling_operation set overlap_quantity = 500 where id = $1`, op1.ID); err == nil {
		t.Fatal("an overlap above the quantity was stored, want the check constraint to refuse it")
	}
	if _, err := admin.Exec(context.Background(), `update scheduling_operation set quantity = 0 where id = $1`, op1.ID); err == nil {
		t.Fatal("a zero quantity was stored, want the check constraint to refuse it")
	}
}
