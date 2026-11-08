package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

// productGroupsOf answers ProductGroups from a map, so the groups differ by product.
type productGroupsOf map[string][]string

func (productGroupsOf) VendorGroups(context.Context, string) ([]string, error) { return nil, nil }
func (g productGroupsOf) ProductGroups(_ context.Context, id string) ([]string, error) {
	return g[id], nil
}

// threeLines is a PO of 100 x 10 (product p1), 50 x 4 (p2, 10% off) and 10 x 20 (p1): amounts 1000, 180, 200.
func threeLines(vendor string, p1, p2 string, extra string) string {
	return `{"legalEntityId":"` + leA + `","vendorId":"` + vendor + `","currency":"USD"` + extra + `,"lines":[
	 {"productId":"` + p1 + `","qty":"100","unit":"EA","unitPrice":"10","deliveryDate":"2026-04-01"},
	 {"productId":"` + p2 + `","qty":"50","unit":"EA","unitPrice":"4","discountPct":"10","deliveryDate":"2026-04-01"},
	 {"productId":"` + p1 + `","qty":"10","unit":"EA","unitPrice":"20","deliveryDate":"2026-04-01"}]}`
}

func TestCreateOrder_LineChargesPriceOnTheirOwnLine(t *testing.T) {
	h := newHarness(t)
	vendor, p1, p2 := ids.New(), ids.New(), ids.New()
	code := h.seedCode(freight())
	body := strings.Replace(threeLines(vendor, p1, p2, ""), `}]}`, `}],"charges":[
	 {"calc":"PERCENT","value":"10","lineNo":2},
	 {"calc":"PER_UNIT","value":"0.5","lineNo":1},
	 {"chargeCodeId":"`+code.ID+`","value":"7","lineNo":3},
	 {"calc":"PERCENT","value":"1"}]}`, 1)
	po, status, c := createOrder(h, body)
	if status != http.StatusCreated {
		t.Fatalf("status %d %s", status, c)
	}
	want := []struct {
		line   int
		amount string
	}{{2, "18"}, {1, "50"}, {3, "7"}, {0, "13.8"}}
	if len(po.Charges) != len(want) {
		t.Fatalf("charges %+v", po.Charges)
	}
	for i, w := range want {
		if po.Charges[i].LineNo != w.line || !po.Charges[i].Amount.Equal(decimal.RequireFromString(w.amount)) {
			t.Errorf("charge %d = line %d amount %s; want line %d amount %s", i, po.Charges[i].LineNo, po.Charges[i].Amount, w.line, w.amount)
		}
	}
	// 1380 lines (1000 + 180 + 200) + 88.8 charges.
	if !po.ChargesTotal.Equal(decimal.RequireFromString("88.8")) || !po.Total.Equal(decimal.RequireFromString("1468.8")) {
		t.Errorf("chargesTotal %s total %s; want 88.8 and 1468.8", po.ChargesTotal, po.Total)
	}
	if po.Charges[2].ChargeCode != "FREIGHT" || po.Charges[2].AllocationRule != domain.AllocateQty {
		t.Errorf("a coded line charge keeps the code's sides: %+v", po.Charges[2])
	}
}

func TestCreateOrder_LineChargeNamesALineOfTheOrder(t *testing.T) {
	h := newHarness(t)
	for _, n := range []string{"4", "-1"} {
		body := strings.Replace(threeLines(ids.New(), ids.New(), ids.New(), ""), `}]}`, `}],"charges":[{"calc":"FIXED","value":"1","lineNo":`+n+`}]}`, 1)
		if _, status, _ := createOrder(h, body); status != http.StatusUnprocessableEntity {
			t.Errorf("lineNo %s: status %d, want 422", n, status)
		}
	}
}

func TestCreateOrder_AutoChargesOnAProductAndAProductGroupAddLineCharges(t *testing.T) {
	h := newHarness(t)
	vendor, p1, p2, grp := ids.New(), ids.New(), ids.New(), ids.New()
	h.api.WithMemberships(productGroupsOf{p2: {grp}})
	fr := h.seedCode(freight())
	pct := h.seedCode(domain.ChargeCode{Code: "HANDLING", Name: "Handling", Calc: domain.CalcPercent, DefaultValue: decimal.NewFromInt(5),
		DebitType: domain.ChargeSideItem, CreditType: domain.ChargeSideVendor, AllocationRule: domain.AllocateAmount})
	day := mustDay(t, "2026-01-01")
	h.fake.charges.autos = []domain.AutoCharge{
		// 40 USD freight on each line of p1: lines 1 and 3.
		{ID: ids.New(), ChargeCodeID: fr.ID, AccountCode: domain.AutoChargeAll, ItemCode: domain.AutoChargeItemProduct, ItemID: p1, Currency: "USD", ValidFrom: day, Status: domain.ChargeActive},
		// 5% handling on each line of the group: line 2 only.
		{ID: ids.New(), ChargeCodeID: pct.ID, AccountCode: domain.AutoChargeVendor, AccountID: vendor, ItemCode: domain.AutoChargeItemGroup, ItemID: grp, ValidFrom: day, Status: domain.ChargeActive},
		// A product nobody orders adds nothing.
		{ID: ids.New(), ChargeCodeID: fr.ID, AccountCode: domain.AutoChargeAll, ItemCode: domain.AutoChargeItemProduct, ItemID: ids.New(), Currency: "USD", ValidFrom: day, Status: domain.ChargeActive},
	}
	po, status, c := createOrder(h, threeLines(vendor, p1, p2, `,"applyAutoCharges":true`))
	if status != http.StatusCreated {
		t.Fatalf("status %d %s", status, c)
	}
	type got struct {
		code   string
		line   int
		amount string
	}
	var have []got
	for _, ch := range po.Charges {
		if !ch.Auto {
			t.Errorf("charge %+v should be marked auto", ch)
		}
		have = append(have, got{ch.ChargeCode, ch.LineNo, ch.Amount.String()})
	}
	// 5% of the 180.00 line 2 is 9.
	want := []got{{"FREIGHT", 1, "40"}, {"FREIGHT", 3, "40"}, {"HANDLING", 2, "9"}}
	if len(have) != len(want) {
		t.Fatalf("charges %v; want %v", have, want)
	}
	for i := range want {
		if have[i] != want[i] {
			t.Errorf("charge %d = %v, want %v", i, have[i], want[i])
		}
	}
	if len(po.AutoChargeCodes) != 3 || po.AutoChargeCodes[0] != fr.ID+"#1" || po.AutoChargeCodes[1] != fr.ID+"#3" || po.AutoChargeCodes[2] != pct.ID+"#2" {
		t.Errorf("autoChargeCodes %v; want one key per charge and line", po.AutoChargeCodes)
	}
	if !po.ChargesTotal.Equal(decimal.NewFromInt(89)) {
		t.Errorf("chargesTotal %s, want 89", po.ChargesTotal)
	}
	// A line charge entered by hand for the same code and line is not added a second time; the other line is.
	body := strings.Replace(threeLines(vendor, p1, p2, `,"applyAutoCharges":true`), `}]}`, `}],"charges":[{"chargeCodeId":"`+fr.ID+`","value":"3","lineNo":1}]}`, 1)
	po, _, _ = createOrder(h, body)
	var freightLines []int
	for _, ch := range po.Charges {
		if ch.ChargeCode == "FREIGHT" {
			freightLines = append(freightLines, ch.LineNo)
		}
	}
	if len(freightLines) != 2 || freightLines[0] != 1 || freightLines[1] != 3 || po.Charges[0].Auto || !po.Charges[0].Amount.Equal(decimal.NewFromInt(3)) {
		t.Errorf("charges %+v; want the entered line 1 charge kept and line 3 added", po.Charges)
	}
}

func TestEditOrder_LineChargesFollowTheirLinesAndStayRemoved(t *testing.T) {
	h := newHarness(t)
	fr := h.seedCode(freight())
	p1 := ids.New()
	h.fake.charges.autos = []domain.AutoCharge{{ID: ids.New(), ChargeCodeID: fr.ID, AccountCode: domain.AutoChargeAll, ItemCode: domain.AutoChargeItemProduct,
		ItemID: p1, Currency: "USD", ValidFrom: mustDay(t, "2026-01-01"), Status: domain.ChargeActive}}
	po := storedPO(h, domain.POStatusDraft)
	edit := func(body string) (store.PurchaseOrder, int) {
		t.Helper()
		rec := h.do(call{method: "PATCH", path: poPath(po.ID), duties: []string{dutyPurchaseOrderMaintain}, body: body,
			headers: map[string]string{"If-Match": `"5"`}})
		if rec.Code != http.StatusOK {
			return store.PurchaseOrder{}, rec.Code
		}
		return h.fake.lastEdit.next, rec.Code
	}
	// Line 3 is kept; the line without a number is new and becomes line 4 (the stored PO has lines 1 to 3).
	lines := `"lines":[{"lineNo":3,"description":"Freight box","qty":"2","unit":"EA","unitPrice":"4","deliveryDate":"2026-04-01"},
	 {"productId":"` + p1 + `","qty":"5","unit":"EA","unitPrice":"10","deliveryDate":"2026-04-01"}]`
	head := `{"legalEntityId":"` + leA + `","vendorId":"` + vendorA + `","currency":"USD",`
	next, status := edit(head + lines + `,"applyAutoCharges":true,"charges":[{"calc":"PERCENT","value":"10","lineNo":3}]}`)
	if status != http.StatusOK || len(next.Charges) != 2 {
		t.Fatalf("status %d charges %+v", status, next.Charges)
	}
	if c := next.Charges[0]; c.LineNo != 3 || !c.Amount.Equal(decimal.RequireFromString("0.8")) {
		t.Errorf("charge on line 3 = %+v; want 10%% of 8.00", c)
	}
	if c := next.Charges[1]; c.LineNo != 4 || c.ChargeCode != "FREIGHT" || !c.Auto || !c.Amount.Equal(decimal.NewFromInt(40)) {
		t.Errorf("auto charge = %+v; want FREIGHT 40 on the new line 4", c)
	}
	if len(next.AutoChargeCodes) != 1 || next.AutoChargeCodes[0] != fr.ID+"#4" {
		t.Errorf("autoChargeCodes %v", next.AutoChargeCodes)
	}
	// Line 1 is cancelled in this edit, so a charge on it is refused; line 2 was cancelled before.
	for _, n := range []string{"1", "2", "9"} {
		if _, status := edit(head + lines + `,"charges":[{"calc":"FIXED","value":"1","lineNo":` + n + `}]}`); status != http.StatusUnprocessableEntity {
			t.Errorf("a charge on line %s: status %d, want 422", n, status)
		}
	}
	// The PO has carried the line 4 freight before and the buyer removed it: it is not added again,
	// but a further new line (5) gets one.
	po.AutoChargeCodes = []string{fr.ID + "#4"}
	po.Lines = append(po.Lines, store.PurchaseOrderLine{LineNo: 4, ProductID: p1, Qty: decimal.NewFromInt(5), Unit: "EA",
		UnitPrice: decimal.NewFromInt(10), PriceUnit: decimal.NewFromInt(1), PriceSource: "MANUAL", Status: domain.POLineOpen})
	h.fake.poByID[po.ID] = po
	line := func(no string) string {
		return `{` + no + `"productId":"` + p1 + `","qty":"5","unit":"EA","unitPrice":"10","deliveryDate":"2026-04-01"}`
	}
	next, status = edit(head + `"lines":[` + line(`"lineNo":4,`) + `,` + line("") + `],"applyAutoCharges":true}`)
	if status != http.StatusOK || len(next.Charges) != 1 || next.Charges[0].LineNo != 5 {
		t.Errorf("status %d charges %+v; want only a line 5 freight charge", status, next.Charges)
	}
}

// The bound on header auto charges counts header charges only: line charges added before do not use it up,
// while more header auto charges than maxAutoCharges are refused.
func TestCreateOrder_AutoChargeBoundsCountHeaderAndLineChargesApart(t *testing.T) {
	day := mustDay(t, "2026-01-01")
	p1 := ids.New()
	lines := func(n int) string {
		out := make([]string, n)
		for i := range out {
			out[i] = `{"productId":"` + p1 + `","qty":"1","unit":"EA","unitPrice":"10","deliveryDate":"2026-04-01"}`
		}
		return `{"legalEntityId":"` + leA + `","vendorId":"` + ids.New() + `","currency":"USD","applyAutoCharges":true,"lines":[` + strings.Join(out, ",") + `]}`
	}

	h := newHarness(t)
	fr := h.seedCode(freight())
	hd := h.seedCode(domain.ChargeCode{Code: "HANDLING", Name: "Handling", Calc: domain.CalcFixed, DefaultValue: decimal.NewFromInt(5),
		DebitType: domain.ChargeSideItem, CreditType: domain.ChargeSideVendor, AllocationRule: domain.AllocateAmount})
	h.fake.charges.autos = []domain.AutoCharge{
		{ID: ids.New(), ChargeCodeID: fr.ID, AccountCode: domain.AutoChargeAll, ItemCode: domain.AutoChargeItemProduct, ItemID: p1, Currency: "USD", ValidFrom: day, Status: domain.ChargeActive},
		{ID: ids.New(), ChargeCodeID: hd.ID, AccountCode: domain.AutoChargeAll, ItemCode: domain.AutoChargeItemAll, Currency: "USD", ValidFrom: day, Status: domain.ChargeActive},
	}
	po, status, c := createOrder(h, lines(maxAutoCharges))
	if status != http.StatusCreated || len(po.Charges) != maxAutoCharges+1 || po.Charges[len(po.Charges)-1].LineNo != 0 {
		t.Fatalf("status %d (%s), %d charges; want a line charge on each of %d lines and the header charge", status, c, len(po.Charges), maxAutoCharges)
	}

	h = newHarness(t)
	h.fake.charges.autos = nil
	for i := 0; i <= maxAutoCharges; i++ {
		code := h.seedCode(domain.ChargeCode{Code: "C" + strconv.Itoa(i), Name: "Fee", Calc: domain.CalcFixed, DefaultValue: decimal.NewFromInt(1),
			DebitType: domain.ChargeSideItem, CreditType: domain.ChargeSideVendor, AllocationRule: domain.AllocateAmount})
		h.fake.charges.autos = append(h.fake.charges.autos, domain.AutoCharge{ID: ids.New(), ChargeCodeID: code.ID, AccountCode: domain.AutoChargeAll,
			ItemCode: domain.AutoChargeItemAll, Currency: "USD", ValidFrom: day, Status: domain.ChargeActive})
	}
	if _, status, _ := createOrder(h, lines(1)); status != http.StatusUnprocessableEntity {
		t.Errorf("%d header auto charges: status %d, want 422", maxAutoCharges+1, status)
	}
}
