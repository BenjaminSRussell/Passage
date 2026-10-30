package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
	prodevents "github.com/BenjaminSRussell/passage/services/production/internal/events"
	"github.com/BenjaminSRussell/passage/services/production/migrations"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// tenantCtx carries a principal with no legal-entity scope, so RLS shows every legal entity.
func tenantCtx(tid string) context.Context {
	return scopedCtx(tid, "")
}

func scopedCtx(tid, legalEntity string) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: tid, LegalEntityID: legalEntity, UserID: "planner-1", Duties: testkit.ScopeDuties(legalEntity)})
}

func migrated(t *testing.T) (*pgxpool.Pool, *Postgres) {
	t.Helper()
	pool := testkit.DB(t)
	if err := dbx.Migrate(context.Background(), pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	return pool, New(pool)
}

// sampleOrder is the BR-21.7 shape: 100 units, 3 kg per unit at 10.00, one
// operation of 0.5 h per unit at 30.00 labor and 10.00 overhead. The same raw
// material also sits under a phantom, so the estimate must merge it.
func sampleOrder(t *testing.T, key, legalEntity string) NewOrder {
	t.Helper()
	rm := ids.New()
	bom := []domain.BOMComponent{
		{LineNo: "10", ItemID: rm, QtyPer: d("2"), UnitCost: d("10")},
		{LineNo: "20", Phantom: true, QtyPer: d("1"), Children: []domain.BOMComponent{{ItemID: rm, QtyPer: d("1"), UnitCost: d("10")}}},
	}
	lines, err := domain.Explode(bom, d("100"))
	if err != nil {
		t.Fatal(err)
	}
	ops, err := domain.PlanOperations([]domain.Operation{{OperNumber: 10, ResourceID: ids.New(), RunHPer: d("0.5"), LaborRate: d("30"), OverheadRate: d("10")}}, d("100"))
	if err != nil {
		t.Fatal(err)
	}
	date := "2026-11-30"
	return NewOrder{LegalEntityID: legalEntity, ProductID: ids.New(), SiteID: ids.New(), WarehouseID: ids.New(), Currency: "USD",
		Quantity: d("100"), DeliveryDate: &date, OriginType: "Manual", Lines: lines, Operations: ops, IdempotencyKey: key, RequestHash: "hash-" + key}
}

func problemStatus(err error) (int, string) {
	var p *httpx.Problem
	if errors.As(err, &p) {
		return p.Status, p.Code
	}
	return 0, ""
}

func expectProblem(t *testing.T, err error, status int, code string) {
	t.Helper()
	if gotStatus, gotCode := problemStatus(err); gotStatus != status || gotCode != code {
		t.Fatalf("want %d %s, got %v", status, code, err)
	}
}

type stepper struct {
	t       *testing.T
	s       *Postgres
	ctx     context.Context
	orderID string
	version int
	n       int
}

// step applies a command with a fresh idempotency key and the current version.
func (st *stepper) step(cmd domain.Command, mut func(*Transition)) (Order, error) {
	st.n++
	tr := Transition{OrderID: st.orderID, IfMatch: st.version, Command: cmd, Actor: "planner-1", TraceID: "trace-1",
		IdempotencyKey: st.orderID + "-" + string(cmd) + "-" + string(rune('a'+st.n)), RequestHash: string(cmd)}
	if mut != nil {
		mut(&tr)
	}
	o, _, err := st.s.Transition(st.ctx, tr)
	if err == nil {
		st.version = o.Version
	}
	return o, err
}

func (st *stepper) must(cmd domain.Command, mut func(*Transition)) Order {
	st.t.Helper()
	o, err := st.step(cmd, mut)
	if err != nil {
		st.t.Fatalf("%s: %v", cmd, err)
	}
	return o
}

func outboxTypes(t *testing.T, pool *pgxpool.Pool, orderID string) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `select type from outbox where aggregate_id = $1 order by created_at, id`, orderID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var typ string
		if err := rows.Scan(&typ); err != nil {
			t.Fatal(err)
		}
		out = append(out, typ)
	}
	return out
}

func TestStore_CreateAndIdempotency(t *testing.T) {
	pool, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(21))
	in := sampleOrder(t, "key-create-1", ids.New())
	o, created, err := s.CreateOrder(ctx, in)
	if err != nil || !created {
		t.Fatalf("create: created=%v err=%v", created, err)
	}
	if o.Status != domain.Created || o.OrderNumber != "PO-000001" || o.Version != 1 || len(o.Lines) != 2 || len(o.Operations) != 1 {
		t.Fatalf("unexpected order: %+v", o)
	}
	if o.Lines[0].LineNo != "10" || o.Lines[1].LineNo != "20.1" || o.DeliveryDate == nil || *o.DeliveryDate != "2026-11-30" {
		t.Fatalf("lines or date: %+v %v", o.Lines, o.DeliveryDate)
	}

	replay, created, err := s.CreateOrder(ctx, in)
	if err != nil || created || replay.ID != o.ID {
		t.Fatalf("replay: created=%v id=%s err=%v", created, replay.ID, err)
	}
	other := in
	other.RequestHash = "another body"
	_, _, err = s.CreateOrder(ctx, other)
	expectProblem(t, err, http.StatusUnprocessableEntity, CodeIdempotencyReuse)

	second, _, err := s.CreateOrder(ctx, sampleOrder(t, "key-create-2", in.LegalEntityID))
	if err != nil || second.OrderNumber != "PO-000002" {
		t.Fatalf("second number: %s %v", second.OrderNumber, err)
	}
	third, _, err := s.CreateOrder(ctx, sampleOrder(t, "key-create-3", ids.New()))
	if err != nil || third.OrderNumber != "PO-000001" {
		t.Fatalf("numbering is per legal entity, got %s %v", third.OrderNumber, err)
	}

	if types := outboxTypes(t, pool, o.ID); len(types) != 1 || types[0] != prodevents.OrderCreated {
		t.Fatalf("outbox after create and replay: %v", types)
	}
	var payload json.RawMessage
	if err := pool.QueryRow(context.Background(), `select payload from outbox where aggregate_id = $1`, o.ID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var evt struct {
		Type     string `json:"type"`
		TenantID string `json:"tenantid"`
		Actor    string `json:"actor"`
		Data     struct {
			OrderNumber string `json:"orderNumber"`
			Quantity    string `json:"quantity"`
		} `json:"data"`
	}
	if err := json.Unmarshal(payload, &evt); err != nil || evt.TenantID != testkit.TenantID(21) || evt.Actor != "planner-1" ||
		evt.Data.OrderNumber != "PO-000001" || evt.Data.Quantity != "100" {
		t.Fatalf("event payload: %s", payload)
	}
}

// AC-21.2, AC-21.3 and the BR-21.1 lifecycle through the store.
func TestStore_Lifecycle(t *testing.T) {
	pool, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(21))
	in := sampleOrder(t, "key-life", ids.New())
	allProfile(t, s, ctx, in.LegalEntityID)
	o, _, err := s.CreateOrder(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	st := &stepper{t: t, s: s, ctx: ctx, orderID: o.ID, version: o.Version}

	_, err = st.step(domain.CmdStart, nil)
	expectProblem(t, err, http.StatusConflict, domain.CodeIllegalTransition)

	est := st.must(domain.CmdEstimate, nil)
	if est.Status != domain.Estimated || est.Version != 2 {
		t.Fatalf("estimate: %s v%d", est.Status, est.Version)
	}
	planned := map[domain.CostGroup]decimal.Decimal{}
	for _, c := range est.Costs {
		planned[c.Group] = planned[c.Group].Add(c.Amount)
	}
	if len(est.Costs) != 3 || !planned[domain.GroupMaterial].Equal(d("3000")) || !planned[domain.GroupLabor].Equal(d("1500")) || !planned[domain.GroupOverhead].Equal(d("500")) {
		t.Fatalf("planned cost: %+v", est.Costs)
	}

	// Stale If-Match.
	_, _, err = s.Transition(ctx, Transition{OrderID: o.ID, IfMatch: 1, Command: domain.CmdSchedule, Actor: "planner-1", IdempotencyKey: "k-stale", RequestHash: "schedule"})
	expectProblem(t, err, http.StatusPreconditionFailed, CodeVersionConflict)

	// Replays: same key and request answer the current order; another command under the key is refused.
	estKey := Transition{OrderID: o.ID, IfMatch: 1, Command: domain.CmdEstimate, Actor: "planner-1", IdempotencyKey: "k-est", RequestHash: "estimate"}
	if _, _, err := s.Transition(ctx, Transition{OrderID: o.ID, IfMatch: 2, Command: domain.CmdEstimate, Actor: "planner-1", IdempotencyKey: "k-est", RequestHash: "estimate"}); err != nil {
		t.Fatalf("re-estimate: %v", err)
	}
	st.version = 3
	again, replayed, err := s.Transition(ctx, estKey)
	if err != nil || !replayed || again.Version != 3 {
		t.Fatalf("replay: replayed=%v version=%d err=%v", replayed, again.Version, err)
	}
	reuse := estKey
	reuse.Command, reuse.RequestHash = domain.CmdSchedule, "schedule"
	_, _, err = s.Transition(ctx, reuse)
	expectProblem(t, err, http.StatusUnprocessableEntity, CodeIdempotencyReuse)

	st.must(domain.CmdSchedule, nil)
	// FR-21.3: re-estimate until Started keeps the status.
	if re := st.must(domain.CmdEstimate, nil); re.Status != domain.Scheduled || len(re.Costs) != 3 {
		t.Fatalf("re-estimate on Scheduled: %s %d rows", re.Status, len(re.Costs))
	}
	st.must(domain.CmdRelease, nil)
	st.must(domain.CmdStart, nil)
	_, err = st.step(domain.CmdEstimate, nil)
	expectProblem(t, err, http.StatusConflict, domain.CodeIllegalTransition)

	_, err = st.step(domain.CmdReportFinished, func(tr *Transition) { tr.Good = d("101") })
	expectProblem(t, err, http.StatusUnprocessableEntity, domain.CodeRAFExceeds)
	raf := st.must(domain.CmdReportFinished, func(tr *Transition) { tr.Good, tr.Error = d("60"), d("2") })
	if raf.Status != domain.ReportedAsFinished || !raf.QtyRAF.Equal(d("60")) || !raf.QtyError.Equal(d("2")) {
		t.Fatalf("RAF: %s raf %s err %s", raf.Status, raf.QtyRAF, raf.QtyError)
	}
	_, err = st.step(domain.CmdReportFinished, func(tr *Transition) { tr.Good = d("39") })
	expectProblem(t, err, http.StatusUnprocessableEntity, domain.CodeRAFExceeds)
	more := st.must(domain.CmdReportFinished, func(tr *Transition) { tr.Good, tr.RAFTolerancePct = d("39"), d("1") })
	if !more.QtyRAF.Equal(d("99")) {
		t.Fatalf("second RAF: %s", more.QtyRAF)
	}
	ended := st.must(domain.CmdEnd, nil)
	if ended.Status != domain.Ended {
		t.Fatalf("end: %s", ended.Status)
	}
	_, err = st.step(domain.CmdResetStatus, nil)
	expectProblem(t, err, http.StatusConflict, domain.CodeIllegalTransition)

	log, err := s.StatusLog(ctx, o.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantLog := []struct {
		cmd      domain.Command
		from, to domain.Status
	}{
		{domain.CmdEstimate, domain.Created, domain.Estimated},
		{domain.CmdEstimate, domain.Estimated, domain.Estimated},
		{domain.CmdSchedule, domain.Estimated, domain.Scheduled},
		{domain.CmdEstimate, domain.Scheduled, domain.Scheduled},
		{domain.CmdRelease, domain.Scheduled, domain.Released},
		{domain.CmdStart, domain.Released, domain.Started},
		{domain.CmdReportFinished, domain.Started, domain.ReportedAsFinished},
		{domain.CmdReportFinished, domain.ReportedAsFinished, domain.ReportedAsFinished},
		{domain.CmdEnd, domain.ReportedAsFinished, domain.Ended},
	}
	if len(log) != len(wantLog) {
		t.Fatalf("status log: want %d rows, got %+v", len(wantLog), log)
	}
	for i, w := range wantLog {
		if log[i].Command != w.cmd || log[i].From != w.from || log[i].To != w.to || log[i].Actor != "planner-1" {
			t.Errorf("log %d: want %s %s→%s, got %+v", i, w.cmd, w.from, w.to, log[i])
		}
	}

	// Events: created, one statuschanged per status change and per RAF (re-estimates emit none),
	// one journal.posted per RAF journal, and order.settled at End.
	counts := map[string]int{}
	types := outboxTypes(t, pool, o.ID)
	for _, typ := range types {
		counts[typ]++
	}
	if types[0] != prodevents.OrderCreated || counts[prodevents.OrderCreated] != 1 || counts[prodevents.OrderStatusChanged] != 7 ||
		counts[prodevents.JournalPosted] != 2 || counts[prodevents.OrderSettled] != 1 || len(types) != 11 {
		t.Fatalf("outbox: %v", types)
	}
}

func TestStore_ResetAndZeroOutput(t *testing.T) {
	_, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(22))
	in := sampleOrder(t, "key-reset", ids.New())
	allProfile(t, s, ctx, in.LegalEntityID)
	o, _, err := s.CreateOrder(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	st := &stepper{t: t, s: s, ctx: ctx, orderID: o.ID, version: o.Version}
	for _, cmd := range []domain.Command{domain.CmdEstimate, domain.CmdSchedule, domain.CmdRelease, domain.CmdStart} {
		st.must(cmd, nil)
	}
	st.must(domain.CmdReportFinished, func(tr *Transition) { tr.Good = d("40") })

	// FR-21.9: reset from ReportedAsFinished reverses the reported quantities.
	back := st.must(domain.CmdResetStatus, nil)
	if back.Status != domain.Started || !back.QtyRAF.IsZero() || !back.QtyError.IsZero() {
		t.Fatalf("reset to Started: %s raf %s", back.Status, back.QtyRAF)
	}
	// BR-21.1: Started → Ended with zero output needs the duty.
	_, err = st.step(domain.CmdEnd, nil)
	expectProblem(t, err, http.StatusUnprocessableEntity, domain.CodeZeroOutput)

	// A multi-step reset to Created drops the estimate.
	created := st.must(domain.CmdResetStatus, func(tr *Transition) { tr.ResetTo = domain.Created })
	if created.Status != domain.Created || len(created.Costs) != 0 {
		t.Fatalf("reset to Created: %s %d cost rows", created.Status, len(created.Costs))
	}
	_, err = st.step(domain.CmdResetStatus, func(tr *Transition) { tr.ResetTo = domain.Released })
	expectProblem(t, err, http.StatusConflict, domain.CodeIllegalTransition)

	for _, cmd := range []domain.Command{domain.CmdEstimate, domain.CmdSchedule, domain.CmdRelease, domain.CmdStart} {
		st.must(cmd, nil)
	}
	if ended := st.must(domain.CmdEnd, func(tr *Transition) { tr.ZeroOutputDuty = true }); ended.Status != domain.Ended {
		t.Fatalf("zero-output end: %s", ended.Status)
	}

	cancelled, _, err := s.CreateOrder(ctx, sampleOrder(t, "key-cancel", ids.New()))
	if err != nil {
		t.Fatal(err)
	}
	cs := &stepper{t: t, s: s, ctx: ctx, orderID: cancelled.ID, version: cancelled.Version}
	if c := cs.must(domain.CmdCancel, nil); c.Status != domain.Cancelled {
		t.Fatalf("cancel: %s", c.Status)
	}
	_, err = cs.step(domain.CmdEstimate, nil)
	expectProblem(t, err, http.StatusConflict, domain.CodeIllegalTransition)
}

func TestStore_UpdateOrder(t *testing.T) {
	pool, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(23))
	o, _, err := s.CreateOrder(ctx, sampleOrder(t, "key-update", ids.New()))
	if err != nil {
		t.Fatal(err)
	}
	st := &stepper{t: t, s: s, ctx: ctx, orderID: o.ID, version: o.Version}
	st.must(domain.CmdEstimate, nil)

	forty := d("40")
	u, err := s.UpdateOrder(ctx, OrderUpdate{OrderID: o.ID, IfMatch: st.version, Quantity: &forty, SetDeliveryDate: true, Actor: "planner-1"})
	if err != nil {
		t.Fatal(err)
	}
	st.version = u.Version
	if !u.Quantity.Equal(forty) || u.DeliveryDate != nil || !u.Lines[0].QtyRequired.Equal(d("80")) || !u.Operations[0].ProcessQty.Equal(forty) {
		t.Fatalf("requantified order: qty %s date %v line %s op %s", u.Quantity, u.DeliveryDate, u.Lines[0].QtyRequired, u.Operations[0].ProcessQty)
	}
	material := decimal.Zero
	for _, c := range u.Costs {
		if c.Group == domain.GroupMaterial {
			material = material.Add(c.Amount)
		}
	}
	if !material.Equal(d("1200")) {
		t.Fatalf("re-estimated material: %s", material)
	}

	_, err = s.UpdateOrder(ctx, OrderUpdate{OrderID: o.ID, IfMatch: 1, Quantity: &forty})
	expectProblem(t, err, http.StatusPreconditionFailed, CodeVersionConflict)

	st.must(domain.CmdSchedule, nil)
	_, err = s.UpdateOrder(ctx, OrderUpdate{OrderID: o.ID, IfMatch: st.version, Quantity: &forty})
	expectProblem(t, err, http.StatusConflict, domain.CodeNotEditable)
	date := "2026-12-15"
	dated, err := s.UpdateOrder(ctx, OrderUpdate{OrderID: o.ID, IfMatch: st.version, SetDeliveryDate: true, DeliveryDate: &date})
	if err != nil || dated.DeliveryDate == nil || *dated.DeliveryDate != date {
		t.Fatalf("date change on Scheduled: %v %v", dated.DeliveryDate, err)
	}

	_, err = s.UpdateOrder(ctx, OrderUpdate{OrderID: ids.New(), IfMatch: 1, SetDeliveryDate: true})
	expectProblem(t, err, http.StatusNotFound, CodeOrderNotFound)

	updated := 0
	for _, typ := range outboxTypes(t, pool, o.ID) {
		if typ == prodevents.OrderUpdated {
			updated++
		}
	}
	if updated != 2 {
		t.Fatalf("want 2 updated events, got %d", updated)
	}
}

func TestStore_ListOrders(t *testing.T) {
	_, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(24))
	le := ids.New()
	var orders []Order
	for i := 0; i < 5; i++ {
		o, _, err := s.CreateOrder(ctx, sampleOrder(t, "key-list-"+string(rune('a'+i)), le))
		if err != nil {
			t.Fatal(err)
		}
		orders = append(orders, o)
	}
	st := &stepper{t: t, s: s, ctx: ctx, orderID: orders[1].ID, version: 1}
	st.must(domain.CmdEstimate, nil)

	first, err := s.ListOrders(ctx, ListFilter{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 2 || first.Items[0].ID != orders[4].ID || first.Items[1].ID != orders[3].ID || first.NextCursor != orders[3].ID {
		t.Fatalf("first page: %+v next %s", first.Items, first.NextCursor)
	}
	rest, err := s.ListOrders(ctx, ListFilter{Limit: 10, Cursor: first.NextCursor})
	if err != nil || len(rest.Items) != 3 || rest.NextCursor != "" || rest.Items[2].ID != orders[0].ID {
		t.Fatalf("second page: %+v %v", rest, err)
	}

	estimated, err := s.ListOrders(ctx, ListFilter{Status: domain.Estimated})
	if err != nil || len(estimated.Items) != 1 {
		t.Fatalf("status filter: %+v %v", estimated, err)
	}
	row := estimated.Items[0]
	if row.ID != orders[1].ID || !row.PlannedCost.Equal(d("5000")) || !row.Remaining.Equal(d("100")) || row.OrderNumber != "PO-000002" {
		t.Fatalf("summary row: %+v", row)
	}
	byProduct, err := s.ListOrders(ctx, ListFilter{ProductID: orders[2].ProductID})
	if err != nil || len(byProduct.Items) != 1 || byProduct.Items[0].ID != orders[2].ID {
		t.Fatalf("product filter: %+v %v", byProduct, err)
	}
	none, err := s.ListOrders(ctx, ListFilter{LegalEntityID: ids.New()})
	if err != nil || len(none.Items) != 0 {
		t.Fatalf("legal entity filter: %+v %v", none, err)
	}
	if _, err := s.StatusLog(ctx, ids.New()); err == nil {
		t.Fatal("status log of an unknown order")
	} else {
		expectProblem(t, err, http.StatusNotFound, CodeOrderNotFound)
	}
}

// Tenant and legal-entity isolation under the non-superuser application role.
func TestStore_RowLevelSecurity(t *testing.T) {
	if os.Getenv(testkit.EnvURL) == "" || os.Getenv(testkit.EnvAppURL) == "" {
		t.Skipf("%s and %s are needed for the RLS test", testkit.EnvURL, testkit.EnvAppURL)
	}
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	var super, bypass bool
	if err := app.QueryRow(context.Background(), `select rolsuper, rolbypassrls from pg_roles where rolname = current_user`).Scan(&super, &bypass); err != nil || super || bypass {
		t.Fatalf("the app role must be subject to RLS: super=%v bypass=%v err=%v", super, bypass, err)
	}
	for _, table := range []string{"production_order_sequence", "production_order", "production_bom_line", "production_operation",
		"production_cost_estimate", "production_status_log", "outbox", "production_posting_profile", "production_journal_sequence",
		"production_journal", "production_journal_line", "production_wip_posting", "production_variance", "production_as_built", "production_order_edit", "production_operation_log"} {
		var enabled, forced bool
		if err := admin.QueryRow(context.Background(), `select relrowsecurity, relforcerowsecurity from pg_class
			where relname = $1 and relnamespace = current_schema()::regnamespace`, table).Scan(&enabled, &forced); err != nil || !enabled || !forced {
			t.Errorf("%s: rls enabled=%v forced=%v err=%v", table, enabled, forced, err)
		}
	}

	s := New(app)
	tenantA, tenantB := testkit.TenantID(31), testkit.TenantID(32)
	leA, leB := ids.New(), ids.New()
	ctxA := tenantCtx(tenantA)
	inA, _, err := s.CreateOrder(ctxA, sampleOrder(t, "rls-a", leA))
	if err != nil {
		t.Fatal(err)
	}
	inB, _, err := s.CreateOrder(ctxA, sampleOrder(t, "rls-b", leB))
	if err != nil {
		t.Fatal(err)
	}

	ctxOther := tenantCtx(tenantB)
	_, err = s.GetOrder(ctxOther, inA.ID)
	expectProblem(t, err, http.StatusNotFound, CodeOrderNotFound)
	if page, err := s.ListOrders(ctxOther, ListFilter{}); err != nil || len(page.Items) != 0 {
		t.Fatalf("other tenant sees %d orders (%v)", len(page.Items), err)
	}
	_, _, err = s.Transition(ctxOther, Transition{OrderID: inA.ID, IfMatch: 1, Command: domain.CmdEstimate, IdempotencyKey: "x", RequestHash: "x"})
	expectProblem(t, err, http.StatusNotFound, CodeOrderNotFound)

	scoped := scopedCtx(tenantA, leA)
	page, err := s.ListOrders(scoped, ListFilter{})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != inA.ID {
		t.Fatalf("legal-entity scope: %+v %v", page.Items, err)
	}
	_, err = s.GetOrder(scoped, inB.ID)
	expectProblem(t, err, http.StatusNotFound, CodeOrderNotFound)
	if _, _, err := s.CreateOrder(scoped, sampleOrder(t, "rls-c", leB)); err == nil {
		t.Fatal("an order was created outside the caller's legal-entity scope")
	}

	// Journals, WIP postings and posting profiles are tenant- and legal-entity-scoped too.
	allProfile(t, s, ctxA, leA)
	st := &stepper{t: t, s: s, ctx: ctxA, orderID: inA.ID, version: inA.Version}
	for _, cmd := range []domain.Command{domain.CmdEstimate, domain.CmdSchedule, domain.CmdRelease, domain.CmdStart} {
		st.must(cmd, nil)
	}
	j := st.mustPost(st.mustJournal(NewJournal{Type: domain.JournalPickingList, Issues: []IssueInput{{LineNo: "10", Qty: d("1")}}}).ID)
	_, err = s.GetJournal(ctxOther, j.ID)
	expectProblem(t, err, http.StatusNotFound, CodeJournalNotFound)
	_, err = s.GetJournal(scopedCtx(tenantA, leB), j.ID)
	expectProblem(t, err, http.StatusNotFound, CodeJournalNotFound)
	_, err = s.Costs(ctxOther, inA.ID)
	expectProblem(t, err, http.StatusNotFound, CodeOrderNotFound)
	if ps, err := s.ListPostingProfiles(ctxOther, leA); err != nil || len(ps) != 0 {
		t.Fatalf("another tenant sees %d profiles (%v)", len(ps), err)
	}
	if _, _, err := s.SavePostingProfile(scoped, ProfileSave{LegalEntityID: leB, Profile: testProfile(domain.AllKey, domain.AllKey, "1450"), Actor: "x"}); err == nil {
		t.Fatal("a posting profile was saved outside the caller's legal-entity scope")
	}
	// As-built records (FR-21.19) are tenant- and legal-entity-scoped too.
	motor := st.refresh().Lines[0].ItemID
	if _, _, err := s.AssignAsBuilt(ctxA, NewAsBuilt{OrderID: inA.ID, Actor: "x", IdempotencyKey: "rls-ab", RequestHash: "rls-ab",
		AsBuilt: domain.AsBuilt{FinishedSerial: "S-1", ComponentItemID: motor, Tracking: domain.TrackSerial, ComponentSerial: "M-1", Qty: d("1")}}); err != nil {
		t.Fatal(err)
	}
	for _, ctx := range []context.Context{ctxOther, scopedCtx(tenantA, leB)} {
		if rows, err := s.ListAsBuilt(ctx, AsBuiltFilter{ComponentSerialOrBatch: "M-1"}); err != nil || len(rows) != 0 {
			t.Fatalf("as-built leaked outside its tenant or legal entity: %d rows (%v)", len(rows), err)
		}
	}
	// Order edits (FR-21.18) and the WIP and variance reports (4.21.14) are scoped too.
	if _, err := s.EditOrder(ctxA, OrderEdit{OrderID: inB.ID, IfMatch: inB.Version, Actor: "x",
		Edits: domain.OrderEdits{Lines: []domain.LineEdit{{Op: domain.EditChange, LineNo: "10", ScrapPct: qty("1")}}}}); err != nil {
		t.Fatal(err)
	}
	_, err = s.OrderEdits(scoped, inB.ID)
	expectProblem(t, err, http.StatusNotFound, CodeOrderNotFound)
	_, err = s.EditOrder(ctxOther, OrderEdit{OrderID: inB.ID, IfMatch: inB.Version + 1, Actor: "x",
		Edits: domain.OrderEdits{Lines: []domain.LineEdit{{Op: domain.EditRemove, LineNo: "10"}}}})
	expectProblem(t, err, http.StatusNotFound, CodeOrderNotFound)
	for _, ctx := range []context.Context{ctxOther, scopedCtx(tenantA, leB)} {
		if rep, err := s.WIPReport(ctx, WIPFilter{}); err != nil || len(rep.Items) != 0 {
			t.Fatalf("WIP report leaked outside its tenant or legal entity: %d rows (%v)", len(rep.Items), err)
		}
	}
	// The subcontract operations inquiry (FR-21.10) is scoped through the order.
	if _, _, err := s.CreateOrder(ctxA, subcontractOrder(t, "rls-sub", leA, ids.New())); err != nil {
		t.Fatal(err)
	}
	if rep, err := s.SubcontractOperations(scoped, SubcontractFilter{}); err != nil || len(rep.Items) != 1 {
		t.Fatalf("subcontract inquiry in scope: %d rows (%v)", len(rep.Items), err)
	}
	for _, ctx := range []context.Context{ctxOther, scopedCtx(tenantA, leB)} {
		if rep, err := s.SubcontractOperations(ctx, SubcontractFilter{}); err != nil || len(rep.Items) != 0 || len(rep.Totals) != 0 {
			t.Fatalf("subcontract inquiry leaked outside its tenant or legal entity: %d rows (%v)", len(rep.Items), err)
		}
	}
	var leaked int
	if err := dbx.WithTenantTx(ctxOther, app, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `select (select count(*) from production_wip_posting) + (select count(*) from production_journal_line)
			+ (select count(*) from production_as_built) + (select count(*) from production_order_edit)`).Scan(&leaked)
	}); err != nil || leaked != 0 {
		t.Fatalf("another tenant sees %d WIP or journal rows (%v)", leaked, err)
	}
}
