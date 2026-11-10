package domain

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/image/font/sfnt"
	"pgregory.net/rapid"
)

func sampleDocument(docType string) DocumentRequest {
	return DocumentRequest{
		DocumentType: docType,
		Number:       "PO-000123",
		LegalEntity:  DocParty{Name: "Cedar Ridge Pumps, Inc.", Address: []string{"400 Industrial Way", "Cedar Rapids, IA 52404"}},
		Counterparty: &DocParty{Label: "Vendor", Name: "Ironbridge Foundry", Address: []string{"88 Foundry Rd", "Birmingham, AL"}},
		Fields:       []DocField{{Label: "Order date", Value: "2026-11-10"}, {Label: "Currency", Value: "USD"}},
		Columns:      []DocColumn{{Header: "Item"}, {Header: "Qty", Align: "R"}, {Header: "Unit price", Align: "R"}, {Header: "Amount", Align: "R"}},
		Lines: [][]string{
			{"Impeller casting (CST-100) (rev B)", "40", "112.50", "4,500.00"},
			{"Volute casting", "12", "98.00", "1,176.00"},
		},
		Totals: []DocField{{Label: "Total", Value: "5,676.00 USD"}},
		Notes:  "Deliver to dock 2.",
	}
}

// unicodeInvoice has names in Polish, Czech, Greek, Cyrillic and German, a long wrapped
// description, a deliver-to party, and characters the fonts lack (CJK, the rupee sign).
func unicodeInvoice() DocumentRequest {
	return DocumentRequest{
		DocumentType: DocCustomerInvoice,
		Number:       "INV-2026-0042",
		LegalEntity:  DocParty{Name: "Łódzkie Zakłady Pomp Sp. z o.o.", Address: []string{"ul. Piotrkowska 104", "90-926 Łódź", "PL"}},
		Counterparty: &DocParty{Label: "Bill to", Name: "Žluťoučký kůň s.r.o.", Address: []string{"Náměstí Míru 15", "120 00 Praha 2", "CZ"}},
		AdditionalParties: []DocParty{
			{Label: "Ship to", Name: "Αθηναϊκή Εμπορική Α.Ε.", Address: []string{"Οδός Ερμού 25", "105 63 Αθήνα", "GR"}},
		},
		Fields:  []DocField{{Label: "Invoice date", Value: "2026-11-10"}, {Label: "Due date", Value: "2026-12-10"}, {Label: "Contact", Value: "Пётр Иванович Сидоров"}, {Label: "Buyer ref", Value: "東京支店 発注"}},
		Columns: []DocColumn{{Header: "Description"}, {Header: "Qty", Align: "R"}, {Header: "Unit price", Align: "R"}, {Header: "Net", Align: "R"}, {Header: "Tax", Align: "R"}},
		Lines: [][]string{
			{"Pompa odśrodkowa – wirnik ze stali nierdzewnej, uszczelnienie mechaniczne, kołnierz DN50; zgodna z normą PN-EN 733", "2", "1 250,00", "2 500,00", "575,00"},
			{"Straßenablauf (Gußeisen) — Größe 3", "10", "45,00", "450,00", "103,50"},
			{"Сервисное обслуживание насосной станции", "1", "900,00", "900,00", "207,00"},
		},
		Totals: []DocField{{Label: "Net", Value: "3 850,00"}, {Label: "Tax", Value: "885,50"}, {Label: "Total PLN", Value: "4 735,50"}},
		Notes:  "Płatność przelewem w terminie 30 dni.\nThank you — merci — danke — спасибо — 谢谢 — ₹.",
	}
}

// multiPagePackingSlip has enough lines, one of them long, to need three pages.
func multiPagePackingSlip() DocumentRequest {
	d := DocumentRequest{
		DocumentType: DocPackingSlip,
		Number:       "PS-000777",
		LegalEntity:  DocParty{Name: "Cedar Ridge Pumps, Inc.", Address: []string{"400 Industrial Way", "Cedar Rapids, IA 52404", "US"}},
		Counterparty: &DocParty{Label: "Ship to", Name: "Großhandel Müller GmbH", Address: []string{"Königstraße 12", "70173 Stuttgart", "DE"}},
		AdditionalParties: []DocParty{
			{Label: "Ship from", Name: "Cedar Rapids DC", Address: []string{"1200 Dock Rd", "Cedar Rapids, IA 52404"}},
		},
		Fields:  []DocField{{Label: "Sales order", Value: "SO-004512"}, {Label: "Shipped on", Value: "2026-11-10"}},
		Columns: []DocColumn{{Header: "Item"}, {Header: "Order line", Align: "R"}, {Header: "Qty shipped", Align: "R"}, {Header: "Unit"}},
	}
	for i := 1; i <= 70; i++ {
		item := fmt.Sprintf("Flange gasket DN%d", 10*i)
		if i == 33 {
			item = strings.TrimSpace(strings.Repeat("Replacement seal kit for the CR-200 centrifugal pump, with O-rings and instructions. ", 4))
		}
		d.Lines = append(d.Lines, []string{item, strconv.Itoa(i), strconv.Itoa(i * 3), "pcs"})
	}
	return d
}

// remittanceAdvice is one payment of a payment run as the payables page sends it (#528):
// the vendor, the invoices the payment settled with discount and tax withheld, and the
// totals. The request has the same shape as apps/web/src/modules/payables/remittanceDocument.ts.
func remittanceAdvice() DocumentRequest {
	return DocumentRequest{
		DocumentType: DocRemittanceAdvice,
		Number:       "E2E-20261120-0001",
		LegalEntity:  DocParty{Name: "Cedar Ridge Pumps, Inc.", Address: []string{"400 Industrial Way", "Cedar Rapids, IA 52404", "US"}},
		Counterparty: &DocParty{Label: "Vendor", Name: "Ironbridge Foundry (V-20004)"},
		Fields:       []DocField{{Label: "Payment date", Value: "2026-11-20"}, {Label: "Payment method", Value: "ACH credit"}, {Label: "Currency", Value: "USD"}},
		Columns: []DocColumn{
			{Header: "Invoice"}, {Header: "Invoice date"}, {Header: "Amount open", Align: "R"},
			{Header: "Discount", Align: "R"}, {Header: "Tax withheld", Align: "R"}, {Header: "Paid", Align: "R"},
		},
		Lines: [][]string{
			{"INV-1001", "2026-09-01", "600.10", "20.00", "12.00", "568.10"},
			{"INV-1002", "2026-09-03", "400.00", "0.00", "0.00", "400.00"},
		},
		Totals: []DocField{{Label: "Discount taken", Value: "20.00"}, {Label: "Tax withheld", Value: "12.00"}, {Label: "Total paid USD", Value: "968.10"}},
	}
}

// dumpPDF writes a rendered document to DOCUMENT_PDF_DIR, when set, to look at.
func dumpPDF(t *testing.T, name string, pdf []byte) {
	if dir := os.Getenv("DOCUMENT_PDF_DIR"); dir != "" {
		if err := os.WriteFile(filepath.Join(dir, name), pdf, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func checkGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	dumpPDF(t, name, got)
	golden := filepath.Join("..", "..", "testdata", "golden", name)
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(golden, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden: %v (run with UPDATE_GOLDEN=1 to create it)", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s differs from testdata/golden/%s; if the layout change is intended, run with UPDATE_GOLDEN=1", name, name)
	}
}

func TestRenderDocument_MatchesGoldenAndIsDeterministic(t *testing.T) {
	cases := map[string]DocumentRequest{
		"purchase-order.pdf":       sampleDocument(DocPurchaseOrder),
		"invoice-unicode.pdf":      unicodeInvoice(),
		"packing-slip-3-pages.pdf": multiPagePackingSlip(),
		"remittance-advice.pdf":    remittanceAdvice(),
	}
	for name, d := range cases {
		t.Run(name, func(t *testing.T) {
			first, err := RenderDocument(d)
			if err != nil {
				t.Fatal(err)
			}
			second, _ := RenderDocument(d)
			if !bytes.Equal(first, second) {
				t.Fatal("two renders of the same document differ")
			}
			if !bytes.HasPrefix(first, []byte("%PDF-")) || !bytes.HasSuffix(first, []byte("%%EOF\n")) {
				t.Fatalf("not a PDF: %q", first[:20])
			}
			checkPDFStructure(t, first)
			checkGolden(t, name, first)
		})
	}
}

// checkPDFStructure checks that every xref entry points at its object and that every
// stream's /Length matches its data.
func checkPDFStructure(t *testing.T, pdf []byte) {
	t.Helper()
	at := bytes.LastIndex(pdf, []byte("startxref\n"))
	xref, err := strconv.Atoi(strings.TrimSpace(string(pdf[at+10 : len(pdf)-6])))
	if err != nil || !bytes.HasPrefix(pdf[xref:], []byte("xref\n")) {
		t.Fatalf("startxref does not point at the xref table: %v", err)
	}
	entries := regexp.MustCompile(`(\d{10}) 00000 n `).FindAllSubmatch(pdf[xref:], -1)
	for i, e := range entries {
		off, _ := strconv.Atoi(string(e[1]))
		if want := fmt.Sprintf("%d 0 obj\n", i+1); !bytes.HasPrefix(pdf[off:], []byte(want)) {
			t.Errorf("xref entry %d points at %q", i+1, pdf[off:off+12])
		}
	}
	for _, m := range regexp.MustCompile(`/Length (\d+)[^>]*>>\nstream\n`).FindAllSubmatchIndex(pdf, -1) {
		n, _ := strconv.Atoi(string(pdf[m[2]:m[3]]))
		rest := pdf[m[1]:]
		if len(rest) < n || !bytes.HasPrefix(bytes.TrimLeft(rest[n:], "\n"), []byte("endstream")) {
			t.Errorf("stream at %d is not %d bytes long", m[1], n)
		}
	}
}

// draws counts how often the PDF draws s in the face.
func draws(pdf []byte, face int, s string) int {
	hex := regexp.QuoteMeta(hexGlyphs(newDocText().encode(face, s).glyphs))
	return len(regexp.MustCompile(fmt.Sprintf(`/F%d [0-9.]+ Tf [0-9.]+ [0-9.]+ Td %s Tj`, face+1, hex)).FindAll(pdf, -1))
}

// shows says whether the PDF draws s in either face.
func shows(pdf []byte, s string) bool {
	return draws(pdf, faceRegular, s)+draws(pdf, faceBold, s) > 0
}

func TestRenderDocument_ShowsEntityNumberTitleAndCounterparty(t *testing.T) {
	for docType, title := range docTitles {
		pdf, err := RenderDocument(sampleDocument(docType))
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"Cedar Ridge Pumps, Inc.", "400 Industrial Way", "PO-000123", title, "VENDOR", "Ironbridge Foundry", "88 Foundry Rd", "Page 1 of 1", "5,676.00 USD"} {
			if !shows(pdf, want) {
				t.Errorf("%s: PDF does not show %q", docType, want)
			}
		}
	}
}

func TestRenderDocument_DrawsUnicodeWithEmbeddedSubsetFonts(t *testing.T) {
	r, err := RenderDocumentReport(unicodeInvoice())
	if err != nil {
		t.Fatal(err)
	}
	pdf := r.PDF
	for _, want := range []string{"Łódzkie Zakłady Pomp Sp. z o.o.", "Žluťoučký kůň s.r.o.", "Αθηναϊκή Εμπορική Α.Ε.", "Пётр Иванович", "Οδός Ερμού 25", "SHIP TO"} {
		if !shows(pdf, want) {
			t.Errorf("PDF does not show %q", want)
		}
	}
	for _, want := range []string{"/Subtype /Type0", "/Encoding /Identity-H", "/Subtype /CIDFontType2", "/CIDToGIDMap /Identity", "/FontFile2", "/ToUnicode", "+GoRegular", "+GoBold"} {
		if !bytes.Contains(pdf, []byte(want)) {
			t.Errorf("PDF lacks %s", want)
		}
	}
	if bytes.Contains(pdf, []byte("/Helvetica")) {
		t.Error("PDF still refers to a non-embedded base font")
	}
	// The ToUnicode CMap maps the glyph of Ł back to U+0141, so the text can be copied.
	g, _ := newDocText().glyphIndex(faceBold, 'Ł')
	if !bytes.Contains(pdf, []byte(fmt.Sprintf("<%04X> <0141>", uint16(g)))) {
		t.Error("ToUnicode does not map Ł")
	}
	// 東京支店 発注, 谢谢 and ₹ are not in the fonts: 9 characters drawn as U+FFFD, with
	// the original text kept as ActualText.
	if r.MissingGlyphs != 9 {
		t.Errorf("missing glyphs = %d, want 9", r.MissingGlyphs)
	}
	if !bytes.Contains(pdf, []byte("/ActualText "+utf16Hex("東京支店 発注"))) {
		t.Error("the CJK field value is not kept as ActualText")
	}
	// Each embedded program is a valid TrueType font that keeps the glyph ids.
	programs := regexp.MustCompile(`/Length1 (\d+) >>\nstream\n`).FindAllSubmatchIndex(pdf, -1)
	if len(programs) != 2 {
		t.Fatalf("embedded font programs = %d, want 2", len(programs))
	}
	for i, m := range programs {
		n, _ := strconv.Atoi(string(pdf[m[2]:m[3]]))
		program := pdf[m[1] : m[1]+n]
		f, err := sfnt.Parse(program)
		if err != nil {
			t.Fatalf("font %d does not parse: %v", i, err)
		}
		if f.NumGlyphs() != docFaces[i].numGlyphs {
			t.Errorf("font %d has %d glyphs, want %d", i, f.NumGlyphs(), docFaces[i].numGlyphs)
		}
		if len(program) > len(docFaces[i].tables["glyf"]) {
			t.Errorf("font %d is %d bytes: it is not a subset", i, len(program))
		}
	}
}

func TestSubset_KeepsCompositeGlyphParts(t *testing.T) {
	f := docFaces[faceRegular]
	var composite sfnt.GlyphIndex
	for _, r := range "óąŻÅéü" {
		g, _ := newDocText().glyphIndex(faceRegular, r)
		if len(components(f.glyph(int(g)))) > 0 {
			composite = g
			break
		}
	}
	if composite == 0 {
		t.Skip("no composite glyph among the accented letters")
	}
	tables, err := ttfTables(f.subset([]sfnt.GlyphIndex{composite}))
	if err != nil {
		t.Fatal(err)
	}
	sub := &fontFace{tables: tables, numGlyphs: f.numGlyphs}
	if len(sub.glyph(int(composite))) == 0 {
		t.Fatal("the composite glyph is empty in the subset")
	}
	for _, c := range components(f.glyph(int(composite))) {
		if len(sub.glyph(c)) == 0 {
			t.Errorf("component %d of the composite glyph is empty in the subset", c)
		}
	}
	if g, _ := newDocText().glyphIndex(faceRegular, 'W'); len(sub.glyph(int(g))) != 0 {
		t.Error("an unused glyph kept its outline")
	}
}

func TestRenderDocument_NormalizesCombiningAccents(t *testing.T) {
	d := sampleDocument(DocPurchaseOrder)
	d.Counterparty.Name = "Zakłady Łódź" // z + combining acute
	pdf, err := RenderDocument(d)
	if err != nil {
		t.Fatal(err)
	}
	if !shows(pdf, "Zakłady Łódź") {
		t.Error("a combining accent is not composed with its letter")
	}
}

// Number formats use no-break, narrow no-break and thin spaces, and pasted text carries
// soft hyphens and zero-width characters; none of them may print as a missing glyph.
func TestRenderDocument_PrintsEverySpaceAsASpaceAndDropsInvisibleCharacters(t *testing.T) {
	d := sampleDocument(DocPurchaseOrder)
	d.Totals = []DocField{{Label: "Total\u00a0EUR", Value: "1\u202f250,00\u2009€"}}
	d.Counterparty.Name = "Iron\u00adbridge\u200b Foundry\ufeff"
	d.Fields[0].Value = "2026-11-10\tconfirmed"
	r, err := RenderDocumentReport(d)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Total EUR", "1 250,00 €", "Ironbridge Foundry", "2026-11-10 confirmed"} {
		if !shows(r.PDF, want) {
			t.Errorf("PDF does not show %q", want)
		}
	}
	if r.MissingGlyphs != 0 {
		t.Errorf("missing glyphs = %d, want 0", r.MissingGlyphs)
	}
}

func TestRenderDocument_EscapesAndPaginatesWithRepeatedHeaders(t *testing.T) {
	d := sampleDocument(DocCustomerInvoice)
	d.LegalEntity.Name = `Acme (East) \ Co`
	d.Lines = nil
	for i := 0; i < 90; i++ {
		d.Lines = append(d.Lines, []string{fmt.Sprintf("Line %d", i), "1", "1.00", "1.00"})
	}
	r, err := RenderDocumentReport(d)
	if err != nil {
		t.Fatal(err)
	}
	dumpPDF(t, "paginated.pdf", r.PDF)
	if r.Pages != 3 || bytes.Count(r.PDF, []byte("/Type /Page ")) != 3 {
		t.Fatalf("pages = %d, want 3", r.Pages)
	}
	for _, want := range []string{`Acme (East) \ Co`, "Page 1 of 3", "Page 2 of 3", "Page 3 of 3", "Line 89"} {
		if !shows(r.PDF, want) {
			t.Errorf("PDF does not show %q", want)
		}
	}
	// The table header and the running header repeat on every page.
	if got := draws(r.PDF, faceBold, "Unit price"); got != 3 {
		t.Errorf("table header drawn %d times, want 3", got)
	}
	if got := draws(r.PDF, faceBold, "Invoice PO-000123"); got != 2 {
		t.Errorf("running header drawn %d times, want 2", got)
	}
	if r.MissingGlyphs != 0 {
		t.Errorf("missing glyphs = %d", r.MissingGlyphs)
	}
}

func TestRenderDocument_KeepsLongRowsWholeAndWrapsDescriptions(t *testing.T) {
	d := sampleDocument(DocPurchaseOrder)
	long := strings.TrimSpace(strings.Repeat("Stainless impeller with mechanical seal and DN50 flange. ", 8))
	d.Lines = nil
	for i := 0; i < 30; i++ {
		d.Lines = append(d.Lines, []string{fmt.Sprintf("%d %s", i, long), "1", "1.00", "1.00"})
	}
	r, err := RenderDocumentReport(d)
	if err != nil {
		t.Fatal(err)
	}
	if r.Pages < 3 {
		t.Errorf("pages = %d, want the long rows to need at least 3", r.Pages)
	}
	if bytes.Contains(r.PDF, []byte(hexGlyphs(newDocText().encode(faceRegular, long).glyphs))) {
		t.Error("the long description is drawn on one line")
	}
	l := &docLayout{d: d, t: newDocText()}
	for _, s := range l.wrap(styleCell, long, 200) {
		if w := l.width(styleCell, s); w > 200 {
			t.Errorf("wrapped line %q is %.1f wide", s, w)
		}
	}
	if got := l.wrap(styleCell, "Supercalifragilisticexpialidocious", 40); len(got) < 2 {
		t.Errorf("a word wider than the column is not broken: %q", got)
	}
}

func TestColumnWidths_RightAlignedColumnsKeepTheirTextWidth(t *testing.T) {
	d := normalizeDocument(unicodeInvoice())
	l := &docLayout{d: d, t: newDocText()}
	w := l.columnWidths()
	total := 0.0
	for i, c := range d.Columns {
		total += w[i]
		if c.Align == "R" {
			for _, line := range d.Lines {
				if l.width(styleCell, line[i])+2*cellPad > w[i]+0.01 {
					t.Errorf("column %s is %.1f wide, narrower than %q", c.Header, w[i], line[i])
				}
			}
		}
	}
	if total < contentWidth-0.01 || total > contentWidth+0.01 {
		t.Errorf("columns are %.2f wide in all, want %.2f", total, contentWidth)
	}
}

func TestValidateDocument_NamesTheField(t *testing.T) {
	cases := map[string]func(d *DocumentRequest){
		"documentType":              func(d *DocumentRequest) { d.DocumentType = "RECEIPT" },
		"number":                    func(d *DocumentRequest) { d.Number = " " },
		"legalEntity.name":          func(d *DocumentRequest) { d.LegalEntity.Name = "" },
		"lines[0]":                  func(d *DocumentRequest) { d.Lines[0] = []string{"only one"} },
		"lines[1][0]":               func(d *DocumentRequest) { d.Lines[1][0] = "Volute\x07" },
		"counterparty.address[1]":   func(d *DocumentRequest) { d.Counterparty.Address[1] = "Line\nbreak" },
		"additionalParties":         func(d *DocumentRequest) { d.AdditionalParties = make([]DocParty, 3) },
		"additionalParties[0].name": func(d *DocumentRequest) { d.AdditionalParties = []DocParty{{Label: "Ship to"}} },
		"columns":                   func(d *DocumentRequest) { d.Columns = nil },
		"notes":                     func(d *DocumentRequest) { d.Notes = strings.Repeat("x", 2001) },
		"fields[0].value":           func(d *DocumentRequest) { d.Fields[0].Value = "a\u2028b" },
		"totals[0].label":           func(d *DocumentRequest) { d.Totals[0].Label = "" },
	}
	for field, mutate := range cases {
		d := sampleDocument(DocPurchaseOrder)
		mutate(&d)
		_, err := RenderDocument(d)
		de, ok := err.(*Error)
		if !ok || de.Code != CodeDocument || !strings.Contains(de.Detail, field) {
			t.Errorf("%s: err = %v, want %s naming the field", field, err, CodeDocument)
		}
	}
}

func TestValidateDocument_AcceptsAnyPrintableText(t *testing.T) {
	d := sampleDocument(DocPurchaseOrder)
	d.Lines[1][0] = "Volute ✓ 漢字 ₹"
	d.Notes = "Line one\nLine two"
	if err := ValidateDocument(d); err != nil {
		t.Fatal(err)
	}
}

func TestValidateDocument_TooManyLinesHasItsOwnCode(t *testing.T) {
	d := sampleDocument(DocPurchaseOrder)
	for len(d.Lines) <= maxDocLines {
		d.Lines = append(d.Lines, []string{"Casting", "1", "1.00", "1.00"})
	}
	_, err := RenderDocument(d)
	if de, ok := err.(*Error); !ok || de.Code != CodeDocumentTooLarge {
		t.Fatalf("err = %v, want %s", err, CodeDocumentTooLarge)
	}
	d.Lines = d.Lines[:maxDocLines]
	if _, err := RenderDocument(d); err != nil {
		t.Fatalf("%d lines: %v", maxDocLines, err)
	}
}

// textRun is one piece of text a page draws: where, how wide, and in which style.
type textRun struct {
	page        int
	x, y, width float64
}

var textRunRE = regexp.MustCompile(`BT /F(\d) ([0-9.]+) Tf (-?[0-9.]+) (-?[0-9.]+) Td <([0-9A-F]*)> Tj ET`)

// textRuns reads back every text run of a rendered document, page by page.
func textRuns(pages []string) ([]textRun, error) {
	var out []textRun
	for i, page := range pages {
		if strings.Count(page, " Tj") != len(textRunRE.FindAllString(page, -1)) {
			return nil, fmt.Errorf("page %d has a text run the test cannot read", i+1)
		}
		for _, m := range textRunRE.FindAllStringSubmatch(page, -1) {
			face, _ := strconv.Atoi(m[1])
			size, _ := strconv.ParseFloat(m[2], 64)
			x, _ := strconv.ParseFloat(m[3], 64)
			y, _ := strconv.ParseFloat(m[4], 64)
			units := 0
			for k := 0; k+4 <= len(m[5]); k += 4 {
				g, _ := strconv.ParseUint(m[5][k:k+4], 16, 16)
				units += int(docFaces[face-1].advance[g])
			}
			out = append(out, textRun{page: i + 1, x: x, y: y, width: float64(units) * size / docFaces[face-1].upem})
		}
	}
	return out, nil
}

// pageStreams returns the content stream of every page of a rendered document.
func pageStreams(l *docLayout) []string {
	out := make([]string, len(l.pages))
	for i, p := range l.pages {
		out[i] = p.String()
	}
	return out
}

func layOut(d DocumentRequest) *docLayout {
	l := &docLayout{d: normalizeDocument(d), t: newDocText(), title: docTitles[d.DocumentType]}
	l.newPage()
	l.header()
	l.parties()
	l.table()
	l.totals()
	l.notes()
	l.footers()
	return l
}

// insidePage lays d out and says whether any text leaves the margins or runs into the
// footer, and whether every page is numbered.
func insidePage(d DocumentRequest) error {
	if err := ValidateDocument(d); err != nil {
		return fmt.Errorf("the document is not valid: %w", err)
	}
	pages := pageStreams(layOut(d))
	runs, err := textRuns(pages)
	if err != nil {
		return err
	}
	footers := 0
	for _, r := range runs {
		if r.x < contentLeft-0.01 || r.x+r.width > contentRight+0.01 {
			return fmt.Errorf("page %d: text from %.2f to %.2f leaves the margins (%.0f to %.0f)", r.page, r.x, r.x+r.width, contentLeft, contentRight)
		}
		if r.y == 32 {
			footers++
			continue
		}
		if r.y < contentFloor-0.01 || r.y > pageTop {
			return fmt.Errorf("page %d: text at height %.2f is outside %.0f to %.0f", r.page, r.y, contentFloor, pageTop)
		}
	}
	if footers != 2*len(pages) {
		return fmt.Errorf("%d footer texts on %d pages", footers, len(pages))
	}
	for i, p := range pages {
		want := hexGlyphs(newDocText().encode(faceRegular, fmt.Sprintf("Page %d of %d", i+1, len(pages))).glyphs)
		if !strings.Contains(p, want) {
			return fmt.Errorf("page %d is not numbered %d of %d", i+1, i+1, len(pages))
		}
	}
	return nil
}

// The fullest document the limits allow: every block at its maximum, with text that
// cannot be broken at spaces.
func TestRenderDocument_FullestDocumentStaysInsideThePage(t *testing.T) {
	for _, filler := range []string{"Wide unbroken? No: words. ", "W"} {
		fill := func(n int) string { return strings.Repeat(filler, n/len(filler)+1)[:n] }
		party := func() DocParty {
			p := DocParty{Label: fill(maxDocField), Name: fill(maxDocField)}
			for i := 0; i < maxDocAddressLines; i++ {
				p.Address = append(p.Address, fill(maxDocField))
			}
			return p
		}
		counterparty := party()
		d := DocumentRequest{
			DocumentType:      DocPurchaseOrder,
			Number:            fill(maxDocField),
			LegalEntity:       party(),
			Counterparty:      &counterparty,
			AdditionalParties: []DocParty{party(), party()},
			Notes:             fill(maxDocNotes),
		}
		for i := 0; i < maxDocFieldCount; i++ {
			d.Fields = append(d.Fields, DocField{Label: fill(maxDocField), Value: fill(maxDocField)})
			d.Totals = append(d.Totals, DocField{Label: fill(maxDocField), Value: fill(maxDocField)})
		}
		for i := 0; i < maxDocCols; i++ {
			d.Columns = append(d.Columns, DocColumn{Header: fill(maxDocField), Align: []string{"L", "R"}[i%2]})
		}
		for i := 0; i < 12; i++ {
			line := make([]string, maxDocCols)
			for j := range line {
				line[j] = fill(maxDocCell)
			}
			d.Lines = append(d.Lines, line)
		}
		if err := insidePage(d); err != nil {
			t.Errorf("filler %q: %v", filler, err)
		}
		r, err := RenderDocumentReport(d)
		if err != nil {
			t.Fatal(err)
		}
		dumpPDF(t, fmt.Sprintf("fullest-%d.pdf", len(filler)), r.PDF)
	}
}

// Whatever a valid document holds, every piece of text stays between the margins and
// above the footer, and every page is numbered.
func TestRenderDocument_KeepsAllTextInsideThePage(t *testing.T) {
	text := func(maxLen int) *rapid.Generator[string] {
		return rapid.OneOf(
			rapid.StringOfN(rapid.RuneFrom([]rune("abcdefghij ąćęłńóśźż ЖЩЫ αβγ 0123456789.,-")), 0, maxLen, -1),
			rapid.StringOfN(rapid.RuneFrom([]rune("Wm0")), 0, maxLen, -1), // wide and unbroken
			rapid.SampledFrom([]string{"", "1,234.50", "pcs", "Impeller casting", "東京"}),
		)
	}
	name := func(t *rapid.T, label string) string {
		if s := strings.TrimSpace(text(maxDocField).Draw(t, label)); s != "" {
			return s
		}
		return "Name"
	}
	party := func(t *rapid.T, label string) DocParty {
		return DocParty{
			Label:   text(40).Draw(t, label+" label"),
			Name:    name(t, label+" name"),
			Address: rapid.SliceOfN(text(maxDocField), 0, maxDocAddressLines).Draw(t, label+" address"),
		}
	}
	fields := func(t *rapid.T, label string) []DocField {
		n := rapid.IntRange(0, maxDocFieldCount).Draw(t, label+" count")
		out := make([]DocField, n)
		for i := range out {
			out[i] = DocField{Label: name(t, label+" label"), Value: text(maxDocField).Draw(t, label+" value")}
		}
		return out
	}
	rapid.Check(t, func(t *rapid.T) {
		d := DocumentRequest{
			DocumentType: rapid.SampledFrom([]string{DocPurchaseOrder, DocCustomerInvoice, DocPackingSlip, DocRemittanceAdvice}).Draw(t, "type"),
			Number:       name(t, "number"),
			LegalEntity:  party(t, "entity"),
			Fields:       fields(t, "fields"),
			Totals:       fields(t, "totals"),
			Notes:        text(maxDocNotes).Draw(t, "notes"),
		}
		if rapid.Bool().Draw(t, "has counterparty") {
			c := party(t, "counterparty")
			d.Counterparty = &c
		}
		for i := rapid.IntRange(0, maxDocParties).Draw(t, "parties"); i > 0; i-- {
			d.AdditionalParties = append(d.AdditionalParties, party(t, "party"))
		}
		cols := rapid.IntRange(1, maxDocCols).Draw(t, "columns")
		for i := 0; i < cols; i++ {
			d.Columns = append(d.Columns, DocColumn{Header: name(t, "header"), Align: rapid.SampledFrom([]string{"", "L", "R"}).Draw(t, "align")})
		}
		for i := rapid.IntRange(0, 60).Draw(t, "lines"); i > 0; i-- {
			d.Lines = append(d.Lines, rapid.SliceOfN(text(maxDocCell), cols, cols).Draw(t, "line"))
		}
		if err := insidePage(d); err != nil {
			t.Fatal(err)
		}
	})
}
