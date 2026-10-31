package store

import (
	"context"
	"fmt"
	"os"
	"slices"
	"sync"
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
// (10 waves of 50 orders of 20 lines) is released and worked by 10 concurrent
// pickers. No call fails (a deadlock aborts a transaction with an error), every
// work completes, the stock falls by exactly the lines' quantities, and the p95
// of work assignment is under one second. The default run is a tenth of the
// day; PASSAGE_WMS_SIM_10K=1 runs all of it.
func TestStore_Simulation10kLinesPerDay(t *testing.T) {
	waves := 1
	if os.Getenv("PASSAGE_WMS_SIM_10K") == "1" {
		waves = 10
	}
	const ordersPerWave, linesPerOrder, pickers, skus = 50, 20, 10, 50
	st, ctx, wh := setup(t)
	var locs []domain.Location
	for s := range skus {
		locs = append(locs, loc(fmt.Sprintf("F-%02d", s), "FWD", s, domain.Capacity{}))
	}
	mustLocations(t, st, ctx, wh, locs...)
	mustDirective(t, st, ctx, pickDirective(wh))
	for s := range skus {
		seedStock(t, st, ctx, fmt.Sprintf("F-%02d", s), locs, fmt.Sprintf("SKU-%02d", s), "1000000")
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
		if _, _, err := st.ReleaseWave(ctx, key("sim-wave-"+ids.New()), WaveInput{WarehouseID: wh, Orders: orders}); err != nil {
			t.Fatalf("wave %d: %v", w, err)
		}
		works += ordersPerWave
	}

	var (
		mu        sync.Mutex
		latencies []time.Duration
		completed int
		failures  []error
		wg        sync.WaitGroup
	)
	for p := range pickers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			user := fmt.Sprintf("picker-%d", p)
			for {
				started := time.Now()
				a, _, err := st.AssignNext(ctx, key("assign-"+ids.New()), wh, user, 30*time.Minute, started)
				took := time.Since(started)
				mu.Lock()
				latencies = append(latencies, took)
				if err != nil {
					failures = append(failures, err)
				}
				mu.Unlock()
				if err != nil || a.Work == nil {
					return
				}
				for _, l := range a.Work.Lines {
					if _, _, err := st.ConfirmLine(ctx, key("confirm-"+ids.New()), ConfirmInput{WorkID: a.Work.ID, LineNo: l.LineNo, Qty: l.Qty, User: user, Now: time.Now()}); err != nil {
						mu.Lock()
						failures = append(failures, err)
						mu.Unlock()
						return
					}
				}
				mu.Lock()
				completed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if len(failures) > 0 {
		t.Fatalf("%d calls failed, first: %v", len(failures), failures[0])
	}
	if completed != works {
		t.Fatalf("completed %d of %d works", completed, works)
	}
	slices.Sort(latencies)
	p95 := latencies[len(latencies)*95/100]
	t.Logf("%d lines, %d works, %d assignments, assign p95 %s, max %s", works*linesPerOrder, works, len(latencies), p95, latencies[len(latencies)-1])
	if p95 >= time.Second {
		t.Fatalf("assignment p95 = %s, want under 1s", p95)
	}
	for item, qty := range picked {
		s := item[len("SKU-"):]
		if got := stockAt(t, st, ctx, wh, "F-"+s, item); !got.Equal(decimal.NewFromInt(int64(1000000 - qty))) {
			t.Errorf("%s stock = %s, want %d", item, got, 1000000-qty)
		}
	}
}
