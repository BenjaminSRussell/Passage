package domain

import (
	"regexp"
	"strings"

	"github.com/google/uuid"
)

const (
	RoleVendor      = "vendor"
	AggregateVendor = "vendor"
)

var (
	currencyPattern   = regexp.MustCompile(`^[A-Z]{3}$`)
	countryPattern    = regexp.MustCompile(`^[A-Z]{2}$`)
	accountNoPattern  = regexp.MustCompile(`^[A-Z0-9]{4,34}$`)
	routingPattern    = regexp.MustCompile(`^[A-Z0-9]{4,20}$`)
	vendorAcctPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,20}$`)
)

type Vendor struct {
	ID            string  `json:"id"`
	PartyID       string  `json:"partyId"`
	LegalEntityID string  `json:"legalEntityId"`
	AccountNumber string  `json:"accountNumber"`
	VendorGroupID string  `json:"vendorGroupId"`
	PaymentTermID *string `json:"paymentTermId,omitempty"`
	CurrencyCode  string  `json:"currencyCode"`
	Blocked       string  `json:"blocked"`
	Version       int     `json:"version"`
}

type NewVendor struct {
	AccountNumber string  `json:"accountNumber"`
	VendorGroupID string  `json:"vendorGroupId"`
	PaymentTermID *string `json:"paymentTermId"`
	CurrencyCode  string  `json:"currencyCode"`
}

type BankAccount struct {
	ID            string `json:"id"`
	VendorID      string `json:"vendorId"`
	AccountHolder string `json:"accountHolder"`
	BankCountry   string `json:"bankCountry"`
	CurrencyCode  string `json:"currencyCode"`
	AccountLast4  string `json:"accountLast4"`
	AccountNumber string `json:"accountNumber,omitempty"`
	RoutingNumber string `json:"routingNumber,omitempty"`
}

type NewBankAccount struct {
	AccountHolder string `json:"accountHolder"`
	BankCountry   string `json:"bankCountry"`
	CurrencyCode  string `json:"currencyCode"`
	AccountNumber string `json:"accountNumber"`
	RoutingNumber string `json:"routingNumber"`
}

func (v NewVendor) Clean() NewVendor {
	v.AccountNumber = strings.TrimSpace(v.AccountNumber)
	v.VendorGroupID = strings.ToLower(strings.TrimSpace(v.VendorGroupID))
	v.CurrencyCode = strings.ToUpper(strings.TrimSpace(v.CurrencyCode))
	if v.PaymentTermID != nil {
		id := strings.ToLower(strings.TrimSpace(*v.PaymentTermID))
		v.PaymentTermID = &id
	}
	return v
}

func (v NewVendor) Validate() map[string]string {
	errs := map[string]string{}
	if !vendorAcctPattern.MatchString(v.AccountNumber) {
		errs["accountNumber"] = "is required: 1 to 20 letters, digits, dot, dash or underscore"
	}
	if _, err := uuid.Parse(v.VendorGroupID); err != nil {
		errs["vendorGroupId"] = "must be a UUID"
	}
	if v.PaymentTermID != nil {
		if _, err := uuid.Parse(*v.PaymentTermID); err != nil {
			errs["paymentTermId"] = "must be a UUID"
		}
	}
	if !currencyPattern.MatchString(v.CurrencyCode) {
		errs["currencyCode"] = "must be a 3-letter ISO 4217 code"
	}
	return errs
}

func (b NewBankAccount) Clean() NewBankAccount {
	b.AccountHolder = strings.TrimSpace(b.AccountHolder)
	b.BankCountry = strings.ToUpper(strings.TrimSpace(b.BankCountry))
	b.CurrencyCode = strings.ToUpper(strings.TrimSpace(b.CurrencyCode))
	b.AccountNumber = NormalizeTaxNumber(b.AccountNumber)
	b.RoutingNumber = NormalizeTaxNumber(b.RoutingNumber)
	return b
}

func (b NewBankAccount) Validate() map[string]string {
	errs := map[string]string{}
	if n := len([]rune(b.AccountHolder)); n < 1 || n > 140 {
		errs["accountHolder"] = "is required, at most 140 characters"
	}
	if !countryPattern.MatchString(b.BankCountry) {
		errs["bankCountry"] = "must be a 2-letter ISO 3166 code"
	}
	if !currencyPattern.MatchString(b.CurrencyCode) {
		errs["currencyCode"] = "must be a 3-letter ISO 4217 code"
	}
	if !accountNoPattern.MatchString(b.AccountNumber) {
		errs["accountNumber"] = "must be an IBAN or account number of 4 to 34 letters and digits"
	}
	if b.RoutingNumber != "" && !routingPattern.MatchString(b.RoutingNumber) {
		errs["routingNumber"] = "must be 4 to 20 letters and digits"
	}
	return errs
}

func (b NewBankAccount) Last4() string { return b.AccountNumber[len(b.AccountNumber)-4:] }
