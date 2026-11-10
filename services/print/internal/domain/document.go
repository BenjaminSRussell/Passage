package domain

import (
	"bytes"
	"fmt"
	"strings"
)

// Business documents (issue #530): a purchase order, customer invoice or packing slip
// rendered to a PDF. The owning service sends the document's data; this file lays it out.
// The output is deterministic: no dates, no random ids, the base-14 Helvetica fonts (not
// embedded) and one fixed page layout, so the same request always yields the same bytes
// and a golden file can pin the layout.

const (
	DocPurchaseOrder   = "PURCHASE_ORDER"
	DocCustomerInvoice = "CUSTOMER_INVOICE"
	DocPackingSlip     = "PACKING_SLIP"

	CodeDocument = "PRN-0008" // a business document cannot be rendered (service-local)

	maxDocLines  = 500
	maxDocCols   = 8
	maxDocField  = 200
	pageWidth    = 595.0 // A4 in points
	pageHeight   = 842.0
	pageMargin   = 50.0
	linesPerPage = 38
)

var docTitles = map[string]string{
	DocPurchaseOrder:   "Purchase order",
	DocCustomerInvoice: "Invoice",
	DocPackingSlip:     "Packing slip",
}

// DocParty is a named party with address lines: the legal entity issuing the document or
// the counterparty (vendor, bill-to or ship-to).
type DocParty struct {
	Label   string   `json:"label,omitempty"`
	Name    string   `json:"name"`
	Address []string `json:"address,omitempty"`
}

// DocField is one labelled value in the document header or totals.
type DocField struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// DocColumn is a table column. Align is "L" (default) or "R".
type DocColumn struct {
	Header string `json:"header"`
	Align  string `json:"align,omitempty"`
}

// DocumentRequest is everything a document needs. Text is limited to Latin-1.
type DocumentRequest struct {
	DocumentType string      `json:"documentType"`
	Number       string      `json:"number"`
	LegalEntity  DocParty    `json:"legalEntity"`
	Counterparty *DocParty   `json:"counterparty,omitempty"`
	Fields       []DocField  `json:"fields,omitempty"`
	Columns      []DocColumn `json:"columns"`
	Lines        [][]string  `json:"lines"`
	Totals       []DocField  `json:"totals,omitempty"`
	Notes        string      `json:"notes,omitempty"`
}

func docErr(field, detail string) *Error {
	return &Error{Code: CodeDocument, Detail: field + " " + detail}
}

func checkText(field, s string, required bool) error {
	if required && strings.TrimSpace(s) == "" {
		return docErr(field, "is required")
	}
	if len([]rune(s)) > maxDocField {
		return docErr(field, fmt.Sprintf("is longer than %d characters", maxDocField))
	}
	for _, r := range s {
		if !(r >= 0x20 && r <= 0x7E) && !(r >= 0xA0 && r <= 0xFF) {
			return docErr(field, fmt.Sprintf("contains %q, which the document font cannot print", r))
		}
	}
	return nil
}

// ValidateDocument says why a document cannot be rendered, naming the field.
func ValidateDocument(d DocumentRequest) error {
	if _, ok := docTitles[d.DocumentType]; !ok {
		return docErr("documentType", "must be PURCHASE_ORDER, CUSTOMER_INVOICE or PACKING_SLIP")
	}
	if err := checkText("number", d.Number, true); err != nil {
		return err
	}
	check := func(prefix string, p DocParty) error {
		if err := checkText(prefix+".name", p.Name, true); err != nil {
			return err
		}
		if err := checkText(prefix+".label", p.Label, false); err != nil {
			return err
		}
		for i, a := range p.Address {
			if err := checkText(fmt.Sprintf("%s.address[%d]", prefix, i), a, false); err != nil {
				return err
			}
		}
		return nil
	}
	if err := check("legalEntity", d.LegalEntity); err != nil {
		return err
	}
	if d.Counterparty != nil {
		if err := check("counterparty", *d.Counterparty); err != nil {
			return err
		}
	}
	for _, group := range []struct {
		name string
		f    []DocField
	}{{"fields", d.Fields}, {"totals", d.Totals}} {
		for i, f := range group.f {
			if err := checkText(fmt.Sprintf("%s[%d].label", group.name, i), f.Label, true); err != nil {
				return err
			}
			if err := checkText(fmt.Sprintf("%s[%d].value", group.name, i), f.Value, false); err != nil {
				return err
			}
		}
	}
	if len(d.Columns) == 0 || len(d.Columns) > maxDocCols {
		return docErr("columns", fmt.Sprintf("needs between 1 and %d entries", maxDocCols))
	}
	for i, c := range d.Columns {
		if err := checkText(fmt.Sprintf("columns[%d].header", i), c.Header, true); err != nil {
			return err
		}
		if c.Align != "" && c.Align != "L" && c.Align != "R" {
			return docErr(fmt.Sprintf("columns[%d].align", i), "must be L or R")
		}
	}
	if len(d.Lines) > maxDocLines {
		return docErr("lines", fmt.Sprintf("has more than %d lines", maxDocLines))
	}
	for i, l := range d.Lines {
		if len(l) != len(d.Columns) {
			return docErr(fmt.Sprintf("lines[%d]", i), fmt.Sprintf("has %d cells but there are %d columns", len(l), len(d.Columns)))
		}
		for j, cell := range l {
			if err := checkText(fmt.Sprintf("lines[%d][%d]", i, j), cell, false); err != nil {
				return err
			}
		}
	}
	return checkText("notes", d.Notes, false)
}

// textWidth approximates Helvetica advance widths (units of 1/1000 em, coarse classes).
func textWidth(s string, size float64) float64 {
	w := 0.0
	for _, r := range s {
		switch {
		case strings.ContainsRune("ijl.,:;'|! ", r):
			w += 0.28
		case strings.ContainsRune("tfrI()[]-/", r):
			w += 0.34
		case strings.ContainsRune("mwMW", r):
			w += 0.83
		case r >= 'A' && r <= 'Z':
			w += 0.67
		case r >= '0' && r <= '9':
			w += 0.556
		default:
			w += 0.52
		}
	}
	return w * size
}

// fit shortens s with ".." so that it is at most width points wide.
func fit(s string, size, width float64) string {
	if textWidth(s, size) <= width {
		return s
	}
	r := []rune(s)
	for len(r) > 0 && textWidth(string(r)+"..", size) > width {
		r = r[:len(r)-1]
	}
	return string(r) + ".."
}

type pageBuilder struct {
	buf bytes.Buffer
}

func pdfText(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '\\', '(', ')':
			b.WriteByte('\\')
			b.WriteRune(r)
		default:
			b.WriteByte(byte(r)) // Latin-1 only: ValidateDocument rejects anything else
		}
	}
	return b.String()
}

func (p *pageBuilder) text(x, y, size float64, bold bool, s string) {
	font := "F1"
	if bold {
		font = "F2"
	}
	fmt.Fprintf(&p.buf, "BT /%s %.1f Tf %.2f %.2f Td (%s) Tj ET\n", font, size, x, y, pdfText(s))
}

func (p *pageBuilder) rule(y float64) {
	fmt.Fprintf(&p.buf, "0.5 w %.2f %.2f m %.2f %.2f l S\n", pageMargin, y, pageWidth-pageMargin, y)
}

// RenderDocument lays out a document and returns the PDF bytes.
func RenderDocument(d DocumentRequest) ([]byte, error) {
	if err := ValidateDocument(d); err != nil {
		return nil, err
	}
	contentW := pageWidth - 2*pageMargin
	colW := make([]float64, len(d.Columns))
	if len(colW) == 1 {
		colW[0] = contentW
	} else {
		first := contentW * 0.4
		for i := range colW {
			colW[i] = (contentW - first) / float64(len(colW)-1)
		}
		colW[0] = first
	}
	colX := func(i int) float64 {
		x := pageMargin
		for j := 0; j < i; j++ {
			x += colW[j]
		}
		return x
	}
	cell := func(p *pageBuilder, i int, y float64, s string, bold bool) {
		s = fit(s, 9, colW[i]-8)
		x := colX(i) + 2
		if d.Columns[i].Align == "R" {
			x = colX(i) + colW[i] - 6 - textWidth(s, 9)
		}
		p.text(x, y, 9, bold, s)
	}

	var pages []*pageBuilder
	first := &pageBuilder{}
	y := pageHeight - pageMargin
	// Header: legal entity at the left, title and number at the right.
	first.text(pageMargin, y-8, 14, true, d.LegalEntity.Name)
	ey := y - 24
	for _, a := range d.LegalEntity.Address {
		first.text(pageMargin, ey, 9, false, a)
		ey -= 12
	}
	title := docTitles[d.DocumentType]
	first.text(pageWidth-pageMargin-textWidth(title, 16)-6, y-8, 16, true, title)
	first.text(pageWidth-pageMargin-textWidth(d.Number, 11)-6, y-26, 11, false, d.Number)
	y = ey - 10
	if d.Counterparty != nil {
		label := d.Counterparty.Label
		if label == "" {
			label = "To"
		}
		first.text(pageMargin, y, 8, true, strings.ToUpper(label))
		y -= 12
		first.text(pageMargin, y, 10, true, d.Counterparty.Name)
		y -= 12
		for _, a := range d.Counterparty.Address {
			first.text(pageMargin, y, 9, false, a)
			y -= 12
		}
		y -= 4
	}
	for _, f := range d.Fields {
		first.text(pageMargin, y, 9, true, f.Label)
		first.text(pageMargin+120, y, 9, false, f.Value)
		y -= 12
	}
	y -= 10

	cur := first
	tableTop := y
	header := func(p *pageBuilder, y float64) float64 {
		p.rule(y + 11)
		for i, c := range d.Columns {
			cell(p, i, y, c.Header, true)
		}
		p.rule(y - 4)
		return y - 18
	}
	y = header(cur, tableTop)
	n := 0
	for _, line := range d.Lines {
		if y < pageMargin+60 || n == linesPerPage {
			pages = append(pages, cur)
			cur = &pageBuilder{}
			y = header(cur, pageHeight-pageMargin)
			n = 0
		}
		for i, c := range line {
			cell(cur, i, y, c, false)
		}
		y -= 14
		n++
	}
	cur.rule(y + 8)
	y -= 10
	for _, t := range d.Totals {
		if y < pageMargin+20 {
			pages = append(pages, cur)
			cur = &pageBuilder{}
			y = pageHeight - pageMargin
		}
		cur.text(pageWidth-pageMargin-200, y, 10, true, t.Label)
		cur.text(pageWidth-pageMargin-6-textWidth(t.Value, 10), y, 10, false, t.Value)
		y -= 14
	}
	if d.Notes != "" {
		y -= 8
		cur.text(pageMargin, y, 9, false, fit(d.Notes, 9, contentW))
	}
	pages = append(pages, cur)
	for i, p := range pages {
		footer := fmt.Sprintf("%s %s - page %d of %d", title, d.Number, i+1, len(pages))
		p.text(pageMargin, 30, 8, false, footer)
	}
	return assemblePDF(pages), nil
}

// assemblePDF writes the objects in a fixed order: 1 catalog, 2 page tree, 3 and 4 fonts,
// then a page and its content stream per page.
func assemblePDF(pages []*pageBuilder) []byte {
	var out bytes.Buffer
	var offsets []int
	obj := func(body string) {
		offsets = append(offsets, out.Len())
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", len(offsets), body)
	}
	out.WriteString("%PDF-1.4\n")
	obj("<< /Type /Catalog /Pages 2 0 R >>")
	kids := make([]string, len(pages))
	for i := range pages {
		kids[i] = fmt.Sprintf("%d 0 R", 5+2*i)
	}
	obj(fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), len(pages)))
	obj("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>")
	obj("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica-Bold /Encoding /WinAnsiEncoding >>")
	for i, p := range pages {
		obj(fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %.0f %.0f] /Resources << /Font << /F1 3 0 R /F2 4 0 R >> >> /Contents %d 0 R >>", pageWidth, pageHeight, 6+2*i))
		obj(fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", p.buf.Len(), p.buf.String()))
	}
	xref := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", len(offsets)+1)
	for _, o := range offsets {
		fmt.Fprintf(&out, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets)+1, xref)
	return out.Bytes()
}
