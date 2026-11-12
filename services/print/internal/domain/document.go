package domain

import (
	"bytes"
	"fmt"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Business documents (issue #530): a purchase order, customer invoice or packing slip
// rendered to a PDF. The owning service sends the document's data; this file lays it out.
// The output is deterministic: no dates, no random ids, embedded font subsets
// (docfont.go) and one fixed A4 layout, so the same request always yields the same bytes
// and a golden file can pin the layout.
//
// Layout: the legal entity's name and address with the title and number at the top; the
// counterparty, any additional parties (ship to, deliver to) and the header fields in a
// row of blocks; then the line table, whose left-aligned columns wrap and whose
// right-aligned (number) columns keep their width; the totals block at the right; the
// notes. A row that does not fit goes to the next page, which repeats a short header and
// the table header. Every page ends with "Page n of m".

const (
	DocPurchaseOrder   = "PURCHASE_ORDER"
	DocCustomerInvoice = "CUSTOMER_INVOICE"
	DocPackingSlip     = "PACKING_SLIP"
	// DocRemittanceAdvice is sent by the payables payment run page (#528): the vendor is the
	// counterparty, the lines are the invoices a payment settled and the totals what was paid.
	DocRemittanceAdvice = "REMITTANCE_ADVICE"
	// Correspondence (#46-4): letters with a body text from a text block. The table is
	// optional, so a payment notice or a balance confirmation can be a letter alone.
	DocPeriodicStatement   = "PERIODIC_STATEMENT"
	DocPaymentNotice       = "PAYMENT_NOTICE"
	DocBalanceConfirmation = "BALANCE_CONFIRMATION"

	CodeDocument         = "PRN-0008" // a business document cannot be rendered (service-local)
	CodeDocumentTooLarge = "PRN-0009" // a business document has more lines than one PDF holds (service-local)

	maxDocLines        = 500
	maxDocCols         = 8
	maxDocField        = 200  // names, labels, values, address lines, the number
	maxDocCell         = 500  // a table cell (descriptions wrap)
	maxDocNotes        = 2000 // the notes (wrap; may hold line breaks)
	maxDocBody         = 4000 // the letter body of a correspondence document
	maxDocTitle        = 60
	maxDocFooter       = 200
	maxDocAddressLines = 8
	maxDocParties      = 2 // additional parties besides the counterparty
	maxDocFieldCount   = 20

	pageWidth    = 595.0 // A4 in points
	pageHeight   = 842.0
	pageMargin   = 50.0
	contentLeft  = pageMargin
	contentRight = pageWidth - pageMargin
	contentWidth = contentRight - contentLeft
	pageTop      = pageHeight - pageMargin
	contentFloor = 62.0 // nothing but the footer goes below this
	cellPad      = 4.0
	numberGap    = 10.0 // extra room left of a right-aligned column, so numbers stand apart
)

var docTitles = map[string]string{
	DocPurchaseOrder:    "Purchase order",
	DocCustomerInvoice:  "Invoice",
	DocPackingSlip:      "Packing slip",
	DocRemittanceAdvice: "Remittance advice",

	DocPeriodicStatement:   "Statement of account",
	DocPaymentNotice:       "Payment notice",
	DocBalanceConfirmation: "Balance confirmation",
}

// IsCorrespondence reports whether t is a letter type: its table is optional and it has a body.
func IsCorrespondence(t string) bool {
	return t == DocPeriodicStatement || t == DocPaymentNotice || t == DocBalanceConfirmation
}

// DocumentTypes lists every type the renderer knows, in a fixed order.
func DocumentTypes() []string {
	return []string{DocPurchaseOrder, DocCustomerInvoice, DocPackingSlip, DocRemittanceAdvice,
		DocPeriodicStatement, DocPaymentNotice, DocBalanceConfirmation}
}

// DocParty is a named party with address lines: the legal entity issuing the document,
// the counterparty (vendor, bill-to or ship-to) or an additional party.
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

// DocColumn is a table column. Align is "L" (default) or "R" (numbers).
type DocColumn struct {
	Header string `json:"header"`
	Align  string `json:"align,omitempty"`
}

// DocumentRequest is everything a document needs. Text is Unicode; see docfont.go for the
// characters the fonts can draw.
type DocumentRequest struct {
	DocumentType      string      `json:"documentType"`
	Number            string      `json:"number"`
	LegalEntity       DocParty    `json:"legalEntity"`
	Counterparty      *DocParty   `json:"counterparty,omitempty"`
	AdditionalParties []DocParty  `json:"additionalParties,omitempty"`
	Fields            []DocField  `json:"fields,omitempty"`
	Columns           []DocColumn `json:"columns"`
	Lines             [][]string  `json:"lines"`
	Totals            []DocField  `json:"totals,omitempty"`
	Notes             string      `json:"notes,omitempty"`
	// Title replaces the standard title (a template sets it), Body is the letter text of
	// a correspondence document and FooterText a line at the foot of every page.
	Title      string `json:"title,omitempty"`
	Body       string `json:"body,omitempty"`
	FooterText string `json:"footerText,omitempty"`
}

// RenderedDocument is a rendered PDF with what the caller may want to tell the user.
type RenderedDocument struct {
	PDF   []byte
	Pages int
	// MissingGlyphs counts the characters the fonts cannot draw; each shows as U+FFFD.
	MissingGlyphs int
}

func docErr(field, detail string) *Error {
	return &Error{Code: CodeDocument, Detail: field + " " + detail}
}

func checkText(field, s string, required bool, limit int, multiline bool) error {
	if required && strings.TrimSpace(s) == "" {
		return docErr(field, "is required")
	}
	if n := len([]rune(s)); n > limit {
		return docErr(field, fmt.Sprintf("is longer than %d characters", limit))
	}
	for _, r := range s {
		if r == '\n' && multiline {
			continue
		}
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return docErr(field, fmt.Sprintf("contains the control character %U", r))
		}
	}
	return nil
}

func checkParty(prefix string, p DocParty) error {
	if err := checkText(prefix+".name", p.Name, true, maxDocField, false); err != nil {
		return err
	}
	if err := checkText(prefix+".label", p.Label, false, maxDocField, false); err != nil {
		return err
	}
	if len(p.Address) > maxDocAddressLines {
		return docErr(prefix+".address", fmt.Sprintf("has more than %d lines", maxDocAddressLines))
	}
	for i, a := range p.Address {
		if err := checkText(fmt.Sprintf("%s.address[%d]", prefix, i), a, false, maxDocField, false); err != nil {
			return err
		}
	}
	return nil
}

// ValidateDocument says why a document cannot be rendered, naming the field.
func ValidateDocument(d DocumentRequest) error {
	d = normalizeDocument(d)
	if _, ok := docTitles[d.DocumentType]; !ok {
		return docErr("documentType", "must be PURCHASE_ORDER, CUSTOMER_INVOICE, PACKING_SLIP, REMITTANCE_ADVICE, PERIODIC_STATEMENT, PAYMENT_NOTICE or BALANCE_CONFIRMATION")
	}
	if err := checkText("number", d.Number, true, maxDocField, false); err != nil {
		return err
	}
	if err := checkParty("legalEntity", d.LegalEntity); err != nil {
		return err
	}
	if d.Counterparty != nil {
		if err := checkParty("counterparty", *d.Counterparty); err != nil {
			return err
		}
	}
	if len(d.AdditionalParties) > maxDocParties {
		return docErr("additionalParties", fmt.Sprintf("has more than %d entries", maxDocParties))
	}
	for i, p := range d.AdditionalParties {
		if err := checkParty(fmt.Sprintf("additionalParties[%d]", i), p); err != nil {
			return err
		}
	}
	for _, group := range []struct {
		name string
		f    []DocField
	}{{"fields", d.Fields}, {"totals", d.Totals}} {
		if len(group.f) > maxDocFieldCount {
			return docErr(group.name, fmt.Sprintf("has more than %d entries", maxDocFieldCount))
		}
		for i, f := range group.f {
			if err := checkText(fmt.Sprintf("%s[%d].label", group.name, i), f.Label, true, maxDocField, false); err != nil {
				return err
			}
			if err := checkText(fmt.Sprintf("%s[%d].value", group.name, i), f.Value, false, maxDocField, false); err != nil {
				return err
			}
		}
	}
	if IsCorrespondence(d.DocumentType) {
		if err := checkText("body", d.Body, true, maxDocBody, true); err != nil {
			return err
		}
	} else if strings.TrimSpace(d.Body) != "" {
		return docErr("body", "is only used by correspondence documents")
	}
	if err := checkText("title", d.Title, false, maxDocTitle, false); err != nil {
		return err
	}
	if err := checkText("footerText", d.FooterText, false, maxDocFooter, false); err != nil {
		return err
	}
	if len(d.Columns) > maxDocCols || (len(d.Columns) == 0 && !IsCorrespondence(d.DocumentType)) {
		return docErr("columns", fmt.Sprintf("needs between 1 and %d entries", maxDocCols))
	}
	for i, c := range d.Columns {
		if err := checkText(fmt.Sprintf("columns[%d].header", i), c.Header, true, maxDocField, false); err != nil {
			return err
		}
		if c.Align != "" && c.Align != "L" && c.Align != "R" {
			return docErr(fmt.Sprintf("columns[%d].align", i), "must be L or R")
		}
	}
	if len(d.Lines) > maxDocLines {
		return &Error{Code: CodeDocumentTooLarge, Detail: fmt.Sprintf("lines has %d entries; one document holds at most %d", len(d.Lines), maxDocLines)}
	}
	for i, l := range d.Lines {
		if len(l) != len(d.Columns) {
			return docErr(fmt.Sprintf("lines[%d]", i), fmt.Sprintf("has %d cells but there are %d columns", len(l), len(d.Columns)))
		}
		for j, cell := range l {
			if err := checkText(fmt.Sprintf("lines[%d][%d]", i, j), cell, false, maxDocCell, true); err != nil {
				return err
			}
		}
	}
	return checkText("notes", d.Notes, false, maxDocNotes, true)
}

// normalizeDocument puts all text in Unicode NFC, so a letter sent as a base letter and a
// combining accent prints as the precomposed glyph. It turns CR LF into LF, a tab and every
// kind of space (no-break, narrow no-break, thin and so on, as number formats use them)
// into a plain space, and drops invisible format characters such as the soft hyphen, the
// zero-width space and the byte order mark, none of which the fonts have a glyph for.
func normalizeDocument(d DocumentRequest) DocumentRequest {
	n := func(s string) string {
		s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
		s = strings.Map(func(r rune) rune {
			switch {
			case r == '\t' || unicode.Is(unicode.Zs, r):
				return ' '
			case unicode.Is(unicode.Cf, r):
				return -1
			}
			return r
		}, s)
		return norm.NFC.String(s)
	}
	party := func(p DocParty) DocParty {
		out := DocParty{Label: n(p.Label), Name: n(p.Name)}
		for _, a := range p.Address {
			out.Address = append(out.Address, n(a))
		}
		return out
	}
	fields := func(fs []DocField) []DocField {
		var out []DocField
		for _, f := range fs {
			out = append(out, DocField{Label: n(f.Label), Value: n(f.Value)})
		}
		return out
	}
	out := DocumentRequest{
		DocumentType: d.DocumentType,
		Number:       n(d.Number),
		LegalEntity:  party(d.LegalEntity),
		Fields:       fields(d.Fields),
		Totals:       fields(d.Totals),
		Notes:        n(d.Notes),
		Title:        n(d.Title),
		Body:         n(d.Body),
		FooterText:   n(d.FooterText),
	}
	if d.Counterparty != nil {
		c := party(*d.Counterparty)
		out.Counterparty = &c
	}
	for _, p := range d.AdditionalParties {
		out.AdditionalParties = append(out.AdditionalParties, party(p))
	}
	for _, c := range d.Columns {
		out.Columns = append(out.Columns, DocColumn{Header: n(c.Header), Align: c.Align})
	}
	for _, l := range d.Lines {
		row := make([]string, len(l))
		for i, c := range l {
			row[i] = n(c)
		}
		out.Lines = append(out.Lines, row)
	}
	return out
}

// textStyle is a face, a size, a line height and a gray level (0 black).
type textStyle struct {
	face  int
	size  float64
	lead  float64
	gray  float64
	upper bool
}

var (
	styleTitle    = textStyle{face: faceBold, size: 18, lead: 21}
	styleNumber   = textStyle{face: faceRegular, size: 11, lead: 13.5}
	styleEntity   = textStyle{face: faceBold, size: 13, lead: 16}
	styleBody     = textStyle{face: faceRegular, size: 9, lead: 11.5}
	styleName     = textStyle{face: faceBold, size: 10, lead: 12.5}
	styleLabel    = textStyle{face: faceBold, size: 7, lead: 10, gray: 0.4, upper: true}
	styleField    = textStyle{face: faceBold, size: 8, lead: 11.5, gray: 0.35}
	styleHead     = textStyle{face: faceBold, size: 8, lead: 10, gray: 0.2}
	styleBodyText = textStyle{face: faceRegular, size: 10, lead: 14}
	styleCell     = textStyle{face: faceRegular, size: 9, lead: 11}
	styleTotal    = textStyle{face: faceRegular, size: 9.5, lead: 15}
	styleGrand    = textStyle{face: faceBold, size: 10.5, lead: 16}
	styleRunning  = textStyle{face: faceBold, size: 9, lead: 11}
	styleFooter   = textStyle{face: faceRegular, size: 7.5, lead: 9, gray: 0.4}
)

const ellipsis = "…"

type docLayout struct {
	d     DocumentRequest
	t     *docText
	title string
	pages []*bytes.Buffer
	cur   *bytes.Buffer
	y     float64
	colW  []float64
}

func (l *docLayout) width(st textStyle, s string) float64 {
	if st.upper {
		s = strings.ToUpper(s)
	}
	return l.t.width(st.face, st.size, s)
}

// text draws s with its baseline at y, starting at x.
func (l *docLayout) text(st textStyle, x, y float64, s string) {
	if st.upper {
		s = strings.ToUpper(s)
	}
	if s == "" {
		return
	}
	run := l.t.encode(st.face, s)
	if run.missing {
		fmt.Fprintf(l.cur, "/Span << /ActualText %s >> BDC\n", utf16Hex(s))
	}
	if st.gray != 0 {
		fmt.Fprintf(l.cur, "%.2f g\n", st.gray)
	}
	fmt.Fprintf(l.cur, "BT /F%d %.1f Tf %.2f %.2f Td %s Tj ET\n", st.face+1, st.size, x, y, hexGlyphs(run.glyphs))
	if st.gray != 0 {
		l.cur.WriteString("0 g\n")
	}
	if run.missing {
		l.cur.WriteString("EMC\n")
	}
}

// textRight draws s so that it ends at x.
func (l *docLayout) textRight(st textStyle, x, y float64, s string) {
	l.text(st, x-l.width(st, s), y, s)
}

func (l *docLayout) rule(x1, x2, y, w, gray float64) {
	fmt.Fprintf(l.cur, "%.2f G %.2f w %.2f %.2f m %.2f %.2f l S 0 G\n", gray, w, x1, y, x2, y)
}

func (l *docLayout) fill(x, y, w, h, gray float64) {
	fmt.Fprintf(l.cur, "%.2f g %.2f %.2f %.2f %.2f re f 0 g\n", gray, x, y, w, h)
}

// wrap breaks s into lines at most width wide: at spaces where it can, inside a word where
// a word is wider than the line, and at every line break in s.
func (l *docLayout) wrap(st textStyle, s string, width float64) []string {
	var out []string
	for _, para := range strings.Split(s, "\n") {
		words := strings.Fields(para)
		if len(words) == 0 {
			out = append(out, "")
			continue
		}
		line := ""
		for _, w := range words {
			candidate := w
			if line != "" {
				candidate = line + " " + w
			}
			if l.width(st, candidate) <= width {
				line = candidate
				continue
			}
			if line != "" {
				out = append(out, line)
				line = ""
			}
			for l.width(st, w) > width {
				r := []rune(w)
				n := 1
				for n < len(r) && l.width(st, string(r[:n+1])) <= width {
					n++
				}
				out = append(out, string(r[:n]))
				w = string(r[n:])
			}
			line = w
		}
		out = append(out, line)
	}
	return out
}

// clip keeps at most n lines, ending the last kept line with an ellipsis.
func (l *docLayout) clip(st textStyle, lines []string, n int, width float64) []string {
	if len(lines) <= n {
		return lines
	}
	lines = lines[:n]
	lines[n-1] = l.fit(st, lines[n-1]+ellipsis, width)
	return lines
}

// fit shortens s with an ellipsis so that it is at most width wide.
func (l *docLayout) fit(st textStyle, s string, width float64) string {
	if l.width(st, s) <= width {
		return s
	}
	r := []rune(strings.TrimSuffix(s, ellipsis))
	for len(r) > 0 && l.width(st, string(r)+ellipsis) > width {
		r = r[:len(r)-1]
	}
	return string(r) + ellipsis
}

func (l *docLayout) newPage() {
	l.cur = &bytes.Buffer{}
	l.pages = append(l.pages, l.cur)
	l.y = pageTop
}

// continuation starts a new page with a short running header.
func (l *docLayout) continuation() {
	l.newPage()
	right := l.title + " " + l.d.Number
	rw := min(l.width(styleRunning, right), contentWidth/2)
	l.text(styleRunning, contentLeft, l.y-8, l.fit(styleRunning, l.d.LegalEntity.Name, contentWidth-rw-20))
	l.textRight(styleRunning, contentRight, l.y-8, l.fit(styleRunning, right, contentWidth/2))
	l.rule(contentLeft, contentRight, l.y-15, 0.5, 0.6)
	l.y -= 32
}

// header draws the first page's letterhead.
func (l *docLayout) header() {
	top := l.y
	leftW := contentWidth * 0.58
	y := top - 12
	for _, s := range l.clip(styleEntity, l.wrap(styleEntity, l.d.LegalEntity.Name, leftW), 3, leftW) {
		l.text(styleEntity, contentLeft, y, s)
		y -= styleEntity.lead
	}
	y += styleEntity.lead - styleBody.lead - 2
	for _, a := range l.d.LegalEntity.Address {
		for _, s := range l.clip(styleBody, l.wrap(styleBody, a, leftW), 2, leftW) {
			l.text(styleBody, contentLeft, y, s)
			y -= styleBody.lead
		}
	}
	leftBottom := y + styleBody.lead
	rightW := contentWidth - leftW - 16
	ry := top - 14
	l.textRight(styleTitle, contentRight, ry, l.fit(styleTitle, l.title, rightW))
	ry -= styleTitle.lead
	for _, s := range l.clip(styleNumber, l.wrap(styleNumber, l.d.Number, rightW), 3, rightW) {
		l.textRight(styleNumber, contentRight, ry, s)
		ry -= styleNumber.lead
	}
	rightBottom := ry + styleNumber.lead
	l.y = min(leftBottom, rightBottom) - 12
	l.rule(contentLeft, contentRight, l.y, 0.75, 0.6)
	l.y -= 20
}

// partyBlock draws a party at x within width w and returns the lowest baseline used.
func (l *docLayout) partyBlock(p DocParty, defaultLabel string, x, y, w float64) float64 {
	label := p.Label
	if label == "" {
		label = defaultLabel
	}
	l.text(styleLabel, x, y, l.fit(styleLabel, label, w))
	y -= styleLabel.lead + 2
	for _, s := range l.clip(styleName, l.wrap(styleName, p.Name, w), 3, w) {
		l.text(styleName, x, y, s)
		y -= styleName.lead
	}
	for _, a := range p.Address {
		for _, s := range l.clip(styleBody, l.wrap(styleBody, a, w), 2, w) {
			l.text(styleBody, x, y, s)
			y -= styleBody.lead
		}
	}
	return y
}

// fieldBlock draws the header fields as label and value pairs and returns the lowest
// baseline used. Fields that do not fit above the footer go on at the same place on a
// continuation page; moved then says that the block ends on a later page than it began.
func (l *docLayout) fieldBlock(x, y, w float64) (bottom float64, moved bool) {
	labelW := 0.0
	for _, f := range l.d.Fields {
		labelW = max(labelW, l.width(styleField, f.Label))
	}
	labelW = min(labelW, w*0.5)
	valueX := x + labelW + 8
	valueW := w - labelW - 8
	for _, f := range l.d.Fields {
		vals := l.clip(styleBody, l.wrap(styleBody, f.Value, valueW), 3, valueW)
		if y-float64(len(vals)-1)*styleBody.lead < contentFloor {
			l.continuation()
			y, moved = l.y, true
		}
		l.text(styleField, x, y, l.fit(styleField, f.Label, labelW))
		for i, s := range vals {
			l.text(styleBody, valueX, y, s)
			if i < len(vals)-1 {
				y -= styleBody.lead
			}
		}
		y -= styleField.lead
	}
	return y, moved
}

func (l *docLayout) parties() {
	type block struct {
		party *DocParty
		def   string
	}
	var blocks []block
	if l.d.Counterparty != nil {
		def := map[string]string{DocPurchaseOrder: "Vendor", DocCustomerInvoice: "Bill to", DocPackingSlip: "Ship to", DocRemittanceAdvice: "Vendor",
			DocPeriodicStatement: "Customer", DocPaymentNotice: "Customer", DocBalanceConfirmation: "Customer"}[l.d.DocumentType]
		blocks = append(blocks, block{l.d.Counterparty, def})
	}
	for i := range l.d.AdditionalParties {
		blocks = append(blocks, block{&l.d.AdditionalParties[i], ""})
	}
	n := len(blocks)
	if len(l.d.Fields) > 0 {
		n++
	}
	if n == 0 {
		return
	}
	const gap = 18.0
	w := min((contentWidth-gap*float64(n-1))/float64(n), 240)
	bottom := l.y
	x := contentLeft
	for _, b := range blocks {
		bottom = min(bottom, l.partyBlock(*b.party, b.def, x, l.y, w))
		x += w + gap
	}
	if len(l.d.Fields) > 0 {
		fx := max(x, contentRight-w)
		fieldBottom, moved := l.fieldBlock(fx, l.y, contentRight-fx)
		if moved { // the parties are on an earlier page
			bottom = fieldBottom
		}
		bottom = min(bottom, fieldBottom)
	}
	l.y = bottom - 10
}

// naturalWidths is the width each column would like: room for its widest cell and for its
// header, either the whole header or, with wrapHeaders, its longest word.
func (l *docLayout) naturalWidths(wrapHeaders bool) []float64 {
	natural := make([]float64, len(l.d.Columns))
	for i, c := range l.d.Columns {
		if wrapHeaders {
			for _, word := range strings.Fields(c.Header) {
				natural[i] = max(natural[i], l.width(styleHead, word))
			}
		} else {
			natural[i] = l.width(styleHead, c.Header)
		}
		for _, line := range l.d.Lines {
			for _, part := range strings.Split(line[i], "\n") {
				natural[i] = max(natural[i], l.width(styleCell, part))
			}
		}
		natural[i] = max(natural[i]+2*cellPad, 24)
		if c.Align == "R" {
			natural[i] += numberGap
		}
	}
	return natural
}

// columnWidths gives right-aligned columns their natural width and shares the rest among
// the left-aligned columns, which wrap. Headers stay on one line unless that leaves the
// left-aligned columns less than two fifths of the table.
func (l *docLayout) columnWidths() []float64 {
	widths := l.shareWidth(l.naturalWidths(false))
	text, hasText := 0.0, false
	for i, c := range l.d.Columns {
		if c.Align != "R" {
			text += widths[i]
			hasText = true
		}
	}
	if hasText && text < 0.4*contentWidth {
		widths = l.shareWidth(l.naturalWidths(true))
	}
	return widths
}

// shareWidth fits the natural column widths into the content width.
func (l *docLayout) shareWidth(natural []float64) []float64 {
	cols := l.d.Columns
	widths := make([]float64, len(cols))
	total := 0.0
	for _, w := range natural {
		total += w
	}
	widest := -1 // the left-aligned column with the most text takes any spare width
	for i, c := range cols {
		if c.Align != "R" && (widest < 0 || natural[i] > natural[widest]) {
			widest = i
		}
	}
	if total <= contentWidth {
		copy(widths, natural)
		if widest < 0 {
			widest = 0
		}
		widths[widest] += contentWidth - total
		return widths
	}
	// Too wide: number columns keep their width (at most a fifth of the line each), the
	// text columns share what is left, shortest first, and wrap.
	rest := contentWidth
	var flexible []int
	for i, c := range cols {
		if c.Align == "R" {
			widths[i] = min(natural[i], contentWidth/5)
			rest -= widths[i]
		} else {
			flexible = append(flexible, i)
		}
	}
	if len(flexible) == 0 || rest < 40*float64(len(flexible)) {
		for i := range widths {
			widths[i] = natural[i] * contentWidth / total
		}
		return widths
	}
	for len(flexible) > 0 {
		share := rest / float64(len(flexible))
		next := flexible[:0]
		fixed := false
		for _, i := range flexible {
			if natural[i] <= share {
				widths[i] = natural[i]
				rest -= natural[i]
				fixed = true
			} else {
				next = append(next, i)
			}
		}
		flexible = next
		if !fixed {
			for _, i := range flexible {
				widths[i] = rest / float64(len(flexible))
			}
			break
		}
	}
	return widths
}

func (l *docLayout) colX(i int) float64 {
	x := contentLeft
	for j := 0; j < i; j++ {
		x += l.colW[j]
	}
	return x
}

// row draws one table row of wrapped cells and moves down by its height.
func (l *docLayout) row(st textStyle, cells [][]string) {
	n := 1
	for _, c := range cells {
		n = max(n, len(c))
	}
	for i, c := range cells {
		for k, s := range c {
			y := l.y - cellPad - st.size*0.9 - float64(k)*st.lead
			if l.d.Columns[i].Align == "R" {
				l.textRight(st, l.colX(i)+l.colW[i]-cellPad, y, s)
			} else {
				l.text(st, l.colX(i)+cellPad, y, s)
			}
		}
	}
	l.y -= rowHeight(st, n)
}

func rowHeight(st textStyle, lines int) float64 {
	return 2*cellPad + st.size*0.9 + float64(lines-1)*st.lead + st.size*0.35
}

func (l *docLayout) wrapRow(st textStyle, texts []string, maxLines int) [][]string {
	cells := make([][]string, len(texts))
	for i, s := range texts {
		w := l.colW[i] - 2*cellPad
		cells[i] = l.clip(st, l.wrap(st, s, w), maxLines, w)
	}
	return cells
}

func (l *docLayout) tableHeader() {
	headers := make([]string, len(l.d.Columns))
	for i, c := range l.d.Columns {
		headers[i] = c.Header
	}
	cells := l.wrapRow(styleHead, headers, 3)
	n := 1
	for _, c := range cells {
		n = max(n, len(c))
	}
	h := rowHeight(styleHead, n)
	l.fill(contentLeft, l.y-h, contentWidth, h, 0.93)
	l.row(styleHead, cells)
	l.rule(contentLeft, contentRight, l.y, 0.6, 0.5)
}

func (l *docLayout) headerHeight() float64 {
	n := 1
	for i, c := range l.d.Columns {
		n = max(n, min(3, len(l.wrap(styleHead, c.Header, l.colW[i]-2*cellPad))))
	}
	return rowHeight(styleHead, n)
}

// body draws the letter text of a correspondence document.
func (l *docLayout) body() {
	if strings.TrimSpace(l.d.Body) == "" {
		return
	}
	for _, s := range l.wrap(styleBodyText, strings.TrimSpace(l.d.Body), contentWidth) {
		if l.y < contentFloor {
			l.continuation()
		}
		l.text(styleBodyText, contentLeft, l.y, s)
		l.y -= styleBodyText.lead
	}
	l.y -= 10
}

func (l *docLayout) table() {
	if len(l.d.Columns) == 0 {
		return
	}
	l.colW = l.columnWidths()
	headerH := l.headerHeight()
	// A row never needs more than a fresh continuation page.
	maxLines := int((pageTop - 32 - headerH - contentFloor - rowHeight(styleCell, 1)) / styleCell.lead)
	if len(l.d.Lines) > 0 {
		first := rowHeight(styleCell, maxCells(l.wrapRow(styleCell, l.d.Lines[0], maxLines)))
		if l.y-headerH-first < contentFloor {
			l.continuation()
		}
	}
	l.tableHeader()
	for _, line := range l.d.Lines {
		cells := l.wrapRow(styleCell, line, maxLines)
		h := rowHeight(styleCell, maxCells(cells))
		if l.y-h < contentFloor {
			l.continuation()
			l.tableHeader()
		}
		l.row(styleCell, cells)
		l.rule(contentLeft, contentRight, l.y, 0.4, 0.82)
	}
	l.y -= 8
}

func maxCells(cells [][]string) int {
	n := 1
	for _, c := range cells {
		n = max(n, len(c))
	}
	return n
}

// totals draws the totals block at the right, the last total in bold under a rule.
func (l *docLayout) totals() {
	if len(l.d.Totals) == 0 {
		return
	}
	labelW, valueW := 0.0, 0.0
	for _, t := range l.d.Totals {
		labelW = max(labelW, l.width(styleGrand, t.Label))
		valueW = max(valueW, l.width(styleGrand, t.Value))
	}
	blockW := min(max(labelW+valueW+24, 200), contentWidth)
	labelMax := max(blockW-valueW-16, 40)
	h := 6.0
	for i := range l.d.Totals {
		if i == len(l.d.Totals)-1 {
			h += styleGrand.lead + 4
		} else {
			h += styleTotal.lead
		}
	}
	if l.y-h < contentFloor {
		l.continuation()
	}
	x := contentRight - blockW
	right := contentRight - cellPad
	y := l.y - 6 // the top of the next total
	for i, t := range l.d.Totals {
		st := styleTotal
		if i == len(l.d.Totals)-1 {
			st = styleGrand
			y -= 2
			l.rule(x, contentRight, y, 0.75, 0.3)
			y -= 2
		}
		baseline := y - st.size
		l.text(st, x+cellPad, baseline, l.fit(st, t.Label, labelMax))
		l.textRight(st, right, baseline, l.fit(st, t.Value, blockW-cellPad*2))
		y -= st.lead
	}
	l.y = y - 4
}

func (l *docLayout) notes() {
	if strings.TrimSpace(l.d.Notes) == "" {
		return
	}
	l.y -= 10
	if l.y-styleLabel.lead-styleBody.lead < contentFloor {
		l.continuation()
	}
	l.text(styleLabel, contentLeft, l.y, "Notes")
	l.y -= styleLabel.lead + 3
	for _, s := range l.wrap(styleBody, strings.TrimSpace(l.d.Notes), contentWidth) {
		if l.y < contentFloor {
			l.continuation()
		}
		l.text(styleBody, contentLeft, l.y, s)
		l.y -= styleBody.lead
	}
}

func (l *docLayout) footers() {
	left := l.title + " " + l.d.Number
	for i, p := range l.pages {
		l.cur = p
		page := fmt.Sprintf("Page %d of %d", i+1, len(l.pages))
		l.rule(contentLeft, contentRight, 44, 0.4, 0.8)
		l.text(styleFooter, contentLeft, 32, l.fit(styleFooter, left, contentWidth-l.width(styleFooter, page)-20))
		l.textRight(styleFooter, contentRight, 32, page)
		if l.d.FooterText != "" {
			l.text(styleFooter, contentLeft, 20, l.fit(styleFooter, l.d.FooterText, contentWidth))
		}
	}
}

// RenderDocument lays out a document and returns the PDF bytes.
func RenderDocument(d DocumentRequest) ([]byte, error) {
	r, err := RenderDocumentReport(d)
	if err != nil {
		return nil, err
	}
	return r.PDF, nil
}

// RenderDocumentReport lays out a document and returns the PDF with its page count and
// the number of characters the fonts could not draw.
func RenderDocumentReport(d DocumentRequest) (RenderedDocument, error) {
	if err := ValidateDocument(d); err != nil {
		return RenderedDocument{}, err
	}
	l := &docLayout{d: normalizeDocument(d), t: newDocText(), title: docTitles[d.DocumentType]}
	if l.d.Title != "" {
		l.title = l.d.Title
	}
	l.newPage()
	l.header()
	l.parties()
	l.body()
	l.table()
	l.totals()
	l.notes()
	l.footers()
	return RenderedDocument{
		PDF:           assemblePDF(l.pages, l.t, l.title+" "+l.d.Number),
		Pages:         len(l.pages),
		MissingGlyphs: l.t.missing,
	}, nil
}

// assemblePDF writes the objects in a fixed order: 1 catalog, 2 page tree, 3 document
// information, 4 to 8 the regular font, 9 to 13 the bold font, then a page and its content
// stream per page.
func assemblePDF(pages []*bytes.Buffer, t *docText, title string) []byte {
	var out bytes.Buffer
	var offsets []int
	obj := func(body string) {
		offsets = append(offsets, out.Len())
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", len(offsets), body)
	}
	out.WriteString("%PDF-1.5\n%\xE2\xE3\xCF\xD3\n") // 1.5: ActualText on marked content
	const firstPage = 14
	obj("<< /Type /Catalog /Pages 2 0 R >>")
	kids := make([]string, len(pages))
	for i := range pages {
		kids[i] = fmt.Sprintf("%d 0 R", firstPage+2*i)
	}
	obj(fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), len(pages)))
	obj(fmt.Sprintf("<< /Title %s /Producer (Passage print service) >>", utf16Hex(title)))
	// The fonts come after the pages are laid out, when the glyphs used are known.
	for _, o := range pdfFontObjects(faceRegular, t.used[faceRegular], 4) {
		obj(o)
	}
	for _, o := range pdfFontObjects(faceBold, t.used[faceBold], 9) {
		obj(o)
	}
	for i, p := range pages {
		obj(fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %.0f %.0f] /Resources << /Font << /F1 4 0 R /F2 9 0 R >> >> /Contents %d 0 R >>", pageWidth, pageHeight, firstPage+1+2*i))
		obj(fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", p.Len(), p.String()))
	}
	xref := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", len(offsets)+1)
	for _, o := range offsets {
		fmt.Fprintf(&out, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root 1 0 R /Info 3 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets)+1, xref)
	return out.Bytes()
}
