// Package domain holds the pure treasury rules: bank account validation and
// lifecycle, statement parsing and invariants, matching scores and forecast
// buckets. Nothing here performs I/O.
package domain

import (
	"fmt"
	"regexp"
	"strings"
)

// AccountStatus is the bank account lifecycle (FR-36.1): Draft -> Active -> Inactive -> Closed.
type AccountStatus string

const (
	AccountDraft    AccountStatus = "draft"
	AccountActive   AccountStatus = "active"
	AccountInactive AccountStatus = "inactive"
	AccountClosed   AccountStatus = "closed"
)

var accountTransitions = map[AccountStatus][]AccountStatus{
	AccountDraft:    {AccountActive},
	AccountActive:   {AccountInactive},
	AccountInactive: {AccountActive, AccountClosed},
}

// CanTransition reports whether a bank account may move from one status to another.
func CanTransition(from, to AccountStatus) bool {
	for _, next := range accountTransitions[from] {
		if next == to {
			return true
		}
	}
	return false
}

var (
	bicPattern         = regexp.MustCompile(`^[A-Z]{4}[A-Z]{2}[A-Z0-9]{2}([A-Z0-9]{3})?$`)
	ibanShape          = regexp.MustCompile(`^[A-Z]{2}[0-9]{2}[A-Z0-9]{11,30}$`)
	accountCodePattern = regexp.MustCompile(`^[A-Z0-9-]{1,20}$`)
	currencyPattern    = regexp.MustCompile(`^[A-Z]{3}$`)
)

// NormalizeIBAN strips spaces and upper-cases an IBAN.
func NormalizeIBAN(raw string) string {
	return strings.ToUpper(strings.Join(strings.Fields(raw), ""))
}

// ValidateIBAN checks the ISO 13616 shape and the mod-97 check digits (TRS-0401).
func ValidateIBAN(raw string) error {
	iban := NormalizeIBAN(raw)
	if len(iban) < 15 || len(iban) > 34 || !ibanShape.MatchString(iban) {
		return fmt.Errorf("IBAN has an invalid shape")
	}
	rearranged := iban[4:] + iban[:4]
	rem := 0
	for _, r := range rearranged {
		switch {
		case r >= '0' && r <= '9':
			rem = (rem*10 + int(r-'0')) % 97
		case r >= 'A' && r <= 'Z':
			rem = (rem*100 + int(r-'A') + 10) % 97
		default:
			return fmt.Errorf("IBAN contains an invalid character")
		}
	}
	if rem != 1 {
		return fmt.Errorf("IBAN fails the mod-97 check")
	}
	return nil
}

// MaskIBAN keeps the country code and the last four characters; everything
// else is replaced so the full number never reaches the API or the database.
func MaskIBAN(raw string) string {
	iban := NormalizeIBAN(raw)
	if len(iban) <= 6 {
		return strings.Repeat("*", len(iban))
	}
	return iban[:2] + strings.Repeat("*", len(iban)-6) + iban[len(iban)-4:]
}

// Last4 returns the final four characters of an IBAN, used for display.
func Last4(raw string) string {
	iban := NormalizeIBAN(raw)
	if len(iban) < 4 {
		return iban
	}
	return iban[len(iban)-4:]
}

// ValidBIC checks ISO 9362 shape (8 or 11 characters).
func ValidBIC(s string) bool { return bicPattern.MatchString(strings.ToUpper(s)) }

// ValidAccountCode checks the per-entity account code shape.
func ValidAccountCode(s string) bool { return accountCodePattern.MatchString(s) }

// ValidCurrency checks an ISO 4217 alpha-3 shape.
func ValidCurrency(s string) bool { return currencyPattern.MatchString(s) }

// MaxAccountsPerEntity is the FR-36.1 cap on bank accounts per legal entity.
const MaxAccountsPerEntity = 500
