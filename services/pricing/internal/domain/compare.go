package domain

import "github.com/shopspring/decimal"

// LineComparison sets one line's result under procedure version A beside version B (FR-26.13).
// Changed is true when the net price, the line amount, the error or the applied conditions differ.
type LineComparison struct {
	No           int             `json:"lineNo"`
	NetUnitA     decimal.Decimal `json:"netUnitPriceA"`
	NetUnitB     decimal.Decimal `json:"netUnitPriceB"`
	NetUnitDelta decimal.Decimal `json:"netUnitPriceDelta"`
	LineNetA     decimal.Decimal `json:"lineNetA"`
	LineNetB     decimal.Decimal `json:"lineNetB"`
	LineNetDelta decimal.Decimal `json:"lineNetDelta"`
	ErrorA       string          `json:"errorCodeA,omitempty"`
	ErrorB       string          `json:"errorCodeB,omitempty"`
	// OnlyA and OnlyB name the condition steps (type and record) that one version applied and the other did not.
	OnlyA   []string `json:"appliedOnlyA"`
	OnlyB   []string `json:"appliedOnlyB"`
	Changed bool     `json:"changed"`
}

// Comparison is the difference between two resolutions of the same request: the deltas are B minus A.
type Comparison struct {
	Identical          bool             `json:"identical"`
	SubtotalDelta      decimal.Decimal  `json:"subtotalDelta"`
	TotalDiscountDelta decimal.Decimal  `json:"totalDiscountDelta"`
	SurchargeDelta     decimal.Decimal  `json:"surchargeDelta"`
	OrderNetDelta      decimal.Decimal  `json:"orderNetDelta"`
	ChangedLines       int              `json:"changedLines"`
	Lines              []LineComparison `json:"lines"`
}

// conditionSet lists the conditions of a result as "TYPE:record" keys (with " (statistical)" for a
// statistical step), so a record applied at another step number counts as the same condition.
func conditionSet(applied []Applied) map[string]bool {
	out := make(map[string]bool, len(applied))
	for _, a := range applied {
		out[appliedKey(a)] = true
	}
	return out
}

func appliedKey(a Applied) string {
	k := string(a.Type) + ":" + a.RecordID
	if a.Statistical {
		k += " (statistical)"
	}
	return k
}

func only(from []Applied, other map[string]bool) []string {
	out := []string{}
	for _, a := range from {
		if k := appliedKey(a); !other[k] {
			out = append(out, k)
		}
	}
	return out
}

// Compare lines up two results of the same request by line number and reports what differs.
// It is pure: the results carry everything it reads. Both results must come from the same
// lines, so a line missing on one side is reported as zero there with its error code.
func Compare(a, b Result) Comparison {
	n := len(a.Lines)
	if len(b.Lines) > n {
		n = len(b.Lines)
	}
	out := Comparison{
		SubtotalDelta:      b.Subtotal.Sub(a.Subtotal),
		TotalDiscountDelta: b.TotalDiscount.Sub(a.TotalDiscount),
		SurchargeDelta:     b.Surcharge.Sub(a.Surcharge),
		OrderNetDelta:      b.OrderNet.Sub(a.OrderNet),
		Lines:              make([]LineComparison, 0, n),
	}
	for i := 0; i < n; i++ {
		var la, lb LineResult
		if i < len(a.Lines) {
			la = a.Lines[i]
		}
		if i < len(b.Lines) {
			lb = b.Lines[i]
		}
		no := la.No
		if no == 0 {
			no = lb.No
		}
		lc := LineComparison{No: no,
			NetUnitA: la.NetUnit, NetUnitB: lb.NetUnit, NetUnitDelta: lb.NetUnit.Sub(la.NetUnit),
			LineNetA: la.LineNet, LineNetB: lb.LineNet, LineNetDelta: lb.LineNet.Sub(la.LineNet),
			ErrorA: la.Error, ErrorB: lb.Error,
			OnlyA: only(la.Applied, conditionSet(lb.Applied)), OnlyB: only(lb.Applied, conditionSet(la.Applied)),
		}
		lc.Changed = !lc.NetUnitDelta.IsZero() || !lc.LineNetDelta.IsZero() || la.Error != lb.Error || len(lc.OnlyA) > 0 || len(lc.OnlyB) > 0
		if lc.Changed {
			out.ChangedLines++
		}
		out.Lines = append(out.Lines, lc)
	}
	out.Identical = out.ChangedLines == 0 && out.SubtotalDelta.IsZero() && out.TotalDiscountDelta.IsZero() &&
		out.SurchargeDelta.IsZero() && out.OrderNetDelta.IsZero()
	return out
}
