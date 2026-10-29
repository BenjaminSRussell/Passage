package domain

import (
	"fmt"
	"regexp"
	"sort"
	"time"
	"unicode/utf8"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

// CodeForecastDuplicate is service-specific (not in §8.41.18): a project already
// has a forecast model with that name.
const CodeForecastDuplicate = "PROJ-FC-001"

// Bounds of a forecast model.
const (
	ForecastNameMax  = 80
	MaxForecastLines = 2000
)

// ForecastLine is one planned quantity and amount of a transaction type in a
// period, optionally on a WBS node (FR-41.20, §8.41.4 ProjectForecast). Period
// is a calendar month YYYY-MM until wave-14 fiscal periods exist. The node is
// kept by WBS code, as transactions are, because WORKING node ids change when
// the WBS is replaced. Amounts are in the project currency.
type ForecastLine struct {
	LineNo      int     `json:"lineNo"`
	Period      string  `json:"period"`
	WBSCode     *string `json:"wbsCode,omitempty"`
	TxnType     TxnType `json:"txnType"`
	Quantity    string  `json:"quantity"`
	CostAmount  string  `json:"costAmount"`
	SalesAmount string  `json:"salesAmount"`
}

// ForecastModel is a named set of forecast lines of one project. A list
// returns models without lines; LineCount and the totals are always set.
type ForecastModel struct {
	ID         string         `json:"id"`
	ProjectID  string         `json:"projectId"`
	Name       string         `json:"name"`
	Currency   string         `json:"currency"`
	Version    int            `json:"version"`
	LineCount  int            `json:"lineCount"`
	TotalCost  string         `json:"totalCost"`
	TotalSales string         `json:"totalSales"`
	Lines      []ForecastLine `json:"lines,omitempty"`
	CreatedBy  string         `json:"createdBy"`
	CreatedAt  time.Time      `json:"createdAt"`
	UpdatedAt  time.Time      `json:"updatedAt"`
}

// ForecastRow compares forecast and actual for one period and transaction type.
// Variances are actual minus forecast, so a positive cost variance is overspend.
type ForecastRow struct {
	Period           string  `json:"period"`
	TxnType          TxnType `json:"txnType"`
	ForecastQuantity string  `json:"forecastQuantity"`
	ForecastCost     string  `json:"forecastCost"`
	ForecastSales    string  `json:"forecastSales"`
	ActualQuantity   string  `json:"actualQuantity"`
	ActualCost       string  `json:"actualCost"`
	ActualSales      string  `json:"actualSales"`
	QuantityVariance string  `json:"quantityVariance"`
	CostVariance     string  `json:"costVariance"`
	SalesVariance    string  `json:"salesVariance"`
}

// ActualTotal is the recorded total of one period and transaction type, summed
// from the project transactions.
type ActualTotal struct {
	Period      string
	TxnType     TxnType
	Quantity    decimal.Decimal
	CostAmount  decimal.Decimal
	SalesAmount decimal.Decimal
}

var periodPattern = regexp.MustCompile(`^[0-9]{4}-(0[1-9]|1[0-2])$`)

// ValidPeriod reports whether s is a YYYY-MM period.
func ValidPeriod(s string) bool { return periodPattern.MatchString(s) }

// ValidForecastName reports whether a model name has 1 to ForecastNameMax characters.
func ValidForecastName(name string) bool {
	n := utf8.RuneCountInString(name)
	return n >= 1 && n <= ForecastNameMax
}

// NormalizeForecastLines validates the lines of a forecast model and returns
// them numbered from 1, with quantities at 3 decimals and amounts rounded half
// even to the currency's minor unit (the same rule as transaction amounts,
// BR-41.5). Field errors are keyed lines[i].field; a second line for the same
// period, type and node is an error. Quantities and costs are non-negative;
// sales may be negative only on ON_ACCOUNT (on-account credits, BR-41.4).
// wbsCodes is the set of codes a line may name (the WORKING plan).
func NormalizeForecastLines(lines []ForecastLine, currency string, wbsCodes map[string]bool) ([]ForecastLine, map[string]string) {
	fields := map[string]string{}
	if len(lines) > MaxForecastLines {
		fields["lines"] = fmt.Sprintf("must have at most %d lines", MaxForecastLines)
		return nil, fields
	}
	scale := money.MinorUnits(currency)
	out := make([]ForecastLine, 0, len(lines))
	seen := map[string]bool{}
	for i, l := range lines {
		key := fmt.Sprintf("lines[%d].", i)
		if !ValidPeriod(l.Period) {
			fields[key+"period"] = "must be a month YYYY-MM"
		}
		if !ValidTxnType(l.TxnType) {
			fields[key+"txnType"] = "must be HOUR, COST, ITEM, FEE or ON_ACCOUNT"
		}
		if l.WBSCode != nil && !wbsCodes[*l.WBSCode] {
			fields[key+"wbsCode"] = "must name a node of the project's WORKING plan"
		}
		q, err := decimal.NewFromString(l.Quantity)
		if err != nil || q.IsNegative() || !q.Equal(q.Round(3)) {
			fields[key+"quantity"] = "must be a non-negative decimal with at most 3 fraction digits"
		}
		c, err := decimal.NewFromString(l.CostAmount)
		if err != nil || c.IsNegative() {
			fields[key+"costAmount"] = "must be a non-negative decimal"
		}
		s, err := decimal.NewFromString(l.SalesAmount)
		switch {
		case err != nil:
			fields[key+"salesAmount"] = "must be a decimal"
		case s.IsNegative() && l.TxnType != TxnOnAccount:
			fields[key+"salesAmount"] = "may be negative only on an ON_ACCOUNT line"
		}
		dup := l.Period + "|" + string(l.TxnType) + "|" + deref(l.WBSCode)
		if seen[dup] {
			fields[key+"period"] = "repeats the period, type and WBS node of an earlier line"
		}
		seen[dup] = true
		out = append(out, ForecastLine{LineNo: i + 1, Period: l.Period, WBSCode: l.WBSCode, TxnType: l.TxnType,
			Quantity: q.StringFixed(3), CostAmount: c.RoundBank(scale).StringFixed(scale), SalesAmount: s.RoundBank(scale).StringFixed(scale)})
	}
	if len(fields) > 0 {
		return nil, fields
	}
	return out, nil
}

// ForecastTotals returns the cost and sales totals of a model's lines in the
// currency's minor unit.
func ForecastTotals(lines []ForecastLine, currency string) (string, string) {
	cost, sales := decimal.Zero, decimal.Zero
	for _, l := range lines {
		c, _ := decimal.NewFromString(l.CostAmount)
		s, _ := decimal.NewFromString(l.SalesAmount)
		cost, sales = cost.Add(c), sales.Add(s)
	}
	scale := money.MinorUnits(currency)
	return cost.StringFixed(scale), sales.StringFixed(scale)
}

// CompareForecast joins forecast lines with actuals by (period, type) and
// returns one row per key on either side, ordered by period then by the
// transaction type order of TxnTypes. Lines on different WBS nodes are summed
// into their period and type. Amounts are in the currency's minor unit.
func CompareForecast(lines []ForecastLine, actuals []ActualTotal, currency string) []ForecastRow {
	type key struct {
		period string
		typ    TxnType
	}
	type sums struct{ fq, fc, fs, aq, ac, as decimal.Decimal }
	acc := map[key]*sums{}
	get := func(k key) *sums {
		if acc[k] == nil {
			acc[k] = &sums{}
		}
		return acc[k]
	}
	for _, l := range lines {
		s := get(key{l.Period, l.TxnType})
		q, _ := decimal.NewFromString(l.Quantity)
		c, _ := decimal.NewFromString(l.CostAmount)
		sa, _ := decimal.NewFromString(l.SalesAmount)
		s.fq, s.fc, s.fs = s.fq.Add(q), s.fc.Add(c), s.fs.Add(sa)
	}
	for _, a := range actuals {
		s := get(key{a.Period, a.TxnType})
		s.aq, s.ac, s.as = s.aq.Add(a.Quantity), s.ac.Add(a.CostAmount), s.as.Add(a.SalesAmount)
	}
	rank := make(map[TxnType]int, len(TxnTypes))
	for i, t := range TxnTypes {
		rank[t] = i
	}
	keys := make([]key, 0, len(acc))
	for k := range acc {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].period != keys[j].period {
			return keys[i].period < keys[j].period
		}
		return rank[keys[i].typ] < rank[keys[j].typ]
	})
	scale := money.MinorUnits(currency)
	rows := make([]ForecastRow, 0, len(keys))
	for _, k := range keys {
		s := acc[k]
		rows = append(rows, ForecastRow{
			Period: k.period, TxnType: k.typ,
			ForecastQuantity: s.fq.StringFixed(3), ForecastCost: s.fc.StringFixed(scale), ForecastSales: s.fs.StringFixed(scale),
			ActualQuantity: s.aq.StringFixed(3), ActualCost: s.ac.StringFixed(scale), ActualSales: s.as.StringFixed(scale),
			QuantityVariance: s.aq.Sub(s.fq).StringFixed(3),
			CostVariance:     s.ac.Sub(s.fc).StringFixed(scale),
			SalesVariance:    s.as.Sub(s.fs).StringFixed(scale),
		})
	}
	return rows
}
