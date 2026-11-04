package domain

import (
	"sort"
	"time"

	"github.com/shopspring/decimal"
)

type SortMethod string

const (
	SortRemainingTerm SortMethod = "remaining_term"
	SortDebitCredit   SortMethod = "debit_credit_balance"
	SortAccountSign   SortMethod = "account_balance_sign"
)

type OpenItem struct {
	ID            string
	Account       string
	Party         string
	Currency      string
	Dimensions    string
	Amount        decimal.Decimal
	Due           *time.Time
	NormalBalance string
}

type TermBucket struct {
	FromDays          int    `json:"fromDays"`
	ToDays            int    `json:"toDays"`
	AdjustmentAccount string `json:"adjustmentAccount"`
	TargetAccount     string `json:"targetAccount"`
}

type RegroupingRule struct {
	Code              string
	SortMethod        SortMethod
	Sequence          int
	SourceAccount     string
	AdjustmentAccount string
	TargetAccount     string
	NormalBalance     string
	Buckets           []TermBucket
}

type ReclassLine struct {
	RuleCode          string          `json:"ruleCode"`
	AdjustmentAccount string          `json:"adjustmentAccount"`
	TargetAccount     string          `json:"targetAccount"`
	Amount            decimal.Decimal `json:"amount"`
	SourceCredit      bool            `json:"sourceCredit,omitempty"`
	Currency          string          `json:"currency,omitempty"`
	Dimensions        string          `json:"dimensions,omitempty"`
	Party             string          `json:"party,omitempty"`
	Bucket            string          `json:"bucket,omitempty"`
	ReverseOn         string          `json:"reverseOn,omitempty"`
	ItemIDs           []string        `json:"itemIds,omitempty"`
}

func Regroup(items []OpenItem, balances []Balance, rules []RegroupingRule, keyDate time.Time) ([]ReclassLine, error) {
	ordered := append([]RegroupingRule(nil), rules...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Sequence < ordered[j].Sequence })
	used := map[string]bool{}
	var lines []ReclassLine
	reverseOn := keyDate.AddDate(0, 0, 1).Format("2006-01-02")
	for _, rule := range ordered {
		var more []ReclassLine
		var err error
		switch rule.SortMethod {
		case SortRemainingTerm:
			more, err = regroupRemainingTerm(items, used, rule, keyDate, reverseOn)
		case SortDebitCredit:
			more, err = regroupDebitCredit(items, used, rule, reverseOn)
		case SortAccountSign:
			more, err = regroupAccountSign(balances, rule, reverseOn)
		default:
			return nil, fail(CodeValidation, "unknown regrouping sort method "+string(rule.SortMethod))
		}
		if err != nil {
			return nil, err
		}
		lines = append(lines, more...)
	}
	return lines, nil
}

func regroupRemainingTerm(items []OpenItem, used map[string]bool, rule RegroupingRule, keyDate time.Time, reverseOn string) ([]ReclassLine, error) {
	if len(rule.Buckets) == 0 {
		return nil, fail(CodeValidation, "remaining_term rules need at least one bucket")
	}
	type groupKey struct {
		bucket int
		curr   string
		dims   string
	}
	type agg struct {
		amount decimal.Decimal
		ids    []string
	}
	groups := map[groupKey]agg{}
	shortest := 0
	for i, b := range rule.Buckets {
		if b.FromDays < rule.Buckets[shortest].FromDays {
			shortest = i
		}
	}
	for _, item := range items {
		if used[item.ID] || (rule.SourceAccount != "" && item.Account != rule.SourceAccount) {
			continue
		}
		term := remainingDays(item.Due, keyDate)
		idx := shortest
		if term >= 0 {
			for i, b := range rule.Buckets {
				if term >= b.FromDays && (b.ToDays == 0 || term <= b.ToDays) {
					idx = i
					break
				}
			}
		}
		bucket := rule.Buckets[idx]
		used[item.ID] = true
		if bucket.AdjustmentAccount == "" || bucket.AdjustmentAccount == bucket.TargetAccount {
			continue
		}
		key := groupKey{bucket: idx, curr: item.Currency, dims: item.Dimensions}
		g := groups[key]
		g.amount = g.amount.Add(item.Amount)
		g.ids = append(g.ids, item.ID)
		groups[key] = g
	}
	var lines []ReclassLine
	for key, g := range groups {
		if g.amount.IsZero() {
			continue
		}
		b := rule.Buckets[key.bucket]
		lines = append(lines, ReclassLine{
			RuleCode: rule.Code, AdjustmentAccount: b.AdjustmentAccount, TargetAccount: b.TargetAccount,
			Amount: g.amount.Abs(), SourceCredit: g.amount.IsNegative(), Currency: key.curr, Dimensions: key.dims, ReverseOn: reverseOn, ItemIDs: g.ids,
		})
	}
	sort.Slice(lines, func(i, j int) bool {
		a, b := lines[i], lines[j]
		if a.AdjustmentAccount != b.AdjustmentAccount {
			return a.AdjustmentAccount < b.AdjustmentAccount
		}
		if a.Currency != b.Currency {
			return a.Currency < b.Currency
		}
		return a.Dimensions < b.Dimensions
	})
	return lines, nil
}

func remainingDays(due *time.Time, keyDate time.Time) int {
	if due == nil {
		return -1
	}
	return int(due.Sub(keyDate).Hours() / 24)
}

func regroupDebitCredit(items []OpenItem, used map[string]bool, rule RegroupingRule, reverseOn string) ([]ReclassLine, error) {
	if rule.AdjustmentAccount == "" || rule.TargetAccount == "" {
		return nil, fail(CodeValidation, "debit_credit_balance rules need adjustment and target accounts")
	}
	type group struct {
		amount decimal.Decimal
		ids    []string
		curr   string
		dims   string
		normal string
		party  string
	}
	groups := map[string]group{}
	for _, item := range items {
		if used[item.ID] || (rule.SourceAccount != "" && item.Account != rule.SourceAccount) {
			continue
		}
		key := item.Party + "|" + item.Account + "|" + item.Currency + "|" + item.Dimensions
		g := groups[key]
		g.amount = g.amount.Add(item.Amount)
		g.ids = append(g.ids, item.ID)
		g.curr, g.dims, g.party = item.Currency, item.Dimensions, item.Party
		if item.NormalBalance != "" {
			g.normal = item.NormalBalance
		}
		groups[key] = g
	}
	var lines []ReclassLine
	for _, g := range groups {
		normal := g.normal
		if normal == "" {
			normal = rule.NormalBalance
		}
		if !oppositeSign(g.amount, normal) {
			continue
		}
		for _, id := range g.ids {
			used[id] = true
		}
		lines = append(lines, ReclassLine{
			RuleCode: rule.Code, AdjustmentAccount: rule.AdjustmentAccount, TargetAccount: rule.TargetAccount,
			Amount: g.amount.Abs(), SourceCredit: g.amount.IsNegative(), Currency: g.curr, Dimensions: g.dims, Party: g.party, ReverseOn: reverseOn, ItemIDs: g.ids,
		})
	}
	sort.Slice(lines, func(i, j int) bool {
		a, b := lines[i], lines[j]
		if a.Party != b.Party {
			return a.Party < b.Party
		}
		if a.Currency != b.Currency {
			return a.Currency < b.Currency
		}
		return a.Dimensions < b.Dimensions
	})
	return lines, nil
}

func oppositeSign(amount decimal.Decimal, normal string) bool {
	if amount.IsZero() {
		return false
	}
	if normal == "credit" {
		return amount.IsPositive()
	}
	return amount.IsNegative()
}

func regroupAccountSign(balances []Balance, rule RegroupingRule, reverseOn string) ([]ReclassLine, error) {
	if rule.AdjustmentAccount == "" || rule.TargetAccount == "" {
		return nil, fail(CodeValidation, "account_balance_sign rules need adjustment and target accounts")
	}
	var lines []ReclassLine
	for _, b := range balances {
		if rule.SourceAccount != "" && b.Account != rule.SourceAccount {
			continue
		}
		if !oppositeSign(b.Amount, rule.NormalBalance) {
			continue
		}
		lines = append(lines, ReclassLine{
			RuleCode: rule.Code, AdjustmentAccount: rule.AdjustmentAccount, TargetAccount: rule.TargetAccount,
			Amount: b.Amount.Abs(), SourceCredit: b.Amount.IsNegative(), Dimensions: b.Dimensions, ReverseOn: reverseOn,
		})
	}
	return lines, nil
}
