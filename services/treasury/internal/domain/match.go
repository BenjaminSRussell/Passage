package domain

import (
	"errors"
	"math"
	"regexp"
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
	Amount       decimal.Decimal
	ValueDate    string // YYYY-MM-DD
	Reference    string // end-to-end id, check number or invoice number
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
type Weights struct{ Amount, Date, Reference, Counterparty int }

var (
	DefaultWeights   = Weights{WeightAmount, WeightDate, WeightReference, WeightCounterparty}
	ReferenceWeights = Weights{Amount: 40, Date: 10, Reference: 50}
)

// ScoreWith is Score under the weights of a rule.
func ScoreWith(line, cand Facts, tol Tolerance, w Weights) int {
	total := float64(w.Amount)*amountResult(line.Amount, cand.Amount, tol.Amount) +
		float64(w.Date)*dateResult(line.ValueDate, cand.ValueDate, tol.DateWindowDays) +
		float64(w.Reference)*referenceResult(line.Reference, cand.Reference) +
		float64(w.Counterparty)*similarity(line.Counterparty, cand.Counterparty)
	return int(math.Round(total))
}

// amountResult is 1 for an exact amount, 1 - |delta|/tolerance inside the tolerance and 0 outside it.
func amountResult(a, b, tolerance decimal.Decimal) float64 {
	diff := a.Sub(b).Abs()
	if diff.IsZero() {
		return 1
	}
	if !tolerance.IsPositive() {
		return 0
	}
	return clip01(1 - diff.Div(tolerance).InexactFloat64())
}

// dateResult is 1 - |days|/window; with no window only the same day scores.
func dateResult(a, b string, window int) float64 {
	da, errA := time.Parse("2006-01-02", a)
	db, errB := time.Parse("2006-01-02", b)
	if errA != nil || errB != nil {
		return 0
	}
	days := math.Abs(da.Sub(db).Hours() / 24)
	if window <= 0 {
		if days == 0 {
			return 1
		}
		return 0
	}
	return clip01(1 - days/float64(window))
}

var refStrip = regexp.MustCompile(`[^A-Z0-9]`)

// referenceResult is 1 for equal references, 0.5 when one contains the other
// (at least six characters, so a short number cannot match by accident), else 0.
func referenceResult(a, b string) float64 {
	na, nb := refStrip.ReplaceAllString(strings.ToUpper(a), ""), refStrip.ReplaceAllString(strings.ToUpper(b), "")
	switch {
	case na == "" || nb == "":
		return 0
	case na == nb:
		return 1
	case len(na) >= 6 && strings.Contains(nb, na), len(nb) >= 6 && strings.Contains(na, nb):
		return 0.5
	}
	return 0
}

var nameStrip = regexp.MustCompile(`[^a-z0-9 ]`)

// similarity is the Jaccard index of the trigram sets of two normalized names.
func similarity(a, b string) float64 {
	ta, tb := trigrams(a), trigrams(b)
	if len(ta) == 0 || len(tb) == 0 {
		return 0
	}
	shared := 0
	for g := range ta {
		if _, ok := tb[g]; ok {
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
}

// Decide applies the thresholds. A line auto-matches only when its best
// candidate scores at least Auto and leads the runner-up by TieMargin (BR-36.2);
// otherwise a best candidate scoring at least Suggest is suggested.
func Decide(cands []Candidate, t Thresholds) Decision {
	ranked := append([]Candidate(nil), cands...)
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].Score != ranked[j].Score {
			return ranked[i].Score > ranked[j].Score
		}
		return ranked[i].ID < ranked[j].ID
	})
	if len(ranked) > 3 {
		ranked = ranked[:3]
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

// Rule is one ordered matching rule (FR-36.3): a weight set and a tolerance.
type Rule struct {
	Name      string
	Weights   Weights
	Tolerance Tolerance
}

// DefaultRules run the balanced rule first, then the reference rule that
// carries lines whose counterparty text the bank garbled.
func DefaultRules(tol Tolerance) []Rule {
	return []Rule{{"balanced", DefaultWeights, tol}, {"reference", ReferenceWeights, tol}}
}

// MatchLine ranks the candidates under each rule in order. The first rule that
// auto-matches decides the line; otherwise the strongest suggestion wins, with
// the earlier rule on equal scores. Candidates are keyed by ID.
func MatchLine(line Facts, cands map[string]Facts, rules []Rule, t Thresholds) Decision {
	best := Decision{Status: StatusUnmatched}
	for _, r := range rules {
		scored := make([]Candidate, 0, len(cands))
		for id, c := range cands {
			scored = append(scored, Candidate{ID: id, Score: ScoreWith(line, c, r.Tolerance, r.Weights)})
		}
		d := Decide(scored, t)
		if d.Status == StatusMatched {
			return d
		}
		if d.Best != nil && (best.Best == nil || d.Best.Score > best.Best.Score) {
			best = d
		}
	}
	return best
}
