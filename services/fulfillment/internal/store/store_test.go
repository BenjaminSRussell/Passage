package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/domain"
	fulevents "github.com/BenjaminSRussell/passage/services/fulfillment/internal/events"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/store"
	"github.com/BenjaminSRussell/passage/services/fulfillment/migrations"
)

// setup needs PASSAGE_TEST_DATABASE_URL (testkit skips without it) and
// PASSAGE_TEST_APP_DATABASE_URL: the store runs as the application role
// (NOSUPERUSER NOBYPASSRLS), so row-level security applies as in production.
// admin applies the migrations and reads the outbox for assertions.
func setup(t *testing.T) (*store.Postgres, *pgxpool.Pool) {
	t.Helper()
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return store.New(app, "0614141", 0), admin
}

// ctxFor is a principal of tenant n; scope is its legal-entity scope ("" = all).
func ctxFor(n int, scope string) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(n), LegalEntityID: scope, UserID: "user-test",
		Duties: testkit.ScopeDuties(scope)})
}

func idem(name string) store.Idem { return store.Idem{Key: name + "-" + ids.New(), Hash: "h-" + name} }

func problemCode(err error) string {
	var p *httpx.Problem
	if errors.As(err, &p) {
		return p.Code
	}
	return ""
}

func today() time.Time {
	now := time.Now().UTC()
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
}

type fixture struct {
	legalEntity, warehouse string
	shipment               store.Shipment
}

// keepOpen saves KEEP_OPEN for a warehouse: most tests re-pick the remainder of
// a partly shipped shipment, which is no longer the default (REMAINDER_BACKORDER).
func keepOpen(t *testing.T, s *store.Postgres, ctx context.Context, warehouse, legalEntity string) {
	t.Helper()
	if _, err := s.SaveWarehouseSetting(ctx, warehouse, legalEntity, 0, domain.ActionKeepOpen, idem("keep-open")); err != nil {
		t.Fatalf("save KEEP_OPEN: %v", err)
	}
}

// released releases one order with the given line quantities and returns its
// shipment; the warehouse is set to KEEP_OPEN.
func released(t *testing.T, s *store.Postgres, ctx context.Context, qtys ...int64) fixture {
	t.Helper()
	f := releasedDefault(t, s, ctx, qtys...)
	keepOpen(t, s, ctx, f.warehouse, f.legalEntity)
	return f
}

// releasedDefault is released for a warehouse with no saved setting.
func releasedDefault(t *testing.T, s *store.Postgres, ctx context.Context, qtys ...int64) fixture {
	t.Helper()
	return releasedIn(t, s, ctx, ids.New(), ids.New(), qtys...)
}

// releasedIn releases one order of legalEntity in warehouse.
func releasedIn(t *testing.T, s *store.Postgres, ctx context.Context, warehouse, legalEntity string, qtys ...int64) fixture {
	t.Helper()
	return releasedOf(t, s, ctx, warehouse, legalEntity, nil, qtys...)
}

// releasedOf is releasedIn with the product of each line named (a fresh one when products is nil).
func releasedOf(t *testing.T, s *store.Postgres, ctx context.Context, warehouse, legalEntity string, products []string, qtys ...int64) fixture {
	t.Helper()
	f := fixture{legalEntity: legalEntity, warehouse: warehouse}
	order, customer := ids.New(), ids.New()
	var lines []domain.ReleaseLine
	for i, q := range qtys {
		product := ids.New()
		if products != nil {
			product = products[i]
		}
		lines = append(lines, domain.ReleaseLine{OrderID: order, OrderLineID: ids.New(), CustomerID: customer, LegalEntityID: f.legalEntity,
			WarehouseID: f.warehouse, ProductID: product, Unit: "EA", Qty: decimal.NewFromInt(q), ConfirmedShipDate: today(), ATPOK: true})
	}
	groups, skipped := domain.Release(lines, domain.ReleaseRule{Today: today(), HorizonDays: 2, RequireFullATP: true})
	if len(skipped) != 0 || len(groups) != 1 {
		t.Fatalf("release: groups=%d skipped=%v", len(groups), skipped)
	}
	res, err := s.RunRelease(ctx, f.warehouse, groups, idem("rel"))
	if err != nil || len(res.Shipments) != 1 {
		t.Fatalf("RunRelease: %+v, %v", res, err)
	}
	f.shipment = res.Shipments[0]
	return f
}

// pickAndPack confirms picks of every line at the planned quantity and packs them in one container.
func pickAndPack(t *testing.T, s *store.Postgres, ctx context.Context, sh store.Shipment) store.PackResult {
	t.Helper()
	var picks []store.PickInput
	var packs []store.PackInput
	for _, l := range sh.Lines {
		picks = append(picks, store.PickInput{ShipmentLineID: l.ID, Picked: l.Planned})
		packs = append(packs, store.PackInput{ShipmentLineID: l.ID, Qty: l.Planned})
	}
	picked, err := s.ConfirmPick(ctx, sh.ID, sh.Version, picks, idem("pick"))
	if err != nil {
		t.Fatalf("ConfirmPick: %v", err)
	}
	if picked.Status != domain.StatusPicked {
		t.Fatalf("status after full pick = %s, want PICKED", picked.Status)
	}
	packed, err := s.PackContainer(ctx, sh.ID, picked.Version, "CARTON-C12", packs, idem("pack"))
	if err != nil {
		t.Fatalf("PackContainer: %v", err)
	}
	return packed
}

func costs(sh store.Shipment, unit string) map[string]store.Cost {
	out := map[string]store.Cost{}
	for _, l := range sh.Lines {
		out[l.ID] = store.Cost{UnitCost: decimal.RequireFromString(unit), CostSnapshotID: "snap-" + l.ID}
	}
	return out
}

func countEvents(t *testing.T, admin *pgxpool.Pool, eventType string) int {
	t.Helper()
	var n int
	if err := admin.QueryRow(context.Background(), `select count(*) from outbox where type = $1`, eventType).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// AC-28.1 and AC-28.2: 10 units at 12.50 post Dr 1405 125.00 / Cr 1400 125.00;
// cancelling mirrors the journal and the quantities, returns the shipment to
// PACKED and the containers to CLOSED, and a replayed cancel returns the stored reversal.
func TestPostAndCancelPackingSlip_AC28_1_AC28_2(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1, "")
	f := released(t, s, ctx, 10)
	packed := pickAndPack(t, s, ctx, f.shipment)
	if packed.Shipment.Status != domain.StatusPacked {
		t.Fatalf("status after full pack = %s, want PACKED", packed.Shipment.Status)
	}
	if err := domain.ValidSSCC(packed.Container.SSCC); err != nil || packed.Container.SSCC[:8] != "00614141" {
		t.Fatalf("SSCC %s: %v", packed.Container.SSCC, err)
	}

	in := store.PostInput{PostingDate: today(), Currency: "USD", Costs: costs(f.shipment, "12.50")}
	postKey := idem("post")
	slip, err := s.PostPackingSlip(ctx, f.shipment.ID, packed.Shipment.Version, in, postKey)
	if err != nil {
		t.Fatalf("PostPackingSlip: %v", err)
	}
	if slip.Number != "PS-0000001" || !slip.TotalCost.Equal(decimal.RequireFromString("125")) {
		t.Fatalf("slip %s total %s, want PS-0000001 and 125.00", slip.Number, slip.TotalCost)
	}
	want := []store.JournalEntry{
		{Account: "1405", Amount: decimal.RequireFromString("125"), Currency: "USD", OrderLineID: f.shipment.Lines[0].OrderLineID},
		{Account: "1400", Amount: decimal.RequireFromString("-125"), Currency: "USD", OrderLineID: f.shipment.Lines[0].OrderLineID},
	}
	assertJournal(t, slip.Journal, want)
	replayed, err := s.PostPackingSlip(ctx, f.shipment.ID, packed.Shipment.Version, in, postKey)
	if err != nil || replayed.ID != slip.ID {
		t.Fatalf("replayed post = %s, %v; want the stored slip %s", replayed.ID, err, slip.ID)
	}
	shipped, err := s.GetShipment(ctx, f.shipment.ID)
	if err != nil {
		t.Fatal(err)
	}
	if shipped.Status != domain.StatusShipped || shipped.ActualShipDate == nil || !shipped.Lines[0].Shipped.Equal(decimal.NewFromInt(10)) ||
		shipped.Containers[0].Status != "SHIPPED" {
		t.Fatalf("after post: status %s actual %v shipped %s container %s", shipped.Status, shipped.ActualShipDate, shipped.Lines[0].Shipped, shipped.Containers[0].Status)
	}

	cancelKey := idem("cancel")
	rev, err := s.CancelPackingSlip(ctx, slip.ID, cancelKey)
	if err != nil {
		t.Fatalf("CancelPackingSlip: %v", err)
	}
	if rev.ReversalOfID == nil || *rev.ReversalOfID != slip.ID || rev.Number != "PS-0000002" {
		t.Fatalf("reversal %s of %v, want PS-0000002 of %s", rev.Number, rev.ReversalOfID, slip.ID)
	}
	assertJournal(t, rev.Journal, []store.JournalEntry{
		{Account: "1405", Amount: decimal.RequireFromString("-125"), Currency: "USD", OrderLineID: want[0].OrderLineID},
		{Account: "1400", Amount: decimal.RequireFromString("125"), Currency: "USD", OrderLineID: want[0].OrderLineID},
	})
	if !rev.Lines[0].Qty.Equal(decimal.NewFromInt(-10)) || !rev.TotalCost.Equal(decimal.RequireFromString("-125")) {
		t.Fatalf("reversal qty %s total %s, want -10 and -125", rev.Lines[0].Qty, rev.TotalCost)
	}
	again, err := s.CancelPackingSlip(ctx, slip.ID, cancelKey)
	if err != nil || again.ID != rev.ID {
		t.Fatalf("replayed cancel = %s, %v; want the stored reversal %s", again.ID, err, rev.ID)
	}
	if _, err := s.CancelPackingSlip(ctx, slip.ID, idem("cancel-2")); problemCode(err) != store.CodeSlipNotCancellable {
		t.Fatalf("second cancel: %v, want %s", err, store.CodeSlipNotCancellable)
	}
	after, err := s.GetShipment(ctx, f.shipment.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != domain.StatusPacked || after.ActualShipDate != nil || !after.Lines[0].Shipped.IsZero() || after.Containers[0].Status != "CLOSED" {
		t.Fatalf("after cancel: status %s actual %v shipped %s container %s", after.Status, after.ActualShipDate, after.Lines[0].Shipped, after.Containers[0].Status)
	}
	orig, err := s.GetPackingSlip(ctx, slip.ID)
	if err != nil || orig.Status != "CANCELLED" {
		t.Fatalf("original slip status %s, %v; want CANCELLED", orig.Status, err)
	}
	if n := countEvents(t, admin, fulevents.PackingSlipPosted); n != 1 {
		t.Fatalf("packingslip.posted events = %d, want 1 (replay must not enqueue again)", n)
	}
	if n := countEvents(t, admin, fulevents.PackingSlipCancelled); n != 1 {
		t.Fatalf("packingslip.cancelled events = %d, want 1", n)
	}

	// The shipment can be posted again after the cancellation (BR-28.6 SHIPPED -> PACKED).
	if _, err := s.PostPackingSlip(ctx, f.shipment.ID, after.Version, in, idem("repost")); err != nil {
		t.Fatalf("repost after cancel: %v", err)
	}
	slips, err := s.ListPackingSlips(ctx, store.PackingSlipFilter{ShipmentID: f.shipment.ID, Limit: 10})
	if err != nil || len(slips) != 3 {
		t.Fatalf("slips for shipment = %d, %v; want 3 (original, reversal, repost)", len(slips), err)
	}
}

func assertJournal(t *testing.T, got, want []store.JournalEntry) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("journal = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i].Account != want[i].Account || !got[i].Amount.Equal(want[i].Amount) || got[i].Currency != want[i].Currency || got[i].OrderLineID != want[i].OrderLineID {
			t.Fatalf("journal[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// PGI-style posting (cogs_at_shipment) debits 5000 instead of 1405 (BR-28.1).
func TestPostPackingSlip_CogsAtShipment(t *testing.T) {
	s, _ := setup(t)
	ctx := ctxFor(2, "")
	f := released(t, s, ctx, 3)
	setCogsPolicy(t, s, ctx, f.legalEntity, true)
	packed := pickAndPack(t, s, ctx, f.shipment)
	slip, err := s.PostPackingSlip(ctx, f.shipment.ID, packed.Shipment.Version,
		store.PostInput{PostingDate: today(), Currency: "EUR", CogsAtShipment: true, Costs: costs(f.shipment, "2.005")}, idem("post"))
	if err != nil {
		t.Fatal(err)
	}
	// 3 x 2.005 = 6.015, rounded once per line half away from zero to 6.02.
	assertJournal(t, slip.Journal, []store.JournalEntry{
		{Account: "5000", Amount: decimal.RequireFromString("6.02"), Currency: "EUR", OrderLineID: f.shipment.Lines[0].OrderLineID},
		{Account: "1400", Amount: decimal.RequireFromString("-6.02"), Currency: "EUR", OrderLineID: f.shipment.Lines[0].OrderLineID},
	})
}

// AC-28.4: a 100-unit line picked at 90 keeps qty_picked 90 and reports 10 on
// backorder in pick.confirmed; an over-pick is FUL-0004 and a stale version 412.
func TestConfirmPick_ShortPickOverPickAndVersion(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(3, "")
	f := released(t, s, ctx, 100)
	line := f.shipment.Lines[0]
	if _, err := s.ConfirmPick(ctx, f.shipment.ID, f.shipment.Version,
		[]store.PickInput{{ShipmentLineID: line.ID, Picked: decimal.NewFromInt(101)}}, idem("over")); problemCode(err) != store.CodeOverPick {
		t.Fatalf("over-pick: %v, want %s", err, store.CodeOverPick)
	}
	if _, err := s.ConfirmPick(ctx, f.shipment.ID, f.shipment.Version+1,
		[]store.PickInput{{ShipmentLineID: line.ID, Picked: decimal.NewFromInt(90)}}, idem("stale")); problemCode(err) != store.CodeVersion {
		t.Fatalf("stale version: %v, want %s", err, store.CodeVersion)
	}
	sh, err := s.ConfirmPick(ctx, f.shipment.ID, f.shipment.Version,
		[]store.PickInput{{ShipmentLineID: line.ID, Picked: decimal.NewFromInt(90)}}, idem("short"))
	if err != nil {
		t.Fatal(err)
	}
	if !sh.Lines[0].Picked.Equal(decimal.NewFromInt(90)) || sh.Status != domain.StatusPicked || sh.Version != 2 {
		t.Fatalf("after short pick: picked %s status %s version %d", sh.Lines[0].Picked, sh.Status, sh.Version)
	}
	var backordered string
	if err := admin.QueryRow(context.Background(), `select payload->'data'->'lines'->0->>'backordered' from outbox where type = $1`,
		fulevents.PickConfirmed).Scan(&backordered); err != nil {
		t.Fatal(err)
	}
	if backordered != "10" {
		t.Fatalf("backordered in pick.confirmed = %q, want 10", backordered)
	}
	if _, err := s.PackContainer(ctx, f.shipment.ID, sh.Version, "PALLET",
		[]store.PackInput{{ShipmentLineID: line.ID, Qty: decimal.NewFromInt(91)}}, idem("overpack")); problemCode(err) != store.CodeOverPack {
		t.Fatalf("over-pack: %v, want %s", err, store.CodeOverPack)
	}
}

// Packing in two containers keeps the shipment PICKED until every picked unit
// is packed; each container gets the next gap-free SSCC serial.
func TestPackContainer_PartialThenFull(t *testing.T) {
	s, _ := setup(t)
	ctx := ctxFor(4, "")
	f := released(t, s, ctx, 30)
	line := f.shipment.Lines[0]
	sh, err := s.ConfirmPick(ctx, f.shipment.ID, 1, []store.PickInput{{ShipmentLineID: line.ID, Picked: decimal.NewFromInt(30)}}, idem("pick"))
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.PackContainer(ctx, sh.ID, sh.Version, "C12", []store.PackInput{{ShipmentLineID: line.ID, Qty: decimal.NewFromInt(12)}}, idem("c1"))
	if err != nil {
		t.Fatal(err)
	}
	if first.Shipment.Status != domain.StatusPicked {
		t.Fatalf("status after partial pack = %s, want PICKED", first.Shipment.Status)
	}
	second, err := s.PackContainer(ctx, sh.ID, first.Shipment.Version, "C12", []store.PackInput{{ShipmentLineID: line.ID, Qty: decimal.NewFromInt(18)}}, idem("c2"))
	if err != nil {
		t.Fatal(err)
	}
	if second.Shipment.Status != domain.StatusPacked || len(second.Shipment.Containers) != 2 {
		t.Fatalf("after full pack: status %s containers %d", second.Shipment.Status, len(second.Shipment.Containers))
	}
	want1, _ := domain.SSCC(0, "0614141", 1)
	want2, _ := domain.SSCC(0, "0614141", 2)
	if first.Container.SSCC != want1 || second.Container.SSCC != want2 {
		t.Fatalf("SSCCs %s, %s; want %s, %s", first.Container.SSCC, second.Container.SSCC, want1, want2)
	}
}

// A line that is already on an open shipment is skipped as already_released,
// so a second release run (a new Idempotency-Key) never ships it twice.
func TestRunRelease_SkipsLinesAlreadyReleased(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(5, "")
	f := released(t, s, ctx, 5)
	line := f.shipment.Lines[0]
	again := []domain.ReleaseGroup{{OrderID: f.shipment.OrderID, WarehouseID: f.warehouse, ShipDate: today(), Lines: []domain.ReleaseLine{{
		OrderID: f.shipment.OrderID, OrderLineID: line.OrderLineID, CustomerID: f.shipment.CustomerID, LegalEntityID: f.legalEntity,
		WarehouseID: f.warehouse, ProductID: line.ProductID, Unit: "EA", Qty: decimal.NewFromInt(5), ConfirmedShipDate: today(), ATPOK: true,
	}}}}
	res, err := s.RunRelease(ctx, f.warehouse, again, idem("rel-again"))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Shipments) != 0 || len(res.Skipped) != 1 || res.Skipped[0].Reason != domain.SkipAlreadyReleased {
		t.Fatalf("second run = %+v, want no shipment and one already_released skip", res)
	}
	if n := countEvents(t, admin, fulevents.ShipmentReleased); n != 1 {
		t.Fatalf("shipment.released events = %d, want 1", n)
	}
	list, err := s.ListShipments(ctx, store.ShipmentFilter{WarehouseID: f.warehouse, Limit: 10})
	if err != nil || len(list) != 1 || list[0].LineCount != 1 || list[0].ShipmentNumber != f.shipment.ShipmentNumber {
		t.Fatalf("list = %+v, %v; want the one shipment", list, err)
	}
	if list, _ := s.ListShipments(ctx, store.ShipmentFilter{Status: "SHIPPED", Limit: 10}); len(list) != 0 {
		t.Fatalf("SHIPPED filter returned %d shipments, want 0", len(list))
	}
}

// Posting needs PACKED (FUL-0001); a missing cost is FUL-0042; an invoiced slip
// cannot be cancelled (FUL-0010).
func TestPostAndCancel_Refusals(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(6, "")
	f := released(t, s, ctx, 2)
	if _, err := s.PostPackingSlip(ctx, f.shipment.ID, 1, store.PostInput{PostingDate: today(), Currency: "USD", Costs: costs(f.shipment, "1")}, idem("early")); problemCode(err) != store.CodeStatus {
		t.Fatalf("post before packing: %v, want %s", err, store.CodeStatus)
	}
	packed := pickAndPack(t, s, ctx, f.shipment)
	if _, err := s.PostPackingSlip(ctx, f.shipment.ID, packed.Shipment.Version, store.PostInput{PostingDate: today(), Currency: "USD", Costs: map[string]store.Cost{}}, idem("nocost")); problemCode(err) != store.CodeCostMissing {
		t.Fatalf("post without cost: %v, want %s", err, store.CodeCostMissing)
	}
	slip, err := s.PostPackingSlip(ctx, f.shipment.ID, packed.Shipment.Version, store.PostInput{PostingDate: today(), Currency: "USD", Costs: costs(f.shipment, "1")}, idem("post"))
	if err != nil {
		t.Fatal(err)
	}
	// Billing (wave 29) marks a slip INVOICED; simulate it.
	if _, err := admin.Exec(context.Background(), `update packing_slip set status = 'INVOICED' where id = $1`, slip.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CancelPackingSlip(ctx, slip.ID, idem("cancel")); problemCode(err) != store.CodeSlipInvoiced {
		t.Fatalf("cancel invoiced: %v, want %s", err, store.CodeSlipInvoiced)
	}
	if _, err := s.GetPackingSlip(ctx, ids.New()); problemCode(err) != store.CodeSlipNotFound {
		t.Fatalf("unknown slip: %v, want %s", err, store.CodeSlipNotFound)
	}
}

// A shipment short-picked to zero has nothing to pack, stays PICKED and cannot
// post an empty packing slip (cancelling it is a later slice).
func TestPostPackingSlip_ZeroPickCannotPost(t *testing.T) {
	s, _ := setup(t)
	ctx := ctxFor(7, "")
	f := released(t, s, ctx, 4)
	line := f.shipment.Lines[0]
	sh, err := s.ConfirmPick(ctx, f.shipment.ID, 1, []store.PickInput{{ShipmentLineID: line.ID, Picked: decimal.Zero}}, idem("zero"))
	if err != nil {
		t.Fatal(err)
	}
	if sh.Status != domain.StatusPicked {
		t.Fatalf("status = %s, want PICKED", sh.Status)
	}
	if _, err := s.PostPackingSlip(ctx, sh.ID, sh.Version, store.PostInput{PostingDate: today(), Currency: "USD", Costs: costs(sh, "1")}, idem("post")); problemCode(err) != store.CodeStatus {
		t.Fatalf("post of a PICKED shipment: %v, want %s", err, store.CodeStatus)
	}
}

// Rows of one tenant are invisible to another, and a principal scoped to a
// different legal entity cannot see the shipment either.
func TestTenantAndLegalEntityIsolation(t *testing.T) {
	s, _ := setup(t)
	a := ctxFor(8, "")
	f := released(t, s, a, 1)
	if _, err := s.GetShipment(ctxFor(9, ""), f.shipment.ID); problemCode(err) != store.CodeShipmentNotFound {
		t.Fatalf("other tenant read the shipment: %v", err)
	}
	if list, err := s.ListShipments(ctxFor(9, ""), store.ShipmentFilter{Limit: 10}); err != nil || len(list) != 0 {
		t.Fatalf("other tenant listed %d shipments, %v", len(list), err)
	}
	if _, err := s.GetShipment(ctxFor(8, ids.New()), f.shipment.ID); problemCode(err) != store.CodeShipmentNotFound {
		t.Fatalf("principal of another legal entity read the shipment: %v", err)
	}
	if _, err := s.GetShipment(ctxFor(8, f.legalEntity), f.shipment.ID); err != nil {
		t.Fatalf("principal of the shipment's legal entity: %v", err)
	}
	if _, err := s.ConfirmPick(ctxFor(9, ""), f.shipment.ID, 1, []store.PickInput{{ShipmentLineID: f.shipment.Lines[0].ID, Picked: decimal.NewFromInt(1)}}, idem("x")); problemCode(err) != store.CodeShipmentNotFound {
		t.Fatalf("other tenant picked the shipment: %v", err)
	}
}

// The same key with a different body is refused (idempotency_key_reuse).
func TestIdempotencyKeyReuseWithAnotherBody(t *testing.T) {
	s, _ := setup(t)
	ctx := ctxFor(10, "")
	f := released(t, s, ctx, 3)
	key := store.Idem{Key: "pick-key", Hash: "body-1"}
	pick := []store.PickInput{{ShipmentLineID: f.shipment.Lines[0].ID, Picked: decimal.NewFromInt(3)}}
	if _, err := s.ConfirmPick(ctx, f.shipment.ID, 1, pick, key); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConfirmPick(ctx, f.shipment.ID, 1, pick, store.Idem{Key: "pick-key", Hash: "body-2"}); problemCode(err) != store.CodeIdempotencyReuse {
		t.Fatalf("reused key: %v, want %s", err, store.CodeIdempotencyReuse)
	}
}
