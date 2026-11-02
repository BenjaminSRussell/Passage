package domain

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// Codes of the opening-item import (integration handoff H5). Row codes appear in
// the per-row results of POST /open-items:batch.
const (
	CodeCreditItem            = "AR-CREDIT-ITEM"
	CodeOpeningInvalid        = "AR-OPENING-INVALID"
	CodeOpeningDuplicateKey   = "AR-OPENING-DUPLICATE-KEY"
	CodeOpeningIdentity       = "AR-OPENING-IDENTITY"
	CodeOpeningSettled        = "AR-OPENING-SETTLED"
	CodeOpeningInCollection   = "AR-OPENING-IN-COLLECTION"
	CodeOpeningDocumentExists = "AR-OPENING-DOCUMENT-EXISTS"
	CodeOpeningRejected       = "AR-OPENING-REJECTED"
)

// Import limits. MaxOpeningRows keeps one request and its event small; the loader
// sends several batches. Amounts fit numeric(20,6).
const (
	MaxOpeningRows   = 1000
	MaxLegacyKey     = 120
	MaxDocumentRef   = 60
	MaxImportSource  = 40
	openingScale     = 6
	openingMaxDigits = 14
)

var (
	openingCurrency = regexp.MustCompile(`^[A-Z]{3}$`)
	openingUUID     = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	// ImportSourceName is the legacy system label: letters, digits, dot, dash, underscore.
	ImportSourceName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
)

// OpeningRow is one legacy open item as the loader sends it. Amounts are
// magnitudes (positive); Side says whether the item is owed by the customer
// (DEBIT) or to the customer (CREDIT).
type OpeningRow struct {
	LegacyKey          string
	DocumentNumber     string
	CustomerID         string
	Kind               string
	Side               string
	DocumentDate       string
	DueDate            string
	Currency           string
	OriginalAmount     decimal.Decimal
	OpenAmount         decimal.Decimal
	CashDiscountDate   *string
	CashDiscountAmount decimal.Decimal
}

// OpeningKinds maps the loader's kind to the open item source type and the side
// it may carry. An ADJUSTMENT may be either side.
var OpeningKinds = map[string][]string{
	"INVOICE":     {"DEBIT"},
	"CREDIT_NOTE": {"CREDIT"},
	"PAYMENT":     {"CREDIT"},
	"ADJUSTMENT":  {"DEBIT", "CREDIT"},
}

// Signed returns the stored amounts: negative for a credit-side item.
func (r OpeningRow) Signed() (amount, open decimal.Decimal) {
	if r.Side == "CREDIT" {
		return r.OriginalAmount.Neg(), r.OpenAmount.Neg()
	}
	return r.OriginalAmount, r.OpenAmount
}

// ValidateOpeningRow returns field -> rule for everything wrong with a row; an
// empty map means the row may be stored.
func ValidateOpeningRow(r OpeningRow) map[string]string {
	f := map[string]string{}
	if k := strings.TrimSpace(r.LegacyKey); k == "" || len(r.LegacyKey) > MaxLegacyKey {
		f["legacyKey"] = fmt.Sprintf("must be 1 to %d characters", MaxLegacyKey)
	}
	if d := strings.TrimSpace(r.DocumentNumber); d == "" || len(r.DocumentNumber) > MaxDocumentRef {
		f["documentNumber"] = fmt.Sprintf("must be 1 to %d characters", MaxDocumentRef)
	}
	if !openingUUID.MatchString(r.CustomerID) {
		f["customerId"] = "must be a UUID"
	}
	sides, ok := OpeningKinds[r.Kind]
	switch {
	case !ok:
		f["kind"] = "must be INVOICE, CREDIT_NOTE, PAYMENT or ADJUSTMENT"
	case r.Side != "DEBIT" && r.Side != "CREDIT":
		f["side"] = "must be DEBIT or CREDIT"
	default:
		allowed := false
		for _, s := range sides {
			allowed = allowed || s == r.Side
		}
		if !allowed {
			f["side"] = fmt.Sprintf("a %s is a %s item", r.Kind, strings.Join(sides, " or "))
		}
	}
	doc, docErr := time.Parse("2006-01-02", r.DocumentDate)
	if docErr != nil {
		f["documentDate"] = "must be a YYYY-MM-DD date"
	}
	if _, err := time.Parse("2006-01-02", r.DueDate); err != nil {
		f["dueDate"] = "must be a YYYY-MM-DD date"
	}
	if r.CashDiscountDate != nil {
		if d, err := time.Parse("2006-01-02", *r.CashDiscountDate); err != nil {
			f["cashDiscountDate"] = "must be a YYYY-MM-DD date"
		} else if docErr == nil && d.Before(doc) {
			f["cashDiscountDate"] = "must not be before the document date"
		}
	}
	if !openingCurrency.MatchString(r.Currency) {
		f["currency"] = "must be an ISO 4217 alpha-3 code"
	}
	if msg := openingAmountRule(r.OriginalAmount); msg != "" {
		f["originalAmount"] = msg
	}
	if msg := openingAmountRule(r.OpenAmount); msg != "" {
		f["openAmount"] = msg
	} else if f["originalAmount"] == "" && r.OpenAmount.GreaterThan(r.OriginalAmount) {
		f["openAmount"] = "must not exceed originalAmount (amounts are magnitudes; the side gives the sign)"
	}
	if r.CashDiscountAmount.IsNegative() || r.CashDiscountAmount.Round(openingScale).Cmp(r.CashDiscountAmount) != 0 {
		f["cashDiscountAmount"] = "must be zero or a positive decimal with at most 6 decimals"
	} else if r.CashDiscountAmount.GreaterThan(r.OpenAmount) {
		f["cashDiscountAmount"] = "must not exceed openAmount"
	}
	return f
}

func openingAmountRule(d decimal.Decimal) string {
	switch {
	case !d.IsPositive():
		return "must be a positive decimal; an item with nothing open is not an open item"
	case d.Round(openingScale).Cmp(d) != 0:
		return "must have at most 6 decimals"
	case d.GreaterThanOrEqual(decimal.New(1, openingMaxDigits)):
		return "must be below 100000000000000"
	}
	return ""
}
