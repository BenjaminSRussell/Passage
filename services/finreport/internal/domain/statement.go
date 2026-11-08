package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"

	"github.com/shopspring/decimal"
)

// ErrDrillMismatch means the balance keys of a cell do not sum to its value.
var ErrDrillMismatch = errors.New("finreport: drill keys do not sum to the cell value")

// Balance is one row of the reporting read model (FR-15.1): net is debit minus
// credit for a ledger, period, account, dimension combination and currency type.
// ClosingNet is the part of Net posted by closing entries (BR-15.2).
type Balance struct {
	LedgerID     string
	Period       int // index into the calendar
	Account      string
	Combination  string
	Dims         map[string]string
	CurrencyType string
	Net          decimal.Decimal
	ClosingNet   decimal.Decimal
}

// Key is one balance that contributed to a cell, with its displayed amount
// (sign presentation applied, BR-15.3). Level 1 drill-down returns these.
// Credit records that the amount is -net because the row the balance came
// from shows credits positive; a total row mixes keys of both signs, so drill
// level 2 signs each line by its key, not by the cell's row.
type Key struct {
	LedgerID     string          `json:"ledger"`
	Period       string          `json:"period"`
	Account      string          `json:"account"`
	Combination  string          `json:"combination"`
	CurrencyType string          `json:"currencyType"`
	Amount       decimal.Decimal `json:"amount"`
	Credit       bool            `json:"credit,omitempty"`
}

// ExcludesClosing reports whether a statement kind leaves closing entries out:
// income statements, and cash flows so the year-end transfer to equity is not
// a cash flow (BR-15.2, BR-15.5). Evaluation and drill level 2 share it.
func ExcludesClosing(k Kind) bool { return k == KindIncomeStatement || k == KindCashFlow }

// Cell is one row x column value of a statement run. Computed cells (calculation
// rows and CALC columns) are derived from other cells and carry no balance keys.
type Cell struct {
	Row      string          `json:"row"`
	Column   string          `json:"column"`
	Value    decimal.Decimal `json:"value"`
	Blank    bool            `json:"blank"`
	Computed bool            `json:"computed,omitempty"`
	Note     string          `json:"note,omitempty"`
	Keys     []Key           `json:"keys"`
}

// KeySum is the sum of the cell's balance keys.
func (c Cell) KeySum() decimal.Decimal {
	sum := decimal.Zero
	for _, k := range c.Keys {
		sum = sum.Add(k.Amount)
	}
	return sum
}

// CheckDrill enforces BR-15.4 for balance-backed cells: the keys sum to the
// value exactly. Computed and blank cells must carry no keys.
func (c Cell) CheckDrill() error {
	if c.Blank || c.Computed {
		if len(c.Keys) > 0 {
			return ErrDrillMismatch
		}
		return nil
	}
	if !c.KeySum().Equal(c.Value) {
		return ErrDrillMismatch
	}
	return nil
}

// Input is everything a statement run evaluates. Balances may contain other
// ledgers; only LedgerID is used. At is the run period's calendar index.
// CurrencyType is the run's currency type, used by columns that do not name one.
// IncludeAdjustmentPeriods counts adjustment periods for this run even when the
// definition does not (BR-15.1).
type Input struct {
	Definition               Definition
	LedgerID                 string
	CurrencyType             string
	IncludeAdjustmentPeriods bool
	Calendar                 Calendar
	At                       int
	Balances                 []Balance
	// Consolidated reads the balances of every ledger in Balances instead of
	// only LedgerID; the definition's columns then name their ledger (Consolidate).
	Consolidated bool
}

// Result holds the cells in row-major order and their SHA-256 (hex). The hash
// covers the cell values, so equal balances and definition give an equal hash.
type Result struct {
	Cells []Cell
	Hash  string
}

type colPlan struct {
	calc     bool
	found    bool
	currency string
	include  map[int]bool
	// dims is the column's dimension filter; nil keeps every balance.
	dims map[string][]string
	// ledger, when set, keeps only that ledger's balances (consolidated runs).
	ledger string
	// side is the column's balance side ("debit", "credit" or empty).
	side string
}

type slot struct {
	currency string
	period   int
}

type cellVal struct {
	v        decimal.Decimal
	blank    bool
	computed bool
	keys     []Key
	note     string
}

type evaluator struct {
	def    Definition
	cal    Calendar
	plans  []colPlan
	index  map[slot][]Balance
	rowIdx map[string]int
	colIdx map[string]int
	memo   map[[2]int]cellVal
	busy   map[[2]int]bool
	// accounts holds the sorted, distinct explicit account list of each row
	// that has one, so its balances are found by binary search.
	accounts map[string][]string
	// byValue indexes a slot's balances by the value of one dimension
	// attribute (positions in the sorted slot list), built on first use.
	byValue map[dimSlot]map[string][]int
}

type dimSlot struct {
	slot
	attr string
}

// Evaluate computes every cell of a definition for one ledger and run period.
// Income and cash flow statements exclude closing entries, and cash flow
// statements leave out opening periods; balance sheets and trial balances
// are cumulative from the opening period (period 0, which carries the year-end
// brought-forward entries) of the fiscal year of the column's last period
// through that period (BR-15.2), so a trial balance shows balances, not
// movements. Missing prior-year periods leave cells blank with REPORT-0010
// (BR-15.9).
func Evaluate(in Input) (Result, error) {
	def := in.Definition
	if issues := def.Validate(); len(issues) > 0 {
		return Result{}, &Error{Code: issues[0].Code, Detail: issues[0].Field + ": " + issues[0].Detail}
	}
	plans, err := planColumns(in)
	if err != nil {
		return Result{}, err
	}
	periods := in.Calendar.Periods()
	e := &evaluator{
		def: def, cal: in.Calendar, plans: plans,
		index: map[slot][]Balance{}, rowIdx: map[string]int{}, colIdx: map[string]int{},
		memo: map[[2]int]cellVal{}, busy: map[[2]int]bool{},
		accounts: map[string][]string{}, byValue: map[dimSlot]map[string][]int{},
	}
	for j, c := range def.Columns {
		e.colIdx[c.Code] = j
	}
	for i, r := range def.Rows {
		e.rowIdx[r.Code] = i
		if len(r.Accounts) > 0 {
			e.accounts[r.Code] = distinctSorted(r.Accounts)
		}
	}
	for _, b := range in.Balances {
		if (!in.Consolidated && b.LedgerID != in.LedgerID) || b.Period < 0 || b.Period >= len(periods) {
			continue
		}
		k := slot{b.CurrencyType, b.Period}
		e.index[k] = append(e.index[k], b)
	}
	for _, list := range e.index {
		sort.SliceStable(list, func(i, j int) bool {
			if list[i].Account != list[j].Account {
				return list[i].Account < list[j].Account
			}
			if list[i].Combination != list[j].Combination {
				return list[i].Combination < list[j].Combination
			}
			return list[i].LedgerID < list[j].LedgerID
		})
	}

	cells := make([]Cell, 0, len(def.Rows)*len(def.Columns))
	for ri, r := range def.Rows {
		for ci, c := range def.Columns {
			v, err := e.cell(ri, ci)
			if err != nil {
				return Result{}, err
			}
			cell := Cell{Row: r.Code, Column: c.Code, Note: v.note, Keys: v.keys, Computed: v.computed}
			if v.blank {
				cell.Blank = true
			} else {
				cell.Value = v.v
			}
			cells = append(cells, cell)
		}
	}
	return Result{Cells: cells, Hash: hashCells(cells)}, nil
}

// planColumns resolves, for each column, the calendar periods it sums and its
// currency type. Balance sheets and trial balances are cumulative from the
// opening period of the fiscal year (BR-15.2). Cash flow statements show
// movements, so they leave out opening periods (period number 0): the
// brought-forward balances there are not cash flows of the year.
func planColumns(in Input) ([]colPlan, error) {
	def := in.Definition
	periods := in.Calendar.Periods()
	includeAdj := def.IncludeAdjustmentPeriods || in.IncludeAdjustmentPeriods
	counted := func(i int) bool { return includeAdj || !periods[i].Adjustment }
	plans := make([]colPlan, len(def.Columns))
	for j, c := range def.Columns {
		if c.PeriodExpr == "CALC" {
			plans[j].calc = true
			continue
		}
		sel, found, err := Select(c.PeriodExpr, in.At, in.Calendar, includeAdj)
		if err != nil {
			return nil, err
		}
		if !found {
			continue
		}
		include := map[int]bool{}
		hi := -1
		for _, i := range sel {
			if def.Kind == KindCashFlow && periods[i].No == 0 {
				continue
			}
			include[i] = true
			if i > hi {
				hi = i
			}
		}
		if (def.Kind == KindBalanceSheet || def.Kind == KindTrialBalance) && hi >= 0 {
			for i := 0; i <= hi; i++ {
				if counted(i) && periods[i].Year == periods[hi].Year {
					include[i] = true
				}
			}
		}
		currency := c.CurrencyType
		if currency == "" {
			currency = in.CurrencyType
		}
		plans[j] = colPlan{found: true, currency: currency, include: include, dims: c.DimensionFilter, ledger: c.Ledger, side: c.BalanceSide}
	}
	return plans, nil
}

func (e *evaluator) cell(ri, ci int) (cellVal, error) {
	k := [2]int{ri, ci}
	if v, ok := e.memo[k]; ok {
		return v, nil
	}
	if e.busy[k] {
		return cellVal{}, errf(CodeExpression, "row %s depends on itself", e.def.Rows[ri].Code)
	}
	e.busy[k] = true
	v, err := e.compute(ri, ci)
	delete(e.busy, k)
	if err != nil {
		return cellVal{}, err
	}
	e.memo[k] = v
	return v, nil
}

func (e *evaluator) compute(ri, ci int) (cellVal, error) {
	row := e.def.Rows[ri]
	if e.plans[ci].calc {
		return e.calcColumn(ri, ci)
	}
	if !e.plans[ci].found {
		return cellVal{blank: true, note: CodePYMissing}, nil
	}
	switch row.Kind {
	case RowAccountRange, RowDimensionFilter:
		return e.sumBalances(row, ci), nil
	case RowTotal:
		var out cellVal
		for _, ref := range row.References {
			rv, err := e.cell(e.rowIdx[ref], ci)
			if err != nil {
				return cellVal{}, err
			}
			if rv.blank {
				continue
			}
			out.v = out.v.Add(rv.v)
			out.keys = append(out.keys, rv.keys...)
		}
		return out, nil
	case RowCalculation:
		raw, expr, err := ParseRowExpr(row.Expression)
		if err != nil {
			return cellVal{}, err
		}
		val, err := expr.Eval(func(ref string) (Value, error) {
			ri := e.rowIdx[ref]
			rv, err := e.cell(ri, ci)
			v := rv.v
			// raw: uses stored net (debit - credit) instead of the displayed value (BR-15.3).
			// A balance-side column shows credits positive whatever the row's normal
			// balance, so there the column decides the sign.
			if raw && shownAsCredit(e.def.Rows[ri], e.plans[ci].side) {
				v = v.Neg()
			}
			return Value{V: v, Blank: rv.blank}, err
		})
		if err != nil {
			return cellVal{}, err
		}
		return cellVal{v: val.V, blank: val.Blank, computed: true}, nil
	default: // header
		return cellVal{blank: true}, nil
	}
}

// calcColumn evaluates a CALC column for one row from other columns' values.
func (e *evaluator) calcColumn(ri, ci int) (cellVal, error) {
	if e.def.Rows[ri].Kind == RowHeader {
		return cellVal{blank: true}, nil
	}
	expr, err := ParseExpr(e.def.Columns[ci].Expression)
	if err != nil {
		return cellVal{}, err
	}
	val, err := expr.Eval(func(ref string) (Value, error) {
		cv, err := e.cell(ri, e.colIdx[ref])
		return Value{V: cv.v, Blank: cv.blank}, err
	})
	if err != nil {
		return cellVal{}, err
	}
	return cellVal{v: val.V, blank: val.Blank, computed: true}, nil
}

// matching returns the positions in list (the sorted balances of slot k) of
// the balances row picks up, in list order. A contiguous account range is cut by
// binary search; an explicit account list looks each account up; a filter on
// one dimension attribute without wildcards reads the slot's value index. Rows
// written by hierarchy runs use the last two, so a 2,000-node hierarchy stays
// within the FR-15.6 budget instead of scanning every balance per row.
func (e *evaluator) matching(row Row, k slot, list []Balance) []int {
	lo, hi := 0, len(list)
	if row.AccountFrom != "" {
		lo = sort.Search(len(list), func(j int) bool { return list[j].Account >= row.AccountFrom })
		hi = sort.Search(len(list), func(j int) bool { return list[j].Account > row.AccountTo })
	}
	var out []int
	keep := func(j int) {
		if dimsMatch(row.DimensionFilter, list[j].Dims) {
			out = append(out, j)
		}
	}
	if accts, ok := e.accounts[row.Code]; ok {
		for _, a := range accts {
			j := lo + sort.Search(hi-lo, func(n int) bool { return list[lo+n].Account >= a })
			for ; j < hi && list[j].Account == a; j++ {
				keep(j)
			}
		}
		return out
	}
	if attr, ok := singleExactAttribute(row.DimensionFilter); ok {
		idx := e.valueIndex(k, attr, list)
		var pos []int
		for _, v := range distinctSorted(row.DimensionFilter[attr]) {
			pos = append(pos, idx[v]...)
		}
		sort.Ints(pos)
		for _, j := range pos {
			if j >= lo && j < hi {
				out = append(out, j)
			}
		}
		return out
	}
	for j := lo; j < hi; j++ {
		keep(j)
	}
	return out
}

// valueIndex maps each value of attr (empty when a balance has none) to the
// positions of the balances of slot k that carry it, in list order.
func (e *evaluator) valueIndex(k slot, attr string, list []Balance) map[string][]int {
	key := dimSlot{k, attr}
	if idx, ok := e.byValue[key]; ok {
		return idx
	}
	idx := map[string][]int{}
	for j, b := range list {
		v := b.Dims[attr]
		idx[v] = append(idx[v], j)
	}
	e.byValue[key] = idx
	return idx
}

// singleExactAttribute reports the attribute of a filter on exactly one
// attribute whose values are all literal (no "*" wildcard).
func singleExactAttribute(filter map[string][]string) (string, bool) {
	if len(filter) != 1 {
		return "", false
	}
	for attr, values := range filter {
		for _, v := range values {
			if v == "*" {
				return "", false
			}
		}
		return attr, true
	}
	return "", false
}

func distinctSorted(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	n := 0
	for i, v := range out {
		if i == 0 || v != out[n-1] {
			out[n] = v
			n++
		}
	}
	return out[:n]
}

// sumBalances sums the displayed amounts of matching balances over the
// column's periods. Keys are produced in (period, account, combination) order.
func (e *evaluator) sumBalances(row Row, ci int) cellVal {
	plan := e.plans[ci]
	credit := row.NormalBalance == "credit"
	// Closing entries are left out of income statements and of cash flow
	// movements, so the year-end transfer to equity is not a cash flow (BR-15.2,
	// BR-15.5).
	excludeClosing := ExcludesClosing(e.def.Kind)
	periods := e.cal.Periods()
	var out cellVal
	// A balance-side column keeps whole accounts: the sign of an account's net
	// over the column decides which side it is on, so a key is kept or dropped
	// with its account and the keys still sum to the cell (BR-15.4).
	byAccount := map[string]decimal.Decimal{}
	for i := range periods {
		if !plan.include[i] {
			continue
		}
		k := slot{plan.currency, i}
		list := e.index[k]
		for _, j := range e.matching(row, k, list) {
			b := list[j]
			if plan.dims != nil && !dimsMatch(plan.dims, b.Dims) {
				continue
			}
			if plan.ledger != "" && b.LedgerID != plan.ledger {
				continue
			}
			net := b.Net
			if excludeClosing {
				net = net.Sub(b.ClosingNet)
			}
			if plan.side != "" {
				byAccount[b.Account] = byAccount[b.Account].Add(net)
				continue
			}
			amt := net
			if credit {
				amt = net.Neg()
			}
			out.v = out.v.Add(amt)
			out.keys = append(out.keys, Key{LedgerID: b.LedgerID, Period: periods[i].Label(), Account: b.Account,
				Combination: b.Combination, CurrencyType: b.CurrencyType, Amount: amt, Credit: credit})
		}
	}
	if plan.side == "" {
		return out
	}
	creditSide := plan.side == BalanceSideCredit
	for i := range periods {
		if !plan.include[i] {
			continue
		}
		k := slot{plan.currency, i}
		list := e.index[k]
		for _, j := range e.matching(row, k, list) {
			b := list[j]
			if plan.dims != nil && !dimsMatch(plan.dims, b.Dims) {
				continue
			}
			if plan.ledger != "" && b.LedgerID != plan.ledger {
				continue
			}
			total := byAccount[b.Account]
			if (creditSide && !total.IsNegative()) || (!creditSide && total.Sign() <= 0) {
				continue
			}
			net := b.Net
			if excludeClosing {
				net = net.Sub(b.ClosingNet)
			}
			amt := net
			if creditSide {
				amt = net.Neg()
			}
			out.v = out.v.Add(amt)
			out.keys = append(out.keys, Key{LedgerID: b.LedgerID, Period: periods[i].Label(), Account: b.Account,
				Combination: b.Combination, CurrencyType: b.CurrencyType, Amount: amt, Credit: creditSide})
		}
	}
	return out
}

// shownAsCredit reports whether a balance-backed cell of row in a column with
// the given balance side shows -net: a credit side column always does, a debit
// side column never does, and any other column follows the row (BR-15.3).
func shownAsCredit(row Row, side string) bool {
	if side != "" {
		return side == BalanceSideCredit
	}
	return row.NormalBalance == "credit"
}

func dimsMatch(filter map[string][]string, dims map[string]string) bool {
	for attr, allowed := range filter {
		v := dims[attr]
		ok := false
		for _, a := range allowed {
			if a == "*" || a == v {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

func hashCells(cells []Cell) string {
	h := sha256.New()
	for _, c := range cells {
		v := "-"
		if !c.Blank {
			v = c.Value.StringFixed(6)
		}
		fmt.Fprintf(h, "%s|%s|%s|%t|%s\n", c.Row, c.Column, v, c.Blank, c.Note)
	}
	return hex.EncodeToString(h.Sum(nil))
}
