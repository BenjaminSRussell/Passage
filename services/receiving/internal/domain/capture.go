package domain

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

const (
	CodeInvalidCapture    = "RCV-CAP-001"
	CodeCaptureExtraction = "RCV-CAP-002"

	CaptureSourceUBL = "UBL"
	CaptureSourceOCR = "OCR"

	CaptureCaptured    = "CAPTURED"
	CaptureNeedsReview = "NEEDS_REVIEW"

	ReviewLowConfidence = "LOW_CONFIDENCE"
	ReviewTotalMismatch = "TOTAL_MISMATCH"
	ReviewNoVendorTaxID = "NO_VENDOR_TAX_ID"

	MaxCaptureLines     = 500
	maxCaptureNumberLen = 64
	maxCaptureTextLen   = 200
	maxCaptureTaxIDLen  = 40
	maxDecimalText      = 40
	maxDecimalExp       = 12
	minDecimalExp       = -18
)

var decimalMagnitude = decimal.New(1, 12)

func decimalInBounds(d decimal.Decimal) bool {
	return d.Exponent() <= maxDecimalExp && d.Exponent() >= minDecimalExp && d.Abs().LessThanOrEqual(decimalMagnitude)
}

func boundedDecimal(text string) (decimal.Decimal, error) {
	if len(text) > maxDecimalText {
		return decimal.Zero, errors.New("number is too long")
	}
	d, err := decimal.NewFromString(text)
	if err != nil {
		return decimal.Zero, err
	}
	if !decimalInBounds(d) {
		return decimal.Zero, errors.New("number is out of range")
	}
	return d, nil
}

var (
	MinCaptureConfidence = decimal.RequireFromString("0.80")
	captureTotalTol      = decimal.RequireFromString("0.01")
	ErrExtraction        = errors.New("document extraction failed")
)

type CaptureLine struct {
	Description string          `json:"description"`
	Unit        string          `json:"unit"`
	Qty         decimal.Decimal `json:"qty"`
	UnitPrice   decimal.Decimal `json:"unitPrice"`
	Amount      decimal.Decimal `json:"amount"`
}

type ExtractedInvoice struct {
	VendorTaxID   string
	InvoiceNumber string
	InvoiceDate   time.Time
	Currency      string
	Total         decimal.Decimal
	Confidence    decimal.Decimal
	Lines         []CaptureLine
}

type DocumentExtractor interface {
	Extract(ctx context.Context, content []byte, mediaType string) (ExtractedInvoice, error)
}

type FakeExtractor struct {
	Result ExtractedInvoice
	Err    error
	Calls  int
}

func (f *FakeExtractor) Extract(_ context.Context, _ []byte, _ string) (ExtractedInvoice, error) {
	f.Calls++
	return f.Result, f.Err
}

func (e ExtractedInvoice) Validate() error {
	invalid := func(format string, a ...any) error {
		return &Error{Code: CodeInvalidCapture, Detail: fmt.Sprintf(format, a...)}
	}
	numbers := []decimal.Decimal{e.Total, e.Confidence}
	for _, l := range e.Lines {
		numbers = append(numbers, l.Qty, l.UnitPrice, l.Amount)
	}
	for _, n := range numbers {
		if !decimalInBounds(n) {
			return invalid("a number is out of range (at most 12 integer digits, exponent -18 to 12)")
		}
	}
	switch {
	case len(e.VendorTaxID) > maxCaptureTaxIDLen:
		return invalid("vendor tax id is at most %d characters", maxCaptureTaxIDLen)
	case e.InvoiceNumber == "" || len(e.InvoiceNumber) > maxCaptureNumberLen:
		return invalid("invoice number is required and at most %d characters", maxCaptureNumberLen)
	case e.InvoiceDate.IsZero():
		return invalid("invoice date is required")
	case len(e.Currency) != 3 || strings.ToUpper(e.Currency) != e.Currency:
		return invalid("currency must be a 3-letter upper-case ISO 4217 code")
	case e.Total.IsNegative():
		return invalid("total must not be negative")
	case len(e.Lines) == 0 || len(e.Lines) > MaxCaptureLines:
		return invalid("the document must hold 1 to %d lines", MaxCaptureLines)
	case e.Confidence.IsNegative() || e.Confidence.GreaterThan(decimal.NewFromInt(1)):
		return invalid("confidence must be between 0 and 1")
	}
	for i, l := range e.Lines {
		if !l.Qty.IsPositive() || l.UnitPrice.IsNegative() || l.Amount.IsNegative() || len(l.Description) > maxCaptureTextLen || len(l.Unit) > 20 {
			return invalid("line %d needs a positive quantity, non-negative prices and short text", i+1)
		}
	}
	return nil
}

func (e ExtractedInvoice) ReviewReasons() []string {
	reasons := []string{}
	if e.Confidence.LessThan(MinCaptureConfidence) {
		reasons = append(reasons, ReviewLowConfidence)
	}
	sum := decimal.Zero
	for _, l := range e.Lines {
		sum = sum.Add(l.Amount)
	}
	if e.Total.Sub(sum).Abs().GreaterThan(captureTotalTol) {
		reasons = append(reasons, ReviewTotalMismatch)
	}
	if e.VendorTaxID == "" {
		reasons = append(reasons, ReviewNoVendorTaxID)
	}
	return reasons
}

type ublInvoice struct {
	XMLName  xml.Name `xml:"Invoice"`
	ID       string   `xml:"ID"`
	Issued   string   `xml:"IssueDate"`
	Currency string   `xml:"DocumentCurrencyCode"`
	TaxID    string   `xml:"AccountingSupplierParty>Party>PartyTaxScheme>CompanyID"`
	Net      string   `xml:"LegalMonetaryTotal>TaxExclusiveAmount"`
	LineSum  string   `xml:"LegalMonetaryTotal>LineExtensionAmount"`
	Payable  string   `xml:"LegalMonetaryTotal>PayableAmount"`
	Lines    []struct {
		Description string `xml:"Item>Name"`
		Quantity    struct {
			Value string `xml:",chardata"`
			Unit  string `xml:"unitCode,attr"`
		} `xml:"InvoicedQuantity"`
		Amount string `xml:"LineExtensionAmount"`
		Price  string `xml:"Price>PriceAmount"`
	} `xml:"InvoiceLine"`
}

func ParseUBL(content []byte) (ExtractedInvoice, error) {
	if bytes.Contains(bytes.ToUpper(content), []byte("<!DOCTYPE")) {
		return ExtractedInvoice{}, &Error{Code: CodeInvalidCapture, Detail: "the document must not declare a DOCTYPE"}
	}
	var doc ublInvoice
	if err := xml.Unmarshal(content, &doc); err != nil {
		return ExtractedInvoice{}, &Error{Code: CodeInvalidCapture, Detail: "the document is not a UBL Invoice"}
	}
	out := ExtractedInvoice{VendorTaxID: strings.TrimSpace(doc.TaxID), InvoiceNumber: strings.TrimSpace(doc.ID), Currency: strings.TrimSpace(doc.Currency), Confidence: decimal.NewFromInt(1)}
	var err error
	if out.InvoiceDate, err = time.Parse("2006-01-02", strings.TrimSpace(doc.Issued)); err != nil {
		return ExtractedInvoice{}, &Error{Code: CodeInvalidCapture, Detail: "IssueDate must be YYYY-MM-DD"}
	}
	total := firstNonBlank(doc.Net, doc.LineSum, doc.Payable)
	if out.Total, err = boundedDecimal(total); err != nil {
		return ExtractedInvoice{}, &Error{Code: CodeInvalidCapture, Detail: "the document has no usable monetary total"}
	}
	for i, l := range doc.Lines {
		line := CaptureLine{Description: strings.TrimSpace(l.Description), Unit: strings.TrimSpace(l.Quantity.Unit)}
		amounts := []struct {
			into *decimal.Decimal
			text string
		}{{&line.Qty, l.Quantity.Value}, {&line.Amount, l.Amount}, {&line.UnitPrice, l.Price}}
		for _, a := range amounts {
			if *a.into, err = boundedDecimal(strings.TrimSpace(a.text)); err != nil {
				return ExtractedInvoice{}, &Error{Code: CodeInvalidCapture, Detail: fmt.Sprintf("invoice line %d has an unreadable quantity, amount or price", i+1)}
			}
		}
		out.Lines = append(out.Lines, line)
	}
	return out, nil
}

func firstNonBlank(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
