package domain

import (
	"fmt"
	"sort"
	"time"
)

// Dimension derivation (FR-11.22, BR-11.14). A per-ledger strategy is an ordered
// list of steps that fill, copy, clear or set dimension values on a line before
// the account structure is validated. Evaluation is pure: the caller loads the
// strategy and the lookup tables valid on the posting date.

// Step kinds. The PRD's `expression` kind needs CEL and is not built (see NOT-IMPLEMENTED.md).
const (
	StepTableLookup = "table_lookup"
	StepMove        = "move"
	StepClear       = "clear"
	StepConstant    = "constant"
)

// Strategy statuses: one version per ledger is active at a time.
const (
	StrategyDraft   = "draft"
	StrategyActive  = "active"
	StrategyRetired = "retired"
)

// Condition operators of a step.
const (
	CondEq       = "eq"
	CondNe       = "ne"
	CondIn       = "in"
	CondBlank    = "blank"
	CondNotBlank = "notblank"
)

// Source field names a step may read besides the ledger's dimension attributes.
const (
	FieldAccount     = "ACCOUNT"
	FieldAccountType = "ACCOUNT_TYPE"
)

// Limits of FR-11.22 and BR-11.14.
const (
	MaxDerivationSteps    = 50
	MaxLookupSourceFields = 5
	MaxLookupRows         = 5000
	MaxConditionValues    = 20
	LookupWildcard        = "*"
	CodeDerivationInvalid = "LEDGER_DERIVATION_INVALID"
	maxLookupNameLength   = 80
	maxDerivationNameLen  = 80
	maxLookupValueLength  = 20
)

// StepCondition guards a step: the step runs only when the field's current value satisfies it.
type StepCondition struct {
	Field  string   `json:"field"`
	Op     string   `json:"op"`
	Values []string `json:"values,omitempty"`
}

// DerivationStep is one step of a strategy.
type DerivationStep struct {
	ID            string         `json:"id"`
	Sequence      int            `json:"sequence"`
	Kind          string         `json:"kind"`
	Condition     *StepCondition `json:"condition,omitempty"`
	SourceFields  []string       `json:"sourceFields"`
	Target        string         `json:"target"`
	LookupTableID string         `json:"lookupTableId,omitempty"`
	Value         string         `json:"value,omitempty"`
	Overwrite     bool           `json:"overwrite"`
}

// LookupRow is one row of a lookup table: a source tuple (exact values or `*`),
// the target value and the validity window (inclusive).
type LookupRow struct {
	Source    []string `json:"source"`
	Target    string   `json:"target"`
	ValidFrom string   `json:"validFrom"`
	ValidTo   *string  `json:"validTo,omitempty"`
}

// LookupTable maps a source tuple to one target dimension value.
type LookupTable struct {
	ID           string      `json:"id"`
	LedgerID     string      `json:"ledgerId"`
	Name         string      `json:"name"`
	SourceFields []string    `json:"sourceFields"`
	Target       string      `json:"target"`
	RowCount     int         `json:"rowCount"`
	Version      int         `json:"version"`
	Rows         []LookupRow `json:"rows,omitempty"`
	CreatedAt    time.Time   `json:"createdAt"`
	UpdatedAt    time.Time   `json:"updatedAt"`
}

// DerivationStrategy is one versioned strategy of a ledger.
type DerivationStrategy struct {
	ID       string `json:"id"`
	LedgerID string `json:"ledgerId"`
	Version  int    `json:"version"`
	// LockVersion is the optimistic-concurrency counter of the row (entity tag).
	LockVersion int              `json:"lockVersion"`
	Name        string           `json:"name"`
	Status      string           `json:"status"`
	Steps       []DerivationStep `json:"steps"`
	ActivatedBy string           `json:"activatedBy,omitempty"`
	ActivatedAt *time.Time       `json:"activatedAt,omitempty"`
	CreatedAt   time.Time        `json:"createdAt"`
	UpdatedAt   time.Time        `json:"updatedAt"`
}

// DerivationTrace records one applied step on one line (derivation trace, PRD §2.11.16).
type DerivationTrace struct {
	StepID     string   `json:"stepId"`
	Sequence   int      `json:"sequence"`
	Kind       string   `json:"kind"`
	Target     string   `json:"target"`
	Before     string   `json:"before"`
	After      string   `json:"after"`
	MatchedRow []string `json:"matchedRow,omitempty"`
}

// ValidLookupName reports whether name is a usable lookup table name.
func ValidLookupName(name string) bool {
	n := len([]rune(name))
	return n >= 1 && n <= maxLookupNameLength
}

// ValidDerivationName reports whether name is a usable strategy name.
func ValidDerivationName(name string) bool {
	n := len([]rune(name))
	return n >= 1 && n <= maxDerivationNameLen
}

func readable(field string, attrs map[string]bool) bool {
	return field == FieldAccount || field == FieldAccountType || attrs[field]
}

// ValidateLookupTable checks a lookup table definition and its rows against the
// ledger's dimension attributes. The result maps a field name to the problem.
func ValidateLookupTable(t LookupTable, attributes []string) map[string]string {
	attrs := attrSet(attributes)
	out := map[string]string{}
	if !ValidLookupName(t.Name) {
		out["name"] = "must be 1 to 80 characters"
	}
	if n := len(t.SourceFields); n < 1 || n > MaxLookupSourceFields {
		out["sourceFields"] = fmt.Sprintf("must list 1 to %d fields", MaxLookupSourceFields)
	}
	seen := map[string]bool{}
	for _, f := range t.SourceFields {
		switch {
		case !readable(f, attrs):
			out["sourceFields"] = fmt.Sprintf("%s is neither ACCOUNT, ACCOUNT_TYPE nor a dimension attribute of the ledger", f)
		case seen[f]:
			out["sourceFields"] = fmt.Sprintf("%s is listed twice", f)
		}
		seen[f] = true
	}
	if !attrs[t.Target] {
		out["target"] = "must be a dimension attribute of the ledger"
	} else if seen[t.Target] {
		out["target"] = "must not also be a source field"
	}
	if len(t.Rows) > MaxLookupRows {
		out["rows"] = fmt.Sprintf("at most %d rows per table", MaxLookupRows)
		return out
	}
	keys := map[string]bool{}
	for i, r := range t.Rows {
		field := fmt.Sprintf("rows[%d]", i)
		if len(r.Source) != len(t.SourceFields) {
			out[field] = fmt.Sprintf("source must have %d values", len(t.SourceFields))
			continue
		}
		for _, v := range r.Source {
			if v != LookupWildcard && !dimValueRe.MatchString(v) {
				out[field] = fmt.Sprintf("source value %q must be %s or 1 to %d letters, digits, dot, underscore or dash", v, LookupWildcard, maxLookupValueLength)
			}
		}
		if !dimValueRe.MatchString(r.Target) {
			out[field] = fmt.Sprintf("target %q must be 1 to %d letters, digits, dot, underscore or dash", r.Target, maxLookupValueLength)
		}
		from, err := time.Parse(time.DateOnly, r.ValidFrom)
		if err != nil {
			out[field] = "validFrom must be a date (YYYY-MM-DD)"
			continue
		}
		if r.ValidTo != nil {
			to, err := time.Parse(time.DateOnly, *r.ValidTo)
			if err != nil || to.Before(from) {
				out[field] = "validTo must be a date on or after validFrom"
				continue
			}
		}
		k := fmt.Sprintf("%q|%s", r.Source, r.ValidFrom)
		if keys[k] {
			out[field] = "duplicates an earlier row with the same source and validFrom"
		}
		keys[k] = true
	}
	return out
}

func attrSet(attributes []string) map[string]bool {
	m := make(map[string]bool, len(attributes))
	for _, a := range attributes {
		m[a] = true
	}
	return m
}

// ValidateStrategy checks the shape of a strategy (BR-11.14): at most 50 steps,
// unique step ids, known kinds, fields that exist, no step reading a field it
// writes, `clear` steps with overwrite. tables supplies the lookup tables (by id) for
// table_lookup steps; when nil, table references are not resolved (draft save).
// It returns one detail per problem; an empty result means the strategy can be activated.
func ValidateStrategy(s DerivationStrategy, attributes []string, tables map[string]LookupTable, resolveTables bool) []string {
	attrs := attrSet(attributes)
	var out []string
	add := func(i int, format string, args ...any) {
		out = append(out, fmt.Sprintf("step %d: ", i+1)+fmt.Sprintf(format, args...))
	}
	if len(s.Steps) == 0 {
		return []string{"a strategy needs at least one step"}
	}
	if len(s.Steps) > MaxDerivationSteps {
		return []string{fmt.Sprintf("a strategy has at most %d steps; this one has %d", MaxDerivationSteps, len(s.Steps))}
	}
	lastSeq := 0
	stepIDs := map[string]bool{}
	for i, st := range s.Steps {
		if st.ID != "" {
			if stepIDs[st.ID] {
				add(i, "id %s is used by an earlier step", st.ID)
			}
			stepIDs[st.ID] = true
		}
		if st.Sequence <= lastSeq {
			add(i, "sequence %d must be greater than the previous step's", st.Sequence)
		}
		lastSeq = st.Sequence
		if !attrs[st.Target] {
			add(i, "target %q is not a dimension attribute of the ledger", st.Target)
		}
		if c := st.Condition; c != nil {
			switch {
			case !readable(c.Field, attrs):
				add(i, "condition field %q is unknown", c.Field)
			case c.Field == st.Target:
				add(i, "the condition reads %s, which the step writes", c.Field)
			}
			switch c.Op {
			case CondEq, CondNe:
				if len(c.Values) != 1 {
					add(i, "condition %s needs exactly one value", c.Op)
				}
			case CondIn:
				if n := len(c.Values); n < 1 || n > MaxConditionValues {
					add(i, "condition in needs 1 to %d values", MaxConditionValues)
				}
			case CondBlank, CondNotBlank:
				if len(c.Values) != 0 {
					add(i, "condition %s takes no values", c.Op)
				}
			default:
				add(i, "condition operator %q must be eq, ne, in, blank or notblank", c.Op)
			}
		}
		for _, f := range st.SourceFields {
			if !readable(f, attrs) {
				add(i, "source field %q is unknown", f)
			}
			if f == st.Target {
				add(i, "the step reads %s, which it also writes", f)
			}
		}
		switch st.Kind {
		case StepTableLookup:
			if st.LookupTableID == "" {
				add(i, "table_lookup needs a lookup table")
				break
			}
			if !resolveTables {
				break
			}
			t, ok := tables[st.LookupTableID]
			switch {
			case !ok:
				add(i, "lookup table %s does not exist on this ledger", st.LookupTableID)
			case !sameStrings(t.SourceFields, st.SourceFields) || t.Target != st.Target:
				add(i, "sourceFields and target must equal those of lookup table %q (%v -> %s)", t.Name, t.SourceFields, t.Target)
			}
		case StepMove:
			if len(st.SourceFields) != 1 {
				add(i, "move reads exactly one source field")
			}
		case StepClear:
			if len(st.SourceFields) != 0 {
				add(i, "clear reads no source field")
			}
			if !st.Overwrite {
				add(i, "clear needs overwrite, otherwise it can never change a value")
			}
		case StepConstant:
			if !dimValueRe.MatchString(st.Value) {
				add(i, "constant value %q must be 1 to 20 letters, digits, dot, underscore or dash", st.Value)
			}
		default:
			add(i, "kind %q must be table_lookup, move, clear or constant", st.Kind)
		}
	}
	return out
}

func sameStrings(a, b []string) bool {
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

func conditionHolds(c *StepCondition, value string) bool {
	if c == nil {
		return true
	}
	switch c.Op {
	case CondEq:
		return len(c.Values) == 1 && value == c.Values[0]
	case CondNe:
		return len(c.Values) == 1 && value != c.Values[0]
	case CondIn:
		for _, v := range c.Values {
			if value == v {
				return true
			}
		}
		return false
	case CondBlank:
		return value == ""
	case CondNotBlank:
		return value != ""
	}
	return false
}

// LookupMatch returns the target of the most specific row of t valid on date
// that matches values (BR-11.14): fewest wildcards first, then the latest
// validFrom. A blank value matches only `*`. ok is false when no row matches.
func LookupMatch(rows []LookupRow, values []string, date time.Time) (target string, matched []string, ok bool) {
	day := date.Format(time.DateOnly)
	bestWild, bestFrom := 0, ""
	for _, r := range rows {
		if len(r.Source) != len(values) || r.ValidFrom > day || (r.ValidTo != nil && *r.ValidTo < day) {
			continue
		}
		wild, hit := 0, true
		for i, rv := range r.Source {
			switch {
			case rv == LookupWildcard:
				wild++
			case rv != values[i] || values[i] == "":
				hit = false
			}
			if !hit {
				break
			}
		}
		if !hit {
			continue
		}
		if !ok || wild < bestWild || (wild == bestWild && r.ValidFrom > bestFrom) {
			target, matched, ok, bestWild, bestFrom = r.Target, r.Source, true, wild, r.ValidFrom
		}
	}
	return target, matched, ok
}

// Derive applies the strategy to one line (BR-11.14). Steps run in sequence.
// The precedence of FR-11.22 is: explicit line value > step with overwrite >
// step without overwrite. So a value the request carries is never changed by
// any step (not even with overwrite, nor by clear), and a step whose target was
// set by an earlier step in the same run is skipped unless overwrite is set.
// rows holds the validity-filtered rows of each lookup table by id. It returns
// the dimensions after derivation and the trace of the steps that changed a value.
func Derive(steps []DerivationStep, rows map[string][]LookupRow, date time.Time, account Account, dims map[string]string) (map[string]string, []DerivationTrace) {
	out := make(map[string]string, len(dims))
	explicit := make(map[string]bool, len(dims))
	for k, v := range dims {
		out[k] = v
		if v != "" {
			explicit[k] = true
		}
	}
	read := func(field string) string {
		switch field {
		case FieldAccount:
			return account.Number
		case FieldAccountType:
			return account.Type
		}
		return out[field]
	}
	ordered := append([]DerivationStep(nil), steps...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Sequence < ordered[j].Sequence })
	var trace []DerivationTrace
	for _, st := range ordered {
		if st.Condition != nil && !conditionHolds(st.Condition, read(st.Condition.Field)) {
			continue
		}
		if explicit[st.Target] {
			continue
		}
		before := out[st.Target]
		if before != "" && !st.Overwrite {
			continue
		}
		var after string
		var matched []string
		switch st.Kind {
		case StepTableLookup:
			values := make([]string, len(st.SourceFields))
			for i, f := range st.SourceFields {
				values[i] = read(f)
			}
			target, row, ok := LookupMatch(rows[st.LookupTableID], values, date)
			if !ok {
				continue
			}
			after, matched = target, row
		case StepMove:
			if len(st.SourceFields) != 1 {
				continue
			}
			after = read(st.SourceFields[0])
			if after == "" {
				continue
			}
		case StepConstant:
			after = st.Value
		case StepClear:
			after = ""
		default:
			continue
		}
		if after == before {
			continue
		}
		if after == "" {
			delete(out, st.Target)
		} else {
			out[st.Target] = after
		}
		trace = append(trace, DerivationTrace{StepID: st.ID, Sequence: st.Sequence, Kind: st.Kind, Target: st.Target,
			Before: before, After: after, MatchedRow: matched})
	}
	return out, trace
}

// StepIDs lists the distinct step ids of a trace in the order they ran.
func StepIDs(trace []DerivationTrace) []string {
	var ids []string
	seen := map[string]bool{}
	for _, t := range trace {
		if !seen[t.StepID] {
			seen[t.StepID] = true
			ids = append(ids, t.StepID)
		}
	}
	return ids
}
