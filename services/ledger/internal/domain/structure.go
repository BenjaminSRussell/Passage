package domain

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// Account structure statuses (FR-11.3). Only active versions govern postings.
const (
	StructureDraft   = "draft"
	StructureActive  = "active"
	StructureRetired = "retired"

	// MainAccountSegment is the implicit first segment of every structure. Its
	// values are governed by the account filter; constraint rows may refer to it
	// in Under so a row applies to some accounts only (AX constraint tree).
	MainAccountSegment = "MAIN_ACCOUNT"

	// CodeStructureInvalid is raised when a structure definition is malformed.
	CodeStructureInvalid = "LEDGER_STRUCTURE_INVALID"
	// CodeStructureNotFound is raised when no active structure covers a line
	// (FR-11.3), and as 404 when a structure id is unknown.
	CodeStructureNotFound = "LEDGER_STRUCTURE_NOT_FOUND"
	// CodeStructureLocked is the 409 for a change to an active or retired version.
	CodeStructureLocked = "LEDGER_STRUCTURE_LOCKED"
	// CodeStructureVersionExists is the 409 for a duplicate (name, version).
	CodeStructureVersionExists = "LEDGER_STRUCTURE_VERSION_EXISTS"
	// CodeDimensionRequired and CodeDimensionInvalid are constraint failures (FR-11.3, BR-11.9).
	CodeDimensionRequired = "LEDGER_DIMENSION_REQUIRED"
	CodeDimensionInvalid  = "LEDGER_DIMENSION_INVALID"
	// CodeDimensionInactive is raised when a registered value is suspended or
	// outside its effective dates (FR-11.2).
	CodeDimensionInactive = "LEDGER_DIMENSION_INACTIVE"

	// tokenAny and tokenBlank are the wildcard tokens of filters and constraints.
	tokenAny   = "*"
	tokenBlank = "blank"

	maxStructureName  = 60
	maxDimensionName  = 60
	maxConstraintRows = 500
)

var structureNameRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,60}$`)

// Structure is one version of an account structure (FR-11.3). Segments name the
// ledger's dimension attributes in order after the implicit main account. The
// account filter is a list of tokens (exact, "from..to" range, or "*") that
// admits main accounts; empty admits every account. Constraint rows restrict a
// segment's values under the values of earlier segments (the data model's
// parent_path, written as Under).
type Structure struct {
	ID            string       `json:"id"`
	Name          string       `json:"name"`
	Version       int          `json:"version"`
	Priority      int          `json:"priority"`
	EffectiveFrom time.Time    `json:"effectiveFrom"`
	Status        string       `json:"status"`
	Segments      []string     `json:"segments"`
	AccountFilter []string     `json:"accountFilter"`
	Constraints   []Constraint `json:"constraints"`
}

// Constraint limits one segment. Under maps MAIN_ACCOUNT or earlier segments to
// the token their value must match for the row to apply: an exact value, a
// "from..to" range, "blank", or "*" (any value, blank included). Allowed lists
// the admitted tokens: exact values, ranges, "*" (any non-blank value) and
// "blank" (the segment may stay empty). Ranges compare as text.
type Constraint struct {
	Segment string            `json:"segment"`
	Under   map[string]string `json:"under,omitempty"`
	Allowed []string          `json:"allowed"`
}

// DimensionValue is a registered value of a dimension attribute with effective
// dates and suspension (FR-11.2). Unregistered values are format-checked only.
type DimensionValue struct {
	Attribute  string     `json:"attribute"`
	Value      string     `json:"value"`
	Name       string     `json:"name"`
	ActiveFrom *time.Time `json:"activeFrom,omitempty"`
	ActiveTo   *time.Time `json:"activeTo,omitempty"`
	Suspended  bool       `json:"suspended"`
}

// ValueKey is the map key of a dimension value.
func ValueKey(attribute, value string) string { return attribute + "|" + value }

// ActiveOn reports whether the value may be used on date (active_to inclusive).
func (v DimensionValue) ActiveOn(date time.Time) bool {
	if v.Suspended {
		return false
	}
	if v.ActiveFrom != nil && date.Before(*v.ActiveFrom) {
		return false
	}
	if v.ActiveTo != nil && date.After(*v.ActiveTo) {
		return false
	}
	return true
}

// ValidStructureStatus reports whether s is a known structure status.
func ValidStructureStatus(s string) bool {
	return s == StructureDraft || s == StructureActive || s == StructureRetired
}

// validToken reports whether tok is a well-formed filter or constraint token.
func validToken(tok string) bool {
	switch tok {
	case tokenAny, tokenBlank:
		return true
	}
	if from, to, ok := strings.Cut(tok, ".."); ok {
		return dimValueRe.MatchString(from) && dimValueRe.MatchString(to) && from <= to
	}
	return dimValueRe.MatchString(tok)
}

// tokenMatches reports whether an Allowed or filter token admits v ("" is blank).
func tokenMatches(tok, v string) bool {
	switch tok {
	case tokenBlank:
		return v == ""
	case tokenAny:
		return v != ""
	}
	if from, to, ok := strings.Cut(tok, ".."); ok {
		return v != "" && v >= from && v <= to
	}
	return v != "" && v == tok
}

func anyTokenMatches(tokens []string, v string) bool {
	for _, t := range tokens {
		if tokenMatches(t, v) {
			return true
		}
	}
	return false
}

// ValidateStructure checks a structure definition, not its use. ledgerAttrs are
// the ledger's dimension attributes; every segment must be one of them.
func ValidateStructure(s Structure, ledgerAttrs []string) []Violation {
	var out []Violation
	bad := func(field, msg string) {
		out = append(out, Violation{Code: CodeStructureInvalid, Field: field, Message: msg})
	}
	if !structureNameRe.MatchString(s.Name) {
		bad("name", fmt.Sprintf("must be 1 to %d characters of A-Z, a-z, 0-9, '.', '_' or '-'", maxStructureName))
	}
	if s.Version < 1 {
		bad("version", "must be at least 1")
	}
	if s.Priority < 0 {
		bad("priority", "must not be negative")
	}
	if !ValidStructureStatus(s.Status) {
		bad("status", "must be draft, active or retired")
	}
	known := make(map[string]bool, len(ledgerAttrs))
	for _, a := range ledgerAttrs {
		known[a] = true
	}
	// index holds each segment's position; the main account is position -1 so
	// every listed segment may be constrained under it.
	index := map[string]int{MainAccountSegment: -1}
	if len(s.Segments) > MaxDimensionAttributes {
		bad("segments", fmt.Sprintf("must list at most %d dimension attributes", MaxDimensionAttributes))
	}
	for i, seg := range s.Segments {
		switch {
		case seg == MainAccountSegment:
			bad("segments", "the main account is the implicit first segment and must not be listed")
			continue
		case !known[seg]:
			bad("segments", fmt.Sprintf("segment %s is not a dimension attribute of this ledger", seg))
		}
		if _, dup := index[seg]; dup {
			bad("segments", fmt.Sprintf("segment %s is listed twice", seg))
			continue
		}
		index[seg] = i
	}
	for _, tok := range s.AccountFilter {
		if tok == tokenBlank || !validToken(tok) {
			bad("accountFilter", fmt.Sprintf("token %q is not an account number, a from..to range or *", tok))
		}
	}
	if len(s.Constraints) > maxConstraintRows {
		bad("constraints", fmt.Sprintf("at most %d constraint rows", maxConstraintRows))
	}
	seen := map[string]int{}
	for i, c := range s.Constraints {
		field := fmt.Sprintf("constraints[%d]", i)
		segIdx, listed := index[c.Segment]
		if !listed || c.Segment == MainAccountSegment {
			bad(field+".segment", fmt.Sprintf("segment %q is not a dimension segment of the structure", c.Segment))
			continue
		}
		for attr, want := range c.Under {
			if prev, ok := index[attr]; !ok || prev >= segIdx {
				bad(field+".under", fmt.Sprintf("%s must be the main account or an earlier segment of the structure", attr))
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

// constraintKey identifies a row by its segment and conditions; two rows with
// the same key would make the governing row ambiguous.
func constraintKey(c Constraint) string {
	parts := make([]string, 0, len(c.Under))
	for attr, want := range c.Under {
		if want != tokenAny {
			parts = append(parts, attr+"="+want)
		}
	}
	sort.Strings(parts)
	return c.Segment + "|" + strings.Join(parts, ",")
}

// accountMatches reports whether a structure's account filter admits account.
func accountMatches(filter []string, account string) bool {
	if len(filter) == 0 {
		return true
	}
	return anyTokenMatches(filter, account)
}

// EffectiveVersions returns, per structure name, the active version in effect
// on date: the one with the latest effective_from on or before date, the
// highest version number breaking a tie (FR-11.3). The result is ordered by
// priority, then name, which is the order structures are tried in.
func EffectiveVersions(structs []Structure, date time.Time) []Structure {
	byName := map[string]Structure{}
	for _, s := range structs {
		if s.Status != StructureActive || s.EffectiveFrom.After(date) {
			continue
		}
		cur, ok := byName[s.Name]
		if !ok || s.EffectiveFrom.After(cur.EffectiveFrom) || (s.EffectiveFrom.Equal(cur.EffectiveFrom) && s.Version > cur.Version) {
			byName[s.Name] = s
		}
	}
	out := make([]Structure, 0, len(byName))
	for _, s := range byName {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Priority != out[j].Priority {
			return out[i].Priority < out[j].Priority
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// SelectStructure returns the structure version that governs account on date
// (FR-11.3): of the versions in effect, the first (by priority, then name)
// whose main-account filter admits the account. A later version of a structure
// replaces the earlier one entirely, so narrowing its filter never falls back
// to the superseded version. Advanced rules (FR-11.16) are not applied here.
func SelectStructure(structs []Structure, account string, date time.Time) (Structure, bool) {
	for _, s := range EffectiveVersions(structs, date) {
		if accountMatches(s.AccountFilter, account) {
			return s, true
		}
	}
	return Structure{}, false
}

// governingConstraint picks the row that applies to seg: among rows whose Under
// conditions match the line, the most specific (most conditions other than
// "*") wins; ties keep the earlier row. ok is false when no row applies, which
// leaves the segment free (any value or blank).
func governingConstraint(cs []Constraint, seg string, values map[string]string) (Constraint, bool) {
	var best Constraint
	found, bestSpec := false, -1
	for _, c := range cs {
		if c.Segment != seg {
			continue
		}
		spec, ok := underSpecificity(c.Under, values)
		if ok && spec > bestSpec {
			best, found, bestSpec = c, true, spec
		}
	}
	return best, found
}

func underSpecificity(under map[string]string, values map[string]string) (int, bool) {
	spec := 0
	for attr, want := range under {
		if want == tokenAny {
			continue
		}
		if !tokenMatches(want, values[attr]) {
			return 0, false
		}
		spec++
	}
	return spec, true
}

// SegmentFault is one structure failure on a line. Segment is empty for a
// fault on the line as a whole.
type SegmentFault struct {
	Segment string
	Code    string
	Message string
}

// CheckSegments applies a structure to one line. A dimension that is not a
// segment of the structure is DIMENSION_INVALID (the structure defines which
// dimensions an account carries). For each segment, the governing row decides:
// a blank it does not allow is DIMENSION_REQUIRED; a value outside its tokens
// is DIMENSION_INVALID.
func CheckSegments(s Structure, account string, dims map[string]string) []SegmentFault {
	var out []SegmentFault
	inStructure := make(map[string]bool, len(s.Segments))
	for _, seg := range s.Segments {
		inStructure[seg] = true
	}
	extra := make([]string, 0)
	for attr := range dims {
		if !inStructure[attr] {
			extra = append(extra, attr)
		}
	}
	sort.Strings(extra)
	for _, attr := range extra {
		out = append(out, SegmentFault{Segment: attr, Code: CodeDimensionInvalid,
			Message: fmt.Sprintf("dimension %s is not a segment of structure %s for account %s", attr, s.Name, account)})
	}
	values := make(map[string]string, len(dims)+1)
	for k, v := range dims {
		values[k] = v
	}
	values[MainAccountSegment] = account
	for _, seg := range s.Segments {
		c, ok := governingConstraint(s.Constraints, seg, values)
		if !ok {
			continue
		}
		v := dims[seg]
		if anyTokenMatches(c.Allowed, v) {
			continue
		}
		if v == "" {
			out = append(out, SegmentFault{Segment: seg, Code: CodeDimensionRequired,
				Message: fmt.Sprintf("dimension %s is required for account %s", seg, account)})
			continue
		}
		out = append(out, SegmentFault{Segment: seg, Code: CodeDimensionInvalid,
			Message: fmt.Sprintf("value %s is not allowed for %s under structure %s", v, seg, s.Name)})
	}
	return out
}

// checkStructure selects the structure that governs a line on date and checks
// the line's dimensions against it. It returns the line's violations (Field is
// the segment path) and the governing version's id. With no governing
// structure the line is refused with LEDGER_STRUCTURE_NOT_FOUND.
func checkStructure(structs []Structure, account string, dims map[string]string, date time.Time) ([]Violation, string) {
	s, found := SelectStructure(structs, account, date)
	if !found {
		return []Violation{{Code: CodeStructureNotFound, Field: "lines.account",
			Message: fmt.Sprintf("no active account structure covers account %s on %s", account, date.Format(time.DateOnly))}}, ""
	}
	var out []Violation
	for _, f := range CheckSegments(s, account, dims) {
		out = append(out, Violation{Code: f.Code, Field: "lines.dimensions." + f.Segment, Message: f.Message})
	}
	return out, s.ID
}

// StructureEditAllowed reports whether prev may be replaced by next. Drafts
// change freely (activation included). An active or retired version is
// immutable: it may only be re-sent unchanged, or an active one retired.
func StructureEditAllowed(prev, next Structure) bool {
	if prev.Status == StructureDraft {
		return true
	}
	if prev.Status != next.Status && !(prev.Status == StructureActive && next.Status == StructureRetired) {
		return false
	}
	return sameDefinition(prev, next)
}

// sameDefinition compares everything but id and status.
func sameDefinition(a, b Structure) bool {
	if a.Name != b.Name || a.Version != b.Version || a.Priority != b.Priority || !a.EffectiveFrom.Equal(b.EffectiveFrom) ||
		!equalStrings(a.Segments, b.Segments) || !equalStrings(a.AccountFilter, b.AccountFilter) || len(a.Constraints) != len(b.Constraints) {
		return false
	}
	for i := range a.Constraints {
		ca, cb := a.Constraints[i], b.Constraints[i]
		if ca.Segment != cb.Segment || !equalStrings(ca.Allowed, cb.Allowed) || len(ca.Under) != len(cb.Under) {
			return false
		}
		for k, v := range ca.Under {
			if w, ok := cb.Under[k]; !ok || w != v {
				return false
			}
		}
	}
	return true
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ValidateDimensionValue checks a register entry (attribute, value and dates).
func ValidateDimensionValue(v DimensionValue, ledgerAttrs []string) []Violation {
	var out []Violation
	known := false
	for _, a := range ledgerAttrs {
		if a == v.Attribute {
			known = true
		}
	}
	if !known {
		out = append(out, Violation{Code: CodeDimensionInvalid, Field: "attribute",
			Message: fmt.Sprintf("dimension %s is not defined on this ledger", v.Attribute)})
	}
	if !dimValueRe.MatchString(v.Value) {
		out = append(out, Violation{Code: CodeDimensionInvalid, Field: "value",
			Message: "must be 1 to 20 characters of A-Z, a-z, 0-9, '.', '_' or '-'"})
	}
	if utf8.RuneCountInString(v.Name) > maxDimensionName {
		out = append(out, Violation{Code: CodeDimensionInvalid, Field: "name", Message: fmt.Sprintf("must be at most %d characters", maxDimensionName)})
	}
	if v.ActiveFrom != nil && v.ActiveTo != nil && v.ActiveTo.Before(*v.ActiveFrom) {
		out = append(out, Violation{Code: CodeDimensionInvalid, Field: "activeTo", Message: "must not be before activeFrom"})
	}
	return out
}
