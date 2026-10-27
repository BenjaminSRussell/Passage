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
type Key struct {
	LedgerID     string          `json:"ledger"`
	Period       string          `json:"period"`
	Account      string          `json:"account"`
	Combination  string          `json:"combination"`
	CurrencyType string          `json:"currencyType"`
	Amount       decimal.Decimal `json:"amount"`
}

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
}

// Evaluate computes every cell of a definition for one ledger and run period.
// Income statements exclude closing entries; balance sheets are cumulative from
// the opening period (period 0, which carries the year-end brought-forward
// entries) of the fiscal year of the column's last period through that period
// (BR-15.2). Missing prior-year periods leave cells blank with REPORT-0010
// (BR-15.9).
func Evaluate(in Input) (Result, error) {
	def := in.Definition
	if issues := def.Validate(); len(issues) > 0 {
		return Result{}, &Error{Code: issues[0].Code, Detail: issues[0].Field + ": " + issues[0].Detail}
	}
	periods := in.Calendar.Periods()
	includeAdj := def.IncludeAdjustmentPeriods || in.IncludeAdjustmentPeriods
	counted := func(i int) bool { return includeAdj || !periods[i].Adjustment }
	e := &evaluator{
		def: def, cal: in.Calendar, plans: make([]colPlan, len(def.Columns)),
		index: map[slot][]Balance{}, rowIdx: map[string]int{}, colIdx: map[string]int{},
		memo: map[[2]int]cellVal{}, busy: map[[2]int]bool{},
	}
	for j, c := range def.Columns {
		e.colIdx[c.Code] = j
		if c.PeriodExpr == "CALC" {
			e.plans[j].calc = true
			continue
		}
		sel, found, err := Select(c.PeriodExpr, in.At, in.Calendar, includeAdj)
		if err != nil {
			return Result{}, err
		}
		if !found {
			continue
		}
		include := map[int]bool{}
		hi := -1
		for _, i := range sel {
			include[i] = true
			if i > hi {
				hi = i
			}
		}
		if def.Kind == KindBalanceSheet && hi >= 0 {
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
		e.plans[j] = colPlan{found: true, currency: currency, include: include}
	}
	for i, r := range def.Rows {
		e.rowIdx[r.Code] = i
	}
	for _, b := range in.Balances {
		if b.LedgerID != in.LedgerID || b.Period < 0 || b.Period >= len(periods) {
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
			return list[i].Combination < list[j].Combination
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
			if raw && e.def.Rows[ri].NormalBalance == "credit" {
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

// sumBalances sums the displayed amounts of matching balances over the
// column's periods. Keys are produced in (period, account, combination) order.
func (e *evaluator) sumBalances(row Row, ci int) cellVal {
	plan := e.plans[ci]
	credit := row.NormalBalance == "credit"
	pl := e.def.Kind == KindIncomeStatement
	periods := e.cal.Periods()
	var out cellVal
	for i := range periods {
		if !plan.include[i] {
			continue
		}
		list := e.index[slot{plan.currency, i}]
		lo, hi := 0, len(list)
		if row.AccountFrom != "" {
			lo = sort.Search(len(list), func(j int) bool { return list[j].Account >= row.AccountFrom })
			hi = sort.Search(len(list), func(j int) bool { return list[j].Account > row.AccountTo })
		}
		for _, b := range list[lo:hi] {
			if !dimsMatch(row.DimensionFilter, b.Dims) {
				continue
			}
			net := b.Net
			if pl {
				net = net.Sub(b.ClosingNet)
			}
			amt := net
			if credit {
				amt = net.Neg()
			}
			out.v = out.v.Add(amt)
			out.keys = append(out.keys, Key{LedgerID: b.LedgerID, Period: periods[i].Label(), Account: b.Account,
				Combination: b.Combination, CurrencyType: b.CurrencyType, Amount: amt})
		}
	}
	return out
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
