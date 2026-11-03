package store_test

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	purchasingevents "github.com/BenjaminSRussell/passage/services/purchasing/internal/events"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

// Slice 16: a PO line is delivered to its own warehouse or, when it names none, the PO header's.
// The destination is stored only when it differs (so a header move carries the following lines),
// shows in the version patch, rides on po.confirmed.v1 per line and feeds the open supply list.
func TestLineDestination_DefaultsOverrideVersionAndEvent(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(61)
	whA, whB, site := ids.New(), ids.New(), ids.New()

	in := lifecyclePO(t, ids.New(), "100", "2027-01-05")
	in.SiteID, in.WarehouseID = site, whA
	in.Lines[0].SiteID, in.Lines[0].WarehouseID = site, whA // follows the header
	second := in.Lines[0]
	second.LineNo, second.WarehouseID = 2, whB // overrides it
	in.Lines = append(in.Lines, second)
	in.Subtotal, in.Total = dec("2000"), dec("2000")
	in.Lines[1].Qty, in.Lines[1].GrossAmount, in.Lines[1].LineAmount = dec("100"), dec("1000"), dec("1000")
	in.Lines[0].GrossAmount, in.Lines[0].LineAmount = dec("1000"), dec("1000")
	in.Subtotal, in.Total = dec("2000"), dec("2000")

	po := confirmedPO(t, ctx, s, in)
	if po.Lines[0].WarehouseID != whA || po.Lines[1].WarehouseID != whB || po.Lines[1].SiteID != site {
		t.Fatalf("effective destinations = %s %s %s", po.Lines[0].WarehouseID, po.Lines[1].WarehouseID, po.Lines[1].SiteID)
	}

	// po.confirmed.v1 carries each line's destination.
	ev := outbox(t, ctx, pool)
	var lines []any
	for _, r := range ev {
		if r.Type == purchasingevents.POConfirmed {
			lines, _ = r.Data["lines"].([]any)
		}
	}
	if len(lines) != 2 || lines[0].(map[string]any)["warehouse_id"] != whA || lines[1].(map[string]any)["warehouse_id"] != whB {
		t.Fatalf("po.confirmed lines = %v", lines)
	}

	// Open supply by warehouse: line 1 under A (inherited), line 2 under B.
	a, err := s.ListOpenOrderLines(ctx, store.OpenLineFilter{WarehouseID: whA}, "", 0)
	if err != nil || len(a.Items) != 1 || a.Items[0].LineNo != 1 || !a.Items[0].OpenQty.Equal(dec("100")) {
		t.Fatalf("warehouse A open lines = %+v, %v", a.Items, err)
	}
	b, err := s.ListOpenOrderLines(ctx, store.OpenLineFilter{WarehouseID: whB}, "", 0)
	if err != nil || len(b.Items) != 1 || b.Items[0].LineNo != 2 || b.Items[0].SiteID != site {
		t.Fatalf("warehouse B open lines = %+v, %v", b.Items, err)
	}

	// Moving line 1 to warehouse B is a new confirmed version (no reapproval rule fires) whose patch names it.
	next := edited(po, in)
	next.Lines = []store.PurchaseOrderLine{po.Lines[0], po.Lines[1]}
	next.Lines[0].WarehouseID = whB
	moved, err := s.EditPurchaseOrder(ctx, po.ID, po.RowVersion, next)
	if err != nil {
		t.Fatal(err)
	}
	if moved.VersionNo != 2 || moved.ApprovalStatus != domain.POStatusConfirmed || moved.Lines[0].WarehouseID != whB {
		t.Fatalf("after move: v%d %s warehouse %s", moved.VersionNo, moved.ApprovalStatus, moved.Lines[0].WarehouseID)
	}
	v2, err := s.GetPurchaseOrderVersion(ctx, po.ID, 2)
	if err != nil || !hasPatchPath(v2.Patch, "/lines/0/warehouseId") {
		t.Fatalf("version 2 patch = %+v, %v", v2.Patch, err)
	}
	if a, _ := s.ListOpenOrderLines(ctx, store.OpenLineFilter{WarehouseID: whA}, "", 0); len(a.Items) != 0 {
		t.Fatalf("warehouse A still lists %d lines", len(a.Items))
	}

	// Receipts pin the destination.
	setLineQuantities(t, ctx, pool, po.ID, "10", "0")
	cur, _ := s.GetPurchaseOrder(ctx, po.ID)
	back := edited(cur, in)
	back.Lines = []store.PurchaseOrderLine{cur.Lines[0], cur.Lines[1]}
	back.Lines[0].WarehouseID = whA
	if _, err := s.EditPurchaseOrder(ctx, cur.ID, cur.RowVersion, back); err == nil || !strings.Contains(err.Error(), "has receipts") {
		t.Fatalf("moving a received line should be refused for its receipts, got %v", err)
	}
}

// The open supply list pages by delivery date and leaves out cancelled and fully received lines.
func TestListOpenOrderLines_PagingAndFilters(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(62)
	wh := ids.New()
	var first store.PurchaseOrder
	for i, d := range []string{"2027-03-03", "2027-03-01", "2027-03-02"} {
		in := lifecyclePO(t, ids.New(), "10", d)
		in.WarehouseID = wh
		po := confirmedPO(t, ctx, s, in)
		if i == 0 {
			first = po
		}
	}
	p1, err := s.ListOpenOrderLines(ctx, store.OpenLineFilter{WarehouseID: wh}, "", 2)
	if err != nil || len(p1.Items) != 2 || p1.Next == "" || p1.Items[0].DeliveryDate.String() != "2027-03-01" || p1.Items[1].DeliveryDate.String() != "2027-03-02" {
		t.Fatalf("page 1 = %+v next %q, %v", p1.Items, p1.Next, err)
	}
	p2, err := s.ListOpenOrderLines(ctx, store.OpenLineFilter{WarehouseID: wh}, p1.Next, 2)
	if err != nil || len(p2.Items) != 1 || p2.Items[0].DeliveryDate.String() != "2027-03-03" || p2.Next != "" {
		t.Fatalf("page 2 = %+v, %v", p2.Items, err)
	}
	to := day(t, "2027-03-01")
	if r, _ := s.ListOpenOrderLines(ctx, store.OpenLineFilter{WarehouseID: wh, DueTo: &to}, "", 0); len(r.Items) != 1 {
		t.Fatalf("dueTo filter = %d lines", len(r.Items))
	}
	setLineQuantities(t, ctx, pool, first.ID, "10", "0")
	if r, _ := s.ListOpenOrderLines(ctx, store.OpenLineFilter{WarehouseID: wh}, "", 0); len(r.Items) != 2 {
		t.Fatalf("fully received line still open: %d lines", len(r.Items))
	}
	if _, err := s.ListOpenOrderLines(ctx, store.OpenLineFilter{}, "garbage", 0); err == nil {
		t.Fatal("bad cursor should be refused")
	}
}

// The warehouse a requisition line is wanted in is stored and shows in the consolidation queue.
func TestRequisitionLineWarehouse_RoundTrip(t *testing.T) {
	s := store.New(migrated(t))
	ctx := principalContext(63)
	in := requisition(t, ids.New(), "10")
	wh := ids.New()
	in.Lines[0].WarehouseID = wh
	r, err := s.CreateRequisition(ctx, in, store.Idem{})
	if err != nil || r.Lines[0].WarehouseID != wh {
		t.Fatalf("created line warehouse = %q, %v", r.Lines[0].WarehouseID, err)
	}
	if r, err = s.SubmitRequisition(ctx, r.ID, 1, nil, "builtin-1", store.Idem{}); err != nil {
		t.Fatal(err)
	}
	q, err := s.ListOpenRequisitionLines(ctx, store.SourcingFilter{}, "", 0)
	if err != nil || len(q.Items) != 1 || q.Items[0].WarehouseID != wh {
		t.Fatalf("queue = %+v, %v", q.Items, err)
	}
}

// A header move carries the lines that follow the header only while the PO has never been
// confirmed (so no line can have receipts yet); from the first confirmation the header destination
// is fixed and a line that follows it cannot be moved through it.
func TestLineDestination_HeaderMove(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(64)
	whA, whB, whC := ids.New(), ids.New(), ids.New()

	in := lifecyclePO(t, ids.New(), "10", "2027-01-05")
	in.WarehouseID = whA
	second := in.Lines[0]
	second.LineNo, second.WarehouseID = 2, whC
	in.Lines[0].WarehouseID = whA
	in.Lines = append(in.Lines, second)
	in.Subtotal, in.Total = dec("200"), dec("200")
	draft, err := s.CreatePurchaseOrder(ctx, in, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}

	// Draft: the header moves to B; line 1 follows it (the API resolves it to B), line 2 keeps C.
	next := edited(draft, in)
	next.WarehouseID = whB
	next.Lines = []store.PurchaseOrderLine{draft.Lines[0], draft.Lines[1]}
	next.Lines[0].WarehouseID = whB
	moved, err := s.EditPurchaseOrder(ctx, draft.ID, draft.RowVersion, next)
	if err != nil {
		t.Fatal(err)
	}
	if moved.WarehouseID != whB || moved.Lines[0].WarehouseID != whB || moved.Lines[1].WarehouseID != whC {
		t.Fatalf("after header move: header %s lines %s %s", moved.WarehouseID, moved.Lines[0].WarehouseID, moved.Lines[1].WarehouseID)
	}

	po := walk(t, ctx, s, moved, domain.POActionSubmit, domain.POActionApprove, domain.POActionConfirm)
	setLineQuantities(t, ctx, pool, po.ID, "4", "0")
	cur, err := s.GetPurchaseOrder(ctx, po.ID)
	if err != nil {
		t.Fatal(err)
	}
	again := edited(cur, in)
	again.WarehouseID = whA
	again.Lines = []store.PurchaseOrderLine{cur.Lines[0], cur.Lines[1]}
	again.Lines[0].WarehouseID = whA
	if _, err := s.EditPurchaseOrder(ctx, cur.ID, cur.RowVersion, again); err == nil || !strings.Contains(err.Error(), "after the purchase order is confirmed") {
		t.Fatalf("moving the header of a confirmed PO should be refused, got %v", err)
	}
	// The received line that follows the header cannot be moved on its own either.
	again.WarehouseID = whB
	if _, err := s.EditPurchaseOrder(ctx, cur.ID, cur.RowVersion, again); err == nil || !strings.Contains(err.Error(), "has receipts") {
		t.Fatalf("moving a received line should be refused, got %v", err)
	}
}

// Keyset paging is stable when many lines share a delivery date (ties break on PO id and line
// number), and open supply never shows a legal entity outside the caller's scope.
func TestListOpenOrderLines_TiesAndLegalEntityScope(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(65)
	wh := ids.New()
	want := map[string]bool{}
	for i := 0; i < 3; i++ {
		in := lifecyclePO(t, ids.New(), "10", "2027-04-01")
		in.WarehouseID = wh
		second := in.Lines[0]
		second.LineNo = 2
		in.Lines = append(in.Lines, second)
		in.Subtotal, in.Total = dec("200"), dec("200")
		po := confirmedPO(t, ctx, s, in)
		want[po.ID+"/1"], want[po.ID+"/2"] = true, true
	}
	seen := map[string]bool{}
	after := ""
	for pages := 0; ; pages++ {
		if pages > len(want) {
			t.Fatal("paging does not end")
		}
		page, err := s.ListOpenOrderLines(ctx, store.OpenLineFilter{WarehouseID: wh}, after, 1)
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range page.Items {
			key := l.PurchaseOrderID + "/" + strconv.Itoa(l.LineNo)
			if seen[key] || !want[key] {
				t.Fatalf("page %d returned %s again or unexpectedly", pages, key)
			}
			seen[key] = true
		}
		if page.Next == "" {
			break
		}
		after = page.Next
	}
	if len(seen) != len(want) {
		t.Fatalf("paged %d lines, want %d", len(seen), len(want))
	}

	// The same tenant, scoped to another legal entity: nothing, also when it asks for the first one.
	other := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(65), LegalEntityID: ids.New(), UserID: ids.New()})
	for _, f := range []store.OpenLineFilter{{WarehouseID: wh}, {LegalEntityID: legalEntity}} {
		if r, err := s.ListOpenOrderLines(other, f, "", 0); err != nil || len(r.Items) != 0 {
			t.Fatalf("other legal entity sees %d lines (%v)", len(r.Items), err)
		}
	}
	if r, err := s.ListOpenOrderLines(ctx, store.OpenLineFilter{LegalEntityID: legalEntity, WarehouseID: wh}, "", 0); err != nil || len(r.Items) != len(want) {
		t.Fatalf("own legal entity sees %d lines (%v)", len(r.Items), err)
	}
}
