package domain

import (
	"sort"

	"github.com/shopspring/decimal"
)

// CodeMergeRefused is the PMF-0006 refusal: merged batches differ in an attribute
// that is not marked merge-override, or a source is not mergeable (FR-24.16).
const CodeMergeRefused = "PMF-0006"

// Batch is one inventory batch as the merge sees it. Values are keyed by attribute
// name; the shelf-life dates are the attributes "expiry" and "best-before".
type Batch struct {
	ID          string
	ItemID      string
	WarehouseID string
	CWUnit      string
	Disposition string
	Qty         decimal.Decimal
	UnitCost    decimal.Decimal
	Values      map[string]Value
}

// MergeResult is the target batch that a merge would produce (FR-24.16). Parents
// are the source batch ids for genealogy.
type MergeResult struct {
	Qty         decimal.Decimal
	UnitCost    decimal.Decimal
	Disposition string
	Values      map[string]Value
	Parents     []string
}

// Merge computes the target of a merge of two or more batches of the same item,
// warehouse and catch-weight unit (FR-24.16, BR-24.11). Numeric attributes are
// averaged by quantity, date attributes take the earliest date, and text
// attributes must agree unless listed in override (then the largest source wins).
// The sources must share one disposition; that disposition is kept.
func Merge(sources []Batch, override map[string]bool) (MergeResult, error) {
	if len(sources) < 2 {
		return MergeResult{}, errorf(CodeInvalid, "merge needs at least two batches")
	}
	first := sources[0]
	seen := map[string]bool{}
	qty, costSum := decimal.Zero, decimal.Zero
	for _, b := range sources {
		if seen[b.ID] {
			return MergeResult{}, errorf(CodeInvalid, "batch %s listed twice", b.ID)
		}
		seen[b.ID] = true
		if !b.Qty.IsPositive() {
			return MergeResult{}, errorf(CodeInvalid, "batch %s has no quantity", b.ID)
		}
		if b.ItemID != first.ItemID || b.WarehouseID != first.WarehouseID || b.CWUnit != first.CWUnit {
			return MergeResult{}, errorf(CodeMergeRefused, "merge refused: batch %s differs from %s in item, warehouse or catch-weight unit", b.ID, first.ID)
		}
		if b.Disposition != first.Disposition {
			return MergeResult{}, errorf(CodeMergeRefused, "merge refused: disposition differs (%s vs %s)", first.Disposition, b.Disposition)
		}
		qty = qty.Add(b.Qty)
		costSum = costSum.Add(b.UnitCost.Mul(b.Qty))
	}
	res := MergeResult{Qty: qty, UnitCost: costSum.DivRound(qty, 24).Round(QtyPrecision), Disposition: first.Disposition}

	names := attributeNames(sources)
	res.Values = make(map[string]Value, len(names))
	for _, name := range names {
		v, err := mergeAttribute(name, sources, override[name])
		if err != nil {
			return MergeResult{}, err
		}
		if !v.Empty() {
			res.Values[name] = v
		}
	}
	for _, b := range sources {
		res.Parents = append(res.Parents, b.ID)
	}
	return res, nil
}

// attributeNames returns the union of attribute names, sorted.
func attributeNames(sources []Batch) []string {
	set := map[string]bool{}
	for _, b := range sources {
		for n := range b.Values {
			set[n] = true
		}
	}
	out := make([]string, 0, len(set))
	for n := range set {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// mergeAttribute derives one attribute of the target from the sources. An
// attribute must have one kind (number, date or text) across the sources.
func mergeAttribute(name string, sources []Batch, override bool) (Value, error) {
	var nums []Sender
	var texts []Batch
	var dates []Sender
	for _, b := range sources {
		v, ok := b.Values[name]
		if !ok || v.Empty() {
			continue
		}
		switch {
		case v.Num != nil:
			nums = append(nums, Sender{BatchID: b.ID, Value: v, Qty: b.Qty})
		case v.Date != nil:
			dates = append(dates, Sender{BatchID: b.ID, Value: v, Qty: b.Qty})
		default:
			texts = append(texts, b)
		}
	}
	kinds := 0
	for _, n := range []int{len(nums), len(dates), len(texts)} {
		if n > 0 {
			kinds++
		}
	}
	if kinds > 1 {
		return Value{}, errorf(CodeMergeRefused, "merge refused: attribute %s mixes numbers, dates and text across the batches", name)
	}
	switch {
	case len(nums) > 0:
		d, err := Derive(Rule{Attribute: name, Aggregation: AggWeightedAverage, Decimals: DefaultDecimals}, nums)
		return d.Value, err
	case len(dates) > 0:
		d, err := Derive(Rule{Attribute: name, Aggregation: AggEarliestDate}, dates)
		return d.Value, err
	case len(texts) > 0:
		first := *texts[0].Values[name].Text
		largest := texts[0]
		for _, b := range texts[1:] {
			if *b.Values[name].Text != first {
				if !override {
					return Value{}, errorf(CodeMergeRefused, "merge refused: attribute %s differs (%s vs %s)", name, first, *b.Values[name].Text)
				}
			}
			if b.Qty.GreaterThan(largest.Qty) {
				largest = b
			}
		}
		if override {
			t := *largest.Values[name].Text
			return Value{Text: &t}, nil
		}
		return Value{Text: &first}, nil
	}
	return Value{}, nil
}
