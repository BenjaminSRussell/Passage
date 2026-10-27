package store

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
	prodevents "github.com/BenjaminSRussell/passage/services/production/internal/events"
)

func ptrTo[T any](v T) *T { return &v }

// edit applies edits to the stepper's order at its current version.
func (st *stepper) edit(edits domain.OrderEdits, releaseDuty bool) (Order, error) {
	o, err := st.s.EditOrder(st.ctx, OrderEdit{OrderID: st.orderID, IfMatch: st.version, Edits: edits, ReleaseDuty: releaseDuty,
		Actor: "engineer-1", TraceID: "trace-edit"})
	if err == nil {
		st.version = o.Version
	}
	return o, err
}

func bomEditedEvents(t *testing.T, pool *pgxpool.Pool, orderID string) []prodevents.OrderBOMEditedData {
	t.Helper()
	rows, err := pool.Query(context.Background(), `select payload from outbox where aggregate_id = $1 and type = $2 order by created_at, id`,
		orderID, prodevents.OrderBOMEdited)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []prodevents.OrderBOMEditedData
	for rows.Next() {
		var raw json.RawMessage
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var evt struct {
			Data prodevents.OrderBOMEditedData `json:"data"`
		}
		if err := json.Unmarshal(raw, &evt); err != nil {
			t.Fatal(err)
		}
		out = append(out, evt.Data)
	}
	return out
}

// FR-21.18: edits of an Estimated order re-explode, re-plan and re-estimate, flag the
// order, keep the audit trail and publish order.bomedited; Released needs the duty;
// Started refuses; a line with journals keeps its place after a reset.
func TestEditOrder_FR2118(t *testing.T) {
	pool, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(51))
	in := sampleOrder(t, "key-edit", ids.New())
	allProfile(t, s, ctx, in.LegalEntityID)
	o, _, err := s.CreateOrder(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	st := &stepper{t: t, s: s, ctx: ctx, orderID: o.ID, version: o.Version}
	st.must(domain.CmdEstimate, nil)

	paint, robot := ids.New(), ids.New()
	edited, err := st.edit(domain.OrderEdits{
		Lines: []domain.LineEdit{
			{Op: domain.EditAdd, LineNo: "40", ItemID: &paint, QtyPer: qty("0.5"), UnitCost: qty("4"), Flushing: ptrTo(domain.FlushFinish)},
			{Op: domain.EditChange, LineNo: "10", QtyPer: qty("2.5")},
			{Op: domain.EditRemove, LineNo: "20.1"},
		},
		Operations: []domain.OperationEdit{
			{Op: domain.EditAdd, OperNumber: 20, ResourceID: &robot, RunHPer: qty("0.1"), LaborRate: qty("30"), ScrapPct: qty("5")},
			{Op: domain.EditChange, OperNumber: 10, SetupH: qty("1")},
		},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if edited.Version != 3 || !edited.OrderBOMModified || edited.Status != domain.Estimated || len(edited.Lines) != 2 || len(edited.Operations) != 2 {
		t.Fatalf("edited order: %+v", edited)
	}
	if l := edited.Lines; l[0].LineNo != "10" || !l[0].QtyRequired.Equal(d("250")) || l[1].LineNo != "40" || !l[1].QtyRequired.Equal(d("50")) || l[1].Flushing != domain.FlushFinish {
		t.Fatalf("lines: %+v", l)
	}
	// BR-21.8: operation 20 scraps 5%, so both operations start ⌈100 / 0.95⌉₆ = 105.263158.
	if ops := edited.Operations; ops[1].OperNumber != 20 || !ops[1].ProcessQty.Equal(d("105.263158")) || !ops[0].ProcessQty.Equal(d("105.263158")) || !ops[0].SetupH.Equal(d("1")) {
		t.Fatalf("operations: %+v", ops)
	}
	// Re-estimated: material 250 × 10 + 50 × 4 = 2,700.
	if c := st.costs(); !groupSum(c.Planned, domain.GroupMaterial).Equal(d("2700")) {
		t.Fatalf("planned material after the edit: %+v", c.Planned)
	}

	trail, err := s.OrderEdits(ctx, o.ID)
	if err != nil || len(trail) != 5 {
		t.Fatalf("audit trail: %d %v", len(trail), err)
	}
	removed := trail[2]
	if removed.OrderVersion != 3 || removed.Seq != 3 || removed.Op != domain.EditRemove || removed.Key != "20.1" || string(removed.After) != "null" ||
		removed.Actor != "engineer-1" || removed.TraceID != "trace-edit" {
		t.Fatalf("remove entry: %+v", removed)
	}
	var before domain.OrderLine
	if err := json.Unmarshal(removed.Before, &before); err != nil || !before.QtyRequired.Equal(d("100")) {
		t.Fatalf("before image of the removed line: %s", removed.Before)
	}
	var addedOp domain.Operation
	if err := json.Unmarshal(trail[3].After, &addedOp); err != nil || trail[3].Target != domain.TargetOperation || !addedOp.ProcessQty.Equal(d("105.263158")) {
		t.Fatalf("after image of the added operation: %s", trail[3].After)
	}

	evts := bomEditedEvents(t, pool, o.ID)
	if len(evts) != 1 || evts[0].Version != 3 || len(evts[0].Edits) != 5 || len(evts[0].Lines) != 2 || evts[0].PlannedCost == nil ||
		evts[0].PlannedCost.Material != "2700.00" || evts[0].Lines[1].QtyRequired != "50" {
		t.Fatalf("order.bomedited: %+v", evts)
	}

	// A stale version and a missing line are refused, and nothing changes.
	_, err = s.EditOrder(ctx, OrderEdit{OrderID: o.ID, IfMatch: 2, Edits: domain.OrderEdits{Lines: []domain.LineEdit{{Op: domain.EditRemove, LineNo: "40"}}}, Actor: "x"})
	expectProblem(t, err, http.StatusPreconditionFailed, CodeVersionConflict)
	_, err = st.edit(domain.OrderEdits{Lines: []domain.LineEdit{{Op: domain.EditRemove, LineNo: "99"}}}, false)
	expectProblem(t, err, http.StatusUnprocessableEntity, domain.CodeEditTargetMissing)
	_, err = st.edit(domain.OrderEdits{Lines: []domain.LineEdit{{Op: domain.EditAdd, LineNo: "10", ItemID: &paint, QtyPer: qty("1")}}}, false)
	expectProblem(t, err, http.StatusConflict, domain.CodeEditTargetExists)
	if trail, _ := s.OrderEdits(ctx, o.ID); len(trail) != 5 {
		t.Fatalf("a refused edit was audited: %d rows", len(trail))
	}

	// Released: the after-release duty is required.
	st.must(domain.CmdSchedule, nil)
	st.must(domain.CmdRelease, nil)
	change := domain.OrderEdits{Lines: []domain.LineEdit{{Op: domain.EditChange, LineNo: "40", QtyPer: qty("0.6")}}}
	_, err = st.edit(change, false)
	expectProblem(t, err, http.StatusForbidden, CodeEditAfterRelease)
	if released, err := st.edit(change, true); err != nil || !released.Lines[1].QtyRequired.Equal(d("60")) {
		t.Fatalf("edit after release: %+v %v", released.Lines, err)
	}

	// Started: refused. After consumption on line 10 and a reset to Released, line 10
	// is journaled: it can change quantity but not be removed.
	st.must(domain.CmdStart, nil)
	_, err = st.edit(change, true)
	expectProblem(t, err, http.StatusConflict, domain.CodeEditNotAllowed)
	st.mustPost(st.mustJournal(NewJournal{Type: domain.JournalPickingList, Issues: []IssueInput{{LineNo: "10", Qty: d("5")}}}).ID)
	st.must(domain.CmdResetStatus, func(tr *Transition) { tr.ResetTo = domain.Released })
	_, err = st.edit(domain.OrderEdits{Lines: []domain.LineEdit{{Op: domain.EditRemove, LineNo: "10"}}}, true)
	expectProblem(t, err, http.StatusConflict, domain.CodeEditTargetInUse)
	_, err = st.edit(domain.OrderEdits{Operations: []domain.OperationEdit{{Op: domain.EditRemove, OperNumber: 20}}}, true)
	if err != nil {
		t.Fatalf("an operation without feedback or journals is removable: %v", err)
	}
	if _, err := st.edit(domain.OrderEdits{Lines: []domain.LineEdit{{Op: domain.EditChange, LineNo: "10", QtyPer: qty("3")}}}, true); err != nil {
		t.Fatalf("quantity change on a journaled line: %v", err)
	}

	// FR-21.15 with FR-21.18: a split order carries the edited BOM and the flag.
	res, err := s.SplitOrder(ctx, Split{OrderID: o.ID, IfMatch: st.version, SplitQty: d("40"), Actor: "x", IdempotencyKey: "edit-split", RequestHash: "edit-split"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.New.OrderBOMModified || len(res.New.Lines) != 2 || res.New.Lines[1].LineNo != "40" || !res.New.Lines[1].QtyRequired.Equal(d("24")) {
		t.Fatalf("split of an edited order: %+v", res.New)
	}
}

// A Created order has no estimate: the edit leaves it without one and the event's planned cost is null.
func TestEditOrder_CreatedOrderAndOrderNotFound(t *testing.T) {
	pool, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(52))
	o, _, err := s.CreateOrder(ctx, sampleOrder(t, "key-edit-created", ids.New()))
	if err != nil {
		t.Fatal(err)
	}
	st := &stepper{t: t, s: s, ctx: ctx, orderID: o.ID, version: o.Version}
	if _, err := st.edit(domain.OrderEdits{Lines: []domain.LineEdit{{Op: domain.EditChange, LineNo: "10", ScrapPct: qty("10")}}}, false); err != nil {
		t.Fatal(err)
	}
	if c := st.costs(); len(c.Planned) != 0 {
		t.Fatalf("a Created order was estimated by an edit: %+v", c.Planned)
	}
	if evts := bomEditedEvents(t, pool, o.ID); len(evts) != 1 || evts[0].PlannedCost != nil || evts[0].Lines[0].QtyRequired != "220" {
		t.Fatalf("order.bomedited of a Created order: %+v", evts)
	}
	_, err = s.EditOrder(ctx, OrderEdit{OrderID: ids.New(), IfMatch: 1, Edits: domain.OrderEdits{Lines: []domain.LineEdit{{Op: domain.EditRemove, LineNo: "10"}}}})
	expectProblem(t, err, http.StatusNotFound, CodeOrderNotFound)
	_, err = s.OrderEdits(ctx, ids.New())
	expectProblem(t, err, http.StatusNotFound, CodeOrderNotFound)
}

// PRD 4.21.14: the WIP balance report reconciles to the subledger before and
// after End, and the variance report carries the BR-21.7 variances with their
// share of planned cost.
func TestReports_WIPAndVariances(t *testing.T) {
	pool, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(53))
	in := sampleOrder(t, "key-report-a", ids.New())
	st := startedOrder(t, s, ctx, in)
	st.mustPost(st.mustJournal(NewJournal{Type: domain.JournalPickingList, Issues: []IssueInput{
		{LineNo: "10", Qty: d("203"), UnitCost: qty("10.20")}, {LineNo: "20.1", Qty: d("102"), UnitCost: qty("10.20")}}}).ID)
	st.mustPost(st.mustJournal(NewJournal{Type: domain.JournalRouteCard, Hours: []HoursInput{{OperNumber: 10, Hours: d("46"), QtyGood: d("100")}}}).ID)
	st.must(domain.CmdReportFinished, func(tr *Transition) { tr.Good = d("100") })

	other := sampleOrder(t, "key-report-b", in.LegalEntityID)
	o2, _, err := s.CreateOrder(ctx, other)
	if err != nil {
		t.Fatal(err)
	}
	st2 := &stepper{t: t, s: s, ctx: ctx, orderID: o2.ID, version: o2.Version}
	for _, cmd := range []domain.Command{domain.CmdEstimate, domain.CmdSchedule, domain.CmdRelease, domain.CmdStart} {
		st2.must(cmd, nil)
	}
	st2.mustPost(st2.mustJournal(NewJournal{Type: domain.JournalPickingList, Issues: []IssueInput{{LineNo: "10", Qty: d("10")}}}).ID)
	// A Created order has no WIP rows and is not listed.
	if _, _, err := s.CreateOrder(ctx, sampleOrder(t, "key-report-c", in.LegalEntityID)); err != nil {
		t.Fatal(err)
	}

	today := time.Now().UTC().Format(time.DateOnly)
	rep, err := s.WIPReport(ctx, WIPFilter{AsOf: today})
	if err != nil || len(rep.Items) != 2 || len(rep.Totals) != 1 {
		t.Fatalf("WIP report: %+v %v", rep, err)
	}
	byOrder := map[string]WIPRow{}
	for _, r := range rep.Items {
		byOrder[r.OrderID] = r
	}
	// BR-21.7: 3,111 material + 1,380 labor + 460 overhead into WIP, 5,000 received, −49 left.
	a := byOrder[st.orderID]
	if !a.Consumption.Equal(d("4951")) || !a.RAFValue.Equal(d("5000")) || !a.Settled.IsZero() || !a.Balance.Equal(d("-49")) || a.Status != domain.ReportedAsFinished {
		t.Fatalf("order A before End: %+v", a)
	}
	if b := byOrder[o2.ID]; !b.Consumption.Equal(d("100")) || !b.Balance.Equal(d("100")) {
		t.Fatalf("order B: %+v", b)
	}
	if tot := rep.Totals[0]; tot.Orders != 2 || tot.Currency != "USD" || !tot.Balance.Equal(d("51")) {
		t.Fatalf("totals: %+v", tot)
	}
	yesterday := time.Now().UTC().AddDate(0, 0, -1).Format(time.DateOnly)
	if rep, err := s.WIPReport(ctx, WIPFilter{AsOf: yesterday}); err != nil || len(rep.Items) != 0 || len(rep.Totals) != 0 {
		t.Fatalf("nothing was posted before today: %+v %v", rep, err)
	}
	page, err := s.WIPReport(ctx, WIPFilter{AsOf: today, Limit: 1})
	if err != nil || len(page.Items) != 1 || page.NextCursor == "" || page.Totals[0].Orders != 2 {
		t.Fatalf("first page: %+v %v", page, err)
	}
	if next, err := s.WIPReport(ctx, WIPFilter{AsOf: today, Limit: 1, Cursor: page.NextCursor}); err != nil || len(next.Items) != 1 ||
		next.Items[0].OrderID == page.Items[0].OrderID || next.NextCursor != "" {
		t.Fatalf("second page: %+v %v", next, err)
	}

	st.must(domain.CmdEnd, nil)
	rep, err = s.WIPReport(ctx, WIPFilter{AsOf: today, Status: domain.Ended})
	if err != nil || len(rep.Items) != 1 || !rep.Items[0].Settled.Equal(d("-49")) || !rep.Items[0].Balance.IsZero() {
		t.Fatalf("after End: %+v %v", rep.Items, err)
	}
	// The report's balance is the subledger's WIP balance.
	if c := st.costs(); !c.WIPBalance.Equal(rep.Items[0].Balance) {
		t.Fatalf("report %s, subledger %s", rep.Items[0].Balance, c.WIPBalance)
	}
	if net := ledgerNet(t, pool, st.orderID); !net.IsZero() {
		t.Fatalf("subledger: %s", net)
	}

	vars, err := s.VarianceReport(ctx, VarianceFilter{From: today, To: today})
	if err != nil || len(vars.Items) != 4 {
		t.Fatalf("variance report: %+v %v", vars, err)
	}
	want := map[string]string{"production-quantity/material": "1.67", "production-price/material": "2.03",
		"production-quantity/labor": "-8", "production-quantity/overhead": "-8"}
	for _, v := range vars.Items {
		key := string(v.Type) + "/" + string(v.Group)
		if w, ok := want[key]; !ok || v.PctOfPlanned == nil || !v.PctOfPlanned.Equal(d(w)) || v.OrderID != st.orderID || v.PostedOn != today {
			t.Errorf("%s: %+v (pct %v), want %s%%", key, v, v.PctOfPlanned, w)
		}
	}
	total := d("0")
	for _, tt := range vars.Totals {
		total = total.Add(tt.Amount)
	}
	if !total.Equal(d("-49")) || len(vars.Totals) != 2 {
		t.Fatalf("totals: %+v", vars.Totals)
	}
	q, err := s.VarianceReport(ctx, VarianceFilter{Type: "production-quantity", Limit: 2})
	if err != nil || len(q.Items) != 2 || q.NextCursor == "" || len(q.Totals) != 1 || !q.Totals[0].Amount.Equal(d("-110")) {
		t.Fatalf("quantity variances, first page: %+v %v", q, err)
	}
	if rest, err := s.VarianceReport(ctx, VarianceFilter{Type: "production-quantity", Limit: 2, Cursor: q.NextCursor}); err != nil || len(rest.Items) != 1 {
		t.Fatalf("quantity variances, second page: %+v %v", rest, err)
	}
	tomorrow := time.Now().UTC().AddDate(0, 0, 1).Format(time.DateOnly)
	if none, err := s.VarianceReport(ctx, VarianceFilter{From: tomorrow}); err != nil || len(none.Items) != 0 {
		t.Fatalf("from tomorrow: %+v %v", none, err)
	}
	if none, err := s.VarianceReport(ctx, VarianceFilter{OrderID: o2.ID}); err != nil || len(none.Items) != 0 {
		t.Fatalf("order B has no variances: %+v %v", none, err)
	}
}
