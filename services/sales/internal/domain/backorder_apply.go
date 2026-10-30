package domain

import (
	"errors"
	"fmt"
	"sort"

	"github.com/shopspring/decimal"
)

// DatedQuantity is the quantity of entries that carry a promised date: what a
// backorder run counts as confirmed for a line (CONFIRMED and BACKORDER
// schedule lines; an UNPROMISED line has no supply behind it).
func DatedQuantity(entries []ScheduleEntry) decimal.Decimal {
	sum := decimal.Zero
	for _, e := range entries {
		if e.Date != "" {
			sum = sum.Add(e.Quantity)
		}
	}
	return sum
}

// RescheduleLine applies a backorder run's result to one line's schedule so
// that exactly after units carry a promised date (FR-27.17, BR-27.11).
//
// A smaller quantity is taken from the latest dated entries first and becomes
// UNPROMISED; a larger one is taken from the UNPROMISED quantity and promised
// in one new ATP entry on gainDate, drawn on the supply bucket supplyDate. The
// entries are returned dated first in date order, the UNPROMISED rest last,
// with statuses following the line's requested date, and add up to the same
// quantity as current. An after above the line's whole quantity is an error.
func RescheduleLine(current []ScheduleEntry, requested string, after decimal.Decimal, gainDate, supplyDate string) ([]ScheduleEntry, error) {
	if after.IsNegative() {
		return nil, errors.New("quantity must not be negative")
	}
	var dated []ScheduleEntry
	open := decimal.Zero
	for _, e := range current {
		if e.Date == "" {
			open = open.Add(e.Quantity)
		} else {
			dated = append(dated, e)
		}
	}
	sort.SliceStable(dated, func(i, j int) bool { return dated[i].Date < dated[j].Date })
	have := DatedQuantity(dated)
	if after.GreaterThan(have.Add(open)) {
		return nil, fmt.Errorf("%s exceeds the line quantity %s", after, have.Add(open))
	}
	switch {
	case after.LessThan(have):
		cut := have.Sub(after)
		for i := len(dated) - 1; i >= 0 && cut.IsPositive(); i-- {
			take := decimal.Min(dated[i].Quantity, cut)
			dated[i].Quantity = dated[i].Quantity.Sub(take)
			cut = cut.Sub(take)
			open = open.Add(take)
		}
	case after.GreaterThan(have):
		if gainDate == "" {
			return nil, errors.New("a gaining line needs a date")
		}
		gain := after.Sub(have)
		open = open.Sub(gain)
		dated = append(dated, ScheduleEntry{Date: gainDate, Quantity: gain, Source: SchedSourceATP, BucketDate: supplyDate})
	}
	out := make([]ScheduleEntry, 0, len(dated)+1)
	for _, e := range dated {
		if !e.Quantity.IsPositive() {
			continue
		}
		e.Status = ScheduleStatus(e.Date, requested)
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Date < out[j].Date })
	if open.IsPositive() {
		out = append(out, ScheduleEntry{Quantity: open, Status: SchedUnpromised, Source: SchedSourceATP})
	}
	return out, nil
}
