package domain

import (
	"time"

	"github.com/shopspring/decimal"
)

// ChargeStatus is where a submitted IC charge stands (FR-40.13).
type ChargeStatus string

const (
	ChargePending     ChargeStatus = "pending"
	ChargePosted      ChargeStatus = "posted"
	ChargeCompensated ChargeStatus = "compensated"
	// ChargeSuspense: no transfer price applied (IC-0821, BR-40.12), so nothing
	// is posted. Submitting the source again with the next version reprices it.
	ChargeSuspense ChargeStatus = "suspense"
)

// ChargeRecord is a stored IC charge request with its price and the journal it posted.
type ChargeRecord struct {
	ID              string
	SourceID        string
	Version         int
	Lender          string
	Borrower        string
	Category        string
	Role            string
	TransactionDate time.Time
	Quantity        decimal.Decimal
	UnitCost        decimal.Decimal
	Currency        string
	Status          ChargeStatus
	Method          string // the transfer price method that applied; empty on suspense
	Cost            decimal.Decimal
	Price           decimal.Decimal
	Margin          decimal.Decimal
	JournalID       string
	FailureCode     string
	CreatedBy       string
	CreatedAt       time.Time
}

// Resolution of a reconciliation item (FR-40.6).
type Resolution string

const (
	// ResolutionNone: the item matched and needs no action.
	ResolutionNone Resolution = "none"
	// ResolutionOpen: a Timing or Mismatch item that nobody has resolved.
	ResolutionOpen     Resolution = "open"
	ResolutionResolved Resolution = "resolved"
)

// ReconRun is a stored reconciliation run: the classification of every pair of a period.
type ReconRun struct {
	ID             string
	Period         string
	Currency       string
	FXTolerancePct decimal.Decimal
	Matched        int
	Timing         int
	Mismatch       int
	Open           int
	CreatedBy      string
	CreatedAt      time.Time
}

// ReconItem is one entity pair's classified item.
type ReconItem struct {
	ID               string
	RunID            string
	Entity           string
	Counterparty     string
	Ref              string
	AmountA          decimal.Decimal
	AmountB          decimal.Decimal
	NextPeriodA      decimal.Decimal
	NextPeriodB      decimal.Decimal
	FXApplies        bool
	Status           ReconStatus
	Difference       decimal.Decimal
	ToleranceApplied bool
	Resolution       Resolution
	Note             string
	ResolvedBy       string
	ResolvedAt       time.Time
}

// ClassifyItem applies Classify to a stored item's amounts and tolerance.
func ClassifyItem(it ReconItem, fxTolerancePct decimal.Decimal) ReconResult {
	return Classify(ReconInput{AmountA: it.AmountA, AmountB: it.AmountB, NextPeriodA: it.NextPeriodA, NextPeriodB: it.NextPeriodB,
		FXApplies: it.FXApplies, FXTolerancePct: fxTolerancePct})
}
