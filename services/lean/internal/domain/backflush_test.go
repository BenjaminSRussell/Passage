package domain

import (
	"errors"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func sauceWork() []ActivityWork {
	return []ActivityWork{
		{ActivityID: "mix", Sequence: 1, Name: "Mix", Kind: "process", CycleTimeS: 45, Jobs: 10, Qty: dec("250")},
		{ActivityID: "move", Sequence: 2, Name: "Move to filler", Kind: "transfer", CycleTimeS: 0, Jobs: 10, Qty: dec("250")},
		{ActivityID: "fill", Sequence: 3, Name: "Fill", Kind: "process", CycleTimeS: 30, Jobs: 9, Qty: dec("221.5")},
		{ActivityID: "label", Sequence: 4, Name: "Label (vendor)", Kind: "service", CycleTimeS: 20, Jobs: 9, Qty: dec("221.5")},
	}
}

// FR-24.12: conversion is cycle time × processed quantity × hourly rate per
// activity, rounded once per activity, and the total is the sum of the lines.
func TestBackflushConversion_FR2412(t *testing.T) {
	bf, err := BackflushConversion("USD", sauceWork(), map[string]decimal.Decimal{
		"mix": dec("90"), "move": dec("0"), "fill": dec("72.5"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bf.Lines) != 4 {
		t.Fatalf("lines %+v", bf.Lines)
	}
	mix, fill, label := bf.Lines[0], bf.Lines[2], bf.Lines[3]
	// Mix: 45 s × 250 = 11 250 s = 3.125 h × 90 = 281.25.
	if mix.Hours.String() != "3.125" || mix.Cost.Value.String() != "281.25" || mix.Rate.Value.String() != "90" {
		t.Fatalf("mix %+v", mix)
	}
	// Fill: 30 s × 221.5 = 6 645 s = 1.84583… h × 72.5 = 133.8229… → 133.82.
	if fill.Hours.String() != "1.8458" || fill.Cost.Value.String() != "133.82" {
		t.Fatalf("fill hours %s cost %s", fill.Hours, fill.Cost.Value)
	}
	// The vendor activity is priced by its purchase agreement, not here.
	if label.Cost != nil || label.Rate != nil || label.Hours.String() != "1.2306" {
		t.Fatalf("service %+v", label)
	}
	if bf.Conversion.Currency != "USD" || bf.Conversion.Value.String() != "415.07" {
		t.Fatalf("total %s", bf.Conversion)
	}
}

func TestBackflushConversion_Refusals(t *testing.T) {
	cases := map[string]struct {
		currency string
		rates    map[string]decimal.Decimal
		want     string
	}{
		"missing rate for worked activity": {"USD", map[string]decimal.Decimal{"mix": dec("90"), "move": dec("0")}, "Fill completed 9 jobs"},
		"unknown activity":                 {"USD", map[string]decimal.Decimal{"mix": dec("1"), "move": dec("1"), "fill": dec("1"), "other": dec("1")}, "not an activity of this flow"},
		"rate for service":                 {"USD", map[string]decimal.Decimal{"mix": dec("1"), "move": dec("1"), "fill": dec("1"), "label": dec("1")}, "service activity"},
		"negative":                         {"USD", map[string]decimal.Decimal{"mix": dec("-1"), "move": dec("1"), "fill": dec("1")}, "zero or more"},
		"too precise":                      {"USD", map[string]decimal.Decimal{"mix": dec("1.0000001"), "move": dec("1"), "fill": dec("1")}, "more than 6 decimals"},
		"too large":                        {"USD", map[string]decimal.Decimal{"mix": dec("100000000000000"), "move": dec("1"), "fill": dec("1")}, "below 10^14"},
		"currency":                         {"usd", map[string]decimal.Decimal{"mix": dec("1"), "move": dec("1"), "fill": dec("1")}, "currency"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := BackflushConversion(c.currency, sauceWork(), c.rates)
			var de *Error
			if !errors.As(err, &de) || de.Code != CodeInvalid || !strings.Contains(de.Detail, c.want) {
				t.Fatalf("got %v, want %q", err, c.want)
			}
		})
	}
}

// An activity without jobs in the period needs no rate and costs nothing; a
// zero-decimal currency rounds to whole units.
func TestBackflushConversion_IdleActivityAndMinorUnit(t *testing.T) {
	work := []ActivityWork{
		{ActivityID: "mix", Name: "Mix", Kind: "process", CycleTimeS: 45, Jobs: 1, Qty: dec("10")},
		{ActivityID: "fill", Name: "Fill", Kind: "process", CycleTimeS: 30, Jobs: 0, Qty: decimal.Zero},
	}
	bf, err := BackflushConversion("JPY", work, map[string]decimal.Decimal{"mix": dec("1000")})
	if err != nil {
		t.Fatal(err)
	}
	// 450 s = 0.125 h × 1000 = 125 JPY.
	if bf.Conversion.Value.String() != "125" || bf.Lines[1].Rate != nil || !bf.Lines[1].Cost.Value.IsZero() {
		t.Fatalf("%+v", bf)
	}
}

// Property: the total always equals the sum of the line costs exactly, each
// line at the currency's minor unit.
func TestBackflushConversion_TotalIsSumOfLines(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 8).Draw(t, "n")
		work := make([]ActivityWork, n)
		rates := map[string]decimal.Decimal{}
		for i := range work {
			id := string(rune('a' + i))
			work[i] = ActivityWork{ActivityID: id, Name: id, Kind: "process", CycleTimeS: rapid.IntRange(0, 7200).Draw(t, "cycle"),
				Jobs: rapid.IntRange(0, 50).Draw(t, "jobs"), Qty: decimal.New(rapid.Int64Range(0, 1_000_000).Draw(t, "qty"), -3)}
			rates[id] = decimal.New(rapid.Int64Range(0, 50_000_000).Draw(t, "rate"), -4)
		}
		bf, err := BackflushConversion("EUR", work, rates)
		if err != nil {
			t.Fatal(err)
		}
		sum := decimal.Zero
		for _, l := range bf.Lines {
			if !l.Cost.Value.Equal(l.Cost.Value.Round(2)) {
				t.Fatalf("line not at minor unit: %s", l.Cost.Value)
			}
			sum = sum.Add(l.Cost.Value)
		}
		if !sum.Equal(bf.Conversion.Value) {
			t.Fatalf("total %s, lines %s", bf.Conversion.Value, sum)
		}
	})
}
