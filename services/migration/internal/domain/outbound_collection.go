package domain

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/BenjaminSRussell/passage/platform/ids"
)

// Collections (integration track S5 follow-up): array iteration in the outbound mapping. A collection maps every
// element of an array in the event (an order's data.lines) to one object of an array in the request body, so one push
// creates the header and all its lines in the legacy ERP. Each element is mapped by the collection's own field rules,
// with its own xref and lookup resolution, so each line gets its own product code and converted quantity or price.
//
// The row an element's rules read is the element itself (source "sku" reads the element's sku), plus
//
//	parent.*  the whole event (parent.data.currency, parent.legalentityid)
//	index     the element's position, from 0
//	xref.<n>  the collection's xrefs, resolved for this element (idPath is relative to the element)
//	lookup.<n> the collection's lookups, read for this element (keyPaths and quantityPath are relative to the element;
//	          a path starting with parent. reads the event)
//
// so "parent", "index", "xref" and "lookup" are not usable as element field names.

const (
	// DefaultCollectionItems is the element limit when a collection sets none; MaxCollectionItems is the hard limit.
	DefaultCollectionItems = 200
	MaxCollectionItems     = 1000
	maxCollections         = 4
	maxUnresolvedPerList   = 50
)

var collectionNameRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,39}(\.[A-Za-z][A-Za-z0-9_]{0,39}){0,3}$`)

// OutboundCollection maps an event array to a body array.
type OutboundCollection struct {
	// Name is the body target of the array; dots nest ("Header.Lines"). It may not collide with a mapping target.
	Name string `json:"name"`
	// Path is the dotted path of the array in the event, such as data.lines.
	Path string `json:"path"`
	// MaxItems is the most elements one event may carry (default 200, at most 1000); more is refused, not truncated.
	MaxItems int `json:"maxItems,omitempty"`
	// Optional leaves the array out of the body when the event has none; otherwise a missing or empty array is refused.
	Optional bool `json:"optional,omitempty"`
	// Subject names the Passage record each element creates in the legacy ERP (an order line); IDPath is relative to
	// the element. The render lists one ref per element, so the hub can record the legacy key the ERP assigns each
	// line with xref:record and a later change to a line updates it by key.
	Subject *OutboundSubject `json:"subject,omitempty"`
	// Xrefs resolve an id of each element to its legacy key; IdPath is relative to the element.
	Xrefs []OutboundXref `json:"xrefs,omitempty"`
	// Lookups read lookup table entries for each element.
	Lookups []OutboundLookup `json:"lookups,omitempty"`
	// Fields map each element to a body object, with the mapping DSL of the definition's own mapping.
	Fields     []FieldRule  `json:"fields"`
	RejectWhen []RejectWhen `json:"rejectWhen,omitempty"`
}

func (d OutboundDefinition) itemMapping(c OutboundCollection) (Mapping, error) {
	raw, err := json.Marshal(Mapping{Entity: c.Name, Version: max(d.Mapping.Version, 1), SourceTimezone: d.Mapping.SourceTimezone, Fields: c.Fields, RejectWhen: c.RejectWhen})
	if err != nil {
		return Mapping{}, &Error{CodeOutboundInvalid, err.Error()}
	}
	m, err := ParseMapping(raw)
	if err != nil {
		if e, ok := err.(*Error); ok {
			return Mapping{}, &Error{CodeOutboundInvalid, "collection " + c.Name + ": " + e.Detail}
		}
		return Mapping{}, err
	}
	return m, nil
}

func (d OutboundDefinition) checkCollections() error {
	bad := func(c OutboundCollection, format string, a ...any) error {
		return &Error{CodeOutboundInvalid, "collection " + c.Name + ": " + fmt.Sprintf(format, a...)}
	}
	if len(d.Collections) > maxCollections {
		return &Error{CodeOutboundInvalid, fmt.Sprintf("at most %d collections", maxCollections)}
	}
	names, paths := map[string]bool{}, map[string]bool{}
	for _, c := range d.Collections {
		switch {
		case !collectionNameRE.MatchString(c.Name):
			return bad(c, "name must be a target such as Lines or Header.Lines (letters, digits, _, at most 4 segments)")
		case names[c.Name]:
			return bad(c, "duplicate collection name")
		case !eventPathRE.MatchString(c.Path):
			return bad(c, "path must be a dotted path to an array in the event, such as data.lines")
		case paths[c.Path]:
			return bad(c, "path %s is already used by another collection", c.Path)
		case c.MaxItems < 0 || c.MaxItems > MaxCollectionItems:
			return bad(c, "maxItems must be 1 to %d", MaxCollectionItems)
		case c.Subject != nil && !xrefEntityRE.MatchString(c.Subject.Entity):
			return bad(c, "subject.entity must start with a letter and use letters, digits, _ . -")
		case c.Subject != nil && !eventPathRE.MatchString(c.Subject.IDPath):
			return bad(c, "subject.idPath must be a dotted path into the element, such as id")
		case len(c.Xrefs) > maxOutboundXrefs:
			return bad(c, "at most %d xrefs", maxOutboundXrefs)
		case len(c.Lookups) > maxOutboundLookups:
			return bad(c, "at most %d lookups", maxOutboundLookups)
		}
		names[c.Name], paths[c.Path] = true, true
		seen := map[string]bool{}
		for _, x := range c.Xrefs {
			switch {
			case !xrefNameRE.MatchString(x.Name) || x.Name == selfXrefName:
				return bad(c, "xref name %q must start with a lower-case letter, use letters, digits and _, and not be %q", x.Name, selfXrefName)
			case seen[x.Name]:
				return bad(c, "duplicate xref name %q", x.Name)
			case !xrefEntityRE.MatchString(x.Entity):
				return bad(c, "xref %s: entity must start with a letter and use letters, digits, _ . -", x.Name)
			case !eventPathRE.MatchString(x.IDPath):
				return bad(c, "xref %s: idPath must be a dotted path into the element", x.Name)
			}
			seen[x.Name] = true
		}
		seenLookup := map[string]bool{}
		for _, l := range c.Lookups {
			if err := l.check(); err != nil {
				return bad(c, "%s", strings.TrimPrefix(err.Error(), CodeOutboundInvalid+": "))
			}
			if seenLookup[l.Name] {
				return bad(c, "duplicate lookup name %q", l.Name)
			}
			seenLookup[l.Name] = true
		}
		if err := checkTargets(fieldTargets(c.Fields)); err != nil {
			return bad(c, "%s", strings.TrimPrefix(err.Error(), CodeOutboundInvalid+": "))
		}
		if _, err := d.itemMapping(c); err != nil {
			return err
		}
	}
	return nil
}

func fieldTargets(fields []FieldRule) []string {
	out := make([]string, len(fields))
	for i, f := range fields {
		out[i] = f.Target
	}
	return out
}

// itemRows is the element rows of one collection whose array the event carries. A nil row is an element that is not
// an object (refused).
type itemRows struct {
	c    OutboundCollection
	rows []map[string]any
}

// lookupCache reads each lookup table once per render. prefetch reads every key that the definition's lookups and every
// collection element will ask for in one query per table, so an order of 1000 lines costs one read per table instead
// of one per line, and every value of one render comes from the same version of a table (the audit's lookups then name
// one version per table, even if the table is edited while the render runs).
type lookupCache struct {
	inner  LookupResolver
	tables map[string]cachedTable
}

type cachedTable struct {
	ans   LookupAnswer
	asked map[string]bool
}

// lookupSource wraps resolver in a lookupCache when it can read lookup tables. A resolver that cannot is returned as it
// is, and resolveLookups reports that it cannot read tables.
func lookupSource(resolver any) (any, *lookupCache) {
	lr, ok := resolver.(LookupResolver)
	if !ok {
		return resolver, nil
	}
	c := &lookupCache{inner: lr, tables: map[string]cachedTable{}}
	return c, c
}

func (c *lookupCache) prefetch(ctx context.Context, keys map[string][]string) error {
	for _, table := range slices.Sorted(maps.Keys(keys)) {
		ans, err := c.inner.LookupValues(ctx, table, keys[table])
		if err != nil {
			return err
		}
		asked := make(map[string]bool, len(keys[table]))
		for _, k := range keys[table] {
			asked[k] = true
		}
		c.tables[table] = cachedTable{ans: ans, asked: asked}
	}
	return nil
}

// LookupValues answers from the prefetched read; a key the prefetch did not ask for is read from the table.
func (c *lookupCache) LookupValues(ctx context.Context, table string, keys []string) (LookupAnswer, error) {
	if t, ok := c.tables[table]; ok && !slices.ContainsFunc(keys, func(k string) bool { return !t.asked[k] }) {
		return t.ans, nil
	}
	return c.inner.LookupValues(ctx, table, keys)
}

// lookupKeys lists, per table, every key the definition's lookups (on the event) and the collections' lookups (on
// each element) will read. A key that cannot be built (a missing event value) is not read.
func (d OutboundDefinition) lookupKeys(event map[string]any, items []itemRows) map[string][]string {
	keys := map[string][]string{}
	seen := map[string]bool{}
	add := func(lookups []OutboundLookup, row map[string]any) {
		for _, l := range lookups {
			k, why := l.lookupKey(row)
			if why != "" || seen[l.Table+"\x00"+k] {
				continue
			}
			seen[l.Table+"\x00"+k] = true
			keys[l.Table] = append(keys[l.Table], k)
		}
	}
	add(d.Lookups, event)
	for _, it := range items {
		for _, row := range it.rows {
			if row != nil {
				add(it.c.Lookups, row)
			}
		}
	}
	return keys
}

// collectionRows reads every collection's array and bounds it. It returns the element rows (the element, parent and
// index) and the refusals about the arrays themselves. An optional collection without an array has no rows and is left
// out of the body.
func (d OutboundDefinition) collectionRows(event map[string]any) ([]itemRows, []Reject) {
	var (
		all     []itemRows
		rejects []Reject
	)
	for _, c := range d.Collections {
		raw := Lookup(event, c.Path)
		items, isArray := raw.([]any)
		switch {
		case raw == nil:
			if !c.Optional {
				rejects = append(rejects, Reject{Field: c.Name, Code: "missing_array", Detail: "the event has no array at " + c.Path})
			}
			continue
		case !isArray:
			rejects = append(rejects, Reject{Field: c.Name, Code: "not_an_array", Detail: "the value at " + c.Path + " is not an array"})
			continue
		case len(items) == 0 && !c.Optional:
			rejects = append(rejects, Reject{Field: c.Name, Code: "empty_array", Detail: "the array at " + c.Path + " has no elements"})
			continue
		}
		limit := c.MaxItems
		if limit == 0 {
			limit = DefaultCollectionItems
		}
		if len(items) > limit {
			rejects = append(rejects, Reject{Field: c.Name, Code: "too_many_items", Detail: fmt.Sprintf("the array at %s has %d elements, more than the %d this collection allows", c.Path, len(items), limit)})
			continue
		}
		rows := make([]map[string]any, len(items))
		for i, it := range items {
			obj, ok := it.(map[string]any)
			if !ok {
				rejects = append(rejects, Reject{Field: fmt.Sprintf("%s[%d]", c.Name, i), Code: "not_an_object", Detail: "the element is not an object"})
				continue
			}
			row := make(map[string]any, len(obj)+4)
			for k, v := range obj {
				row[k] = v
			}
			row["parent"], row["index"] = event, json.Number(strconv.Itoa(i))
			rows[i] = row
		}
		all = append(all, itemRows{c: c, rows: rows})
	}
	return all, rejects
}

// resolveItems resolves each element's xrefs (one query per xref of a collection, for all its elements) and lookups
// (through tables, the render's lookupCache), and sets them on the rows as xref and lookup. It returns everything
// unresolved (at most maxUnresolvedPerList per collection, then a count) and the lookup versions read.
func (d OutboundDefinition) resolveItems(ctx context.Context, resolver LegacyKeyResolver, tables any, sourceSystem string, items []itemRows) ([]UnresolvedRef, []LookupUse, error) {
	var (
		unresolved []UnresolvedRef
		used       []LookupUse
	)
	usedSeen := map[LookupUse]bool{}
	for _, it := range items {
		c, rows := it.c, it.rows
		keys := make([]map[string]any, len(rows))
		for i := range keys {
			keys[i] = map[string]any{}
		}
		over := 0
		note := func(r UnresolvedRef) {
			if over++; over <= maxUnresolvedPerList {
				unresolved = append(unresolved, r)
			}
		}
		for _, x := range c.Xrefs {
			var want []string
			wantSeen := map[string]bool{}
			for i, row := range rows {
				if row == nil {
					continue
				}
				id, ok := idAt(row, x.IDPath)
				if !ok {
					if !x.Optional {
						note(UnresolvedRef{Name: itemName(c, i, x.Name), Entity: x.Entity, Reason: "the element has no id at " + x.IDPath})
					}
					continue
				}
				if !wantSeen[id] {
					wantSeen[id], want = true, append(want, id)
				}
			}
			found := map[string]string{}
			if len(want) > 0 {
				var err error
				if found, err = resolver.LegacyKeys(ctx, sourceSystem, x.Entity, want); err != nil {
					return nil, nil, err
				}
			}
			for i, row := range rows {
				if row == nil {
					continue
				}
				id, ok := idAt(row, x.IDPath)
				if !ok {
					continue
				}
				if key, ok := found[id]; ok {
					keys[i][x.Name] = key
				} else if !x.Optional {
					note(UnresolvedRef{Name: itemName(c, i, x.Name), Entity: x.Entity, ID: id, Reason: "no legacy key is recorded for this record yet"})
				}
			}
		}
		sub := OutboundDefinition{Lookups: c.Lookups}
		for i, row := range rows {
			if row == nil {
				continue
			}
			vals, miss, u, err := sub.resolveLookups(ctx, tables, row)
			if err != nil {
				return nil, nil, err
			}
			for _, m := range miss {
				m.Name = itemName(c, i, m.Name)
				note(m)
			}
			for _, use := range u {
				use.Name = c.Name + "." + use.Name
				if !usedSeen[use] {
					usedSeen[use] = true
					used = append(used, use)
				}
			}
			row["xref"], row["lookup"] = keys[i], vals
		}
		if over > maxUnresolvedPerList {
			unresolved = append(unresolved, UnresolvedRef{Name: c.Name, Entity: "collection", Reason: fmt.Sprintf("%d more unresolved references of this collection are not listed", over-maxUnresolvedPerList)})
		}
	}
	return unresolved, used, nil
}

// itemSubjectRefs lists the subject of every element of the collections that name one, in collection and element order.
// An element without a Passage id (UUID) at the subject path is unresolved (at most maxUnresolvedPerList per
// collection, then a count): the push would create a line that xref:record could not tie a key to.
func itemSubjectRefs(items []itemRows) ([]OutboundRef, []UnresolvedRef) {
	var (
		refs []OutboundRef
		bad  []UnresolvedRef
	)
	for _, it := range items {
		if it.c.Subject == nil {
			continue
		}
		over := 0
		for i, row := range it.rows {
			if row == nil {
				continue
			}
			id, ok := idAt(row, it.c.Subject.IDPath)
			reason := ""
			switch {
			case !ok:
				reason = "the element has no id at " + it.c.Subject.IDPath
			case !ids.Valid(id):
				reason = "the id at " + it.c.Subject.IDPath + " is not a Passage id (UUID)"
			}
			if reason != "" {
				if over++; over <= maxUnresolvedPerList {
					bad = append(bad, UnresolvedRef{Name: itemName(it.c, i, "subject"), Entity: it.c.Subject.Entity, ID: id, Reason: reason})
				}
				continue
			}
			idx := i
			refs = append(refs, OutboundRef{Entity: it.c.Subject.Entity, ID: id, Collection: it.c.Name, Index: &idx})
		}
		if over > maxUnresolvedPerList {
			bad = append(bad, UnresolvedRef{Name: it.c.Name, Entity: "collection", Reason: fmt.Sprintf("%d more element subjects of this collection are not listed", over-maxUnresolvedPerList)})
		}
	}
	return refs, bad
}

func itemName(c OutboundCollection, i int, name string) string {
	return fmt.Sprintf("%s[%d].%s", c.Name, i, name)
}

// mapCollections maps the resolved element rows and places each array into body.
func (d OutboundDefinition) mapCollections(prepared []itemRows, body map[string]any) ([]Warning, []Reject, error) {
	var warnings []Warning
	var rejects []Reject
	for _, p := range prepared {
		m, err := d.itemMapping(p.c)
		if err != nil {
			return nil, nil, err
		}
		list := make([]any, 0, len(p.rows))
		for i, row := range p.rows {
			if row == nil {
				continue
			}
			out, ws, rs := m.ApplyWithWarnings(row)
			prefix := fmt.Sprintf("%s[%d].", p.c.Name, i)
			for _, w := range ws {
				w.Field = prefix + w.Field
				warnings = append(warnings, w)
			}
			for _, r := range rs {
				r.Field = prefix + r.Field
				rejects = append(rejects, r)
			}
			if len(rs) > 0 {
				continue
			}
			obj, err := shapeFields(p.c.Fields, out, d.DecimalsAsNumbers)
			if err != nil {
				return nil, nil, err
			}
			list = append(list, obj)
		}
		if err := place(body, p.c.Name, list); err != nil {
			return nil, nil, err
		}
	}
	return warnings, rejects, nil
}
