package domain

import (
	"fmt"
	"slices"
	"sort"
	"time"
)

// Advanced rule structures (FR-11.16, AC-11.18). An advanced rule belongs to one account
// structure version. When a line matches its filter, the rule's segments and constraint
// rows are appended to the structure that governs the line.
const (
	// CodeAdvancedRuleInvalid is the 422 for a malformed advanced rule.
	CodeAdvancedRuleInvalid = "LEDGER_ADVANCED_RULE_INVALID"
	// CodeAdvancedRuleNotFound is the 404 for an unknown advanced rule.
	CodeAdvancedRuleNotFound = "LEDGER_ADVANCED_RULE_NOT_FOUND"
	// CodeAdvancedRuleDuplicate is the 409 for a name used twice in one structure version.
	CodeAdvancedRuleDuplicate = "LEDGER_ADVANCED_RULE_NAME_DUPLICATE"
	// CodeAdvancedRuleSegmentLimit is raised when the segments of a structure and its matching
	// rules exceed 20 attributes (a posting is refused; a definition is refused at save).
	CodeAdvancedRuleSegmentLimit = "LEDGER_ADVANCED_RULE_SEGMENT_LIMIT"
	// CodeAdvancedRuleLimit is the 409 for a structure version that already has MaxAdvancedRules rules.
	CodeAdvancedRuleLimit = "LEDGER_ADVANCED_RULE_LIMIT"

	// MaxAdvancedRules is the number of advanced rules one structure version may carry.
	MaxAdvancedRules = 100
	// maxAdvancedFilterTokens bounds the account tokens of one rule.
	maxAdvancedFilterTokens = 50
)

// AdvancedRule is the filter and the appended structure of one advanced rule. Accounts are
// main account tokens (exact, from..to, "*"); Dimensions map an attribute to a token
// (exact, range, "*" for any value, "blank"); every listed condition must match. Segments
// are added after the structure's own; Constraints are rows over those segments, with the
// same Under semantics as the structure's.
type AdvancedRule struct {
	ID          string            `json:"id"`
	LedgerID    string            `json:"ledgerId"`
	StructureID string            `json:"structureId"`
	Name        string            `json:"name"`
	Priority    int               `json:"priority"`
	Accounts    []string          `json:"accounts"`
	Dimensions  map[string]string `json:"dimensions"`
	Segments    []string          `json:"segments"`
	Constraints []Constraint      `json:"constraints"`
	LockVersion int               `json:"lockVersion"`
	CreatedAt   time.Time         `json:"createdAt"`
	UpdatedAt   time.Time         `json:"updatedAt"`
}

// ValidateAdvancedRule checks a rule against its structure and the ledger's attributes.
func ValidateAdvancedRule(r AdvancedRule, st Structure, ledgerAttrs []string) []Violation {
	var out []Violation
	bad := func(field, msg string) {
		out = append(out, Violation{Code: CodeAdvancedRuleInvalid, Field: field, Message: msg})
	}
	known := map[string]bool{}
	for _, a := range ledgerAttrs {
		known[a] = true
	}
	if !structureNameRe.MatchString(r.Name) {
		bad("name", "must be 1 to 60 characters of A-Z, a-z, 0-9, '.', '_' or '-'")
	}
	if r.Priority < 0 {
		bad("priority", "must not be negative")
	}
	if len(r.Accounts) > maxAdvancedFilterTokens {
		bad("accounts", fmt.Sprintf("at most %d tokens", maxAdvancedFilterTokens))
	}
	for _, tok := range r.Accounts {
		if tok == tokenBlank || !validToken(tok) {
			bad("accounts", fmt.Sprintf("token %q is not an account number, a from..to range or *", tok))
		}
	}
	for attr, tok := range r.Dimensions {
		if !known[attr] {
			bad("dimensions", fmt.Sprintf("%s is not a dimension attribute of this ledger", attr))
		}
		if !validToken(tok) {
			bad("dimensions", fmt.Sprintf("token %q for %s is not valid", tok, attr))
		}
	}
	if len(r.Accounts) == 0 && len(r.Dimensions) == 0 {
		bad("accounts", "the filter needs at least one account token or dimension condition")
	}
	if len(r.Segments) == 0 {
		bad("segments", "must add at least one segment")
	}
	own := map[string]bool{}
	for _, seg := range r.Segments {
		switch {
		case !known[seg]:
			bad("segments", fmt.Sprintf("segment %s is not a dimension attribute of this ledger", seg))
		case slices.Contains(st.Segments, seg):
			bad("segments", fmt.Sprintf("segment %s is already a segment of structure %s", seg, st.Name))
		case own[seg]:
			bad("segments", fmt.Sprintf("segment %s is listed twice", seg))
		}
		own[seg] = true
	}
	if len(st.Segments)+len(r.Segments) > MaxDimensionAttributes {
		out = append(out, Violation{Code: CodeAdvancedRuleSegmentLimit, Field: "segments",
			Message: fmt.Sprintf("structure %s and this rule would have more than %d segments", st.Name, MaxDimensionAttributes)})
	}
	if len(r.Constraints) > maxConstraintRows {
		bad("constraints", fmt.Sprintf("at most %d constraint rows", maxConstraintRows))
	}
	seen := map[string]int{}
	for i, c := range r.Constraints {
		field := fmt.Sprintf("constraints[%d]", i)
		if !own[c.Segment] {
			bad(field+".segment", fmt.Sprintf("segment %q is not one of the segments this rule adds", c.Segment))
			continue
		}
		for attr, want := range c.Under {
			if attr != MainAccountSegment && !known[attr] {
				bad(field+".under", fmt.Sprintf("%s must be the main account or a dimension attribute", attr))
			}
			if attr == c.Segment {
				bad(field+".under", "a row cannot be conditioned on its own segment")
			}
			if !validToken(want) {
				bad(field+".under", fmt.Sprintf("token %q for %s is not valid", want, attr))
			}
		}
		if len(c.Allowed) == 0 {
			bad(field+".allowed", "must list at least one allowed token")
		}
		for _, tok := range c.Allowed {
			if !validToken(tok) {
				bad(field+".allowed", fmt.Sprintf("token %q is not valid", tok))
			}
		}
		key := constraintKey(c)
		if j, dup := seen[key]; dup {
			bad(field, fmt.Sprintf("repeats the segment and conditions of constraints[%d]", j))
		} else {
			seen[key] = i
		}
	}
	return out
}

// Matches reports whether the rule's filter admits a line on account with dims.
func (r AdvancedRule) Matches(account string, dims map[string]string) bool {
	if len(r.Accounts) > 0 && !anyTokenMatches(r.Accounts, account) {
		return false
	}
	for attr, tok := range r.Dimensions {
		if !tokenMatches(tok, dims[attr]) {
			return false
		}
	}
	return true
}

// WithAdvancedRules returns s extended by every advanced rule that matches the line, in
// priority order (then name): their segments follow the structure's own and their constraint
// rows follow its rows, so the structure's rows win a tie in specificity. ok is false when
// the combined segment list exceeds 20 attributes.
func (s Structure) WithAdvancedRules(account string, dims map[string]string) (Structure, bool) {
	var matched []AdvancedRule
	for _, r := range s.AdvancedRules {
		if r.Matches(account, dims) {
			matched = append(matched, r)
		}
	}
	if len(matched) == 0 {
		return s, true
	}
	sort.SliceStable(matched, func(i, j int) bool {
		if matched[i].Priority != matched[j].Priority {
			return matched[i].Priority < matched[j].Priority
		}
		return matched[i].Name < matched[j].Name
	})
	out := s
	out.Segments = slices.Clone(s.Segments)
	out.Constraints = slices.Clone(s.Constraints)
	for _, r := range matched {
		for _, seg := range r.Segments {
			if !slices.Contains(out.Segments, seg) {
				out.Segments = append(out.Segments, seg)
			}
		}
		out.Constraints = append(out.Constraints, r.Constraints...)
	}
	return out, len(out.Segments) <= MaxDimensionAttributes
}
