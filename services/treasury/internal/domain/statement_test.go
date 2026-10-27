package domain

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"
)

func TestSequenceGap_Table(t *testing.T) {
	seq := func(n int) *int { return &n }
	cases := []struct {
		name     string
		previous *int
		incoming int
		want     string // "" for no warning
	}{
		{"first numbered statement", nil, 7, ""},
		{"next in sequence", seq(7), 8, ""},
		{"one statement missing", seq(7), 9, "statement sequence gap: expected 8, received 9"},
		{"unnumbered statement", seq(7), 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SequenceGap(tc.previous, tc.incoming)
			switch {
			case tc.want == "" && got != nil:
				t.Fatalf("got warning %v, want none", got)
			case tc.want != "" && (got == nil || got.Code != CodeStatementGap || got.Detail != tc.want):
				t.Fatalf("got %v, want %s %q", got, CodeStatementGap, tc.want)
			}
		})
	}
}

func TestDuplicate_SameClosingReplaysOtherwiseConflicts(t *testing.T) {
	if err := Duplicate(decimal.RequireFromString("100.50"), decimal.RequireFromString("100.500000")); err != nil {
		t.Fatalf("equal closing balances in different scale: %v, want replay", err)
	}
	err := Duplicate(decimal.RequireFromString("100.50"), decimal.RequireFromString("100.51"))
	var de *Error
	if err == nil || !errors.As(err, &de) || de.Code != CodeStatementConflict {
		t.Fatalf("different closing: %v, want %s", err, CodeStatementConflict)
	}
}
