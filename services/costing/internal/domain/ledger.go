package domain

import (
	"fmt"

	"github.com/shopspring/decimal"
)

// LedgerSpec is one inventory valuation ledger of a legal entity (FR-20.15): a
// costing method and a currency applied to the same transaction sequence.
type LedgerSpec struct {
	ID       string
	Method   Method
	Currency string
	// Scale is the minor unit of Currency.
	Scale int32
	// Rate converts one unit of the account currency (the primary ledger's
	// currency) into Currency. The primary ledger's rate is exactly 1.
	Rate    decimal.Decimal
	Primary bool
	// Standard is the standard unit cost in Currency (STANDARD only).
	Standard decimal.Decimal
	Fallback Fallback
}

// LedgerResult is the settlement of one valuation ledger.
type LedgerResult struct {
	ID       string
	Currency string
	Primary  bool
	Result   Result
}

// CodeLedgerInvalid is the message-catalog code for a malformed ledger set.
const CodeLedgerInvalid = "CST-VLG-001"

// SettleLedgers settles the same transactions under every valuation ledger
// (BR-20.13). Each ledger converts receipt values at its own rate, rounds them
// half-even to its scale, and settles independently under its own method, so
// ledgers share quantities and sequence but not values. Exactly one ledger is
// primary; results keep the input order of specs.
func SettleLedgers(txns []Txn, specs []LedgerSpec) ([]LedgerResult, error) {
	if len(specs) == 0 {
		return nil, &Error{CodeLedgerInvalid, "at least one valuation ledger is required"}
	}
	seen := map[string]bool{}
	primaries := 0
	for _, s := range specs {
		switch {
		case s.ID == "":
			return nil, &Error{CodeLedgerInvalid, "every valuation ledger needs an id"}
		case seen[s.ID]:
			return nil, &Error{CodeLedgerInvalid, fmt.Sprintf("valuation ledger %s appears twice", s.ID)}
		case !s.Rate.IsPositive():
			return nil, &Error{CodeLedgerInvalid, fmt.Sprintf("valuation ledger %s needs a positive rate", s.ID)}
		case s.Primary && !s.Rate.Equal(decimal.NewFromInt(1)):
			return nil, &Error{CodeLedgerInvalid, fmt.Sprintf("primary valuation ledger %s must have rate 1", s.ID)}
		}
		seen[s.ID] = true
		if s.Primary {
			primaries++
		}
	}
	if primaries != 1 {
		return nil, &Error{CodeLedgerInvalid, fmt.Sprintf("exactly one primary valuation ledger is required, got %d", primaries)}
	}
	out := make([]LedgerResult, 0, len(specs))
	for _, s := range specs {
		converted := make([]Txn, len(txns))
		for i, t := range txns {
			converted[i] = t
			if t.Kind == Receipt {
				converted[i].Value = t.Value.Mul(s.Rate).RoundBank(s.Scale)
			}
		}
		res, err := Settle(converted, Options{Method: s.Method, Scale: s.Scale, Standard: s.Standard, Fallback: s.Fallback})
		if err != nil {
			return nil, fmt.Errorf("valuation ledger %s: %w", s.ID, err)
		}
		out = append(out, LedgerResult{ID: s.ID, Currency: s.Currency, Primary: s.Primary, Result: res})
	}
	return out, nil
}
