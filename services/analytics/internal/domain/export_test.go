package domain

import (
	"testing"

	"pgregory.net/rapid"
)

func TestExportCanonical_BindsUserFormatAndPlan(t *testing.T) {
	plan := Plan{Model: "fin_journal_line", Grain: "journal_line", Measures: []string{"revenue"}, From: "2026-01-01", To: "2026-01-31",
		Filters: []Filter{{Member: "legal_entity_id", Operator: "equals", Values: []string{"usmf"}}}}
	base := QueryHash(ExportCanonical("user-a", "XLSX", plan))
	if base != QueryHash(ExportCanonical("user-a", "XLSX", plan)) {
		t.Fatal("the same request must hash the same")
	}
	other := plan
	other.Filters = []Filter{{Member: "legal_entity_id", Operator: "equals", Values: []string{"demf"}}}
	for name, h := range map[string]string{
		"another user":   QueryHash(ExportCanonical("user-b", "XLSX", plan)),
		"another format": QueryHash(ExportCanonical("user-a", "CSV", plan)),
		"another scope":  QueryHash(ExportCanonical("user-a", "XLSX", other)),
	} {
		if h == base {
			t.Errorf("%s must not share an export fingerprint", name)
		}
	}
}

// Property: requests that differ in the period never share a fingerprint.
func TestExportCanonical_PeriodProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		from := rapid.StringMatching(`20[0-9]{2}-0[1-9]-[0-2][1-9]`).Draw(t, "from")
		to := rapid.StringMatching(`20[0-9]{2}-0[1-9]-[0-2][1-9]`).Draw(t, "to")
		a := Plan{Model: "fin_journal_line", Measures: []string{"revenue"}, From: from, To: from}
		b := a
		b.To = to
		if (from == to) != (ExportCanonical("u", "CSV", a) == ExportCanonical("u", "CSV", b)) {
			t.Fatalf("from %s to %s", from, to)
		}
	})
}
