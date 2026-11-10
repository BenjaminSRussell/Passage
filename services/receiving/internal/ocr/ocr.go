// Package ocr holds the invoice OCR providers behind domain.DocumentExtractor.
//
// Only the deterministic local provider exists: it reads the text layer of a
// PDF (no image recognition, no network, no API key). A scanned image has no
// text layer, so it fails extraction and the invoice is keyed by hand, the
// documented fallback while no commercial provider is chosen (architecture Q10).
package ocr

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
)

const (
	ProviderNone  = ""
	ProviderLocal = "local"

	maxTextBytes = 4 << 20
	maxLineBytes = 1024
	maxSegments  = 2000
)

// LocalConfidence is the fixed confidence of a text-layer read: above the
// review threshold, but below 1 because the fields come from labels, not from
// a structured document like UBL.
var LocalConfidence = decimal.RequireFromString("0.85")

// New returns the extractor for OCR_PROVIDER. The empty name means no provider
// (an OCR capture answers 503); an unknown name is a configuration error.
func New(name string) (domain.DocumentExtractor, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case ProviderNone:
		return nil, nil
	case ProviderLocal:
		return Local{}, nil
	default:
		return nil, fmt.Errorf("OCR_PROVIDER %q is unknown (use %q or leave it empty)", name, ProviderLocal)
	}
}

// Local reads labelled fields from the text layer of a PDF. One text-show
// operator (Tj, ', " or TJ) is one line; the lines it understands are
//
//	Invoice Number: INV-1
//	Invoice Date: 2026-11-05
//	Currency: EUR
//	Vendor Tax ID: DE123456789
//	Total: 150.00
//	Line: description | qty | unit | unit price | amount
//
// Labels are case-insensitive; other text is ignored. The same bytes always
// give the same result.
type Local struct{}

func (Local) Extract(_ context.Context, content []byte, mediaType string) (domain.ExtractedInvoice, error) {
	if mediaType != "application/pdf" {
		return domain.ExtractedInvoice{}, fmt.Errorf("%w: the local provider reads only the text layer of a PDF, not %s", domain.ErrExtraction, mediaType)
	}
	if !bytes.HasPrefix(bytes.TrimLeft(content, " \t\r\n"), []byte("%PDF-")) {
		return domain.ExtractedInvoice{}, fmt.Errorf("%w: not a PDF", domain.ErrExtraction)
	}
	segments, err := pdfText(content)
	if err != nil {
		return domain.ExtractedInvoice{}, err
	}
	return parseFields(segments)
}

func parseFields(segments []string) (domain.ExtractedInvoice, error) {
	out := domain.ExtractedInvoice{Confidence: LocalConfidence}
	var total string
	seen := map[string]bool{}
	for _, s := range segments {
		label, value, ok := strings.Cut(s, ":")
		if !ok {
			continue
		}
		label = strings.ToLower(strings.Join(strings.Fields(label), " "))
		value = strings.TrimSpace(value)
		if label != "line" {
			if seen[label] {
				return domain.ExtractedInvoice{}, fmt.Errorf("%w: %q appears twice", domain.ErrExtraction, label)
			}
			seen[label] = true
		}
		switch label {
		case "invoice number":
			out.InvoiceNumber = value
		case "invoice date":
			d, err := time.Parse("2006-01-02", value)
			if err != nil {
				return domain.ExtractedInvoice{}, fmt.Errorf("%w: invoice date must be YYYY-MM-DD", domain.ErrExtraction)
			}
			out.InvoiceDate = d
		case "currency":
			out.Currency = strings.ToUpper(value)
		case "vendor tax id":
			out.VendorTaxID = value
		case "total":
			total = value
		case "line":
			if len(out.Lines) >= domain.MaxCaptureLines {
				return domain.ExtractedInvoice{}, fmt.Errorf("%w: more than %d lines", domain.ErrExtraction, domain.MaxCaptureLines)
			}
			l, err := parseLine(value)
			if err != nil {
				return domain.ExtractedInvoice{}, fmt.Errorf("%w: line %d: %v", domain.ErrExtraction, len(out.Lines)+1, err)
			}
			out.Lines = append(out.Lines, l)
		}
	}
	switch {
	case out.InvoiceNumber == "":
		return domain.ExtractedInvoice{}, fmt.Errorf("%w: no invoice number", domain.ErrExtraction)
	case out.InvoiceDate.IsZero():
		return domain.ExtractedInvoice{}, fmt.Errorf("%w: no invoice date", domain.ErrExtraction)
	case total == "":
		return domain.ExtractedInvoice{}, fmt.Errorf("%w: no total", domain.ErrExtraction)
	case len(out.Lines) == 0:
		return domain.ExtractedInvoice{}, fmt.Errorf("%w: no lines", domain.ErrExtraction)
	}
	t, err := amount(total)
	if err != nil {
		return domain.ExtractedInvoice{}, fmt.Errorf("%w: total: %v", domain.ErrExtraction, err)
	}
	out.Total = t
	return out, nil
}

func parseLine(value string) (domain.CaptureLine, error) {
	parts := strings.Split(value, "|")
	if len(parts) != 5 {
		return domain.CaptureLine{}, fmt.Errorf("want description | qty | unit | unit price | amount")
	}
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	nums := make([]decimal.Decimal, 3)
	for i, p := range []string{parts[1], parts[3], parts[4]} {
		d, err := amount(p)
		if err != nil {
			return domain.CaptureLine{}, err
		}
		nums[i] = d
	}
	return domain.CaptureLine{Description: parts[0], Qty: nums[0], Unit: parts[2], UnitPrice: nums[1], Amount: nums[2]}, nil
}

// amount reads a plain decimal; thousands separators are dropped, a decimal
// comma is not guessed at (bounds are checked by ExtractedInvoice.Validate).
func amount(s string) (decimal.Decimal, error) {
	s = strings.ReplaceAll(s, ",", "")
	if s == "" || len(s) > 40 {
		return decimal.Zero, fmt.Errorf("unreadable number")
	}
	d, err := decimal.NewFromString(s)
	if err != nil {
		return decimal.Zero, fmt.Errorf("unreadable number %q", s)
	}
	return d, nil
}

// pdfText collects the literal strings of text-show operators from every
// content stream, inflating FlateDecode streams. Hex strings, fonts with
// custom encodings and object streams are not decoded.
func pdfText(content []byte) ([]string, error) {
	var segments []string
	budget := maxTextBytes
	rest := content
	for {
		i := bytes.Index(rest, []byte("stream"))
		if i < 0 {
			break
		}
		// "endstream" also contains "stream": skip it.
		if i >= 3 && bytes.Equal(rest[i-3:i], []byte("end")) {
			rest = rest[i+len("stream"):]
			continue
		}
		body := rest[i+len("stream"):]
		body = bytes.TrimPrefix(body, []byte("\r"))
		body = bytes.TrimPrefix(body, []byte("\n"))
		j := bytes.Index(body, []byte("endstream"))
		if j < 0 {
			break
		}
		data := body[:j]
		rest = body[j+len("endstream"):]
		if inflated, err := inflate(data, budget); err == nil {
			data = inflated
		}
		if len(data) > budget {
			return nil, fmt.Errorf("%w: the text layer is too large", domain.ErrExtraction)
		}
		budget -= len(data)
		segments = append(segments, showStrings(data)...)
		if len(segments) > maxSegments {
			return nil, fmt.Errorf("%w: the text layer has too many segments", domain.ErrExtraction)
		}
	}
	if len(segments) == 0 {
		return nil, fmt.Errorf("%w: the PDF has no text layer", domain.ErrExtraction)
	}
	return segments, nil
}

func inflate(data []byte, limit int) ([]byte, error) {
	r, err := zlib.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()
	out, err := io.ReadAll(io.LimitReader(r, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	return out, nil
}

// showStrings scans a content stream: literal strings are collected, and a
// Tj, ', " or TJ operator emits the strings gathered since the last operator
// as one segment.
func showStrings(data []byte) []string {
	var out []string
	var pending strings.Builder
	r := bufio.NewReader(bytes.NewReader(data))
	var word []byte
	flush := func() {
		switch string(word) {
		case "Tj", "'", "\"", "TJ":
			if s := strings.TrimSpace(pending.String()); s != "" && len(s) <= maxLineBytes {
				out = append(out, s)
			}
			pending.Reset()
		case "", "[", "]":
		default:
			if !isOperand(word) {
				pending.Reset()
			}
		}
		word = word[:0]
	}
	for {
		c, err := r.ReadByte()
		if err != nil {
			flush()
			return out
		}
		switch c {
		case '(':
			flush()
			if lit := literal(r); pending.Len()+len(lit) <= maxLineBytes+1 {
				pending.WriteString(lit)
			}
		case '[', ']', ' ', '\t', '\r', '\n':
			flush()
		default:
			word = append(word, c)
		}
	}
}

// isOperand reports whether a bare token is a number or name (kerning in a
// TJ array, or operands of Td and the like), which keeps pending text.
func isOperand(w []byte) bool {
	if len(w) > 0 && w[0] == '/' {
		return true
	}
	for _, c := range w {
		if (c < '0' || c > '9') && c != '.' && c != '-' && c != '+' {
			return false
		}
	}
	return true
}

// literal reads a PDF literal string after its opening parenthesis.
func literal(r *bufio.Reader) string {
	var b strings.Builder
	depth := 1
	for {
		c, err := r.ReadByte()
		if err != nil {
			return b.String()
		}
		switch c {
		case '\\':
			e, err := r.ReadByte()
			if err != nil {
				return b.String()
			}
			switch e {
			case 'n', 'r':
				b.WriteByte(' ')
			case 't':
				b.WriteByte('\t')
			case '\n', '\r':
			case '0', '1', '2', '3', '4', '5', '6', '7':
				v := int(e - '0')
				for k := 0; k < 2; k++ {
					n, err := r.ReadByte()
					if err != nil || n < '0' || n > '7' {
						if err == nil {
							_ = r.UnreadByte()
						}
						break
					}
					v = v*8 + int(n-'0')
				}
				b.WriteByte(byte(v))
			default:
				b.WriteByte(e)
			}
		case '(':
			depth++
			b.WriteByte(c)
		case ')':
			depth--
			if depth == 0 {
				return b.String()
			}
			b.WriteByte(c)
		default:
			if b.Len() < maxLineBytes+1 {
				b.WriteByte(c)
			}
		}
	}
}
