// Package domain holds the pure receivables rules: cash-application matching
// (BR-30.1), settlement validation (BR-30.4) and aging buckets (FR-30.6).
// Nothing here performs I/O.
package domain

import (
	"sort"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// Decision thresholds from BR-30.1 and config MATCH_AUTO_THRESHOLD / MATCH_MARGIN.
const (
	AutoThreshold    = 85
	AutoMargin       = 20
	SuggestThreshold = 60
	// MaxSubsetSize bounds the number of open items a single payment may settle (BR-30.1).
	MaxSubsetSize = 5
	// MaxSubsetPool bounds the candidates considered for subset-sum search, keeping
	// the search under the 50 ms per-payment budget (§5.30.19).
	MaxSubsetPool = 12
	// MaxRefLength is the shortest document number that R1 will trust as a reference.
	// Shorter numbers appear by chance in remittance text.
	MaxRefLength = 4
)

// Rule weights for R1, R2, R3 and R6 (BR-30.1). The payer-identity rules R4, R5
// and R7 live in identity.go.
const (
	weightR1       = 60
	weightR2Single = 30
	weightR2Subset = 25
	weightR3       = 28
	weightR6       = 5
	r6WindowDays   = 7
)

// Item is the part of an open item the matcher reads. Amounts are positive.
type Item struct {
	ID             string
	CustomerID     string
	Currency       string
	DocumentNumber string
	DueDate        time.Time
	OpenAmount     decimal.Decimal
	DiscountDate   *time.Time
	DiscountAmount decimal.Decimal
}

// RemittanceLine is one reference from the payer's remittance detail.
type RemittanceLine struct {
	Reference string          `json:"reference"`
	Amount    decimal.Decimal `json:"amount"`
}

// Payment is the part of a payment the matcher reads.
type Payment struct {
	Amount     decimal.Decimal
	ValueDate  time.Time
	Remittance []RemittanceLine
	// PayerName is the payer name as received (R5).
	PayerName string
	// ShortPayReason explains a short payment; R7 needs it (empty means none).
	ShortPayReason string
	// PayerCustomers holds the customers already bound to the payer account hash (R4).
	PayerCustomers map[string]bool
	// LearnedNames holds payer names previously applied per customer (R5).
	LearnedNames map[string][]string

	nameScores map[string]float64
}

// Candidate is one scored set of open items that could settle a payment.
type Candidate struct {
	ItemIDs []string `json:"openItemIds"`
	Score   int      `json:"score"`
	Rules   []string `json:"rules"`
}

// Decision is what the matcher recommends for a payment.
type Decision string

const (
	DecisionAutoApply Decision = "AUTO_APPLY"
	DecisionSuggested Decision = "SUGGESTED"
	DecisionOnAccount Decision = "ON_ACCOUNT"
)

// Result is the matcher output: a decision and the top three candidates.
type Result struct {
	Decision Decision    `json:"decision"`
	Top      []Candidate `json:"candidates"`
}

// Match scores candidate sets for a payment and decides (BR-30.1). Singles are
// scored over all open items; multi-item sets are searched only among the first
// MaxSubsetPool items, those the remittance references first and then by due
// date, so the search stays bounded and an unidentified payer's multi-invoice
// payment still finds its referenced items among hundreds of open ones.
func Match(p Payment, open []Item) Result {
	p.nameScores = payerNameScores(p)
	pool := append([]Item(nil), open...)
	sort.SliceStable(pool, func(i, j int) bool {
		if !pool[i].DueDate.Equal(pool[j].DueDate) {
			return pool[i].DueDate.Before(pool[j].DueDate)
		}
		return pool[i].ID < pool[j].ID
	})

	var sets []Candidate
	subsetPool := make([]Item, 0, len(pool))
	for _, it := range pool {
		if c, ok := scoreSingle(p, it); ok {
			sets = append(sets, c)
		}
		if referenced(p, it.DocumentNumber) {
			subsetPool = append(subsetPool, it)
		}
	}
	for _, it := range pool {
		if !referenced(p, it.DocumentNumber) {
			subsetPool = append(subsetPool, it)
		}
	}
	limit := len(subsetPool)
	if limit > MaxSubsetPool {
		limit = MaxSubsetPool
	}
	exactSubsets(subsetPool[:limit], p.Amount, MaxSubsetSize, func(picked []Item) {
		// A payment settles one customer's items (apply refuses a mix), so a set
		// spanning customers is never a candidate.
		if oneCustomer(picked) {
			sets = append(sets, scoreSubset(p, picked))
		}
	})

	sort.SliceStable(sets, func(i, j int) bool {
		if sets[i].Score != sets[j].Score {
			return sets[i].Score > sets[j].Score
		}
		if len(sets[i].ItemIDs) != len(sets[j].ItemIDs) {
			return len(sets[i].ItemIDs) < len(sets[j].ItemIDs)
		}
		return strings.Join(sets[i].ItemIDs, ",") < strings.Join(sets[j].ItemIDs, ",")
	})

	res := Result{Decision: DecisionOnAccount, Top: []Candidate{}}
	for i := 0; i < len(sets) && i < 3; i++ {
		res.Top = append(res.Top, sets[i])
	}
	if len(sets) == 0 {
		return res
	}
	// A set made only of the top set's items is part of the same settlement,
	// not a rival explanation of the payment, so it neither counts against
	// "exactly one set >= 85" nor runs up. Without this, learning a payer
	// (R4, R5) lifts each referenced invoice of a multi-invoice payment to 85 on
	// its own and demotes the exact multi-invoice set to SUGGESTED.
	top := sets[0]
	atThreshold, runnerUp := 0, 0
	for i, c := range sets {
		if i > 0 && within(c.ItemIDs, top.ItemIDs) {
			continue
		}
		if c.Score >= AutoThreshold {
			atThreshold++
		}
		if i > 0 && c.Score > runnerUp {
			runnerUp = c.Score
		}
	}
	switch {
	case top.Score >= AutoThreshold && atThreshold == 1 && runnerUp <= top.Score-AutoMargin:
		res.Decision = DecisionAutoApply
	case top.Score >= SuggestThreshold:
		res.Decision = DecisionSuggested
	}
	return res
}

// scoreSingle scores one open item as the whole settlement. It is a candidate
// only when a strong signal (R1 reference, R2 exact amount, R3 discount) applies.
func scoreSingle(p Payment, it Item) (Candidate, bool) {
	c := Candidate{ItemIDs: []string{it.ID}, Rules: []string{}}
	strong := false
	if referenced(p, it.DocumentNumber) {
		c.Score += weightR1
		c.Rules = append(c.Rules, "R1")
		strong = true
	}
	if it.OpenAmount.Equal(p.Amount) {
		c.Score += weightR2Single
		c.Rules = append(c.Rules, "R2")
		strong = true
	}
	if discountApplies(p, it) {
		c.Score += weightR3
		c.Rules = append(c.Rules, "R3")
		strong = true
	}
	if payerKnown(p, it) {
		c.Score += weightR4
		c.Rules = append(c.Rules, "R4")
	}
	if payerNameMatches(p, it) {
		c.Score += weightR5
		c.Rules = append(c.Rules, "R5")
	}
	if !hasRule(c.Rules, "R3") && shortPaid(p, it) {
		c.Score += weightR7
		c.Rules = append(c.Rules, "R7")
		strong = true
	}
	if withinWindow(p.ValueDate, it.DueDate) {
		c.Score += weightR6
		c.Rules = append(c.Rules, "R6")
	}
	return c, strong
}

// scoreSubset scores an exact multi-item settlement (R2 subset).
func scoreSubset(p Payment, picked []Item) Candidate {
	c := Candidate{Rules: []string{"R2-SUBSET"}, Score: weightR2Subset}
	earliest := picked[0].DueDate
	anyReferenced := false
	for _, it := range picked {
		c.ItemIDs = append(c.ItemIDs, it.ID)
		if referenced(p, it.DocumentNumber) {
			anyReferenced = true
		}
		if it.DueDate.Before(earliest) {
			earliest = it.DueDate
		}
	}
	if anyReferenced {
		c.Score += weightR1
		c.Rules = append(c.Rules, "R1")
	}
	// The items share one customer, so R4 and R5 hold for all of them or none.
	if payerKnown(p, picked[0]) {
		c.Score += weightR4
		c.Rules = append(c.Rules, "R4")
	}
	if payerNameMatches(p, picked[0]) {
		c.Score += weightR5
		c.Rules = append(c.Rules, "R5")
	}
	if withinWindow(p.ValueDate, earliest) {
		c.Score += weightR6
		c.Rules = append(c.Rules, "R6")
	}
	return c
}

// within reports whether every id of part is in whole.
func within(part, whole []string) bool {
	for _, id := range part {
		found := false
		for _, w := range whole {
			if w == id {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// oneCustomer reports whether every item belongs to the same customer.
func oneCustomer(items []Item) bool {
	for _, it := range items[1:] {
		if it.CustomerID != items[0].CustomerID {
			return false
		}
	}
	return true
}

func hasRule(rules []string, code string) bool {
	for _, r := range rules {
		if r == code {
			return true
		}
	}
	return false
}

// exactSubsets calls visit for every subset of two to max items of pool whose
// open amounts sum exactly to target. Amounts are positive, so overshooting
// branches are pruned.
func exactSubsets(pool []Item, target decimal.Decimal, max int, visit func([]Item)) {
	var picked []Item
	var rec func(start int, sum decimal.Decimal)
	rec = func(start int, sum decimal.Decimal) {
		if len(picked) >= 2 && sum.Equal(target) {
			visit(append([]Item(nil), picked...))
		}
		if len(picked) == max {
			return
		}
		for i := start; i < len(pool); i++ {
			next := sum.Add(pool[i].OpenAmount)
			if next.GreaterThan(target) {
				continue
			}
			picked = append(picked, pool[i])
			rec(i+1, next)
			picked = picked[:len(picked)-1]
		}
	}
	rec(0, decimal.Zero)
}

// referenced reports whether the remittance names docNumber as a whole token
// (R1). A plain substring test would let INV-1001 match a remittance for
// INV-10012, which is a false match; BR-30.1 allows none.
func referenced(p Payment, docNumber string) bool {
	doc := strings.ToUpper(strings.TrimSpace(docNumber))
	if len(doc) < MaxRefLength {
		return false
	}
	for _, line := range p.Remittance {
		ref := strings.ToUpper(line.Reference)
		for from := 0; ; {
			i := strings.Index(ref[from:], doc)
			if i < 0 {
				break
			}
			start := from + i
			end := start + len(doc)
			if tokenEdge(ref, start-1, -1) && tokenEdge(ref, end, 1) {
				return true
			}
			from = start + 1
		}
	}
	return false
}

// tokenEdge reports whether position i of s (just outside a candidate match,
// stepping in direction dir) ends the token: the string edge, or a character
// that is not a letter or digit. A joiner (- / _ .) followed by a letter or
// digit continues the token, so INV-1001 does not match INV-1001-2.
func tokenEdge(s string, i, dir int) bool {
	if i < 0 || i >= len(s) {
		return true
	}
	c := s[i]
	if isAlnum(c) {
		return false
	}
	if c == '-' || c == '/' || c == '_' || c == '.' {
		next := i + dir
		return next < 0 || next >= len(s) || !isAlnum(s[next])
	}
	return true
}

func isAlnum(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}

func discountApplies(p Payment, it Item) bool {
	if it.DiscountDate == nil || !it.DiscountAmount.IsPositive() {
		return false
	}
	if p.ValueDate.After(*it.DiscountDate) {
		return false
	}
	return p.Amount.Equal(it.OpenAmount.Sub(it.DiscountAmount))
}

func withinWindow(valueDate, due time.Time) bool {
	days := valueDate.Sub(due).Hours() / 24
	if days < 0 {
		days = -days
	}
	return days <= r6WindowDays
}
