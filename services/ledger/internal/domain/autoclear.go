package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"sort"
	"strings"

	"github.com/shopspring/decimal"
)

// Automatic clearing limits (FR-11.20).
const (
	MaxAutoCriteria = 5
	// MaxAutoItems caps the open items one rule reads in one run; the rest wait for the next run.
	MaxAutoItems = 100000
)

// Criteria fields a rule may group open items by. A dim.<ATTRIBUTE> criterion groups
// by that dimension value; document_id by the documentId of the posting request;
// amount_txn by the absolute amount in the ledger currency (so a debit and a credit
// of the same size meet); posting_date by posting month.
const (
	CriterionAssignment    = "assignment"
	CriterionSourceLineRef = "source_line_ref"
	CriterionDocumentID    = "document_id"
	CriterionAmount        = "amount_txn"
	CriterionPostingDate   = "posting_date"
)

var dimCriterionRe = regexp.MustCompile(`^dim\.[A-Z][A-Z0-9_]{0,19}$`)

// ValidCriterion reports whether c is a grouping field.
func ValidCriterion(c string) bool {
	switch c {
	case CriterionAssignment, CriterionSourceLineRef, CriterionDocumentID, CriterionAmount, CriterionPostingDate:
		return true
	}
	return dimCriterionRe.MatchString(c)
}

// AutoItem is the part of an open item an automatic rule looks at.
type AutoItem struct {
	ID            string
	Account       string
	Amount        decimal.Decimal // signed, debit positive
	PostingDate   string          // YYYY-MM-DD
	Assignment    string
	SourceLineRef string
	DocumentID    string
	Dimensions    map[string]string
}

// CriterionValue returns the value of item for one criterion; ok is false when
// the item has none (a blank assignment never matches anything).
func (it AutoItem) CriterionValue(c string) (string, bool) {
	var v string
	switch {
	case c == CriterionAssignment:
		v = it.Assignment
	case c == CriterionSourceLineRef:
		v = it.SourceLineRef
	case c == CriterionDocumentID:
		v = it.DocumentID
	case c == CriterionAmount:
		v = it.Amount.Abs().String()
	case c == CriterionPostingDate:
		if len(it.PostingDate) >= 7 {
			v = it.PostingDate[:7]
		}
	case strings.HasPrefix(c, "dim."):
		v = it.Dimensions[strings.TrimPrefix(c, "dim.")]
	}
	return v, v != ""
}

// AutoGroup is a set of two or more open items of one account that share every criterion value.
type AutoGroup struct {
	Account string
	Items   []AutoItem
	Hash    string
	Debit   decimal.Decimal
	Credit  decimal.Decimal
	Net     decimal.Decimal
	Latest  string // latest posting date
}

// GroupItems groups items by account and the rule's criteria, in order of first
// appearance. Items without a value for some criterion and groups of one item are
// returned as unmatched (the count of items that no group took).
func GroupItems(items []AutoItem, criteria []string) (groups []AutoGroup, unmatched int) {
	index := map[string]int{}
	byKey := [][]AutoItem{}
	for _, it := range items {
		parts := []string{it.Account}
		ok := true
		for _, c := range criteria {
			v, has := it.CriterionValue(c)
			if !has {
				ok = false
				break
			}
			parts = append(parts, v)
		}
		if !ok {
			unmatched++
			continue
		}
		key := strings.Join(parts, "\x1f")
		i, seen := index[key]
		if !seen {
			i = len(byKey)
			index[key] = i
			byKey = append(byKey, nil)
		}
		byKey[i] = append(byKey[i], it)
	}
	for _, members := range byKey {
		if len(members) < MinClearingItems {
			unmatched += len(members)
			continue
		}
		amounts := make([]decimal.Decimal, len(members))
		idsSorted := make([]string, len(members))
		latest := ""
		for i, m := range members {
			amounts[i], idsSorted[i] = m.Amount, m.ID
			if m.PostingDate > latest {
				latest = m.PostingDate
			}
		}
		sort.Strings(idsSorted)
		sum := sha256.Sum256([]byte(members[0].Account + "|" + strings.Join(idsSorted, ",")))
		g := AutoGroup{Account: members[0].Account, Items: members, Hash: hex.EncodeToString(sum[:16]), Latest: latest}
		g.Debit, g.Credit, g.Net = ClearingTotals(amounts)
		groups = append(groups, g)
	}
	return groups, unmatched
}

// AutoDecision says what a rule does with a group.
type AutoDecision struct {
	Clear   bool
	Reason  string // set when Clear is false
	Message string
}

// DecideGroup applies BR-11.12 to a group: it clears at zero net, and with a
// tolerance (set only when the rule allows it) when the residual is within it. An
// automatic run never creates a residual item.
func DecideGroup(g AutoGroup, tolerance *Tolerance) AutoDecision {
	switch {
	case len(g.Items) > MaxClearingItems:
		return AutoDecision{Reason: "LEDGER_AUTOCLEAR_GROUP_TOO_LARGE", Message: "the group has more than 10,000 items; clear it manually in parts"}
	case g.Net.IsZero():
		return AutoDecision{Clear: true}
	case tolerance != nil && WithinTolerance(g.Net, g.Debit, g.Credit, *tolerance):
		return AutoDecision{Clear: true}
	}
	return AutoDecision{Reason: "LEDGER_CLEARING_UNBALANCED", Message: "the group nets to " + g.Net.String() + ", outside the rule's tolerance"}
}
