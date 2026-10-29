package domain

import (
	"fmt"
	"sort"
	"strings"

	"github.com/shopspring/decimal"
)

// Variance classes (FR-49.5 step 3).
const (
	ClassExpected    = "EXPECTED"
	ClassExplained   = "EXPLAINED"
	ClassUnexplained = "UNEXPLAINED"
)

// RuleRND001 is the built-in rounding rule: a difference of at most 0.01 per document (BR-49.7).
const RuleRND001 = "RND-001"

// Area names the subledger a canonical tuple belongs to (TB, INV, AP, AR, FA).
type Area string

// Line is a canonical tuple from either side of a parallel run. Key joins the dimension set,
// currency and period so that the outer join matches like with like.
type Line struct {
	Area    Area
	Key     string
	Legacy  decimal.Decimal
	Passage decimal.Decimal
}

// Rule is a variance_rule: EXPECTED when |delta| <= MaxAbs for lines in Area.
type Rule struct {
	ID     string
	Area   Area // empty = any area
	MaxAbs decimal.Decimal
}

// DefaultRules is the tenant default variance rule set (`migration.variance_rules` = RND-001).
func DefaultRules() []Rule {
	return []Rule{{ID: RuleRND001, MaxAbs: decimal.RequireFromString("0.01")}}
}

// Variance is one classified difference.
type Variance struct {
	Area    Area
	Key     string
	Legacy  decimal.Decimal
	Passage decimal.Decimal
	Delta   decimal.Decimal
	Class   string
	RuleID  string
	Reason  string
}

// Explanations maps a variance (area, key) to the reason an operator attached (FR-49.5
// EXPLAINED). The area is part of the key: a reason given for a trial-balance line must not
// explain an AR line that happens to share the same key text.
type Explanations map[string]string

func explanationKey(area Area, key string) string { return string(area) + "|" + key }

// Explain attaches a reason to the variance at (area, key). An empty reason is rejected.
func (e Explanations) Explain(area Area, key, reason string) error {
	if strings.TrimSpace(reason) == "" {
		return fmt.Errorf("%w: an explanation needs a reason", ErrInvalid)
	}
	e[explanationKey(area, key)] = strings.TrimSpace(reason)
	return nil
}

// Diff outer-joins the legacy and Passage tuples on (area, key), skips equal pairs and
// classifies each remaining difference. Missing on one side counts as zero.
func Diff(legacy, passage []Line, rules []Rule, ex Explanations) []Variance {
	type pair struct{ l, p decimal.Decimal }
	joined := make(map[string]pair)
	areas := make(map[string]Area)
	for _, l := range legacy {
		k := explanationKey(l.Area, l.Key)
		p := joined[k]
		p.l = p.l.Add(l.Legacy)
		joined[k] = p
		areas[k] = l.Area
	}
	for _, l := range passage {
		k := explanationKey(l.Area, l.Key)
		p := joined[k]
		p.p = p.p.Add(l.Passage)
		joined[k] = p
		areas[k] = l.Area
	}
	keys := make([]string, 0, len(joined))
	for k := range joined {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []Variance
	for _, k := range keys {
		p := joined[k]
		if p.l.Equal(p.p) {
			continue
		}
		area := areas[k]
		key := strings.SplitN(k, "|", 2)[1]
		out = append(out, classify(area, key, p.l, p.p, rules, ex))
	}
	return out
}

func classify(area Area, key string, legacy, passage decimal.Decimal, rules []Rule, ex Explanations) Variance {
	v := Variance{Area: area, Key: key, Legacy: legacy, Passage: passage, Delta: passage.Sub(legacy)}
	for _, r := range rules {
		if (r.Area == "" || r.Area == area) && v.Delta.Abs().LessThanOrEqual(r.MaxAbs) {
			v.Class, v.RuleID = ClassExpected, r.ID
			return v
		}
	}
	if reason, ok := ex[explanationKey(area, key)]; ok {
		v.Class, v.Reason = ClassExplained, reason
		return v
	}
	v.Class = ClassUnexplained
	return v
}

// Summary is the aggregate a parallel run stores (parallel_run.unexplained_* and explained_amount).
type Summary struct {
	UnexplainedCount  int
	UnexplainedAmount decimal.Decimal
	ExplainedAmount   decimal.Decimal
	ByRule            map[string]int
}

// Summarize aggregates variances by class and rule (FR-49.5 step 4-5).
func Summarize(vs []Variance) Summary {
	s := Summary{ByRule: map[string]int{}, UnexplainedAmount: decimal.Zero, ExplainedAmount: decimal.Zero}
	for _, v := range vs {
		switch v.Class {
		case ClassUnexplained:
			s.UnexplainedCount++
			s.UnexplainedAmount = s.UnexplainedAmount.Add(v.Delta.Abs())
		case ClassExplained:
			s.ExplainedAmount = s.ExplainedAmount.Add(v.Delta.Abs())
		}
		if v.RuleID != "" {
			s.ByRule[v.RuleID]++
		}
	}
	return s
}

// SignOffGate is the FR-49.5 exit criterion: zero unexplained variances and explained amount no
// greater than maxPct percent of period revenue. Failure is MIG-1509.
func SignOffGate(s Summary, revenue decimal.Decimal, maxPct decimal.Decimal) error {
	if s.UnexplainedCount > 0 {
		return fmt.Errorf("%w: MIG-1509 parallel run cannot be signed off: %d unexplained variances (%s)", ErrInvalid, s.UnexplainedCount, s.UnexplainedAmount.StringFixed(2))
	}
	limit := revenue.Mul(maxPct).Div(decimal.NewFromInt(100))
	if s.ExplainedAmount.GreaterThan(limit) {
		return fmt.Errorf("%w: explained amount %s exceeds %s%% of revenue", ErrInvalid, s.ExplainedAmount.StringFixed(2), maxPct.StringFixed(2))
	}
	return nil
}
