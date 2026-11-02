package domain

import (
	"errors"
	"fmt"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

// Accounts used by the packing-slip posting matrix (BR-28.1). They come from
// the wave 16 posting profile; the constants name the roles.
const (
	AccountInventory            = "1400"
	AccountDeliveredNotInvoiced = "1405"
	AccountCOGS                 = "5000"
	AccountGRIRClearing         = "2110"
)

// ErrUnbalanced means a posting does not net to zero. It is a programming error.
var ErrUnbalanced = errors.New("posting: journal lines do not balance")

// LineKind selects the BR-28.1 row that applies to a line.
type LineKind string

// Line kinds.
const (
	KindStocked    LineKind = "STOCKED"
	KindDropShip   LineKind = "DROP_SHIP"
	KindNonStocked LineKind = "NON_STOCKED"
)

// PostingLine is one shipped line at its cost snapshot.
type PostingLine struct {
	OrderLineID    string
	Kind           LineKind
	Qty            decimal.Decimal
	UnitCost       decimal.Decimal
	CostSnapshotID string
}

// JournalLine is a signed journal entry: debits are positive, credits negative.
type JournalLine struct {
	Account     string       `json:"account"`
	Amount      money.Amount `json:"amount"`
	OrderLineID string       `json:"orderLineId"`
}

// PackingSlipPosting returns the journal for a packing slip at packing-slip
// post time. Stocked lines post Dr 1405 / Cr 1400 at cost (physical posting,
// the default), or Dr 5000 / Cr 1400 when cogsAtShipment is set (PGI-style).
// Drop-ship and non-stocked lines post nothing here; drop-ship COGS is posted
// when the vendor confirms (DropShipPosting). Each line rounds once.
func PackingSlipPosting(lines []PostingLine, cogsAtShipment bool, currency string) ([]JournalLine, error) {
	var out []JournalLine
	for _, l := range lines {
		if l.Kind != KindStocked {
			continue
		}
		amt := money.Amount{Value: l.UnitCost, Currency: currency}.Mul(l.Qty)
		if amt.IsZero() {
			continue
		}
		debit := AccountDeliveredNotInvoiced
		if cogsAtShipment {
			debit = AccountCOGS
		}
		out = append(out,
			JournalLine{Account: debit, Amount: amt, OrderLineID: l.OrderLineID},
			JournalLine{Account: AccountInventory, Amount: amt.Neg(), OrderLineID: l.OrderLineID},
		)
	}
	if err := checkBalanced(out, currency); err != nil {
		return nil, err
	}
	return out, nil
}

// Reverse returns the mirror of a posting: same accounts and dimensions, every
// amount negated. Cancelling a packing slip posts this (FR-28.5).
func Reverse(lines []JournalLine) []JournalLine {
	out := make([]JournalLine, len(lines))
	for i, l := range lines {
		out[i] = JournalLine{Account: l.Account, Amount: l.Amount.Neg(), OrderLineID: l.OrderLineID}
	}
	return out
}

// Net returns the sum of signed amounts posted to account, in currency.
func Net(lines []JournalLine, account, currency string) (money.Amount, error) {
	var amounts []money.Amount
	for _, l := range lines {
		if l.Account == account {
			amounts = append(amounts, l.Amount)
		}
	}
	return money.Sum(currency, amounts...)
}

func checkBalanced(lines []JournalLine, currency string) error {
	var amounts []money.Amount
	for _, l := range lines {
		amounts = append(amounts, l.Amount)
	}
	total, err := money.Sum(currency, amounts...)
	if err != nil {
		return err
	}
	if !total.IsZero() {
		return fmt.Errorf("%w: net %s", ErrUnbalanced, total)
	}
	return nil
}
