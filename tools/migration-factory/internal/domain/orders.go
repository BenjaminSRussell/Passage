package domain

import (
	"fmt"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// TableOpenOrders names the open_orders pack in legacy_xref: its lines are held under the source
// table of their order type (OrderSourceTable).
const TableOpenOrders = "open_orders"

// OrderSourceTable is the legacy_xref source_table of a line of the order type: open_orders.sales
// or open_orders.purchase. Sales and purchase order lines come from two legacy tables (AX SalesLine
// and PurchLine, SAP VBAP and EKPO) whose row ids are unique per table only, so one legacy id may
// name a sales line and a purchase line, and the two must not collide in the cross-reference.
func OrderSourceTable(orderType string) string {
	return TableOpenOrders + "." + strings.ToLower(orderType)
}

// Order types of the open_orders pack: a sales order the customer has not been fully shipped, a
// purchase order the vendor has not fully delivered.
const (
	OrderSales    = "SALES"
	OrderPurchase = "PURCHASE"
)

// Control total names of the open_orders pack (FR-49.2).
const (
	OrdersQuantityName = "open_orders.open_quantity"
	OrdersValueName    = "open_orders.open_value"
	OrdersLineName     = "open_orders.line_count"
	OrdersOrderName    = "open_orders.order_count"
)

// OpenOrderLine is one open order line at the cutoff (AX SalesLine and PurchLine, SAP VBAP/VBEP
// and EKPO/EKET). OrderedQty is the quantity on the order and DeliveredQty what has been shipped
// or received; the open quantity is the difference, and the open value is that quantity at the
// order's unit price, rounded to six decimals (the scale of every control total). DeliveryDate is
// the confirmed or requested date of the open quantity, an ISO date, or blank.
type OpenOrderLine struct {
	LegacyID     string
	OrderType    string
	OrderNumber  string
	LineNumber   string
	Item         string
	Party        string
	Unit         string
	Currency     string
	OrderedQty   decimal.Decimal
	DeliveredQty decimal.Decimal
	UnitPrice    decimal.Decimal
	DeliveryDate string
}

// OpenQty is the quantity still to deliver.
func (o OpenOrderLine) OpenQty() decimal.Decimal { return o.OrderedQty.Sub(o.DeliveredQty) }

// OpenValue is the open quantity at the unit price, to six decimals.
func (o OpenOrderLine) OpenValue() decimal.Decimal { return o.OpenQty().Mul(o.UnitPrice).Round(6) }

// HeldKey identifies a line by its legacy id within its order type, as legacy_xref does through
// OrderSourceTable.
func (o OpenOrderLine) HeldKey() string { return o.OrderType + "\x1f" + o.LegacyID }

// Key is the natural key of a line: its order type, order and line number. Two lines with the
// same key would open the same order line twice (FR-49.10). The fields are joined with the ASCII
// unit separator so a "|" or "/" inside a legacy code cannot make two keys collide.
func (o OpenOrderLine) Key() string {
	return strings.Join([]string{o.OrderType, o.OrderNumber, o.LineNumber}, "\x1f")
}

// Record maps a line onto the shared record shape that quarantine keeps: the item in Account, the
// order and line in Dimensions, the party in Party and the open quantity in Amount.
func (o OpenOrderLine) Record() Record {
	return Record{LegacyID: o.LegacyID, Party: o.Party, Account: o.Item, Dimensions: o.OrderType + "/" + o.OrderNumber + "/" + o.LineNumber,
		Currency: o.Currency, Amount: o.OpenQty()}
}

// HashOpenOrders returns the run's input hash: the lines sorted, each as legacy id, natural key,
// party, item, unit, currency, ordered, delivered and price, so a changed delivery or price changes
// the hash (FR-49.10).
func HashOpenOrders(in []OpenOrderLine) string {
	keyed := make([]Keyed, len(in))
	for i, o := range in {
		keyed[i] = Keyed{LegacyID: strings.Join([]string{o.LegacyID, o.Key(), o.Party, o.Item, o.Unit, o.Currency,
			o.OrderedQty.String(), o.UnitPrice.String(), o.DeliveryDate}, "|"), Amount: o.DeliveredQty}
	}
	return HashRecords(keyed)
}

// OrderMasters are the masters the referential rule checks open orders against, given with the
// run: the item master and the customers (sales orders) and vendors (purchase orders).
type OrderMasters struct {
	Items     map[string]struct{}
	Customers map[string]struct{}
	Vendors   map[string]struct{}
}

// ValidateOpenOrders applies the open_orders suite (FR-49.11): uniqueness (a legacy id once per
// order type, and an order line once), referential (the item is in the item master, the party a customer of a
// sales order or a vendor of a purchase order) and domain (an order type of SALES or PURCHASE,
// order and line number, unit and an ISO 4217 currency present, a positive ordered quantity, a
// delivered quantity of zero or more and below the ordered one, a price of zero or more, an ISO
// delivery date when given). A fully delivered line is not open and is quarantined. A failing line
// is quarantined with the rule and reason; the caller loads the rest.
func ValidateOpenOrders(in []OpenOrderLine, m OrderMasters) (accepted []OpenOrderLine, quarantined []Quarantined) {
	seenID := make(map[string]struct{}, len(in))
	seenKey := make(map[string]struct{}, len(in))
	hold := func(o OpenOrderLine, rule, reason string) {
		quarantined = append(quarantined, Quarantined{Record: o.Record(), Rule: rule, Reason: reason, Line: o})
	}
	for _, o := range in {
		if strings.TrimSpace(o.LegacyID) == "" {
			hold(o, RuleUniqueness, "legacy id is blank")
			continue
		}
		if o.OrderType != OrderSales && o.OrderType != OrderPurchase {
			hold(o, RuleDomain, fmt.Sprintf("order type %q must be SALES or PURCHASE", o.OrderType))
			continue
		}
		if _, dup := seenID[o.HeldKey()]; dup {
			hold(o, RuleUniqueness, "legacy id appears twice among the extract's "+strings.ToLower(o.OrderType)+" order lines")
			continue
		}
		seenID[o.HeldKey()] = struct{}{}
		if o.OrderNumber == "" || o.LineNumber == "" {
			hold(o, RuleDomain, "order number and line number are required")
			continue
		}
		if _, dup := seenKey[o.Key()]; dup {
			hold(o, RuleUniqueness, "order type, order number and line number appear twice in the extract")
			continue
		}
		seenKey[o.Key()] = struct{}{}
		if _, ok := m.Items[o.Item]; !ok {
			hold(o, RuleReferential, fmt.Sprintf("item %q is not in the item master", o.Item))
			continue
		}
		parties, label := m.Customers, "customer"
		if o.OrderType == OrderPurchase {
			parties, label = m.Vendors, "vendor"
		}
		if _, ok := parties[o.Party]; !ok {
			hold(o, RuleReferential, fmt.Sprintf("%s %q is not in the %s master", label, o.Party, label))
			continue
		}
		switch {
		case o.Unit == "":
			hold(o, RuleDomain, "unit of measure is required")
		case !o.OrderedQty.IsPositive():
			hold(o, RuleDomain, fmt.Sprintf("ordered quantity %s must be positive", o.OrderedQty.String()))
		case o.DeliveredQty.IsNegative():
			hold(o, RuleDomain, fmt.Sprintf("delivered quantity %s must not be negative", o.DeliveredQty.String()))
		case !o.OpenQty().IsPositive():
			hold(o, RuleDomain, fmt.Sprintf("delivered quantity %s covers the ordered quantity %s; the line is not open", o.DeliveredQty.String(), o.OrderedQty.String()))
		case o.UnitPrice.IsNegative():
			hold(o, RuleDomain, fmt.Sprintf("unit price %s must not be negative", o.UnitPrice.String()))
		case o.DeliveryDate != "" && !isISODate(o.DeliveryDate):
			hold(o, RuleDomain, fmt.Sprintf("delivery date %q is not an ISO date (YYYY-MM-DD)", o.DeliveryDate))
		default:
			if _, ok := iso4217[o.Currency]; !ok {
				hold(o, RuleDomain, fmt.Sprintf("currency %q is not ISO 4217", o.Currency))
				continue
			}
			accepted = append(accepted, o)
		}
	}
	return accepted, quarantined
}

func isISODate(s string) bool {
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

// ReconcileOpenOrders finishes an open_orders run once the accepted lines are held. loaded is how
// many lines this run inserted; held maps each accepted line's HeldKey to the line Passage holds. It
// computes the FR-49.2 totals of the whole extract against what Passage holds: the open quantity
// per (order type, item, unit), the open value per (order type, currency), and the line and order
// counts per order type. The run moves from VALIDATED to LOADED and, when no total is MISMATCH, to
// RECONCILED. Quarantined lines are part of the source, so a run with quarantine stays LOADED.
func ReconcileOpenOrders(r *PackRun, source, accepted []OpenOrderLine, loaded int, held map[string]OpenOrderLine) error {
	if r.Status != StatusValidated {
		return fmt.Errorf("%w: reconcile needs a VALIDATED run, got %s", ErrTransition, r.Status)
	}
	r.RecordsLoaded = loaded
	r.AlreadyPresent = len(accepted) - loaded
	if err := r.Advance(StatusLoaded); err != nil {
		return err
	}
	var target []OpenOrderLine
	for _, o := range accepted {
		if h, ok := held[o.HeldKey()]; ok {
			target = append(target, h)
		}
	}
	src := source
	company := r.SourceCompany
	var totals []ControlTotal
	add := func(name string, key map[string]string, s, t decimal.Decimal) error {
		ct, err := NewControlTotal(name, key, s, t, Tolerance{})
		if err != nil {
			return err
		}
		totals = append(totals, ct)
		return nil
	}
	type agg struct {
		qty, value, lines decimal.Decimal
		orders            map[string]struct{}
		first             OpenOrderLine
	}
	group := func(in []OpenOrderLine, k func(OpenOrderLine) string) map[string]*agg {
		out := map[string]*agg{}
		for _, o := range in {
			a, ok := out[k(o)]
			if !ok {
				a = &agg{orders: map[string]struct{}{}, first: o}
				out[k(o)] = a
			}
			a.qty = a.qty.Add(o.OpenQty())
			a.value = a.value.Add(o.OpenValue())
			a.lines = a.lines.Add(decimal.NewFromInt(1))
			a.orders[o.OrderNumber] = struct{}{}
		}
		return out
	}
	keys := func(a, b map[string]*agg) []string {
		seen := map[string]struct{}{}
		for k := range a {
			seen[k] = struct{}{}
		}
		for k := range b {
			seen[k] = struct{}{}
		}
		return sortedKeys(seen)
	}
	pick := func(a, b map[string]*agg, k string) OpenOrderLine {
		if x, ok := a[k]; ok {
			return x.first
		}
		return b[k].first
	}
	val := func(a *agg, f func(*agg) decimal.Decimal) decimal.Decimal {
		if a == nil {
			return decimal.Zero
		}
		return f(a)
	}
	perItem := func(o OpenOrderLine) string { return o.OrderType + "\x1f" + o.Item + "\x1f" + o.Unit }
	s1, t1 := group(src, perItem), group(target, perItem)
	for _, k := range keys(s1, t1) {
		o := pick(s1, t1, k)
		if err := add(OrdersQuantityName, map[string]string{"company": company, "order_type": o.OrderType, "item": o.Item, "unit": o.Unit},
			val(s1[k], func(a *agg) decimal.Decimal { return a.qty }), val(t1[k], func(a *agg) decimal.Decimal { return a.qty })); err != nil {
			return err
		}
	}
	perCurrency := func(o OpenOrderLine) string { return o.OrderType + "\x1f" + o.Currency }
	s2, t2 := group(src, perCurrency), group(target, perCurrency)
	for _, k := range keys(s2, t2) {
		o := pick(s2, t2, k)
		if err := add(OrdersValueName, map[string]string{"company": company, "order_type": o.OrderType, "currency": o.Currency},
			val(s2[k], func(a *agg) decimal.Decimal { return a.value }), val(t2[k], func(a *agg) decimal.Decimal { return a.value })); err != nil {
			return err
		}
	}
	perType := func(o OpenOrderLine) string { return o.OrderType }
	s3, t3 := group(src, perType), group(target, perType)
	for _, k := range keys(s3, t3) {
		key := map[string]string{"company": company, "order_type": k}
		if err := add(OrdersLineName, key, val(s3[k], func(a *agg) decimal.Decimal { return a.lines }), val(t3[k], func(a *agg) decimal.Decimal { return a.lines })); err != nil {
			return err
		}
		orders := func(a *agg) decimal.Decimal { return decimal.NewFromInt(int64(len(a.orders))) }
		if err := add(OrdersOrderName, key, val(s3[k], orders), val(t3[k], orders)); err != nil {
			return err
		}
	}
	r.Totals = totals
	if err := r.applyTolerances(); err != nil {
		return err
	}
	if !r.HasMismatch() {
		return r.Advance(StatusReconciled)
	}
	return nil
}
