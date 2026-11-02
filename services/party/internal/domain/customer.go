package domain

import (
	"regexp"
	"strings"

	"github.com/google/uuid"
)

var (
	customerAcctPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,20}$`)
	creditLimitPattern  = regexp.MustCompile(`^[0-9]{1,16}(\.[0-9]{1,2})?$`)
)

const maxCreditRating = 20

// Customer is the customer role of a party in one legal entity.
type Customer struct {
	ID                    string  `json:"id"`
	PartyID               string  `json:"partyId"`
	LegalEntityID         string  `json:"legalEntityId"`
	AccountNumber         string  `json:"accountNumber"`
	CustomerGroupID       string  `json:"customerGroupId"`
	PaymentTermID         string  `json:"paymentTermId"`
	DeliveryTermID        *string `json:"deliveryTermId,omitempty"`
	DeliveryModeID        *string `json:"deliveryModeId,omitempty"`
	PostingProfileID      string  `json:"postingProfileId"`
	InvoiceAccountPartyID *string `json:"invoiceAccountPartyId,omitempty"`
	CurrencyCode          string  `json:"currencyCode"`
	CreditLimit           *string `json:"creditLimit,omitempty"`
	CreditCurrency        *string `json:"creditCurrency,omitempty"`
	CreditRating          *string `json:"creditRating,omitempty"`
	CreditHold            bool    `json:"creditHold"`
	Blocked               string  `json:"blocked"`
	Version               int     `json:"version"`
}

// NewCustomer is the body of POST /parties/{id}/customer. CreditLimit is a
// decimal string with at most two places, never a float.
type NewCustomer struct {
	AccountNumber         string  `json:"accountNumber"`
	CustomerGroupID       string  `json:"customerGroupId"`
	PaymentTermID         string  `json:"paymentTermId"`
	DeliveryTermID        *string `json:"deliveryTermId"`
	DeliveryModeID        *string `json:"deliveryModeId"`
	PostingProfileID      string  `json:"postingProfileId"`
	InvoiceAccountPartyID *string `json:"invoiceAccountPartyId"`
	CurrencyCode          string  `json:"currencyCode"`
	CreditLimit           *string `json:"creditLimit"`
	CreditCurrency        *string `json:"creditCurrency"`
	CreditRating          *string `json:"creditRating"`
	CreditHold            bool    `json:"creditHold"`
	Blocked               string  `json:"blocked"`
}

func cleanID(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

func cleanOptionalID(p *string) *string {
	if p == nil {
		return nil
	}
	s := cleanID(*p)
	return &s
}

func (c NewCustomer) Clean() NewCustomer {
	c.AccountNumber = strings.TrimSpace(c.AccountNumber)
	c.CustomerGroupID = cleanID(c.CustomerGroupID)
	c.PaymentTermID = cleanID(c.PaymentTermID)
	c.PostingProfileID = cleanID(c.PostingProfileID)
	c.DeliveryTermID = cleanOptionalID(c.DeliveryTermID)
	c.DeliveryModeID = cleanOptionalID(c.DeliveryModeID)
	c.InvoiceAccountPartyID = cleanOptionalID(c.InvoiceAccountPartyID)
	c.CurrencyCode = strings.ToUpper(strings.TrimSpace(c.CurrencyCode))
	c.Blocked = strings.ToLower(strings.TrimSpace(c.Blocked))
	if c.Blocked == "" {
		c.Blocked = "none"
	}
	if c.CreditLimit != nil {
		s := strings.TrimSpace(*c.CreditLimit)
		c.CreditLimit = &s
		if c.CreditCurrency == nil {
			cur := c.CurrencyCode
			c.CreditCurrency = &cur
		}
	}
	if c.CreditCurrency != nil {
		s := strings.ToUpper(strings.TrimSpace(*c.CreditCurrency))
		c.CreditCurrency = &s
	}
	if c.CreditRating != nil {
		s := strings.TrimSpace(*c.CreditRating)
		c.CreditRating = &s
	}
	return c
}

func (c NewCustomer) Validate() map[string]string {
	errs := map[string]string{}
	if !customerAcctPattern.MatchString(c.AccountNumber) {
		errs["accountNumber"] = "is required: 1 to 20 letters, digits, dot, dash or underscore"
	}
	for field, v := range map[string]string{"customerGroupId": c.CustomerGroupID, "paymentTermId": c.PaymentTermID, "postingProfileId": c.PostingProfileID} {
		if _, err := uuid.Parse(v); err != nil {
			errs[field] = "must be a UUID"
		}
	}
	for field, p := range map[string]*string{"deliveryTermId": c.DeliveryTermID, "deliveryModeId": c.DeliveryModeID, "invoiceAccountPartyId": c.InvoiceAccountPartyID} {
		if p == nil {
			continue
		}
		if _, err := uuid.Parse(*p); err != nil {
			errs[field] = "must be a UUID"
		}
	}
	if !currencyPattern.MatchString(c.CurrencyCode) {
		errs["currencyCode"] = "must be a 3-letter ISO 4217 code"
	}
	if c.CreditLimit != nil && !creditLimitPattern.MatchString(*c.CreditLimit) {
		errs["creditLimit"] = "must be a non-negative decimal with at most 2 places"
	}
	if c.CreditCurrency != nil && !currencyPattern.MatchString(*c.CreditCurrency) {
		errs["creditCurrency"] = "must be a 3-letter ISO 4217 code"
	}
	if c.CreditRating != nil && (*c.CreditRating == "" || len([]rune(*c.CreditRating)) > maxCreditRating) {
		errs["creditRating"] = "must be 1 to 20 characters"
	}
	switch c.Blocked {
	case "none", "invoice", "all":
	default:
		errs["blocked"] = "must be none, invoice or all"
	}
	return errs
}
