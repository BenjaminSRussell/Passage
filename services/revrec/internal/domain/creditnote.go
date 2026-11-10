package domain

import (
	"slices"

	"github.com/shopspring/decimal"
)

// Credit-note reversal (BR-29.6, AC-29.6). Billing books a credit note as the mirror of its original invoice at the revrec
// allocation: it debits 2400 deferred revenue by the deferred part of each credited line, whatever revrec has recognized of it
// since (billing's creditnote.booked carries that amount per line). Revrec treats the credit as a reduction of the transaction
// price of the credited obligations that applies to what is still unsatisfied first (ASC 606-10-32-42 to 32-45, the change
// allocated on the allocation basis of the contract, applied prospectively to the remaining schedule):
//
//   - the deferred amount of a line is split over the deferred (RATABLE, MILESTONE) obligations of its product in proportion to
//     their allocations (largest remainder at the currency's minor unit);
//   - each obligation's share reduces its unrecognized schedule first, pro rata over its SCHEDULED lines, so the remaining
//     service is still recognized on its plan at the lower price; a line reduced to zero is REVERSED;
//   - what exceeds the unrecognized amount reverses revenue already recognized (Dr 4000 / Cr 2400), in the credit note's
//     posting period, and never more than the obligation recognized less what earlier credit notes reversed.
//
// The roll-forward shows the line's deferred amount as a negative adjustment and the recognized revenue reversed as a
// reversal back into deferred revenue, so its closing balance follows GL 2400 through the credit note.

// ReversalPlan is how one obligation absorbs its share of a credit: Reduced comes off its SCHEDULED lines, Reversed is revenue
// already recognized that is reversed. Reduced + Reversed is the share.
type ReversalPlan struct {
	Reduced  decimal.Decimal
	Reversed decimal.Decimal
}

// PlanReversal applies share to an obligation with unrecognized scheduled amount and recognized revenue not yet reversed by
// an earlier credit note. ok is false when the share exceeds both together (a credit of more than the obligation holds).
func PlanReversal(share, unrecognized, recognized decimal.Decimal) (ReversalPlan, bool) {
	reduced := decimal.Min(share, decimal.Max(unrecognized, decimal.Zero))
	reversed := share.Sub(reduced)
	return ReversalPlan{Reduced: reduced, Reversed: reversed}, !reversed.GreaterThan(recognized)
}

// Apportion splits amount over weights in proportion, at places decimals, by largest remainder (ties to the earlier weight), so
// the parts sum to amount exactly when amount has at most places decimals. Zero total weight gives zero parts.
func Apportion(amount decimal.Decimal, weights []decimal.Decimal, places int32) []decimal.Decimal {
	out := make([]decimal.Decimal, len(weights))
	total := decimal.Zero
	for _, w := range weights {
		total = total.Add(w)
	}
	if !total.IsPositive() {
		for i := range out {
			out[i] = decimal.Zero
		}
		return out
	}
	exact := make([]decimal.Decimal, len(weights))
	left := amount
	for i, w := range weights {
		if w.Equal(total) {
			exact[i] = amount
		} else {
			exact[i] = amount.Mul(w).DivRound(total, 16)
		}
		out[i] = exact[i].RoundFloor(places)
		left = left.Sub(out[i])
	}
	order := make([]int, len(weights))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int { return exact[b].Sub(out[b]).Cmp(exact[a].Sub(out[a])) })
	unit := decimal.New(1, -places)
	for _, i := range order {
		if left.LessThan(unit) {
			break
		}
		out[i], left = out[i].Add(unit), left.Sub(unit)
	}
	return out
}

// ReduceLines takes reduce off scheduled line amounts pro rata (Apportion at places), never taking a line below zero; reduce
// equal to the total zeroes every line. It returns the reduction per line.
func ReduceLines(amounts []decimal.Decimal, reduce decimal.Decimal, places int32) []decimal.Decimal {
	total := decimal.Zero
	for _, a := range amounts {
		total = total.Add(a)
	}
	if reduce.GreaterThanOrEqual(total) {
		return slices.Clone(amounts)
	}
	parts := Apportion(reduce, amounts, places)
	// A line can only receive more than its amount by one minor unit of rounding; move that excess to lines with room.
	excess := decimal.Zero
	for i := range parts {
		if parts[i].GreaterThan(amounts[i]) {
			excess, parts[i] = excess.Add(parts[i].Sub(amounts[i])), amounts[i]
		}
	}
	for i := range parts {
		if !excess.IsPositive() {
			break
		}
		room := amounts[i].Sub(parts[i])
		move := decimal.Min(room, excess)
		parts[i], excess = parts[i].Add(move), excess.Sub(move)
	}
	return parts
}
