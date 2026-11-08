package domain

import (
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// Charge code and auto charge codes (FR-17.9). The PRD message catalog (3.17.18) lists no charge
// codes; these are this service's.
const (
	// CodeChargeInactive (PRC-CHG-004): the charge code is inactive, or belongs to another legal entity.
	CodeChargeInactive = "PRC-CHG-004"
	// CodeChargeCalcMismatch (PRC-CHG-005): the calculation on a PO charge differs from its charge code's.
	CodeChargeCalcMismatch = "PRC-CHG-005"
)

// Sides a charge is debited to (DebitType) and credited to (CreditType) (FR-17.9): ITEM capitalizes
// the charge into the inventory cost of the lines, LEDGER books it to an account, VENDOR makes it
// payable to a vendor. A charge is never credited to the item.
const (
	ChargeSideItem   = "ITEM"
	ChargeSideLedger = "LEDGER"
	ChargeSideVendor = "VENDOR"
)

// Rules that allocate a header charge to lines at invoice time (FR-17.9; wave 18 applies them).
const (
	AllocateQty    = "QTY"
	AllocateAmount = "AMOUNT"
	AllocateLines  = "LINES"
)

// Charge code and auto charge statuses.
const (
	ChargeActive   = "ACTIVE"
	ChargeInactive = "INACTIVE"
)

// What an auto charge is keyed on: one vendor, the vendors of one vendor group, or every vendor.
const (
	AutoChargeVendor = "VENDOR"
	AutoChargeGroup  = "GROUP"
	AutoChargeAll    = "ALL"
)

// What an auto charge is keyed on besides the vendor: every item (a header charge on the PO), one
// product, or the products of one product group (a line charge on each matching line).
const (
	AutoChargeItemAll     = "ALL"
	AutoChargeItemProduct = "PRODUCT"
	AutoChargeItemGroup   = "GROUP"
)

// ValidChargeCalc reports whether c is FIXED, PER_UNIT or PERCENT.
func ValidChargeCalc(c string) bool { return c == CalcFixed || c == CalcPerUnit || c == CalcPercent }

// ValidAllocationRule reports whether r is QTY, AMOUNT or LINES.
func ValidAllocationRule(r string) bool {
	return r == AllocateQty || r == AllocateAmount || r == AllocateLines
}

// ValidChargeStatus reports whether s is ACTIVE or INACTIVE.
func ValidChargeStatus(s string) bool { return s == ChargeActive || s == ChargeInactive }

// ChargeCode names a kind of charge of a legal entity (FR-17.9): its calculation and default value, the
// debit and credit sides, and the rule that allocates it to lines. DebitAccountID and CreditAccountID
// name ledger accounts (wave 11) for a LEDGER side; VendorID is the vendor a VENDOR side is payable to.
type ChargeCode struct {
	ID              string          `json:"id"`
	LegalEntityID   string          `json:"legalEntityId"`
	Code            string          `json:"code"`
	Name            string          `json:"name"`
	Calc            string          `json:"calc"`
	DefaultValue    decimal.Decimal `json:"defaultValue"`
	DebitType       string          `json:"debitType"`
	CreditType      string          `json:"creditType"`
	DebitAccountID  string          `json:"debitAccountId,omitempty"`
	CreditAccountID string          `json:"creditAccountId,omitempty"`
	VendorID        string          `json:"vendorId,omitempty"`
	AllocationRule  string          `json:"allocationRule"`
	Status          string          `json:"status"`
	Version         int             `json:"version"`
	CreatedBy       string          `json:"createdBy,omitempty"`
	CreatedAt       time.Time       `json:"createdAt"`
	UpdatedBy       string          `json:"updatedBy,omitempty"`
	UpdatedAt       time.Time       `json:"updatedAt"`
}

// NormalizeChargeCode trims and upper-cases the code and trims the name.
func NormalizeChargeCode(c ChargeCode) ChargeCode {
	c.Code = strings.ToUpper(strings.TrimSpace(c.Code))
	c.Name = strings.TrimSpace(c.Name)
	return c
}

// Validate checks a charge code's own rules: the code and name, the calculation and value, and that
// each side has what it books to and nothing else (a LEDGER side an account, a VENDOR debit a vendor,
// a VENDOR credit optionally the vendor it is payable to instead of the PO vendor). The account ids are
// wave 11 ledger accounts and are not checked against the chart of accounts yet. It returns the first
// violation as COM-0001.
func (c ChargeCode) Validate() *Error {
	switch {
	case c.Code == "" || len(c.Code) > 20 || strings.Trim(c.Code, "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-") != "":
		return fail(CodeValidation, "code must be 1 to 20 characters of A-Z, 0-9, _ and -")
	case c.Name == "" || len([]rune(c.Name)) > 200:
		return fail(CodeValidation, "name is required and at most 200 characters")
	case !ValidChargeCalc(c.Calc):
		return fail(CodeValidation, "calc must be FIXED, PER_UNIT or PERCENT")
	case c.DefaultValue.Sign() < 0:
		return fail(CodeValidation, "defaultValue must not be negative")
	case c.Calc == CalcPercent && c.DefaultValue.GreaterThan(hundred):
		return fail(CodeValidation, "defaultValue of a PERCENT charge must be at most 100")
	case c.DebitType != ChargeSideItem && c.DebitType != ChargeSideLedger && c.DebitType != ChargeSideVendor:
		return fail(CodeValidation, "debitType must be ITEM, LEDGER or VENDOR")
	case c.CreditType != ChargeSideLedger && c.CreditType != ChargeSideVendor:
		return fail(CodeValidation, "creditType must be LEDGER or VENDOR")
	case c.DebitType == ChargeSideLedger && c.DebitAccountID == "":
		return fail(CodeValidation, "debitAccountId is required for a LEDGER debit")
	case c.CreditType == ChargeSideLedger && c.CreditAccountID == "":
		return fail(CodeValidation, "creditAccountId is required for a LEDGER credit")
	case c.DebitType == ChargeSideVendor && c.VendorID == "":
		return fail(CodeValidation, "vendorId is required for a VENDOR debit")
	case c.DebitType == ChargeSideVendor && c.CreditType == ChargeSideVendor:
		return fail(CodeValidation, "a VENDOR debit needs a LEDGER credit; vendorId names one vendor")
	case c.DebitType != ChargeSideLedger && c.DebitAccountID != "":
		return fail(CodeValidation, "debitAccountId is only for a LEDGER debit")
	case c.CreditType != ChargeSideLedger && c.CreditAccountID != "":
		return fail(CodeValidation, "creditAccountId is only for a LEDGER credit")
	case c.VendorID != "" && c.DebitType != ChargeSideVendor && c.CreditType != ChargeSideVendor:
		return fail(CodeValidation, "vendorId is only for a VENDOR side")
	case !ValidAllocationRule(c.AllocationRule):
		return fail(CodeValidation, "allocationRule must be QTY, AMOUNT or LINES")
	case !ValidChargeStatus(c.Status):
		return fail(CodeValidation, "status must be ACTIVE or INACTIVE")
	}
	return nil
}

// AutoCharge adds a charge code to the POs of a vendor, of a vendor group or of every vendor in a legal
// entity (FR-17.9). Value overrides the code's default value; MinOrderAmount, in Currency, applies it
// only to POs whose lines total at least that much after discount. A FIXED or PER_UNIT charge is an
// amount of money, so it applies only to POs in Currency; a PERCENT charge with no Currency applies
// in every currency. The window and status decide whether it applies on a day.
type AutoCharge struct {
	ID            string `json:"id"`
	LegalEntityID string `json:"legalEntityId"`
	ChargeCodeID  string `json:"chargeCodeId"`
	AccountCode   string `json:"accountCode"`
	AccountID     string `json:"accountId,omitempty"`
	// ItemCode and ItemID key the charge on products (FR-17.9): ALL (no ItemID) adds a header charge,
	// PRODUCT or GROUP adds a line charge to each line of that product or product group.
	ItemCode       string           `json:"itemCode"`
	ItemID         string           `json:"itemId,omitempty"`
	Value          *decimal.Decimal `json:"value,omitempty"`
	MinOrderAmount *decimal.Decimal `json:"minOrderAmount,omitempty"`
	Currency       string           `json:"currency,omitempty"`
	ValidFrom      Day              `json:"validFrom"`
	ValidTo        *Day             `json:"validTo,omitempty"`
	Status         string           `json:"status"`
	Version        int              `json:"version"`
	CreatedBy      string           `json:"createdBy,omitempty"`
	CreatedAt      time.Time        `json:"createdAt"`
	UpdatedBy      string           `json:"updatedBy,omitempty"`
	UpdatedAt      time.Time        `json:"updatedAt"`
}

// Validate checks an auto charge against its charge code: the key, the value, and that the
// currency is given whenever the charge is an amount of money or has a minimum order amount.
func (a AutoCharge) Validate(code ChargeCode) *Error {
	switch {
	case a.AccountCode != AutoChargeVendor && a.AccountCode != AutoChargeGroup && a.AccountCode != AutoChargeAll:
		return fail(CodeValidation, "accountCode must be VENDOR, GROUP or ALL")
	case (a.AccountCode == AutoChargeAll) != (a.AccountID == ""):
		return fail(CodeValidation, "accountId is required for VENDOR and GROUP and must be empty for ALL")
	case a.ItemCode != AutoChargeItemAll && a.ItemCode != AutoChargeItemProduct && a.ItemCode != AutoChargeItemGroup:
		return fail(CodeValidation, "itemCode must be PRODUCT, GROUP or ALL")
	case (a.ItemCode == AutoChargeItemAll) != (a.ItemID == ""):
		return fail(CodeValidation, "itemId is required for PRODUCT and GROUP and must be empty for ALL")
	case !ValidChargeStatus(a.Status):
		return fail(CodeValidation, "status must be ACTIVE or INACTIVE")
	case a.Value != nil && a.Value.Sign() < 0:
		return fail(CodeValidation, "value must not be negative")
	case a.Value != nil && code.Calc == CalcPercent && a.Value.GreaterThan(hundred):
		return fail(CodeValidation, "value of a PERCENT charge must be at most 100")
	case a.MinOrderAmount != nil && a.MinOrderAmount.Sign() <= 0:
		return fail(CodeValidation, "minOrderAmount must be positive")
	case a.MinOrderAmount != nil && a.Currency == "":
		return fail(CodeValidation, "currency is required with minOrderAmount")
	case code.Calc != CalcPercent && a.Currency == "":
		return fail(CodeValidation, "currency is required for a FIXED or PER_UNIT charge")
	case a.ValidTo != nil && a.ValidTo.Before(a.ValidFrom.Time):
		return fail(CodeValidation, "validTo must not be before validFrom")
	}
	return nil
}

// Resolve returns the calculation and value the auto charge adds to a PO: the code's calculation and
// the auto charge's value, or the code's default value when it sets none.
func (a AutoCharge) Resolve(code ChargeCode) (calc string, value decimal.Decimal) {
	if a.Value != nil {
		return code.Calc, *a.Value
	}
	return code.Calc, code.DefaultValue
}

// Applies reports whether the auto charge adds its code to a PO of vendorID (a member of
// vendorGroups) in currency on day, whose lines total orderBase after discount.
func (a AutoCharge) Applies(vendorID string, vendorGroups []string, currency string, orderBase decimal.Decimal, on Day) bool {
	if a.Status != ChargeActive || on.Before(a.ValidFrom.Time) || (a.ValidTo != nil && on.After(a.ValidTo.Time)) {
		return false
	}
	switch a.AccountCode {
	case AutoChargeVendor:
		if a.AccountID != vendorID {
			return false
		}
	case AutoChargeGroup:
		found := false
		for _, g := range vendorGroups {
			if g == a.AccountID {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if a.Currency != "" && a.Currency != currency {
		return false
	}
	return a.MinOrderAmount == nil || !orderBase.LessThan(*a.MinOrderAmount)
}

// OnLines reports whether the auto charge is a line charge (keyed on a product or product group)
// and not a header charge.
func (a AutoCharge) OnLines() bool {
	return a.ItemCode == AutoChargeItemProduct || a.ItemCode == AutoChargeItemGroup
}

// MatchesLine reports whether a line charge of the auto charge belongs on a line of productID, a member
// of productGroups. A header auto charge matches no line.
func (a AutoCharge) MatchesLine(productID string, productGroups []string) bool {
	switch a.ItemCode {
	case AutoChargeItemProduct:
		return productID != "" && a.ItemID == productID
	case AutoChargeItemGroup:
		for _, g := range productGroups {
			if g == a.ItemID {
				return true
			}
		}
	}
	return false
}
