package domain

import (
	"strings"
	"testing"
	"time"
)

func day(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

func TestValidateShape_Limits(t *testing.T) {
	ok := Query{Measures: []string{"revenue"}, From: day(2026, 1, 1), To: day(2026, 12, 31)}
	tests := []struct {
		name   string
		mutate func(*Query)
		field  string
	}{
		{"valid year", func(*Query) {}, ""},
		{"no measures", func(q *Query) { q.Measures = nil }, "measures"},
		{"eleven measures", func(q *Query) { q.Measures = make([]string, MaxMeasures+1) }, "measures"},
		{"nine dimensions", func(q *Query) { q.Dimensions = make([]string, MaxDimensions+1) }, "dimensions"},
		{"to before from", func(q *Query) { q.To = day(2025, 12, 31) }, "to"},
		{"span over five years", func(q *Query) { q.To = day(2031, 1, 2) }, "to"},
		{"span exactly five years", func(q *Query) { q.To = day(2031, 1, 1) }, ""},
		{"missing from", func(q *Query) { q.From = time.Time{} }, "from"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			q := ok
			tc.mutate(&q)
			err := ValidateShape(q)
			if tc.field == "" {
				if err != nil {
					t.Fatalf("unexpected %v", err)
				}
				return
			}
			if err == nil || err.Fields[tc.field] == "" {
				t.Fatalf("err = %+v, want a field error on %s", err, tc.field)
			}
		})
	}
}

func TestBuildPlan_Table(t *testing.T) {
	cat := GovernedMetrics()
	span := func(measures ...string) Query {
		return Query{Measures: measures, From: day(2026, 1, 1), To: day(2026, 3, 31)}
	}
	usmf := "00000000-0000-0000-0000-00000000000a"

	t.Run("revenue by entity for one entity scope", func(t *testing.T) {
		q := span("revenue", "cogs")
		q.Dimensions = []string{"legal_entity_id", "fiscal_period"}
		plan, err := BuildPlan(q, cat, ScopeFromPrincipal(usmf))
		if err != nil {
			t.Fatal(err)
		}
		if plan.Model != "fin_journal_line" || plan.Grain != GrainJournalLine || plan.From != "2026-01-01" || plan.To != "2026-03-31" {
			t.Fatalf("plan = %+v", plan)
		}
		if len(plan.Filters) != 1 || plan.Filters[0].Values[0] != usmf {
			t.Fatalf("scope filter = %+v", plan.Filters)
		}
	})
	t.Run("unknown metric", func(t *testing.T) {
		if _, err := BuildPlan(span("margin_guess"), cat, Scope{Unrestricted: true}); err == nil || err.Fields["measures"] == "" {
			t.Fatalf("err = %+v", err)
		}
	})
	t.Run("mixed grains", func(t *testing.T) {
		if _, err := BuildPlan(span("revenue", "otif_pct"), cat, Scope{Unrestricted: true}); err == nil || !strings.Contains(err.Fields["measures"], "grain") {
			t.Fatalf("err = %+v", err)
		}
	})
	t.Run("dimension not allowed by a measure", func(t *testing.T) {
		q := span("dso_days")
		q.Dimensions = []string{"account_code"}
		if _, err := BuildPlan(q, cat, Scope{Unrestricted: true}); err == nil || err.Fields["dimensions"] == "" {
			t.Fatalf("err = %+v", err)
		}
	})
	t.Run("no scope yields NO_SCOPE and no filter", func(t *testing.T) {
		plan, err := BuildPlan(span("revenue"), cat, Scope{})
		if err != nil || plan.Reason != ReasonNoScope || len(plan.Filters) != 0 {
			t.Fatalf("plan = %+v, err = %v", plan, err)
		}
	})
}
