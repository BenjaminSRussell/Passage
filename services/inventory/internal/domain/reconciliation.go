package domain

import (
	"fmt"
	"regexp"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

// CodeReconciliationNotFound is an unknown reconciliation run (service code,
// not in the 3.16.18 catalog).
const CodeReconciliationNotFound = "INV-REC-001"

// currencyCode is an ISO 4217 alpha-3 code.
var currencyCode = regexp.MustCompile(`^[A-Z]{3}$`)

// Reconciliation status of one daily run (FR-16.12).
const (
	ReconciliationMatched    = "MATCHED"
	ReconciliationDifference = "DIFFERENCE"
)

// Ledger balance sources a reconciliation can compare against.
const (
	// LedgerSourceJournals means the balance is the net of the inventory account
	// over the GL entries of the POSTED inventory journals in the currency (the
	// only ledger lines this service produces until the wave 11 ledger Post).
	LedgerSourceJournals = "GL_JOURNALS"
	// LedgerSourceOverride means the caller supplied the ledger balance, read
	// from the wave 11 ledger, which also carries manual journals (AC-16.9).
	LedgerSourceOverride = "LEDGER_OVERRIDE"
)

// Reconciliation is one run of the inventory-to-ledger check for a legal
// entity, inventory account and currency on a run date. NegativeRows counts the
// on-hand rows of the legal entity whose physical inventory is below zero
// (FR-16.4: negative stock, where allowed, is flagged in the daily
// reconciliation); it does not change the status.
type Reconciliation struct {
	ID             string
	LegalEntityID  string
	Account        string
	Currency       string
	RunDate        time.Time
	InventoryValue decimal.Decimal
	LedgerBalance  decimal.Decimal
	Diff           decimal.Decimal
	Status         string
	LedgerSource   string
	NegativeRows   int
	CreatedAt      time.Time
}

// BalanceComparison is the outcome of comparing the two sides of a reconciliation.
type BalanceComparison struct {
	InventoryValue decimal.Decimal
	LedgerBalance  decimal.Decimal
	Diff           decimal.Decimal
	Status         string
}

// CompareBalances rounds both sides to the currency's minor unit, the precision
// the ledger posts at, and returns inventory minus ledger with its status. The
// run never adjusts a balance: any difference at the minor unit is reported.
func CompareBalances(inventory, ledger decimal.Decimal, currency string) (BalanceComparison, error) {
	if !currencyCode.MatchString(currency) {
		return BalanceComparison{}, &Error{Code: CodeInvalid, Detail: fmt.Sprintf("invalid currency %q", currency)}
	}
	scale := money.MinorUnits(currency)
	inv := inventory.Round(scale)
	led := ledger.Round(scale)
	diff := inv.Sub(led)
	status := ReconciliationMatched
	if !diff.IsZero() {
		status = ReconciliationDifference
	}
	return BalanceComparison{InventoryValue: inv, LedgerBalance: led, Diff: diff, Status: status}, nil
}
