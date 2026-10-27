package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"sort"

	"github.com/shopspring/decimal"
)

// Balance confirmation replies (FR-30.20). NO_REPLY is the state until a reply is recorded.
const (
	ReplyAgreed    = "AGREED"
	ReplyDisagreed = "DISAGREED"
	ReplyNone      = "NO_REPLY"

	// BalanceDifferenceReason is the reason on the DISPUTE case a disagreement opens (BR-30.13).
	BalanceDifferenceReason = "BALANCE_DIFFERENCE"
	// ConfirmationCaseOwner owns the DISPUTE cases a disagreement opens until a collector takes them.
	ConfirmationCaseOwner = "collections"

	CodeConfirmationReplied = "AR-CONFIRMATION-REPLIED"
	CodeConfirmationReply   = "AR-CONFIRMATION-REPLY"
)

// CustomerBalance is one customer and currency row of the as-of balance.
type CustomerBalance struct {
	CustomerID string
	Currency   string
	Balance    decimal.Decimal
}

// ValidReply reports whether r is a reply a customer can give.
func ValidReply(r string) bool { return r == ReplyAgreed || r == ReplyDisagreed }

// SelectConfirmations picks the balances to confirm (FR-30.20, BR-30.13). A
// non-zero balance qualifies when its absolute value reaches threshold; a zero
// balance qualifies only when includeZero is set. When sample > 0 the qualifying
// set is cut to sample rows ranked by a hash of seed, so a run is reproducible
// from its seed. The result is sorted by customer and currency.
func SelectConfirmations(balances []CustomerBalance, threshold decimal.Decimal, sample int, includeZero bool, seed string) []CustomerBalance {
	picked := make([]CustomerBalance, 0, len(balances))
	for _, b := range balances {
		if b.Balance.IsZero() {
			if includeZero {
				picked = append(picked, b)
			}
			continue
		}
		if b.Balance.Abs().GreaterThanOrEqual(threshold) {
			picked = append(picked, b)
		}
	}
	if sample > 0 && sample < len(picked) {
		rank := func(b CustomerBalance) string {
			sum := sha256.Sum256([]byte(seed + "|" + b.CustomerID + "|" + b.Currency))
			return hex.EncodeToString(sum[:])
		}
		sort.Slice(picked, func(i, j int) bool { return rank(picked[i]) < rank(picked[j]) })
		picked = picked[:sample]
	}
	sort.Slice(picked, func(i, j int) bool {
		if picked[i].CustomerID != picked[j].CustomerID {
			return picked[i].CustomerID < picked[j].CustomerID
		}
		return picked[i].Currency < picked[j].Currency
	})
	return picked
}

// ReplyBalance checks a reply against our balance and returns the customer's
// balance to record. AGREED confirms our balance, so a different stated balance
// is refused; DISAGREED needs the customer's balance.
func ReplyBalance(reply string, ours decimal.Decimal, theirs *decimal.Decimal) (decimal.Decimal, *Error) {
	switch reply {
	case ReplyAgreed:
		if theirs != nil && !theirs.Equal(ours) {
			return decimal.Zero, &Error{Status: http.StatusUnprocessableEntity, Code: CodeConfirmationReply,
				Detail: "an AGREED reply confirms our balance " + ours.String() + "; record a different balance as DISAGREED"}
		}
		return ours, nil
	case ReplyDisagreed:
		if theirs == nil {
			return decimal.Zero, &Error{Status: http.StatusBadRequest, Code: CodeConfirmationReply, Detail: "a DISAGREED reply needs the customer's balance"}
		}
		return *theirs, nil
	}
	return decimal.Zero, &Error{Status: http.StatusBadRequest, Code: CodeConfirmationReply, Detail: "reply must be AGREED or DISAGREED"}
}

// BalanceDifference is the customer's balance minus ours; zero means agreement.
func BalanceDifference(ours, theirs decimal.Decimal) decimal.Decimal { return theirs.Sub(ours) }
