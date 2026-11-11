package domain

import (
	"fmt"
	"regexp"
	"time"

	"github.com/shopspring/decimal"
)

// Codes raised by the group structure and the collection. They are not in the
// PRD catalog and are listed in NOT-IMPLEMENTED.md for the catalog owner.
const (
	CodeGroupNotFound     = "CNS-0924"
	CodeGroupExists       = "CNS-0925"
	CodeUnitExists        = "CNS-0926"
	CodeCollectionError   = "CNS-0927"
	CodeCollectionGone    = "CNS-0928"
	CodeNoLedger          = "CNS-0929"
	CodeUnitTerms         = "CNS-0930" // a unit has no acquisition terms, or its terms are unusable
	CodeUnitInUse         = "CNS-0931" // a unit that collections or a holding refer to cannot be removed
	CodeRunFromCollection = "CNS-0932" // a run cannot be derived from the collection
)

// Account types of the chart mapping: balance sheet accounts translate at the
// closing rate and income accounts at the average rate (IAS 21.39).
const (
	AccountBalanceSheet = "balance_sheet"
	AccountIncome       = "income"
)

// MaxMappingRows bounds one chart mapping.
const MaxMappingRows = 5000

var (
	groupCodePattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)
	currencyPattern  = regexp.MustCompile(`^[A-Z]{3}$`)
)

// Group is a consolidation group with the currency its statements are in.
type Group struct {
	ID        string
	Code      string
	Name      string
	Currency  string
	CreatedBy string
	CreatedAt time.Time
}

// Validate checks a group before it is stored.
func (g Group) Validate() error {
	switch {
	case !groupCodePattern.MatchString(g.Code):
		return invalid("group code must be 1 to 64 letters, digits or . _ : -")
	case len(g.Name) == 0 || len(g.Name) > 200:
		return invalid("group name must be 1 to 200 characters")
	case !currencyPattern.MatchString(g.Currency):
		return invalid("group currency must be an ISO 4217 code")
	}
	return nil
}

// Holding is one effective-dated stake in a unit. HolderUnitID is empty when
// the group's top parent holds it. To is zero while the holding is open.
type Holding struct {
	ID           string
	HolderUnitID string
	From         time.Time
	To           time.Time
	Percent      decimal.Decimal
}

// Unit is one legal entity of the group.
type Unit struct {
	ID            string
	GroupID       string
	LegalEntityID string
	LedgerID      string
	Currency      string
	// FiscalYearStartMonth is the month the unit's fiscal year starts in (1 to 12; 0 reads as 1, the calendar year). A
	// collection takes the unit's income accounts from the start of the fiscal year the period falls in.
	FiscalYearStartMonth int
	Holdings             []Holding
}

// FiscalStart returns the first month (YYYY-MM) of the unit's fiscal year that period (YYYY-MM) falls in.
func (u Unit) FiscalStart(period string) string {
	return FiscalYearStart(period, u.FiscalYearStartMonth)
}

// Validate checks a unit before it is stored.
func (u Unit) Validate() error {
	switch {
	case u.LegalEntityID == "":
		return invalid("a unit needs a legal entity")
	case len(u.LedgerID) == 0 || len(u.LedgerID) > 64:
		return invalid("a unit needs the ledger its balances are collected from")
	case !currencyPattern.MatchString(u.Currency):
		return invalid("unit currency must be an ISO 4217 code")
	case u.FiscalYearStartMonth < 0 || u.FiscalYearStartMonth > 12:
		return invalid("the fiscal year starts in a month from 1 to 12")
	}
	return nil
}

// FiscalYearStart is the first month (YYYY-MM) of the fiscal year that period (YYYY-MM) falls in, for a fiscal year that
// starts in the given month (0 or 1 is the calendar year). With a start month of 4, 2027-02 is in the year that began 2026-04.
func FiscalYearStart(period string, startMonth int) string {
	if startMonth < 1 {
		startMonth = 1
	}
	year, month := 0, 0
	_, _ = fmt.Sscanf(period, "%d-%d", &year, &month)
	if month < startMonth {
		year--
	}
	return fmt.Sprintf("%04d-%02d", year, startMonth)
}

// PreviousFiscalYearEnd is the last month (YYYY-MM) of the fiscal year before the one period falls in.
func PreviousFiscalYearEnd(period string, startMonth int) string {
	start := FiscalYearStart(period, startMonth)
	year, month := 0, 0
	_, _ = fmt.Sscanf(start, "%d-%d", &year, &month)
	if month == 1 {
		return fmt.Sprintf("%04d-12", year-1)
	}
	return fmt.Sprintf("%04d-%02d", year, month-1)
}

// PercentOn returns the share of the unit held in total on a date, summed over
// every holder (CNS-0904 keeps it at or below 100.00).
func (u Unit) PercentOn(on time.Time) decimal.Decimal {
	total := decimal.Zero
	for _, h := range u.Holdings {
		if inForce(OwnershipInterval{From: h.From, To: h.To, Percent: h.Percent}, on) {
			total = total.Add(h.Percent)
		}
	}
	return total
}

// AddHolding validates a new holding against the history and returns the
// history with it applied. A holding from the same holder that is still open
// and started earlier is closed on the new start date, so a change of
// ownership keeps the old interval. The result is checked with
// ValidateOwnership: all holders together never exceed 100.00 %.
func AddHolding(history []Holding, add Holding) ([]Holding, error) {
	if add.From.IsZero() {
		return nil, invalid("ownership needs an effective date")
	}
	out := make([]Holding, len(history), len(history)+1)
	copy(out, history)
	for i, h := range out {
		if h.HolderUnitID != add.HolderUnitID {
			continue
		}
		if h.From.Equal(add.From) {
			return nil, invalid(fmt.Sprintf("ownership already starts on %s", add.From.Format("2006-01-02")))
		}
		if h.To.IsZero() && h.From.Before(add.From) {
			out[i].To = add.From
		}
	}
	out = append(out, add)
	intervals := make([]OwnershipInterval, 0, len(out))
	for _, h := range out {
		intervals = append(intervals, OwnershipInterval{From: h.From, To: h.To, Percent: h.Percent})
	}
	if err := ValidateOwnership(intervals); err != nil {
		return nil, err
	}
	return out, nil
}

// AccountMapping maps one main account to a consolidation account.
type AccountMapping struct {
	MainAccount          string
	ConsolidationAccount string
	Type                 string
	// Role says what the account is for the run: asset, liability or equity of the balance sheet, or the intercompany
	// ic_sales, ic_cost, ic_receivable, ic_payable. Empty for an account a run does not read by role.
	Role string
}

// Account roles.
const (
	RoleAsset        = "asset"
	RoleLiability    = "liability"
	RoleEquity       = "equity"
	RoleICSales      = "ic_sales"
	RoleICCost       = "ic_cost"
	RoleICReceivable = "ic_receivable"
	RoleICPayable    = "ic_payable"
)

// ValidRole reports whether r is an account role or empty.
func ValidRole(r string) bool {
	switch r {
	case "", RoleAsset, RoleLiability, RoleEquity, RoleICSales, RoleICCost, RoleICReceivable, RoleICPayable:
		return true
	}
	return false
}

// ValidateMapping checks a whole chart mapping: unique main accounts, valid
// codes and a known account type.
func ValidateMapping(rows []AccountMapping) error {
	if len(rows) > MaxMappingRows {
		return invalid(fmt.Sprintf("a mapping holds at most %d accounts", MaxMappingRows))
	}
	seen := map[string]bool{}
	for _, r := range rows {
		if !codePattern.MatchString(r.MainAccount) || !codePattern.MatchString(r.ConsolidationAccount) {
			return invalid(fmt.Sprintf("account %q or %q is not a valid account code", r.MainAccount, r.ConsolidationAccount))
		}
		if r.Type != AccountBalanceSheet && r.Type != AccountIncome {
			return invalid(fmt.Sprintf("account %s must be balance_sheet or income", r.MainAccount))
		}
		if !ValidRole(r.Role) {
			return invalid(fmt.Sprintf("account %s has the unknown role %q", r.MainAccount, r.Role))
		}
		switch r.Role {
		case RoleAsset, RoleLiability, RoleEquity, RoleICReceivable, RoleICPayable:
			if r.Type != AccountBalanceSheet {
				return invalid(fmt.Sprintf("account %s has the balance sheet role %s but is not a balance_sheet account", r.MainAccount, r.Role))
			}
		case RoleICSales, RoleICCost:
			if r.Type != AccountIncome {
				return invalid(fmt.Sprintf("account %s has the income role %s but is not an income account", r.MainAccount, r.Role))
			}
		}
		if seen[r.MainAccount] {
			return invalid(fmt.Sprintf("main account %s is mapped twice", r.MainAccount))
		}
		seen[r.MainAccount] = true
	}
	return nil
}
