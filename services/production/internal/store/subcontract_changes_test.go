package store

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
	prodevents "github.com/BenjaminSRussell/passage/services/production/internal/events"
)

func poChanges(t *testing.T, pool *pgxpool.Pool, orderID string) []prodevents.SubcontractPOChangedData {
	t.Helper()
	return poEventData[prodevents.SubcontractPOChangedData](t, pool, orderID, prodevents.SubcontractPOChanged)
}

func poCancels(t *testing.T, pool *pgxpool.Pool, orderID string) []prodevents.SubcontractPOCancelledData {
	t.Helper()
	return poEventData[prodevents.SubcontractPOCancelledData](t, pool, orderID, prodevents.SubcontractPOCancelled)
}

func poEventData[T any](t *testing.T, pool *pgxpool.Pool, orderID, typ string) []T {
	t.Helper()
	rows, err := pool.Query(context.Background(), `select payload from outbox where aggregate_id = $1 and type = $2 order by created_at, id`, orderID, typ)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []T
	for rows.Next() {
		var raw json.RawMessage
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var evt struct {
			Data T `json:"data"`
		}
		if err := json.Unmarshal(raw, &evt); err != nil {
			t.Fatal(err)
		}
		out = append(out, evt.Data)
	}
	return out
}

func releasedSubcontractOrder(t *testing.T, s *Postgres, ctx context.Context, key, legalEntity, vendor string) (Order, *stepper) {
	t.Helper()
	o, _, err := s.CreateOrder(ctx, subcontractOrder(t, key, legalEntity, vendor))
	if err != nil {
		t.Fatal(err)
	}
	st := &stepper{t: t, s: s, ctx: ctx, orderID: o.ID, version: o.Version}
	st.must(domain.CmdEstimate, nil)
	st.must(domain.CmdSchedule, nil)
	return st.must(domain.CmdRelease, nil), st
}

// FR-21.10: a quantity PATCH and a price edit of a released order tell purchasing the
// figures it had and the ones now in force; an edit that changes nothing a vendor
// buys, and the first request itself, send nothing.
func TestSubcontractChanges_QuantityAndPriceChangeThePO(t *testing.T) {
	pool, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(74))
	vendor := ids.New()
	released, st := releasedSubcontractOrder(t, s, ctx, "chg-qty", ids.New(), vendor)
	if got := poChanges(t, pool, released.ID); len(got) != 0 {
		t.Fatalf("a release is not a change: %+v", got)
	}

	// The quantity is editable below Released; the reset keeps the PO, so the new
	// quantity reaches purchasing as a change.
	st.must(domain.CmdResetStatus, func(tr *Transition) { tr.ResetTo = domain.Estimated })
	qty := d("80")
	if _, err := s.UpdateOrder(ctx, OrderUpdate{OrderID: released.ID, IfMatch: st.version, Quantity: &qty, Actor: "planner-1"}); err != nil {
		t.Fatalf("patch: %v", err)
	}
	got := poChanges(t, pool, released.ID)
	if len(got) != 1 {
		t.Fatalf("want one change, got %+v", got)
	}
	c := got[0]
	if c.OperNumber != 20 || c.ResourceID != vendor || !d(c.PreviousQty).Equal(d("100")) || !d(c.Qty).Equal(d("80")) ||
		c.PreviousAmount != "125.0000" || c.Amount != "100.0000" || !d(c.UnitPrice).Equal(d("1.25")) || c.Currency != "USD" ||
		c.DueDate == nil || c.Actor != "planner-1" || c.OrderNumber != released.OrderNumber {
		t.Fatalf("change after the quantity patch: %+v", c)
	}

	// A price edit changes it again, from the figures now on the PO.
	price := d("2")
	if _, err := s.EditOrder(ctx, OrderEdit{OrderID: released.ID, IfMatch: st.version + 1, ReleaseDuty: true, Actor: "engineer-1",
		Edits: domain.OrderEdits{Operations: []domain.OperationEdit{{Op: domain.EditChange, OperNumber: 20, SubcontractPrice: &price}}}}); err != nil {
		t.Fatalf("edit price: %v", err)
	}
	got = poChanges(t, pool, released.ID)
	if len(got) != 2 || !d(got[1].PreviousUnitPrice).Equal(d("1.25")) || !d(got[1].UnitPrice).Equal(d("2")) || got[1].Amount != "160.0000" ||
		got[1].PreviousAmount != "100.0000" {
		t.Fatalf("change after the price edit: %+v", got)
	}

	// An edit of the in-house operation changes nothing the vendor buys.
	hours := d("0.7")
	if _, err := s.EditOrder(ctx, OrderEdit{OrderID: released.ID, IfMatch: st.version + 2, ReleaseDuty: true, Actor: "engineer-1",
		Edits: domain.OrderEdits{Operations: []domain.OperationEdit{{Op: domain.EditChange, OperNumber: 10, RunHPer: &hours}}}}); err != nil {
		t.Fatalf("edit hours: %v", err)
	}
	if got := poChanges(t, pool, released.ID); len(got) != 2 {
		t.Fatalf("an in-house edit changed the PO: %+v", got)
	}
	if got := poCancels(t, pool, released.ID); len(got) != 0 {
		t.Fatalf("nothing was cancelled: %+v", got)
	}
}

// Turning subcontracting off, removing the operation or moving it to another vendor
// withdraws the request, and the operation can be requested afresh afterwards.
func TestSubcontractChanges_EditsWithdrawTheRequest(t *testing.T) {
	pool, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(75))
	vendor, vendor2 := ids.New(), ids.New()
	released, st := releasedSubcontractOrder(t, s, ctx, "chg-edit", ids.New(), vendor)
	version := st.version

	off := false
	o, err := s.EditOrder(ctx, OrderEdit{OrderID: released.ID, IfMatch: version, ReleaseDuty: true, Actor: "engineer-1",
		Edits: domain.OrderEdits{Operations: []domain.OperationEdit{{Op: domain.EditChange, OperNumber: 20, Subcontract: &off}}}})
	if err != nil {
		t.Fatalf("turn off: %v", err)
	}
	cancels := poCancels(t, pool, released.ID)
	if len(cancels) != 1 || cancels[0].Reason != domain.CancelSubcontractOff || cancels[0].OperNumber != 20 || cancels[0].ResourceID != vendor ||
		cancels[0].Amount != "125.0000" || !d(cancels[0].Qty).Equal(d("100")) {
		t.Fatalf("cancel after turning off: %+v", cancels)
	}
	if op := operation(o, 20); op.PORequestedAt != nil || op.POCancelledAt == nil {
		t.Fatalf("markers after the cancel: %+v", op)
	}

	// Turning it back on (the order is Released) requests afresh and clears the cancel.
	on, price := true, d("1.5")
	o, err = s.EditOrder(ctx, OrderEdit{OrderID: released.ID, IfMatch: o.Version, ReleaseDuty: true, Actor: "engineer-1",
		Edits: domain.OrderEdits{Operations: []domain.OperationEdit{{Op: domain.EditChange, OperNumber: 20, Subcontract: &on, SubcontractPrice: &price}}}})
	if err != nil {
		t.Fatalf("turn on: %v", err)
	}
	if reqs := poRequests(t, pool, released.ID); len(reqs) != 2 || reqs[1].UnitPrice != "1.5" {
		t.Fatalf("requests after turning on: %+v", reqs)
	}
	if op := operation(o, 20); op.PORequestedAt == nil || op.POCancelledAt != nil {
		t.Fatalf("markers after the new request: %+v", op)
	}

	// Another vendor resource: the old PO is cancelled and a new one requested.
	o, err = s.EditOrder(ctx, OrderEdit{OrderID: released.ID, IfMatch: o.Version, ReleaseDuty: true, Actor: "engineer-1",
		Edits: domain.OrderEdits{Operations: []domain.OperationEdit{{Op: domain.EditChange, OperNumber: 20, ResourceID: &vendor2}}}})
	if err != nil {
		t.Fatalf("change vendor: %v", err)
	}
	cancels = poCancels(t, pool, released.ID)
	reqs := poRequests(t, pool, released.ID)
	if len(cancels) != 2 || cancels[1].Reason != domain.CancelVendorChanged || cancels[1].ResourceID != vendor ||
		len(reqs) != 3 || reqs[2].ResourceID != vendor2 {
		t.Fatalf("vendor change: cancels %+v requests %+v", cancels, reqs)
	}

	// Removing the operation cancels the request for good.
	if _, err = s.EditOrder(ctx, OrderEdit{OrderID: released.ID, IfMatch: o.Version, ReleaseDuty: true, Actor: "engineer-1",
		Edits: domain.OrderEdits{Operations: []domain.OperationEdit{{Op: domain.EditRemove, OperNumber: 20}}}}); err != nil {
		t.Fatalf("remove: %v", err)
	}
	cancels = poCancels(t, pool, released.ID)
	if len(cancels) != 3 || cancels[2].Reason != domain.CancelOpRemoved || cancels[2].ResourceID != vendor2 {
		t.Fatalf("remove: %+v", cancels)
	}
}

// Cancelling the order withdraws its requests in the same transaction as the status
// change; an order that was never released has nothing to withdraw, and a reset below
// Released keeps the PO (it is requested once).
func TestSubcontractChanges_CancelWithdrawsTheRequests(t *testing.T) {
	pool, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(76))
	le := ids.New()
	released, st := releasedSubcontractOrder(t, s, ctx, "chg-cancel", le, ids.New())
	st.must(domain.CmdResetStatus, nil) // Scheduled: the PO stays
	if got := poCancels(t, pool, released.ID); len(got) != 0 {
		t.Fatalf("a reset withdrew the PO: %+v", got)
	}
	cancelled := st.must(domain.CmdCancel, nil)
	got := poCancels(t, pool, released.ID)
	if len(got) != 1 || got[0].Reason != domain.CancelOrderCancelled || got[0].Amount != "125.0000" {
		t.Fatalf("cancel: %+v", got)
	}
	if op := operation(cancelled, 20); op.PORequestedAt != nil || op.POCancelledAt == nil {
		t.Fatalf("markers: %+v", op)
	}
	types := outboxTypes(t, pool, released.ID)
	if types[len(types)-1] != prodevents.OrderStatusChanged || types[len(types)-2] != prodevents.SubcontractPOCancelled {
		t.Fatalf("events in order: %v", types)
	}

	unreleased, _, err := s.CreateOrder(ctx, subcontractOrder(t, "chg-never", le, ids.New()))
	if err != nil {
		t.Fatal(err)
	}
	ust := &stepper{t: t, s: s, ctx: ctx, orderID: unreleased.ID, version: unreleased.Version}
	ust.must(domain.CmdCancel, nil)
	if got := poCancels(t, pool, unreleased.ID); len(got) != 0 {
		t.Fatalf("an unrequested order cancelled a PO: %+v", got)
	}
}

// A split shrinks the original's PO, a merge grows the survivor's and withdraws the PO of
// the orders merged away.
func TestSubcontractChanges_SplitAndMerge(t *testing.T) {
	pool, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(77))
	vendor := ids.New()
	base := subcontractOrder(t, "chg-merge-a", ids.New(), vendor)
	a, _, err := s.CreateOrder(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	st := &stepper{t: t, s: s, ctx: ctx, orderID: a.ID, version: a.Version}
	st.must(domain.CmdEstimate, nil)
	st.must(domain.CmdSchedule, nil)
	released := st.must(domain.CmdRelease, nil)

	res, err := s.SplitOrder(ctx, Split{OrderID: a.ID, IfMatch: released.Version, SplitQty: d("40"), Actor: "planner-1", TraceID: "t",
		IdempotencyKey: "chg-split", RequestHash: "split-40"})
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	got := poChanges(t, pool, a.ID)
	if len(got) != 1 || !d(got[0].PreviousQty).Equal(d("100")) || !d(got[0].Qty).Equal(d("60")) || got[0].Amount != "75.0000" {
		t.Fatalf("original after the split: %+v", got)
	}
	if got := poChanges(t, pool, res.New.ID); len(got) != 0 {
		t.Fatalf("the new order only requests: %+v", got)
	}

	// Merge the split order back (both Released): the survivor's PO returns to 100, the
	// merged order's PO is withdrawn.
	merged, err := s.MergeOrders(ctx, Merge{Orders: []MergeRef{{res.Original.ID, res.Original.Version}, {res.New.ID, res.New.Version}},
		Actor: "planner-1", TraceID: "t", IdempotencyKey: "chg-merge", RequestHash: "merge"})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	got = poChanges(t, pool, a.ID)
	if len(got) != 2 || !d(got[1].PreviousQty).Equal(d("60")) || !d(got[1].Qty).Equal(d("100")) || got[1].Amount != "125.0000" {
		t.Fatalf("survivor after the merge: %+v", got)
	}
	cancels := poCancels(t, pool, res.New.ID)
	if len(cancels) != 1 || cancels[0].Reason != domain.CancelOrderCancelled || !d(cancels[0].Qty).Equal(d("40")) || merged.Merged[0] != res.New.ID {
		t.Fatalf("merged order: %+v", cancels)
	}
}

// The subcontract inquiry shows a withdrawn request as cancelled, not requested.
func TestSubcontractChanges_Inquiry(t *testing.T) {
	_, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(78))
	released, st := releasedSubcontractOrder(t, s, ctx, "chg-inq", ids.New(), ids.New())
	st.must(domain.CmdCancel, nil)
	rep, err := s.SubcontractOperations(ctx, SubcontractFilter{OrderStatus: domain.Cancelled})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Items) != 1 || rep.Items[0].OrderID != released.ID || rep.Items[0].PORequestedAt != nil || rep.Items[0].POCancelledAt == nil {
		t.Fatalf("inquiry: %+v", rep.Items)
	}
}

// FR-21.10 / AC-21.9: the PO carries the order's delivery date as its due date, so moving
// the date of a released order changes the PO, and a quantity and date PATCH in one call
// sends the new date with the new quantity; clearing the date is a change too.
func TestSubcontractChanges_DeliveryDateMovesTheDueDate(t *testing.T) {
	pool, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(79))
	released, st := releasedSubcontractOrder(t, s, ctx, "chg-due", ids.New(), ids.New())
	if reqs := poRequests(t, pool, released.ID); len(reqs) != 1 || reqs[0].DueDate == nil || *reqs[0].DueDate != "2026-11-30" {
		t.Fatalf("request: %+v", reqs)
	}

	later := "2026-12-15"
	o, err := s.UpdateOrder(ctx, OrderUpdate{OrderID: released.ID, IfMatch: st.version, SetDeliveryDate: true, DeliveryDate: &later, Actor: "planner-1"})
	if err != nil {
		t.Fatalf("date patch: %v", err)
	}
	got := poChanges(t, pool, released.ID)
	if len(got) != 1 || got[0].PreviousDueDate == nil || *got[0].PreviousDueDate != "2026-11-30" || got[0].DueDate == nil ||
		*got[0].DueDate != later || !d(got[0].Qty).Equal(d("100")) || !d(got[0].PreviousQty).Equal(d("100")) || got[0].Amount != "125.0000" {
		t.Fatalf("change after the date patch: %+v", got)
	}

	// The same date again changes nothing.
	if o, err = s.UpdateOrder(ctx, OrderUpdate{OrderID: released.ID, IfMatch: o.Version, SetDeliveryDate: true, DeliveryDate: &later, Actor: "planner-1"}); err != nil {
		t.Fatalf("same date: %v", err)
	}
	if got := poChanges(t, pool, released.ID); len(got) != 1 {
		t.Fatalf("an unchanged date changed the PO: %+v", got)
	}

	// Reset to Estimated (the PO stays) and change quantity and date in one call.
	st.version = o.Version
	st.must(domain.CmdResetStatus, func(tr *Transition) { tr.ResetTo = domain.Estimated })
	qty, earlier := d("50"), "2026-12-01"
	if o, err = s.UpdateOrder(ctx, OrderUpdate{OrderID: released.ID, IfMatch: st.version, Quantity: &qty, SetDeliveryDate: true, DeliveryDate: &earlier,
		Actor: "planner-1"}); err != nil {
		t.Fatalf("quantity and date patch: %v", err)
	}
	got = poChanges(t, pool, released.ID)
	if len(got) != 2 || *got[1].PreviousDueDate != later || got[1].DueDate == nil || *got[1].DueDate != earlier || !d(got[1].Qty).Equal(d("50")) {
		t.Fatalf("change after the quantity and date patch: %+v", got)
	}

	// Clearing the date is a change to no due date.
	if _, err = s.UpdateOrder(ctx, OrderUpdate{OrderID: released.ID, IfMatch: o.Version, SetDeliveryDate: true, Actor: "planner-1"}); err != nil {
		t.Fatalf("clear date: %v", err)
	}
	got = poChanges(t, pool, released.ID)
	if len(got) != 3 || got[2].DueDate != nil || *got[2].PreviousDueDate != earlier {
		t.Fatalf("change after clearing the date: %+v", got)
	}
}
