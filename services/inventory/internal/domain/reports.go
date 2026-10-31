package domain

import (
	"sort"

	"github.com/shopspring/decimal"
)

// Report error codes (PRD 3.16.14).
const (
	CodeTraceKey = "INV-TRC-001" // a trace needs a batch or a serial (service code, not in the 3.16.18 catalog)
)

// Trace directions of the batch / serial trace (3.16.14): forward follows the
// stock out (issues), backward follows it back to its receipts, both lists
// every movement in date order.
const (
	TraceBoth     = "BOTH"
	TraceForward  = "FORWARD"
	TraceBackward = "BACKWARD"
)

// ValidTraceDirection reports whether d is an accepted trace direction.
func ValidTraceDirection(d string) bool {
	return d == TraceBoth || d == TraceForward || d == TraceBackward
}

// Value bases of a count variance line: JOURNAL values the variance at the cost
// of the posted COUNT journal line, ESTIMATE at the running average of the
// on-hand row today (the sheet is not posted yet).
const (
	ValueBasisJournal  = "JOURNAL"
	ValueBasisEstimate = "ESTIMATE"
)

// CountVarianceQty is the variance a count line reports: the posted variance
// once the sheet is posted (it is measured against the book at posting,
// BR-16.8), the counted minus the expected quantity before that.
func CountVarianceQty(expected, counted decimal.Decimal, posted *decimal.Decimal) decimal.Decimal {
	if posted != nil {
		return *posted
	}
	return counted.Sub(expected)
}

// CountVarianceTotals sums the variance report per counter.
type CountVarianceTotals struct {
	Counter       string
	Lines         int
	GainValue     decimal.Decimal // value of positive variances
	LossValue     decimal.Decimal // value of negative variances, as a positive number
	NetValue      decimal.Decimal
	AbsVarianceQt decimal.Decimal
}

// SummarizeVarianceByCounter groups (counter, variance qty, variance value)
// triples by counter. Counters sort by name; lines with no counter are
// reported under the empty name.
func SummarizeVarianceByCounter(counters []string, qty, value []decimal.Decimal) []CountVarianceTotals {
	by := map[string]*CountVarianceTotals{}
	for i, c := range counters {
		t, ok := by[c]
		if !ok {
			t = &CountVarianceTotals{Counter: c}
			by[c] = t
		}
		t.Lines++
		t.AbsVarianceQt = t.AbsVarianceQt.Add(qty[i].Abs())
		t.NetValue = t.NetValue.Add(value[i])
		switch {
		case value[i].IsPositive():
			t.GainValue = t.GainValue.Add(value[i])
		case value[i].IsNegative():
			t.LossValue = t.LossValue.Add(value[i].Neg())
		}
	}
	out := make([]CountVarianceTotals, 0, len(by))
	for _, t := range by {
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Counter < out[j].Counter })
	return out
}

// MovesPhysicalStock reports whether a transaction in the given issue or
// receipt status is part of physical stock (posted, received, registered,
// deducted or picked), as opposed to ordered or reserved quantities.
func MovesPhysicalStock(issueStatus, receiptStatus string) bool {
	switch issueStatus {
	case "PICKED", "DEDUCTED", "SOLD":
		return true
	}
	switch receiptStatus {
	case "REGISTERED", "RECEIVED", "PURCHASED":
		return true
	}
	return false
}
