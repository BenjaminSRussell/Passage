package domain

import (
	"fmt"
	"sort"

	"github.com/shopspring/decimal"
)

// CodeNothingToRemit is TAX-0014: no unremitted withholding in the period.
const CodeNothingToRemit = "TAX-0014"

// WithholdingEntry is the withholding of one payment, recorded when the payment was made (the result of POST
// /v1/tax/withholding/calculate): what was paid, the base tax was withheld on, and the tax withheld.
type WithholdingEntry struct {
	ID          string `json:"id"`
	PartyType   string `json:"party_type"`
	PartyRef    string `json:"party_ref"`
	Code        string `json:"code"`
	PaymentDate Date   `json:"payment_date"`
	Currency    string `json:"currency"`
	Gross       Dec    `json:"gross_amount"`
	TaxableBase Dec    `json:"taxable_base"`
	Withheld    Dec    `json:"withholding_amount"`
}

// ValidateWithholdingEntry checks an entry: the payee is named, the amounts are consistent (the base is at most the gross, the tax at
// most the base) and at most six decimals.
func ValidateWithholdingEntry(e WithholdingEntry) error {
	switch {
	case !oneOf(e.PartyType, PartyVendor, PartyCustomer):
		return invalid("party_type", "party_type must be %q or %q", PartyVendor, PartyCustomer)
	case e.PartyRef == "" || len(e.PartyRef) > 64:
		return invalid("party_ref", "party_ref is required and at most 64 characters")
	case e.Code == "" || len(e.Code) > 64:
		return invalid("code", "code is required and at most 64 characters")
	case e.PaymentDate.IsZero():
		return invalid("payment_date", "payment_date is required")
	case len(e.Currency) != 3 || e.Currency != upper(e.Currency):
		return invalid("currency", "currency must be a 3-letter ISO 4217 code")
	case !e.Gross.IsPositive():
		return invalid("gross_amount", "gross_amount must be positive")
	case e.TaxableBase.IsNegative() || e.TaxableBase.GreaterThan(e.Gross.Decimal):
		return invalid("taxable_base", "taxable_base must be between 0 and the gross amount")
	case e.Withheld.IsNegative() || e.Withheld.GreaterThan(e.TaxableBase.Decimal):
		return invalid("withholding_amount", "withholding_amount must be between 0 and the taxable base")
	case e.Gross.Exponent() < -maxScale || e.TaxableBase.Exponent() < -maxScale || e.Withheld.Exponent() < -maxScale:
		return invalid("gross_amount", "amounts have at most %d decimal places", maxScale)
	}
	return nil
}

func upper(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'z' {
			b[i] = c - 32
		}
	}
	return string(b)
}

// RemittanceLine is what one run owes the authority for a code in a currency.
type RemittanceLine struct {
	Code        string          `json:"code"`
	Currency    string          `json:"currency"`
	Gross       decimal.Decimal `json:"gross_amount"`
	TaxableBase decimal.Decimal `json:"taxable_base"`
	Withheld    decimal.Decimal `json:"withholding_amount"`
	Entries     int             `json:"entries"`
	Payees      int             `json:"payees"`
}

// WithholdingCertificate is the certificate of the tax withheld from one payee for a code and currency in a run's period.
type WithholdingCertificate struct {
	ID          string          `json:"id"`
	Number      string          `json:"number"`
	RunID       string          `json:"run_id"`
	PartyType   string          `json:"party_type"`
	PartyRef    string          `json:"party_ref"`
	Code        string          `json:"code"`
	Currency    string          `json:"currency"`
	PeriodFrom  Date            `json:"period_from"`
	PeriodTo    Date            `json:"period_to"`
	Gross       decimal.Decimal `json:"gross_amount"`
	TaxableBase decimal.Decimal `json:"taxable_base"`
	Withheld    decimal.Decimal `json:"withholding_amount"`
	Entries     int             `json:"entry_count"`
}

// CertificateGroup is the payee, code and currency a certificate covers and the entries it totals.
type CertificateGroup struct {
	PartyType, PartyRef, Code, Currency string
	Gross, TaxableBase, Withheld        decimal.Decimal
	EntryIDs                            []string
}

// PlanRemittance groups the entries of a run: one line per code and currency, one certificate group per payee, code and currency, both
// in a fixed order, so the numbers a run hands out follow the same order whenever it runs.
func PlanRemittance(entries []WithholdingEntry) ([]RemittanceLine, []CertificateGroup) {
	type lineKey struct{ code, currency string }
	type groupKey struct{ partyType, partyRef, code, currency string }
	lines := map[lineKey]*RemittanceLine{}
	payees := map[lineKey]map[string]bool{}
	groups := map[groupKey]*CertificateGroup{}
	for _, e := range entries {
		lk := lineKey{e.Code, e.Currency}
		l := lines[lk]
		if l == nil {
			l = &RemittanceLine{Code: e.Code, Currency: e.Currency}
			lines[lk] = l
			payees[lk] = map[string]bool{}
		}
		l.Gross, l.TaxableBase, l.Withheld = l.Gross.Add(e.Gross.Decimal), l.TaxableBase.Add(e.TaxableBase.Decimal), l.Withheld.Add(e.Withheld.Decimal)
		l.Entries++
		payees[lk][e.PartyType+"|"+e.PartyRef] = true

		gk := groupKey{e.PartyType, e.PartyRef, e.Code, e.Currency}
		g := groups[gk]
		if g == nil {
			g = &CertificateGroup{PartyType: e.PartyType, PartyRef: e.PartyRef, Code: e.Code, Currency: e.Currency}
			groups[gk] = g
		}
		g.Gross, g.TaxableBase, g.Withheld = g.Gross.Add(e.Gross.Decimal), g.TaxableBase.Add(e.TaxableBase.Decimal), g.Withheld.Add(e.Withheld.Decimal)
		g.EntryIDs = append(g.EntryIDs, e.ID)
	}
	outLines := make([]RemittanceLine, 0, len(lines))
	for lk, l := range lines {
		l.Payees = len(payees[lk])
		outLines = append(outLines, *l)
	}
	sort.Slice(outLines, func(i, j int) bool {
		if outLines[i].Code != outLines[j].Code {
			return outLines[i].Code < outLines[j].Code
		}
		return outLines[i].Currency < outLines[j].Currency
	})
	outGroups := make([]CertificateGroup, 0, len(groups))
	for _, g := range groups {
		outGroups = append(outGroups, *g)
	}
	sort.Slice(outGroups, func(i, j int) bool {
		a, b := outGroups[i], outGroups[j]
		return fmt.Sprint(a.Code, "|", a.Currency, "|", a.PartyType, "|", a.PartyRef) < fmt.Sprint(b.Code, "|", b.Currency, "|", b.PartyType, "|", b.PartyRef)
	})
	return outLines, outGroups
}

// RemittanceNumber is the number of a remittance run: WHR-<year>-<sequence>, the sequence gap-free per legal entity and year.
func RemittanceNumber(year, seq int) string { return fmt.Sprintf("WHR-%d-%04d", year, seq) }

// CertificateNumber is the number of a withholding certificate: WHC-<year>-<sequence>, gap-free per legal entity and year.
func CertificateNumber(year, seq int) string { return fmt.Sprintf("WHC-%d-%06d", year, seq) }
