package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
)

// A plan that includes vendor-owned stock counts it (SAP MIK1) and posts the
// variance as quantity only: RECEIVED or DEDUCTED, no value, no GL entry
// (BR-16.14). Company stock on the same sheet is valued as usual.
func TestCountOfVendorOwnedStockPostsQuantityOnly(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	product, own := ids.New(), ids.New()
	receiveConsignment(t, s, ctx, product, "50", "2")
	post(t, ctx, s, domain.JournalOpeningBalance, line(own, "10", "3", dims()))

	plain := generate(t, s, ctx, controlPlan(t, s, ctx, func(p *domain.CountPlan) { p.ToleranceValue = dec("1000000"); p.TolerancePct = dec("100") }))
	for _, l := range plain.Lines {
		if domain.VendorOwned(l.Dims) {
			t.Fatalf("a plan without includeVendorOwned put vendor-owned stock on the sheet: %+v", l)
		}
	}

	plan := controlPlan(t, s, ctx, func(p *domain.CountPlan) {
		p.IncludeVendorOwned, p.TolerancePct, p.ToleranceValue = true, dec("100"), dec("1000000")
	})
	if !plan.IncludeVendorOwned {
		t.Fatal("plan did not keep includeVendorOwned")
	}
	sheet := generate(t, s, ctx, plan)
	var vendorLine, ownLine domain.CountLine
	for _, l := range sheet.Lines {
		switch {
		case domain.VendorOwned(l.Dims) && l.ProductID == product:
			vendorLine = l
		case l.ProductID == own:
			ownLine = l
		}
	}
	if vendorLine.ID == "" || ownLine.ID == "" {
		t.Fatalf("sheet lacks the vendor-owned or the company line: %+v", sheet.Lines)
	}
	var err error
	sheet, err = s.EnterCount(ctx, sheet.ID, vendorLine.ID, sheet.Version, dec("47"))
	if err != nil {
		t.Fatal(err)
	}
	sheet, err = s.EnterCount(ctx, sheet.ID, ownLine.ID, sheet.Version, dec("11"))
	if err != nil {
		t.Fatal(err)
	}
	posted, err := s.PostCountSheet(approverCtx(ctx), sheet.ID, sheet.Version, true, postParams())
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	if qty, val := ownerStock(t, s, ctx, product, "V1"); !qty.Equal(dec("47")) || !val.IsZero() {
		t.Fatalf("vendor-owned stock %s valued %s, want 47 and no value", qty, val)
	}
	if qty, val := ownerStock(t, s, ctx, own, ""); !qty.Equal(dec("11")) || !val.Equal(dec("33")) {
		t.Fatalf("company stock %s valued %s, want 11 at 33", qty, val)
	}
	j, err := s.GetJournal(ctx, posted.JournalID)
	if err != nil {
		t.Fatal(err)
	}
	if j.GLEntry == nil || !j.GLEntry.Net(inventoryAccount).Equal(dec("3")) {
		t.Fatalf("GL entry %+v, want the company variance of 3.00 on the inventory account only", j.GLEntry)
	}

	// Count it up: a RECEIVED transaction, still no value; the ledger rebuilds clean.
	again := generate(t, s, ctx, plan)
	for _, l := range again.Lines {
		if domain.VendorOwned(l.Dims) && l.ProductID == product {
			again, err = s.EnterCount(ctx, again.ID, l.ID, again.Version, dec("52"))
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := s.PostCountSheet(approverCtx(ctx), again.ID, again.Version, true, postParams()); err != nil {
		t.Fatalf("post gain: %v", err)
	}
	if qty, val := ownerStock(t, s, ctx, product, "V1"); !qty.Equal(dec("52")) || !val.IsZero() {
		t.Fatalf("vendor-owned stock %s valued %s after a gain, want 52 and no value", qty, val)
	}
	if rep, err := s.RebuildOnHand(tenantWideCtx(1), false); err != nil || rep.DiffCount != 0 {
		t.Fatalf("projection must rebuild from the ledger: %v %+v", err, rep)
	}
}

// vendorLine returns the sheet line of the product's vendor-owned row.
func vendorLine(t *testing.T, sheet domain.CountSheet, product string) domain.CountLine {
	t.Helper()
	for _, l := range sheet.Lines {
		if domain.VendorOwned(l.Dims) && l.ProductID == product {
			return l
		}
	}
	t.Fatalf("sheet has no vendor-owned line of %s: %+v", product, sheet.Lines)
	return domain.CountLine{}
}

// countVendor counts the product's vendor-owned row at qty and posts the sheet.
func countVendor(t *testing.T, s *store.Postgres, ctx context.Context, plan domain.CountPlan, product, qty string) domain.CountSheet {
	t.Helper()
	sheet := generate(t, s, ctx, plan)
	sheet, err := s.EnterCount(ctx, sheet.ID, vendorLine(t, sheet, product).ID, sheet.Version, dec(qty))
	if err != nil {
		t.Fatal(err)
	}
	posted, err := s.PostCountSheet(approverCtx(ctx), sheet.ID, sheet.Version, true, postParams())
	if err != nil {
		t.Fatalf("post count: %v", err)
	}
	return posted
}

// A vendor-owned count gain announces the RECEIVED bucket it moved (a loss
// DEDUCTED), not a zero DEDUCTED delta.
func TestVendorOwnedCountAnnouncesTheBucketItMoved(t *testing.T) {
	s, _, admin := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	receiveConsignment(t, s, ctx, product, "10", "2")
	plan := controlPlan(t, s, ctx, func(p *domain.CountPlan) { p.IncludeVendorOwned, p.TolerancePct = true, dec("100") })
	for _, c := range []struct{ qty, bucket, delta string }{{"15", "RECEIVED", "5"}, {"12", "DEDUCTED", "3"}} {
		posted := countVendor(t, s, ctx, plan, product, c.qty)
		var bucket, delta string
		if err := admin.QueryRow(context.Background(), `select o.payload->'data'->>'deltaBucket', o.payload->'data'->>'deltaQty'
			from outbox o where o.tenant_id = $1 and o.type = 'dev.passage.inventory.onhand.changed.v1'
			  and o.payload->'data'->>'productId' = $2 order by o.created_at desc, o.id desc limit 1`,
			testkit.TenantID(1), product).Scan(&bucket, &delta); err != nil {
			t.Fatal(err)
		}
		if bucket != c.bucket || !dec(delta).Equal(dec(c.delta)) {
			t.Fatalf("count to %s (journal %s): onhand.changed %s %s, want %s %s", c.qty, posted.JournalID, bucket, delta, c.bucket, c.delta)
		}
	}
}

// INV-OWN-002 still holds for a COUNT journal: the reversal of a vendor-owned
// count gain cannot leave the vendor with negative stock once an ownership
// change has taken the quantity it found.
func TestReversingAVendorOwnedCountGainCannotGoBelowZero(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	receiveConsignment(t, s, ctx, product, "10", "2")
	plan := controlPlan(t, s, ctx, func(p *domain.CountPlan) { p.IncludeVendorOwned, p.TolerancePct = true, dec("100") })
	gain := countVendor(t, s, ctx, plan, product, "15")

	take := line(product, "15", "2", vendorDims())
	take.LineNo = 1
	j, err := newJournal(ctx, s, domain.JournalOwnershipChange, take)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PostJournal(ctx, j.ID, j.Version, consignmentParams()); err != nil {
		t.Fatalf("ownership change: %v", err)
	}
	rev, _, err := s.ReverseJournal(ctx, gain.JournalID, time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PostJournal(ctx, rev.ID, rev.Version, postParams()); problemCode(err) != domain.CodeOwnershipExceeds {
		t.Fatalf("reversal below zero vendor stock: %v, want %s", err, domain.CodeOwnershipExceeds)
	}
	if qty, _ := ownerStock(t, s, ctx, product, "V1"); !qty.IsZero() {
		t.Fatalf("vendor-owned stock %s after the refused reversal, want 0", qty)
	}
}

// A consignment receipt is a movement of vendor-owned stock: refused on a row
// frozen by an open sheet (INV-CNT-002), and on a plan without a freeze it
// flags the counted line for recount (BR-16.8) instead of letting the count
// post a false loss.
func TestConsignmentReceiptHonoursTheCountOfVendorOwnedStock(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	receiveConsignment(t, s, ctx, product, "10", "2")

	frozen := generate(t, s, ctx, controlPlan(t, s, ctx, func(p *domain.CountPlan) { p.IncludeVendorOwned, p.FreezeStock = true, true }))
	vendorLine(t, frozen, product)
	o, _, err := s.CreateConsignment(ctx, ids.New(), replenishment(ctx, product, "4", "2"))
	if err != nil {
		t.Fatal(err)
	}
	receipt := []domain.ConsignmentReceipt{{LineID: o.Lines[0].ID, Qty: dec("4")}}
	day := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	if _, err := s.ReceiveConsignment(ctx, o.ID, o.Version, receipt, day); problemCode(err) != domain.CodeCountFrozen {
		t.Fatalf("receipt on a frozen vendor-owned row: %v, want %s", err, domain.CodeCountFrozen)
	}
	if _, err := s.CancelCountSheet(ctx, frozen.ID, frozen.Version); err != nil {
		t.Fatal(err)
	}

	open := generate(t, s, ctx, controlPlan(t, s, ctx, func(p *domain.CountPlan) { p.IncludeVendorOwned = true }))
	open, err = s.EnterCount(ctx, open.ID, vendorLine(t, open, product).ID, open.Version, dec("10"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReceiveConsignment(ctx, o.ID, o.Version, receipt, day); err != nil {
		t.Fatalf("receipt: %v", err)
	}
	reread, err := s.GetCountSheet(ctx, open.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !vendorLine(t, reread, product).RecountRequested {
		t.Fatalf("a receipt after counting did not flag the vendor-owned line for recount: %+v", reread)
	}
}
