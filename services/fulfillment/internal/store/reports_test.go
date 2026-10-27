package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/domain"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/store"
)

func lineIDs(rows []store.BackorderRow) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.ShipmentLineID
	}
	return out
}

func backorders(t *testing.T, s *store.Postgres, ctx context.Context, f store.BackorderFilter) []store.BackorderRow {
	t.Helper()
	if f.Limit == 0 {
		f.Limit = 100
	}
	rows, err := s.ListBackorders(ctx, f)
	if err != nil {
		t.Fatalf("ListBackorders: %v", err)
	}
	return rows
}

func shortPick(t *testing.T, s *store.Postgres, ctx context.Context, sh store.Shipment, picked ...int64) store.Shipment {
	t.Helper()
	var picks []store.PickInput
	for i, l := range sh.Lines {
		picks = append(picks, store.PickInput{ShipmentLineID: l.ID, Picked: decimal.NewFromInt(picked[i])})
	}
	out, err := s.ConfirmPick(ctx, sh.ID, sh.Version, picks, idem("pick"))
	if err != nil {
		t.Fatalf("ConfirmPick: %v", err)
	}
	return out
}

// FR-28.2 and AC-28.4 on the short picks and backorders report: a short pick
// opens a backorder of the remainder, shipping what was picked keeps it open,
// a re-pick to plan closes it, and cancelling the shipment closes it too.
// Filters, the cursor, tenant and legal-entity isolation apply.
func TestBackorders_ShortPickReport(t *testing.T) {
	s, _ := setup(t)
	ctx := ctxFor(11, "")

	a := released(t, s, ctx, 100)
	aPicked := shortPick(t, s, ctx, a.shipment, 90)
	b := released(t, s, ctx, 5, 5)
	shortPick(t, s, ctx, b.shipment, 5, 3)
	c := released(t, s, ctx, 4)
	cPicked := shortPick(t, s, ctx, c.shipment, 1)
	full := released(t, s, ctx, 2)
	pickAll(t, s, ctx, full.shipment)

	open := backorders(t, s, ctx, store.BackorderFilter{})
	if len(open) != 3 {
		t.Fatalf("open backorders = %d, want 3 (a, b's second line, c)", len(open))
	}
	// Newest short pick first.
	if got := lineIDs(open); got[0] != c.shipment.Lines[0].ID || got[1] != b.shipment.Lines[1].ID || got[2] != a.shipment.Lines[0].ID {
		t.Fatalf("order = %v", got)
	}
	ra := open[2]
	if !ra.Planned.Equal(decimal.NewFromInt(100)) || !ra.Picked.Equal(decimal.NewFromInt(90)) || ra.Status != domain.StatusPicked ||
		ra.WarehouseID != a.warehouse || ra.OrderLineID != a.shipment.Lines[0].OrderLineID || time.Since(ra.ShortPickedAt) > time.Minute {
		t.Fatalf("row a = %+v", ra)
	}
	if rem, isOpen := domain.Backorder(ra.Status, ra.Planned, ra.Picked); !rem.Equal(decimal.NewFromInt(10)) || !isOpen {
		t.Fatalf("row a remainder = %s open %v, want 10 open", rem, isOpen)
	}

	// Filters: warehouse, and the short-pick date.
	if got := backorders(t, s, ctx, store.BackorderFilter{WarehouseID: b.warehouse}); len(got) != 1 || got[0].ShipmentLineID != b.shipment.Lines[1].ID {
		t.Fatalf("warehouse b = %v", lineIDs(got))
	}
	if got := backorders(t, s, ctx, store.BackorderFilter{From: today(), To: today()}); len(got) != 3 {
		t.Fatalf("today = %d rows, want 3", len(got))
	}
	if got := backorders(t, s, ctx, store.BackorderFilter{From: today().AddDate(0, 0, 1)}); len(got) != 0 {
		t.Fatalf("from tomorrow = %d rows, want 0", len(got))
	}
	if got := backorders(t, s, ctx, store.BackorderFilter{To: today().AddDate(0, 0, -1)}); len(got) != 0 {
		t.Fatalf("to yesterday = %d rows, want 0", len(got))
	}

	// Keyset cursor: two pages of one row cover the first two rows in order.
	page1 := backorders(t, s, ctx, store.BackorderFilter{Limit: 1})
	page2 := backorders(t, s, ctx, store.BackorderFilter{Limit: 1, AfterLineID: page1[0].ShipmentLineID})
	if len(page1) != 1 || len(page2) != 1 || page1[0].ShipmentLineID != open[0].ShipmentLineID || page2[0].ShipmentLineID != open[1].ShipmentLineID {
		t.Fatalf("pages = %v, %v; want %v", lineIDs(page1), lineIDs(page2), lineIDs(open[:2]))
	}

	// Ship the 90 that were picked: the shipment is PARTIALLY_SHIPPED and the 10 stay open.
	packed, err := s.PackContainer(ctx, a.shipment.ID, aPicked.Version, "CARTON-C12",
		[]store.PackInput{{ShipmentLineID: a.shipment.Lines[0].ID, Qty: decimal.NewFromInt(90)}}, idem("pack"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PostPackingSlip(ctx, a.shipment.ID, packed.Shipment.Version, store.PostInput{PostingDate: today(), Currency: "USD", Costs: costs(a.shipment, "1")}, idem("post")); err != nil {
		t.Fatal(err)
	}
	got := backorders(t, s, ctx, store.BackorderFilter{WarehouseID: a.warehouse})
	if len(got) != 1 || got[0].Status != domain.StatusPartiallyShipped || !got[0].Shipped.Equal(decimal.NewFromInt(90)) {
		t.Fatalf("after shipping 90: %+v", got)
	}
	firstShort := got[0].ShortPickedAt

	// Re-pick the remainder: the backorder closes but keeps its first short-pick time.
	sh := getShipment(t, s, ctx, a.shipment.ID)
	shortPick(t, s, ctx, sh, 100)
	if got := backorders(t, s, ctx, store.BackorderFilter{WarehouseID: a.warehouse}); len(got) != 0 {
		t.Fatalf("after re-pick, open = %d, want 0", len(got))
	}
	all := backorders(t, s, ctx, store.BackorderFilter{WarehouseID: a.warehouse, IncludeClosed: true})
	if len(all) != 1 || !all[0].Picked.Equal(decimal.NewFromInt(100)) || !all[0].ShortPickedAt.Equal(firstShort) {
		t.Fatalf("closed a = %+v, want picked 100 and short-picked at %s", all, firstShort)
	}

	// Cancelling a short-picked shipment closes its backorder.
	if _, err := s.CancelShipment(ctx, c.shipment.ID, cPicked.Version, idem("cancel")); err != nil {
		t.Fatalf("CancelShipment: %v", err)
	}
	if got := backorders(t, s, ctx, store.BackorderFilter{WarehouseID: c.warehouse}); len(got) != 0 {
		t.Fatalf("cancelled shipment still open: %v", lineIDs(got))
	}
	if got := backorders(t, s, ctx, store.BackorderFilter{WarehouseID: c.warehouse, IncludeClosed: true}); len(got) != 1 || got[0].Status != domain.StatusCancelled {
		t.Fatalf("cancelled shipment with status=all: %+v", got)
	}
	if got := backorders(t, s, ctx, store.BackorderFilter{WarehouseID: full.warehouse, IncludeClosed: true}); len(got) != 0 {
		t.Fatalf("a full pick is not a backorder: %v", lineIDs(got))
	}

	// Isolation: another tenant sees nothing; a principal scoped to b's legal entity sees only b.
	if got := backorders(t, s, ctxFor(12, ""), store.BackorderFilter{IncludeClosed: true}); len(got) != 0 {
		t.Fatalf("tenant 12 sees %d backorders", len(got))
	}
	if got := backorders(t, s, ctxFor(11, b.legalEntity), store.BackorderFilter{IncludeClosed: true}); len(got) != 1 || got[0].ShipmentID != b.shipment.ID {
		t.Fatalf("legal entity b sees %v", lineIDs(got))
	}
}

// shipPallets ships n returnable pallets to customer on a slip posted daysAgo.
func shipPallets(t *testing.T, s *store.Postgres, ctx context.Context, customer string, n int64, daysAgo int) store.PackingSlip {
	t.Helper()
	line := domain.ReleaseLine{OrderID: ids.New(), OrderLineID: ids.New(), CustomerID: customer, LegalEntityID: ids.New(),
		WarehouseID: ids.New(), ProductID: ids.New(), Unit: "EA", Qty: decimal.NewFromInt(n), ConfirmedShipDate: today(), ATPOK: true}
	groups, _ := domain.Release([]domain.ReleaseLine{line}, domain.ReleaseRule{Today: today(), HorizonDays: 2, RequireFullATP: true})
	res, err := s.RunRelease(ctx, line.WarehouseID, groups, idem("rel"))
	if err != nil || len(res.Shipments) != 1 {
		t.Fatalf("RunRelease: %+v, %v", res, err)
	}
	saveRule(t, s, ctx, store.PackingRule{ProductID: line.ProductID, ContainerType: "PALLET", QtyPerContainer: d("1"), MaxWeightKg: d("1000")})
	picked := pickAll(t, s, ctx, res.Shipments[0])
	packed, err := s.AutoPack(ctx, picked.ID, picked.Version, nil, idem("ap"))
	if err != nil || int64(len(packed.Containers)) != n {
		t.Fatalf("AutoPack = %d containers, %v", len(packed.Containers), err)
	}
	slip, err := s.PostPackingSlip(ctx, picked.ID, packed.Shipment.Version, store.PostInput{PostingDate: today().AddDate(0, 0, -daysAgo),
		Currency: "EUR", Costs: costs(res.Shipments[0], "1")}, idem("post"))
	if err != nil {
		t.Fatalf("PostPackingSlip: %v", err)
	}
	return slip
}

// FR-28.16 aging: 10 pallets shipped 100 days ago and 4 shipped 10 days ago,
// 8 returned, leave 6 held: first in, first out, 2 of them are from the old
// slip, so the balance is 100 days outstanding and chargeable after 90. A
// cancelled slip's pallets do not count, and another tenant sees nothing.
func TestReturnableAging_FirstInFirstOutFromSlips(t *testing.T) {
	s, _ := setup(t)
	ctx := ctxFor(13, "")
	saveType(t, s, ctx, store.ContainerType{Code: "PALLET", TareKg: d("25"), Returnable: true, PackagingMaterial: strp("WOOD")})
	c1, c2 := ids.New(), ids.New()
	shipPallets(t, s, ctx, c1, 10, 100)
	shipPallets(t, s, ctx, c1, 4, 10)
	cancelled := shipPallets(t, s, ctx, c1, 3, 50)
	if _, err := s.CancelPackingSlip(ctx, cancelled.ID, idem("cancel")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReturnPackaging(ctx, store.ReturnInput{CustomerID: c1, ContainerType: "PALLET", Qty: d("8")}, idem("ret")); err != nil {
		t.Fatal(err)
	}
	shipPallets(t, s, ctx, c2, 2, 5)
	if _, err := s.ReturnPackaging(ctx, store.ReturnInput{CustomerID: c2, ContainerType: "PALLET", Qty: d("2")}, idem("ret2")); err != nil {
		t.Fatal(err)
	}

	rows, err := s.ReturnableAging(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].CustomerID != c1 || !rows[0].Balance.Equal(d("6")) {
		t.Fatalf("aging rows = %+v, want only c1 holding 6 (c2 returned everything)", rows)
	}
	buckets := map[string]string{}
	for _, b := range rows[0].Shipped {
		buckets[b.Date.Format("2006-01-02")] = b.Qty.String()
	}
	old, recent := today().AddDate(0, 0, -100).Format("2006-01-02"), today().AddDate(0, 0, -10).Format("2006-01-02")
	if len(buckets) != 2 || buckets[old] != "10" || buckets[recent] != "4" {
		t.Fatalf("shipped buckets = %v, want 10 on %s and 4 on %s (the cancelled slip left out)", buckets, old, recent)
	}
	age := domain.AgeReturnables(rows[0].Balance, rows[0].Shipped, today(), domain.DefaultRTPChargeDays)
	if age.OldestOutstanding == nil || age.OldestOutstanding.Format("2006-01-02") != old || age.DaysOutstanding != 100 || !age.ChargeDue {
		t.Fatalf("age = %+v, want 100 days from %s and due", age, old)
	}

	if got, err := s.ReturnableAging(ctx, c2); err != nil || len(got) != 0 {
		t.Fatalf("c2 = %+v, %v; want no outstanding balance", got, err)
	}
	if got, err := s.ReturnableAging(ctxFor(14, ""), ""); err != nil || len(got) != 0 {
		t.Fatalf("tenant 14 sees %d balances, %v", len(got), err)
	}
}
