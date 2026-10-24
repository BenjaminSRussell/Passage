package domain

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// VariantHash is the unique lookup key for a dimension combination (BR-8.2).
func VariantHash(values map[string]string) string {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte('|')
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(values[k])
	}
	sum := sha1.Sum([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// VariantNumber applies the tenant/master nomenclature (FR-8.2, BR-8.11).
func VariantNumber(master, pattern string, order []string, values map[string]string) string {
	if pattern == "" {
		pattern = DefaultVariantPattern
	}
	repl := map[string]string{"{master}": master}
	for _, dim := range []string{"size", "color", "style", "config"} {
		repl["{"+dim+"}"] = values[dim]
	}
	for _, dim := range order {
		if _, ok := repl["{"+dim+"}"]; !ok {
			repl["{"+dim+"}"] = values[dim]
		}
	}
	out := pattern
	for token, val := range repl {
		out = strings.ReplaceAll(out, token, val)
	}
	parts := strings.Split(out, "-")
	kept := parts[:0]
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	out = strings.Join(kept, "-")
	out = strings.ToUpper(out)
	if len(out) <= MaxProductNumber && productNumberRE.MatchString(out) {
		return out
	}
	hash := VariantHash(values)
	if len(hash) > 8 {
		hash = hash[:8]
	}
	base := out
	if len(base) > 32 {
		base = base[:32]
	}
	base = strings.TrimRight(base, ".-_")
	if base == "" {
		base = master
	}
	n := base + hash
	if len(n) > MaxProductNumber {
		n = n[:MaxProductNumber]
	}
	return strings.ToUpper(n)
}

// Explode generates the Cartesian product (or an explicit list) and skips existing hashes (BR-8.3).
func Explode(existing []Variant, req ExplosionRequest) (ExplosionResult, error) {
	max := req.MaxVariants
	if max <= 0 {
		max = MaxVariantsDefault
	}
	order := make([]string, 0, len(req.Dimensions))
	allowed := map[string]map[string]bool{}
	for _, d := range req.Dimensions {
		order = append(order, d.Code)
		allowed[d.Code] = map[string]bool{}
		for _, v := range d.Values {
			allowed[d.Code][v] = true
		}
	}
	combos, err := combinations(req, order, allowed)
	if err != nil {
		return ExplosionResult{}, err
	}
	if len(combos) > max {
		return ExplosionResult{}, errf(CodeVarCap, fmt.Sprintf("explosion would create %d variants; limit is %d", len(combos), max))
	}
	seen := map[string]bool{}
	for _, v := range existing {
		h := v.VariantHash
		if h == "" {
			h = VariantHash(v.DimensionValues)
		}
		seen[h] = true
	}
	out := ExplosionResult{Created: []Variant{}, Skipped: []Variant{}}
	for _, values := range combos {
		v := Variant{DimensionValues: values, VariantHash: VariantHash(values)}
		v.ProductNumber = VariantNumber(req.MasterNumber, req.Pattern, order, values)
		if seen[v.VariantHash] {
			out.Skipped = append(out.Skipped, v)
			continue
		}
		seen[v.VariantHash] = true
		out.Created = append(out.Created, v)
	}
	return out, nil
}

// Lookup resolves a variant by dimension combination.
func Lookup(variants []Variant, values map[string]string) (Variant, bool) {
	want := VariantHash(values)
	for _, v := range variants {
		h := v.VariantHash
		if h == "" {
			h = VariantHash(v.DimensionValues)
		}
		if h == want {
			return v, true
		}
	}
	return Variant{}, false
}

func combinations(req ExplosionRequest, order []string, allowed map[string]map[string]bool) ([]map[string]string, error) {
	if len(req.Explicit) > 0 {
		out := make([]map[string]string, 0, len(req.Explicit))
		for _, row := range req.Explicit {
			if err := checkValues(row, allowed); err != nil {
				return nil, err
			}
			out = append(out, copyValues(row))
		}
		return out, nil
	}
	lists := make([][]string, 0, len(order))
	for _, dim := range order {
		vals := req.Selected[dim]
		if len(vals) == 0 {
			for _, d := range req.Dimensions {
				if d.Code == dim {
					vals = d.Values
					break
				}
			}
		}
		if err := checkList(dim, vals, allowed[dim]); err != nil {
			return nil, err
		}
		lists = append(lists, vals)
	}
	return cartesian(order, lists), nil
}

func checkValues(row map[string]string, allowed map[string]map[string]bool) error {
	for dim, val := range row {
		if allowed[dim] == nil || !allowed[dim][val] {
			return errf(CodeVarDimUnknown, val+" is not a value of dimension "+dim)
		}
	}
	return nil
}

func checkList(dim string, vals []string, allowed map[string]bool) error {
	for _, v := range vals {
		if !allowed[v] {
			return errf(CodeVarDimUnknown, v+" is not a value of dimension "+dim)
		}
	}
	return nil
}

func cartesian(order []string, lists [][]string) []map[string]string {
	if len(lists) == 0 {
		return nil
	}
	total := 1
	for _, l := range lists {
		total *= len(l)
		if total == 0 {
			return nil
		}
	}
	out := make([]map[string]string, 0, total)
	idx := make([]int, len(lists))
	for {
		row := make(map[string]string, len(order))
		for i, dim := range order {
			row[dim] = lists[i][idx[i]]
		}
		out = append(out, row)
		i := len(idx) - 1
		for i >= 0 {
			idx[i]++
			if idx[i] < len(lists[i]) {
				break
			}
			idx[i] = 0
			i--
		}
		if i < 0 {
			break
		}
	}
	return out
}

func copyValues(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
