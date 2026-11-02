package store

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
	prodevents "github.com/BenjaminSRussell/passage/services/production/internal/events"
)

// orderOf builds a NewOrder for product with the given BOM and one operation of
// runHPer hours per unit at 30.00 labor and no overhead.
func orderOf(t *testing.T, key, legalEntity, product string, quantity string, bom []domain.BOMComponent, runHPer string) NewOrder {
	t.Helper()
	lines, err := domain.Explode(bom, d(quantity))
	if err != nil {
		t.Fatal(err)
	}
	ops, err := domain.PlanOperations([]domain.Operation{{OperNumber: 10, ResourceID: ids.New(), RunHPer: d(runHPer), LaborRate: d("30")}}, d(quantity))
	if err != nil {
		t.Fatal(err)
	}
	return NewOrder{LegalEntityID: legalEntity, ProductID: product, SiteID: ids.New(), WarehouseID: ids.New(), Currency: "USD",
		Quantity: d(quantity), OriginType: "Manual", Lines: lines, Operations: ops, IdempotencyKey: key, RequestHash: "hash-" + key}
}

func wantProblem(t *testing.T, err error, status int, code string) {
	t.Helper()
	if s, c := problemStatus(err); s != status || c != code {
		t.Fatalf("want %d %s, got %v", status, code, err)
	}
}

// AC-21.15 (FR-21.20): a rework order for 5 units of FG-100 with 2.00 h of
// rework at 30.00/h posts a rework variance of 60.00 when ended, and the 5
// units are received at their existing unit cost (42.00, issued into WIP on
// the order's line for the product itself).
func TestRework_AC2115(t *testing.T) {
	pool, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(61))
	le, fg := ids.New(), ids.New()

	original := startedOrder(t, s, ctx, orderOf(t, "rw-original", le, fg, "10",
		[]domain.BOMComponent{{LineNo: "10", ItemID: ids.New(), QtyPer: d("1"), UnitCost: d("40")}}, "0.1"))
	original.must(domain.CmdReportFinished, func(tr *Transition) { tr.Good = d("8") })

	ref := &domain.ReworkRef{Type: domain.ReworkRefOrder, ID: original.orderID}
	rework := func(key, product, quantity string) NewOrder {
		in := orderOf(t, key, le, product, quantity, []domain.BOMComponent{{LineNo: "10", ItemID: fg, QtyPer: d("1"), UnitCost: d("42")}}, "0.4")
		in.OriginType, in.ReworkReference = domain.OriginRework, ref
		return in
	}

	// The reference must name an order of the same product that reported enough.
	_, _, err := s.CreateOrder(ctx, rework("rw-other-product", ids.New(), "5"))
	wantProblem(t, err, http.StatusUnprocessableEntity, domain.CodeReworkReference)
	_, _, err = s.CreateOrder(ctx, rework("rw-too-many", fg, "9"))
	wantProblem(t, err, http.StatusUnprocessableEntity, domain.CodeReworkReference)
	missing := rework("rw-missing", fg, "5")
	missing.ReworkReference = &domain.ReworkRef{Type: domain.ReworkRefOrder, ID: ids.New()}
	_, _, err = s.CreateOrder(ctx, missing)
	wantProblem(t, err, http.StatusUnprocessableEntity, domain.CodeReworkReference)

	o, created, err := s.CreateOrder(ctx, rework("rw-1", fg, "5"))
	if err != nil || !created {
		t.Fatalf("create rework order: %v", err)
	}
	if o.OriginType != domain.OriginRework || o.ReworkReference == nil || *o.ReworkReference != *ref {
		t.Fatalf("rework order: origin %s reference %+v", o.OriginType, o.ReworkReference)
	}
	page, err := s.ListOrders(ctx, ListFilter{ReworkOf: original.orderID, Limit: 10})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != o.ID || page.Items[0].OriginType != domain.OriginRework ||
		page.Items[0].ReworkReference == nil {
		t.Fatalf("rework orders of the original: %+v %v", page.Items, err)
	}
	var created1 json.RawMessage
	if err := pool.QueryRow(context.Background(), `select payload from outbox where aggregate_id = $1 and type = $2`, o.ID, prodevents.OrderCreated).Scan(&created1); err != nil {
		t.Fatal(err)
	}
	var evt struct {
		Data prodevents.OrderCreatedData `json:"data"`
	}
	if err := json.Unmarshal(created1, &evt); err != nil || evt.Data.ReworkReference == nil || evt.Data.ReworkReference.ID != original.orderID ||
		evt.Data.OriginType != domain.OriginRework {
		t.Fatalf("order.created: %s", created1)
	}

	st := &stepper{t: t, s: s, ctx: ctx, orderID: o.ID, version: o.Version}
	for _, cmd := range []domain.Command{domain.CmdEstimate, domain.CmdSchedule, domain.CmdRelease, domain.CmdStart} {
		st.must(cmd, nil)
	}
	st.mustPost(st.mustJournal(NewJournal{Type: domain.JournalPickingList, Issues: []IssueInput{{LineNo: "10", Qty: d("5")}}}).ID)
	st.mustPost(st.mustJournal(NewJournal{Type: domain.JournalRouteCard, Hours: []HoursInput{{OperNumber: 10, Hours: d("2"), QtyGood: d("5")}}}).ID)
	st.must(domain.CmdReportFinished, func(tr *Transition) { tr.Good = d("5") })

	js, err := s.ListJournals(ctx, st.orderID)
	if err != nil {
		t.Fatal(err)
	}
	var receipt decimal.Decimal
	for _, j := range js {
		for _, l := range j.Lines {
			if l.Kind == KindReceipt {
				receipt = l.Amount
			}
		}
	}
	if !receipt.Equal(d("210")) {
		t.Fatalf("the 5 units are received at %s, want 5 × 42.00 = 210.00", receipt)
	}
	if c := st.costs(); !c.WIPBalance.Equal(d("60")) {
		t.Fatalf("WIP before End = %s, want the 60.00 of rework", c.WIPBalance)
	}

	// Ending needs a rework variance account on the profile.
	_, err = st.step(domain.CmdEnd, nil)
	wantProblem(t, err, http.StatusUnprocessableEntity, domain.CodeNoPostingProfile)
	prof := testProfile(domain.AllKey, domain.AllKey, "1450")
	prof.VarRework = "5650"
	if _, _, err := s.SavePostingProfile(ctx, ProfileSave{LegalEntityID: le, Profile: prof, IfMatch: 1, Actor: "admin"}); err != nil {
		t.Fatal(err)
	}
	st.must(domain.CmdEnd, nil)

	after := st.costs()
	if len(after.Variances) != 1 || after.Variances[0].Type != domain.VarRework || after.Variances[0].Group != domain.GroupLabor ||
		!after.Variances[0].Amount.Equal(d("60")) || !after.WIPBalance.IsZero() {
		t.Fatalf("settlement: %+v, WIP %s", after.Variances, after.WIPBalance)
	}
	if net := ledgerNet(t, pool, st.orderID); !net.IsZero() {
		t.Fatalf("the WIP subledger does not balance: %s", net)
	}
	var account, dr string
	if err := pool.QueryRow(context.Background(), `select ledger_account, amount_dr::text from production_wip_posting
		where order_id = $1 and posting_type = 'variance-rework'`, st.orderID).Scan(&account, &dr); err != nil {
		t.Fatal(err)
	}
	if account != "5650" || !d(dr).Equal(d("60")) {
		t.Fatalf("rework variance posting: %s %s", account, dr)
	}
}

// A rework order without its own product on the BOM receives nothing: the
// units never left inventory, and all cost settles as rework.
func TestRework_WithoutProductLineReceivesNothing(t *testing.T) {
	_, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(62))
	le := ids.New()
	in := orderOf(t, "rw-batch", le, ids.New(), "4", []domain.BOMComponent{{LineNo: "10", ItemID: ids.New(), QtyPer: d("1"), UnitCost: d("3")}}, "0.25")
	in.OriginType, in.ReworkReference = domain.OriginRework, &domain.ReworkRef{Type: domain.ReworkRefBatch, ID: "B-2026-07"}
	prof := testProfile(domain.AllKey, domain.AllKey, "1450")
	prof.VarRework = "5650"
	if _, _, err := s.SavePostingProfile(ctx, ProfileSave{LegalEntityID: le, Profile: prof, Actor: "admin"}); err != nil {
		t.Fatal(err)
	}
	o, _, err := s.CreateOrder(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	st := &stepper{t: t, s: s, ctx: ctx, orderID: o.ID, version: o.Version}
	for _, cmd := range []domain.Command{domain.CmdEstimate, domain.CmdSchedule, domain.CmdRelease, domain.CmdStart} {
		st.must(cmd, nil)
	}
	st.mustPost(st.mustJournal(NewJournal{Type: domain.JournalPickingList, Issues: []IssueInput{{LineNo: "10", Qty: d("4")}}}).ID)
	st.mustPost(st.mustJournal(NewJournal{Type: domain.JournalRouteCard, Hours: []HoursInput{{OperNumber: 10, Hours: d("1")}}}).ID)
	st.must(domain.CmdReportFinished, func(tr *Transition) { tr.Good = d("4") })
	if c := st.costs(); !c.WIPBalance.Equal(d("42")) {
		t.Fatalf("WIP after RAF = %s, want 12.00 material + 30.00 labor", c.WIPBalance)
	}
	st.must(domain.CmdEnd, nil)
	got := map[domain.CostGroup]string{}
	for _, v := range st.costs().Variances {
		if v.Type != domain.VarRework {
			t.Fatalf("variance type %s on a rework order", v.Type)
		}
		got[v.Group] = v.Amount.String()
	}
	if len(got) != 2 || got[domain.GroupMaterial] != "12" || got[domain.GroupLabor] != "30" {
		t.Fatalf("rework variances by group: %v", got)
	}
}

// AC-21.14 and BR-21.14: as-built of a finished serial and where-used of a
// component serial; a component serial belongs to one finished serial; the
// assigned quantity stays within the consumption; a reset that reverses the
// reports as finished detaches the records.
func TestAsBuilt_AC2114(t *testing.T) {
	pool, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(63))
	le, motor, bolt := ids.New(), ids.New(), ids.New()
	bom := []domain.BOMComponent{
		{LineNo: "10", ItemID: motor, QtyPer: d("1"), UnitCost: d("100")},
		{LineNo: "20", ItemID: bolt, QtyPer: d("4"), UnitCost: d("0.5")},
	}
	created, _, err := s.CreateOrder(ctx, orderOf(t, "ab-created", le, ids.New(), "2", bom, "1"))
	if err != nil {
		t.Fatal(err)
	}
	assign := func(orderID, key, finished, item string, tracking domain.Tracking, component, q string) (AsBuiltRecord, bool, error) {
		return s.AssignAsBuilt(ctx, NewAsBuilt{OrderID: orderID, Actor: "operator-1", IdempotencyKey: key, RequestHash: "h-" + key + finished + component + q,
			AsBuilt: domain.AsBuilt{FinishedSerial: finished, ComponentItemID: item, Tracking: tracking, ComponentSerial: component, Qty: d(q)}})
	}
	_, _, err = assign(created.ID, "ab-0", "S-1", motor, domain.TrackSerial, "M-1", "1")
	wantProblem(t, err, http.StatusConflict, domain.CodeAsBuiltState)

	st := startedOrder(t, s, ctx, orderOf(t, "ab-order", le, ids.New(), "2", bom, "1"))
	_, _, err = assign(st.orderID, "ab-early", "S-1", motor, domain.TrackSerial, "M-1", "1")
	wantProblem(t, err, http.StatusUnprocessableEntity, domain.CodeAsBuiltExceedsConsumed)
	st.mustPost(st.mustJournal(NewJournal{Type: domain.JournalPickingList, Issues: []IssueInput{{LineNo: "10", Qty: d("2")}, {LineNo: "20", Qty: d("8")}}}).ID)

	m1, fresh, err := assign(st.orderID, "ab-1", "S-1", motor, domain.TrackSerial, "M-1", "1")
	if err != nil || !fresh || m1.OrderNumber == "" || m1.Tracking != domain.TrackSerial || m1.AssignedBy != "operator-1" {
		t.Fatalf("assign M-1: %+v %v", m1, err)
	}
	if replay, again, err := assign(st.orderID, "ab-1", "S-1", motor, domain.TrackSerial, "M-1", "1"); err != nil || again || replay.ID != m1.ID {
		t.Fatalf("replay: %+v created=%v %v", replay, again, err)
	}
	_, _, err = assign(st.orderID, "ab-1", "S-2", motor, domain.TrackSerial, "M-2", "1")
	wantProblem(t, err, http.StatusUnprocessableEntity, CodeIdempotencyReuse)
	m2, _, err := assign(st.orderID, "ab-2", "S-2", motor, domain.TrackSerial, "M-2", "1")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := assign(st.orderID, "ab-3", "S-1", bolt, domain.TrackBatch, "B-1", "4"); err != nil {
		t.Fatal(err)
	}

	asBuilt, err := s.ListAsBuilt(ctx, AsBuiltFilter{FinishedSerialOrBatch: "S-2"})
	if err != nil || len(asBuilt) != 1 || asBuilt[0].ComponentSerialOrBatch != "M-2" {
		t.Fatalf("as-built of S-2: %+v %v", asBuilt, err)
	}
	whereUsed, err := s.ListAsBuilt(ctx, AsBuiltFilter{ComponentSerialOrBatch: "M-1"})
	if err != nil || len(whereUsed) != 1 || whereUsed[0].FinishedSerialOrBatch != "S-1" || whereUsed[0].OrderID != st.orderID {
		t.Fatalf("where-used of M-1: %+v %v", whereUsed, err)
	}

	_, _, err = assign(st.orderID, "ab-4", "S-1", motor, domain.TrackSerial, "M-3", "1")
	wantProblem(t, err, http.StatusUnprocessableEntity, domain.CodeAsBuiltExceedsConsumed)
	_, _, err = assign(st.orderID, "ab-5", "S-3", bolt, domain.TrackBatch, "B-1", "1")
	wantProblem(t, err, http.StatusUnprocessableEntity, domain.CodeAsBuiltFinishedCount)
	_, _, err = assign(st.orderID, "ab-6", "S-1", ids.New(), domain.TrackSerial, "X-1", "1")
	wantProblem(t, err, http.StatusUnprocessableEntity, domain.CodeAsBuiltNotOnBOM)

	// Detaching M-2 frees one motor of the consumption; M-1 is still taken.
	detached, err := s.DetachAsBuilt(ctx, m2.ID, "operator-1")
	if err != nil || detached.DetachedAt == nil || detached.DetachedBy == nil {
		t.Fatalf("detach: %+v %v", detached, err)
	}
	if again, err := s.DetachAsBuilt(ctx, m2.ID, "operator-2"); err != nil || *again.DetachedBy != "operator-1" {
		t.Fatalf("second detach: %+v %v", again, err)
	}
	_, _, err = assign(st.orderID, "ab-7", "S-2", motor, domain.TrackSerial, "M-1", "1")
	wantProblem(t, err, http.StatusUnprocessableEntity, domain.CodeSerialDuplicate)
	if _, _, err := assign(st.orderID, "ab-8", "S-2", motor, domain.TrackSerial, "M-2", "1"); err != nil {
		t.Fatalf("M-2 reassigned after detach: %v", err)
	}

	st.must(domain.CmdReportFinished, func(tr *Transition) { tr.Good = d("2") })
	st.must(domain.CmdResetStatus, func(tr *Transition) { tr.ResetTo = domain.Started })
	left, err := s.ListAsBuilt(ctx, AsBuiltFilter{OrderID: st.orderID})
	if err != nil || len(left) != 0 {
		t.Fatalf("after the RAF reversal: %+v %v", left, err)
	}
	counts := map[string]int{}
	for _, typ := range outboxTypes(t, pool, st.orderID) {
		counts[typ]++
	}
	if counts[prodevents.AsBuiltAssigned] != 4 || counts[prodevents.AsBuiltDetached] != 4 {
		t.Fatalf("as-built events: %v", counts)
	}
	_, err = s.DetachAsBuilt(ctx, ids.New(), "operator-1")
	wantProblem(t, err, http.StatusNotFound, CodeAsBuiltNotFound)
}

// A component serial is unique across the orders of a tenant, and another
// tenant neither sees nor blocks it.
func TestAsBuilt_SerialAcrossOrdersAndTenants(t *testing.T) {
	_, s := migrated(t)
	motor := ids.New()
	bom := []domain.BOMComponent{{LineNo: "10", ItemID: motor, QtyPer: d("1"), UnitCost: d("100")}}
	run := func(tenantNo int, key string) (*stepper, context.Context) {
		ctx := tenantCtx(testkit.TenantID(tenantNo))
		st := startedOrder(t, s, ctx, orderOf(t, key, ids.New(), ids.New(), "1", bom, "1"))
		st.mustPost(st.mustJournal(NewJournal{Type: domain.JournalPickingList, Issues: []IssueInput{{LineNo: "10", Qty: d("1")}}}).ID)
		return st, ctx
	}
	assign := func(ctx context.Context, orderID, key string) error {
		_, _, err := s.AssignAsBuilt(ctx, NewAsBuilt{OrderID: orderID, Actor: "operator-1", IdempotencyKey: key, RequestHash: key,
			AsBuilt: domain.AsBuilt{FinishedSerial: "S-" + key, ComponentItemID: motor, Tracking: domain.TrackSerial, ComponentSerial: "M-1", Qty: d("1")}})
		return err
	}
	a, ctxA := run(64, "ser-a")
	b, _ := run(64, "ser-b")
	c, ctxC := run(65, "ser-c")
	if err := assign(ctxA, a.orderID, "a"); err != nil {
		t.Fatal(err)
	}
	wantProblem(t, assign(ctxA, b.orderID, "b"), http.StatusUnprocessableEntity, domain.CodeSerialDuplicate)
	if err := assign(ctxC, c.orderID, "c"); err != nil {
		t.Fatalf("another tenant's M-1: %v", err)
	}
	if rows, err := s.ListAsBuilt(ctxC, AsBuiltFilter{ComponentSerialOrBatch: "M-1"}); err != nil || len(rows) != 1 || rows[0].OrderID != c.orderID {
		t.Fatalf("tenant 65 where-used: %+v %v", rows, err)
	}
}

// rework.capitalize (PRD 4.21.13): the 60.00 of rework cost of the AC-21.15 order moves into
// finished goods at End (Dr 1410 / Cr WIP), no variance is stored, WIP is zero and order.settled
// says how much was capitalized. A second End is refused and posts nothing, and switching the
// parameter off afterwards leaves the settlement as it was (TestRework_AC2115 is the default).
func TestRework_CapitalizeMovesTheCostIntoFinishedGoods(t *testing.T) {
	pool, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(63))
	le, fg := ids.New(), ids.New()
	saveParams(t, s, ctx, le, func(p *domain.Parameters) { p.ReworkCapitalize = true }, domain.AllKey, 0)

	original := startedOrder(t, s, ctx, orderOf(t, "rwc-original", le, fg, "10",
		[]domain.BOMComponent{{LineNo: "10", ItemID: ids.New(), QtyPer: d("1"), UnitCost: d("40")}}, "0.1"))
	original.must(domain.CmdReportFinished, func(tr *Transition) { tr.Good = d("8") })
	in := orderOf(t, "rwc-1", le, fg, "5", []domain.BOMComponent{{LineNo: "10", ItemID: fg, QtyPer: d("1"), UnitCost: d("42")}}, "0.4")
	in.OriginType, in.ReworkReference = domain.OriginRework, &domain.ReworkRef{Type: domain.ReworkRefOrder, ID: original.orderID}
	o, _, err := s.CreateOrder(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	st := &stepper{t: t, s: s, ctx: ctx, orderID: o.ID, version: o.Version}
	for _, cmd := range []domain.Command{domain.CmdEstimate, domain.CmdSchedule, domain.CmdRelease, domain.CmdStart} {
		st.must(cmd, nil)
	}
	st.mustPost(st.mustJournal(NewJournal{Type: domain.JournalPickingList, Issues: []IssueInput{{LineNo: "10", Qty: d("5")}}}).ID)
	st.mustPost(st.mustJournal(NewJournal{Type: domain.JournalRouteCard, Hours: []HoursInput{{OperNumber: 10, Hours: d("2"), QtyGood: d("5")}}}).ID)
	st.must(domain.CmdReportFinished, func(tr *Transition) { tr.Good = d("5") })
	// The rework variance account is not needed: nothing is posted as a variance.
	st.must(domain.CmdEnd, nil)

	after := st.costs()
	if len(after.Variances) != 0 || !after.WIPBalance.IsZero() {
		t.Fatalf("settlement: %+v, WIP %s", after.Variances, after.WIPBalance)
	}
	if net := ledgerNet(t, pool, st.orderID); !net.IsZero() {
		t.Fatalf("the WIP subledger does not balance: %s", net)
	}
	var account, dr string
	if err := pool.QueryRow(context.Background(), `select ledger_account, amount_dr::text from production_wip_posting
		where order_id = $1 and posting_type = 'rework-capitalized'`, st.orderID).Scan(&account, &dr); err != nil {
		t.Fatal(err)
	}
	if account != "1410" || !d(dr).Equal(d("60")) {
		t.Fatalf("capitalization posting: %s %s", account, dr)
	}
	var raw json.RawMessage
	if err := pool.QueryRow(context.Background(), `select payload from outbox where aggregate_id = $1 and type = $2`, o.ID, prodevents.OrderSettled).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var evt struct {
		Data prodevents.OrderSettledData `json:"data"`
	}
	if err := json.Unmarshal(raw, &evt); err != nil || evt.Data.ReworkCapitalized != "60" || len(evt.Data.Variances) != 0 || evt.Data.WIPCleared != "60" {
		t.Fatalf("order.settled: %s %v", raw, err)
	}

	// The settlement is final: a second End is refused and posts nothing more, and the
	// parameter as of End decides, not its later value.
	var rows int
	countPostings := func() int {
		t.Helper()
		if err := pool.QueryRow(context.Background(), `select count(*) from production_wip_posting where order_id = $1`, st.orderID).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		return rows
	}
	before := countPostings()
	_, err = st.step(domain.CmdEnd, nil)
	wantProblem(t, err, http.StatusConflict, domain.CodeIllegalTransition)
	saveParams(t, s, ctx, le, nil, domain.AllKey, 1)
	if after := st.costs(); countPostings() != before || len(after.Variances) != 0 || !after.WIPBalance.IsZero() {
		t.Fatalf("after a second End and the parameter off: %d postings (was %d), %+v", rows, before, after.Variances)
	}
}

// A rework order ended without output has no units to carry the cost: with rework.capitalize
// on, its WIP balance still settles as rework variances and nothing goes to finished goods.
func TestRework_CapitalizeWithoutOutputIsExpensed(t *testing.T) {
	pool, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(64))
	le := ids.New()
	saveParams(t, s, ctx, le, func(p *domain.Parameters) { p.ReworkCapitalize = true }, domain.AllKey, 0)
	prof := testProfile(domain.AllKey, domain.AllKey, "1450")
	prof.VarRework = "5650"
	if _, _, err := s.SavePostingProfile(ctx, ProfileSave{LegalEntityID: le, Profile: prof, Actor: "admin"}); err != nil {
		t.Fatal(err)
	}
	in := orderOf(t, "rwc-zero", le, ids.New(), "4", []domain.BOMComponent{{LineNo: "10", ItemID: ids.New(), QtyPer: d("1"), UnitCost: d("3")}}, "0.25")
	in.OriginType, in.ReworkReference = domain.OriginRework, &domain.ReworkRef{Type: domain.ReworkRefBatch, ID: "B-2026-08"}
	o, _, err := s.CreateOrder(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	st := &stepper{t: t, s: s, ctx: ctx, orderID: o.ID, version: o.Version}
	for _, cmd := range []domain.Command{domain.CmdEstimate, domain.CmdSchedule, domain.CmdRelease, domain.CmdStart} {
		st.must(cmd, nil)
	}
	st.mustPost(st.mustJournal(NewJournal{Type: domain.JournalRouteCard, Hours: []HoursInput{{OperNumber: 10, Hours: d("1")}}}).ID)
	st.must(domain.CmdEnd, func(tr *Transition) { tr.ZeroOutputDuty = true })

	after := st.costs()
	if len(after.Variances) != 1 || after.Variances[0].Type != domain.VarRework || !after.Variances[0].Amount.Equal(d("30")) || !after.WIPBalance.IsZero() {
		t.Fatalf("settlement: %+v, WIP %s", after.Variances, after.WIPBalance)
	}
	var capitalized int
	if err := pool.QueryRow(context.Background(), `select count(*) from production_wip_posting
		where order_id = $1 and posting_type = 'rework-capitalized'`, st.orderID).Scan(&capitalized); err != nil || capitalized != 0 {
		t.Fatalf("capitalization postings without output: %d %v", capitalized, err)
	}
}
