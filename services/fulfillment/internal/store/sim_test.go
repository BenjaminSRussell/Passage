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

// Fixture warehouse-sim-1k (testdata/warehouse-sim-1k/README.md, section 5.28.9):
// 1,000 confirmed orders of 1 to 3 lines over 120 products, 3 warehouses, 150
// customers and 2 legal entities, generated from a fixed seed.
const (
	simOrders     = 1000
	simProducts   = 120
	simWarehouses = 3
	simCustomers  = 150
	simSeed       = 28
	simWorkers    = 8
)

type simLine struct {
	productID string
	qty       decimal.Decimal
	unitCost  decimal.Decimal
}

// simShip runs one released shipment through pick, pack and post. Every tenth
// shipment short-picks its first line by a tenth (at least one unit when the
// line has two or more), posts what was picked, re-picks the remainder and
// posts it on a second slip. It returns the quantity short-picked.
func simShip(ctx context.Context, s *store.Postgres, sh store.Shipment, short bool, lines map[string]simLine) (decimal.Decimal, error) {
	cost := map[string]store.Cost{}
	for _, l := range sh.Lines {
		cost[l.ID] = store.Cost{UnitCost: lines[l.OrderLineID].unitCost, CostSnapshotID: "snap-" + l.OrderLineID}
	}
	shortQty := decimal.Zero
	picks := make([]store.PickInput, len(sh.Lines))
	for i, l := range sh.Lines {
		picks[i] = store.PickInput{ShipmentLineID: l.ID, Picked: l.Planned}
	}
	if short && sh.Lines[0].Planned.GreaterThan(decimal.NewFromInt(1)) {
		shortQty = decimal.Max(sh.Lines[0].Planned.Div(decimal.NewFromInt(10)).Floor(), decimal.NewFromInt(1))
		picks[0].Picked = sh.Lines[0].Planned.Sub(shortQty)
	}
	shipRound := func(cur store.Shipment, picks []store.PickInput, round string) (store.Shipment, error) {
		picked, err := s.ConfirmPick(ctx, cur.ID, cur.Version, picks, idem("pick-"+round))
		if err != nil {
			return cur, fmt.Errorf("pick %s %s: %w", cur.ShipmentNumber, round, err)
		}
		var packs []store.PackInput
		for _, l := range picked.Lines {
			if open := l.Picked.Sub(l.Packed); open.IsPositive() {
				packs = append(packs, store.PackInput{ShipmentLineID: l.ID, Qty: open})
			}
		}
		packed, err := s.PackContainer(ctx, cur.ID, picked.Version, "CARTON-C12", packs, idem("pack-"+round))
		if err != nil {
			return cur, fmt.Errorf("pack %s %s: %w", cur.ShipmentNumber, round, err)
		}
		if _, err := s.PostPackingSlip(ctx, cur.ID, packed.Shipment.Version, store.PostInput{PostingDate: today(), Currency: "USD", Costs: cost}, idem("post-"+round)); err != nil {
			return cur, fmt.Errorf("post %s %s: %w", cur.ShipmentNumber, round, err)
		}
		return s.GetShipment(ctx, cur.ID)
	}
	after, err := shipRound(sh, picks, "1")
	if err != nil {
		return shortQty, err
	}
	if shortQty.IsPositive() {
		if after.Status != domain.StatusPartiallyShipped {
			return shortQty, fmt.Errorf("%s after a short pick is %s, want PARTIALLY_SHIPPED", sh.ShipmentNumber, after.Status)
		}
		// The re-pick confirms the remainder line at its planned total (FR-28.8).
		l := after.Lines[0]
		if after, err = shipRound(after, []store.PickInput{{ShipmentLineID: l.ID, Picked: l.Planned}}, "2"); err != nil {
			return shortQty, err
		}
	}
	if after.Status != domain.StatusShipped {
		return shortQty, fmt.Errorf("%s ends %s, want SHIPPED", sh.ShipmentNumber, after.Status)
	}
	return shortQty, nil
}

// TestWarehouseSim1k_ZeroInventoryMismatch is AC-28.7 (section 5.28.9
// "pick-pack-ship 1k E2E") against Postgres: 1,000 confirmed orders released,
// picked (one in ten short, the remainder re-picked), packed and posted. Every
// order ends SHIPPED; per product, the quantities in the packingslip.posted
// events (what inventory issues from, wave 16) equal the packing slip lines
// and the released quantities, with zero mismatch; Dr 1405 equals Cr 1400
// equals the shipped cost; the short-pick report holds every short pick, all
// closed, and pick.confirmed carried each backordered quantity.
func TestWarehouseSim1k_ZeroInventoryMismatch(t *testing.T) {
	if testing.Short() {
		t.Skip("1,000-order simulation skipped in -short mode")
	}
	s, admin := setup(t)
	ctx := ctxFor(28, "")
	rng := rand.New(rand.NewSource(simSeed))
	newIDs := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = ids.New()
		}
		return out
	}
	products, warehouses, customers, entities := newIDs(simProducts), newIDs(simWarehouses), newIDs(simCustomers), newIDs(2)

	lines := map[string]simLine{} // by order line id
	released := map[string]decimal.Decimal{}
	byWarehouse := map[string][]domain.ReleaseLine{}
	for o := 0; o < simOrders; o++ {
		order, customer := ids.New(), customers[rng.Intn(simCustomers)]
		wh, le := warehouses[rng.Intn(simWarehouses)], entities[o%2]
		ship := today().AddDate(0, 0, rng.Intn(3))
		for k := 0; k < 1+rng.Intn(3); k++ {
			l := simLine{productID: products[rng.Intn(simProducts)], qty: decimal.NewFromInt(int64(1 + rng.Intn(100))),
				unitCost: decimal.New(int64(100+rng.Intn(4900)), -2)}
			ol := ids.New()
			lines[ol] = l
			released[l.productID] = released[l.productID].Add(l.qty)
			byWarehouse[wh] = append(byWarehouse[wh], domain.ReleaseLine{OrderID: order, OrderLineID: ol, CustomerID: customer, LegalEntityID: le,
				WarehouseID: wh, ProductID: l.productID, Unit: "EA", Qty: l.qty, ConfirmedShipDate: ship, ATPOK: true})
		}
	}

	var shipments []store.Shipment
	for _, wh := range warehouses {
		groups, skipped := domain.Release(byWarehouse[wh], domain.ReleaseRule{Today: today(), HorizonDays: 2, RequireFullATP: true})
		if len(skipped) != 0 {
			t.Fatalf("release skipped %d lines: %+v", len(skipped), skipped[0])
		}
		res, err := s.RunRelease(ctx, wh, groups, idem("rel-"+wh))
		if err != nil || len(res.Skipped) != 0 {
			t.Fatalf("RunRelease %s: %d skipped, %v", wh, len(res.Skipped), err)
		}
		// The simulation re-picks short remainders, so the warehouse keeps them open.
		if cur, err := s.GetWarehouseSetting(ctx, wh); err != nil || cur.LegalEntityID == nil {
			t.Fatalf("warehouse %s setting: %+v, %v", wh, cur, err)
		} else {
			keepOpen(t, s, ctx, wh, *cur.LegalEntityID)
		}
		shipments = append(shipments, res.Shipments...)
	}
	if len(shipments) != simOrders {
		t.Fatalf("released %d shipments, want one per order (%d)", len(shipments), simOrders)
	}

	var (
		mu        sync.Mutex
		failures  []error
		shortSum  = decimal.Zero
		shortRows = 0
		wg        sync.WaitGroup
		work      = make(chan int)
	)
	for w := 0; w < simWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range work {
				short, err := simShip(ctx, s, shipments[i], i%10 == 0, lines)
				mu.Lock()
				if err != nil {
					failures = append(failures, err)
				} else if short.IsPositive() {
					shortSum, shortRows = shortSum.Add(short), shortRows+1
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
	if shortRows == 0 {
		t.Fatal("the simulation made no short pick")
	}

	bg := context.Background()
	var notShipped, orders int
	if err := admin.QueryRow(bg, `select count(*) filter (where status <> 'SHIPPED'), count(distinct order_id) from shipment`).Scan(&notShipped, &orders); err != nil {
		t.Fatal(err)
	}
	if notShipped != 0 || orders != simOrders {
		t.Fatalf("%d shipments not SHIPPED, %d orders; want 0 and %d", notShipped, orders, simOrders)
	}

	// What inventory issues from: the packingslip.posted events.
	issued := map[string]decimal.Decimal{}
	dr1405, cr1400, cost := decimal.Zero, decimal.Zero, decimal.Zero
	rows, err := admin.Query(bg, `select payload->'data' from outbox where type = $1`, fulevents.PackingSlipPosted)
	if err != nil {
		t.Fatal(err)
	}
	slips := 0
	for rows.Next() {
		var raw []byte
		var data fulevents.PackingSlipPostedData
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &data); err != nil {
			t.Fatal(err)
		}
		slips++
		for _, l := range data.Lines {
			sl := lines[l.OrderLineID]
			qty := decimal.RequireFromString(l.Qty)
			issued[sl.productID] = issued[sl.productID].Add(qty)
			cost = cost.Add(qty.Mul(sl.unitCost))
		}
		for _, j := range data.Journal {
			switch j.Account {
			case domain.AccountDeliveredNotInvoiced:
				dr1405 = dr1405.Add(decimal.RequireFromString(j.Amount))
			case domain.AccountInventory:
				cr1400 = cr1400.Add(decimal.RequireFromString(j.Amount))
			default:
				t.Fatalf("physical posting touched account %s", j.Account)
			}
		}
	}
	rows.Close()
	if slips != simOrders+shortRows {
		t.Fatalf("%d slips posted, want %d (one per order plus one per remainder)", slips, simOrders+shortRows)
	}

	// The stored packing slip lines per product.
	slipped := map[string]decimal.Decimal{}
	srows, err := admin.Query(bg, `select sl.product_id::text, sum(psl.qty)::text from packing_slip_line psl
		join shipment_line sl on sl.tenant_id = psl.tenant_id and sl.id = psl.shipment_line_id group by sl.product_id`)
	if err != nil {
		t.Fatal(err)
	}
	for srows.Next() {
		var product, qty string
		if err := srows.Scan(&product, &qty); err != nil {
			t.Fatal(err)
		}
		slipped[product] = decimal.RequireFromString(qty)
	}
	srows.Close()

	keys := make([]string, 0, len(released))
	for p := range released {
		keys = append(keys, p)
	}
	sort.Strings(keys)
	mismatches := 0
	for _, p := range keys {
		if !issued[p].Equal(released[p]) || !slipped[p].Equal(released[p]) {
			mismatches++
			t.Errorf("product %s: released %s, slip lines %s, posted events %s", p, released[p], slipped[p], issued[p])
		}
	}
	if mismatches != 0 || len(issued) != len(released) || len(slipped) != len(released) {
		t.Fatalf("inventory mismatch on %d products (%d released, %d slipped, %d issued)", mismatches, len(released), len(slipped), len(issued))
	}
	t.Logf("%d orders, %d lines over %d products, %d short picks (%s units), %d slips, shipped cost %s",
		simOrders, len(lines), len(released), shortRows, shortSum, slips, cost)
	if !dr1405.Equal(cost) || !cr1400.Equal(cost.Neg()) {
		t.Fatalf("Dr 1405 %s, Cr 1400 %s; want %s and -%s", dr1405, cr1400, cost, cost)
	}

	// FR-28.2: every short pick is on the report, closed by its re-pick.
	if open := backorders(t, s, ctx, store.BackorderFilter{Limit: 500}); len(open) != 0 {
		t.Fatalf("%d backorders still open", len(open))
	}
	if all := backorders(t, s, ctx, store.BackorderFilter{IncludeClosed: true, Limit: 500}); len(all) != shortRows {
		t.Fatalf("short-pick report has %d lines, want %d", len(all), shortRows)
	}
	var backordered string
	if err := admin.QueryRow(bg, `select coalesce(sum((l->>'backordered')::numeric), 0)::text
		from outbox, jsonb_array_elements(payload->'data'->'lines') l where type = $1`, fulevents.PickConfirmed).Scan(&backordered); err != nil {
		t.Fatal(err)
	}
	if !decimal.RequireFromString(backordered).Equal(shortSum) {
		t.Fatalf("pick.confirmed backordered %s, want %s", backordered, shortSum)
	}
}
