package domain_test

import (
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/metadata/internal/domain"
)

func TestDecisionTableFirstHitExplainsMatchedRow(t *testing.T) {
	table := domain.DecisionTable{
		Name:      "vendor-risk",
		HitPolicy: domain.HitFirst,
		Inputs: []domain.RuleColumn{
			{Name: "country", Type: "string"},
			{Name: "spend", Type: "decimal"},
		},
		Outputs: []domain.RuleColumn{{Name: "review", Type: "bool"}},
		Rows: []domain.DecisionRow{
			{ID: "high-spend", When: map[string]string{"spend": ">=10000"}, Then: map[string]any{"review": true}},
			{ID: "default", When: map[string]string{}, Then: map[string]any{"review": false}},
		},
	}

	result, err := domain.Evaluate(table, map[string]any{"country": "US", "spend": "12500.00"}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Outputs["review"]; got != true {
		t.Fatalf("review = %#v", got)
	}
	if len(result.Matches) != 1 || result.Matches[0].RowID != "high-spend" {
		t.Fatalf("matches = %#v", result.Matches)
	}
	if !strings.Contains(result.Matches[0].Explanation, "spend >=10000") {
		t.Fatalf("explanation = %q", result.Matches[0].Explanation)
	}
}

func TestDecisionTableEffectiveDatesAndCustomerOverride(t *testing.T) {
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	table := domain.DecisionTable{
		Name:      "vendor-tier",
		HitPolicy: domain.HitFirst,
		Inputs:    []domain.RuleColumn{{Name: "country", Type: "string"}},
		Outputs:   []domain.RuleColumn{{Name: "tier", Type: "string"}},
		Rows: []domain.DecisionRow{
			{ID: "system-us", When: map[string]string{"country": "=US"}, Then: map[string]any{"tier": "standard"}, EffectiveFrom: &from, EffectiveTo: &to},
			{ID: "customer-us", Layer: domain.LayerCustomer, When: map[string]string{"country": "=US"}, Then: map[string]any{"tier": "preferred"}, EffectiveFrom: &from, EffectiveTo: &to},
		},
	}

	result, err := domain.Evaluate(table, map[string]any{"country": "US"}, time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["tier"] != "preferred" || result.Matches[0].RowID != "customer-us" {
		t.Fatalf("customer override not selected: %#v", result)
	}
	if _, err := domain.Evaluate(table, map[string]any{"country": "US"}, to); err == nil {
		t.Fatal("expected no-match error outside the effective interval")
	}
}

func TestDecisionTableRejectsWrongInputType(t *testing.T) {
	table := domain.DecisionTable{
		Name:      "typed",
		HitPolicy: domain.HitFirst,
		Inputs:    []domain.RuleColumn{{Name: "active", Type: "bool"}},
		Outputs:   []domain.RuleColumn{{Name: "answer", Type: "string"}},
		Rows:      []domain.DecisionRow{{ID: "yes", When: map[string]string{"active": "=true"}, Then: map[string]any{"answer": "ok"}}},
	}
	if _, err := domain.Evaluate(table, map[string]any{"active": "not-a-bool"}, time.Now()); err == nil || !strings.Contains(err.Error(), "MET-0301") {
		t.Fatalf("wrong-type error = %v", err)
	}
}
