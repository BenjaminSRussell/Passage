package domain

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// AuditRuleDuplicate is the rule type of the vendor-invoice duplicate check (FR-48.14).
const AuditRuleDuplicate = "DUPLICATE"

// Duplicate rule constants (BR-48.14).
const (
	dupAmountFloor = "0.01"
	dupAmountPct   = "0.005"
	dupWindowDays  = 30
	dupMaxEditDist = 1
)

var (
	nonAlnum     = regexp.MustCompile(`[^A-Za-z0-9]`)
	leadingZeros = regexp.MustCompile(`(^|[^0-9])0+([0-9])`)
)

// Invoice is the subset of a vendor invoice the duplicate rule reads.
type Invoice struct {
	ID          string          `json:"id"`
	VendorID    string          `json:"vendorId"`
	Number      string          `json:"number"`
	Amount      decimal.Decimal `json:"amount"`
	Currency    string          `json:"currency"`
	InvoiceDate time.Time       `json:"invoiceDate"`
	// PostedCredit marks a posted credit note; those are excluded (BR-48.14).
	PostedCredit bool `json:"postedCredit"`
}

// DuplicatePair is one match of the DUPLICATE rule. Original is the earlier
// invoice (by invoice date, then id) and Duplicate the later one, so a pair has
// one canonical form.
type DuplicatePair struct {
	Original  string `json:"original"`
	Duplicate string `json:"duplicate"`
	VendorID  string `json:"vendorId"`
}

// DuplicateViolation is one flagged document: the later invoice of one or more
// pairs, with the earlier invoices it repeats. A run writes one audit_violation
// per flagged document, keyed by ViolationKey (FR-48.14).
type DuplicateViolation struct {
	DocumentID  string   `json:"documentId"`
	VendorID    string   `json:"vendorId"`
	DuplicateOf []string `json:"duplicateOf"`
}

// NormalizeInvoiceNumber strips non-alphanumerics, upper-cases and removes the leading
// zeros of every digit run (BR-48.14), so INV-0042 becomes INV42. A run of zeros keeps
// one zero.
func NormalizeInvoiceNumber(s string) string {
	s = strings.ToUpper(nonAlnum.ReplaceAllString(s, ""))
	return leadingZeros.ReplaceAllString(s, "${1}${2}")
}

// Levenshtein returns the edit distance between a and b over runes.
func Levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

// AmountTolerance is max(0.01, 0.5% of the larger absolute amount).
func AmountTolerance(a, b decimal.Decimal) decimal.Decimal {
	floor := decimal.RequireFromString(dupAmountFloor)
	pct := decimal.RequireFromString(dupAmountPct)
	larger := decimal.Max(a.Abs(), b.Abs())
	if t := larger.Mul(pct); t.GreaterThan(floor) {
		return t
	}
	return floor
}

// IsDuplicate applies BR-48.14 to two invoices: same vendor and currency, amounts
// within tolerance, dates within 30 days, normalized numbers equal or one edit apart.
// Posted credit notes never match, and an invoice without a usable number (empty
// once normalized) is not compared on number at all.
func IsDuplicate(a, b Invoice) bool {
	if a.PostedCredit || b.PostedCredit || a.ID == b.ID {
		return false
	}
	if a.VendorID != b.VendorID || a.Currency != b.Currency {
		return false
	}
	if a.Amount.Sub(b.Amount).Abs().GreaterThan(AmountTolerance(a.Amount, b.Amount)) {
		return false
	}
	if a.InvoiceDate.Sub(b.InvoiceDate).Abs() > dupWindowDays*24*time.Hour {
		return false
	}
	na, nb := NormalizeInvoiceNumber(a.Number), NormalizeInvoiceNumber(b.Number)
	if na == "" || nb == "" {
		return false
	}
	return Levenshtein(na, nb) <= dupMaxEditDist
}

// earlier orders invoices by invoice date, then id.
func earlier(a, b Invoice) bool {
	if !a.InvoiceDate.Equal(b.InvoiceDate) {
		return a.InvoiceDate.Before(b.InvoiceDate)
	}
	return a.ID < b.ID
}

// FindDuplicates returns every duplicate pair in invs. Invoices are compared only
// within one vendor and only inside the 30-day window, so a nightly run over a
// large vendor stays near linear. Output is sorted for stable reruns.
func FindDuplicates(invs []Invoice) []DuplicatePair {
	byVendor := map[string][]Invoice{}
	for _, inv := range invs {
		byVendor[inv.VendorID] = append(byVendor[inv.VendorID], inv)
	}
	window := dupWindowDays * 24 * time.Hour
	var out []DuplicatePair
	for vendor, group := range byVendor {
		sort.Slice(group, func(i, j int) bool { return earlier(group[i], group[j]) })
		for i := range group {
			for j := i + 1; j < len(group) && group[j].InvoiceDate.Sub(group[i].InvoiceDate) <= window; j++ {
				if IsDuplicate(group[i], group[j]) {
					out = append(out, DuplicatePair{Original: group[i].ID, Duplicate: group[j].ID, VendorID: vendor})
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Duplicate != out[j].Duplicate {
			return out[i].Duplicate < out[j].Duplicate
		}
		return out[i].Original < out[j].Original
	})
	return out
}

// DuplicateViolations folds pairs into one violation per later document, sorted
// by document id. An original invoice is never flagged for being repeated.
func DuplicateViolations(pairs []DuplicatePair) []DuplicateViolation {
	var out []DuplicateViolation
	index := map[string]int{}
	for _, p := range pairs {
		i, ok := index[p.Duplicate]
		if !ok {
			i = len(out)
			index[p.Duplicate] = i
			out = append(out, DuplicateViolation{DocumentID: p.Duplicate, VendorID: p.VendorID})
		}
		out[i].DuplicateOf = append(out[i].DuplicateOf, p.Original)
	}
	for i := range out {
		sort.Strings(out[i].DuplicateOf)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DocumentID < out[j].DocumentID })
	return out
}

// ViolationKey is the idempotency key of one violation: a rerun of the same policy
// version over the same document creates no new row (FR-48.14).
func ViolationKey(policyID string, policyVersion int, documentID string) string {
	return fmt.Sprintf("audit:%s:v%d:%s", policyID, policyVersion, documentID)
}

// GroupByVendor groups violations into one case per vendor (AC-48.13).
func GroupByVendor(violations []DuplicateViolation) map[string][]DuplicateViolation {
	out := map[string][]DuplicateViolation{}
	for _, v := range violations {
		out[v.VendorID] = append(out[v.VendorID], v)
	}
	return out
}
