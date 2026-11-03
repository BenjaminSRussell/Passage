package domain

import (
	"fmt"
	"strings"

	"github.com/shopspring/decimal"
)

// TableOnhand is the legacy_xref source_table the onhand pack writes.
const TableOnhand = "onhand"

// Control total names of the onhand pack (FR-49.2).
const (
	OnhandQuantityName = "onhand.quantity"
	OnhandValueName    = "onhand.value"
	OnhandEntityName   = "onhand.value_entity"
	OnhandAccountName  = "onhand.inventory_account"
)

// OnhandLine is one legacy on-hand line at the cutoff, as extracted for the onhand pack
// (FR-49.1; AX InventSum with InventDim, SAP MARD/MCHB/MSKA). Quantity is in Unit, Value the
// total cost of the quantity at legacy cost (BR-49.3), not a unit price. Batch and Serial are
// blank for stock that is not tracked.
type OnhandLine struct {
	LegacyID  string
	Item      string
	Site      string
	Warehouse string
	Location  string
	Batch     string
	Serial    string
	Unit      string
	Currency  string
	Quantity  decimal.Decimal
	Value     decimal.Decimal
}

// Key is the natural key of a line: item and the full inventory dimension. Two rows with the
// same key would open the same stock twice (FR-49.10). The fields are joined with the ASCII unit
// separator so a "|" or "/" inside a legacy code cannot make two different keys collide.
func (o OnhandLine) Key() string {
	return strings.Join([]string{o.Item, o.Site, o.Warehouse, o.Location, o.Batch, o.Serial}, "\x1f")
}

// serialKey is the item and serial of a serial-tracked line, or "" for a line without a serial.
// A serial is one physical unit: it is in one place, so it appears once per item and company.
func (o OnhandLine) serialKey() string {
	if o.Serial == "" {
		return ""
	}
	return o.Item + "\x1f" + o.Serial
}

// DimensionText is the inventory dimension as one display value, for the quarantine view.
func (o OnhandLine) DimensionText() string {
	return strings.Join([]string{o.Site, o.Warehouse, o.Location, o.Batch, o.Serial}, "/")
}

// Record maps a line onto the shared record shape that quarantine keeps: the item in Account,
// the dimension in Dimensions and the quantity in Amount.
func (o OnhandLine) Record() Record {
	return Record{LegacyID: o.LegacyID, Account: o.Item, Dimensions: o.DimensionText(), Currency: o.Currency, Amount: o.Quantity}
}

// HashOnhand returns the run's input hash: the lines sorted, each as legacy id, natural key,
// currency and quantity, with the value so a changed cost changes the hash (FR-49.10).
func HashOnhand(in []OnhandLine) string {
	keyed := make([]Keyed, len(in))
	for i, o := range in {
		keyed[i] = Keyed{LegacyID: o.LegacyID + "|" + o.Key() + "|" + o.Unit + "|" + o.Currency + "|" + o.Value.String(), Amount: o.Quantity}
	}
	return HashRecords(keyed)
}

// ValidateOnhand applies the onhand suite (FR-49.11): uniqueness (a legacy id and a natural key
// appear once, and a serial once per item), referential (the item is in the item master given with the run) and domain
// (site and warehouse present, unit present, ISO 4217 currency, a positive quantity, a
// non-negative value, a serial-tracked line of exactly one). A failing line is quarantined with
// the rule and reason; the caller loads the rest.
func ValidateOnhand(in []OnhandLine, items map[string]struct{}) (accepted []OnhandLine, quarantined []Quarantined) {
	seenID := make(map[string]struct{}, len(in))
	seenKey := make(map[string]struct{}, len(in))
	seenSerial := make(map[string]struct{})
	hold := func(o OnhandLine, rule, reason string) {
		quarantined = append(quarantined, Quarantined{Record: o.Record(), Rule: rule, Reason: reason})
	}
	for _, o := range in {
		if strings.TrimSpace(o.LegacyID) == "" {
			hold(o, RuleUniqueness, "legacy id is blank")
			continue
		}
		if _, dup := seenID[o.LegacyID]; dup {
			hold(o, RuleUniqueness, "legacy id appears twice in the extract")
			continue
		}
		seenID[o.LegacyID] = struct{}{}
		if _, dup := seenKey[o.Key()]; dup {
			hold(o, RuleUniqueness, "item, site, warehouse, location, batch and serial appear twice in the extract")
			continue
		}
		seenKey[o.Key()] = struct{}{}
		if sk := o.serialKey(); sk != "" {
			if _, dup := seenSerial[sk]; dup {
				hold(o, RuleUniqueness, fmt.Sprintf("serial %q of item %q appears twice in the extract; a serial is in one place", o.Serial, o.Item))
				continue
			}
			seenSerial[sk] = struct{}{}
		}
		if _, ok := items[o.Item]; !ok {
			hold(o, RuleReferential, fmt.Sprintf("item %q is not in the item master", o.Item))
			continue
		}
		switch {
		case o.Site == "" || o.Warehouse == "":
			hold(o, RuleDomain, "site and warehouse are required")
		case o.Unit == "":
			hold(o, RuleDomain, "unit of measure is required")
		case !o.Quantity.IsPositive():
			hold(o, RuleDomain, fmt.Sprintf("quantity %s must be positive; negative or zero stock is not opened", o.Quantity.String()))
		case o.Value.IsNegative():
			hold(o, RuleDomain, fmt.Sprintf("value %s must not be negative", o.Value.String()))
		case o.Serial != "" && !o.Quantity.Equal(decimal.NewFromInt(1)):
			hold(o, RuleDomain, fmt.Sprintf("serial %q holds quantity %s; a serial is exactly one", o.Serial, o.Quantity.String()))
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

// ReconcileOnhand finishes an onhand run once the accepted lines are held. loaded is how many
// lines this run inserted; held maps each accepted legacy id to the line Passage holds for it.
// glTotals are the control totals of a gl_balances run of the same company to reconcile with,
// and account the inventory GL account to compare (BR-49.3); both are empty otherwise.
//
// It computes the FR-49.2 onhand totals of the whole extract against what Passage holds: the
// quantity per item and inventory dimension, the value per (item, site, currency) and the value
// per entity and currency. With a GL run it adds onhand.inventory_account: the migrated balance
// of the inventory account (all its dimensions) against the loaded on-hand value, to 0.00. The
// run moves from VALIDATED to LOADED and, when no total is MISMATCH, to RECONCILED. Quarantined
// lines are part of the source, so a run with quarantine stays LOADED.
func ReconcileOnhand(r *PackRun, source, accepted []OnhandLine, loaded int, held map[string]OnhandLine, account string, glTotals []ControlTotal) error {
	if r.Status != StatusValidated {
		return fmt.Errorf("%w: reconcile needs a VALIDATED run, got %s", ErrTransition, r.Status)
	}
	r.RecordsLoaded = loaded
	r.AlreadyPresent = len(accepted) - loaded
	if err := r.Advance(StatusLoaded); err != nil {
		return err
	}
	var target []OnhandLine
	for _, o := range accepted {
		if h, ok := held[o.LegacyID]; ok {
			target = append(target, h)
		}
	}
	company := r.SourceCompany
	var totals []ControlTotal
	add := func(name string, key map[string]string, src, tgt decimal.Decimal) error {
		t, err := NewControlTotal(name, key, src, tgt, Tolerance{})
		if err != nil {
			return err
		}
		totals = append(totals, t)
		return nil
	}
	srcQty, tgtQty := sumOnhand(source, OnhandLine.Key), sumOnhand(target, OnhandLine.Key)
	for _, k := range onhandKeys(srcQty, tgtQty) {
		o := firstOf(srcQty, tgtQty, k)
		key := map[string]string{"company": company, "item": o.Item, "site": o.Site, "warehouse": o.Warehouse,
			"location": o.Location, "batch": o.Batch, "serial": o.Serial}
		if err := add(OnhandQuantityName, key, srcQty[k].Quantity, tgtQty[k].Quantity); err != nil {
			return err
		}
	}
	itemSite := func(o OnhandLine) string { return o.Item + "|" + o.Site + "|" + o.Currency }
	srcVal, tgtVal := sumOnhand(source, itemSite), sumOnhand(target, itemSite)
	for _, k := range onhandKeys(srcVal, tgtVal) {
		o := firstOf(srcVal, tgtVal, k)
		if err := add(OnhandValueName, map[string]string{"company": company, "item": o.Item, "site": o.Site, "currency": o.Currency},
			srcVal[k].Value, tgtVal[k].Value); err != nil {
			return err
		}
	}
	entity := func(o OnhandLine) string { return o.Currency }
	srcEnt, tgtEnt := sumOnhand(source, entity), sumOnhand(target, entity)
	for _, k := range onhandKeys(srcEnt, tgtEnt) {
		if err := add(OnhandEntityName, map[string]string{"company": company, "currency": k}, srcEnt[k].Value, tgtEnt[k].Value); err != nil {
			return err
		}
	}
	if account != "" {
		gl := map[string]decimal.Decimal{}
		for _, t := range glTotals {
			if t.Name == "gl_balances.balance" && t.Key["account"] == account {
				gl[t.Key["currency"]] = gl[t.Key["currency"]].Add(t.Target)
			}
		}
		curs := map[string]struct{}{}
		for k := range gl {
			curs[k] = struct{}{}
		}
		for k := range tgtEnt {
			curs[k] = struct{}{}
		}
		for _, cur := range sortedKeys(curs) {
			if err := add(OnhandAccountName, map[string]string{"company": company, "account": account, "currency": cur},
				gl[cur], tgtEnt[cur].Value); err != nil {
				return err
			}
		}
	}
	r.Totals = totals
	if !r.HasMismatch() {
		return r.Advance(StatusReconciled)
	}
	return nil
}

// notPosted names the packs whose load stays in the factory: the run reconciles the factory's own
// opening rows against the extract and nothing reaches the target ledger yet. A RECONCILED run of
// such a pack is not stock or balances in Passage, so the go/no-go checklist does not count it.
var notPosted = map[Pack]string{
	PackOnhand:     "staged in the factory only, not posted to the inventory ledger (H-S9-1)",
	PackGLBalances: "staged in the factory only, not posted to the general ledger (wave 12 handoff)",
	PackProjectWIP: "staged in the factory only, not posted to project operations (D7)",
	PackBankBal:    "staged in the factory only, not posted to treasury (H-S10-1)",
}

// NotPosted reports why a pack's loaded rows are not in the target ledger, or "" when they are or
// the pack has no target.
func NotPosted(p Pack) string { return notPosted[p] }

// OnhandAccountMessage renders the BR-49.3 mismatch: the inventory GL account against the
// loaded on-hand value.
func OnhandAccountMessage(t ControlTotal) string {
	return fmt.Sprintf("MIG-1502 Inventory account %s migrated balance (%s %s) differs from loaded on-hand value (%s %s).",
		t.Key["account"], t.Source.StringFixed(2), t.Key["currency"], t.Target.StringFixed(2), t.Key["currency"])
}

// sumOnhand sums quantity and value by the group function; the first line of a group supplies
// the identity fields. Quantities of lines grouped for value are not meaningful across units, so
// only the value sums are read there.
func sumOnhand(in []OnhandLine, group func(OnhandLine) string) map[string]OnhandLine {
	out := make(map[string]OnhandLine, len(in))
	for _, o := range in {
		k := group(o)
		cur, ok := out[k]
		if !ok {
			cur = o
			cur.Quantity, cur.Value = decimal.Zero, decimal.Zero
		}
		cur.Quantity = cur.Quantity.Add(o.Quantity)
		cur.Value = cur.Value.Add(o.Value)
		out[k] = cur
	}
	return out
}

func onhandKeys(a, b map[string]OnhandLine) []string {
	seen := make(map[string]struct{}, len(a)+len(b))
	for k := range a {
		seen[k] = struct{}{}
	}
	for k := range b {
		seen[k] = struct{}{}
	}
	return sortedKeys(seen)
}

func firstOf(a, b map[string]OnhandLine, k string) OnhandLine {
	if o, ok := a[k]; ok {
		return o
	}
	return b[k]
}
