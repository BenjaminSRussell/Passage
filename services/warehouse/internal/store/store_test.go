package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/warehouse/internal/domain"
	"github.com/BenjaminSRussell/passage/services/warehouse/migrations"
)

func dq(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func key(k string) Idem { return Idem{Key: k, Hash: "hash-" + k} }

// setup returns a migrated store, a tenant-scoped context and a fresh
// warehouse id. The tests skip without PASSAGE_TEST_DATABASE_URL.
func setup(t *testing.T) (*Store, context.Context, string) {
	t.Helper()
	pool := testkit.DB(t)
	ctx := testkit.Ctx(testkit.TenantID(1))
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := New(appOf(t, pool))
	adminPools.Store(st, pool)
	return st, ctx, ids.New()
}

func loc(name, zone string, seq int, c domain.Capacity) domain.Location {
	return domain.Location{ID: ids.New(), Name: name, Zone: zone, PickSequence: seq, Status: domain.StatusActive, Capacity: c}
}

func directive(wh, workType string, dir domain.Direction, strategy domain.Strategy, zone string) domain.Directive {
	return domain.Directive{ID: ids.New(), Sequence: 10, WorkType: workType, Direction: dir, Warehouse: wh,
		Bands: []domain.Band{{Seq: 1, FromQty: decimal.Zero, Actions: []domain.Action{{Seq: 1, Strategy: strategy, Zone: zone}}}}}
}

func mustLocations(t *testing.T, st *Store, ctx context.Context, wh string, locs ...domain.Location) {
	t.Helper()
	if _, _, err := st.CreateLocations(ctx, key("locs-"+ids.New()), wh, locs); err != nil {
		t.Fatalf("CreateLocations: %v", err)
	}
}

func mustDirective(t *testing.T, st *Store, ctx context.Context, d domain.Directive) {
	t.Helper()
	if _, _, err := st.CreateDirective(ctx, key("dir-"+ids.New()), d); err != nil {
		t.Fatalf("CreateDirective: %v", err)
	}
}

func plan(t *testing.T, st *Store, ctx context.Context, wh, workType string, lines ...domain.NewLine) domain.Work {
	t.Helper()
	p, _, err := st.PlanWork(ctx, key("plan-"+ids.New()), PlanInput{WarehouseID: wh, WorkType: workType, Lines: lines})
	if err != nil {
		t.Fatalf("PlanWork: %v", err)
	}
	return p.Work
}

func assign(t *testing.T, st *Store, ctx context.Context, wh, user string, now time.Time) *domain.Work {
	t.Helper()
	a, _, err := st.AssignNext(ctx, key("assign-"+ids.New()), wh, user, 30*time.Minute, now)
	if err != nil {
		t.Fatalf("AssignNext: %v", err)
	}
	return a.Work
}

func confirm(t *testing.T, st *Store, ctx context.Context, w domain.Work, user string) domain.Work {
	t.Helper()
	out, _, err := st.ConfirmLine(ctx, key("confirm-"+ids.New()), ConfirmInput{WorkID: w.ID, LineNo: 1, Qty: w.Lines[0].Qty, User: user, Now: time.Now()})
	if err != nil {
		t.Fatalf("ConfirmLine: %v", err)
	}
	return out
}

func stockAt(t *testing.T, st *Store, ctx context.Context, wh, name, item string) decimal.Decimal {
	t.Helper()
	locs, err := st.ListLocations(ctx, LocationFilter{WarehouseID: wh, Limit: MaxPageSize})
	if err != nil {
		t.Fatalf("ListLocations: %v", err)
	}
	for _, l := range locs {
		if l.Name == name {
			return l.Stock[item]
		}
	}
	t.Fatalf("no location %s", name)
	return decimal.Zero
}

func outboxCount(t *testing.T, pool *pgxpool.Pool, typ string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `select count(*) from outbox where type = $1`, typ).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func assertCode(t *testing.T, err error, code string) {
	t.Helper()
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != code {
		t.Fatalf("err = %v, want %s", err, code)
	}
}

func TestStore_SetupConflictsAndIdempotency(t *testing.T) {
	st, ctx, wh := setup(t)
	a := loc("A-01", "A", 1, domain.Capacity{AllowMixedItems: true})
	created, replayed, err := st.CreateLocations(ctx, key("locs"), wh, []domain.Location{a})
	if err != nil || replayed || len(created) != 1 || created[0].Version != 1 {
		t.Fatalf("CreateLocations = %+v replayed=%v err=%v", created, replayed, err)
	}
	if again, replayed, err := st.CreateLocations(ctx, key("locs"), wh, []domain.Location{a}); err != nil || !replayed || again[0].ID != a.ID {
		t.Fatalf("replay = %+v replayed=%v err=%v", again, replayed, err)
	}
	if _, _, err := st.CreateLocations(ctx, Idem{Key: "locs", Hash: "other"}, wh, []domain.Location{a}); !errors.Is(err, ErrIdempotencyReuse) {
		t.Fatalf("same key, other request: err = %v, want ErrIdempotencyReuse", err)
	}
	dup := loc("A-01", "A", 2, domain.Capacity{})
	if _, _, err := st.CreateLocations(ctx, key("locs-2"), wh, []domain.Location{dup}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate name: err = %v, want ErrConflict", err)
	}
	// The failed batch released its key: a retry with a valid body succeeds.
	if _, _, err := st.CreateLocations(ctx, Idem{Key: "locs-2", Hash: "fixed"}, wh, []domain.Location{loc("A-02", "A", 2, domain.Capacity{})}); err != nil {
		t.Fatalf("retry after a failed request: %v", err)
	}

	d := directive(wh, "PURCHASE_PUTAWAY", domain.DirectionPut, domain.StrategyConsolidate, "A")
	d.Query = domain.Query{"item_group": "FASTENERS"}
	mustDirective(t, st, ctx, d)
	if _, _, err := st.CreateDirective(ctx, key("dir-dup"), directive(wh, "PURCHASE_PUTAWAY", domain.DirectionPut, domain.StrategyConsolidate, "A")); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate sequence: err = %v, want ErrConflict", err)
	}
	mustDirective(t, st, ctx, directive(wh, "PURCHASE_PUTAWAY", domain.DirectionPick, domain.StrategyFIFO, "A"))
	got, err := st.ListDirectives(ctx, wh, "PURCHASE_PUTAWAY", "PUT")
	if err != nil || len(got) != 1 || got[0].ID != d.ID || got[0].Query["item_group"] != "FASTENERS" || got[0].Bands[0].Actions[0].Zone != "A" {
		t.Fatalf("ListDirectives = %+v err=%v", got, err)
	}

	page1, err := st.ListLocations(ctx, LocationFilter{WarehouseID: wh, Limit: 1})
	if err != nil || len(page1) != 1 || page1[0].Name != "A-01" {
		t.Fatalf("page 1 = %+v err=%v", page1, err)
	}
	page2, err := st.ListLocations(ctx, LocationFilter{WarehouseID: wh, AfterName: page1[0].Name, Limit: 1})
	if err != nil || len(page2) != 1 || page2[0].Name != "A-02" {
		t.Fatalf("page 2 = %+v err=%v", page2, err)
	}
}

func TestStore_PutAwayAssignConfirm(t *testing.T) {
	st, ctx, wh := setup(t)
	maxW := dq("100")
	mustLocations(t, st, ctx, wh, loc("A-01", "A", 1, domain.Capacity{MaxWeight: &maxW, AllowMixedItems: true}))
	mustDirective(t, st, ctx, directive(wh, "PURCHASE_PUTAWAY", domain.DirectionPut, domain.StrategyConsolidate, "A"))

	p, replayed, err := st.PlanWork(ctx, key("put"), PlanInput{WarehouseID: wh, WorkType: "PURCHASE_PUTAWAY",
		Lines: []domain.NewLine{{ItemID: "BOLT", Direction: domain.DirectionPut, Qty: dq("5"), Weight: dq("2.5"), LPs: 1}}})
	if err != nil || replayed {
		t.Fatalf("PlanWork: replayed=%v err=%v", replayed, err)
	}
	put := p.Work
	if put.Status != domain.WorkOpen || put.Lines[0].ToLocationID == "" || put.Priority != domain.DefaultPriority || put.Version != 1 || len(p.Traces) != 1 {
		t.Fatalf("planned = %+v", p)
	}
	again, replayed, err := st.PlanWork(ctx, key("put"), PlanInput{})
	if err != nil || !replayed || again.Work.ID != put.ID {
		t.Fatalf("replay: replayed=%v id=%s err=%v", replayed, again.Work.ID, err)
	}

	now := time.Now()
	got := assign(t, st, ctx, wh, "u1", now)
	if got == nil || got.ID != put.ID || got.Status != domain.WorkAssigned || got.AssignedUser != "u1" || got.Version != 2 {
		t.Fatalf("assign u1 = %+v", got)
	}
	if none := assign(t, st, ctx, wh, "u2", now); none != nil {
		t.Fatalf("second assign should find nothing: %+v", none)
	}

	in := ConfirmInput{WorkID: put.ID, LineNo: 1, Qty: dq("5"), User: "u2", Now: now}
	if _, _, err := st.ConfirmLine(ctx, key("c0"), in); !errors.Is(err, domain.ErrNotAssignee) {
		t.Fatalf("confirm by another user: err = %v", err)
	}
	in.User, in.IfMatch = "u1", 1
	if _, _, err := st.ConfirmLine(ctx, key("c0b"), in); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale If-Match: err = %v", err)
	}
	in.IfMatch = 2
	done, replayed, err := st.ConfirmLine(ctx, key("c1"), in)
	if err != nil || replayed || done.Status != domain.WorkCompleted || done.Lines[0].Status != domain.LineCompleted || done.Version != 3 {
		t.Fatalf("confirm: %+v replayed=%v err=%v", done, replayed, err)
	}
	if again, replayed, err := st.ConfirmLine(ctx, key("c1"), in); err != nil || !replayed || again.Version != 3 {
		t.Fatalf("confirm replay: replayed=%v err=%v", replayed, err)
	}
	in.IfMatch = 0
	_, _, err = st.ConfirmLine(ctx, key("c2"), in)
	assertCode(t, err, domain.CodeLineConfirmed)

	if q := stockAt(t, st, ctx, wh, "A-01", "BOLT"); !q.Equal(dq("5")) {
		t.Fatalf("A-01 holds %s BOLT, want 5", q)
	}
	locs, _ := st.ListLocations(ctx, LocationFilter{WarehouseID: wh, Limit: 10})
	if !locs[0].Used.Weight.Equal(dq("2.5")) || locs[0].Used.LPs != 1 || locs[0].Version != 2 {
		t.Fatalf("A-01 usage = %+v version %d", locs[0].Used, locs[0].Version)
	}
	if outboxCount(t, adminOf(st), "dev.passage.wms.work.created.v1") != 1 || outboxCount(t, adminOf(st), "dev.passage.wms.work.completed.v1") != 1 {
		t.Fatal("want one work.created and one work.completed event")
	}
	if _, err := st.GetWork(ctx, ids.New()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing work: err = %v, want ErrNotFound", err)
	}
}

// receive plans a put-away of qty item, assigns it and confirms it, so the
// stock exists for the pick tests. No other work may be OPEN.
func receive(t *testing.T, st *Store, ctx context.Context, wh, item, qty, weight string) {
	t.Helper()
	w := plan(t, st, ctx, wh, "PURCHASE_PUTAWAY", domain.NewLine{ItemID: item, Direction: domain.DirectionPut, Qty: dq(qty), Weight: dq(weight)})
	if got := assign(t, st, ctx, wh, "receiver", time.Now()); got == nil || got.ID != w.ID {
		t.Fatalf("assign put-away = %+v", got)
	}
	if done := confirm(t, st, ctx, w, "receiver"); done.Status != domain.WorkCompleted {
		t.Fatalf("put-away status %s", done.Status)
	}
}

// AC-31.5: an IN_PROGRESS pick of 10 EA short picked at 6 with reason EMPTY
// becomes PARTIALLY_COMPLETED, a new OPEN work for 4 EA references it, and
// only 6 EA leave the location.
func TestStore_ShortPick(t *testing.T) {
	st, ctx, wh := setup(t)
	mustLocations(t, st, ctx, wh, loc("P-01", "PICK", 1, domain.Capacity{AllowMixedItems: true}))
	mustDirective(t, st, ctx, directive(wh, "PURCHASE_PUTAWAY", domain.DirectionPut, domain.StrategyConsolidate, "PICK"))
	mustDirective(t, st, ctx, directive(wh, "SALES_PICK", domain.DirectionPick, domain.StrategyFIFO, "PICK"))
	receive(t, st, ctx, wh, "BOLT", "12", "12")

	pick := plan(t, st, ctx, wh, "SALES_PICK", domain.NewLine{ItemID: "BOLT", Direction: domain.DirectionPick, Qty: dq("10"), Weight: dq("10")})
	if pick.Status != domain.WorkOpen || pick.Lines[0].FromLocationID == "" {
		t.Fatalf("pick = %+v", pick)
	}
	assign(t, st, ctx, wh, "picker", time.Now())
	ex := ExceptionInput{WorkID: pick.ID, Action: ActionShortPick, LineNo: 1, Qty: dq("6"), Reason: "EMPTY", User: "picker", Now: time.Now()}
	out, replayed, err := st.ExceptWork(ctx, key("short"), ex)
	if err != nil || replayed {
		t.Fatalf("short pick: replayed=%v err=%v", replayed, err)
	}
	parent, child := out.Work, out.Remainder
	if parent.Status != domain.WorkPartiallyComplete || parent.Lines[0].Status != domain.LineShort || parent.Lines[0].Reason != "EMPTY" {
		t.Fatalf("parent = %+v", parent)
	}
	if child == nil || child.ParentID != pick.ID || child.Status != domain.WorkOpen || !child.Lines[0].Qty.Equal(dq("4")) || !child.Lines[0].Weight.Equal(dq("4")) {
		t.Fatalf("remainder = %+v", child)
	}
	if q := stockAt(t, st, ctx, wh, "P-01", "BOLT"); !q.Equal(dq("6")) {
		t.Fatalf("P-01 holds %s BOLT, want 12 - 6 = 6", q)
	}
	if again, replayed, err := st.ExceptWork(ctx, key("short"), ex); err != nil || !replayed || again.Remainder == nil || again.Remainder.ID != child.ID {
		t.Fatalf("short pick replay: %+v replayed=%v err=%v", again, replayed, err)
	}
	fetched, err := st.GetWork(ctx, child.ID)
	if err != nil || fetched.ParentID != pick.ID || fetched.Lines[0].FromLocationID != pick.Lines[0].FromLocationID {
		t.Fatalf("GetWork remainder = %+v err=%v", fetched, err)
	}
	// Created: put-away, pick, remainder. Completed: put-away and the short-picked parent.
	if c, d := outboxCount(t, adminOf(st), "dev.passage.wms.work.created.v1"), outboxCount(t, adminOf(st), "dev.passage.wms.work.completed.v1"); c != 3 || d != 2 {
		t.Fatalf("outbox created=%d completed=%d, want 3 and 2", c, d)
	}
}

// BR-31.2: open lines reserve room and stock while later work is planned.
func TestStore_PlanningReservesOpenLines(t *testing.T) {
	st, ctx, wh := setup(t)
	one := 1
	mustLocations(t, st, ctx, wh,
		loc("R-01", "RESERVE", 1, domain.Capacity{MaxLPs: &one}),
		loc("R-02", "RESERVE", 2, domain.Capacity{MaxLPs: &one}),
		loc("P-01", "PICK", 1, domain.Capacity{AllowMixedItems: true}))
	mustDirective(t, st, ctx, directive(wh, "PURCHASE_PUTAWAY", domain.DirectionPut, domain.StrategyEmptyLocation, "RESERVE"))
	pallet := domain.NewLine{ItemID: "BOLT", Direction: domain.DirectionPut, Qty: dq("100"), LPs: 1}
	first := plan(t, st, ctx, wh, "PURCHASE_PUTAWAY", pallet)
	second := plan(t, st, ctx, wh, "PURCHASE_PUTAWAY", pallet)
	third := plan(t, st, ctx, wh, "PURCHASE_PUTAWAY", pallet)
	if first.Lines[0].ToLocationID == second.Lines[0].ToLocationID || second.Lines[0].ToLocationID == "" {
		t.Fatalf("two open pallets planned to one single-LP location: %s, %s", first.Lines[0].ToLocationID, second.Lines[0].ToLocationID)
	}
	if third.Status != domain.WorkBlockedNoLocation || third.Lines[0].Reason != domain.CodeNoLocation {
		t.Fatalf("third pallet = %+v, want BLOCKED_NO_LOCATION", third)
	}
	if n := assign(t, st, ctx, wh, "u1", time.Now()); n == nil || n.ID != first.ID {
		t.Fatalf("assign takes the oldest open work first: %+v", n)
	}
}

func TestStore_PickReservationBlocksOverPromise(t *testing.T) {
	st, ctx, wh := setup(t)
	mustLocations(t, st, ctx, wh, loc("P-01", "PICK", 1, domain.Capacity{AllowMixedItems: true}))
	mustDirective(t, st, ctx, directive(wh, "PURCHASE_PUTAWAY", domain.DirectionPut, domain.StrategyConsolidate, "PICK"))
	mustDirective(t, st, ctx, directive(wh, "SALES_PICK", domain.DirectionPick, domain.StrategyFIFO, "PICK"))
	receive(t, st, ctx, wh, "BOLT", "10", "0")
	line := domain.NewLine{ItemID: "BOLT", Direction: domain.DirectionPick, Qty: dq("10")}
	if w := plan(t, st, ctx, wh, "SALES_PICK", line); w.Status != domain.WorkOpen {
		t.Fatalf("first pick = %s", w.Status)
	}
	if w := plan(t, st, ctx, wh, "SALES_PICK", line); w.Status != domain.WorkBlockedNoLocation {
		t.Fatalf("second pick of the same 10 EA = %s, want BLOCKED_NO_LOCATION", w.Status)
	}
}

func TestStore_CancelAndLockTimeout(t *testing.T) {
	st, ctx, wh := setup(t)
	mustLocations(t, st, ctx, wh, loc("A-01", "A", 1, domain.Capacity{AllowMixedItems: true}))
	mustDirective(t, st, ctx, directive(wh, "PURCHASE_PUTAWAY", domain.DirectionPut, domain.StrategyConsolidate, "A"))
	w := plan(t, st, ctx, wh, "PURCHASE_PUTAWAY", domain.NewLine{ItemID: "BOLT", Direction: domain.DirectionPut, Qty: dq("1")})

	stale := time.Now().Add(-31 * time.Minute)
	if got := assign(t, st, ctx, wh, "u1", stale); got == nil || got.ID != w.ID {
		t.Fatalf("assign u1 = %+v", got)
	}
	// u1 has been idle past the 30 minute lock timeout: the work returns to OPEN and goes to u2.
	if got := assign(t, st, ctx, wh, "u2", time.Now()); got == nil || got.ID != w.ID || got.AssignedUser != "u2" {
		t.Fatalf("reclaimed assign = %+v", got)
	}

	cancel := ExceptionInput{WorkID: w.ID, Action: ActionCancel, Reason: "DAMAGED", User: "u1", Now: time.Now()}
	if _, _, err := st.ExceptWork(ctx, key("cancel-u1"), cancel); !errors.Is(err, domain.ErrNotAssignee) {
		t.Fatalf("cancel by a former assignee: err = %v", err)
	}
	cancel.User, cancel.Supervise = "boss", true
	out, _, err := st.ExceptWork(ctx, key("cancel-boss"), cancel)
	if err != nil || out.Work.Status != domain.WorkCancelled || out.Work.Lines[0].Status != domain.LineCancelled || out.Work.Lines[0].Reason != "DAMAGED" {
		t.Fatalf("supervisor cancel = %+v err=%v", out.Work, err)
	}
	_, _, err = st.ExceptWork(ctx, key("cancel-again"), cancel)
	assertCode(t, err, domain.CodeIllegalTransition)
}

// AC-31.4 in small: concurrent assignment never hands one work to two users.
func TestStore_ConcurrentAssignment(t *testing.T) {
	st, ctx, wh := setup(t)
	mustLocations(t, st, ctx, wh, loc("A-01", "A", 1, domain.Capacity{AllowMixedItems: true}))
	mustDirective(t, st, ctx, directive(wh, "PURCHASE_PUTAWAY", domain.DirectionPut, domain.StrategyConsolidate, "A"))
	const works = 20
	for i := 0; i < works; i++ {
		plan(t, st, ctx, wh, "PURCHASE_PUTAWAY", domain.NewLine{ItemID: "BOLT", Direction: domain.DirectionPut, Qty: dq("1")})
	}
	var (
		mu   sync.Mutex
		seen = map[string]string{}
		wg   sync.WaitGroup
		errs = make(chan error, works+5)
	)
	for i := 0; i < works+5; i++ {
		wg.Add(1)
		go func(user string) {
			defer wg.Done()
			a, _, err := st.AssignNext(ctx, key("a-"+user), wh, user, 30*time.Minute, time.Now())
			if err != nil {
				errs <- err
				return
			}
			if a.Work == nil {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if other, dup := seen[a.Work.ID]; dup {
				errs <- fmt.Errorf("work %s given to %s and %s", a.Work.ID, other, user)
			}
			seen[a.Work.ID] = user
		}(fmt.Sprintf("u%02d", i))
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if len(seen) != works {
		t.Fatalf("%d works assigned, want %d", len(seen), works)
	}
}

func TestStore_ListWorkPages(t *testing.T) {
	st, ctx, wh := setup(t)
	mustLocations(t, st, ctx, wh, loc("A-01", "A", 1, domain.Capacity{AllowMixedItems: true}))
	for i := 0; i < 3; i++ {
		plan(t, st, ctx, wh, "PURCHASE_PUTAWAY", domain.NewLine{ItemID: "BOLT", Direction: domain.DirectionPut, Qty: dq("1")})
	}
	first, err := st.ListWork(ctx, WorkFilter{WarehouseID: wh, Limit: 2})
	if err != nil || len(first) != 2 {
		t.Fatalf("page 1 = %d err=%v", len(first), err)
	}
	last := first[1]
	rest, err := st.ListWork(ctx, WorkFilter{WarehouseID: wh, After: &WorkKey{Priority: last.Priority, CreatedAt: last.CreatedAt, ID: last.ID}, Limit: 2})
	if err != nil || len(rest) != 1 || rest[0].ID == first[0].ID || rest[0].ID == first[1].ID {
		t.Fatalf("page 2 = %+v err=%v", rest, err)
	}
	blocked, err := st.ListWork(ctx, WorkFilter{WarehouseID: wh, Status: string(domain.WorkBlockedNoLocation), Limit: 10})
	if err != nil || len(blocked) != 3 {
		t.Fatalf("no directive exists, so all three are blocked: %d err=%v", len(blocked), err)
	}
}

// Row-level security: through an application (non-owner) role, tenant 2 sees
// none of tenant 1's locations, directives, work, doors or receipts.
func TestStore_TenantIsolation(t *testing.T) {
	if os.Getenv(testkit.EnvAppURL) == "" {
		t.Skipf("%s not set; skipping RLS test", testkit.EnvAppURL)
	}
	admin, app := testkit.DBWithApp(t)
	ctx1 := testkit.Ctx(testkit.TenantID(1))
	ctx2 := testkit.Ctx(testkit.TenantID(2))
	if err := dbx.Migrate(ctx1, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	st := New(app)
	wh := ids.New()
	mustLocations(t, st, ctx1, wh, loc("A-01", "A", 1, domain.Capacity{AllowMixedItems: true}))
	mustDirective(t, st, ctx1, directive(wh, "PURCHASE_PUTAWAY", domain.DirectionPut, domain.StrategyConsolidate, "A"))
	w := plan(t, st, ctx1, wh, "PURCHASE_PUTAWAY", domain.NewLine{ItemID: "BOLT", Direction: domain.DirectionPut, Qty: dq("1")})
	d1 := door(t, st, ctx1, wh, "D1")
	if _, _, err := st.CreateAppointment(ctx1, key("t1-appt"), booking(wh, d1.ID, "ASN-1", slot(9, 0), slot(9, 45))); err != nil {
		t.Fatal(err)
	}
	rec, _, err := st.RegisterASN(ctx1, key("t1-asn"), asn(wh))
	if err != nil {
		t.Fatal(err)
	}

	if locs, err := st.ListLocations(ctx2, LocationFilter{WarehouseID: wh, Limit: 10}); err != nil || len(locs) != 0 {
		t.Fatalf("tenant 2 locations = %+v err=%v", locs, err)
	}
	if dirs, err := st.ListDirectives(ctx2, wh, "", ""); err != nil || len(dirs) != 0 {
		t.Fatalf("tenant 2 directives = %+v err=%v", dirs, err)
	}
	if _, err := st.GetWork(ctx2, w.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("tenant 2 reading tenant 1's work: err = %v", err)
	}
	if a, _, err := st.AssignNext(ctx2, key("t2"), wh, "u", 30*time.Minute, time.Now()); err != nil || a.Work != nil {
		t.Fatalf("tenant 2 assigned tenant 1's work: %+v err=%v", a, err)
	}
	if doors, err := st.ListDoors(ctx2, wh); err != nil || len(doors) != 0 {
		t.Fatalf("tenant 2 doors = %+v err=%v", doors, err)
	}
	if appts, err := st.ListAppointments(ctx2, AppointmentFilter{WarehouseID: wh, Limit: 10}); err != nil || len(appts) != 0 {
		t.Fatalf("tenant 2 appointments = %+v err=%v", appts, err)
	}
	if _, _, err := st.CreateAppointment(ctx2, key("t2-appt"), booking(wh, d1.ID, "ASN-2", slot(10, 0), slot(10, 15))); !errors.Is(err, ErrNotFound) {
		t.Fatalf("tenant 2 booking tenant 1's door: err = %v", err)
	}
	if _, err := st.GetReceipt(ctx2, rec.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("tenant 2 reading tenant 1's receipt: err = %v", err)
	}
	if _, _, err := st.ReceiveLine(ctx2, key("t2-recv"), ReceiveInput{ReceiptID: rec.ID, LineNo: 1, Qty: dq("1"), User: "u", Now: time.Now()}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("tenant 2 receiving tenant 1's pallet: err = %v", err)
	}
	if sums, err := st.ListReceipts(ctx2, ReceiptFilter{WarehouseID: wh, Limit: 10}); err != nil || len(sums) != 0 {
		t.Fatalf("tenant 2 receipts = %+v err=%v", sums, err)
	}
}
