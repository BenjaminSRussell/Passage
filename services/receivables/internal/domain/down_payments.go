package domain

import (
	"net/http"

	"github.com/shopspring/decimal"
)

// Down payments and security deposits (FR-30.21, BR-30.14). Pure functions, no I/O.
const (
	KindDownPayment     = "DOWN_PAYMENT"
	KindSecurityDeposit = "SECURITY_DEPOSIT"

	DownRequested = "REQUESTED"
	DownReceived  = "RECEIVED"
	DownCleared   = "CLEARED"
	DownReleased  = "RELEASED"
	DownCancelled = "CANCELLED"

	CodeDownPaymentState    = "AR-DOWN-PAYMENT-STATE"
	CodeDownPaymentMismatch = "AR-DOWN-PAYMENT-MISMATCH"
	// CodeClearingExceeded is AR-0017: clearing exceeds the remaining down payment.
	CodeClearingExceeded = "AR-0017"
)

// Accounts from §5.30.13 (down_payment_account / security_deposit_account) and
// the receivables control account. The bank account comes from the ledger's
// bank setup; 1000 is the chart default the rest of the service names.
const (
	AccountBank            = "1000"
	AccountReceivable      = "1200"
	AccountDownPayment     = "2190"
	AccountSecurityDeposit = "2195"
)

// Posting is one debit or credit line an event hands to the ledger.
type Posting struct {
	Account string          `json:"account"`
	Debit   decimal.Decimal `json:"debit"`
	Credit  decimal.Decimal `json:"credit"`
}

// ValidDownPaymentKind reports whether k is DOWN_PAYMENT or SECURITY_DEPOSIT.
func ValidDownPaymentKind(k string) bool { return k == KindDownPayment || k == KindSecurityDeposit }

// LiabilityAccount is 2190 for a down payment and 2195 for a security deposit.
func LiabilityAccount(kind string) string {
	if kind == KindSecurityDeposit {
		return AccountSecurityDeposit
	}
	return AccountDownPayment
}

func pair(debit, credit string, amount decimal.Decimal) []Posting {
	return []Posting{{Account: debit, Debit: amount, Credit: decimal.Zero}, {Account: credit, Debit: decimal.Zero, Credit: amount}}
}

// ReceiptPostings: money received, Dr bank / Cr 2190 or 2195.
func ReceiptPostings(kind string, amount decimal.Decimal) []Posting {
	return pair(AccountBank, LiabilityAccount(kind), amount)
}

// ClearingPostings: applied to an open item, Dr 2190 or 2195 / Cr 1200 (AC-30.15).
func ClearingPostings(kind string, amount decimal.Decimal) []Posting {
	return pair(LiabilityAccount(kind), AccountReceivable, amount)
}

// ReleasePostings: a deposit's remainder refunded, Dr 2195 / Cr bank.
func ReleasePostings(kind string, amount decimal.Decimal) []Posting {
	return pair(LiabilityAccount(kind), AccountBank, amount)
}

// ClearingProposal is the most that can clear against an item: min(remaining,
// item open), never negative (BR-30.14 proposes up to min(remaining, invoice gross);
// the open amount bounds it once part of the invoice is settled).
func ClearingProposal(remaining, itemOpen decimal.Decimal) decimal.Decimal {
	if !remaining.IsPositive() || !itemOpen.IsPositive() {
		return decimal.Zero
	}
	return decimal.Min(remaining, itemOpen)
}

// ClearingAmount resolves a requested clearing. With no request it is the
// proposal; a request must be positive and at most the proposal (AR-0017).
func ClearingAmount(remaining, itemOpen decimal.Decimal, requested *decimal.Decimal) (decimal.Decimal, *Error) {
	most := ClearingProposal(remaining, itemOpen)
	amount := most
	if requested != nil {
		amount = *requested
	}
	if !amount.IsPositive() {
		return decimal.Zero, &Error{Status: http.StatusUnprocessableEntity, Code: CodeClearingExceeded, Detail: "there is nothing to clear: the down payment or the item has no open amount"}
	}
	if amount.GreaterThan(most) {
		return decimal.Zero, &Error{Status: http.StatusUnprocessableEntity, Code: CodeClearingExceeded,
			Detail: "clearing " + amount.String() + " exceeds the remaining down payment or the item's open amount; at most " + most.String()}
	}
	return amount, nil
}

// ClearableSource reports whether an open item of sourceType can take a
// clearing of kind: a down payment clears its final invoice; a security deposit
// may be offset against any debit item (invoice, fee or interest).
func ClearableSource(kind, sourceType string) bool {
	if kind == KindDownPayment {
		return sourceType == "INVOICE"
	}
	return sourceType == "INVOICE" || sourceType == "FEE" || sourceType == "INTEREST"
}

// ReceiveAllowed reports whether a request can be recorded as received. A
// received item can then be cleared until nothing remains, and a received
// deposit released.
func ReceiveAllowed(status string) bool { return status == DownRequested }

// CancelAllowed reports whether a request can be withdrawn (nothing was received).
func CancelAllowed(status string) bool { return status == DownRequested }

// ClearAllowed reports whether a down payment or deposit can clear an item.
func ClearAllowed(status string) bool { return status == DownReceived }

// ReleaseAllowed reports whether a deposit's remainder can be refunded.
func ReleaseAllowed(kind, status string) bool {
	return kind == KindSecurityDeposit && status == DownReceived
}

// StatusAfterClearing is CLEARED once nothing remains, else RECEIVED.
func StatusAfterClearing(amount, cleared decimal.Decimal) string {
	if amount.Sub(cleared).IsPositive() {
		return DownReceived
	}
	return DownCleared
}
