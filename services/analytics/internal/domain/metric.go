// Package domain holds the analytics rules: governed metric definitions, the
// formulas behind them, reconciliation against the ledger, the OPA scope mirror
// for Cube, query shape limits and the CDC schema-evolution classifier. Nothing
// in this package performs I/O.
package domain

import (
	"errors"
	"regexp"
	"slices"
)

const (
	GrainJournalLine   = "journal_line"
	GrainSalesLine     = "sales_line"
	GrainResourceShift = "resource_shift"

	StatusDraft      = "DRAFT"
	StatusPublished  = "PUBLISHED"
	StatusDeprecated = "DEPRECATED"

	ledgerSource  = "ledger.balance_by_account"
	subledgerAR   = "subledger.ar_open_items"
	subledgerAP   = "subledger.ap_open_items"
	maxFormulaLen = 4096
)

var metricID = regexp.MustCompile(`^[a-z][a-z0-9_]{1,62}$`)

// Metric is a governed measure: one formula, one grain, one owner. Published
// versions are immutable; a change creates the next version (BR in §9.46.4).
type Metric struct {
	ID                   string   `json:"metricId"`
	Version              int      `json:"version"`
	Name                 string   `json:"name"`
	Formula              string   `json:"formula"`
	Grain                string   `json:"grain"`
	AllowedDimensions    []string `json:"allowedDimensions"`
	ReconciliationSource string   `json:"reconciliationSource,omitempty"`
	NonFinancial         bool     `json:"nonFinancial"`
	Status               string   `json:"status"`
}

var (
	ErrNotPublished = errors.New("only a published metric can be revised in place")
	ErrNoChange     = errors.New("metric revision carries no change")
)

// Validate checks the invariants a metric definition must satisfy before it can
// be stored: a well-formed id, a known grain, a bounded formula, and exactly one
// of a reconciliation source or the non-financial tag.
func (m Metric) Validate() error {
	switch {
	case !metricID.MatchString(m.ID):
		return errors.New("metric id must be lower snake case, 2 to 63 characters")
	case m.Version < 1:
		return errors.New("metric version must be at least 1")
	case m.Name == "":
		return errors.New("metric name is required")
	case m.Formula == "" || len(m.Formula) > maxFormulaLen:
		return errors.New("metric formula is required and at most 4096 characters")
	case !slices.Contains([]string{GrainJournalLine, GrainSalesLine, GrainResourceShift}, m.Grain):
		return errors.New("metric grain must be journal_line, sales_line or resource_shift")
	case (m.ReconciliationSource != "") == m.NonFinancial:
		return errors.New("a metric has exactly one reconciliation source or is tagged non_financial")
	}
	return nil
}

// Revise returns the metric that results from changing published metric m to
// next. An identical definition returns m unchanged; a real change returns the
// next version as a DRAFT, leaving the published version as it was.
func Revise(m Metric, next Metric) (Metric, error) {
	if m.Status != StatusPublished {
		return Metric{}, ErrNotPublished
	}
	next.ID, next.Version = m.ID, m.Version
	next.Status = m.Status
	if equalDefinition(m, next) {
		return Metric{}, ErrNoChange
	}
	next.Version = m.Version + 1
	next.Status = StatusDraft
	return next, nil
}

func equalDefinition(a, b Metric) bool {
	return a.Name == b.Name && a.Formula == b.Formula && a.Grain == b.Grain &&
		a.ReconciliationSource == b.ReconciliationSource && a.NonFinancial == b.NonFinancial &&
		slices.Equal(a.AllowedDimensions, b.AllowedDimensions)
}

// financeDimensions are the dimensions of the Cube model fin_journal_line
// (data/cube/model/fin_journal_line.yml) that a journal-line metric may be
// grouped by. The contract test in cube_contract_test.go keeps the two in step.
var financeDimensions = []string{"legal_entity_id", "fiscal_period", "account_code", "currency"}

// GovernedMetrics is the initial published catalog from FR-46.6. The catalog is
// platform content: it is read-only to tenants and changes only by a new version.
func GovernedMetrics() []Metric {
	published := func(m Metric) Metric {
		m.Version, m.Status = 1, StatusPublished
		return m
	}
	return []Metric{
		published(Metric{ID: "revenue", Name: "Revenue", Grain: GrainJournalLine, AllowedDimensions: financeDimensions,
			Formula: "-SUM(amount_reporting) WHERE account_category = 'REVENUE' AND status = 'POSTED'", ReconciliationSource: ledgerSource}),
		published(Metric{ID: "cogs", Name: "Cost of goods sold", Grain: GrainJournalLine, AllowedDimensions: financeDimensions,
			Formula: "SUM(amount_reporting) WHERE account_category = 'COGS' AND status = 'POSTED'", ReconciliationSource: ledgerSource}),
		published(Metric{ID: "gross_margin", Name: "Gross margin", Grain: GrainJournalLine, AllowedDimensions: financeDimensions,
			Formula: "revenue - cogs", ReconciliationSource: ledgerSource}),
		published(Metric{ID: "gross_margin_pct", Name: "Gross margin %", Grain: GrainJournalLine, AllowedDimensions: financeDimensions,
			Formula: "gross_margin / NULLIF(revenue, 0)", NonFinancial: true}),
		published(Metric{ID: "dso_days", Name: "Days sales outstanding", Grain: GrainJournalLine, AllowedDimensions: []string{"legal_entity_id", "currency"},
			Formula: "ar_open_balance_at(end) / NULLIF(revenue_trailing_90d, 0) * 90", ReconciliationSource: subledgerAR}),
		published(Metric{ID: "dpo_days", Name: "Days payables outstanding", Grain: GrainJournalLine, AllowedDimensions: []string{"legal_entity_id", "currency"},
			Formula: "ap_open_balance_at(end) / NULLIF(cogs_trailing_90d, 0) * 90", ReconciliationSource: subledgerAP}),
		published(Metric{ID: "inventory_turns", Name: "Inventory turns", Grain: GrainJournalLine, AllowedDimensions: []string{"legal_entity_id"},
			Formula: "cogs_trailing_365d / NULLIF(AVG(inventory_value_eom over last 13 month-ends), 0)", NonFinancial: true}),
		published(Metric{ID: "otif_pct", Name: "On time in full %", Grain: GrainSalesLine, AllowedDimensions: []string{"legal_entity_id", "site_id", "customer_group"},
			Formula: "COUNT(on-time and in-full shipped lines) / NULLIF(COUNT(shipped lines), 0)", NonFinancial: true}),
		published(Metric{ID: "oee_pct", Name: "Overall equipment effectiveness %", Grain: GrainResourceShift, AllowedDimensions: []string{"legal_entity_id", "site_id", "resource_id"},
			Formula: "availability * performance * quality", NonFinancial: true}),
	}
}

// IsMetricID reports whether id is a well-formed metric id.
func IsMetricID(id string) bool { return metricID.MatchString(id) }

// CatalogMetric returns the published metric with the given id.
func CatalogMetric(catalog []Metric, id string) (Metric, bool) {
	for _, m := range catalog {
		if m.ID == id && m.Status == StatusPublished {
			return m, true
		}
	}
	return Metric{}, false
}
