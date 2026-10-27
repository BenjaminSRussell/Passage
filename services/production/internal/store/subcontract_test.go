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

// subcontractOrder is the sample order with a second, subcontracted operation 20
// at 1.25 per unit on a vendor resource.
func subcontractOrder(t *testing.T, key, legalEntity, vendor string) NewOrder {
	t.Helper()
	in := sampleOrder(t, key, legalEntity)
	ops := []domain.Operation{
		{OperNumber: 10, ResourceID: ids.New(), RunHPer: d("0.5"), LaborRate: d("30"), OverheadRate: d("10")},
		{OperNumber: 20, ResourceID: vendor, Subcontract: true, SubcontractPrice: d("1.25")},
	}
	planned, err := domain.PlanOperations(ops, in.Quantity)
	if err != nil {
		t.Fatal(err)
	}
	in.Operations = planned
	return in
}

func poRequests(t *testing.T, pool *pgxpool.Pool, orderID string) []prodevents.SubcontractPORequestedData {
	t.Helper()
	rows, err := pool.Query(context.Background(), `select payload from outbox where aggregate_id = $1 and type = $2 order by created_at, id`,
		orderID, prodevents.SubcontractPORequested)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []prodevents.SubcontractPORequestedData
	for rows.Next() {
		var raw json.RawMessage
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var evt struct {
			Data prodevents.SubcontractPORequestedData `json:"data"`
		}
		if err := json.Unmarshal(raw, &evt); err != nil {
			t.Fatal(err)
		}
		out = append(out, evt.Data)
	}
	return out
}

func operation(o Order, number int) domain.Operation {
	for _, op := range o.Operations {
		if op.OperNumber == number {
			return op
		}
	}
	return domain.Operation{}
}

// AC-21.9 (production side): releasing an order with an operation on a vendor
// resource requests the vendor PO once, with the operation's quantity, price and
// the order's due date, in the release transaction; a reset and second release,
// and a re-estimate of the released order, request nothing more.
func TestSubcontract_ReleaseRequestsTheVendorPOOnce(t *testing.T) {
	pool, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(71))
	vendor := ids.New()
	in := subcontractOrder(t, "sub-release", ids.New(), vendor)
	o, _, err := s.CreateOrder(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	st := &stepper{t: t, s: s, ctx: ctx, orderID: o.ID, version: o.Version}
	st.must(domain.CmdEstimate, nil)
	scheduled := st.must(domain.CmdSchedule, nil)
	if got := poRequests(t, pool, o.ID); len(got) != 0 || operation(scheduled, 20).PORequestedAt != nil {
		t.Fatalf("requested before release: %+v", got)
	}

	released := st.must(domain.CmdRelease, nil)
	got := poRequests(t, pool, o.ID)
	if len(got) != 1 {
		t.Fatalf("want one request, got %+v", got)
	}
	req := got[0]
	if req.OrderID != o.ID || req.OrderNumber != o.OrderNumber || req.LegalEntityID != in.LegalEntityID || req.SiteID != in.SiteID ||
		req.WarehouseID != in.WarehouseID || req.OperNumber != 20 || req.ResourceID != vendor || req.Currency != "USD" ||
		req.DueDate == nil || *req.DueDate != "2026-11-30" || req.Actor != "planner-1" {
		t.Fatalf("request = %+v", req)
	}
	// 100 units, no scrap: quantity 100 at 1.25 is 125, the estimate's subcontract row.
	if !d(req.Qty).Equal(d("100")) || !d(req.UnitPrice).Equal(d("1.25")) || req.Amount != "125.0000" {
		t.Fatalf("quantity, price or amount: %+v", req)
	}
	planned := plannedGroup(released, domain.GroupSubcontract)
	if !planned.Equal(d(req.Amount)) {
		t.Fatalf("requested %s, planned %s", req.Amount, planned)
	}
	if operation(released, 20).PORequestedAt == nil || operation(released, 10).PORequestedAt != nil {
		t.Fatalf("poRequestedAt: %+v", released.Operations)
	}
	types := outboxTypes(t, pool, o.ID)
	if types[len(types)-2] != prodevents.OrderStatusChanged || types[len(types)-1] != prodevents.SubcontractPORequested {
		t.Fatalf("events in order: %v", types)
	}

	st.must(domain.CmdEstimate, nil) // a re-estimate keeps Released
	st.must(domain.CmdResetStatus, nil)
	st.must(domain.CmdRelease, nil)
	if got := poRequests(t, pool, o.ID); len(got) != 1 {
		t.Fatalf("a second release requested again: %+v", got)
	}

	// A replay of the release command emits nothing either.
	tr := Transition{OrderID: o.ID, IfMatch: st.version, Command: domain.CmdRelease, Actor: "planner-1",
		IdempotencyKey: o.ID + "-" + string(domain.CmdRelease) + "-" + string(rune('a'+st.n)), RequestHash: string(domain.CmdRelease)}
	if _, replayed, err := s.Transition(ctx, tr); err != nil || !replayed {
		t.Fatalf("replay: replayed=%v err=%v", replayed, err)
	}
	if got := poRequests(t, pool, o.ID); len(got) != 1 {
		t.Fatalf("a replay requested again: %+v", got)
	}

	// An order without subcontract operations requests nothing.
	plain, _, err := s.CreateOrder(ctx, sampleOrder(t, "sub-plain", in.LegalEntityID))
	if err != nil {
		t.Fatal(err)
	}
	ps := &stepper{t: t, s: s, ctx: ctx, orderID: plain.ID, version: plain.Version}
	ps.must(domain.CmdEstimate, nil)
	ps.must(domain.CmdSchedule, nil)
	ps.must(domain.CmdRelease, nil)
	if got := poRequests(t, pool, plain.ID); len(got) != 0 {
		t.Fatalf("in-house order requested: %+v", got)
	}
}

// A subcontract operation added to a released order, and the new order of a
// released split, are requested in their own transaction; the operations
// already requested are not requested again.
func TestSubcontract_EditAndSplitOfAReleasedOrder(t *testing.T) {
	pool, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(72))
	vendor, vendor2 := ids.New(), ids.New()
	o, _, err := s.CreateOrder(ctx, subcontractOrder(t, "sub-edit", ids.New(), vendor))
	if err != nil {
		t.Fatal(err)
	}
	st := &stepper{t: t, s: s, ctx: ctx, orderID: o.ID, version: o.Version}
	st.must(domain.CmdEstimate, nil)
	st.must(domain.CmdSchedule, nil)
	st.must(domain.CmdRelease, nil)

	on, price := true, d("2")
	edited, err := st.edit(domain.OrderEdits{Operations: []domain.OperationEdit{
		{Op: domain.EditAdd, OperNumber: 30, ResourceID: &vendor2, Subcontract: &on, SubcontractPrice: &price},
	}}, true)
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	got := poRequests(t, pool, o.ID)
	if len(got) != 2 || got[1].OperNumber != 30 || got[1].ResourceID != vendor2 || got[1].Amount != "200.0000" {
		t.Fatalf("requests after the edit: %+v", got)
	}
	if operation(edited, 30).PORequestedAt == nil {
		t.Fatalf("added operation not stamped: %+v", edited.Operations)
	}

	res, err := s.SplitOrder(ctx, Split{OrderID: o.ID, IfMatch: st.version, SplitQty: d("40"), Actor: "planner-1", TraceID: "trace-s",
		IdempotencyKey: "sub-split", RequestHash: "split-40"})
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	if got := poRequests(t, pool, o.ID); len(got) != 2 {
		t.Fatalf("the original was requested again: %+v", got)
	}
	split := poRequests(t, pool, res.New.ID)
	if len(split) != 2 || split[0].OperNumber != 20 || split[1].OperNumber != 30 || !d(split[0].Qty).Equal(d("40")) ||
		split[0].OrderNumber != res.New.OrderNumber {
		t.Fatalf("requests of the split order: %+v", split)
	}

	// The split's replay requests nothing more.
	if _, err := s.SplitOrder(ctx, Split{OrderID: o.ID, IfMatch: st.version, SplitQty: d("40"), Actor: "planner-1", TraceID: "trace-s",
		IdempotencyKey: "sub-split", RequestHash: "split-40"}); err != nil {
		t.Fatalf("split replay: %v", err)
	}
	if got := poRequests(t, pool, res.New.ID); len(got) != 2 {
		t.Fatalf("split replay requested again: %+v", got)
	}
}

// The subcontract operations inquiry (4.21.14): only subcontract operations,
// filters, keyset pages, totals per currency over the whole filter, and tenant
// isolation.
func TestSubcontract_OperationsInquiry(t *testing.T) {
	_, s := migrated(t)
	tid := testkit.TenantID(73)
	ctx := tenantCtx(tid)
	le, other := ids.New(), ids.New()
	vendor, vendor2 := ids.New(), ids.New()

	first, _, err := s.CreateOrder(ctx, subcontractOrder(t, "inq-1", le, vendor))
	if err != nil {
		t.Fatal(err)
	}
	st := &stepper{t: t, s: s, ctx: ctx, orderID: first.ID, version: first.Version}
	st.must(domain.CmdEstimate, nil)
	st.must(domain.CmdSchedule, nil)
	st.must(domain.CmdRelease, nil)
	second := subcontractOrder(t, "inq-2", le, vendor2)
	second.Currency = "EUR"
	second.Operations[1].SubcontractPrice = d("0.333")
	if _, _, err := s.CreateOrder(ctx, second); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateOrder(ctx, subcontractOrder(t, "inq-3", other, vendor)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateOrder(ctx, sampleOrder(t, "inq-plain", le)); err != nil {
		t.Fatal(err)
	}

	all, err := s.SubcontractOperations(ctx, SubcontractFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Items) != 3 || all.NextCursor != "" {
		t.Fatalf("want the 3 subcontract operations, got %+v", all.Items)
	}
	row := all.Items[0]
	if row.OrderID != first.ID || row.OperNumber != 20 || row.ResourceID != vendor || row.OrderStatus != domain.Released ||
		row.OperStatus != domain.OperOpen || row.PORequestedAt == nil || !row.Amount.Equal(d("125")) || !row.UnitPrice.Equal(d("1.25")) ||
		row.DueDate == nil || *row.DueDate != "2026-11-30" {
		t.Fatalf("first row = %+v", row)
	}
	if all.Items[1].PORequestedAt != nil || !all.Items[1].Amount.Equal(d("33.3")) {
		t.Fatalf("unreleased row = %+v", all.Items[1])
	}
	if len(all.Totals) != 2 || all.Totals[0].Currency != "EUR" || !all.Totals[0].Amount.Equal(d("33.3")) ||
		all.Totals[1].Operations != 2 || !all.Totals[1].Amount.Equal(d("250")) {
		t.Fatalf("totals = %+v", all.Totals)
	}

	page, err := s.SubcontractOperations(ctx, SubcontractFilter{Limit: 2})
	if err != nil || len(page.Items) != 2 || page.NextCursor != all.Items[1].OrderID+":20" {
		t.Fatalf("page 1: %+v %v", page, err)
	}
	rest, err := s.SubcontractOperations(ctx, SubcontractFilter{Limit: 2, CursorOrder: all.Items[1].OrderID, CursorOper: 20})
	if err != nil || len(rest.Items) != 1 || rest.Items[0].OrderID != all.Items[2].OrderID || rest.NextCursor != "" {
		t.Fatalf("page 2: %+v %v", rest, err)
	}
	// Totals cover the whole filter, not the page.
	if len(rest.Totals) != 2 || rest.Totals[1].Operations != 2 {
		t.Fatalf("page totals = %+v", rest.Totals)
	}

	for name, tc := range map[string]struct {
		f    SubcontractFilter
		want int
	}{
		"vendor":          {SubcontractFilter{ResourceID: vendor}, 2},
		"legal entity":    {SubcontractFilter{LegalEntityID: other}, 1},
		"order status":    {SubcontractFilter{OrderStatus: domain.Released}, 1},
		"operation open":  {SubcontractFilter{OperStatus: domain.OperOpen}, 3},
		"operation done":  {SubcontractFilter{OperStatus: domain.OperFinished}, 0},
		"due in November": {SubcontractFilter{DueFrom: "2026-11-01", DueTo: "2026-11-30"}, 3},
		"due later":       {SubcontractFilter{DueFrom: "2026-12-01"}, 0},
	} {
		got, err := s.SubcontractOperations(ctx, tc.f)
		if err != nil || len(got.Items) != tc.want {
			t.Errorf("%s: want %d rows, got %d (%v)", name, tc.want, len(got.Items), err)
		}
	}

	// Another tenant sees none (the legal-entity scope is RLS, in TestStore_RowLevelSecurity).
	foreign, err := s.SubcontractOperations(tenantCtx(testkit.TenantID(74)), SubcontractFilter{})
	if err != nil || len(foreign.Items) != 0 || len(foreign.Totals) != 0 {
		t.Fatalf("another tenant: %+v %v", foreign, err)
	}
}
