package store_test

import (
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	purchasingevents "github.com/BenjaminSRussell/passage/services/purchasing/internal/events"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

// approvedFor is an approved requisition whose lines each name the preferred vendor given for them.
func approvedFor(t *testing.T, s *store.Postgres, vendors ...string) store.Requisition {
	t.Helper()
	ctx := principalContext(31)
	amounts := make([]string, len(vendors))
	for i := range amounts {
		amounts[i] = "10"
	}
	in := requisition(t, ids.New(), amounts...)
	for i := range in.Lines {
		in.Lines[i].PreferredVendorID = vendors[i]
	}
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

func sourcingOf(t *testing.T, s *store.Postgres, ids ...string) []store.SourcingLine {
	t.Helper()
	out, err := s.RequisitionLinesByID(principalContext(31), ids)
	if err != nil || len(out) != len(ids) {
		t.Fatalf("lines = %d, %v", len(out), err)
	}
	return out
}

func lineStatus(t *testing.T, s *store.Postgres, id string) string {
	return sourcingOf(t, s, id)[0].Status
}

// PRD 3.17.16: a release run writes the POs of the vendor groups that can be written, leaves the lines of a group that
// cannot in the queue with the reason, records the run and its event, and replays by key.
func TestReleaseRequisitions_PartialRunKeepsFailedGroupInTheQueue(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(31)
	va, vb := ids.New(), ids.New()
	reqA := approvedFor(t, s, va, va)
	reqB := approvedFor(t, s, vb)
	noVendor := approvedFor(t, s, "")
	a1, a2, b1 := reqA.Lines[0], reqA.Lines[1], reqB.Lines[0]

	badB := orderFor(t, vb, b1)
	badB.Lines[0].Qty = dec("99") // the requisition line needs 1 EA: the group cannot be written
	in := store.ReleaseInput{LegalEntityID: legalEntity, WindowDays: 7, MaxLines: 200, Considered: 4, RequestedBy: ids.New(),
		Groups: []store.ReleaseGroup{
			{VendorID: va, Lines: sourcingOf(t, s, a1.ID, a2.ID), Orders: []store.PurchaseOrder{orderFor(t, va, a1, a2)}},
			{VendorID: vb, Lines: sourcingOf(t, s, b1.ID), Orders: []store.PurchaseOrder{badB}},
		},
		Skipped: []store.ReleaseSkip{{RequisitionLineID: noVendor.Lines[0].ID, RequisitionNumber: noVendor.Number, LineNo: 1, Code: "PRC-CONS-002", Reason: "no vendor"}},
	}
	// Considered counts all four lines: two released, two skipped (vendor B's and the one with no vendor).
	run, err := s.ReleaseRequisitions(ctx, in, store.Idem{Key: "rel-1", Hash: "h"})
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != store.ReleasePartial || run.LinesConsidered != 4 || run.LinesReleased != 2 || run.LinesSkipped != 2 ||
		len(run.PurchaseOrders) != 1 || len(run.Skipped) != 2 || run.PurchaseOrders[0].VendorID != va || run.PurchaseOrders[0].LineCount != 2 ||
		!run.PurchaseOrders[0].Total.Equal(dec("20")) {
		t.Fatalf("run = %+v", run)
	}
	codes := map[string]string{}
	for _, sk := range run.Skipped {
		codes[sk.RequisitionLineID] = sk.Code
	}
	if codes[noVendor.Lines[0].ID] != "PRC-CONS-002" || codes[b1.ID] == "" || run.Skipped[1].Reason == "" {
		t.Fatalf("skipped = %+v", run.Skipped)
	}
	if lineStatus(t, s, a1.ID) != "CONSOLIDATED" || lineStatus(t, s, a2.ID) != "CONSOLIDATED" || lineStatus(t, s, b1.ID) != "OPEN" || lineStatus(t, s, noVendor.Lines[0].ID) != "OPEN" {
		t.Fatal("A's lines must be CONSOLIDATED, B's and the vendorless line still OPEN")
	}
	po, err := s.GetPurchaseOrder(ctx, run.PurchaseOrders[0].PurchaseOrderID)
	if err != nil || po.ApprovalStatus != "DRAFT" || len(po.Lines) != 2 || po.Lines[0].RequisitionLineID != a1.ID {
		t.Fatalf("PO = %+v, %v", po, err)
	}
	if got, _ := s.GetRequisition(ctx, reqA.ID); got.Status != "CLOSED" {
		t.Fatalf("requisition A is %s, want CLOSED", got.Status)
	}

	var evt map[string]any
	n := 0
	for _, row := range outbox(t, ctx, pool) {
		if row.Type == purchasingevents.RequisitionReleaseCompleted {
			evt, n = row.Data, n+1
		}
	}
	if n != 1 || evt["run_id"] != run.ID || evt["status"] != "PARTIAL" || int(evt["skipped_count"].(float64)) != 2 ||
		len(evt["purchase_orders"].([]any)) != 1 || len(evt["skipped"].([]any)) != 2 {
		t.Fatalf("requisitionrelease.completed.v1 x%d = %+v", n, evt)
	}

	// The same key returns the stored run and writes nothing more.
	again, err := s.ReleaseRequisitions(ctx, in, store.Idem{Key: "rel-1", Hash: "h"})
	if err != nil || again.ID != run.ID || len(again.PurchaseOrders) != 1 || again.PurchaseOrders[0].PurchaseOrderID != run.PurchaseOrders[0].PurchaseOrderID {
		t.Fatalf("replay = %+v, %v", again, err)
	}
	if _, err := s.ReleaseRequisitions(ctx, in, store.Idem{Key: "rel-1", Hash: "other"}); err == nil {
		t.Fatal("reusing the key with another body must fail")
	}
	pos, _ := s.ListPurchaseOrders(ctx, store.PurchaseOrderFilter{}, "", 0)
	if len(pos.Items) != 1 {
		t.Fatalf("POs = %d, want 1", len(pos.Items))
	}

	got, err := s.GetReleaseRun(ctx, run.ID)
	if err != nil || got.Status != store.ReleasePartial || len(got.Skipped) != 2 || got.RequestedBy != in.RequestedBy {
		t.Fatalf("get = %+v, %v", got, err)
	}
	if _, err := s.GetReleaseRun(ctx, ids.New()); err == nil {
		t.Fatal("unknown run must be 404")
	}
	page, err := s.ListReleaseRuns(ctx, store.ReleaseRunFilter{LegalEntityID: legalEntity, Status: store.ReleasePartial}, "", 0)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("list = %d, %v", len(page.Items), err)
	}
	if other, _ := s.ListReleaseRuns(ctx, store.ReleaseRunFilter{Status: store.ReleaseFailed}, "", 0); len(other.Items) != 0 {
		t.Fatalf("FAILED runs = %d", len(other.Items))
	}
}

func TestReleaseRequisitions_EmptyAndFailedRuns(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(31)

	empty, err := s.ReleaseRequisitions(ctx, store.ReleaseInput{LegalEntityID: legalEntity, WindowDays: 7, MaxLines: 200}, store.Idem{Key: "e", Hash: "h"})
	if err != nil || empty.Status != store.ReleaseEmpty || empty.LinesConsidered != 0 || len(empty.PurchaseOrders) != 0 {
		t.Fatalf("empty = %+v, %v", empty, err)
	}

	v := ids.New()
	r := approvedFor(t, s, v)
	bad := orderFor(t, v, r.Lines[0])
	bad.Lines[0].Qty = dec("5")
	failed, err := s.ReleaseRequisitions(ctx, store.ReleaseInput{LegalEntityID: legalEntity, WindowDays: 7, MaxLines: 200, Considered: 1,
		Groups: []store.ReleaseGroup{{VendorID: v, Lines: sourcingOf(t, s, r.Lines[0].ID), Orders: []store.PurchaseOrder{bad}}}}, store.Idem{Key: "f", Hash: "h"})
	if err != nil || failed.Status != store.ReleaseFailed || failed.LinesSkipped != 1 || failed.LinesReleased != 0 || lineStatus(t, s, r.Lines[0].ID) != "OPEN" {
		t.Fatalf("failed = %+v, %v", failed, err)
	}

	types := map[string]int{}
	for _, row := range outbox(t, ctx, pool) {
		types[row.Type]++
	}
	if types[purchasingevents.RequisitionReleaseCompleted] != 1 {
		t.Fatalf("events = %v; want one for the failed run and none for the empty one", types)
	}
}

// A line a concurrent write already ordered is skipped by the run that read it earlier; nothing is ordered twice.
func TestReleaseRequisitions_LineOrderedMeanwhileIsSkipped(t *testing.T) {
	s := store.New(migrated(t))
	ctx := principalContext(31)
	v := ids.New()
	r := approvedFor(t, s, v)
	lines := sourcingOf(t, s, r.Lines[0].ID)
	if _, err := s.CreatePurchaseOrder(ctx, orderFor(t, v, r.Lines[0]), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	run, err := s.ReleaseRequisitions(ctx, store.ReleaseInput{LegalEntityID: legalEntity, WindowDays: 7, MaxLines: 200, Considered: 1,
		Groups: []store.ReleaseGroup{{VendorID: v, Lines: lines, Orders: []store.PurchaseOrder{orderFor(t, v, r.Lines[0])}}}}, store.Idem{Key: "x", Hash: "h"})
	if err != nil || run.Status != store.ReleaseFailed || len(run.Skipped) != 1 || run.Skipped[0].Code != store.CodeRequisitionState {
		t.Fatalf("run = %+v, %v", run, err)
	}
	pos, _ := s.ListPurchaseOrders(ctx, store.PurchaseOrderFilter{}, "", 0)
	if len(pos.Items) != 1 {
		t.Fatalf("POs = %d, want 1", len(pos.Items))
	}
}

// A vendor group that fails after it has written part of its work is undone whole by its savepoint: the first PO of
// the group, its requisition line link, its agreement draw and the agreement.threshold.v1 it enqueued are all gone,
// and the transaction is still usable, so the next vendor's group is written and the run is recorded.
func TestReleaseRequisitions_FailedGroupLeavesNothingBehind(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(31)
	v, w, product := ids.New(), ids.New(), ids.New()
	agreement, err := s.CreateAgreement(ctx, blanket(t, v, product), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	qtyLine := agreement.Agreement.Lines[0].ID
	x, y, z := approvedFor(t, s, v).Lines[0], approvedFor(t, s, v).Lines[0], approvedFor(t, s, w).Lines[0]

	// PO 1 of vendor v orders line x and draws 900 of the 1,000 EA agreement line (its 90 % threshold).
	first := orderFor(t, v, x)
	drawn := release(t, v, product, "900", qtyLine, "2027-03-01").Lines[0]
	drawn.LineNo = 2
	first.Lines = append(first.Lines, drawn)
	first.Subtotal, first.Total = first.Subtotal.Add(drawn.LineAmount), first.Total.Add(drawn.LineAmount)
	// PO 2 of vendor v orders line y, which a buyer orders by hand before the run writes.
	second := orderFor(t, v, y)
	if _, err := s.CreatePurchaseOrder(ctx, orderFor(t, v, y), store.Idem{}); err != nil {
		t.Fatal(err)
	}

	in := store.ReleaseInput{LegalEntityID: legalEntity, WindowDays: 7, MaxLines: 200, Considered: 3, Groups: []store.ReleaseGroup{
		{VendorID: v, Lines: sourcingOf(t, s, x.ID, y.ID), Orders: []store.PurchaseOrder{first, second}},
		{VendorID: w, Lines: sourcingOf(t, s, z.ID), Orders: []store.PurchaseOrder{orderFor(t, w, z)}},
	}}
	run, err := s.ReleaseRequisitions(ctx, in, store.Idem{Key: "sp", Hash: "h"})
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != store.ReleasePartial || run.LinesReleased != 1 || len(run.PurchaseOrders) != 1 || run.PurchaseOrders[0].VendorID != w ||
		len(run.Skipped) != 2 || run.Skipped[0].Code != store.CodeRequisitionState || len(run.Warnings) != 0 {
		t.Fatalf("run = %+v; want vendor v's lines skipped with PRC-REQ-STATE and no warning of its undone draw", run)
	}
	if lineStatus(t, s, x.ID) != "OPEN" || sourcingOf(t, s, x.ID)[0].PurchaseOrderID != "" || lineStatus(t, s, z.ID) != "CONSOLIDATED" {
		t.Fatal("line x must be OPEN and unlinked, line z ordered")
	}
	got, err := s.GetAgreement(ctx, agreement.Agreement.ID)
	if err != nil || !got.Agreement.Lines[0].ReleasedQty.IsZero() {
		t.Fatalf("agreement line released %s, %v; want the undone draw gone", got.Agreement.Lines[0].ReleasedQty, err)
	}
	pos, _ := s.ListPurchaseOrders(ctx, store.PurchaseOrderFilter{}, "", 0)
	if len(pos.Items) != 2 {
		t.Fatalf("POs = %d, want the hand-made one and vendor w's", len(pos.Items))
	}
	types := map[string]int{}
	for _, row := range outbox(t, ctx, pool) {
		types[row.Type]++
	}
	if types[purchasingevents.AgreementThreshold] != 0 || types[purchasingevents.RequisitionReleaseCompleted] != 1 {
		t.Fatalf("outbox = %v; the undone group's agreement.threshold.v1 must not survive", types)
	}
}

// The release run reads the queue oldest first, paging forward: every line once, in the order lines joined the queue.
func TestListOpenRequisitionLines_OldestFirstPagesForward(t *testing.T) {
	s := store.New(migrated(t))
	ctx := principalContext(31)
	v := ids.New()
	var want []string
	for range 3 {
		want = append(want, approvedFor(t, s, v).Lines[0].ID)
	}
	f := store.SourcingFilter{LegalEntityID: legalEntity, Sourcing: store.SourcingHasVendor, OldestFirst: true}
	first, err := s.ListOpenRequisitionLines(ctx, f, "", 2)
	if err != nil || len(first.Items) != 2 || first.Next == "" {
		t.Fatalf("first page = %+v, %v", first, err)
	}
	rest, err := s.ListOpenRequisitionLines(ctx, f, first.Next, 2)
	if err != nil || len(rest.Items) != 1 || rest.Next != "" {
		t.Fatalf("second page = %+v, %v", rest, err)
	}
	got := []string{first.Items[0].ID, first.Items[1].ID, rest.Items[0].ID}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
	if newest, _ := s.ListOpenRequisitionLines(ctx, store.SourcingFilter{LegalEntityID: legalEntity}, "", 1); newest.Items[0].ID != want[2] {
		t.Fatal("the board still lists newest first")
	}
}
