// Package dq is the data-quality suite runner of the migration factory (FR-49.11, D1c). A suite is a JSON document of
// expectations over the columns of a pack's extract, named and shaped after Great Expectations (expect_column_values_to_
// not_be_null, ..._be_unique, ..._be_in_set and so on), so the suites can be moved to Great Expectations unchanged in
// meaning. Great Expectations itself is a Python runtime with a data-context, a store and a checkpoint; the factory is a
// Go service that must validate an extract inside the request that loads it, so it runs the suites with this small
// runner instead and keeps the file format compatible. Each expectation names the factory validation `check` it
// expresses; the parity test (parity_test.go) proves a suite flags exactly the rows the Go validator quarantines, and
// the coverage it reports is the share of those checks that some expectation catches (FR-49.11 asks for at least 95 %).
package dq

import (
	"embed"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

//go:embed suites/*.json
var suiteFS embed.FS

// Expectation types, after Great Expectations.
const (
	NotNull           = "expect_column_values_to_not_be_null"
	Unique            = "expect_column_values_to_be_unique"
	CompoundUnique    = "expect_compound_columns_to_be_unique"
	InSet             = "expect_column_values_to_be_in_set"
	Between           = "expect_column_values_to_be_between"
	MatchRegex        = "expect_column_values_to_match_regex"
	MaxDecimals       = "expect_column_values_to_have_max_decimals_for_currency"
	EqualsWhenPresent = "expect_column_values_to_equal_when_column_present"
	// PairGreater is Great Expectations' expect_column_pair_values_A_to_be_greater_than_B over decimal columns.
	PairGreater = "expect_column_pair_values_A_to_be_greater_than_B"
	// Strftime is Great Expectations' expect_column_values_to_match_strftime_format (a real calendar date, not only its shape).
	Strftime = "expect_column_values_to_match_strftime_format"
	// MaxPlaces bounds the decimal places of a column to a fixed number (the control totals' scale).
	MaxPlaces = "expect_column_values_to_have_max_decimal_places"
	// InSetByColumn takes the reference set from another column's value: a sales line's party is a customer, a purchase
	// line's a vendor. A row whose column names no set is unexpected.
	InSetByColumn = "expect_column_values_to_be_in_set_chosen_by_column"
	// SignedSum asserts a column equals the signed sum of other columns exactly (net = gross - tax - deductions).
	SignedSum = "expect_column_values_to_equal_signed_sum_of_columns"
)

// Expectation is one assertion over the rows of an extract.
type Expectation struct {
	Type string `json:"type"`
	// Check names the factory validation this expectation expresses; coverage is counted over checks.
	Check   string   `json:"check"`
	Column  string   `json:"column,omitempty"`
	Columns []string `json:"columns,omitempty"`
	// SkipBlank leaves rows whose column (or, for a compound key, last column) is blank out of a uniqueness check.
	SkipBlank bool `json:"skip_blank,omitempty"`
	// Set names a reference set the caller supplies (a master, or "iso4217").
	Set string `json:"set,omitempty"`
	// Min and StrictMin bound a decimal column from below; Max from above.
	Min       string `json:"min,omitempty"`
	StrictMin bool   `json:"strict_min,omitempty"`
	Max       string `json:"max,omitempty"`
	Regex     string `json:"regex,omitempty"`
	// CurrencyColumn is the column whose currency gives the decimals of MaxDecimals.
	CurrencyColumn string `json:"currency_column,omitempty"`
	// When and Equals: with EqualsWhenPresent, a row whose When column is not blank must hold Equals in Column.
	When   string `json:"when,omitempty"`
	Equals string `json:"equals,omitempty"`
	// ColumnA and ColumnB, OrEqual: with PairGreater, A must be above B (or equal when OrEqual).
	ColumnA string `json:"column_A,omitempty"`
	ColumnB string `json:"column_B,omitempty"`
	OrEqual bool   `json:"or_equal,omitempty"`
	// Format is the strftime format of Strftime; only %Y, %m and %d are understood.
	Format string `json:"strftime_format,omitempty"`
	// Places is the most decimal places of MaxPlaces.
	Places int `json:"places,omitempty"`
	// By and Sets: with InSetByColumn, the value of By picks the reference set name from Sets.
	By   string            `json:"by,omitempty"`
	Sets map[string]string `json:"sets,omitempty"`
	// Terms are the signed columns of SignedSum, each "+column" or "-column".
	Terms []string `json:"terms,omitempty"`
}

// Suite is the expectations of one pack.
type Suite struct {
	Pack         string        `json:"pack"`
	Expectations []Expectation `json:"expectations"`
}

// Row is one record of an extract: column name to its text (a decimal is its decimal string).
type Row map[string]string

// Load reads the embedded suite of a pack.
func Load(pack string) (Suite, error) {
	raw, err := suiteFS.ReadFile("suites/" + pack + ".json")
	if err != nil {
		return Suite{}, fmt.Errorf("no data-quality suite for pack %q", pack)
	}
	var s Suite
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return Suite{}, fmt.Errorf("suite %s: %w", pack, err)
	}
	if s.Pack != pack {
		return Suite{}, fmt.Errorf("suite %s names pack %q", pack, s.Pack)
	}
	return s, nil
}

// Packs lists the packs that have a suite.
func Packs() []string {
	entries, _ := suiteFS.ReadDir("suites")
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, strings.TrimSuffix(e.Name(), ".json"))
	}
	sort.Strings(out)
	return out
}

// Result is what one expectation found.
type Result struct {
	Expectation Expectation
	// Unexpected holds the zero-based indexes of the rows that break the expectation.
	Unexpected []int
}

// Success reports that no row broke the expectation.
func (r Result) Success() bool { return len(r.Unexpected) == 0 }

// Report is the outcome of a suite over an extract.
type Report struct {
	Pack    string
	Results []Result
}

// Failed returns the indexes of the rows that break at least one expectation, ascending.
func (r Report) Failed() []int {
	seen := map[int]bool{}
	for _, res := range r.Results {
		for _, i := range res.Unexpected {
			seen[i] = true
		}
	}
	out := make([]int, 0, len(seen))
	for i := range seen {
		out = append(out, i)
	}
	sort.Ints(out)
	return out
}

// Checks returns the checks that flagged at least one row.
func (r Report) Checks() map[string]bool {
	out := map[string]bool{}
	for _, res := range r.Results {
		if !res.Success() {
			out[res.Expectation.Check] = true
		}
	}
	return out
}

// Run evaluates every expectation of the suite over the rows. sets are the reference sets the expectations name.
// Rows are judged one by one in order, like the Go validators: a row is unexpected for a uniqueness expectation when an
// earlier row holds the same value.
func (s Suite) Run(rows []Row, sets map[string]map[string]bool) (Report, error) {
	rep := Report{Pack: s.Pack}
	for _, e := range s.Expectations {
		bad, err := evaluate(e, rows, sets)
		if err != nil {
			return rep, fmt.Errorf("expectation %s (%s): %w", e.Type, e.Check, err)
		}
		rep.Results = append(rep.Results, Result{Expectation: e, Unexpected: bad})
	}
	return rep, nil
}

func blank(s string) bool { return strings.TrimSpace(s) == "" }

func evaluate(e Expectation, rows []Row, sets map[string]map[string]bool) ([]int, error) {
	var bad []int
	switch e.Type {
	case NotNull:
		for i, r := range rows {
			if blank(r[e.Column]) {
				bad = append(bad, i)
			}
		}
	case Unique, CompoundUnique:
		cols := e.Columns
		if e.Type == Unique {
			cols = []string{e.Column}
		}
		if len(cols) == 0 {
			return nil, fmt.Errorf("no column named")
		}
		seen := map[string]bool{}
		for i, r := range rows {
			parts := make([]string, len(cols))
			for j, c := range cols {
				parts[j] = r[c]
			}
			if e.SkipBlank && blank(parts[len(parts)-1]) {
				continue
			}
			key := strings.Join(parts, "\x1f")
			if seen[key] {
				bad = append(bad, i)
			}
			seen[key] = true
		}
	case InSet:
		set, ok := sets[e.Set]
		if !ok {
			return nil, fmt.Errorf("reference set %q was not supplied", e.Set)
		}
		for i, r := range rows {
			if !set[r[e.Column]] {
				bad = append(bad, i)
			}
		}
	case Between:
		lo, hi, err := bounds(e)
		if err != nil {
			return nil, err
		}
		for i, r := range rows {
			v, err := decimal.NewFromString(strings.TrimSpace(r[e.Column]))
			switch {
			case err != nil:
				bad = append(bad, i)
			case lo != nil && (v.LessThan(*lo) || (e.StrictMin && v.Equal(*lo))):
				bad = append(bad, i)
			case hi != nil && v.GreaterThan(*hi):
				bad = append(bad, i)
			}
		}
	case MatchRegex:
		re, err := regexp.Compile(e.Regex)
		if err != nil {
			return nil, err
		}
		for i, r := range rows {
			if !re.MatchString(r[e.Column]) {
				bad = append(bad, i)
			}
		}
	case MaxDecimals:
		for i, r := range rows {
			v, err := decimal.NewFromString(strings.TrimSpace(r[e.Column]))
			if err != nil || !v.Equal(v.Round(money.MinorUnits(r[e.CurrencyColumn]))) {
				bad = append(bad, i)
			}
		}
	case EqualsWhenPresent:
		want, err := decimal.NewFromString(e.Equals)
		if err != nil {
			return nil, err
		}
		for i, r := range rows {
			if blank(r[e.When]) {
				continue
			}
			if v, err := decimal.NewFromString(strings.TrimSpace(r[e.Column])); err != nil || !v.Equal(want) {
				bad = append(bad, i)
			}
		}
	case PairGreater:
		for i, r := range rows {
			a, errA := decimal.NewFromString(strings.TrimSpace(r[e.ColumnA]))
			b, errB := decimal.NewFromString(strings.TrimSpace(r[e.ColumnB]))
			if errA != nil || errB != nil || a.LessThan(b) || (!e.OrEqual && a.Equal(b)) {
				bad = append(bad, i)
			}
		}
	case Strftime:
		layout, err := goLayout(e.Format)
		if err != nil {
			return nil, err
		}
		for i, r := range rows {
			v := strings.TrimSpace(r[e.Column])
			if e.SkipBlank && v == "" {
				continue
			}
			if _, err := time.Parse(layout, v); err != nil {
				bad = append(bad, i)
			}
		}
	case MaxPlaces:
		for i, r := range rows {
			v, err := decimal.NewFromString(strings.TrimSpace(r[e.Column]))
			if err != nil || !v.Equal(v.Round(int32(e.Places))) {
				bad = append(bad, i)
			}
		}
	case InSetByColumn:
		for i, r := range rows {
			set, ok := sets[e.Sets[r[e.By]]]
			if !ok || !set[r[e.Column]] {
				bad = append(bad, i)
			}
		}
	case SignedSum:
		if len(e.Terms) == 0 {
			return nil, fmt.Errorf("no terms named")
		}
		for i, r := range rows {
			want, err := signedSum(r, e.Terms)
			got, errGot := decimal.NewFromString(strings.TrimSpace(r[e.Column]))
			if err != nil || errGot != nil || !got.Equal(want) {
				bad = append(bad, i)
			}
		}
	default:
		return nil, fmt.Errorf("unknown expectation type")
	}
	return bad, nil
}

func signedSum(r Row, terms []string) (decimal.Decimal, error) {
	sum := decimal.Zero
	for _, t := range terms {
		if len(t) < 2 || (t[0] != '+' && t[0] != '-') {
			return sum, fmt.Errorf("term %q must start with + or -", t)
		}
		v, err := decimal.NewFromString(strings.TrimSpace(r[t[1:]]))
		if err != nil {
			return sum, err
		}
		if t[0] == '-' {
			v = v.Neg()
		}
		sum = sum.Add(v)
	}
	return sum, nil
}

// goLayout turns a strftime format of %Y, %m and %d (and literal characters) into a Go time layout.
func goLayout(format string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(format); i++ {
		if format[i] != '%' {
			b.WriteByte(format[i])
			continue
		}
		if i+1 == len(format) {
			return "", fmt.Errorf("strftime format %q ends with %%", format)
		}
		i++
		switch format[i] {
		case 'Y':
			b.WriteString("2006")
		case 'm':
			b.WriteString("01")
		case 'd':
			b.WriteString("02")
		default:
			return "", fmt.Errorf("strftime directive %%%c is not supported", format[i])
		}
	}
	return b.String(), nil
}

func bounds(e Expectation) (lo, hi *decimal.Decimal, err error) {
	if e.Min != "" {
		v, err := decimal.NewFromString(e.Min)
		if err != nil {
			return nil, nil, err
		}
		lo = &v
	}
	if e.Max != "" {
		v, err := decimal.NewFromString(e.Max)
		if err != nil {
			return nil, nil, err
		}
		hi = &v
	}
	return lo, hi, nil
}

// Flagged runs the pack's suite over the rows and returns, for every row the suite flags, the checks that flagged it.
func Flagged(pack string, rows []Row, sets map[string]map[string]bool) (map[int][]string, error) {
	s, err := Load(pack)
	if err != nil {
		return nil, err
	}
	rep, err := s.Run(rows, sets)
	if err != nil {
		return nil, err
	}
	out := map[int][]string{}
	for _, res := range rep.Results {
		for _, i := range res.Unexpected {
			if !slices.Contains(out[i], res.Expectation.Check) {
				out[i] = append(out[i], res.Expectation.Check)
			}
		}
	}
	return out, nil
}
