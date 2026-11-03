package domain

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// Contract kinds of the broker and royalty accruals and claims (FR-26.16, FR-26.17).
const (
	ContractBroker  = "BROKER"
	ContractRoyalty = "ROYALTY"
)

// Broker rate kinds: a percentage of the invoice line's net, or an amount per unit.
const (
	RatePercentNet = "PERCENT_NET"
	RatePerUnit    = "PER_UNIT"
)

// Royalty bases: net sales value, or quantity sold.
const (
	RoyaltyNetSales = "NET_SALES"
	RoyaltyQuantity = "QUANTITY"
)

// Accrual row kinds: ACCRUAL on an invoice line, CREDIT on a credit note line (negative),
// SHORTFALL for the minimum royalty top-up posted at period close.
const (
	AccrualLine      = "ACCRUAL"
	AccrualCredit    = "CREDIT"
	AccrualShortfall = "SHORTFALL"
)

// Default accounts (PRD 5.26.2): broker commission Dr 6210 / Cr 2175 broker payable;
// royalty expense Dr 6220 / Cr 2176 royalty payable.
const (
	BrokerExpenseAccount  = "6210"
	BrokerPayableAccount  = "2175"
	RoyaltyExpenseAccount = "6220"
	RoyaltyPayableAccount = "2176"
)

// Problem codes of broker and royalty contracts.
const (
	// CodeMinimumRoyaltyShortfall is the PRC-0017 notice: a period closed below the minimum.
	CodeMinimumRoyaltyShortfall = "PRC-0017"
	// CodeContractNotFound: no broker contract or royalty agreement with the id.
	CodeContractNotFound = "PRC-0058"
	// CodeCreditExceedsOriginal: a credit note line credits more than the invoice line accrued
	// on this contract, or the invoice line was never accrued.
	CodeCreditExceedsOriginal = "PRC-0059"
	// CodePeriodClosed: the royalty period was closed, so nothing more accrues in it.
	CodePeriodClosed = "PRC-0060"
	// CodeContractClosed: the contract was closed; it takes no further accruals.
	CodeContractClosed = "PRC-0061"
)

// BrokerContract pays a broker vendor a rate on the invoices of its scope (FR-26.16). An
// empty CustomerID, CustomerGroupID, ProductID and ProductGroupID is all customers or all
// products. Rate is a percentage of line net (PERCENT_NET) or an amount per unit (PER_UNIT).
type BrokerContract struct {
	ID              string
	LegalEntityID   string
	Name            string
	BrokerVendorID  string
	CustomerID      string
	CustomerGroupID string
	ProductID       string
	ProductGroupID  string
	RateKind        string
	Rate            decimal.Decimal
	Currency        string
	ValidFrom       time.Time
	ValidTo         time.Time
	PayableAccount  string
	ExpenseAccount  string
	Status          string
	Version         int
}

// RoyaltyAgreement pays a licensor vendor tiered rates on one product or product group
// (FR-26.17). Tiers follow the rebate rules (BR-26.8): the tier in force at the period's
// cumulative basis pays on all of it. MinimumRoyalty is the least a period pays.
type RoyaltyAgreement struct {
	ID               string
	LegalEntityID    string
	Name             string
	LicensorVendorID string
	ProductID        string
	ProductGroupID   string
	Basis            string
	Period           string
	Tiers            []Tier
	MinimumRoyalty   decimal.Decimal
	Currency         string
	ValidFrom        time.Time
	ValidTo          time.Time
	PayableAccount   string
	ExpenseAccount   string
	Status           string
	Version          int
}

// ContractLine is one posted invoice line (or credit note line) handed to pricing by the
// invoice.posted consumer. A credit note line names the invoice line it reverses and carries
// positive NetAmount and Quantity.
type ContractLine struct {
	InvoiceLineID         string
	InvoiceDate           time.Time
	CustomerID            string
	CustomerGroupID       string
	ProductID             string
	ProductGroupID        string
	Currency              string
	NetAmount             decimal.Decimal
	Quantity              decimal.Decimal
	ReversesInvoiceLineID string
}

// IsCredit reports whether the line is a credit note line.
func (l ContractLine) IsCredit() bool { return l.ReversesInvoiceLineID != "" }

var accountPattern = regexp.MustCompile(`^[0-9]{4,8}$`)

func validDates(from, to time.Time, f map[string]string) {
	if from.IsZero() || to.IsZero() || dayOf(from) > dayOf(to) {
		f["validity"] = "validFrom must be on or before validTo"
	}
}

// ValidateBrokerContract returns field errors for a broker contract; an empty map is valid.
func ValidateBrokerContract(c BrokerContract) map[string]string {
	f := map[string]string{}
	if c.Name == "" || len(c.Name) > 120 {
		f["name"] = "must be 1 to 120 characters"
	}
	if c.BrokerVendorID == "" {
		f["brokerVendorId"] = "is required"
	}
	if c.CustomerID != "" && c.CustomerGroupID != "" {
		f["customer"] = "name at most one of customerId and customerGroupId"
	}
	if c.ProductID != "" && c.ProductGroupID != "" {
		f["product"] = "name at most one of productId and productGroupId"
	}
	switch c.RateKind {
	case RatePercentNet:
		if !c.Rate.IsPositive() || c.Rate.GreaterThan(decimal.NewFromInt(100)) {
			f["rate"] = "a percentage of net must be above 0 and at most 100"
		}
	case RatePerUnit:
		if !c.Rate.IsPositive() {
			f["rate"] = "an amount per unit must be above 0"
		}
	default:
		f["rateKind"] = "must be PERCENT_NET or PER_UNIT"
	}
	if len(c.Currency) != 3 {
		f["currency"] = "must be an ISO 4217 code"
	}
	validDates(c.ValidFrom, c.ValidTo, f)
	if !accountPattern.MatchString(c.PayableAccount) || !accountPattern.MatchString(c.ExpenseAccount) {
		f["accounts"] = "payableAccount and expenseAccount must be account codes"
	}
	return f
}

// RoyaltyRebateView presents a royalty agreement as the rebate agreement whose tier and
// period rules it shares (BR-26.8), so accrual and validation use one implementation.
func RoyaltyRebateView(a RoyaltyAgreement) RebateAgreement {
	basis := BasisNetAmount
	if a.Basis == RoyaltyQuantity {
		basis = BasisQuantity
	}
	return RebateAgreement{ID: a.ID, LegalEntityID: a.LegalEntityID, Name: a.Name, PartyRole: PartyVendor, VendorID: a.LicensorVendorID,
		Basis: basis, Period: a.Period, Tiers: a.Tiers, Currency: a.Currency, ValidFrom: a.ValidFrom, ValidTo: a.ValidTo,
		AccrualAccount: a.PayableAccount, ExpenseAccount: a.ExpenseAccount}
}

// ValidateRoyaltyAgreement returns field errors for a royalty agreement; an empty map is valid.
func ValidateRoyaltyAgreement(a RoyaltyAgreement) map[string]string {
	f := map[string]string{}
	if a.Name == "" || len(a.Name) > 120 {
		f["name"] = "must be 1 to 120 characters"
	}
	if a.LicensorVendorID == "" {
		f["licensorVendorId"] = "is required"
	}
	if (a.ProductID == "") == (a.ProductGroupID == "") {
		f["product"] = "exactly one of productId or productGroupId is required"
	}
	if a.Basis != RoyaltyNetSales && a.Basis != RoyaltyQuantity {
		f["basis"] = "must be NET_SALES or QUANTITY"
	}
	if a.MinimumRoyalty.IsNegative() {
		f["minimumRoyalty"] = "must not be negative"
	} else if len(a.Currency) == 3 && !a.MinimumRoyalty.Equal(roundMinor(a.MinimumRoyalty, a.Currency)) {
		f["minimumRoyalty"] = "must not have more decimals than the currency's minor unit"
	}
	if !accountPattern.MatchString(a.PayableAccount) || !accountPattern.MatchString(a.ExpenseAccount) {
		f["accounts"] = "payableAccount and expenseAccount must be account codes"
	}
	if a.Basis == RoyaltyNetSales || a.Basis == RoyaltyQuantity {
		if err := ValidateAgreement(RoyaltyRebateView(a)); err != nil {
			for k, v := range err.Fields {
				if k != "partyRole" && k != "vendorId" && k != "customer" {
					f[k] = v
				}
			}
		}
	}
	return f
}

func sameScope(want, got string) bool { return want == "" || want == got }

// BrokerApplies reports whether a broker contract earns on the invoice line: the contract is
// ACTIVE, the currency matches, the invoice date is inside the validity, and the customer and
// product scopes match (an empty scope key is all).
func BrokerApplies(c BrokerContract, l ContractLine) bool {
	day := dayOf(l.InvoiceDate)
	return c.Status == AgreementActive && c.Currency == l.Currency && day >= dayOf(c.ValidFrom) && day <= dayOf(c.ValidTo) &&
		sameScope(c.CustomerID, l.CustomerID) && sameScope(c.CustomerGroupID, l.CustomerGroupID) &&
		sameScope(c.ProductID, l.ProductID) && sameScope(c.ProductGroupID, l.ProductGroupID)
}

// RoyaltyApplies reports whether a royalty agreement earns on the invoice line (its product
// or product group, validity and currency).
func RoyaltyApplies(a RoyaltyAgreement, l ContractLine) bool {
	day := dayOf(l.InvoiceDate)
	return a.Status == AgreementActive && a.Currency == l.Currency && day >= dayOf(a.ValidFrom) && day <= dayOf(a.ValidTo) &&
		sameScope(a.ProductID, l.ProductID) && sameScope(a.ProductGroupID, l.ProductGroupID)
}

// BrokerBasis is the part of an invoice line the broker rate multiplies: its net value for a
// percentage contract, its quantity for an amount-per-unit contract.
func BrokerBasis(c BrokerContract, l ContractLine) decimal.Decimal {
	if c.RateKind == RatePerUnit {
		return l.Quantity
	}
	return l.NetAmount
}

// BrokerAmount is the broker claim accrued on an invoice line, rounded half-even to the
// currency's minor unit (FR-26.16).
func BrokerAmount(c BrokerContract, l ContractLine) decimal.Decimal {
	if c.RateKind == RatePerUnit {
		return roundMinor(l.Quantity.Mul(c.Rate), c.Currency)
	}
	return roundMinor(l.NetAmount.Mul(c.Rate).Shift(-2), c.Currency)
}

// BrokerCredit is the negative accrual of a credit note line: the original accrual reversed
// in proportion to the credited share of the original basis (FR-26.16, "credit notes reverse
// proportionally"). origBasis and origAmount are the original line's basis and accrual;
// creditedBasis and creditedAmount what earlier credits already reversed (both as positive
// magnitudes). A credit above the remaining basis is PRC-0059; the credit that uses up the
// remaining basis reverses exactly what is left, so a fully credited line nets to zero.
func BrokerCredit(origBasis, origAmount, creditedBasis, creditedAmount, creditBasis decimal.Decimal, currency string) (decimal.Decimal, *Error) {
	remaining := origBasis.Sub(creditedBasis)
	if creditBasis.IsNegative() || creditBasis.GreaterThan(remaining) {
		return decimal.Zero, &Error{Code: CodeCreditExceedsOriginal,
			Detail: fmt.Sprintf("credit of %s exceeds the %s still creditable on the invoice line", creditBasis, remaining)}
	}
	if origBasis.IsZero() || creditBasis.IsZero() {
		return decimal.Zero, nil
	}
	if creditBasis.Equal(remaining) {
		return origAmount.Sub(creditedAmount).Neg(), nil
	}
	return roundMinor(origAmount.Mul(creditBasis).Div(origBasis), currency).Neg(), nil
}

// MinimumShortfall is what a period tops up to the minimum royalty: the minimum less the
// period's accrued royalty, never negative (FR-26.17, PRC-0017).
func MinimumShortfall(minimum, accrued decimal.Decimal) decimal.Decimal {
	if accrued.GreaterThanOrEqual(minimum) {
		return decimal.Zero
	}
	return minimum.Sub(accrued)
}

var periodKeyPattern = regexp.MustCompile(`^([0-9]{4})(?:-([0-9]{2})|-Q([1-4]))?$`)

// PeriodBounds returns the first and last day of a period key such as 2027-03, 2027-Q1 or 2027.
func PeriodBounds(key string) (first, last time.Time, ok bool) {
	m := periodKeyPattern.FindStringSubmatch(key)
	if m == nil {
		return first, last, false
	}
	year, _ := strconv.Atoi(m[1])
	months, start := 12, 1
	switch {
	case m[2] != "":
		start, _ = strconv.Atoi(m[2])
		months = 1
		if start < 1 || start > 12 {
			return first, last, false
		}
	case m[3] != "":
		q, _ := strconv.Atoi(m[3])
		start, months = (q-1)*3+1, 3
	}
	first = time.Date(year, time.Month(start), 1, 0, 0, 0, 0, time.UTC)
	return first, first.AddDate(0, months, -1), true
}

// PeriodMatches reports whether a period key is of the granularity the agreement accrues in
// (MONTH 2027-03, QUARTER 2027-Q1, YEAR 2027).
func PeriodMatches(period, key string) bool {
	switch period {
	case PeriodQuarter:
		return strings.Contains(key, "-Q")
	case PeriodYear:
		return len(key) == 4
	}
	return len(key) == 7 && !strings.Contains(key, "Q")
}

// PeriodOverlapsValidity reports whether any day of the period key falls inside the validity.
func PeriodOverlapsValidity(key string, from, to time.Time) bool {
	first, last, ok := PeriodBounds(key)
	return ok && dayOf(first) <= dayOf(to) && dayOf(last) >= dayOf(from)
}
