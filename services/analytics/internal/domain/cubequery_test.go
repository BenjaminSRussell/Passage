package domain

import (
	"testing"
	"time"
)

func TestPeriodRange(t *testing.T) {
	now := time.Date(2026, 11, 11, 15, 30, 0, 0, time.UTC)
	for period, want := range map[string][2]string{
		"MTD":          {"2026-11-01", "2026-11-11"},
		"QTD":          {"2026-10-01", "2026-11-11"},
		"YTD":          {"2026-01-01", "2026-11-11"},
		"TRAILING_12M": {"2025-11-12", "2026-11-11"},
	} {
		from, to, err := PeriodRange(period, now)
		if err != nil || from != want[0] || to != want[1] {
			t.Errorf("%s = %s..%s %v, want %v", period, from, to, err, want)
		}
	}
	// The first day of a quarter, and a leap day, stay inside their ranges.
	if from, _, _ := PeriodRange("QTD", time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)); from != "2027-01-01" {
		t.Errorf("QTD on 1 Jan = %s", from)
	}
	if from, _, _ := PeriodRange("TRAILING_12M", time.Date(2028, 2, 29, 0, 0, 0, 0, time.UTC)); from != "2027-03-01" {
		t.Errorf("TRAILING_12M on 29 Feb = %s", from)
	}
	if _, _, err := PeriodRange("WTD", now); err == nil {
		t.Error("an unknown period was accepted")
	}
}

func TestPlan_ToCubeQueryQualifiesMembersAndKeepsTheScope(t *testing.T) {
	from, to := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)
	plan, err := BuildPlan(Query{Measures: []string{"revenue", "cogs"}, Dimensions: []string{"fiscal_period", "currency"}, From: from, To: to},
		GovernedMetrics(), Scope{LegalEntityIDs: []string{"le-2", "le-1"}})
	if err != nil {
		t.Fatal(err)
	}
	q, err := plan.ToCubeQuery("t-1")
	if err != nil {
		t.Fatal(err)
	}
	if q.Measures[0] != "fin_journal_line.revenue" || q.Dimensions[0] != "fin_journal_line.fiscal_period" ||
		q.TimeDimensions[0].Dimension != "fin_journal_line.posting_date" || q.TimeDimensions[0].DateRange[1] != "2026-10-31" {
		t.Fatalf("query = %+v", q)
	}
	if len(q.Filters) != 2 || q.Filters[0].Member != "fin_journal_line.legal_entity_id" || q.Filters[0].Values[0] != "le-1" ||
		q.Filters[1].Member != "fin_journal_line.tenant_id" || q.Filters[1].Values[0] != "t-1" {
		t.Fatalf("filters = %+v", q.Filters)
	}
}

func TestPlan_ToCubeQueryRefusesNoScopeAndUnmodelledMetrics(t *testing.T) {
	from, to := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)
	none, _ := BuildPlan(Query{Measures: []string{"revenue"}, From: from, To: to}, GovernedMetrics(), Scope{})
	if _, err := none.ToCubeQuery("t-1"); err == nil || err.Code != CodeNoScope {
		t.Fatalf("no scope: %v", err)
	}
	dso, _ := BuildPlan(Query{Measures: []string{"dso_days"}, From: from, To: to}, GovernedMetrics(), Scope{Unrestricted: true})
	if _, err := dso.ToCubeQuery("t-1"); err == nil || err.Fields["measures"] == "" {
		t.Fatalf("dso_days: %v", err)
	}
	if got := ShortMember("fin_journal_line.revenue"); got != "revenue" {
		t.Errorf("ShortMember = %s", got)
	}
}

func TestPlan_ToCubeQueryAlwaysFiltersTheTenantAndRequiresCurrency(t *testing.T) {
	from, to := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)
	all, _ := BuildPlan(Query{Measures: []string{"revenue"}, Dimensions: []string{"currency"}, From: from, To: to}, GovernedMetrics(), Scope{Unrestricted: true})
	q, err := all.ToCubeQuery("t-9")
	if err != nil || len(q.Filters) != 1 || q.Filters[0].Member != "fin_journal_line.tenant_id" || q.Filters[0].Values[0] != "t-9" {
		t.Fatalf("unrestricted: %+v %v", q.Filters, err)
	}
	if _, err := all.ToCubeQuery(""); err == nil {
		t.Error("a query without a tenant was built")
	}
	bare, _ := BuildPlan(Query{Measures: []string{"revenue"}, From: from, To: to}, GovernedMetrics(), Scope{Unrestricted: true})
	if _, err := bare.ToCubeQuery("t-9"); err == nil || err.Fields["dimensions"] == "" {
		t.Errorf("a monetary query without the currency dimension: %v", err)
	}
}
