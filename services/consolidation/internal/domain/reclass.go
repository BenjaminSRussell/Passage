package domain

import (
	"fmt"
	"sort"
	"time"

	"github.com/shopspring/decimal"
)

// Reclassification levels and rule types (FR-40.17, `consol.reclassRules`).
const (
	LevelEntity = "Entity"
	LevelGroup  = "Group"

	TypeDebitBalanceVendors    = "DebitBalanceVendors"
	TypeCreditBalanceCustomers = "CreditBalanceCustomers"
	TypeRemainingTerm          = "RemainingTerm"
	TypeSignBased              = "SignBased"
)

// DefaultTermMonths is the RemainingTerm threshold: open items due more than 12
// months after the period end move to non-current (FR-40.17).
const DefaultTermMonths = 12

// ReclassRule is one ordered, versioned regrouping rule. Entity-level rules
// read one entity's open items; group-level rules read consolidated FS item
// balances. Threshold is in the rule's currency and a balance must exceed it
// to move.
type ReclassRule struct {
	Code          string
	Level         string
	Type          string
	Order         int
	Version       int
	SourceAccount string
	TargetAccount string
	Threshold     decimal.Decimal
	TermMonths    int
}

// OpenItem is one open item of an entity's sub-ledger. Amount is signed:
// positive is a debit balance, negative a credit balance.
type OpenItem struct {
	Party   string
	Account string
	DueDate time.Time
	Amount  decimal.Decimal
}

// GroupBalance is one consolidated FS item balance, signed like OpenItem.
type GroupBalance struct {
	Account string
	Amount  decimal.Decimal
}

// ReclassLine is one regrouping line; exactly one of Debit and Credit is
// non-zero. Reverses is true for entity-level lines, which post through the
// entity journal and reverse on the result's ReversalDate (BR-40.14);
// group-level lines are consolidation entries of the run.
type ReclassLine struct {
	RuleCode string
	Level    string
	Account  string
	Party    string
	Debit    decimal.Decimal
	Credit   decimal.Decimal
	Reverses bool
}

// ReclassResult is the outcome of a reclassification dry run (FR-40.17).
// Applied lists the codes of the rules that produced lines, in run order.
// ReversalDate is the first day of the next period when any entity-level line
// was produced, and the zero time otherwise.
type ReclassResult struct {
	Lines        []ReclassLine
	Applied      []string
	ReversalDate time.Time
}

// ReversalDate returns the first calendar day after the period that ends on periodEnd (BR-40.14).
func ReversalDate(periodEnd time.Time) time.Time {
	return time.Date(periodEnd.Year(), periodEnd.Month()+1, 1, 0, 0, 0, 0, time.UTC)
}

// RunReclassification evaluates the rules by Order (ties keep input order) and
// returns the lines they produce, every rule balanced. Balances move per party
// and are rounded to the currency minor unit. It writes nothing, so it serves
// the dry run; posting and the rerun reversal of BR-40.14 come with runs.
func RunReclassification(rules []ReclassRule, items []OpenItem, balances []GroupBalance, periodEnd time.Time, currency string) (ReclassResult, error) {
	if len(currency) != 3 {
		return ReclassResult{}, invalid("currency must be an ISO 4217 code")
	}
	ordered := append([]ReclassRule(nil), rules...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Order < ordered[j].Order })
	seen := map[string]bool{}
	var out ReclassResult
	for _, r := range ordered {
		if err := r.validate(); err != nil {
			return ReclassResult{}, err
		}
		if seen[r.Code] {
			return ReclassResult{}, invalid("duplicate reclassification rule " + r.Code)
		}
		seen[r.Code] = true
		var moves []move
		switch r.Type {
		case TypeDebitBalanceVendors:
			for _, p := range byParty(r.SourceAccount, items, nil, currency) {
				if p.sum.GreaterThan(r.Threshold) {
					moves = append(moves, move{party: p.id, amount: p.sum})
				}
			}
		case TypeCreditBalanceCustomers:
			for _, p := range byParty(r.SourceAccount, items, nil, currency) {
				if p.sum.Neg().GreaterThan(r.Threshold) {
					moves = append(moves, move{party: p.id, amount: p.sum})
				}
			}
		case TypeRemainingTerm:
			horizon := periodEnd.AddDate(0, r.termMonths(), 0)
			due := func(it OpenItem) bool { return it.DueDate.After(horizon) }
			for _, p := range byParty(r.SourceAccount, items, due, currency) {
				if !p.sum.IsZero() && p.sum.Abs().GreaterThan(r.Threshold) {
					moves = append(moves, move{party: p.id, amount: p.sum})
				}
			}
		case TypeSignBased:
			sum := decimal.Zero
			for _, b := range balances {
				if b.Account == r.SourceAccount {
					sum = sum.Add(b.Amount)
				}
			}
			if sum = round(sum, currency); sum.IsNegative() && sum.Neg().GreaterThan(r.Threshold) {
				moves = append(moves, move{amount: sum})
			}
		}
		if len(moves) == 0 {
			continue
		}
		lines := r.lines(moves)
		if err := checkReclassBalanced(r.Code, lines); err != nil {
			return ReclassResult{}, err
		}
		out.Lines = append(out.Lines, lines...)
		out.Applied = append(out.Applied, r.Code)
		if r.Level == LevelEntity {
			out.ReversalDate = ReversalDate(periodEnd)
		}
	}
	return out, nil
}

// move is one signed balance that leaves the source account for the target.
type move struct {
	party  string
	amount decimal.Decimal
}

// lines turns moves into balanced pairs, debit line first: a debit balance is
// credited out of the source and debited to the target; a credit balance the
// other way round.
func (r ReclassRule) lines(moves []move) []ReclassLine {
	reverses := r.Level == LevelEntity
	out := make([]ReclassLine, 0, 2*len(moves))
	for _, m := range moves {
		v := m.amount.Abs()
		target := ReclassLine{RuleCode: r.Code, Level: r.Level, Account: r.TargetAccount, Party: m.party, Reverses: reverses}
		source := ReclassLine{RuleCode: r.Code, Level: r.Level, Account: r.SourceAccount, Party: m.party, Reverses: reverses}
		if m.amount.IsPositive() {
			target.Debit, source.Credit = v, v
			out = append(out, target, source)
		} else {
			source.Debit, target.Credit = v, v
			out = append(out, source, target)
		}
	}
	return out
}

func (r ReclassRule) termMonths() int {
	if r.TermMonths == 0 {
		return DefaultTermMonths
	}
	return r.TermMonths
}

func (r ReclassRule) validate() error {
	switch {
	case r.Code == "":
		return invalid("reclassification rule code is required")
	case r.SourceAccount == "" || r.TargetAccount == "":
		return invalid("reclassification rule " + r.Code + " needs a source and a target account")
	case r.SourceAccount == r.TargetAccount:
		return invalid("reclassification rule " + r.Code + " must move between two accounts")
	case r.Threshold.IsNegative():
		return invalid("reclassification threshold must not be negative")
	case r.Version < 1:
		return invalid("reclassification rule version must be 1 or more")
	case r.TermMonths < 0:
		return invalid("remaining term must not be negative")
	}
	wantLevel := LevelEntity
	switch r.Type {
	case TypeSignBased:
		wantLevel = LevelGroup
	case TypeDebitBalanceVendors, TypeCreditBalanceCustomers, TypeRemainingTerm:
	default:
		return invalid(fmt.Sprintf("unknown reclassification type %q", r.Type))
	}
	if r.Level != wantLevel {
		return invalid(fmt.Sprintf("rule %s of type %s must be at %s level", r.Code, r.Type, wantLevel))
	}
	return nil
}

type partySum struct {
	id  string
	sum decimal.Decimal
}

// byParty nets the open items on account that pass keep (all when nil) per
// party, rounded to the currency minor unit, in party order.
func byParty(account string, items []OpenItem, keep func(OpenItem) bool, currency string) []partySum {
	sums := map[string]decimal.Decimal{}
	for _, it := range items {
		if it.Account == account && (keep == nil || keep(it)) {
			sums[it.Party] = sums[it.Party].Add(it.Amount)
		}
	}
	keys := make([]string, 0, len(sums))
	for k := range sums {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]partySum, 0, len(keys))
	for _, k := range keys {
		out = append(out, partySum{id: k, sum: round(sums[k], currency)})
	}
	return out
}

func checkReclassBalanced(rule string, lines []ReclassLine) error {
	net := decimal.Zero
	for _, l := range lines {
		net = net.Add(l.Debit).Sub(l.Credit)
	}
	if !net.IsZero() {
		return &Error{Code: CodeUnbalancedRule, Detail: fmt.Sprintf("reclassification rule %s unbalanced by %s", rule, net.Abs().String())}
	}
	return nil
}
