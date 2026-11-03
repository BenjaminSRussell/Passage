package domain

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"
)

func orderLine(id, typ, order, line, item, party, ordered, delivered, price string) OpenOrderLine {
	return OpenOrderLine{LegacyID: id, OrderType: typ, OrderNumber: order, LineNumber: line, Item: item, Party: party, Unit: "EA", Currency: "USD",
		OrderedQty: decimal.RequireFromString(ordered), DeliveredQty: decimal.RequireFromString(delivered), UnitPrice: decimal.RequireFromString(price)}
}

var orderMasters = OrderMasters{
	Items:     map[string]struct{}{"BOLT": {}, "NUT": {}},
	Customers: map[string]struct{}{"C1": {}},
	Vendors:   map[string]struct{}{"V1": {}},
}

func TestValidateOpenOrders_Rules(t *testing.T) {
	bad := orderLine("L9", "SALES", "SO9", "1", "BOLT", "C1", "5", "0", "1")
	bad.DeliveryDate = "01/02/2027"
	cur := orderLine("L10", "SALES", "SO10", "1", "BOLT", "C1", "5", "0", "1")
	cur.Currency = "US"
	in := []OpenOrderLine{
		orderLine("L1", "SALES", "SO1", "1", "BOLT", "C1", "10", "4", "2.5"),
		orderLine("L1", "SALES", "SO1", "2", "NUT", "C1", "1", "0", "1"),    // legacy id twice
		orderLine("L2", "SALES", "SO1", "1", "NUT", "C1", "1", "0", "1"),    // order line twice
		orderLine("L3", "SALES", "SO3", "1", "GONE", "C1", "1", "0", "1"),   // item unknown
		orderLine("L4", "SALES", "SO4", "1", "BOLT", "V1", "1", "0", "1"),   // vendor on a sales order
		orderLine("L5", "PURCHASE", "PO5", "1", "NUT", "V1", "3", "0", "0"), // free of charge is legal
		orderLine("L6", "SALES", "SO6", "1", "BOLT", "C1", "3", "3", "1"),   // fully delivered
		orderLine("L7", "TRANSFER", "TO7", "1", "BOLT", "C1", "3", "0", "1"),
		orderLine("L8", "SALES", "SO8", "1", "BOLT", "C1", "3", "-1", "1"),
		bad, cur,
		orderLine("", "SALES", "SO11", "1", "BOLT", "C1", "1", "0", "1"),
		orderLine("L12", "PURCHASE", "PO12", "1", "BOLT", "C1", "1", "0", "1"), // customer on a purchase order
	}
	ok, q := ValidateOpenOrders(in, orderMasters)
	if len(ok) != 2 || ok[0].LegacyID != "L1" || ok[1].LegacyID != "L5" {
		t.Fatalf("accepted %+v", ok)
	}
	want := []string{RuleUniqueness, RuleUniqueness, RuleReferential, RuleReferential, RuleDomain, RuleDomain, RuleDomain, RuleDomain, RuleDomain, RuleUniqueness, RuleReferential}
	if len(q) != len(want) {
		t.Fatalf("quarantined %d: %+v", len(q), q)
	}
	for i, rule := range want {
		if q[i].Rule != rule {
			t.Errorf("quarantine %d is %s, want %s (%s)", i, q[i].Rule, rule, q[i].Reason)
		}
	}
	if got := ok[0].OpenQty().String(); got != "6" {
		t.Errorf("open quantity %s", got)
	}
	if got := ok[0].OpenValue().String(); got != "15" {
		t.Errorf("open value %s", got)
	}
}

func TestReconcileOpenOrders_MatchAndQuarantineMismatch(t *testing.T) {
	lines := []OpenOrderLine{
		orderLine("L1", "SALES", "SO1", "1", "BOLT", "C1", "10", "4", "2.5"),
		orderLine("L2", "SALES", "SO1", "2", "NUT", "C1", "4", "0", "1.333333"),
		orderLine("L3", "PURCHASE", "PO1", "1", "BOLT", "V1", "7", "0", "3"),
	}
	run := func(src []OpenOrderLine) *PackRun {
		r := &PackRun{Pack: PackOpenOrders, SourceCompany: "USMF", Status: StatusValidated}
		held := map[string]OpenOrderLine{}
		for _, l := range lines {
			held[l.HeldKey()] = l
		}
		if err := ReconcileOpenOrders(r, src, lines, 3, held); err != nil {
			t.Fatal(err)
		}
		return r
	}
	r := run(lines)
	if r.Status != StatusReconciled || r.RecordsLoaded != 3 {
		t.Fatalf("status %s totals %+v", r.Status, r.Totals)
	}
	names := map[string]int{}
	for _, c := range r.Totals {
		names[c.Name]++
	}
	// 3 (type, item, unit) groups, 2 (type, currency), 2 line counts and 2 order counts.
	if names[OrdersQuantityName] != 3 || names[OrdersValueName] != 2 || names[OrdersLineName] != 2 || names[OrdersOrderName] != 2 {
		t.Fatalf("totals %v", names)
	}
	// 1.333333 x 4 = 5.333332, kept to six decimals on both sides.
	for _, c := range r.Totals {
		if c.Name == OrdersValueName && c.Key["order_type"] == "SALES" && c.Source.String() != "20.333332" {
			t.Errorf("sales value %s", c.Source.String())
		}
	}
	// A quarantined line is part of the source and not of what Passage holds: the run stays LOADED.
	extra := append(append([]OpenOrderLine{}, lines...), orderLine("L4", "SALES", "SO4", "1", "BOLT", "C1", "2", "0", "1"))
	if r := run(extra); r.Status != StatusLoaded || !r.HasMismatch() {
		t.Fatalf("with quarantine: %s", r.Status)
	}
	if err := ReconcileOpenOrders(&PackRun{Status: StatusExtracted}, lines, lines, 0, nil); !errors.Is(err, ErrTransition) {
		t.Fatalf("wrong status: %v", err)
	}
}

func TestHashOpenOrders_ChangesWithDeliveryAndIgnoresOrder(t *testing.T) {
	a := orderLine("L1", "SALES", "SO1", "1", "BOLT", "C1", "10", "4", "2.5")
	b := orderLine("L2", "PURCHASE", "PO1", "1", "NUT", "V1", "7", "0", "3")
	if HashOpenOrders([]OpenOrderLine{a, b}) != HashOpenOrders([]OpenOrderLine{b, a}) {
		t.Error("order matters")
	}
	c := a
	c.DeliveredQty = decimal.NewFromInt(5)
	if HashOpenOrders([]OpenOrderLine{a, b}) == HashOpenOrders([]OpenOrderLine{c, b}) {
		t.Error("delivery change not in hash")
	}
}

func TestOpenOrdersPackIsAvailableAndStaged(t *testing.T) {
	if !PackAvailable(PackOpenOrders) || NotPosted(PackOpenOrders) == "" {
		t.Fatal("open_orders must run and be staged until sales and purchasing have import APIs")
	}
}

// A sales and a purchase order line come from two legacy tables whose row ids are unique per table
// only: the same legacy id and the same order number on one of each are two lines, held under two
// source tables, while the same legacy id twice within one order type is a duplicate.
func TestValidateOpenOrders_LegacyIDAndOrderNumberAreScopedByOrderType(t *testing.T) {
	in := []OpenOrderLine{
		orderLine("5637144576", "SALES", "00042", "1", "BOLT", "C1", "2", "0", "1"),
		orderLine("5637144576", "PURCHASE", "00042", "1", "BOLT", "V1", "2", "0", "1"),
		orderLine("5637144576", "PURCHASE", "00043", "1", "NUT", "V1", "2", "0", "1"),
	}
	ok, q := ValidateOpenOrders(in, orderMasters)
	if len(ok) != 2 || len(q) != 1 || q[0].Rule != RuleUniqueness || q[0].Record.Dimensions != "PURCHASE/00043/1" {
		t.Fatalf("accepted %+v quarantined %+v", ok, q)
	}
	if ok[0].HeldKey() == ok[1].HeldKey() || ok[0].Key() == ok[1].Key() {
		t.Fatal("a sales and a purchase line must not share a key")
	}
	if OrderSourceTable(OrderSales) != "open_orders.sales" || OrderSourceTable(OrderPurchase) != "open_orders.purchase" {
		t.Fatalf("source tables %s %s", OrderSourceTable(OrderSales), OrderSourceTable(OrderPurchase))
	}
}
