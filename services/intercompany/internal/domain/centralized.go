package domain

import (
	"github.com/BenjaminSRussell/passage/platform/money"
)

// PaymentDirection says whether the paying entity pays a vendor invoice of the
// owning entity or receives a customer payment for it (FR-40.12).
type PaymentDirection string

const (
	DirectionPay     PaymentDirection = "Pay"
	DirectionReceive PaymentDirection = "Receive"
)

// CentralizedPaymentSetup is the pay / receive on behalf setup per (paying,
// owning, payment method) (FR-40.12).
type CentralizedPaymentSetup struct {
	PayingEntity  string
	OwningEntity  string
	PaymentMethod string
	Direction     PaymentDirection
}

// Validate checks the setup before it is stored.
func (s CentralizedPaymentSetup) Validate() error {
	switch {
	case s.PayingEntity == "" || s.OwningEntity == "":
		return invalid("paying and owning entities are required")
	case s.PayingEntity == s.OwningEntity:
		return invalid("paying and owning entity must differ")
	case s.PaymentMethod == "":
		return invalid("payment method is required")
	case s.Direction != DirectionPay && s.Direction != DirectionReceive:
		return invalid("direction must be Pay or Receive")
	}
	return nil
}

// CentralPaymentJournal derives the two-sided postings of one centralized payment
// (FR-40.12, BR-40.4). Pay: the paying entity posts Dr Due from owning / Cr Bank,
// the owning entity posts Dr Vendor (owningAccount) / Cr Due to paying. Receive
// mirrors it: Dr Bank / Cr Due to owning in the paying entity and Dr Due from
// paying / Cr Customer (owningAccount) in the owning entity. Both sides share pairID.
// The amount is rounded to its currency's minor unit first. A missing matrix row
// fails with IC-0805.
func CentralPaymentJournal(m Matrix, pairID string, s CentralizedPaymentSetup, amount money.Amount, bankAccount, owningAccount string) (Journal, error) {
	if err := s.Validate(); err != nil {
		return Journal{}, err
	}
	if len(amount.Currency) != 3 {
		return Journal{}, invalid("payment currency must be an ISO 4217 code")
	}
	amount = amount.Round()
	if !amount.Value.IsPositive() {
		return Journal{}, invalid("payment amount must be positive after rounding to the currency minor unit")
	}
	if bankAccount == "" || owningAccount == "" {
		return Journal{}, invalid("bank and owning accounts are required")
	}
	var j Journal
	j.PairID = pairID
	switch s.Direction {
	case DirectionPay:
		row, err := m.Lookup(s.PayingEntity, s.OwningEntity)
		if err != nil {
			return Journal{}, err
		}
		j.Legs = []Leg{
			{Entity: s.PayingEntity, Account: row.DueFrom, Side: Debit, Amount: amount},
			{Entity: s.PayingEntity, Account: bankAccount, Side: Credit, Amount: amount},
			{Entity: s.OwningEntity, Account: owningAccount, Side: Debit, Amount: amount},
			{Entity: s.OwningEntity, Account: row.DueTo, Side: Credit, Amount: amount},
		}
	case DirectionReceive:
		row, err := m.Lookup(s.OwningEntity, s.PayingEntity)
		if err != nil {
			return Journal{}, err
		}
		j.Legs = []Leg{
			{Entity: s.PayingEntity, Account: bankAccount, Side: Debit, Amount: amount},
			{Entity: s.PayingEntity, Account: row.DueTo, Side: Credit, Amount: amount},
			{Entity: s.OwningEntity, Account: row.DueFrom, Side: Debit, Amount: amount},
			{Entity: s.OwningEntity, Account: owningAccount, Side: Credit, Amount: amount},
		}
	}
	if err := j.Balanced(); err != nil {
		return Journal{}, err
	}
	return j, nil
}
