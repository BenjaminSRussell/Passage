package store_test

import (
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

func twoLinePO(t *testing.T, vendor string) store.PurchaseOrder {
	in := lifecyclePO(t, vendor, "10", "2026-04-01")
	second := in.Lines[0]
	second.LineNo, second.Description = 2, "nut"
	in.Lines = append(in.Lines, second)
	in.Lines[1].GrossAmount, in.Lines[1].LineAmount = dec("100"), dec("100")
	in.Subtotal, in.Total = dec("200"), dec("200")
	return in
}

func TestPurchaseOrder_LineChargesArePersistedAndKeepTheirLine(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := asUser(1, ids.New())
	code, err := s.CreateChargeCode(ctx, freightCode(), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	vendor := ids.New()
	in := twoLinePO(t, vendor)
	in.Charges = []store.PurchaseOrderCharge{
		{Seq: 1, Calc: domain.CalcFixed, Value: dec("40"), Amount: dec("40"), ChargeCodeID: code.ID, ChargeCode: code.Code,
			DebitType: code.DebitType, CreditType: code.CreditType, CreditAccountID: code.CreditAccountID, AllocationRule: code.AllocationRule, Auto: true, LineNo: 2},
		{Seq: 2, Calc: domain.CalcPercent, Value: dec("5"), Amount: dec("5"), LineNo: 1},
		{Seq: 3, Calc: domain.CalcFixed, Value: dec("1"), Amount: dec("1")},
	}
	in.ChargesTotal, in.Total = dec("46"), dec("246")
	in.AutoChargeCodes = []string{code.ID + "#2"}
	po := confirmedPO(t, ctx, s, in)
	got, err := s.GetPurchaseOrder(ctx, po.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Charges) != 3 || got.Charges[0].LineNo != 2 || got.Charges[1].LineNo != 1 || got.Charges[2].LineNo != 0 {
		t.Fatalf("charges %+v; want line charges on 2 and 1 and a header charge", got.Charges)
	}
	if len(got.AutoChargeCodes) != 1 || got.AutoChargeCodes[0] != code.ID+"#2" {
		t.Errorf("autoChargeCodes %v; want the line key", got.AutoChargeCodes)
	}

	// An edit that cancels line 2 and drops its charge: the remaining charges keep their lines, and the
	// version snapshot names the line of each charge.
	next := got
	next.Lines = append([]store.PurchaseOrderLine{}, got.Lines[:1]...)
	next.Charges = []store.PurchaseOrderCharge{got.Charges[1], got.Charges[2]}
	next.Charges[0].Seq, next.Charges[1].Seq = 1, 2
	next.Subtotal, next.ChargesTotal, next.Total = dec("100"), dec("6"), dec("106")
	next.AutoChargeCodes = []string{code.ID + "#1"}
	edit, err := s.EditPurchaseOrder(ctx, po.ID, got.RowVersion, edited(got, next))
	if err != nil {
		t.Fatal(err)
	}
	if len(edit.Charges) != 2 || edit.Charges[0].LineNo != 1 || edit.Charges[1].LineNo != 0 {
		t.Errorf("after the edit: %+v", edit.Charges)
	}
	if len(edit.AutoChargeCodes) != 2 || edit.AutoChargeCodes[0] != code.ID+"#2" || edit.AutoChargeCodes[1] != code.ID+"#1" {
		t.Errorf("autoChargeCodes %v; the set only grows", edit.AutoChargeCodes)
	}
	vs, err := s.ListPurchaseOrderVersions(ctx, po.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, v := range vs {
		for _, c := range v.Snapshot.Charges {
			found = found || c.LineNo == 1
		}
	}
	if !found || edit.VersionNo != 2 {
		t.Errorf("version %d, versions %+v; want version 2 with a snapshot charge on lineNo 1", edit.VersionNo, vs)
	}

	// A line charge must name a line of the order.
	bad := edit
	bad.Charges = append([]store.PurchaseOrderCharge{}, edit.Charges...)
	bad.Charges[0].LineNo = 9
	if _, err := s.EditPurchaseOrder(ctx, po.ID, edit.RowVersion, edited(edit, bad)); err == nil {
		t.Error("a charge on a line the order does not have must be refused by the foreign key")
	}
}

func TestAutoCharge_ItemKeysAreStoredAndListedWithTheActiveRows(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := asUser(1, ids.New())
	code, err := s.CreateChargeCode(ctx, freightCode(), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	vendor, product, group := ids.New(), ids.New(), ids.New()
	mk := func(item, id string) domain.AutoCharge {
		return domain.AutoCharge{LegalEntityID: legalEntity, ChargeCodeID: code.ID, AccountCode: domain.AutoChargeVendor, AccountID: vendor,
			ItemCode: item, ItemID: id, Currency: "USD", ValidFrom: dayOf(t, "2026-03-01"), Status: domain.ChargeActive}
	}
	byProduct, err := s.CreateAutoCharge(ctx, mk(domain.AutoChargeItemProduct, product), store.Idem{})
	if err != nil || byProduct.ItemCode != domain.AutoChargeItemProduct || byProduct.ItemID != product {
		t.Fatalf("create = %+v, %v", byProduct, err)
	}
	byGroup, err := s.CreateAutoCharge(ctx, mk(domain.AutoChargeItemGroup, group), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	header, err := s.CreateAutoCharge(ctx, mk("", ""), store.Idem{})
	if err != nil || header.ItemCode != domain.AutoChargeItemAll || header.ItemID != "" {
		t.Fatalf("an auto charge without an item key is a header charge: %+v, %v", header, err)
	}
	if _, err := s.CreateAutoCharge(ctx, mk(domain.AutoChargeItemProduct, ""), store.Idem{}); err == nil {
		t.Error("PRODUCT without an item id must be refused by the check constraint")
	}
	active, err := s.ActiveAutoCharges(ctx, legalEntity, vendor, nil, dayOf(t, "2026-06-01"))
	if err != nil || len(active) != 3 || active[0].ID != byProduct.ID || active[1].ID != byGroup.ID || active[2].ID != header.ID {
		t.Errorf("active = %+v, %v; want the three rows with their item keys", active, err)
	}
	// An update keeps the item key.
	upd := byProduct
	value := dec("12")
	upd.Value, upd.ItemCode, upd.ItemID = &value, domain.AutoChargeItemAll, ""
	got, err := s.UpdateAutoCharge(ctx, byProduct.ID, 1, upd)
	if err != nil || got.ItemCode != domain.AutoChargeItemProduct || got.ItemID != product || got.Value == nil {
		t.Errorf("update = %+v, %v; the item key does not change", got, err)
	}
}

func TestLiveCharges_DropAChargeOnACancelledLineAndRenumber(t *testing.T) {
	d := decimal.RequireFromString
	live := []store.PurchaseOrderLine{{LineNo: 1}, {LineNo: 3}}
	charges := []store.PurchaseOrderCharge{
		{Seq: 1, Calc: domain.CalcFixed, Value: d("2")},
		{Seq: 2, Calc: domain.CalcPercent, Value: d("10"), LineNo: 2},
		{Seq: 3, Calc: domain.CalcPerUnit, Value: d("1"), LineNo: 3},
	}
	inputs, kept := store.LiveCharges(charges, live)
	if len(inputs) != 2 || len(kept) != 2 {
		t.Fatalf("inputs %+v kept %+v; want the charge on cancelled line 2 dropped", inputs, kept)
	}
	if inputs[0].Line != 0 || inputs[1].Line != 2 || kept[1].LineNo != 3 || kept[0].Seq != 1 || kept[1].Seq != 2 {
		t.Errorf("inputs %+v kept %+v; want line 3 priced as the second live line and seq 1, 2", inputs, kept)
	}
}

// A planning message that changes or cancels a line prices its line charges again on the new line and drops
// the charges of a cancelled line, so charges_total is the sum of the charges left and the total identity
// holds after each step (FR-17.9, BR-17.5).
func TestInbound_LineChargesFollowAPlanningChange(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(86)
	p1, p2 := ids.New(), ids.New()
	in := twoLinePO(t, ids.New())
	in.Lines[0].ProductID, in.Lines[1].ProductID = p1, p2
	in.Charges = []store.PurchaseOrderCharge{
		{Seq: 1, Calc: domain.CalcPerUnit, Value: dec("1.5"), Amount: dec("15"), LineNo: 2},
		{Seq: 2, Calc: domain.CalcPercent, Value: dec("3.333"), Amount: dec("3.33"), LineNo: 1},
		{Seq: 3, Calc: domain.CalcPercent, Value: dec("1"), Amount: dec("2")},
	}
	in.ChargesTotal, in.Total = dec("20.33"), dec("220.33")
	po := confirmedPO(t, ctx, s, in)

	check := func(step string, wantCharges []string, wantLines []int, wantTotal string) {
		t.Helper()
		cur, err := s.GetPurchaseOrder(ctx, po.ID)
		if err != nil {
			t.Fatal(err)
		}
		sum := decimal.Zero
		for i, c := range cur.Charges {
			sum = sum.Add(c.Amount)
			if i >= len(wantCharges) || !c.Amount.Equal(dec(wantCharges[i])) || c.LineNo != wantLines[i] || c.Seq != i+1 {
				t.Errorf("%s: charge %d = %+v; want %v on lines %v", step, i, c, wantCharges, wantLines)
			}
		}
		if len(cur.Charges) != len(wantCharges) || !sum.Equal(cur.ChargesTotal) || !cur.Total.Equal(dec(wantTotal)) ||
			!cur.Total.Equal(cur.Subtotal.Sub(cur.DiscountTotal).Add(cur.ChargesTotal).Add(cur.TaxTotal)) {
			t.Errorf("%s: %d charges summing to %s, chargesTotal %s, total %s; want total %s", step, len(cur.Charges), sum, cur.ChargesTotal, cur.Total, wantTotal)
		}
	}

	// Line 1 goes from 10 to 7: its 3.333 % is now of 70.00 (2.33), the header 1 % of 170.00 (1.70).
	down := message(po, domain.ActionDecrease, "3", "")
	down.ItemID = p1
	if got, _ := applyMessage(t, pool, s, 86, down); got.Status != domain.InboundApplied {
		t.Fatalf("decrease = %+v", got)
	}
	check("after the decrease", []string{"15", "2.33", "1.7"}, []int{2, 1, 0}, "189.03")

	// Line 2 is cancelled: its per-unit charge goes with it and the header 1 % is of 70.00.
	cancel := message(po, domain.ActionCancel, "10", "")
	cancel.ItemID = p2
	if got, _ := applyMessage(t, pool, s, 86, cancel); got.Status != domain.InboundApplied {
		t.Fatalf("cancel = %+v", got)
	}
	check("after the cancel", []string{"2.33", "0.7"}, []int{1, 0}, "73.03")
}
