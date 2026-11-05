package domain

import (
	"fmt"
	"net/http"
	"slices"

	"github.com/shopspring/decimal"
)

// Deduction states (BR-30.12).
const (
	DeductionOpen       = "OPEN"
	DeductionMatched    = "MATCHED"
	DeductionApproved   = "APPROVED"
	DeductionDenied     = "DENIED"
	DeductionSplit      = "SPLIT"
	DeductionWrittenOff = "WRITTEN_OFF"
)

// Deduction error codes. AR-0014 is the catalog's; the others name rules the
// catalog has no number for.
const (
	CodeDeductionExceedsClaim = "AR-0014"
	CodeDeductionState        = "AR-DEDUCTION-STATE"
	CodeDeductionSplit        = "AR-DEDUCTION-SPLIT"
	CodeDeductionMismatch     = "AR-DEDUCTION-MISMATCH"
	// CodeDeductionWriteOffLimit reuses the catalog's write-off tolerance code
	// (AR-0006): above deduction_write_off_limit needs receivables.write-off.approve.
	CodeDeductionWriteOffLimit = CodeWriteOffTolerance
)

// AccountDeductionWriteOff is BR-30.12's 6110 Deductions written off.
const AccountDeductionWriteOff = "6110"

var (
	// DeductionTypes is the default deduction_types code list (§5.30.13).
	DeductionTypes = []string{"PROMO", "PRICE", "SHORT_SHIP", "DAMAGE", "UNKNOWN"}
	// DenialReasons is the default denial_reasons code list (§5.30.13).
	DenialReasons = []string{"NOT_AUTHORIZED", "EXPIRED", "DUPLICATE"}
	// ClaimTypes are the wave 26 claims a deduction can match (FR-30.17).
	ClaimTypes = []string{"REBATE_ACCRUAL", "PROMOTION_FUND", "CHARGEBACK"}
)

// ValidDeductionType reports whether t is a configured deduction type.
func ValidDeductionType(t string) bool { return slices.Contains(DeductionTypes, t) }

// ValidDenialReason reports whether r is a configured denial reason.
func ValidDenialReason(r string) bool { return slices.Contains(DenialReasons, r) }

// ValidClaimType reports whether t is a claim kind a deduction can match.
func ValidClaimType(t string) bool { return slices.Contains(ClaimTypes, t) }

// ValidDeductionStatus reports whether s is a deduction state.
func ValidDeductionStatus(s string) bool {
	switch s {
	case DeductionOpen, DeductionMatched, DeductionApproved, DeductionDenied, DeductionSplit, DeductionWrittenOff:
		return true
	}
	return false
}

// DeductionWriteOffLimit is deduction_write_off_limit (BR-30.12 defaults: 50.00
// USD, 45.00 EUR). Other currencies have no default, so their limit is zero and
// every write-off needs receivables.write-off.approve.
func DeductionWriteOffLimit(currency string) decimal.Decimal {
	switch currency {
	case "USD":
		return decimal.RequireFromString("50.00")
	case "EUR":
		return decimal.RequireFromString("45.00")
	default:
		return decimal.Zero
	}
}

// The workbench moves (BR-30.12). A deduction is worked while OPEN or MATCHED;
// matching and splitting happen before a claim is attached, approval only after.
var deductionMoves = map[string][]string{
	"match":     {DeductionOpen},
	"approve":   {DeductionMatched},
	"deny":      {DeductionOpen, DeductionMatched},
	"split":     {DeductionOpen},
	"write-off": {DeductionOpen, DeductionMatched},
}

// DeductionActions are the custom methods of /deductions/{id}.
var DeductionActions = []string{"match", "approve", "deny", "split", "write-off"}

// CheckDeductionMove returns AR-DEDUCTION-STATE when action is not allowed from status.
func CheckDeductionMove(status, action string) error {
	from, ok := deductionMoves[action]
	if !ok {
		return &Error{Status: http.StatusNotFound, Code: CodeDeductionState, Detail: "unknown deduction action " + action}
	}
	if !slices.Contains(from, status) {
		return &Error{Status: http.StatusConflict, Code: CodeDeductionState,
			Detail: fmt.Sprintf("the deduction is %s; %s needs %v", status, action, from)}
	}
	return nil
}

// CheckClaimCovers enforces that approval cannot exceed the claim's remaining
// amount (BR-30.12, AR-0014): the whole deduction must fit the claim, or the
// clerk splits it and denies or writes off the rest.
func CheckClaimCovers(amount, claimRemaining decimal.Decimal) error {
	if amount.GreaterThan(claimRemaining) {
		return &Error{Status: http.StatusUnprocessableEntity, Code: CodeDeductionExceedsClaim,
			Detail: fmt.Sprintf("Deduction approval %s exceeds claim remaining %s; split or deny the remainder", amount, claimRemaining)}
	}
	return nil
}

// CheckSplit requires at least two positive parts that sum exactly to the parent (BR-30.12).
func CheckSplit(parent decimal.Decimal, parts []decimal.Decimal) error {
	if len(parts) < 2 {
		return &Error{Status: http.StatusUnprocessableEntity, Code: CodeDeductionSplit, Detail: "a split needs at least two parts"}
	}
	sum := decimal.Zero
	for _, p := range parts {
		if !p.IsPositive() {
			return &Error{Status: http.StatusUnprocessableEntity, Code: CodeDeductionSplit, Detail: "every part must be above zero"}
		}
		sum = sum.Add(p)
	}
	if !sum.Equal(parent) {
		return &Error{Status: http.StatusUnprocessableEntity, Code: CodeDeductionSplit,
			Detail: fmt.Sprintf("the parts sum to %s; they must sum to the deduction's %s", sum, parent)}
	}
	return nil
}

// CheckDeductionWriteOff allows a write-off up to deduction_write_off_limit, and
// above it only with receivables.write-off.approve (BR-30.12, §5.30.17).
func CheckDeductionWriteOff(amount decimal.Decimal, currency string, mayApprove bool) error {
	limit := DeductionWriteOffLimit(currency)
	if amount.GreaterThan(limit) && !mayApprove {
		return &Error{Status: http.StatusForbidden, Code: CodeDeductionWriteOffLimit,
			Detail: fmt.Sprintf("Write-off %s %s exceeds the deduction limit %s; it needs receivables.write-off.approve", amount, currency, limit)}
	}
	return nil
}

// DeductionWriteOffPostings are the ledger lines of a deduction write-off.
func DeductionWriteOffPostings(amount decimal.Decimal) []Posting {
	return []Posting{
		{Account: AccountDeductionWriteOff, Debit: amount, Credit: decimal.Zero},
		{Account: AccountReceivable, Debit: decimal.Zero, Credit: amount},
	}
}
