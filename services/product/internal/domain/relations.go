package domain

import "strings"

// SeededRelationTypes are FR-8.13 defaults.
func SeededRelationTypes() []RelationType {
	return []RelationType{
		{Code: "accessory", Name: "Accessory", Symmetric: false},
		{Code: "cross_sell", Name: "Cross-sell", Symmetric: false},
		{Code: "up_sell", Name: "Up-sell", Symmetric: false},
		{Code: "substitute", Name: "Substitute", Symmetric: true},
		{Code: "spare_part", Name: "Spare part", Symmetric: false},
		{Code: "consumable", Name: "Consumable", Symmetric: false},
		{Code: "same_family", Name: "Same family", Symmetric: true},
	}
}

func typeByCode(types []RelationType, code string) (RelationType, bool) {
	for _, t := range types {
		if t.Code == code {
			return t, true
		}
	}
	return RelationType{}, false
}

func relationKey(r Relation) string {
	return r.TypeCode + "|" + r.FromProductID + "|" + r.ToProductID
}

// AddRelation validates BR-8.14 and, for symmetric types, inserts the inverse.
func AddRelation(existing []Relation, in Relation, types []RelationType, toObsolete bool) ([]Relation, error) {
	if in.FromProductID == "" || in.ToProductID == "" {
		return nil, errf(CodeRelSelf, "both products are required")
	}
	if in.FromProductID == in.ToProductID {
		return nil, errf(CodeRelSelf, "a product cannot be related to itself")
	}
	if toObsolete {
		return nil, errf(CodeRelSelf, "a relation to an obsolete-category product is rejected")
	}
	rt, ok := typeByCode(types, in.TypeCode)
	if !ok {
		return nil, errf(CodeRelSelf, "unknown relation type "+in.TypeCode)
	}
	count := 0
	seen := map[string]bool{}
	for _, r := range existing {
		seen[relationKey(r)] = true
		if r.FromProductID == in.FromProductID {
			count++
		}
	}
	if count >= MaxRelations {
		return nil, errf(CodeRelSelf, "maximum 200 relations per product")
	}
	if seen[relationKey(in)] {
		return existing, nil
	}
	out := append(append([]Relation{}, existing...), in)
	if rt.Symmetric {
		inv := in
		inv.FromProductID, inv.ToProductID = in.ToProductID, in.FromProductID
		if !seen[relationKey(inv)] {
			out = append(out, inv)
		}
	}
	return out, nil
}

// DeleteRelation removes a relation and its inverse when the type is symmetric.
func DeleteRelation(existing []Relation, in Relation, types []RelationType) []Relation {
	rt, _ := typeByCode(types, in.TypeCode)
	drop := map[string]bool{relationKey(in): true}
	if rt.Symmetric {
		inv := in
		inv.FromProductID, inv.ToProductID = in.ToProductID, in.FromProductID
		drop[relationKey(inv)] = true
	}
	out := make([]Relation, 0, len(existing))
	for _, r := range existing {
		if !drop[relationKey(r)] {
			out = append(out, r)
		}
	}
	return out
}

// RelationsOf returns relations of an optional type for a product.
func RelationsOf(existing []Relation, productID, typeCode string) []Relation {
	var out []Relation
	for _, r := range existing {
		if r.FromProductID != productID {
			continue
		}
		if typeCode != "" && !strings.EqualFold(r.TypeCode, typeCode) {
			continue
		}
		out = append(out, r)
	}
	return out
}
