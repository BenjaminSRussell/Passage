package domain

import (
	"fmt"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// TableProjectWIP is the legacy_xref source_table the project_wip pack writes.
const TableProjectWIP = "project_wip"

// Category is a project opening transaction category (FR-49.12).
type Category string

const (
	CategoryHour      Category = "HOUR"
	CategoryExpense   Category = "EXPENSE"
	CategoryItem      Category = "ITEM"
	CategoryFee       Category = "FEE"
	CategoryOnAccount Category = "ON_ACCOUNT"
)

// Categories lists the categories in FR-49.12 order.
var Categories = []Category{CategoryHour, CategoryExpense, CategoryItem, CategoryFee, CategoryOnAccount}

// ParseCategory validates a category enum value.
func ParseCategory(s string) (Category, error) {
	for _, c := range Categories {
		if string(c) == s {
			return c, nil
		}
	}
	return "", fmt.Errorf("%w: category %q is not HOUR, EXPENSE, ITEM, FEE or ON_ACCOUNT", ErrInvalid, s)
}

// BillingStatus is the billing state a project opening line carries into Passage.
type BillingStatus string

const (
	BillingChargeableUnbilled BillingStatus = "CHARGEABLE_UNBILLED"
	BillingBilled             BillingStatus = "BILLED"
	BillingNonChargeable      BillingStatus = "NON_CHARGEABLE"
)

// ParseBilling validates a billing status enum value.
func ParseBilling(s string) (BillingStatus, error) {
	switch b := BillingStatus(s); b {
	case BillingChargeableUnbilled, BillingBilled, BillingNonChargeable:
		return b, nil
	}
	return "", fmt.Errorf("%w: billing status %q is not CHARGEABLE_UNBILLED, BILLED or NON_CHARGEABLE", ErrInvalid, s)
}

// Project opening statuses (BR-49.12).
const (
	ProjectReconciled      = "RECONCILED"
	ProjectOpeningMismatch = "OPENING_MISMATCH"
)

// OpeningLine is one legacy project transaction carried in as a beginning balance (FR-49.12).
// It never posts to the GL (BR-49.11).
type OpeningLine struct {
	LegacyID        string
	ProjectID       string
	Category        Category
	TransactionDate time.Time
	Qty             decimal.Decimal
	CostAmount      decimal.Decimal
	SalesAmount     decimal.Decimal
	Currency        string
	WorkerID        string
	ActivityID      string
	Dimensions      map[string]string
	BillingStatus   BillingStatus
	WIPCost         decimal.Decimal
	WIPSales        decimal.Decimal
}

// LineIssue is an opening line held back from load with the rule and reason (FR-49.11).
type LineIssue struct {
	Line   OpeningLine
	Rule   string
	Reason string
}

// ValidateOpeningLines applies the project_wip suite: uniqueness of the legacy id, referential
// (a project id), domain (ISO 4217, a transaction date not after the cutoff) and the billing
// rules. Failing lines are quarantined; the rest load.
func ValidateOpeningLines(lines []OpeningLine, cutoff time.Time) (accepted []OpeningLine, issues []LineIssue) {
	seen := make(map[string]struct{}, len(lines))
	for _, l := range lines {
		_, dup := seen[l.LegacyID]
		rule, reason := "", ""
		switch {
		case strings.TrimSpace(l.LegacyID) == "":
			rule, reason = RuleUniqueness, "legacy id is blank"
		case dup:
			rule, reason = RuleUniqueness, "legacy id appears twice in the extract"
		case strings.TrimSpace(l.ProjectID) == "":
			rule, reason = RuleReferential, "project id is blank"
		case !validCurrency(l.Currency):
			rule, reason = RuleDomain, fmt.Sprintf("currency %q is not ISO 4217", l.Currency)
		case l.TransactionDate.IsZero():
			rule, reason = RuleDomain, "transaction date is missing"
		case l.TransactionDate.After(cutoff):
			rule, reason = RuleDomain, "transaction date is after the extract cutoff"
		case l.Category == CategoryOnAccount && l.BillingStatus != BillingBilled:
			rule, reason = RuleDomain, "on-account lines are invoiced on account, so their billing status is BILLED"
		case l.BillingStatus == BillingNonChargeable && !l.WIPSales.IsZero():
			rule, reason = RuleDomain, "a non-chargeable line carries no WIP sales value"
		}
		if strings.TrimSpace(l.LegacyID) != "" {
			seen[l.LegacyID] = struct{}{}
		}
		if rule != "" {
			issues = append(issues, LineIssue{Line: l, Rule: rule, Reason: reason})
			continue
		}
		accepted = append(accepted, l)
	}
	return accepted, issues
}

func validCurrency(c string) bool {
	_, ok := iso4217[c]
	return ok
}

// LegacyCategoryTotal is the legacy ProjTrans*/COSP-COSS and RA result total of one category of
// one project.
type LegacyCategoryTotal struct {
	Category Category
	Cost     decimal.Decimal
	Sales    decimal.Decimal
	WIPCost  decimal.Decimal
	WIPSales decimal.Decimal
}

// Measures reconciled per project and category, in report order.
var projectMeasures = []string{"cost", "sales", "wip_cost", "wip_sales"}

func measures(cost, sales, wipCost, wipSales decimal.Decimal) [4]decimal.Decimal {
	return [4]decimal.Decimal{cost, sales, wipCost, wipSales}
}

// ProjectTotals compares the opening lines Passage holds for one project with the legacy totals
// per category and measure (FR-49.12: cost, sales value, WIP cost and WIP sales must match to
// 0.00). Categories on either side appear; one missing on a side counts as zero.
func ProjectTotals(projectID string, lines []OpeningLine, legacy []LegacyCategoryTotal) ([]ControlTotal, error) {
	target := map[Category][4]decimal.Decimal{}
	for _, l := range lines {
		if l.ProjectID != projectID {
			continue
		}
		a := target[l.Category]
		for i, v := range measures(l.CostAmount, l.SalesAmount, l.WIPCost, l.WIPSales) {
			a[i] = a[i].Add(v)
		}
		target[l.Category] = a
	}
	source := map[Category][4]decimal.Decimal{}
	for _, t := range legacy {
		a := source[t.Category]
		for i, v := range measures(t.Cost, t.Sales, t.WIPCost, t.WIPSales) {
			a[i] = a[i].Add(v)
		}
		source[t.Category] = a
	}
	var out []ControlTotal
	for _, c := range Categories {
		s, inSource := source[c]
		t, inTarget := target[c]
		if !inSource && !inTarget {
			continue
		}
		for m, name := range projectMeasures {
			ct, err := NewControlTotal("project_wip."+name, map[string]string{"project": projectID, "category": string(c)}, s[m], t[m], Tolerance{})
			if err != nil {
				return nil, err
			}
			out = append(out, ct)
		}
	}
	return out, nil
}

// ProjectStatus is OPENING_MISMATCH when any of the project's totals is MISMATCH (BR-49.12),
// otherwise RECONCILED.
func ProjectStatus(totals []ControlTotal) string {
	for _, t := range totals {
		if t.Status == StatusMismatch {
			return ProjectOpeningMismatch
		}
	}
	return ProjectReconciled
}

// TotalsOfProject returns the totals whose key names the project.
func TotalsOfProject(totals []ControlTotal, projectID string) []ControlTotal {
	var out []ControlTotal
	for _, t := range totals {
		if strings.HasPrefix(t.Name, "project_wip.") && t.Key["project"] == projectID {
			out = append(out, t)
		}
	}
	return out
}

// ProjectsOf lists the projects a project_wip run's totals name, sorted.
func ProjectsOf(totals []ControlTotal) []string {
	seen := map[string]struct{}{}
	for _, t := range totals {
		if p, ok := t.Key["project"]; ok && strings.HasPrefix(t.Name, "project_wip.") {
			seen[p] = struct{}{}
		}
	}
	return sortedKeys(seen)
}

// ReconcileProjects finishes a project_wip run once its accepted lines are in legacy_xref.
// loaded is how many lines this run inserted; present holds every opening line Passage has
// for the run's projects in this (system, company), including lines loaded by earlier runs. It
// moves the run from VALIDATED to LOADED and, when every project reconciles, to RECONCILED.
func ReconcileProjects(r *PackRun, accepted, loaded int, projects []string, present []OpeningLine, legacy map[string][]LegacyCategoryTotal) error {
	if r.Status != StatusValidated {
		return fmt.Errorf("%w: reconcile needs a VALIDATED run, got %s", ErrTransition, r.Status)
	}
	r.RecordsLoaded = loaded
	r.AlreadyPresent = accepted - loaded
	if err := r.Advance(StatusLoaded); err != nil {
		return err
	}
	r.Totals = nil
	for _, id := range projects {
		totals, err := ProjectTotals(id, present, legacy[id])
		if err != nil {
			return err
		}
		r.Totals = append(r.Totals, totals...)
	}
	if !r.HasMismatch() && r.RecordsQuarantined == 0 {
		return r.Advance(StatusReconciled)
	}
	return nil
}
