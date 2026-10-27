package domain

import (
	"slices"
	"time"

	"github.com/shopspring/decimal"
)

// CodeABCThresholds is returned when the A/B/C percentages do not sum to 100.
const (
	CodeABCThresholds = "INV-ABC-001"
	CodeABCNotFound   = "INV-ABC-002" // unknown ABC run (service code, not in the 3.16.18 catalog)
)

// AbcCriterionValue ranks by on-hand posted value, the only criterion built
// (REVENUE, MARGIN and CARRYING_COST need sales and costing data).
const AbcCriterionValue = "VALUE"

// ABC classes. N means not classified (zero activity with zero class setting N).
const (
	ABCA = "A"
	ABCB = "B"
	ABCC = "C"
	ABCN = "N"
)

// AbcThresholds are the cumulative-percentage boundaries of FR-16.19 (default 80/15/5).
type AbcThresholds struct {
	A, B, C decimal.Decimal
}

// DefaultAbcThresholds are the AX/SAP-style defaults.
func DefaultAbcThresholds() AbcThresholds {
	return AbcThresholds{A: decimal.NewFromInt(80), B: decimal.NewFromInt(15), C: decimal.NewFromInt(5)}
}

// Validate checks each percentage is in range and the three sum to 100.
func (t AbcThresholds) Validate() error {
	hundred := decimal.NewFromInt(100)
	for _, p := range []decimal.Decimal{t.A, t.B, t.C} {
		if p.IsNegative() || p.GreaterThan(hundred) {
			return fail(CodeABCThresholds, "ABC thresholds must be between 0 and 100")
		}
	}
	sum := t.A.Add(t.B).Add(t.C)
	if !sum.Equal(hundred) {
		return fail(CodeABCThresholds, "ABC thresholds must sum to 100 (got %s)", sum)
	}
	return nil
}

// AbcItem is one product at one site with the value of the criterion (VALUE:
// on-hand posted value).
type AbcItem struct {
	ProductID string
	Site      string
	Value     decimal.Decimal
}

// AbcRank is an item after classification. CumPct is the cumulative share of the
// ranked criterion up to and including the item (percent).
type AbcRank struct {
	AbcItem
	Class  string
	CumPct decimal.Decimal
}

// AbcRun is one immutable classification run of a legal entity.
type AbcRun struct {
	ID            string
	LegalEntityID string
	Criterion     string
	Thresholds    AbcThresholds
	ZeroClass     string
	CreatedAt     time.Time
	Items         []AbcRank
}

// ClassCounts counts the run's items per class.
func (r AbcRun) ClassCounts() map[string]int {
	out := map[string]int{ABCA: 0, ABCB: 0, ABCC: 0, ABCN: 0}
	for _, it := range r.Items {
		out[it.Class]++
	}
	return out
}

// ClassifyABC ranks items by value, highest first (ties by product then site),
// and assigns classes by cumulative percentage (BR-16.16). An item whose
// cumulative share before it is at or below a boundary falls in the higher
// class. Items with a value of zero or less take zeroClass (ABCC by default or
// ABCN when the legal entity sets abc_zero_class=N).
func ClassifyABC(items []AbcItem, t AbcThresholds, zeroClass string) []AbcRank {
	ranked := slices.Clone(items)
	slices.SortStableFunc(ranked, func(x, y AbcItem) int {
		if c := y.Value.Cmp(x.Value); c != 0 {
			return c
		}
		if x.ProductID != y.ProductID {
			if x.ProductID < y.ProductID {
				return -1
			}
			return 1
		}
		if x.Site < y.Site {
			return -1
		}
		if x.Site > y.Site {
			return 1
		}
		return 0
	})
	total := decimal.Zero
	for _, it := range ranked {
		if it.Value.IsPositive() {
			total = total.Add(it.Value)
		}
	}
	hundred := decimal.NewFromInt(100)
	boundaryA := t.A.Mul(total)
	boundaryAB := t.A.Add(t.B).Mul(total)
	out := make([]AbcRank, 0, len(ranked))
	before := decimal.Zero
	for _, it := range ranked {
		if !it.Value.IsPositive() {
			out = append(out, AbcRank{AbcItem: it, Class: zeroClass, CumPct: hundred})
			continue
		}
		class := ABCC
		scaled := before.Mul(hundred)
		switch {
		case scaled.LessThanOrEqual(boundaryA):
			class = ABCA
		case scaled.LessThanOrEqual(boundaryAB):
			class = ABCB
		}
		before = before.Add(it.Value)
		cum := before.Mul(hundred).Div(total)
		out = append(out, AbcRank{AbcItem: it, Class: class, CumPct: cum})
	}
	return out
}
