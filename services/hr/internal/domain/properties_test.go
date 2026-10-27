package domain

import (
	"fmt"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

var propBase = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// propRange draws a half-open range inside the first 120 days; about half of
// the ranges are open-ended.
func propRange(t *rapid.T, label string) Range {
	from := rapid.IntRange(0, 60).Draw(t, label+"From")
	r := Range{From: propBase.AddDate(0, 0, from)}
	if rapid.Bool().Draw(t, label+"Open") {
		return r
	}
	to := propBase.AddDate(0, 0, from+rapid.IntRange(1, 60).Draw(t, label+"Len"))
	r.To = &to
	return r
}

// referenceManager is an independent model of BR-42.1: index the active
// primary incumbents and reports-to edges first, then walk up the chain.
// First match wins in slice order, the same tie-break Manager uses.
func referenceManager(worker string, at time.Time, asg []Assignment, edges []Edge, depth int) (string, bool) {
	own := ""
	for _, a := range asg {
		if a.Primary && a.WorkerID == worker && a.Validity.Contains(at) {
			own = a.PositionID
			break
		}
	}
	if own == "" {
		return "", false
	}
	parentOf := map[string]string{}
	for _, e := range edges {
		if _, seen := parentOf[e.ChildPositionID]; !seen && e.Validity.Contains(at) {
			parentOf[e.ChildPositionID] = e.ParentPositionID
		}
	}
	incumbent := map[string]string{}
	for _, a := range asg {
		if _, seen := incumbent[a.PositionID]; !seen && a.Primary && a.Validity.Contains(at) {
			incumbent[a.PositionID] = a.WorkerID
		}
	}
	pos := own
	for i := 0; i < depth; i++ {
		parent, ok := parentOf[pos]
		if !ok {
			return "", false
		}
		if w, ok := incumbent[parent]; ok {
			return w, true
		}
		pos = parent
	}
	return "", false
}

// TestPropManagerMatchesReferenceModel is AC-42.1 over random assignments,
// edges (including cycles) and instants: Manager agrees with the model.
func TestPropManagerMatchesReferenceModel(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		const positions = 6
		var asg []Assignment
		for i, n := 0, rapid.IntRange(0, 12).Draw(t, "assignments"); i < n; i++ {
			asg = append(asg, Assignment{
				WorkerID:   fmt.Sprintf("w%d", rapid.IntRange(0, 5).Draw(t, "worker")),
				PositionID: fmt.Sprintf("p%d", rapid.IntRange(0, positions-1).Draw(t, "position")),
				Primary:    rapid.Bool().Draw(t, "primary"),
				Validity:   propRange(t, "asg"),
			})
		}
		var edges []Edge
		for i, n := 0, rapid.IntRange(0, 10).Draw(t, "edges"); i < n; i++ {
			child := rapid.IntRange(0, positions-1).Draw(t, "child")
			parent := rapid.IntRange(0, positions-1).Draw(t, "parent")
			if child == parent {
				continue
			}
			edges = append(edges, Edge{
				ChildPositionID:  fmt.Sprintf("p%d", child),
				ParentPositionID: fmt.Sprintf("p%d", parent),
				Validity:         propRange(t, "edge"),
			})
		}
		worker := fmt.Sprintf("w%d", rapid.IntRange(0, 5).Draw(t, "subject"))
		at := propBase.AddDate(0, 0, rapid.IntRange(0, 120).Draw(t, "day"))

		got, gotOK := Manager(worker, at, asg, edges, 10)
		want, wantOK := referenceManager(worker, at, asg, edges, 10)
		if got != want || gotOK != wantOK {
			t.Fatalf("Manager(%s, %s) = (%q, %v); reference = (%q, %v)", worker, at, got, gotOK, want, wantOK)
		}
	})
}

// TestPropManagerNeverReturnsSelf guards the common cause of duplicate-manager
// answers. w0 holds p0 and later p1 as primary, never at the same time (the
// database forbids two primaries per worker at an instant), and p0 reports to
// p1. At every instant w0 must not be reported as their own manager.
func TestPropManagerNeverReturnsSelf(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		mid := propBase.AddDate(0, 0, rapid.IntRange(1, 60).Draw(t, "switch"))
		asg := []Assignment{
			{WorkerID: "w0", PositionID: "p0", Primary: true, Validity: Range{From: propBase, To: &mid}},
			{WorkerID: "w0", PositionID: "p1", Primary: true, Validity: Range{From: mid}},
			{WorkerID: "w1", PositionID: "p1", Primary: true, Validity: Range{From: propBase}},
		}
		edges := []Edge{{ChildPositionID: "p0", ParentPositionID: "p1", Validity: propRange(t, "edge")}}
		at := propBase.AddDate(0, 0, rapid.IntRange(0, 120).Draw(t, "day"))
		if got, ok := Manager("w0", at, asg, edges, 10); ok && got == "w0" {
			t.Fatalf("w0 reports to themselves on %s", at)
		}
	})
}

// TestPropWouldCycleDetectsLoops is BR-42.7 on a chain p0 <- p1 <- ... <- p4
// (each p(k+1) reports to pk). Making pi report to pj closes a loop exactly
// when pi is on the chain above pj, that is when i <= j.
func TestPropWouldCycleDetectsLoops(t *testing.T) {
	const n = 5
	var edges []Edge
	for k := 0; k < n-1; k++ {
		edges = append(edges, Edge{
			ChildPositionID:  fmt.Sprintf("p%d", k+1),
			ParentPositionID: fmt.Sprintf("p%d", k),
			Validity:         Range{From: propBase},
		})
	}
	rapid.Check(t, func(t *rapid.T) {
		i := rapid.IntRange(0, n-1).Draw(t, "child")
		j := rapid.IntRange(0, n-1).Draw(t, "parent")
		got := WouldCycle(edges, fmt.Sprintf("p%d", i), fmt.Sprintf("p%d", j), propBase)
		if want := i <= j; got != want {
			t.Fatalf("WouldCycle(p%d -> p%d) = %v, want %v", i, j, got, want)
		}
	})
}

// TestPropCheckFTEWithoutOverfillNeverExceeds is FR-42.3: with overfill off,
// an accepted assignment never takes a position past its FTE; with overfill on,
// nothing is refused.
func TestPropCheckFTEWithoutOverfillNeverExceeds(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		fte := decimal.New(int64(rapid.IntRange(5, 200).Draw(t, "fte")), -2)
		existing := decimal.New(int64(rapid.IntRange(0, 300).Draw(t, "existing")), -2)
		add := decimal.New(int64(rapid.IntRange(5, 200).Draw(t, "add")), -2)
		err := CheckFTE(fte, false, existing, add)
		fits := existing.Add(add).LessThanOrEqual(fte)
		if (err == nil) != fits {
			t.Fatalf("CheckFTE(fte=%s, existing=%s, add=%s) err=%v, fits=%v", fte, existing, add, err, fits)
		}
		if err := CheckFTE(fte, true, existing, add); err != nil {
			t.Fatalf("overfill position refused: %v", err)
		}
	})
}

// TestPropAccrualLedgerNeverExceedsMax is BR-42.3's cap: the total never passes
// max_balance, and the discarded part is exactly the amount that did not fit.
func TestPropAccrualLedgerNeverExceedsMax(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		limit := decimal.New(int64(rapid.IntRange(100, 3000).Draw(t, "limit")), -2)
		var l Ledger
		for i, n := 0, rapid.IntRange(1, 30).Draw(t, "accruals"); i < n; i++ {
			amt := decimal.New(int64(rapid.IntRange(0, 400).Draw(t, "amount")), -2)
			before := l.Total()
			var discarded decimal.Decimal
			l, discarded = l.Accrue(amt, limit)
			if l.Total().GreaterThan(limit) {
				t.Fatalf("total %s exceeds max %s", l.Total(), limit)
			}
			if !before.Add(amt).Sub(discarded).Equal(l.Total()) {
				t.Fatalf("kept + discarded does not account for %s", amt)
			}
		}
	})
}

// TestPropAccrualAmountWithinRate checks that a prorated accrual is never
// negative and never more than the full-period rate.
func TestPropAccrualAmountWithinRate(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		rate := decimal.New(int64(rapid.IntRange(0, 500).Draw(t, "rate")), -2)
		days := rapid.IntRange(1, 31).Draw(t, "days")
		employed := rapid.IntRange(0, days).Draw(t, "employed")
		got := AccrualAmount(rate, employed, days, true)
		if got.GreaterThan(rate.Round(2)) || got.LessThan(decimal.Zero) {
			t.Fatalf("AccrualAmount(%s, %d, %d) = %s is outside [0, rate]", rate, employed, days, got)
		}
	})
}
