package store

import (
	"context"
	"fmt"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/warehouse/internal/domain"
)

func seedStock(t *testing.T, st *Store, ctx context.Context, name string, locs []domain.Location, item string, qty string) {
	t.Helper()
	for _, l := range locs {
		if l.Name != name {
			continue
		}
		err := st.inTx(ctx, func(tx pgx.Tx) error {
			l.Stock = map[string]decimal.Decimal{item: dq(qty)}
			return writeLocation(ctx, tx, l, item)
		})
		if err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
		return
	}
	t.Fatalf("no location %s", name)
}

func pickDirective(wh string) domain.Directive {
	return directive(wh, "SALES_PICK", domain.DirectionPick, domain.StrategyConsolidate, "FWD")
}

// executeAll assigns and confirms every OPEN work of the warehouse to user.
func executeAll(t *testing.T, st *Store, ctx context.Context, wh, user string) int {
	t.Helper()
	done := 0
	for {
		w := assign(t, st, ctx, wh, user, time.Now())
		if w == nil {
			return done
		}
		for _, l := range w.Lines {
			if _, _, err := st.ConfirmLine(ctx, key("confirm-"+ids.New()), ConfirmInput{WorkID: w.ID, LineNo: l.LineNo, Qty: l.Qty, User: user, Now: time.Now()}); err != nil {
				t.Fatalf("confirm %s line %d: %v", w.ID, l.LineNo, err)
			}
		}
		done++
	}
}

// TestStore_ReleaseWave500Orders is Proof AC4 against the database: 500 orders
// release 500 pick works and the replenishment the picks need; executing all of
// it leaves each forward location at stock - demand, or at the maximum when it
// was replenished, and the reserve locations drained by exactly the refills.
func TestStore_ReleaseWave500Orders(t *testing.T) {
	st, ctx, wh := setup(t)
	var locs []domain.Location
	for s := range 50 {
		locs = append(locs, loc(fmt.Sprintf("F-%02d", s), "FWD", s, domain.Capacity{}),
			loc(fmt.Sprintf("RA-%02d", s), "RES", 100+s, domain.Capacity{}),
			loc(fmt.Sprintf("RB-%02d", s), "RES", 200+s, domain.Capacity{}))
	}
	mustLocations(t, st, ctx, wh, locs...)
	mustDirective(t, st, ctx, pickDirective(wh))
	var rules []domain.ReplenishRule
	for s := range 50 {
		item := fmt.Sprintf("SKU-%02d", s)
		seedStock(t, st, ctx, fmt.Sprintf("F-%02d", s), locs, item, "120")
		seedStock(t, st, ctx, fmt.Sprintf("RA-%02d", s), locs, item, "15")
		seedStock(t, st, ctx, fmt.Sprintf("RB-%02d", s), locs, item, "5000")
		rules = append(rules, domain.ReplenishRule{ItemID: item, PickZone: "FWD", ReserveZone: "RES", MinQty: dq("40"), MaxQty: dq("150")})
	}
	demand := map[string]int{}
	orders := make([]domain.WaveOrder, 0, 500)
	for i := range 500 {
		item := fmt.Sprintf("SKU-%02d", i%50)
		qty := 1 + (i*7)%10
		demand[item] += qty
		orders = append(orders, domain.WaveOrder{ID: fmt.Sprintf("SO-%03d", i),
			Lines: []domain.NewLine{{ItemID: item, Direction: domain.DirectionPick, Qty: decimal.NewFromInt(int64(qty))}}})
	}

	in := WaveInput{WarehouseID: wh, Orders: orders, Rules: rules}
	rel, replayed, err := st.ReleaseWave(ctx, key("wave"), in)
	if err != nil || replayed {
		t.Fatalf("ReleaseWave: replayed=%v err=%v", replayed, err)
	}
	if len(rel.Picks) != 500 {
		t.Fatalf("picks = %d, want 500", len(rel.Picks))
	}
	wantRefill := map[string]int{}
	for item, d := range demand {
		if left := 120 - d; left < 40 {
			wantRefill[item] = 150 - left
		}
	}
	if len(wantRefill) == 0 || len(rel.Replenishment) != len(wantRefill) {
		t.Fatalf("replenishments = %d, want %d", len(rel.Replenishment), len(wantRefill))
	}
	if again, replayed, err := st.ReleaseWave(ctx, key("wave"), in); err != nil || !replayed || again.WaveID != rel.WaveID {
		t.Fatalf("replay: replayed=%v err=%v", replayed, err)
	}
	page, err := st.ListWork(ctx, WorkFilter{WarehouseID: wh, Status: "OPEN", Limit: MaxPageSize})
	if err != nil || len(page) != MaxPageSize || page[0].WorkType != "REPLENISHMENT" {
		t.Fatalf("replenishment must head the queue: %d works, first %+v err=%v", len(page), page[0], err)
	}

	if done := executeAll(t, st, ctx, wh, "u1"); done != 500+len(wantRefill) {
		t.Fatalf("executed %d works, want %d", done, 500+len(wantRefill))
	}
	for s := range 50 {
		item := fmt.Sprintf("SKU-%02d", s)
		want := 120 - demand[item]
		if refill, ok := wantRefill[item]; ok {
			want = 150
			if got := stockAt(t, st, ctx, wh, fmt.Sprintf("RA-%02d", s), item).Add(stockAt(t, st, ctx, wh, fmt.Sprintf("RB-%02d", s), item)); !got.Equal(dq(fmt.Sprint(5015 - refill))) {
				t.Errorf("%s reserve = %s, want %d", item, got, 5015-refill)
			}
		}
		if got := stockAt(t, st, ctx, wh, fmt.Sprintf("F-%02d", s), item); !got.Equal(decimal.NewFromInt(int64(want))) {
			t.Errorf("%s forward = %s, want %d", item, got, want)
		}
	}
}

// TestStore_Simulation10kLinesPerDay is Proof AC2: a day of 10,000 pick lines
// (10 waves of 50 orders of 20 lines, with min/max replenishment) is released
// while 10 concurrent pickers work the queue. Every call has a deadline, so a
// lock hang fails fast; no call fails (a deadlock aborts a transaction with an
// error), every pick and refill work completes, stock is conserved between the
// forward and reserve locations, and the p95 of work assignment is under one
// second. The default run is a tenth of the day; PASSAGE_WMS_SIM_10K=1 runs all of it.
func TestStore_Simulation10kLinesPerDay(t *testing.T) {
	waves := 1
	if os.Getenv("PASSAGE_WMS_SIM_10K") == "1" {
		waves = 10
	}
	const ordersPerWave, linesPerOrder, pickers, skus = 50, 20, 10, 50
	st, ctx, wh := setup(t)
	call := func() (context.Context, context.CancelFunc) { return context.WithTimeout(ctx, 30*time.Second) }
	var locs []domain.Location
	var rules []domain.ReplenishRule
	for s := range skus {
		locs = append(locs, loc(fmt.Sprintf("F-%02d", s), "FWD", s, domain.Capacity{}), loc(fmt.Sprintf("R-%02d", s), "RES", 100+s, domain.Capacity{}))
		rules = append(rules, domain.ReplenishRule{ItemID: fmt.Sprintf("SKU-%02d", s), PickZone: "FWD", ReserveZone: "RES", MinQty: dq("1000"), MaxQty: dq("1500")})
	}
	mustLocations(t, st, ctx, wh, locs...)
	mustDirective(t, st, ctx, pickDirective(wh))
	for s := range skus {
		seedStock(t, st, ctx, fmt.Sprintf("F-%02d", s), locs, fmt.Sprintf("SKU-%02d", s), "1020")
		seedStock(t, st, ctx, fmt.Sprintf("R-%02d", s), locs, fmt.Sprintf("SKU-%02d", s), "1000000")
	}

	var (
		mu        sync.Mutex
		latencies []time.Duration
		completed int
		failures  []error
		wg        sync.WaitGroup
		released  atomic.Bool
	)
	fail := func(err error) {
		mu.Lock()
		failures = append(failures, err)
		mu.Unlock()
	}
	for p := range pickers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			user := fmt.Sprintf("picker-%d", p)
			for {
				done := released.Load()
				c, cancel := call()
				started := time.Now()
				a, _, err := st.AssignNext(c, key("assign-"+ids.New()), wh, user, 30*time.Minute, started)
				took := time.Since(started)
				cancel()
				mu.Lock()
				latencies = append(latencies, took)
				mu.Unlock()
				if err != nil {
					fail(err)
					return
				}
				if a.Work == nil {
					if done {
						return
					}
					time.Sleep(5 * time.Millisecond)
					continue
				}
				for _, l := range a.Work.Lines {
					c, cancel := call()
					_, _, err := st.ConfirmLine(c, key("confirm-"+ids.New()), ConfirmInput{WorkID: a.Work.ID, LineNo: l.LineNo, Qty: l.Qty, User: user, Now: time.Now()})
					cancel()
					if err != nil {
						fail(err)
						return
					}
				}
				mu.Lock()
				completed++
				mu.Unlock()
			}
		}()
	}

	picked := map[string]int{}
	works := 0
	for w := range waves {
		orders := make([]domain.WaveOrder, ordersPerWave)
		for o := range orders {
			orders[o].ID = fmt.Sprintf("SO-%d-%d", w, o)
			for k := range linesPerOrder {
				item := fmt.Sprintf("SKU-%02d", (o*linesPerOrder+k)%skus)
				qty := 1 + k%3
				picked[item] += qty
				orders[o].Lines = append(orders[o].Lines, domain.NewLine{ItemID: item, Direction: domain.DirectionPick, Qty: decimal.NewFromInt(int64(qty))})
			}
		}
		c, cancel := call()
		rel, _, err := st.ReleaseWave(c, key("sim-wave-"+ids.New()), WaveInput{WarehouseID: wh, Orders: orders, Rules: rules})
		cancel()
		if err != nil {
			fail(err)
			break
		}
		works += len(rel.Picks) + len(rel.Replenishment)
	}
	released.Store(true)
	wg.Wait()

	if len(failures) > 0 {
		t.Fatalf("%d calls failed, first: %v", len(failures), failures[0])
	}
	if completed != works {
		t.Fatalf("completed %d of %d works", completed, works)
	}
	slices.Sort(latencies)
	p95 := latencies[len(latencies)*95/100]
	t.Logf("%d lines, %d works, %d assignments, assign p95 %s, max %s", waves*ordersPerWave*linesPerOrder, works, len(latencies), p95, latencies[len(latencies)-1])
	if p95 >= time.Second {
		t.Fatalf("assignment p95 = %s, want under 1s", p95)
	}
	refilled := false
	for item, qty := range picked {
		s := item[len("SKU-"):]
		fwd, res := stockAt(t, st, ctx, wh, "F-"+s, item), stockAt(t, st, ctx, wh, "R-"+s, item)
		if total := fwd.Add(res); !total.Equal(decimal.NewFromInt(int64(1001020 - qty))) {
			t.Errorf("%s forward+reserve = %s, want %d", item, total, 1001020-qty)
		}
		if fwd.GreaterThan(dq("1500")) || fwd.LessThan(dq("1")) {
			t.Errorf("%s forward = %s, outside 1..1500", item, fwd)
		}
		refilled = refilled || res.LessThan(dq("1000000"))
	}
	if !refilled {
		t.Error("the day never triggered a replenishment")
	}
}

// TestStore_WaitingPickIsNeverAssignedBeforeItsRefillCompletes: a 60 EA pick
// against 50 EA at the forward location comes out WAITING on the refill. Two
// pickers poll for work while the refill is confirmed line by line; whoever
// receives the pick must find the refill COMPLETED, and the pick must confirm.
func TestStore_WaitingPickIsNeverAssignedBeforeItsRefillCompletes(t *testing.T) {
	st, ctx, wh := setup(t)
	locs := []domain.Location{loc("F-01", "FWD", 1, domain.Capacity{}), loc("R-01", "RES", 2, domain.Capacity{}), loc("R-02", "RES", 3, domain.Capacity{})}
	mustLocations(t, st, ctx, wh, locs...)
	mustDirective(t, st, ctx, pickDirective(wh))
	seedStock(t, st, ctx, "F-01", locs, "A", "50")
	seedStock(t, st, ctx, "R-01", locs, "A", "20")
	seedStock(t, st, ctx, "R-02", locs, "A", "5000")
	rule := domain.ReplenishRule{ItemID: "A", PickZone: "FWD", ReserveZone: "RES", MinQty: dq("40"), MaxQty: dq("150")}
	rel, _, err := st.ReleaseWave(ctx, key("wave"), WaveInput{WarehouseID: wh, Rules: []domain.ReplenishRule{rule},
		Orders: []domain.WaveOrder{{ID: "SO", Lines: []domain.NewLine{{ItemID: "A", Direction: domain.DirectionPick, Qty: dq("60")}}}}})
	if err != nil || len(rel.Replenishment) != 1 || len(rel.Picks) != 1 {
		t.Fatalf("ReleaseWave: %+v err=%v", rel, err)
	}
	pick, refill := rel.Picks[0], rel.Replenishment[0]
	stored, err := st.GetWork(ctx, pick.ID)
	if err != nil || stored.Status != domain.WorkWaiting || len(stored.DependsOn) != 1 || stored.DependsOn[0] != refill.ID {
		t.Fatalf("stored pick = %+v err=%v", stored, err)
	}

	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		errs   []error
		picked atomic.Bool
	)
	fail := func(format string, args ...any) {
		mu.Lock()
		errs = append(errs, fmt.Errorf(format, args...))
		mu.Unlock()
	}
	for _, user := range []string{"u1", "u2"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 400 {
				if picked.Load() {
					return
				}
				w := assign(t, st, ctx, wh, user, time.Now())
				if w == nil {
					time.Sleep(2 * time.Millisecond)
					continue
				}
				if w.ID == pick.ID {
					if r, err := st.GetWork(ctx, refill.ID); err != nil || r.Status != domain.WorkCompleted {
						fail("pick assigned while the refill is %s (err %v)", r.Status, err)
					}
				}
				for _, l := range w.Lines {
					time.Sleep(2 * time.Millisecond)
					if _, _, err := st.ConfirmLine(ctx, key("c-"+ids.New()), ConfirmInput{WorkID: w.ID, LineNo: l.LineNo, Qty: l.Qty, User: user, Now: time.Now()}); err != nil {
						fail("confirm %s line %d: %v", w.WorkType, l.LineNo, err)
						return
					}
				}
				if w.ID == pick.ID {
					picked.Store(true)
				}
			}
		}()
	}
	wg.Wait()
	if len(errs) > 0 {
		t.Fatal(errs[0])
	}
	if !picked.Load() {
		t.Fatal("the pick never became assignable after the refill completed")
	}
	if got := stockAt(t, st, ctx, wh, "F-01", "A"); !got.Equal(dq("150")) {
		t.Fatalf("forward = %s, want 150 (50 + 160 - 60)", got)
	}
}

func waitingPickFixture(t *testing.T) (*Store, context.Context, string, Released) {
	t.Helper()
	st, ctx, wh := setup(t)
	locs := []domain.Location{loc("F-01", "FWD", 1, domain.Capacity{}), loc("R-01", "RES", 2, domain.Capacity{}), loc("R-02", "RES", 3, domain.Capacity{})}
	mustLocations(t, st, ctx, wh, locs...)
	mustDirective(t, st, ctx, pickDirective(wh))
	seedStock(t, st, ctx, "F-01", locs, "A", "50")
	seedStock(t, st, ctx, "R-01", locs, "A", "20")
	seedStock(t, st, ctx, "R-02", locs, "A", "5000")
	rule := domain.ReplenishRule{ItemID: "A", PickZone: "FWD", ReserveZone: "RES", MinQty: dq("40"), MaxQty: dq("150")}
	rel, _, err := st.ReleaseWave(ctx, key("wave"), WaveInput{WarehouseID: wh, Rules: []domain.ReplenishRule{rule},
		Orders: []domain.WaveOrder{{ID: "SO", Lines: []domain.NewLine{{ItemID: "A", Direction: domain.DirectionPick, Qty: dq("60")}}}}})
	if err != nil || len(rel.Replenishment) != 1 || len(rel.Picks) != 1 || rel.Picks[0].Status != domain.WorkWaiting {
		t.Fatalf("ReleaseWave: %+v err=%v", rel, err)
	}
	return st, ctx, wh, rel
}

func statusOf(t *testing.T, st *Store, ctx context.Context, id string) domain.Work {
	t.Helper()
	w, err := st.GetWork(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func confirmRest(t *testing.T, st *Store, ctx context.Context, id, user string) {
	t.Helper()
	for _, l := range statusOf(t, st, ctx, id).Lines {
		if l.Status != domain.LineOpen {
			continue
		}
		if _, _, err := st.ConfirmLine(ctx, key("c-"+ids.New()), ConfirmInput{WorkID: id, LineNo: l.LineNo, Qty: l.Qty, User: user, Now: time.Now()}); err != nil {
			t.Fatalf("confirm line %d: %v", l.LineNo, err)
		}
	}
}

// A short-picked refill hands the wait to its remainder work; the pick opens
// only when the remainder ends.
func TestStore_ShortPickedRefillPassesTheWaitToItsRemainder(t *testing.T) {
	st, ctx, wh, rel := waitingPickFixture(t)
	pick, refill := rel.Picks[0], rel.Replenishment[0]
	if w := assign(t, st, ctx, wh, "u1", time.Now()); w == nil || w.ID != refill.ID {
		t.Fatalf("the refill must be assigned first: %+v", w)
	}
	ex, _, err := st.ExceptWork(ctx, key("short"), ExceptionInput{WorkID: refill.ID, Action: ActionShortPick, LineNo: 1, Qty: dq("10"), Reason: "DAMAGED", User: "u1", Now: time.Now()})
	if err != nil || ex.Remainder == nil {
		t.Fatalf("short pick: %+v err=%v", ex, err)
	}
	confirmRest(t, st, ctx, refill.ID, "u1")
	if got := statusOf(t, st, ctx, refill.ID).Status; got != domain.WorkPartiallyComplete {
		t.Fatalf("refill = %s, want PARTIALLY_COMPLETED", got)
	}
	if p := statusOf(t, st, ctx, pick.ID); p.Status != domain.WorkWaiting || len(p.DependsOn) != 1 || p.DependsOn[0] != ex.Remainder.ID {
		t.Fatalf("the pick must wait for the remainder %s: %+v", ex.Remainder.ID, p)
	}
	next := assign(t, st, ctx, wh, "u2", time.Now())
	if next == nil || next.ID != ex.Remainder.ID {
		t.Fatalf("only the remainder is assignable: %+v", next)
	}
	confirmRest(t, st, ctx, next.ID, "u2")
	if p := statusOf(t, st, ctx, pick.ID); p.Status != domain.WorkOpen || len(p.DependsOn) != 0 {
		t.Fatalf("the pick must open once the remainder completes: %+v", p)
	}
	if n := outboxCount(t, adminOf(st), "dev.passage.wms.work.released.v1"); n != 1 {
		t.Fatalf("work.released events = %d, want 1", n)
	}
	last := assign(t, st, ctx, wh, "u3", time.Now())
	if last == nil || last.ID != pick.ID {
		t.Fatalf("the released pick must be assignable: %+v", last)
	}
}

// A cancelled refill opens its dependents: they run, and short pick honestly
// when the stock is not there.
func TestStore_CancelledRefillOpensTheWaitingPick(t *testing.T) {
	st, ctx, wh, rel := waitingPickFixture(t)
	pick, refill := rel.Picks[0], rel.Replenishment[0]
	if w := assign(t, st, ctx, wh, "u1", time.Now()); w == nil || w.ID != refill.ID {
		t.Fatalf("the refill must be assigned first: %+v", w)
	}
	if _, _, err := st.ExceptWork(ctx, key("cancel"), ExceptionInput{WorkID: refill.ID, Action: ActionCancel, Reason: "NO_RESERVE", User: "u1", Now: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if p := statusOf(t, st, ctx, pick.ID); p.Status != domain.WorkOpen || len(p.DependsOn) != 0 {
		t.Fatalf("the pick must open: %+v", p)
	}
	w := assign(t, st, ctx, wh, "u2", time.Now())
	if w == nil || w.ID != pick.ID {
		t.Fatalf("assigned %+v, want the pick", w)
	}
	if _, _, err := st.ConfirmLine(ctx, key("over"), ConfirmInput{WorkID: w.ID, LineNo: 1, Qty: dq("60"), User: "u2", Now: time.Now()}); err == nil {
		t.Fatal("confirming 60 against 50 on hand must be refused")
	}
	if _, _, err := st.ExceptWork(ctx, key("short"), ExceptionInput{WorkID: w.ID, Action: ActionShortPick, LineNo: 1, Qty: dq("50"), Reason: "STOCK", User: "u2", Now: time.Now()}); err != nil {
		t.Fatalf("short pick: %v", err)
	}
	if got := stockAt(t, st, ctx, wh, "F-01", "A"); !got.IsZero() {
		t.Fatalf("forward = %s, want 0", got)
	}
}
