package domain

import (
	"testing"
	"time"
)

func TestNextVersionCode_NumberWidthAndOverflow(t *testing.T) {
	n := Nomenclature{Code: "VNN", Separator: "-", Segments: []NomenSegment{
		{Type: SegConstant, Text: "V"},
		{Type: SegNumber, Width: 2, Start: 1},
	}}
	got, err := n.Next("V-01", time.Time{})
	if err != nil || got != "V-02" {
		t.Fatalf("V-01 -> %q, %v; want V-02", got, err)
	}
	if _, err := n.Next("V-99", time.Time{}); !HasCode(err, CodeNomenOverflow) {
		t.Fatalf("V-99 overflow = %v; want %s", err, CodeNomenOverflow)
	}
}

func TestNextVersionCode_LettersRollZToAA(t *testing.T) {
	n := Nomenclature{Code: "A", Segments: []NomenSegment{{Type: SegLetter}}}
	got, err := n.Next("Z", time.Time{})
	if err != nil || got != "AA" {
		t.Fatalf("Z -> %q, %v; want AA", got, err)
	}
	skip := Nomenclature{Code: "AIO", Segments: []NomenSegment{{Type: SegLetter, SkipIO: true}}}
	got, err = skip.Next("H", time.Time{})
	if err != nil || got != "J" {
		t.Fatalf("skip I: H -> %q, %v; want J", got, err)
	}
}

func TestNextVersionCode_DateUsesReleaseDay(t *testing.T) {
	n := Nomenclature{Code: "D", Separator: "", Segments: []NomenSegment{
		{Type: SegConstant, Text: "R"},
		{Type: SegDate, Format: "yyyyMMdd"},
	}}
	day := time.Date(2026, 7, 1, 15, 0, 0, 0, time.FixedZone("eng", -5*3600))
	got, err := n.Next("", day)
	if err != nil || got != "R20260701" {
		t.Fatalf("date segment = %q, %v; want R20260701 in the engineering org zone", got, err)
	}
}

func TestNextVersionCode_FirstVersionUsesStart(t *testing.T) {
	n := Nomenclature{Code: "VNN", Separator: "-", Segments: []NomenSegment{
		{Type: SegConstant, Text: "V"},
		{Type: SegNumber, Width: 2, Start: 1},
	}}
	got, err := n.Next("", time.Time{})
	if err != nil || got != "V-01" {
		t.Fatalf("empty last = %q, %v; want V-01", got, err)
	}
}

func TestNomenclature_RejectsUnknownSegment(t *testing.T) {
	n := Nomenclature{Code: "X", Segments: []NomenSegment{{Type: "uuid"}}}
	if err := n.Validate(); err == nil {
		t.Fatal("expected validation error")
	}
}
