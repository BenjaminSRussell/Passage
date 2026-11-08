package domain

import (
	"sort"

	"github.com/shopspring/decimal"
)

// MaxNetPriceBreaks caps the quantity breaks resolved for one product row.
const MaxNetPriceBreaks = 20

// NetPriceBreak is the line-level result of pricing one quantity of a product row: the same
// unit price, net unit price, line net and applied conditions a one-line resolve of that
// quantity returns. Order-level conditions (total discount, small-order surcharge) and free
// goods proposals are not part of it.
type NetPriceBreak struct {
	Quantity      decimal.Decimal `json:"quantity"`
	UnitPrice     decimal.Decimal `json:"unitPrice"`
	NetUnitPrice  decimal.Decimal `json:"netUnitPrice"`
	LineNet       decimal.Decimal `json:"lineNet"`
	PriceRecordID string          `json:"priceRecordId,omitempty"`
	Applied       []Applied       `json:"applied"`
	ErrorCode     string          `json:"errorCode,omitempty"`
}

// NetPriceRow is one product dimension of a customer's price list with the net price at each
// of its quantity breaks.
type NetPriceRow struct {
	ProductKind ProductKind     `json:"productKind"`
	ProductKey  string          `json:"productKey"`
	Variant     string          `json:"variant"`
	Unit        string          `json:"unit"`
	Breaks      []NetPriceBreak `json:"breaks"`
}

// NetPriceFilter narrows the rows of a net price list. ProductID keeps the rows keyed to that
// product or one of its variants, ProductGroupID the rows keyed to that product group; with
// both, a row matching either is kept. Empty keeps every row. Filtering picks rows only: the
// breaks of a kept row still come from every entry that prices a line of it.
type NetPriceFilter struct {
	ProductID      string
	ProductGroupID string
}

func (f NetPriceFilter) keeps(k netRowKey) bool {
	if f.ProductID == "" && f.ProductGroupID == "" {
		return true
	}
	switch k.kind {
	case ProductKindProduct, ProductKindVariant:
		return f.ProductID != "" && k.key == f.ProductID
	case ProductKindGroup:
		return f.ProductGroupID != "" && k.key == f.ProductGroupID
	}
	return false
}

// Entries keeps the price list entries the filter keeps, by the product dimension of the record
// (an all-products record is kept only when the filter is empty), in their order. Empty returns
// the list as it is.
func (f NetPriceFilter) Entries(list []PriceListEntry) []PriceListEntry {
	if f.ProductID == "" && f.ProductGroupID == "" {
		return list
	}
	out := make([]PriceListEntry, 0, len(list))
	for _, e := range list {
		if f.keeps(netRowKey{kind: e.Record.ProductKind, key: e.Record.ProductKey}) {
			out = append(out, e)
		}
	}
	return out
}

type netRowKey struct {
	kind    ProductKind
	key     string
	variant string
	unit    string
}

// line is the one-line sale a row is priced as: a product (and variant) line, a line of the
// product group, or a line with no product for an all-products record.
func (k netRowKey) line(q decimal.Decimal) Line {
	l := Line{No: 1, Variant: k.variant, Unit: k.unit, Quantity: q}
	switch k.kind {
	case ProductKindProduct, ProductKindVariant:
		l.ProductID = k.key
	case ProductKindGroup:
		l.ProductGroupID = k.key
	}
	return l
}

// prices reports whether record r can price a line of row k, which is what makes its
// from-quantity a break of the row: the record's unit is empty or the row's, and its product
// dimension is all products or the row's own (a product record also prices its variants).
func (k netRowKey) prices(r Record) bool {
	if r.Unit != "" && r.Unit != k.unit {
		return false
	}
	switch r.ProductKind {
	case ProductKindAll:
		return true
	case ProductKindProduct:
		return (k.kind == ProductKindProduct || k.kind == ProductKindVariant) && r.ProductKey == k.key
	case ProductKindVariant:
		return k.kind == ProductKindVariant && r.ProductKey == k.key && r.Variant == k.variant
	case ProductKindGroup:
		return k.kind == ProductKindGroup && r.ProductKey == k.key
	}
	return false
}

// NetPriceLimits bounds the work of one net price list: at most Rows rows and Breaks breaks in all,
// each break being one one-line resolve.
type NetPriceLimits struct {
	Rows   int
	Breaks int
}

// NetPriceList resolves the net price of the product rows of a price list at each quantity
// break (PRD 5.26.14, "resolve (batch)"). Rows are the product, variant and unit combinations
// of the PRICE entries, in entry order, kept by filter. A row's breaks are the from-quantities
// of the price, line discount and multiline discount entries that can price a line of it (its
// own, its product's for a variant, and the all-products ones, with its unit or none), at or
// above its lowest price break, lowest first and at most MaxNetPriceBreaks; a from-quantity of
// zero is the break at 1. Rows are taken in order while they fit limits; the first row that
// does not fit ends the list and truncated reports it, so no row is resolved past the limits
// and none is returned with fewer breaks than it has. Each break is priced as a one-line resolve
// over in.Header, in.Set (or in.Records) and in.Procedure, so its figures are the line of a
// resolve of that quantity. A product row is priced without a product group (the product master
// is not known here), a group row as a line of that group, an all-products row as a line with
// no product.
func NetPriceList(in Input, entries []PriceListEntry, filter NetPriceFilter, limits NetPriceLimits) (rows []NetPriceRow, truncated bool) {
	// Index the break entries by product key so each row reads only the entries that can price it.
	byKey, anyProduct := map[string][]Record{}, []Record(nil)
	for _, e := range entries {
		switch e.Record.Type {
		case TypePrice, TypeLineDisc, TypeMultiline:
		default:
			continue
		}
		if e.Record.ProductKind == ProductKindAll {
			anyProduct = append(anyProduct, e.Record)
		} else {
			byKey[e.Record.ProductKey] = append(byKey[e.Record.ProductKey], e.Record)
		}
	}
	type planned struct {
		key netRowKey
		qty []decimal.Decimal
	}
	var todo []planned
	seen, budget := map[netRowKey]bool{}, limits.Breaks
	for _, e := range entries {
		if e.Record.Type != TypePrice {
			continue
		}
		k := netRowKey{e.Record.ProductKind, e.Record.ProductKey, e.Record.Variant, e.Record.Unit}
		if seen[k] || !filter.keeps(k) {
			continue
		}
		seen[k] = true
		qty := breaksOf(k, byKey[k.key], anyProduct)
		if len(todo) == limits.Rows || len(qty) > budget {
			truncated = true
			break
		}
		budget -= len(qty)
		todo = append(todo, planned{k, qty})
	}
	rows = make([]NetPriceRow, 0, len(todo))
	if len(todo) == 0 {
		return rows, truncated
	}
	recs := in.Set.index()
	if in.Set == nil {
		recs = bucket(in.Records)
	}
	plan := in.plan()
	for _, p := range todo {
		k := p.key
		row := NetPriceRow{ProductKind: k.kind, ProductKey: k.key, Variant: k.variant, Unit: k.unit, Breaks: make([]NetPriceBreak, 0, len(p.qty))}
		for _, q := range p.qty {
			// A one-line document: the multiline group quantity is the line's own (Resolve's groupQuantities).
			lr := resolveLine(in.Header, k.line(q), recs, plan, q)
			b := NetPriceBreak{Quantity: q, UnitPrice: lr.UnitPrice, NetUnitPrice: lr.NetUnit, LineNet: lr.LineNet, Applied: lr.Applied, ErrorCode: lr.Error}
			for _, ap := range lr.Applied {
				if ap.Type == TypePrice {
					b.PriceRecordID = ap.RecordID
				}
			}
			row.Breaks = append(row.Breaks, b)
		}
		rows = append(rows, row)
	}
	return rows, truncated
}

// breaksOf is the sorted, capped break quantities of row k from the candidate records.
func breaksOf(k netRowKey, own, anyProduct []Record) []decimal.Decimal {
	one := decimal.NewFromInt(1)
	floor, hasPrice := decimal.Zero, false
	qtys := map[string]decimal.Decimal{}
	for _, list := range [][]Record{own, anyProduct} {
		for _, r := range list {
			if !k.prices(r) {
				continue
			}
			q := r.FromQuantity
			if !q.IsPositive() {
				q = one
			}
			if r.Type == TypePrice && (!hasPrice || q.LessThan(floor)) {
				floor, hasPrice = q, true
			}
			qtys[q.String()] = q
		}
	}
	out := make([]decimal.Decimal, 0, len(qtys))
	for _, q := range qtys {
		if !q.LessThan(floor) {
			out = append(out, q)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LessThan(out[j]) })
	if len(out) > MaxNetPriceBreaks {
		out = out[:MaxNetPriceBreaks]
	}
	return out
}
