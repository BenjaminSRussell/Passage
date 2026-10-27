package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode"

	"github.com/shopspring/decimal"
)

// Payer-identity rule weights and thresholds (BR-30.1 R4, R5, R7).
const (
	weightR4 = 15
	weightR5 = 10
	weightR7 = 20
	// r5Threshold is the Jaro-Winkler similarity at which a payer name counts as a match.
	r5Threshold = 0.92
)

// r7ShortPercent is the relative shortfall under which R7 applies (1% of the open amount).
var r7ShortPercent = decimal.RequireFromString("0.01")

// shortPayTolerance is the R7 absolute shortfall per currency (BR-30.1).
var shortPayTolerance = map[string]decimal.Decimal{
	"USD": decimal.NewFromInt(50),
	"EUR": decimal.NewFromInt(45),
}

// NormalizeName upper-cases a name, replaces punctuation with spaces and collapses
// whitespace, so that "Acme, Inc." and "ACME INC" compare equal.
func NormalizeName(s string) string {
	var b strings.Builder
	space := true
	for _, r := range strings.ToUpper(s) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			space = false
		case !space:
			b.WriteByte(' ')
			space = true
		}
	}
	return strings.TrimSpace(b.String())
}

// JaroWinkler returns the Jaro-Winkler similarity of two names in [0,1] over their
// normalized forms. Two empty names are identical; one empty name scores 0.
func JaroWinkler(a, b string) float64 {
	ra, rb := []rune(NormalizeName(a)), []rune(NormalizeName(b))
	if len(ra) == 0 || len(rb) == 0 {
		if len(ra) == 0 && len(rb) == 0 {
			return 1
		}
		return 0
	}
	window := max(len(ra), len(rb))/2 - 1
	if window < 0 {
		window = 0
	}
	matchA := make([]bool, len(ra))
	matchB := make([]bool, len(rb))
	matches := 0
	for i := range ra {
		lo := max(0, i-window)
		hi := min(i+window+1, len(rb))
		for j := lo; j < hi; j++ {
			if matchB[j] || ra[i] != rb[j] {
				continue
			}
			matchA[i], matchB[j] = true, true
			matches++
			break
		}
	}
	if matches == 0 {
		return 0
	}
	// Count mismatched matched characters in order; half of them are transpositions.
	mismatched := 0
	k := 0
	for i := range ra {
		if !matchA[i] {
			continue
		}
		for !matchB[k] {
			k++
		}
		if ra[i] != rb[k] {
			mismatched++
		}
		k++
	}
	m := float64(matches)
	t := float64(mismatched) / 2
	jaro := (m/float64(len(ra)) + m/float64(len(rb)) + (m-t)/m) / 3
	prefix := 0
	for prefix < len(ra) && prefix < len(rb) && prefix < 4 && ra[prefix] == rb[prefix] {
		prefix++
	}
	return jaro + float64(prefix)*0.1*(1-jaro)
}

// MaxPayerNameLength bounds a learned payer name, after normalization.
const MaxPayerNameLength = 200

// LearnableName is the normalized payer name stored for R5, cut to
// MaxPayerNameLength runes; "" means nothing to learn.
func LearnableName(s string) string {
	n := []rune(NormalizeName(s))
	if len(n) > MaxPayerNameLength {
		n = n[:MaxPayerNameLength]
	}
	return strings.TrimSpace(string(n))
}

// NormalizeAccount upper-cases a bank account identifier (IBAN, MICR routing and
// account, other id) and drops everything but letters and digits, so that
// "DE89 3704 0044 0532 0130 00" and "de89370400440532013000" are one account.
func NormalizeAccount(s string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(s) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// AccountHash is the payer account hash R4 compares: the hex SHA-256 of the
// normalized account. The account itself is never stored (§5.30.20: IBANs are
// personal data). It returns "" for an account with no letters or digits.
func AccountHash(account string) string {
	n := NormalizeAccount(account)
	if n == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(n))
	return hex.EncodeToString(sum[:])
}

// payerNameScores returns, per customer with learned payer names, the best
// Jaro-Winkler similarity between the payment's payer name and those names.
func payerNameScores(p Payment) map[string]float64 {
	out := make(map[string]float64, len(p.LearnedNames))
	for customer, names := range p.LearnedNames {
		best := 0.0
		for _, n := range names {
			if s := JaroWinkler(p.PayerName, n); s > best {
				best = s
			}
		}
		out[customer] = best
	}
	return out
}

// payerKnown reports R4: the payment's payer account hash is bound to the item's customer
// (learned when an earlier payment from that account was applied to the customer).
func payerKnown(p Payment, it Item) bool {
	return it.CustomerID != "" && p.PayerCustomers[it.CustomerID]
}

// payerNameMatches reports R5: the payer name matches a name learned for the item's customer.
func payerNameMatches(p Payment, it Item) bool {
	return it.CustomerID != "" && p.nameScores[it.CustomerID] >= r5Threshold
}

// shortPaid reports R7: the payment is short of a single open item by less than 1% of
// the item or the currency tolerance (50.00 USD / 45.00 EUR), and a short-pay reason
// is given. Without the reason the rule does not apply, because a short payment must
// be explained. The caller does not score it when R3 holds: a shortfall that is the
// available cash discount is explained by the discount.
func shortPaid(p Payment, it Item) bool {
	if strings.TrimSpace(p.ShortPayReason) == "" || !p.Amount.IsPositive() {
		return false
	}
	short := it.OpenAmount.Sub(p.Amount)
	if !short.IsPositive() {
		return false
	}
	if short.LessThan(it.OpenAmount.Mul(r7ShortPercent)) {
		return true
	}
	tol, ok := shortPayTolerance[it.Currency]
	return ok && short.LessThan(tol)
}
