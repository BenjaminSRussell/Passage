package domain

import "sort"

// PriceListEntry is one ACTIVE condition record that applies to a customer, with the scope
// that makes it apply: the customer itself, the customer's group, or everyone.
type PriceListEntry struct {
	Record Record
	Scope  string
}

// Price list scopes, most specific first.
const (
	ScopeCustomer = "CUSTOMER"
	ScopeGroup    = "GROUP"
	ScopeAll      = "ALL"
)

func scopeRank(s string) int {
	switch s {
	case ScopeCustomer:
		return 0
	case ScopeGroup:
		return 1
	}
	return 2
}

// PriceListFor keeps the records that apply to the customer (customer key, group key or all)
// and, when types is non-empty, only those condition types. The list is ordered by product,
// variant, unit, scope (customer, group, all) and from-quantity, so a reader sees the most
// specific agreement of a product first. It is an inquiry over the active records: a resolve
// remains the authority on which condition wins, because the access sequence also weighs the
// product dimension and search_next.
func PriceListFor(records []Record, customerID, customerGroupID string, types map[ConditionType]bool) []PriceListEntry {
	out := make([]PriceListEntry, 0, len(records))
	for _, r := range records {
		if len(types) > 0 && !types[r.Type] {
			continue
		}
		scope := ""
		switch {
		case r.CustomerKind == CustomerKindCustomer && r.CustomerKey == customerID:
			scope = ScopeCustomer
		case r.CustomerKind == CustomerKindGroup && customerGroupID != "" && r.CustomerKey == customerGroupID:
			scope = ScopeGroup
		case r.CustomerKind == CustomerKindAll:
			scope = ScopeAll
		default:
			continue
		}
		out = append(out, PriceListEntry{Record: r, Scope: scope})
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		switch {
		case a.Record.ProductKey != b.Record.ProductKey:
			return a.Record.ProductKey < b.Record.ProductKey
		case a.Record.Variant != b.Record.Variant:
			return a.Record.Variant < b.Record.Variant
		case a.Record.Unit != b.Record.Unit:
			return a.Record.Unit < b.Record.Unit
		case a.Record.Type != b.Record.Type:
			return a.Record.Type < b.Record.Type
		case a.Scope != b.Scope:
			return scopeRank(a.Scope) < scopeRank(b.Scope)
		case !a.Record.FromQuantity.Equal(b.Record.FromQuantity):
			return a.Record.FromQuantity.LessThan(b.Record.FromQuantity)
		}
		return a.Record.ID < b.Record.ID
	})
	return out
}
