package domain

import (
	"sort"

	"github.com/shopspring/decimal"
)

type Class string

const (
	ClassProfitLoss   Class = "pl"
	ClassBalanceSheet Class = "bs"
	EntryClosing            = "closing"
	EntryOpening            = "opening"
)

type Balance struct {
	Account    string
	Class      Class
	Dimensions string
	Amount     decimal.Decimal
}

type Entry struct {
	Kind       string          `json:"kind"`
	Account    string          `json:"account"`
	Dimensions string          `json:"dimensions"`
	Amount     decimal.Decimal `json:"amount"`
}

type YearEnd struct {
	Closing []Entry `json:"closing"`
	Opening []Entry `json:"opening"`
}

const (
	RunPending   = "pending"
	RunCompleted = "completed"
)

type YearEndRun struct {
	ID               string  `json:"id"`
	LegalEntityID    string  `json:"legalEntityId"`
	LedgerID         string  `json:"ledgerId"`
	FiscalYear       string  `json:"fiscalYear"`
	Status           string  `json:"status"`
	Currency         string  `json:"currency"`
	ClosingDate      string  `json:"closingDate"`
	OpeningDate      string  `json:"openingDate"`
	Proposals        YearEnd `json:"proposals"`
	ClosingJournalID string  `json:"closingJournalId,omitempty"`
	OpeningJournalID string  `json:"openingJournalId,omitempty"`
	CreatedBy        string  `json:"createdBy"`
	RequestHash      string  `json:"-"`
}

type YearEndCompleted struct {
	RunID            string `json:"runId"`
	LegalEntityID    string `json:"legalEntityId"`
	LedgerID         string `json:"ledgerId"`
	FiscalYear       string `json:"fiscalYear"`
	ClosingJournalID string `json:"closingJournalId,omitempty"`
	OpeningJournalID string `json:"openingJournalId,omitempty"`
}

const EventYearEndCompleted = "dev.passage.close.yearend.completed.v1"

type key struct{ account, dimensions string }

func CloseYear(balances []Balance, retainedEarnings string, retainDimensions bool) (YearEnd, error) {
	if len(balances) == 0 {
		return YearEnd{}, nil
	}
	if retainedEarnings == "" {
		return YearEnd{}, fail(CodeValidation, "retained earnings account is required")
	}
	closing := map[key]decimal.Decimal{}
	position := map[key]decimal.Decimal{}
	total := decimal.Zero
	for _, b := range balances {
		total = total.Add(b.Amount)
		switch b.Class {
		case ClassProfitLoss:
			if b.Account == retainedEarnings {
				return YearEnd{}, fail(CodeValidation, "retained earnings account cannot be a profit and loss account")
			}
			closing[key{b.Account, b.Dimensions}] = closing[key{b.Account, b.Dimensions}].Sub(b.Amount)
			target := key{retainedEarnings, ""}
			if retainDimensions {
				target.dimensions = b.Dimensions
			}
			closing[target] = closing[target].Add(b.Amount)
		case ClassBalanceSheet:
			position[key{b.Account, b.Dimensions}] = position[key{b.Account, b.Dimensions}].Add(b.Amount)
		default:
			return YearEnd{}, fail(CodeValidation, "account "+b.Account+" has unknown class "+string(b.Class))
		}
	}
	if !total.IsZero() {
		return YearEnd{}, fail(CodeValidation, "balances do not sum to zero")
	}
	result := YearEnd{Closing: entries(EntryClosing, closing)}
	for _, e := range result.Closing {
		if e.Account == retainedEarnings {
			position[key{e.Account, e.Dimensions}] = position[key{e.Account, e.Dimensions}].Add(e.Amount)
		}
	}
	result.Opening = entries(EntryOpening, position)
	if err := VerifyOpening(balances, retainedEarnings, result); err != nil {
		return YearEnd{}, err
	}
	return result, nil
}

func VerifyOpening(balances []Balance, retainedEarnings string, ye YearEnd) error {
	expected := map[key]decimal.Decimal{}
	for _, b := range balances {
		if b.Class == ClassBalanceSheet {
			expected[key{b.Account, b.Dimensions}] = expected[key{b.Account, b.Dimensions}].Add(b.Amount)
		}
	}
	for _, e := range ye.Closing {
		if e.Account == retainedEarnings {
			expected[key{e.Account, e.Dimensions}] = expected[key{e.Account, e.Dimensions}].Add(e.Amount)
		}
	}
	sum := decimal.Zero
	opened := map[key]decimal.Decimal{}
	for _, e := range ye.Opening {
		opened[key{e.Account, e.Dimensions}] = e.Amount
		sum = sum.Add(e.Amount)
	}
	for k, want := range expected {
		if !opened[k].Equal(want) {
			return fail(CodeValidation, "opening balance differs from closing balance for "+k.account+" "+k.dimensions)
		}
	}
	for k := range opened {
		if _, ok := expected[k]; !ok {
			return fail(CodeValidation, "opening balance has no closing balance for "+k.account+" "+k.dimensions)
		}
	}
	if !sum.IsZero() {
		return fail(CodeValidation, "opening balances do not sum to zero")
	}
	return nil
}

func entries(kind string, amounts map[key]decimal.Decimal) []Entry {
	out := make([]Entry, 0, len(amounts))
	for k, amount := range amounts {
		if !amount.IsZero() {
			out = append(out, Entry{Kind: kind, Account: k.account, Dimensions: k.dimensions, Amount: amount})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Account != out[j].Account {
			return out[i].Account < out[j].Account
		}
		return out[i].Dimensions < out[j].Dimensions
	})
	return out
}
