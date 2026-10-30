package domain

import (
	"regexp"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// BillUnmatched is the freight bill state at creation, before any carrier invoice.
const BillUnmatched = "UNMATCHED"

var (
	currencyRe = regexp.MustCompile(`^[A-Z]{3}$`)
	// invoiceSources lists how a carrier invoice arrived (EDI 210, manual or API).
	invoiceSources = []string{"EDI210", "MANUAL", "API"}
)

// FreightBill holds the expected charges for one load and carrier, created at
// ship confirmation from rating (FR-34.7).
type FreightBill struct {
	ID               string           `json:"id"`
	LoadID           string           `json:"loadId"`
	CarrierID        string           `json:"carrierId"`
	Currency         string           `json:"currency"`
	ExpectedLines    []Line           `json:"expectedLines"`
	ExpectedTotal    decimal.Decimal  `json:"expectedTotal"`
	MatchStatus      string           `json:"matchStatus"`
	CarrierInvoiceID *string          `json:"carrierInvoiceId,omitempty"`
	VarianceTotal    *decimal.Decimal `json:"varianceTotal,omitempty"`
	Version          int              `json:"version"`
	CreatedAt        time.Time        `json:"createdAt"`
	LegalEntityID    string           `json:"-"`
}

// CarrierInvoice is an invoice received from a carrier. FreightBillID is nil
// when no bill exists, which makes the invoice an UNEXPECTED_INVOICE exception.
type CarrierInvoice struct {
	ID            string           `json:"id"`
	CarrierID     string           `json:"carrierId"`
	InvoiceNo     string           `json:"invoiceNo"`
	InvoiceDate   string           `json:"invoiceDate"`
	Source        string           `json:"source"`
	FreightBillID *string          `json:"freightBillId,omitempty"`
	Currency      string           `json:"currency"`
	Lines         []Line           `json:"lines"`
	Total         decimal.Decimal  `json:"total"`
	MatchStatus   string           `json:"matchStatus"`
	VarianceTotal *decimal.Decimal `json:"varianceTotal,omitempty"`
	CreatedAt     time.Time        `json:"createdAt"`
	LegalEntityID string           `json:"-"`
}

// SumLines adds the amounts of lines.
func SumLines(lines []Line) decimal.Decimal {
	total := decimal.Zero
	for _, l := range lines {
		total = total.Add(l.Amount)
	}
	return total
}

// ValidateLines reports field errors for a list of charges under field.
func ValidateLines(lines []Line, field string) map[string]string {
	f := map[string]string{}
	if len(lines) == 0 {
		f[field] = "at least one charge line is required"
	}
	for _, l := range lines {
		if strings.TrimSpace(l.ChargeCode) == "" || len(l.ChargeCode) > 20 {
			f[field] = "each line needs a charge code of 1 to 20 characters"
		}
		if l.Amount.IsNegative() {
			f[field] = "charge amounts must not be negative"
		}
	}
	return f
}

// ValidateFreightBill reports field errors for a new freight bill.
func ValidateFreightBill(b FreightBill) map[string]string {
	f := ValidateLines(b.ExpectedLines, "expectedLines")
	if !currencyRe.MatchString(b.Currency) {
		f["currency"] = "must be an ISO 4217 alpha-3 code"
	}
	return f
}

// ValidateCarrierInvoice reports field errors for a new carrier invoice.
func ValidateCarrierInvoice(inv CarrierInvoice) map[string]string {
	f := ValidateLines(inv.Lines, "lines")
	if n := len(strings.TrimSpace(inv.InvoiceNo)); n == 0 || n > 40 {
		f["invoiceNo"] = "must be 1 to 40 characters"
	}
	if !currencyRe.MatchString(inv.Currency) {
		f["currency"] = "must be an ISO 4217 alpha-3 code"
	}
	if !contains(invoiceSources, inv.Source) {
		f["source"] = "must be EDI210, MANUAL or API"
	}
	return f
}

// InvoiceFits reports why an invoice cannot be matched to a bill: it must come
// from the bill's carrier and be in the bill's currency (TMS-0112). Currency
// conversion is not applied to carrier invoices.
func InvoiceFits(b FreightBill, inv CarrierInvoice) map[string]string {
	f := map[string]string{}
	if b.CarrierID != inv.CarrierID {
		f["carrierId"] = "must be the carrier of the freight bill"
	}
	if b.Currency != inv.Currency {
		f["currency"] = "must be the freight bill currency " + b.Currency
	}
	return f
}

// MatchBill compares a freight bill with a carrier invoice using the default
// tolerance (BR-34.4). Until the parameter registry is read, every carrier uses it.
func MatchBill(b FreightBill, inv CarrierInvoice) MatchResult {
	return MatchInvoice(b.ExpectedLines, inv.Lines, DefaultTolerance)
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}
