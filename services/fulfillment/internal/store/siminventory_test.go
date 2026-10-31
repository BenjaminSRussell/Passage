package store_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"sort"
	"sync"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/domain"
	fulevents "github.com/BenjaminSRussell/passage/services/fulfillment/internal/events"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/store"
)

// Simulated warehouse for AC-28.7 with an inventory side (warehouse-sim-1k,
// "batch/serial mix"): the same shape as TestWarehouseSim1k_ZeroInventoryMismatch
// (1,000 orders, 120 products, 3 warehouses) from its own seed, plus batch and
// serial tracked products, slip cancellations and stock that is seeded at or
// just above demand. Two ledgers are kept and compared:
//
//   - the floor: what the simulated pickers physically took off the shelves
//     (the batches and serials they chose and scanned) and put back when a slip
//     was cancelled, written by this test as it drives the store;
//   - the projection: on-hand per warehouse, product, batch and serial rebuilt
//     only from the outbox events, the way inventory (wave 16) issues stock from
//     packingslip.posted and reverses the issue of the slip named by
//     packingslip.cancelled.
//
// Zero inventory mismatch means both ledgers agree on every warehouse, product,
// batch and serial, no balance goes negative while the events replay, and the
// final on-hand of every warehouse and product is its seeded stock minus its
// released demand, computed from the generator alone.
const (
	invSeed   = 2828
	invBuffer = 21 // seeded stock = demand + 0..20 units per warehouse and product
)

type stockKey struct{ wh, product, batch, serial string }

type invLine struct {
	product, wh string
	qty, cost   decimal.Decimal
}

// floor is the simulated warehouse: physical on-hand, what is still free to
// pick, the batch and serial order pickers follow, and what each slip took.
type floor struct {
	mu       sync.Mutex
	onHand   map[stockKey]decimal.Decimal
	free     map[stockKey]decimal.Decimal
	batches  map[[2]string][]string
	serials  map[[2]string][]string
	slipTook map[string][]move
}

type move struct {
	key stockKey
	qty decimal.Decimal
}

// allocate picks qty of a tracked product the way a picker does: oldest batch
// first, lowest serial first. It returns the scanned entries and their stock.
func (f *floor) allocate(wh, product, tracking string, qty decimal.Decimal) ([]domain.TrackingEntry, []move, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var entries []domain.TrackingEntry
	var moves []move
	need := qty
	switch tracking {
	case "batch":
		for _, b := range f.batches[[2]string{wh, product}] {
			k := stockKey{wh, product, b, ""}
			take := decimal.Min(need, f.free[k])
			if !take.IsPositive() {
				continue
			}
			f.free[k] = f.free[k].Sub(take)
			entries = append(entries, domain.TrackingEntry{BatchNo: b, LocationCode: "BIN-" + wh[:4], Qty: take})
			moves = append(moves, move{k, take})
			if need = need.Sub(take); need.IsZero() {
				break
			}
		}
	case "serial":
		for _, sn := range f.serials[[2]string{wh, product}] {
			k := stockKey{wh, product, "", sn}
			if !need.IsPositive() {
				break
			}
			if !f.free[k].IsPositive() {
				continue
			}
			f.free[k] = decimal.Zero
			entries = append(entries, domain.TrackingEntry{SerialNo: sn, LocationCode: "BIN-" + wh[:4], Qty: decimal.NewFromInt(1)})
			moves = append(moves, move{k, decimal.NewFromInt(1)})
			need = need.Sub(decimal.NewFromInt(1))
		}
	}
	if need.IsPositive() {
		return nil, nil, fmt.Errorf("floor has %s too few units of %s in %s", need, product, wh)
	}
	return entries, moves, nil
}

// ship records a posted slip leaving the dock; unship a cancelled slip coming back.
func (f *floor) ship(slipID string, moves []move) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.slipTook[slipID] = moves
	for _, m := range moves {
		f.onHand[m.key] = f.onHand[m.key].Sub(m.qty)
	}
}

func (f *floor) unship(slipID string) []move {
	f.mu.Lock()
	defer f.mu.Unlock()
	moves := f.slipTook[slipID]
	for _, m := range moves {
		f.onHand[m.key] = f.onHand[m.key].Add(m.qty)
	}
	return moves
}

// invShip drives one shipment: pick (every tenth short on its first line),
// scan batches and serials of tracked lines, pack, post; every seventh shipment
// cancels its first slip and posts it again; a short pick is re-picked, scanned
// and posted on a second slip. It returns whether it short-picked and cancelled.
func invShip(ctx context.Context, s *store.Postgres, fl *floor, sh store.Shipment, idx int, lines map[string]invLine, tracking map[string]string) (bool, bool, error) {
	cost := map[string]store.Cost{}
	for _, l := range sh.Lines {
		cost[l.ID] = store.Cost{UnitCost: lines[l.OrderLineID].cost, CostSnapshotID: "snap-" + l.OrderLineID}
	}
	short := idx%10 == 0 && sh.Lines[0].Planned.GreaterThan(decimal.NewFromInt(1))
	cancel := idx%7 == 0
	picks := make([]store.PickInput, len(sh.Lines))
	for i, l := range sh.Lines {
		picks[i] = store.PickInput{ShipmentLineID: l.ID, Picked: l.Planned}
	}
	shortQty := decimal.Zero
	if short {
		shortQty = decimal.Max(sh.Lines[0].Planned.Div(decimal.NewFromInt(10)).Floor(), decimal.NewFromInt(1))
		picks[0].Picked = sh.Lines[0].Planned.Sub(shortQty)
	}
	post := func(cur store.Shipment, round string) (store.PackingSlip, error) {
		slip, err := s.PostPackingSlip(ctx, cur.ID, cur.Version, store.PostInput{PostingDate: today(), Currency: "USD", Costs: cost}, idem("post-"+round))
		if err != nil {
			return slip, fmt.Errorf("post %s %s: %w", cur.ShipmentNumber, round, err)
		}
		return slip, nil
	}
	// round picks, scans (tracked lines: qty per line), packs and posts.
	round := func(cur store.Shipment, picks []store.PickInput, scan map[string]decimal.Decimal, name string) (store.Shipment, string, error) {
		cur, err := s.ConfirmPick(ctx, cur.ID, cur.Version, picks, idem("pick-"+name))
		if err != nil {
			return cur, "", fmt.Errorf("pick %s %s: %w", cur.ShipmentNumber, name, err)
		}
		var moves []move
		for _, l := range cur.Lines {
			il := lines[l.OrderLineID]
			q, ok := scan[l.ID]
			if !ok || !q.IsPositive() {
				continue
			}
			if tr := tracking[il.product]; tr != "" {
				entries, m, err := fl.allocate(il.wh, il.product, tr, q)
				if err != nil {
					return cur, "", err
				}
				if cur, err = s.CaptureTracking(ctx, cur.ID, l.ID, cur.Version, entries, idem("cap-"+name)); err != nil {
					return cur, "", fmt.Errorf("capture %s %s: %w", cur.ShipmentNumber, name, err)
				}
				moves = append(moves, m...)
			} else {
				moves = append(moves, move{stockKey{il.wh, il.product, "", ""}, q})
			}
		}
		var packs []store.PackInput
		for _, l := range cur.Lines {
			if open := l.Picked.Sub(l.Packed); open.IsPositive() {
				packs = append(packs, store.PackInput{ShipmentLineID: l.ID, Qty: open})
			}
		}
		packed, err := s.PackContainer(ctx, cur.ID, cur.Version, "CARTON-C12", packs, idem("pack-"+name))
		if err != nil {
			return cur, "", fmt.Errorf("pack %s %s: %w", cur.ShipmentNumber, name, err)
		}
		slip, err := post(packed.Shipment, name)
		if err != nil {
			return cur, "", err
		}
		fl.ship(slip.ID, moves)
		after, err := s.GetShipment(ctx, cur.ID)
		return after, slip.ID, err
	}

	scan1 := map[string]decimal.Decimal{}
	for _, p := range picks {
		scan1[p.ShipmentLineID] = p.Picked
	}
	after, slip1, err := round(sh, picks, scan1, "1")
	if err != nil {
		return short, cancel, err
	}
	if cancel {
		if _, err := s.CancelPackingSlip(ctx, slip1, idem("cancel")); err != nil {
			return short, cancel, fmt.Errorf("cancel %s: %w", sh.ShipmentNumber, err)
		}
		moves := fl.unship(slip1)
		back, err := s.GetShipment(ctx, sh.ID)
		if err != nil {
			return short, cancel, err
		}
		// The scanned entries went back on the pick; the same goods post again.
		slip, err := post(back, "1b")
		if err != nil {
			return short, cancel, err
		}
		fl.ship(slip.ID, moves)
		if after, err = s.GetShipment(ctx, sh.ID); err != nil {
			return short, cancel, err
		}
	}
	if short {
		if after.Status != domain.StatusPartiallyShipped {
			return short, cancel, fmt.Errorf("%s after a short pick is %s, want PARTIALLY_SHIPPED", sh.ShipmentNumber, after.Status)
		}
		l := after.Lines[0]
		if after, _, err = round(after, []store.PickInput{{ShipmentLineID: l.ID, Picked: l.Planned}}, map[string]decimal.Decimal{l.ID: shortQty}, "2"); err != nil {
			return short, cancel, err
		}
	}
	if after.Status != domain.StatusShipped {
		return short, cancel, fmt.Errorf("%s ends %s, want SHIPPED", sh.ShipmentNumber, after.Status)
	}
	return short, cancel, nil
}

func TestWarehouseSim1k_SimulatedWarehouseOnHand(t *testing.T) {
	if testing.Short() {
		t.Skip("1,000-order simulation skipped in -short mode")
	}
	s, admin := setup(t)
	ctx := ctxFor(29, "")
	rng := rand.New(rand.NewSource(invSeed))
	newIDs := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = ids.New()
		}
		return out
	}
	products, warehouses, customers, entities := newIDs(simProducts), newIDs(simWarehouses), newIDs(simCustomers), newIDs(2)
	// Tracking per product from the seed: 7 in 10 untracked, 2 batch, 1 serial.
	tracking, pIndex := map[string]string{}, map[string]int{}
	for i, p := range products {
		pIndex[p] = i
		switch r := rng.Intn(10); {
		case r >= 9:
			tracking[p] = "serial"
		case r >= 7:
			tracking[p] = "batch"
		}
	}

	lines := map[string]invLine{}
	demand := map[[2]string]decimal.Decimal{}
	byWarehouse := map[string][]domain.ReleaseLine{}
	for o := 0; o < simOrders; o++ {
		order, customer := ids.New(), customers[rng.Intn(simCustomers)]
		wh, le := warehouses[rng.Intn(simWarehouses)], entities[o%2]
		ship := today().AddDate(0, 0, rng.Intn(3))
		for k := 0; k < 1+rng.Intn(3); k++ {
			p := products[rng.Intn(simProducts)]
			l := invLine{product: p, wh: wh, qty: decimal.NewFromInt(int64(1 + rng.Intn(100))), cost: decimal.New(int64(100+rng.Intn(4900)), -2)}
			ol := ids.New()
			lines[ol] = l
			demand[[2]string{wh, p}] = demand[[2]string{wh, p}].Add(l.qty)
			byWarehouse[wh] = append(byWarehouse[wh], domain.ReleaseLine{OrderID: order, OrderLineID: ol, CustomerID: customer, LegalEntityID: le,
				WarehouseID: wh, ProductID: p, Unit: "EA", Qty: l.qty, ConfirmedShipDate: ship, ATPOK: true, TrackingRequired: tracking[p] != ""})
		}
	}

	// Seed the shelves: demand plus 0..20 units, batches split 1 to 3 ways,
	// one serial per unit. expected is seeded stock minus demand.
	fl := &floor{onHand: map[stockKey]decimal.Decimal{}, free: map[stockKey]decimal.Decimal{}, batches: map[[2]string][]string{},
		serials: map[[2]string][]string{}, slipTook: map[string][]move{}}
	seeded, expected := map[[2]string]decimal.Decimal{}, map[[2]string]decimal.Decimal{}
	keys := make([][2]string, 0, len(demand))
	for k := range demand {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(a, b int) bool { return keys[a][0]+keys[a][1] < keys[b][0]+keys[b][1] })
	zeroBuffer := 0
	for _, k := range keys {
		buf := int64(rng.Intn(invBuffer))
		if buf == 0 {
			zeroBuffer++
		}
		total := demand[k].Add(decimal.NewFromInt(buf))
		seeded[k], expected[k] = total, decimal.NewFromInt(buf)
		wh, p := k[0], k[1]
		switch tracking[p] {
		case "batch":
			n := 1 + rng.Intn(3)
			left := total.IntPart()
			for b := 0; b < n; b++ {
				q := left
				if b < n-1 {
					q = rng.Int63n(left + 1)
				}
				left -= q
				code := fmt.Sprintf("B%03d-W%d-%d", pIndex[p], indexOf(warehouses, wh), b+1)
				fl.batches[k] = append(fl.batches[k], code)
				key := stockKey{wh, p, code, ""}
				fl.onHand[key], fl.free[key] = decimal.NewFromInt(q), decimal.NewFromInt(q)
			}
		case "serial":
			for n := int64(1); n <= total.IntPart(); n++ {
				sn := fmt.Sprintf("S%03d-W%d-%05d", pIndex[p], indexOf(warehouses, wh), n)
				fl.serials[k] = append(fl.serials[k], sn)
				key := stockKey{wh, p, "", sn}
				fl.onHand[key], fl.free[key] = decimal.NewFromInt(1), decimal.NewFromInt(1)
			}
		default:
			fl.onHand[stockKey{wh, p, "", ""}] = total
		}
	}
	initial := map[stockKey]decimal.Decimal{}
	for k, v := range fl.onHand {
		initial[k] = v
	}

	var shipments []store.Shipment
	for _, wh := range warehouses {
		groups, skipped := domain.Release(byWarehouse[wh], domain.ReleaseRule{Today: today(), HorizonDays: 2, RequireFullATP: true})
		if len(skipped) != 0 {
			t.Fatalf("release skipped %d lines", len(skipped))
		}
		res, err := s.RunRelease(ctx, wh, groups, idem("rel-"+wh))
		if err != nil || len(res.Skipped) != 0 {
			t.Fatalf("RunRelease %s: %d skipped, %v", wh, len(res.Skipped), err)
		}
		shipments = append(shipments, res.Shipments...)
	}
	if len(shipments) != simOrders {
		t.Fatalf("released %d shipments, want %d", len(shipments), simOrders)
	}

	var (
		mu                sync.Mutex
		failures          []error
		shorts, cancelled int
		wg                sync.WaitGroup
		work              = make(chan int)
	)
	for w := 0; w < simWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range work {
				short, cancel, err := invShip(ctx, s, fl, shipments[i], i, lines, tracking)
				mu.Lock()
				if err != nil {
					failures = append(failures, err)
				} else {
					if short {
						shorts++
					}
					if cancel {
						cancelled++
					}
				}
				mu.Unlock()
			}
		}()
	}
	for i := range shipments {
		work <- i
	}
	close(work)
	wg.Wait()
	if len(failures) > 0 {
		t.Fatalf("%d shipments failed; first: %v", len(failures), failures[0])
	}

	bg := context.Background()
	var notShipped int
	if err := admin.QueryRow(bg, `select count(*) filter (where status <> 'SHIPPED') from shipment`).Scan(&notShipped); err != nil {
		t.Fatal(err)
	}
	if notShipped != 0 {
		t.Fatalf("%d shipments not SHIPPED", notShipped)
	}

	// The projection: replay posted and cancelled events per shipment in order.
	proj := map[stockKey]decimal.Decimal{}
	for k, v := range initial {
		proj[k] = v
	}
	rows, err := admin.Query(bg, `select aggregate_id, type, payload->'data' from outbox where type = any($1) order by aggregate_id, created_at, id`,
		[]string{fulevents.PackingSlipPosted, fulevents.PackingSlipCancelled})
	if err != nil {
		t.Fatal(err)
	}
	issuedBySlip := map[string][]move{}
	var problems []string
	trackedLines, entries, postedEvents, cancelEvents := 0, 0, 0, 0
	for rows.Next() {
		var agg, typ string
		var raw []byte
		if err := rows.Scan(&agg, &typ, &raw); err != nil {
			t.Fatal(err)
		}
		if typ == fulevents.PackingSlipCancelled {
			cancelEvents++
			var c fulevents.PackingSlipCancelledData
			if err := json.Unmarshal(raw, &c); err != nil {
				t.Fatal(err)
			}
			issued, ok := issuedBySlip[c.PackingSlipID]
			if !ok {
				problems = append(problems, "cancellation of a slip never issued: "+c.PackingSlipID)
				continue
			}
			var back decimal.Decimal
			for _, l := range c.Lines {
				back = back.Sub(dq(l.Qty))
			}
			var was decimal.Decimal
			for _, m := range issued {
				was = was.Add(m.qty)
			}
			if !back.Equal(was) {
				problems = append(problems, fmt.Sprintf("slip %s: cancellation returns %s, the slip issued %s", c.PackingSlipID, back, was))
			}
			for _, m := range issued {
				proj[m.key] = proj[m.key].Add(m.qty)
			}
			delete(issuedBySlip, c.PackingSlipID)
			continue
		}
		postedEvents++
		var p fulevents.PackingSlipPostedData
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Fatal(err)
		}
		var moves []move
		for _, l := range p.Lines {
			il, ok := lines[l.OrderLineID]
			if !ok || l.ProductID != il.product {
				problems = append(problems, fmt.Sprintf("slip %s line %s: product %q not the released one", p.PackingSlipNumber, l.OrderLineID, l.ProductID))
				continue
			}
			qty := dq(l.Qty)
			if tracking[il.product] == "" {
				if len(l.Tracking) != 0 {
					problems = append(problems, fmt.Sprintf("slip %s: untracked line carries tracking", p.PackingSlipNumber))
				}
				moves = append(moves, move{stockKey{il.wh, il.product, "", ""}, qty})
				continue
			}
			trackedLines++
			covered := decimal.Zero
			for _, e := range l.Tracking {
				entries++
				covered = covered.Add(dq(e.Qty))
				moves = append(moves, move{stockKey{il.wh, il.product, e.BatchNo, e.SerialNo}, dq(e.Qty)})
			}
			if !covered.Equal(qty) {
				problems = append(problems, fmt.Sprintf("slip %s line %s: tracking covers %s of %s", p.PackingSlipNumber, l.OrderLineID, covered, qty))
			}
		}
		for _, m := range moves {
			if _, known := initial[m.key]; !known {
				problems = append(problems, fmt.Sprintf("slip %s issues unknown stock %+v", p.PackingSlipNumber, m.key))
				continue
			}
			proj[m.key] = proj[m.key].Sub(m.qty)
			if proj[m.key].IsNegative() {
				problems = append(problems, fmt.Sprintf("slip %s drives %+v to %s", p.PackingSlipNumber, m.key, proj[m.key]))
			}
		}
		issuedBySlip[p.PackingSlipID] = moves
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	mismatches := 0
	for k, v := range fl.onHand {
		if !proj[k].Equal(v) {
			mismatches++
			if mismatches <= 5 {
				problems = append(problems, fmt.Sprintf("%+v: floor %s, projection %s", k, v, proj[k]))
			}
		}
	}
	totals := map[[2]string]decimal.Decimal{}
	for k, v := range proj {
		totals[[2]string{k.wh, k.product}] = totals[[2]string{k.wh, k.product}].Add(v)
	}
	for k, want := range expected {
		if !totals[k].Equal(want) {
			mismatches++
			problems = append(problems, fmt.Sprintf("warehouse %s product %s: on-hand %s, want seeded %s - demand %s = %s", k[0], k[1], totals[k], seeded[k], demand[k], want))
		}
	}
	t.Logf("%d orders, %d lines; products %d untracked / %d batch / %d serial; %d warehouse-products seeded with no spare unit; "+
		"%d short picks, %d slips cancelled and re-posted, %d posted and %d cancelled events, %d tracked slip lines with %d batch/serial entries; "+
		"%d stock keys, %d mismatches",
		simOrders, len(lines), simProducts-countKind(tracking, "batch")-countKind(tracking, "serial"), countKind(tracking, "batch"), countKind(tracking, "serial"),
		zeroBuffer, shorts, cancelled, postedEvents, cancelEvents, trackedLines, entries, len(fl.onHand), mismatches)
	if len(problems) > 0 || mismatches != 0 {
		t.Fatalf("inventory mismatch (%d): %v", mismatches, problems[:min(len(problems), 10)])
	}
	if cancelEvents != cancelled || postedEvents != simOrders+shorts+cancelled || shorts == 0 || cancelled == 0 || trackedLines == 0 {
		t.Fatalf("events %d posted / %d cancelled for %d orders, %d shorts, %d cancels, %d tracked lines", postedEvents, cancelEvents, simOrders, shorts, cancelled, trackedLines)
	}
}

func indexOf(xs []string, x string) int {
	for i, v := range xs {
		if v == x {
			return i
		}
	}
	return -1
}

func countKind(m map[string]string, kind string) int {
	n := 0
	for _, v := range m {
		if v == kind {
			n++
		}
	}
	return n
}
