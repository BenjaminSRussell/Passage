package domain

import (
	"fmt"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

// Side is the debit or credit side of a journal leg.
type Side string

const (
	Debit  Side = "debit"
	Credit Side = "credit"
)

// ParseSide accepts "debit" or "credit".
func ParseSide(s string) (Side, error) {
	switch Side(s) {
	case Debit, Credit:
		return Side(s), nil
	}
	return "", invalid(fmt.Sprintf("side must be debit or credit, got %q", s))
}

// Opposite returns the other side.
func (s Side) Opposite() Side {
	if s == Debit {
		return Credit
	}
	return Debit
}

// MatrixRow is one entry of the IC accounting matrix (BR-40.4): for entity A
// holding a line against entity B, the receivable account in A and the payable
// account in B.
type MatrixRow struct {
	EntityA string
	EntityB string
	DueFrom string
	DueTo   string
}

// Matrix is the IC accounting matrix for a group.
type Matrix []MatrixRow

// Lookup resolves the matrix row for the ordered pair (a, b). A missing row
// fails the post with IC-0805.
func (m Matrix) Lookup(a, b string) (MatrixRow, error) {
	for _, r := range m {
		if r.EntityA == a && r.EntityB == b {
			return r, nil
		}
	}
	return MatrixRow{}, &Error{Code: CodeNoMatrix, Detail: fmt.Sprintf("no IC accounting matrix entry for %s to %s", a, b)}
}

// Line is an intercompany journal line posted in entity Entity against
// offset entity B (FR-40.5). Amount is in the entity's currency.
type Line struct {
	Entity  string
	Account string
	Side    Side
	Amount  money.Amount
}

// Leg is one posted line of a derived journal.
type Leg struct {
	Entity  string
	Account string
	Side    Side
	Amount  money.Amount
}

// Journal is the set of legs that one IC line derives, sharing one pair id.
type Journal struct {
	PairID string
	Legs   []Leg
}

// DeriveJournal derives the mirrored journal of BR-40.4. For line L in A with
// offset B and offset account O in B:
//
//	A: L (side s), DueFrom(B) (opposite of s)
//	B: DueTo(A) (side s), O (opposite of s), in B's currency at rate
//
// The line amount is rounded to its currency's minor unit first, so the A legs
// are exactly what posts. The result balances in each entity; a missing matrix
// row fails with IC-0805.
func DeriveJournal(m Matrix, pairID string, line Line, offsetEntity, offsetAccount, counterCurrency string, rate decimal.Decimal) (Journal, error) {
	if line.Entity == "" || line.Account == "" || offsetEntity == "" || offsetAccount == "" {
		return Journal{}, invalid("entity, account and offset entity and account are required")
	}
	if line.Entity == offsetEntity {
		return Journal{}, invalid("offset entity must differ from the line entity")
	}
	if len(counterCurrency) != 3 {
		return Journal{}, invalid("counter currency must be an ISO 4217 code")
	}
	if !rate.IsPositive() {
		return Journal{}, invalid("rate must be positive")
	}
	if len(line.Amount.Currency) != 3 {
		return Journal{}, invalid("line currency must be an ISO 4217 code")
	}
	line.Amount = line.Amount.Round()
	if !line.Amount.Value.IsPositive() {
		return Journal{}, invalid("amount must be positive after rounding to the currency minor unit")
	}
	row, err := m.Lookup(line.Entity, offsetEntity)
	if err != nil {
		return Journal{}, err
	}
	counter := money.Amount{Value: line.Amount.Value, Currency: counterCurrency}.Mul(rate)
	j := Journal{PairID: pairID, Legs: []Leg{
		{Entity: line.Entity, Account: line.Account, Side: line.Side, Amount: line.Amount},
		{Entity: line.Entity, Account: row.DueFrom, Side: line.Side.Opposite(), Amount: line.Amount},
		{Entity: offsetEntity, Account: row.DueTo, Side: line.Side, Amount: counter},
		{Entity: offsetEntity, Account: offsetAccount, Side: line.Side.Opposite(), Amount: counter},
	}}
	if err := j.Balanced(); err != nil {
		return Journal{}, err
	}
	return j, nil
}

// Balanced checks that debits equal credits for every entity and currency.
func (j Journal) Balanced() error {
	type key struct{ entity, currency string }
	net := map[key]decimal.Decimal{}
	for _, l := range j.Legs {
		k := key{l.Entity, l.Amount.Currency}
		v := l.Amount.Value
		if l.Side == Credit {
			v = v.Neg()
		}
		net[k] = net[k].Add(v)
	}
	for k, v := range net {
		if !v.IsZero() {
			return &Error{Code: CodeUnbalanced, Detail: fmt.Sprintf("journal unbalanced for %s in %s by %s", k.entity, k.currency, v)}
		}
	}
	return nil
}
