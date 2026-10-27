package domain

import (
	"errors"
	"testing"
)

func validMetric() Metric {
	return Metric{ID: "revenue", Version: 1, Name: "Revenue", Formula: "SUM(x)", Grain: GrainJournalLine,
		ReconciliationSource: ledgerSource, Status: StatusDraft}
}

func TestMetric_Validate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Metric)
		wantErr bool
	}{
		{"valid financial metric", func(*Metric) {}, false},
		{"valid non-financial metric", func(m *Metric) { m.ReconciliationSource, m.NonFinancial = "", true }, false},
		{"id with upper case", func(m *Metric) { m.ID = "Revenue" }, true},
		{"id too short", func(m *Metric) { m.ID = "r" }, true},
		{"version zero", func(m *Metric) { m.Version = 0 }, true},
		{"empty formula", func(m *Metric) { m.Formula = "" }, true},
		{"unknown grain", func(m *Metric) { m.Grain = "invoice" }, true},
		{"no source and not tagged non-financial", func(m *Metric) { m.ReconciliationSource = "" }, true},
		{"both source and non-financial", func(m *Metric) { m.NonFinancial = true }, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := validMetric()
			tc.mutate(&m)
			if err := m.Validate(); (err != nil) != tc.wantErr {
				t.Fatalf("Validate() = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestRevise_PublishedIsImmutable(t *testing.T) {
	published := validMetric()
	published.Status = StatusPublished

	changed := published
	changed.Formula = "SUM(y)"
	next, err := Revise(published, changed)
	if err != nil {
		t.Fatalf("Revise() error = %v", err)
	}
	if next.Version != 2 || next.Status != StatusDraft || next.Formula != "SUM(y)" {
		t.Fatalf("revised = %+v, want version 2 as DRAFT", next)
	}
	if published.Version != 1 || published.Formula != "SUM(x)" {
		t.Fatal("revision must not touch the published version")
	}

	if _, err := Revise(published, published); !errors.Is(err, ErrNoChange) {
		t.Fatalf("identical definition: err = %v, want ErrNoChange", err)
	}
	draft := validMetric()
	if _, err := Revise(draft, changed); !errors.Is(err, ErrNotPublished) {
		t.Fatalf("draft revision: err = %v, want ErrNotPublished", err)
	}
}

func TestGovernedMetrics_CatalogIsValidAndPublished(t *testing.T) {
	seen := map[string]bool{}
	for _, m := range GovernedMetrics() {
		if err := m.Validate(); err != nil {
			t.Errorf("%s: %v", m.ID, err)
		}
		if m.Status != StatusPublished {
			t.Errorf("%s: status %s, want PUBLISHED", m.ID, m.Status)
		}
		if seen[m.ID] {
			t.Errorf("duplicate metric %s", m.ID)
		}
		seen[m.ID] = true
	}
	for _, id := range []string{"revenue", "cogs", "gross_margin", "gross_margin_pct", "dso_days", "dpo_days", "inventory_turns", "otif_pct", "oee_pct"} {
		if !seen[id] {
			t.Errorf("governed metric %s missing", id)
		}
	}
}

func TestCatalogMetric_OnlyPublishedResolves(t *testing.T) {
	draft := validMetric()
	if _, ok := CatalogMetric([]Metric{draft}, "revenue"); ok {
		t.Fatal("a DRAFT metric must not resolve")
	}
	if _, ok := CatalogMetric(GovernedMetrics(), "revenue"); !ok {
		t.Fatal("published revenue must resolve")
	}
}
