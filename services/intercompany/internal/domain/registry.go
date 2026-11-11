package domain

import (
	"fmt"
	"regexp"
	"time"

	"github.com/shopspring/decimal"
)

// Codes of the registry, the FX difference and the settlement of charges. They are not in the PRD catalog and are listed in
// NOT-IMPLEMENTED.md for the catalog owner.
const (
	CodeEntityUnknown      = "IC-0853" // an entity of the posting, matrix or charge is not in the registry
	CodeTransferPriceExist = "IC-0854" // a price for the same pair, category and role is already effective on that date
	CodeNoTaxAccount       = "IC-0855" // a cross-border charge needs the output and input tax account of its two entities
	CodeTaxUnavailable     = "IC-0856" // the tax service could not price the charge; nothing was posted
	CodeNoFXAccount        = "IC-0857" // the entity has no FX-difference account
	CodeNothingToAdjust    = "IC-0858" // the item has no FX difference to book
	CodeRateUnavailable    = "IC-0859" // the rate tables have no rate for the pair and date
	CodeNotConfigured      = "IC-0860" // the stored registry or the ledger is not configured
)

// Settlement modes of an IC charge (ic.chargeSettlementMode, FR-40.13).
const (
	// SettlementJournal posts the charge as an IC journal in both entities.
	SettlementJournal = "Journal"
	// SettlementInvoice hands the charge to the invoice documents of the pairing: nothing is posted here.
	SettlementInvoice = "Invoice"
)

var (
	entityCode  = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)
	accountCode = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)
	countryCode = regexp.MustCompile(`^[A-Z]{2}$`)
	ccyCode     = regexp.MustCompile(`^[A-Z]{3}$`)
)

// EntityRecord is the registry entry of one entity code: where its books are and which accounts the service books to for it.
type EntityRecord struct {
	Entity        string
	LegalEntityID string
	LedgerID      string
	Currency      string
	// Country is the entity's ISO 3166 country; a charge between two countries is cross-border.
	Country string
	// FXDifferenceAccount takes the FX difference of a reconciliation item the entity books.
	FXDifferenceAccount string
	// OutputTaxAccount and InputTaxAccount take the tax of a cross-border charge the entity lends and borrows.
	OutputTaxAccount string
	InputTaxAccount  string
	// TaxGroup is the entity's customer tax group for the tax service.
	TaxGroup string
}

// Validate checks an entry before it is stored.
func (e EntityRecord) Validate() error {
	switch {
	case !entityCode.MatchString(e.Entity):
		return invalid("entity must be 1 to 64 letters, digits or . _ : -")
	case e.LegalEntityID == "":
		return invalid("an entity needs its legal entity")
	case len(e.LedgerID) == 0 || len(e.LedgerID) > 64:
		return invalid("an entity needs the ledger its entries post to")
	case !ccyCode.MatchString(e.Currency):
		return invalid("entity currency must be an ISO 4217 code")
	case e.Country != "" && !countryCode.MatchString(e.Country):
		return invalid("country must be an ISO 3166 alpha-2 code")
	}
	for name, v := range map[string]string{"fxDifferenceAccount": e.FXDifferenceAccount, "outputTaxAccount": e.OutputTaxAccount, "inputTaxAccount": e.InputTaxAccount} {
		if v != "" && !accountCode.MatchString(v) {
			return invalid(name + " must be 1 to 64 letters, digits or . _ : -")
		}
	}
	if len(e.TaxGroup) > 64 {
		return invalid("taxGroup must be at most 64 characters")
	}
	return nil
}

// Targets returns the ledger target of each entity code.
func Targets(entities []EntityRecord) map[string]Target {
	out := make(map[string]Target, len(entities))
	for _, e := range entities {
		out[e.Entity] = Target{LegalEntityID: e.LegalEntityID, LedgerID: e.LedgerID}
	}
	return out
}

// CrossBorder reports whether the two entities are in different, known countries.
func CrossBorder(a, b EntityRecord) bool {
	return a.Country != "" && b.Country != "" && a.Country != b.Country
}

// TransferPriceRecord is a stored transfer price.
type TransferPriceRecord struct {
	ID string
	TransferPrice
	CreatedBy string
	CreatedAt time.Time
}

// Validate checks a transfer price before it is stored.
func (p TransferPrice) Validate() error {
	switch {
	case p.Lender == "" || p.Borrower == "" || p.Lender == p.Borrower:
		return invalid("lender and borrower must be different entities")
	case p.Category != "Expense" && p.Category != "Hour" && p.Category != "Item" && p.Category != "Fee":
		return invalid("category must be Expense, Hour, Item or Fee")
	case p.Method != TPCost && p.Method != TPCostPlus && p.Method != TPTable:
		return invalid("method must be Cost, CostPlus or Table")
	case !ccyCode.MatchString(p.Currency):
		return invalid("currency must be an ISO 4217 code")
	case p.EffectiveFrom.IsZero():
		return invalid("a transfer price needs the date it is effective from")
	case p.MarkupPct.IsNegative() || p.UnitPrice.IsNegative():
		return invalid("markup and unit price must not be negative")
	case p.Method == TPCostPlus && !p.MarkupPct.IsPositive():
		return invalid("a CostPlus price needs a markup above 0")
	case p.Method == TPTable && !p.UnitPrice.IsPositive():
		return invalid("a Table price needs a unit price above 0")
	}
	return nil
}

// Settings are the tenant's IC charge settings.
type Settings struct {
	ChargeSettlementMode string
	CrossBorderTax       bool
	// ReconCurrency is the currency the scheduled reconciliation converts both sides of a pair to; empty switches the schedule off
	// for the tenant.
	ReconCurrency  string
	FXTolerancePct decimal.Decimal
}

// DefaultSettings is what a tenant that stored nothing gets: Journal mode, no cross-border tax, the default FX tolerance.
var DefaultSettings = Settings{ChargeSettlementMode: SettlementJournal, FXTolerancePct: DefaultFXTolerancePct}

// Validate checks the settings before they are stored.
func (s Settings) Validate() error {
	if s.ChargeSettlementMode != SettlementJournal && s.ChargeSettlementMode != SettlementInvoice {
		return invalid("chargeSettlementMode must be Journal or Invoice")
	}
	if s.ReconCurrency != "" && !ccyCode.MatchString(s.ReconCurrency) {
		return invalid("reconCurrency must be an ISO 4217 code")
	}
	return ValidateFXTolerance(s.FXTolerancePct)
}

// ValidateMatrix checks a matrix to be stored: no pair twice, no entity paired with itself and only registered entities.
func ValidateMatrix(m Matrix, registered map[string]bool) error {
	seen := map[[2]string]bool{}
	for i, r := range m {
		switch {
		case r.EntityA == "" || r.EntityB == "" || r.DueFrom == "" || r.DueTo == "":
			return invalid(fmt.Sprintf("matrix row %d needs both entities and both accounts", i))
		case r.EntityA == r.EntityB:
			return invalid(fmt.Sprintf("matrix row %d pairs %s with itself", i, r.EntityA))
		case !registered[r.EntityA]:
			return &Error{Code: CodeEntityUnknown, Detail: fmt.Sprintf("matrix row %d: entity %s is not in the registry", i, r.EntityA)}
		case !registered[r.EntityB]:
			return &Error{Code: CodeEntityUnknown, Detail: fmt.Sprintf("matrix row %d: entity %s is not in the registry", i, r.EntityB)}
		case !accountCode.MatchString(r.DueFrom) || !accountCode.MatchString(r.DueTo):
			return invalid(fmt.Sprintf("matrix row %d: accounts must be 1 to 64 letters, digits or . _ : -", i))
		case seen[[2]string{r.EntityA, r.EntityB}]:
			return invalid(fmt.Sprintf("matrix row %d repeats %s to %s", i, r.EntityA, r.EntityB))
		}
		seen[[2]string{r.EntityA, r.EntityB}] = true
	}
	return nil
}

// ChargeTax is the tax a cross-border charge carries: the lender books the amount as output tax and the borrower as input tax.
type ChargeTax struct {
	Amount          decimal.Decimal // in the charge currency, rounded to its minor unit
	Code            string
	LenderAccount   string
	BorrowerAccount string
}

// ClassifyRun classifies every item with the run's tolerance and fills the run's counts; each item starts open unless it matched.
func ClassifyRun(run ReconRun, items []ReconItem) (ReconRun, []ReconItem) {
	out := make([]ReconItem, 0, len(items))
	for _, it := range items {
		res := ClassifyItem(it, run.FXTolerancePct)
		it.Status, it.Difference, it.ToleranceApplied = res.Status, res.Difference, res.ToleranceApplied
		it.Resolution = ResolutionOpen
		switch res.Status {
		case ReconMatched:
			it.Resolution = ResolutionNone
			run.Matched++
		case ReconTiming:
			run.Timing++
		default:
			run.Mismatch++
		}
		out = append(out, it)
	}
	return run, out
}
