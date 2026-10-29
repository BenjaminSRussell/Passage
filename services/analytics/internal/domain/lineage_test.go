package domain

import "testing"

func TestValidateEdges(t *testing.T) {
	chain := []Edge{
		{Source: "raw.ledger.journal_line", Target: "core.journal_line"},
		{Source: "core.journal_line", Target: "mart.fin_journal_line"},
	}
	tooMany := make([]Edge, MaxLineageEdges+1)
	cases := []struct {
		name  string
		edges []Edge
		field string
	}{
		{"valid", chain, ""},
		{"empty", nil, "edges"},
		{"over the cap", tooMany, "edges"},
		{"bad name", []Edge{chain[0], {Source: "Raw.Ledger", Target: "mart.x"}}, "edges[1]"},
		{"single-part name", []Edge{{Source: "journal_line", Target: "mart.x"}}, "edges[0]"},
		{"self loop", []Edge{{Source: "mart.x", Target: "mart.x"}}, "edges[0]"},
		{"duplicate", []Edge{chain[0], chain[1], chain[0]}, "edges[2]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateEdges(tc.edges)
			if tc.field == "" {
				if err != nil {
					t.Fatalf("want valid, got %v", err)
				}
				return
			}
			if err == nil || err.Fields[tc.field] == "" {
				t.Fatalf("want an error on %s, got %v", tc.field, err)
			}
		})
	}
}

func TestLineageDepth(t *testing.T) {
	for in, want := range map[int]int{0: MaxLineageDepth, 1: 1, MaxLineageDepth: MaxLineageDepth} {
		if got, ok := LineageDepth(in); !ok || got != want {
			t.Errorf("LineageDepth(%d) = %d, %v; want %d", in, got, ok, want)
		}
	}
	for _, in := range []int{-1, MaxLineageDepth + 1} {
		if _, ok := LineageDepth(in); ok {
			t.Errorf("LineageDepth(%d) must be refused", in)
		}
	}
}
