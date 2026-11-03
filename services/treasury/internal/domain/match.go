package domain

import (
	"errors"
	"math"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// Criterion weights of the matching score (BR-36.1). They sum to 100.
const (
	WeightAmount       = 40
	WeightDate         = 20
	WeightReference    = 25
	WeightCounterparty = 15
)

// Status of a statement line after rules have run (7.36.4 BankStatementLine).
type Status string

const (
	StatusUnmatched Status = "unmatched"
	StatusSuggested Status = "suggested"
	StatusMatched   Status = "matched"
)

// Thresholds control auto-matching (FR-36.3, parameters recon.*).
type Thresholds struct {
	Auto      int // score at or above which a unique leader is auto-matched
	Suggest   int // lower bound of Suggested
	TieMargin int // points the leader must lead the runner-up by (BR-36.2)
}

// DefaultThresholds are the parameter defaults: auto 90, suggest 60, tie margin 5.
var DefaultThresholds = Thresholds{Auto: 90, Suggest: 60, TieMargin: 5}

// Valid enforces the parameter ranges: auto 60..100, suggest 1..auto-1, tie 0..20.
func (t Thresholds) Valid() error {
	switch {
	case t.Auto < 60 || t.Auto > 100:
		return errors.New("autoMatchThreshold must be 60 to 100")
	case t.Suggest < 1 || t.Suggest >= t.Auto:
		return errors.New("suggestThreshold must be from 1 up to the auto threshold minus one")
	case t.TieMargin < 0 || t.TieMargin > 20:
		return errors.New("tieMargin must be 0 to 20")
	}
	return nil
}

// Facts are the comparable features of a statement line or a Passage bank transaction.
type Facts struct {
	Amount    decimal.Decimal
	ValueDate string // YYYY-MM-DD
	Reference string // end-to-end id, check number or invoice number
	// AltReference is a second reference of a statement line, such as the
	// remittance text beside the end-to-end id; the better of the two scores.
	AltReference string
	Counterparty string
}

// Tolerance is the amount tolerance and value-date window of a rule.
type Tolerance struct {
	Amount         decimal.Decimal
	DateWindowDays int
}

// Score returns the 0-100 match score of a candidate for a line (BR-36.1).
func Score(line, cand Facts, tol Tolerance) int { return ScoreWith(line, cand, tol, DefaultWeights) }

// Weights are the criterion weights of one rule; they sum to 100.
type Weights struct {
	Amount       int `json:"amount"`
	Date         int `json:"date"`
	Reference    int `json:"reference"`
	Counterparty int `json:"counterparty"`
}

var (
	DefaultWeights   = Weights{WeightAmount, WeightDate, WeightReference, WeightCounterparty}
	ReferenceWeights = Weights{Amount: 40, Date: 10, Reference: 50}
)

type prepared struct {
	Facts
	id        string
	idx       int
	day       int
	hasDay    bool
	reference string
	alt       string
	grams     map[string]struct{}
}

func prepare(id string, f Facts) *prepared {
	p := &prepared{Facts: f, id: id, reference: normalizedReference(f.Reference), alt: normalizedReference(f.AltReference), grams: trigrams(f.Counterparty)}
	if d, err := time.Parse("2006-01-02", f.ValueDate); err == nil {
		p.day, p.hasDay = int(d.Unix()/86400), true
	}
	return p
}

func daysApart(a, b *prepared) (int, bool) {
	if !a.hasDay || !b.hasDay {
		return 0, false
	}
	return max(a.day-b.day, b.day-a.day), true
}

func inWindow(a, b *prepared, window int) bool {
	days, ok := daysApart(a, b)
	return ok && days <= window
}

// ScoreWith is Score under the weights of a rule.
func ScoreWith(line, cand Facts, tol Tolerance, w Weights) int {
	return newPair(prepare("", line), prepare("", cand), true, true).score(tol, w)
}

type pair struct {
	cand    *prepared
	diff    decimal.Decimal
	days    int
	hasDays bool
	ref     float64
	sim     float64
}

func newPair(line, cand *prepared, needRef, needSim bool) pair {
	p := pair{cand: cand, diff: line.Amount.Sub(cand.Amount).Abs()}
	p.days, p.hasDays = daysApart(line, cand)
	if needRef {
		p.ref = math.Max(referenceResult(line.reference, cand.reference), referenceResult(line.alt, cand.reference))
	}
	if needSim {
		p.sim = similarity(line.grams, cand.grams)
	}
	return p
}

func (p pair) score(tol Tolerance, w Weights) int {
	total := float64(w.Amount)*amountResult(p.diff, tol.Amount) +
		float64(w.Date)*dateResult(p.days, p.hasDays, tol.DateWindowDays) +
		float64(w.Reference)*p.ref +
		float64(w.Counterparty)*p.sim
	return int(math.Round(total))
}

// amountResult is 1 for an exact amount, 1 - diff/tolerance inside the tolerance and 0 outside it.
func amountResult(diff, tolerance decimal.Decimal) float64 {
	switch {
	case diff.IsZero():
		return 1
	case !tolerance.IsPositive() || diff.Cmp(tolerance) >= 0:
		return 0
	}
	return clip01(1 - diff.Div(tolerance).InexactFloat64())
}

// dateResult is 1 - |days|/window; with no window only the same day scores.
func dateResult(days int, ok bool, window int) float64 {
	if !ok {
		return 0
	}
	if window <= 0 {
		if days == 0 {
			return 1
		}
		return 0
	}
	return clip01(1 - float64(days)/float64(window))
}

var refStrip = regexp.MustCompile(`[^A-Z0-9]`)

// referenceResult takes normalized references: 1 for equal ones, 0.5 when one
// contains the other (at least six characters, so a short number cannot match
// by accident), else 0.
func referenceResult(na, nb string) float64 {
	switch {
	case na == "" || nb == "" || placeholderReference(na) || placeholderReference(nb):
		return 0
	case na == nb:
		return 1
	case len(na) >= 6 && strings.Contains(nb, na), len(nb) >= 6 && strings.Contains(na, nb):
		return 0.5
	}
	return 0
}

var placeholderReferences = map[string]bool{
	"NOTPROVIDED": true, "NONREF": true, "NOREF": true, "NA": true, "NONE": true, "NULL": true,
	"UNKNOWN": true, "REFERENCE": true, "EMPTY": true, "NOTAVAILABLE": true, "NOTAPPLICABLE": true,
}

func placeholderReference(normalized string) bool {
	return placeholderReferences[normalized] || strings.Count(normalized, normalized[:1]) == len(normalized)
}

var nameStrip = regexp.MustCompile(`[^a-z0-9 ]`)

// similarity is the Jaccard index of two trigram sets of normalized names.
func similarity(ta, tb map[string]struct{}) float64 {
	if len(ta) == 0 || len(tb) == 0 {
		return 0
	}
	small, large := ta, tb
	if len(small) > len(large) {
		small, large = large, small
	}
	shared := 0
	for g := range small {
		if _, ok := large[g]; ok {
			shared++
		}
	}
	return float64(shared) / float64(len(ta)+len(tb)-shared)
}

func trigrams(name string) map[string]struct{} {
	s := " " + strings.Join(strings.Fields(nameStrip.ReplaceAllString(strings.ToLower(name), " ")), " ") + " "
	out := map[string]struct{}{}
	if len(s) < 3 || strings.TrimSpace(s) == "" {
		return out
	}
	for i := 0; i+3 <= len(s); i++ {
		out[s[i:i+3]] = struct{}{}
	}
	return out
}

func clip01(x float64) float64 { return math.Max(0, math.Min(1, x)) }

// Candidate is a scored bank transaction for one statement line.
type Candidate struct {
	ID    string
	Score int
}

// Decision is the outcome of ranking the candidates of one line.
type Decision struct {
	Status Status
	Best   *Candidate  // nil when unmatched
	Ranked []Candidate // top three, best first, for suggestions
	Rule   string      // name of the rule that produced Best, set by MatchLine
}

// Decide applies the thresholds. A line auto-matches only when its best
// candidate scores at least Auto and leads the runner-up by TieMargin (BR-36.2);
// otherwise a best candidate scoring at least Suggest is suggested.
func Decide(cands []Candidate, t Thresholds) Decision {
	var ranked []Candidate
	for _, c := range cands {
		pos := len(ranked)
		for pos > 0 && (ranked[pos-1].Score < c.Score || ranked[pos-1].Score == c.Score && ranked[pos-1].ID > c.ID) {
			pos--
		}
		if pos >= 3 {
			continue
		}
		ranked = slices.Insert(ranked, pos, c)
		if len(ranked) > 3 {
			ranked = ranked[:3]
		}
	}
	if len(ranked) == 0 || ranked[0].Score < t.Suggest {
		return Decision{Status: StatusUnmatched, Ranked: ranked}
	}
	best := ranked[0]
	if best.Score >= t.Auto && (len(ranked) == 1 || ranked[1].Score <= best.Score-t.TieMargin) {
		return Decision{Status: StatusMatched, Best: &best, Ranked: ranked}
	}
	return Decision{Status: StatusSuggested, Best: &best, Ranked: ranked}
}

// MinStrictReferenceLen is the shortest normalized reference a strict rule
// trusts on its own.
const MinStrictReferenceLen = 8

// Rule is one ordered matching rule (FR-36.3): a weight set and a tolerance.
// A Strict rule may only auto-match on a reference: the value date must be
// inside the window, the reference at least MinStrictReferenceLen characters, not
// a placeholder, and no other open item may carry the same reference.
type Rule struct {
	Name      string
	Priority  int
	Weights   Weights
	Tolerance Tolerance
	Strict    bool
}

// Problems returns the field-keyed violations of the stored-rule ranges: weights
// sum to 100, the amount carries at least 20 of them, a non-strict rule cannot
// reach the auto threshold on amount and date alone, the tolerance is not
// negative and the window is 0 to 30 days.
func (r Rule) Problems() map[string]string {
	w, out := r.Weights, map[string]string{}
	switch {
	case w.Amount < 20 || w.Date < 0 || w.Reference < 0 || w.Counterparty < 0:
		out["weights"] = "amount weight must be at least 20 and no weight may be negative"
	case w.Amount+w.Date+w.Reference+w.Counterparty != 100:
		out["weights"] = "weights must sum to 100"
	case !r.Strict && w.Amount+w.Date >= DefaultThresholds.Auto:
		out["weights"] = "amount and date alone could auto-match; give reference or counterparty weight, or make the rule strict"
	}
	if r.Tolerance.Amount.IsNegative() {
		out["amountTolerance"] = "must not be negative"
	}
	if r.Tolerance.DateWindowDays < 0 || r.Tolerance.DateWindowDays > 30 {
		out["dateWindowDays"] = "must be 0 to 30"
	}
	if r.Priority < 1 || r.Priority > 1000 {
		out["priority"] = "must be 1 to 1000"
	}
	if strings.TrimSpace(r.Name) == "" || len(r.Name) > 80 {
		out["name"] = "1 to 80 characters"
	}
	return out
}

// Validate is Problems as one error.
func (r Rule) Validate() error {
	for _, msg := range r.Problems() {
		return errors.New(msg)
	}
	return nil
}

// DefaultRules run the balanced rule first, then the strict reference rule that
// carries lines whose counterparty text the bank garbled.
func DefaultRules(tol Tolerance) []Rule {
	return []Rule{{Name: "balanced", Priority: 1, Weights: DefaultWeights, Tolerance: tol}, {Name: "reference", Priority: 2, Weights: ReferenceWeights, Tolerance: tol, Strict: true}}
}

func normalizedReference(s string) string { return refStrip.ReplaceAllString(strings.ToUpper(s), "") }

func withinWindow(a, b string, window int) bool {
	return inWindow(prepare("", Facts{ValueDate: a}), prepare("", Facts{ValueDate: b}), window)
}

type matchIndex struct {
	items    []*prepared
	byID     map[string]*prepared
	byRef    map[string][]*prepared
	refCount map[string]int
}

func newMatchIndex(cands map[string]Facts) *matchIndex {
	ix := &matchIndex{refCount: map[string]int{}, byID: make(map[string]*prepared, len(cands)), byRef: map[string][]*prepared{}}
	for id, f := range cands {
		p := prepare(id, f)
		ix.items = append(ix.items, p)
		ix.byID[id] = p
		if p.reference != "" {
			ix.refCount[p.reference]++
			ix.byRef[p.reference] = append(ix.byRef[p.reference], p)
		}
	}
	sort.Slice(ix.items, func(i, j int) bool {
		if c := ix.items[i].Amount.Cmp(ix.items[j].Amount); c != 0 {
			return c < 0
		}
		return ix.items[i].id < ix.items[j].id
	})
	for i, p := range ix.items {
		p.idx = i
	}
	return ix
}

func (ix *matchIndex) near(line *prepared, r Rule) ([]*prepared, bool) {
	var out []*prepared
	if r.Strict {
		out = append(out, ix.byRef[line.reference]...)
		if line.alt != line.reference {
			out = append(out, ix.byRef[line.alt]...)
		}
		return out, len(out) < len(ix.items)
	}
	low, high := line.Amount.Sub(r.Tolerance.Amount), line.Amount.Add(r.Tolerance.Amount)
	start := sort.Search(len(ix.items), func(i int) bool { return ix.items[i].Amount.Cmp(low) >= 0 })
	for _, c := range ix.items[start:] {
		if c.Amount.Cmp(high) > 0 {
			break
		}
		if r.Weights.Date == 0 || inWindow(line, c, r.Tolerance.DateWindowDays) {
			out = append(out, c)
		}
	}
	return out, len(out) < len(ix.items)
}

func excludedBound(r Rule) int {
	if r.Strict {
		return 0
	}
	w := r.Weights
	if w.Date == 0 {
		return w.Reference + w.Counterparty
	}
	return w.Reference + w.Counterparty + max(w.Amount, w.Date)
}

func unchangedByExcluded(d Decision, bound int, t Thresholds) bool {
	if d.Best == nil {
		return bound < t.Suggest
	}
	best := d.Best.Score
	return bound < best && (d.Status != StatusMatched || bound <= best-t.TieMargin)
}

// MatchLine ranks the candidates under each rule in order. The first rule that
// auto-matches decides the line; otherwise the strongest suggestion wins, with
// the earlier rule on equal scores. Candidates are keyed by ID.
func MatchLine(line Facts, cands map[string]Facts, rules []Rule, t Thresholds) Decision {
	ix := newMatchIndex(cands)
	return ix.matchLine(prepare("", line), rules, t, false)
}

// matchLine with prefilter scores each rule against its own near items and
// rescores that rule against every item when the items left out could still
// change its decision, so the result equals a full scan.
func (ix *matchIndex) matchLine(line *prepared, rules []Rule, t Thresholds, prefilter bool) Decision {
	all := ix.items
	best := Decision{Status: StatusUnmatched}
	needRef, needSim := false, false
	for _, r := range rules {
		needRef = needRef || r.Weights.Reference > 0
		needSim = needSim || r.Weights.Counterparty > 0
	}
	pairs := make([]pair, len(all))
	known := make([]bool, len(all))
	pairOf := func(c *prepared) pair {
		if !known[c.idx] {
			pairs[c.idx], known[c.idx] = newPair(line, c, needRef, needSim), true
		}
		return pairs[c.idx]
	}
	decided := map[ruleShape]Decision{}
	for _, r := range rules {
		key := ruleShape{r.Weights, r.Tolerance.Amount.String(), r.Tolerance.DateWindowDays, r.Strict}
		d, seen := decided[key]
		if !seen {
			cands, excluded := all, false
			if prefilter {
				cands, excluded = ix.near(line, r)
			}
			d = ix.decide(line, cands, r, t, pairOf)
			if excluded && !unchangedByExcluded(d, excludedBound(r), t) {
				d = ix.decide(line, all, r, t, pairOf)
			}
			decided[key] = d
		}
		d.Rule = r.Name
		if d.Status == StatusMatched {
			winner := ix.byID[d.Best.ID]
			if (r.Strict && ix.refCount[winner.reference] > 1) || (!r.Strict && contradictsReference(line, winner)) {
				d.Status = StatusSuggested
			}
		}
		if d.Status == StatusMatched {
			return d
		}
		if d.Best != nil && (best.Best == nil || d.Best.Score > best.Best.Score) {
			best = d
		}
	}
	return best
}

type ruleShape struct {
	weights   Weights
	tolerance string
	window    int
	strict    bool
}

func (ix *matchIndex) decide(line *prepared, cands []*prepared, r Rule, t Thresholds, pairOf func(*prepared) pair) Decision {
	scored := make([]Candidate, len(cands))
	for i, c := range cands {
		p := pairOf(c)
		score := p.score(r.Tolerance, r.Weights)
		if r.Strict && !strictEligible(line, c, r.Tolerance) {
			score = 0
		}
		scored[i] = Candidate{ID: c.id, Score: score}
	}
	return Decide(scored, t)
}

func usableReference(n string) bool {
	return len(n) >= MinStrictReferenceLen && !placeholderReference(n)
}

func contradictsReference(line, cand *prepared) bool {
	if cand.reference == "" || placeholderReference(cand.reference) {
		return false
	}
	usable := false
	for _, n := range []string{line.reference, line.alt} {
		if !usableReference(n) {
			continue
		}
		usable = true
		if referenceResult(n, cand.reference) > 0 {
			return false
		}
	}
	return usable
}

func strictEligible(line, cand *prepared, tol Tolerance) bool {
	return usableReference(cand.reference) && (cand.reference == line.reference || cand.reference == line.alt) &&
		inWindow(line, cand, tol.DateWindowDays)
}

func lineReferences(f Facts) []string {
	var out []string
	for _, ref := range []string{f.Reference, f.AltReference} {
		if n := normalizedReference(ref); n != "" && !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return out
}
