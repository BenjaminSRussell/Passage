package domain

import (
	"github.com/shopspring/decimal"
)

// RemittanceItem is one RMR line of an 820: the invoice being paid, the amount
// paid against it, the invoice amount and any discount taken. Paid may be
// negative for a credit memo netted into the payment.
type RemittanceItem struct {
	InvoiceNumber string          `json:"invoiceNumber"`
	InvoiceDate   string          `json:"invoiceDate,omitempty"`
	Paid          decimal.Decimal `json:"paid"`
	Invoiced      decimal.Decimal `json:"invoiced"`
	Discount      decimal.Decimal `json:"discount"`
}

// Remittance is the canonical payment order / remittance advice of an X12 820
// (004010). Amount is BPR02 and must equal the sum of the items' Paid. Method
// is BPR04 (ACH, CHK, FWT or BOP); PaymentDate is BPR16 (CCYYMMDD); TraceNumber
// is TRN02, the check or ACH trace the payee reconciles against.
type Remittance struct {
	TraceNumber string           `json:"traceNumber"`
	PaymentDate string           `json:"paymentDate"`
	Method      string           `json:"method"`
	Currency    string           `json:"currency,omitempty"`
	Amount      decimal.Decimal  `json:"amount"`
	Payer       Location         `json:"payer"`
	Payee       Location         `json:"payee"`
	Items       []RemittanceItem `json:"items"`
}

var paymentMethods = map[string]bool{"ACH": true, "CHK": true, "FWT": true, "BOP": true}

// Build820 maps a remittance to the body of an X12 820 in segment order:
// BPR, TRN, CUR, N1 PR and PE, then one ENT loop with RMR and DTM per item.
// The payment amount must equal the items paid: an 820 whose remittance lines
// do not add up to the money moved is refused.
func Build820(r Remittance) (Transaction, error) {
	if r.TraceNumber == "" || len(r.TraceNumber) > 50 {
		return Transaction{}, outboundf("trace number must be 1 to 50 characters")
	}
	if !isDigits(r.PaymentDate, 8) {
		return Transaction{}, outboundf("payment date must be CCYYMMDD")
	}
	if !paymentMethods[r.Method] {
		return Transaction{}, outboundf("payment method %q is not ACH, CHK, FWT or BOP", r.Method)
	}
	if r.Currency != "" && !currencyRe.MatchString(r.Currency) {
		return Transaction{}, outboundf("currency must be an ISO 4217 code")
	}
	if !isGLN(r.Payer.GLN) || !isGLN(r.Payee.GLN) {
		return Transaction{}, outboundf("payer and payee GLNs must be 13 digits")
	}
	if len(r.Items) == 0 {
		return Transaction{}, outboundf("a remittance needs at least one item")
	}
	if !r.Amount.IsPositive() || !twoDecimals(r.Amount) {
		return Transaction{}, outboundf("payment amount must be positive with at most two decimals")
	}
	sum := decimal.Zero
	for _, it := range r.Items {
		if it.InvoiceNumber == "" || len(it.InvoiceNumber) > 30 {
			return Transaction{}, outboundf("item invoice number must be 1 to 30 characters")
		}
		if it.InvoiceDate != "" && !isDigits(it.InvoiceDate, 8) {
			return Transaction{}, outboundf("item %s invoice date must be CCYYMMDD", it.InvoiceNumber)
		}
		if it.Paid.IsZero() || !twoDecimals(it.Paid) || !twoDecimals(it.Invoiced) || !twoDecimals(it.Discount) || it.Discount.IsNegative() {
			return Transaction{}, outboundf("item %s needs a non-zero paid amount and two-decimal invoice and non-negative discount amounts", it.InvoiceNumber)
		}
		sum = sum.Add(it.Paid)
	}
	if !sum.Equal(r.Amount) {
		return Transaction{}, outboundf("payment amount %s differs from the items paid %s (%s)", r.Amount, sum, CodeArithmetic)
	}
	b := &segWriter{}
	b.seg("BPR", "C", r.Amount.StringFixed(2), "C", r.Method, "", "", "", "", "", "", "", "", "", "", "", r.PaymentDate)
	b.seg("TRN", "1", r.TraceNumber)
	if r.Currency != "" {
		b.seg("CUR", "PR", r.Currency)
	}
	b.seg("N1", "PR", r.Payer.Name, "92", r.Payer.GLN)
	b.seg("N1", "PE", r.Payee.Name, "92", r.Payee.GLN)
	b.seg("ENT", "1")
	for _, it := range r.Items {
		b.seg("RMR", "IV", it.InvoiceNumber, "", it.Paid.StringFixed(2), it.Invoiced.StringFixed(2), it.Discount.StringFixed(2))
		if it.InvoiceDate != "" {
			b.seg("DTM", "003", it.InvoiceDate)
		}
	}
	return Transaction{SetID: "820", Body: b.body}, nil
}

// Map820 reads an inbound 820 into the canonical remittance. Like Map850 it
// reports every problem as an Issue and never returns a partial document
// silently; an amount that differs from the RMR lines is EDI-0103-style
// arithmetic (CodeArithmetic) and rejects the transaction.
func Map820(t Transaction) (Remittance, []Issue) {
	r := Remittance{Items: []RemittanceItem{}}
	var issues []Issue
	malformed := func(detail string) { issues = append(issues, Issue{Code: CodeMalformed, Detail: detail, Reject: true}) }
	num := func(s, what string) decimal.Decimal {
		if s == "" {
			return decimal.Zero
		}
		v, err := decimal.NewFromString(s)
		if err != nil {
			malformed(what + " " + s + " is not a number")
		}
		return v
	}
	sawBPR := false
	for _, s := range t.Body {
		switch s.ID {
		case "BPR":
			sawBPR = true
			r.Amount = num(element(s, 1), "BPR02")
			r.Method = element(s, 3)
			r.PaymentDate = element(s, 15)
		case "TRN":
			r.TraceNumber = element(s, 1)
		case "CUR":
			r.Currency = element(s, 1)
		case "N1":
			loc := Location{Name: element(s, 1), GLN: element(s, 3)}
			switch element(s, 0) {
			case "PR":
				r.Payer = loc
			case "PE":
				r.Payee = loc
			}
		case "RMR":
			if element(s, 0) != "IV" {
				continue
			}
			r.Items = append(r.Items, RemittanceItem{InvoiceNumber: element(s, 1), Paid: num(element(s, 3), "RMR04"),
				Invoiced: num(element(s, 4), "RMR05"), Discount: num(element(s, 5), "RMR06")})
		case "DTM":
			if element(s, 0) == "003" && len(r.Items) > 0 {
				r.Items[len(r.Items)-1].InvoiceDate = element(s, 1)
			}
		}
	}
	if !sawBPR {
		malformed("BPR segment is required")
	}
	if r.TraceNumber == "" {
		malformed("TRN02 trace number is required")
	}
	if !isDigits(r.PaymentDate, 8) {
		malformed("BPR16 payment date must be CCYYMMDD")
	}
	if len(r.Items) == 0 {
		malformed("at least one RMR*IV remittance line is required")
	}
	sum := decimal.Zero
	for _, it := range r.Items {
		sum = sum.Add(it.Paid)
	}
	if sawBPR && len(r.Items) > 0 && !sum.Equal(r.Amount) {
		issues = append(issues, Issue{Code: CodeArithmetic, Detail: "BPR02 " + r.Amount.String() + " differs from the RMR04 total " + sum.String(), Reject: true})
	}
	return r, issues
}
