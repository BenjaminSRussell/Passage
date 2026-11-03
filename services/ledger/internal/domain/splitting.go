package domain

import (
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

// Document splitting (FR-11.23, BR-11.15). For a source type, lines that carry none of the
// splitting dimensions inherit them from the lines that do, in proportion to the carrying
// lines' amounts, and zero-balance clearing lines make the entry balance per splitting key.
// Evaluation is pure: the caller loads the rule and the clearing account.

// Problem codes of document splitting.
const (
	CodeSplitNoSource        = "LEDGER_SPLIT_NO_SOURCE"
	CodeSplitClearingMissing = "LEDGER_SPLIT_CLEARING_ACCOUNT_MISSING"
	CodeSplittingInvalid     = "LEDGER_SPLITTING_RULE_INVALID"
)

// Limits and trace kinds.
const (
	MaxSplittingDimensions = 5
	SplitInherited         = "inherited"
	SplitDefault           = "default"
	SplitClearing          = "clearing"
	clearingText           = "Zero-balance clearing"
)

// SplittingRule is the splitting configuration of one source type on one ledger.
type SplittingRule struct {
	ID                         string            `json:"id"`
	LedgerID                   string            `json:"ledgerId"`
	SourceType                 string            `json:"sourceType"`
	SplittingDimensions        []string          `json:"splittingDimensions"`
	ZeroBalanceClearingAccount string            `json:"zeroBalanceClearingAccount"`
	DefaultSplittingValue      map[string]string `json:"defaultSplittingValue"`
	Active                     bool              `json:"active"`
	Version                    int               `json:"version"`
	CreatedAt                  time.Time         `json:"createdAt"`
	UpdatedAt                  time.Time         `json:"updatedAt"`
}

// SplitTrace records one line the splitting stage changed or created. LineIndex is the line's
// index after splitting; SourceIndex is the input line it came from (-1 for clearing lines).
type SplitTrace struct {
	LineIndex   int               `json:"lineIndex"`
	SourceIndex int               `json:"sourceIndex"`
	Kind        string            `json:"kind"`
	Dimensions  map[string]string `json:"dimensions"`
	Amount      string            `json:"amount"`
}

// ValidateSplittingRule returns field -> message for every problem of a rule definition.
func ValidateSplittingRule(r SplittingRule, ledgerAttrs []string) map[string]string {
	out := map[string]string{}
	if n := len(r.SourceType); n < 1 || n > 60 {
		out["sourceType"] = "must be 1 to 60 characters"
	}
	if n := len(r.SplittingDimensions); n < 1 || n > MaxSplittingDimensions {
		out["splittingDimensions"] = fmt.Sprintf("must name 1 to %d dimensions", MaxSplittingDimensions)
	}
	defined := map[string]bool{}
	for _, a := range ledgerAttrs {
		defined[a] = true
	}
	chosen := map[string]bool{}
	for _, d := range r.SplittingDimensions {
		switch {
		case !defined[d]:
			out["splittingDimensions"] = fmt.Sprintf("dimension %s is not defined on this ledger", d)
		case chosen[d]:
			out["splittingDimensions"] = fmt.Sprintf("dimension %s is listed twice", d)
		}
		chosen[d] = true
	}
	for k, v := range r.DefaultSplittingValue {
		switch {
		case !chosen[k]:
			out["defaultSplittingValue."+k] = "is not one of the splitting dimensions"
		case !dimValueRe.MatchString(v):
			out["defaultSplittingValue."+k] = fmt.Sprintf("value %q is not a valid dimension value", v)
		}
	}
	return out
}

// splitKey is the tuple of splitting dimension values of a line.
func splitKey(dims []string, line LineInput) string {
	vals := make([]string, len(dims))
	for i, d := range dims {
		vals[i] = line.Dimensions[d]
	}
	return strings.Join(vals, "\x1f")
}

func splitDims(dims []string, key string) map[string]string {
	out := map[string]string{}
	for i, v := range strings.Split(key, "\x1f") {
		if v != "" {
			out[dims[i]] = v
		}
	}
	return out
}

func signed(l LineInput) decimal.Decimal {
	if l.Side == SideCredit {
		return l.Amount.Neg()
	}
	return l.Amount
}

func cloneDims(m map[string]string) map[string]string {
	out := make(map[string]string, len(m)+1)
	for k, v := range m {
		out[k] = v
	}
	return out
}

// ApplySplitting runs the splitting stage on an entry. It does nothing when the entry does not
// balance (the balance check reports that) or when no line is unsplit and every key balances.
// Original lines keep their index (an unsplit line's first part stays in place; further parts
// and clearing lines are appended), so violations of the other stages still point at the
// caller's lines. clearing is the zero-balance account to post to; empty means none is set.
func ApplySplitting(rule SplittingRule, currency string, lines []LineInput) ([]LineInput, []SplitTrace, []Violation) {
	dims := rule.SplittingDimensions
	if len(dims) == 0 || len(lines) == 0 {
		return lines, nil, nil
	}
	total := decimal.Zero
	for _, l := range lines {
		total = total.Add(signed(l))
	}
	if !total.IsZero() {
		return lines, nil, nil
	}
	minor := money.MinorUnits(currency)
	for _, l := range lines {
		// A line finer than the currency's minor unit is refused by the scale check; splitting
		// it would round the parts and hide that error.
		if !l.Amount.Equal(l.Amount.Round(minor)) {
			return lines, nil, nil
		}
	}
	var viol []Violation
	var traces []SplitTrace
	out := make([]LineInput, len(lines))
	var unsplit []int // all splitting dimensions blank
	weights := map[string]decimal.Decimal{}
	for i, l := range lines {
		out[i] = l
		blank := 0
		for _, d := range dims {
			if l.Dimensions[d] == "" {
				blank++
			}
		}
		switch {
		case blank == len(dims):
			unsplit = append(unsplit, i)
		case blank > 0:
			// partly carried: the missing dimensions come from the default values
			dd := cloneDims(l.Dimensions)
			filled := true
			for _, d := range dims {
				if dd[d] != "" {
					continue
				}
				v := rule.DefaultSplittingValue[d]
				if v == "" {
					idx := i
					filled = false
					viol = append(viol, Violation{Code: CodeSplitNoSource, Field: "lines.dimensions." + d, LineIndex: &idx,
						Message: fmt.Sprintf("Line %d cannot inherit %s: no carrying line and no default value.", i, d)})
					continue
				}
				dd[d] = v
			}
			out[i].Dimensions = dd
			if filled {
				traces = append(traces, SplitTrace{LineIndex: i, SourceIndex: i, Kind: SplitDefault, Dimensions: splitDims(dims, splitKey(dims, out[i])),
					Amount: l.Amount.StringFixed(minor)})
			}
			weights[splitKey(dims, out[i])] = weights[splitKey(dims, out[i])].Add(signed(out[i]))
		default:
			weights[splitKey(dims, l)] = weights[splitKey(dims, l)].Add(signed(l))
		}
	}
	// Proportions come from the absolute net of each key; keys that net to zero carry no weight.
	keys := make([]string, 0, len(weights))
	for k, w := range weights {
		if w.IsZero() {
			delete(weights, k)
			continue
		}
		weights[k] = w.Abs()
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, i := range unsplit {
		l := out[i]
		if l.Amount.IsZero() {
			continue
		}
		if len(keys) == 0 {
			dd := cloneDims(l.Dimensions)
			complete := true
			for _, d := range dims {
				if v := rule.DefaultSplittingValue[d]; v != "" {
					dd[d] = v
				} else {
					complete = false
				}
			}
			if !complete {
				idx := i
				viol = append(viol, Violation{Code: CodeSplitNoSource, Field: "lines.dimensions", LineIndex: &idx,
					Message: fmt.Sprintf("Line %d cannot inherit %s: no carrying line and no default value.", i, strings.Join(dims, ", "))})
				continue
			}
			out[i].Dimensions = dd
			traces = append(traces, SplitTrace{LineIndex: i, SourceIndex: i, Kind: SplitDefault, Dimensions: splitDims(dims, splitKey(dims, out[i])),
				Amount: l.Amount.StringFixed(minor)})
			continue
		}
		shares := proportion(l.Amount, keys, weights, minor, minor)
		// A statistical quantity (BR-11.2) is divided in the same proportions,
		// so the parts add up to the line's quantity instead of repeating it.
		var qtyShares []decimal.Decimal
		if !l.Quantity.IsZero() {
			qtyShares = proportion(l.Quantity, keys, weights, QuantityScale, minor)
		}
		first := true
		for j, k := range keys {
			if shares[j].IsZero() && (qtyShares == nil || qtyShares[j].IsZero()) {
				continue
			}
			part := l
			part.Dimensions = cloneDims(l.Dimensions)
			for d, v := range splitDims(dims, k) {
				part.Dimensions[d] = v
			}
			part.Amount = shares[j]
			if qtyShares != nil {
				part.Quantity = qtyShares[j]
				if part.Quantity.IsZero() {
					part.Unit = ""
				}
			}
			idx := i
			if first {
				out[i] = part
				first = false
			} else {
				idx = len(out)
				out = append(out, part)
			}
			traces = append(traces, SplitTrace{LineIndex: idx, SourceIndex: i, Kind: SplitInherited,
				Dimensions: splitDims(dims, k), Amount: shares[j].StringFixed(minor)})
		}
	}
	if len(viol) > 0 {
		return lines, nil, viol
	}
	// Zero balance: every key must net to zero; the difference goes to the clearing account.
	nets := map[string]decimal.Decimal{}
	for _, l := range out {
		if splitKey(dims, l) == strings.Repeat("\x1f", len(dims)-1) {
			continue
		}
		nets[splitKey(dims, l)] = nets[splitKey(dims, l)].Add(signed(l))
	}
	var open []string
	for k, n := range nets {
		if !n.IsZero() {
			open = append(open, k)
		}
	}
	sort.Strings(open)
	for _, k := range open {
		if rule.ZeroBalanceClearingAccount == "" {
			return lines, nil, []Violation{{Code: CodeSplitClearingMissing, Field: "zeroBalanceClearingAccount",
				Message: "Document splitting is active but no zero-balance clearing account is set for this ledger and source type."}}
		}
		n := nets[k]
		side := SideCredit
		if n.IsNegative() {
			side = SideDebit
		}
		out = append(out, LineInput{Account: rule.ZeroBalanceClearingAccount, Dimensions: splitDims(dims, k), Side: side,
			Amount: n.Abs(), Currency: currency, Text: clearingText})
		traces = append(traces, SplitTrace{LineIndex: len(out) - 1, SourceIndex: -1, Kind: SplitClearing,
			Dimensions: splitDims(dims, k), Amount: n.Abs().StringFixed(minor)})
	}
	return out, traces, nil
}

// proportion divides amount over keys in proportion to weights, in whole units of 10^-scale;
// the units left by rounding down go to the keys with the largest remainders (ties by key
// order). Weights are amounts with at most weightScale decimals.
func proportion(amount decimal.Decimal, keys []string, weights map[string]decimal.Decimal, scale, weightScale int32) []decimal.Decimal {
	units := amount.Shift(scale).BigInt()
	wi := make([]*big.Int, len(keys))
	sum := new(big.Int)
	for i, k := range keys {
		wi[i] = weights[k].Shift(weightScale).BigInt()
		sum.Add(sum, wi[i])
	}
	shares := make([]*big.Int, len(keys))
	rems := make([]*big.Int, len(keys))
	given := new(big.Int)
	for i := range keys {
		num := new(big.Int).Mul(units, wi[i])
		shares[i], rems[i] = new(big.Int).QuoRem(num, sum, new(big.Int))
		given.Add(given, shares[i])
	}
	left := new(big.Int).Sub(units, given).Int64()
	order := make([]int, len(keys))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return rems[order[a]].Cmp(rems[order[b]]) > 0 })
	for n := int64(0); n < left; n++ {
		shares[order[n%int64(len(order))]].Add(shares[order[n%int64(len(order))]], big.NewInt(1))
	}
	out := make([]decimal.Decimal, len(keys))
	for i := range keys {
		out[i] = decimal.NewFromBigInt(shares[i], -scale)
	}
	return out
}
