package domain

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/shopspring/decimal"
)

// Remittance advice formats (FR-30.4, 30-4).
const (
	AdviceCSV  = "CSV"
	AdviceJSON = "JSON"
	AdviceEDI  = "EDI820"
	// CodeAdviceMalformed is a remittance advice file the parser rejects.
	CodeAdviceMalformed = "AR-ADVICE-MALFORMED"
	// MaxAdviceLines bounds the payments one advice file may name.
	MaxAdviceLines = 5000
)

// AdviceError is a malformed remittance advice; Detail names the row, element or segment.
type AdviceError struct{ Detail string }

func (e *AdviceError) Error() string { return e.Detail }

func adviceErr(format string, a ...any) error { return &AdviceError{Detail: fmt.Sprintf(format, a...)} }

// AdviceLine is the remittance advice of one payment: the payer's reference for the payment (the bank's reference, the
// payer's check or trace number) and the invoices it pays.
type AdviceLine struct {
	PaymentRef string           `json:"paymentRef"`
	References []RemittanceLine `json:"references"`
}

// ValidAdviceFormat reports whether f is a supported advice format.
func ValidAdviceFormat(f string) bool { return f == AdviceCSV || f == AdviceJSON || f == AdviceEDI }

// ParseAdvice parses a remittance advice file into one line per payment reference, in file order. CSV has a header
// row naming the columns paymentRef, reference and amount (one row per invoice paid); JSON is {"lines":[{"paymentRef",
// "references":[{"reference","amount"}]}]}; EDI820 is an ASC X12 820 payment order/remittance advice, where each TRN
// segment starts a payment (TRN02 is its reference) and each RMR segment names an invoice (RMR02) and the amount paid on it
// (RMR04).
func ParseAdvice(format, content string) ([]AdviceLine, error) {
	var lines []AdviceLine
	var err error
	switch format {
	case AdviceCSV:
		lines, err = parseAdviceCSV(content)
	case AdviceJSON:
		lines, err = parseAdviceJSON(content)
	case AdviceEDI:
		lines, err = parseAdviceEDI(content)
	default:
		return nil, adviceErr("format must be CSV, JSON or EDI820")
	}
	if err != nil {
		return nil, err
	}
	if len(lines) == 0 {
		return nil, adviceErr("the advice names no payment")
	}
	if len(lines) > MaxAdviceLines {
		return nil, adviceErr("an advice file names at most %d payments", MaxAdviceLines)
	}
	return lines, nil
}

func adviceAmount(text string, where string) (decimal.Decimal, error) {
	amount, err := decimal.NewFromString(strings.TrimSpace(text))
	if err != nil || !amount.IsPositive() || amount.Exponent() < -6 {
		return decimal.Zero, adviceErr("%s: the amount %q must be a positive decimal", where, text)
	}
	return amount, nil
}

// addAdvice appends a reference to the line of ref, keeping first-seen order of payments.
func addAdvice(lines []AdviceLine, index map[string]int, ref string, line RemittanceLine) []AdviceLine {
	i, ok := index[ref]
	if !ok {
		index[ref] = len(lines)
		lines = append(lines, AdviceLine{PaymentRef: ref, References: []RemittanceLine{}})
		i = len(lines) - 1
	}
	lines[i].References = append(lines[i].References, line)
	return lines
}

func parseAdviceCSV(content string) ([]AdviceLine, error) {
	r := csv.NewReader(strings.NewReader(content))
	r.TrimLeadingSpace = true
	rows, err := r.ReadAll()
	if err != nil {
		return nil, adviceErr("csv: %v", err)
	}
	if len(rows) < 2 {
		return nil, adviceErr("csv needs a header row and at least one row")
	}
	col := map[string]int{}
	for i, h := range rows[0] {
		col[strings.ToLower(strings.TrimSpace(strings.TrimPrefix(h, string(rune(0xFEFF)))))] = i
	}
	for _, need := range []string{"paymentref", "reference", "amount"} {
		if _, ok := col[need]; !ok {
			return nil, adviceErr("csv header must name paymentRef, reference and amount")
		}
	}
	var lines []AdviceLine
	index := map[string]int{}
	for n, row := range rows[1:] {
		where := fmt.Sprintf("row %d", n+2)
		if len(row) < len(rows[0]) {
			return nil, adviceErr("%s has %d columns, the header %d", where, len(row), len(rows[0]))
		}
		ref, invoice := strings.TrimSpace(row[col["paymentref"]]), strings.TrimSpace(row[col["reference"]])
		if ref == "" || invoice == "" {
			return nil, adviceErr("%s needs a paymentRef and a reference", where)
		}
		amount, err := adviceAmount(row[col["amount"]], where)
		if err != nil {
			return nil, err
		}
		lines = addAdvice(lines, index, ref, RemittanceLine{Reference: invoice, Amount: amount})
	}
	return lines, nil
}

func parseAdviceJSON(content string) ([]AdviceLine, error) {
	var doc struct {
		Lines []AdviceLine `json:"lines"`
	}
	if err := json.Unmarshal([]byte(content), &doc); err != nil {
		return nil, adviceErr("json: %v", err)
	}
	var lines []AdviceLine
	index := map[string]int{}
	for n, l := range doc.Lines {
		where := fmt.Sprintf("lines[%d]", n)
		ref := strings.TrimSpace(l.PaymentRef)
		if ref == "" || len(l.References) == 0 {
			return nil, adviceErr("%s needs a paymentRef and at least one reference", where)
		}
		for m, rl := range l.References {
			inv := strings.TrimSpace(rl.Reference)
			if inv == "" || !rl.Amount.IsPositive() {
				return nil, adviceErr("%s.references[%d] needs a reference and an amount greater than 0", where, m)
			}
			lines = addAdvice(lines, index, ref, RemittanceLine{Reference: inv, Amount: rl.Amount})
		}
	}
	return lines, nil
}

func parseAdviceEDI(content string) ([]AdviceLine, error) {
	text := strings.TrimSpace(content)
	if !strings.HasPrefix(text, "ISA") && !strings.Contains(text, "ST*820") {
		return nil, adviceErr("edi: not an X12 820 interchange")
	}
	// The element separator is the character after ISA, the segment terminator the one after the 16th ISA element.
	sep, term := "*", "~"
	if len(text) > 3 && strings.HasPrefix(text, "ISA") {
		sep = string(text[3])
		parts := strings.SplitN(text, sep, 17)
		if len(parts) == 17 && len(parts[16]) >= 2 {
			term = string(parts[16][1])
		}
	}
	var lines []AdviceLine
	index := map[string]int{}
	current := ""
	for _, raw := range strings.Split(text, term) {
		seg := strings.Split(strings.TrimSpace(raw), sep)
		switch seg[0] {
		case "TRN":
			if len(seg) < 3 || strings.TrimSpace(seg[2]) == "" {
				return nil, adviceErr("edi: a TRN segment needs a reference in TRN02")
			}
			current = strings.TrimSpace(seg[2])
		case "RMR":
			if current == "" {
				return nil, adviceErr("edi: an RMR segment comes before any TRN")
			}
			if len(seg) < 5 || strings.TrimSpace(seg[2]) == "" {
				return nil, adviceErr("edi: an RMR segment needs the invoice in RMR02 and the amount paid in RMR04")
			}
			amount, err := adviceAmount(seg[4], "edi: RMR04 of "+strings.TrimSpace(seg[2]))
			if err != nil {
				return nil, err
			}
			lines = addAdvice(lines, index, current, RemittanceLine{Reference: strings.TrimSpace(seg[2]), Amount: amount})
		}
	}
	return lines, nil
}
