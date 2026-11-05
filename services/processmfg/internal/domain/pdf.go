package domain

import (
	"bytes"
	"compress/zlib"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
	"unicode/utf16"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

// PDF text layout: US Letter, a monospaced 8 pt font, 11 pt leading.
const (
	pdfLineWidth    = 110
	pdfLinesPerPage = 64
	pdfProducer     = "Passage processmfg"
	// PDFAPart and PDFAConformance are the PDF/A level RenderTextPDF claims in
	// its XMP metadata (pdfaid:part, pdfaid:conformance): PDF/A-2b.
	PDFAPart        = 2
	PDFAConformance = "B"
	pdfFirstChar    = 32
	pdfLastChar     = 255
)

// PDFMeta is the document metadata of a rendering. Created (the seal time of
// a batch record) is written as both creation and modification date; zero
// leaves the dates out. Nothing else about the time of rendering is recorded,
// so the same input renders to the same bytes.
type PDFMeta struct {
	Title   string
	Created time.Time
}

// winAnsiHigh is the WinAnsiEncoding of the codes 128-159 (PDF 32000-1 Annex
// D); 0 marks a code WinAnsi leaves undefined. 32-126 and 160-255 are Latin-1.
var winAnsiHigh = [32]rune{
	0x20ac, 0, 0x201a, 0x0192, 0x201e, 0x2026, 0x2020, 0x2021, 0x02c6, 0x2030, 0x0160, 0x2039, 0x0152, 0, 0x017d, 0,
	0, 0x2018, 0x2019, 0x201c, 0x201d, 0x2022, 0x2013, 0x2014, 0x02dc, 0x2122, 0x0161, 0x203a, 0x0153, 0, 0x017e, 0x0178,
}

func winAnsiRune(code int) rune {
	switch {
	case code >= 128 && code < 160:
		return winAnsiHigh[code-128]
	case code == 127:
		return 0
	}
	return rune(code)
}

// embeddedFont is the font every rendering embeds (PDF/A-2 6.2.11.4): Go Mono
// (BSD licence, golang.org/x/image), a TrueType font with the 600/1000 advance
// of Courier, so the layout is unchanged. Its descriptor and widths are read
// from the font program itself, which PDF/A requires them to agree with.
type embeddedFont struct {
	program    []byte
	baseFont   string
	widths     [pdfLastChar - pdfFirstChar + 1]int
	has        [256]bool // WinAnsi codes whose glyph is in the font
	bbox       [4]int
	ascent     int
	descent    int
	capHeight  int
	missingWid int
}

var pdfFont = loadEmbeddedFont()

func loadEmbeddedFont() *embeddedFont {
	f, err := sfnt.Parse(gomono.TTF)
	if err != nil {
		panic("embedded PDF font: " + err.Error())
	}
	var buf sfnt.Buffer
	upem := int(f.UnitsPerEm())
	ppem := fixed.Int26_6(upem << 6) // one pixel per font unit
	scale := func(v fixed.Int26_6) int {
		// Font units to the PDF glyph space of 1000 units per em, rounded half away from zero.
		n := int64(v) * 1000
		d := int64(upem) << 6
		if n < 0 {
			return -int((-n + d/2) / d)
		}
		return int((n + d/2) / d)
	}
	name, err := f.Name(&buf, sfnt.NameIDPostScript)
	if err != nil || name == "" {
		name = "GoMono"
	}
	out := &embeddedFont{program: gomono.TTF, baseFont: name}
	adv := func(g sfnt.GlyphIndex) int {
		a, err := f.GlyphAdvance(&buf, g, ppem, font.HintingNone)
		if err != nil {
			panic("embedded PDF font: " + err.Error())
		}
		return scale(a)
	}
	out.missingWid = adv(0)
	for code := pdfFirstChar; code <= pdfLastChar; code++ {
		w := out.missingWid
		if r := winAnsiRune(code); r != 0 {
			if g, err := f.GlyphIndex(&buf, r); err == nil && g != 0 {
				w = adv(g)
				out.has[code] = true
			}
		}
		out.widths[code-pdfFirstChar] = w
	}
	b, err := f.Bounds(&buf, ppem, font.HintingNone)
	if err != nil {
		panic("embedded PDF font: " + err.Error())
	}
	// sfnt's y axis points down.
	out.bbox = [4]int{scale(b.Min.X), scale(-b.Max.Y), scale(b.Max.X), scale(-b.Min.Y)}
	m, err := f.Metrics(&buf, ppem, font.HintingNone)
	if err != nil {
		panic("embedded PDF font: " + err.Error())
	}
	out.ascent, out.descent, out.capHeight = scale(m.Ascent), -scale(m.Descent), scale(m.CapHeight)
	return out
}

// RenderTextPDF renders lines as a PDF/A-2b document (ISO 19005-2, level B)
// in a monospaced font, wrapping long lines and breaking pages, with a page
// footer. What PDF/A asks of it: the font program is embedded (FontFile2, its
// widths and descriptor taken from the program), the catalog carries XMP
// metadata with pdfaid part 2 / conformance B that matches the Info
// dictionary, and an sRGB output intent with its ICC profile embedded; the
// trailer has a document ID; nothing is encrypted, transparent, external or
// executable. The output depends only on meta and lines, so the same sealed
// record always renders to the same bytes. A character the WinAnsi encoding
// or the font cannot show is written as '?'.
func RenderTextPDF(meta PDFMeta, lines []string) []byte {
	var wrapped []string
	for _, l := range lines {
		r := []rune(strings.ReplaceAll(l, "\t", "    "))
		for len(r) > pdfLineWidth {
			wrapped = append(wrapped, string(r[:pdfLineWidth]))
			r = append([]rune("  "), r[pdfLineWidth:]...)
		}
		wrapped = append(wrapped, string(r))
	}
	var pages [][]string
	for len(wrapped) > pdfLinesPerPage {
		pages = append(pages, wrapped[:pdfLinesPerPage])
		wrapped = wrapped[pdfLinesPerPage:]
	}
	pages = append(pages, wrapped)

	// The document ID is a hash of what is rendered (ISO 32000-1 14.4): the
	// same input keeps its ID, any change of title, date or text changes it.
	idHash := sha256.New()
	fmt.Fprintf(idHash, "%s\x00%s\x00", meta.Title, meta.Created.UTC().Format(time.RFC3339Nano))
	for _, l := range lines {
		idHash.Write([]byte(l))
		idHash.Write([]byte{0})
	}
	docID := hex.EncodeToString(idHash.Sum(nil)[:16])

	// Objects: 1 catalog, 2 pages, 3 font, 4 info, 5 font descriptor, 6 font
	// program, 7 XMP metadata, 8 output intent, 9 ICC profile, then a page and
	// its content per page.
	const fixedObjects = 9
	n := len(pages)
	objects := make([]string, fixedObjects+2*n)
	kids := make([]string, n)
	for i := range pages {
		kids[i] = fmt.Sprintf("%d 0 R", fixedObjects+1+2*i)
	}
	objects[0] = "<< /Type /Catalog /Pages 2 0 R /Metadata 7 0 R /OutputIntents [8 0 R] >>"
	objects[1] = fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), n)
	widths := make([]string, len(pdfFont.widths))
	for i, w := range pdfFont.widths {
		widths[i] = fmt.Sprint(w)
	}
	objects[2] = fmt.Sprintf("<< /Type /Font /Subtype /TrueType /BaseFont /%s /FirstChar %d /LastChar %d /Widths [%s] /FontDescriptor 5 0 R /Encoding /WinAnsiEncoding >>",
		pdfFont.baseFont, pdfFirstChar, pdfLastChar, strings.Join(widths, " "))
	info := "<< /Title " + pdfTextString(meta.Title) + " /Producer " + pdfTextString(pdfProducer)
	if !meta.Created.IsZero() {
		d := "(D:" + meta.Created.UTC().Format("20060102150405") + "+00'00')"
		info += " /CreationDate " + d + " /ModDate " + d
	}
	objects[3] = info + " >>"
	// Flags 33: FixedPitch and Nonsymbolic (the font is used with WinAnsiEncoding).
	objects[4] = fmt.Sprintf("<< /Type /FontDescriptor /FontName /%s /Flags 33 /FontBBox [%d %d %d %d] /ItalicAngle 0 /Ascent %d /Descent %d /CapHeight %d /StemV 80 /MissingWidth %d /FontFile2 6 0 R >>",
		pdfFont.baseFont, pdfFont.bbox[0], pdfFont.bbox[1], pdfFont.bbox[2], pdfFont.bbox[3], pdfFont.ascent, pdfFont.descent, pdfFont.capHeight, pdfFont.missingWid)
	program := deflate(pdfFont.program)
	objects[5] = fmt.Sprintf("<< /Length %d /Length1 %d /Filter /FlateDecode >>\nstream\n%s\nendstream", len(program), len(pdfFont.program), program)
	xmp := xmpPacket(meta, docID)
	objects[6] = fmt.Sprintf("<< /Type /Metadata /Subtype /XML /Length %d >>\nstream\n%s\nendstream", len(xmp), xmp)
	objects[7] = "<< /Type /OutputIntent /S /GTS_PDFA1 /OutputConditionIdentifier (" + iccDescText + ") /RegistryName (http://www.color.org) /Info (" +
		iccDescText + ") /DestOutputProfile 9 0 R >>"
	icc := deflate(SRGBProfile)
	objects[8] = fmt.Sprintf("<< /N 3 /Length %d /Filter /FlateDecode >>\nstream\n%s\nendstream", len(icc), icc)
	for i, page := range pages {
		var c bytes.Buffer
		c.WriteString("BT /F1 8 Tf 11 TL 36 756 Td\n")
		for _, l := range page {
			c.WriteString("(" + pdfString(l) + ") Tj T*\n")
		}
		c.WriteString("ET\nBT /F1 8 Tf 36 30 Td (" + pdfString(fmt.Sprintf("%s - page %d of %d", meta.Title, i+1, n)) + ") Tj ET")
		objects[fixedObjects+2*i] = fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents %d 0 R /Resources << /Font << /F1 3 0 R >> >> >>", fixedObjects+2+2*i)
		objects[fixedObjects+1+2*i] = fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", c.Len(), c.Bytes())
	}
	var out bytes.Buffer
	out.WriteString("%PDF-1.7\n%\xe2\xe3\xcf\xd3\n")
	offsets := make([]int, len(objects))
	for i, obj := range objects {
		offsets[i] = out.Len()
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", i+1, obj)
	}
	startxref := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, off := range offsets {
		fmt.Fprintf(&out, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root 1 0 R /Info 4 0 R /ID [<%s> <%s>] >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, docID, docID, startxref)
	return out.Bytes()
}

func deflate(b []byte) []byte {
	var out bytes.Buffer
	w, _ := zlib.NewWriterLevel(&out, zlib.BestCompression)
	_, _ = w.Write(b)
	_ = w.Close()
	return out.Bytes()
}

// xmpPacket is the document's XMP metadata (PDF/A-2 6.6): the PDF/A
// identification and the same title, producer and dates as the Info
// dictionary. The packet is written uncompressed.
func xmpPacket(meta PDFMeta, docID string) string {
	var b strings.Builder
	b.WriteString("<?xpacket begin=\"\xef\xbb\xbf\" id=\"W5M0MpCehiHzreSzNTczkc9d\"?>\n")
	b.WriteString(`<x:xmpmeta xmlns:x="adobe:ns:meta/">` + "\n")
	b.WriteString(`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` + "\n")
	b.WriteString(`<rdf:Description rdf:about="" xmlns:pdfaid="http://www.aiim.org/pdfa/ns/id/" xmlns:dc="http://purl.org/dc/elements/1.1/"` +
		` xmlns:pdf="http://ns.adobe.com/pdf/1.3/" xmlns:xmp="http://ns.adobe.com/xap/1.0/" xmlns:xmpMM="http://ns.adobe.com/xap/1.0/mm/">` + "\n")
	fmt.Fprintf(&b, "<pdfaid:part>%d</pdfaid:part>\n<pdfaid:conformance>%s</pdfaid:conformance>\n", PDFAPart, PDFAConformance)
	b.WriteString("<dc:format>application/pdf</dc:format>\n")
	b.WriteString(`<dc:title><rdf:Alt><rdf:li xml:lang="x-default">` + xmlEscape(meta.Title) + "</rdf:li></rdf:Alt></dc:title>\n")
	b.WriteString("<pdf:Producer>" + xmlEscape(pdfProducer) + "</pdf:Producer>\n")
	if !meta.Created.IsZero() {
		d := meta.Created.UTC().Format("2006-01-02T15:04:05Z")
		b.WriteString("<xmp:CreateDate>" + d + "</xmp:CreateDate>\n<xmp:ModifyDate>" + d + "</xmp:ModifyDate>\n")
	}
	b.WriteString("<xmpMM:DocumentID>uuid:" + docID[0:8] + "-" + docID[8:12] + "-" + docID[12:16] + "-" + docID[16:20] + "-" + docID[20:32] + "</xmpMM:DocumentID>\n")
	b.WriteString("</rdf:Description>\n</rdf:RDF>\n</x:xmpmeta>\n")
	b.WriteString(`<?xpacket end="r"?>`)
	return b.String()
}

func xmlEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '&':
			b.WriteString("&amp;")
		case r == '<':
			b.WriteString("&lt;")
		case r == '>':
			b.WriteString("&gt;")
		case r < 0x20 || r == 0xfffe || r == 0xffff:
			b.WriteByte('?')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// pdfTextString writes s as a PDF text string in UTF-16BE with a byte order
// mark (ISO 32000-1 7.9.2.2), so the Info dictionary holds exactly the text
// of the XMP metadata.
func pdfTextString(s string) string {
	var b strings.Builder
	b.WriteString("<FEFF")
	for _, u := range utf16.Encode([]rune(xmlUnsafeToQuestion(s))) {
		fmt.Fprintf(&b, "%04X", u)
	}
	b.WriteString(">")
	return b.String()
}

// xmlUnsafeToQuestion replaces the characters xmlEscape writes as '?'.
func xmlUnsafeToQuestion(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0xfffe || r == 0xffff {
			return '?'
		}
		return r
	}, s)
}

// pdfString escapes s for a PDF literal string in WinAnsi: Latin-1 letters are
// written as octal escapes, anything else outside printable ASCII, or without
// a glyph in the embedded font, as '?'.
func pdfString(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\\' || r == '(' || r == ')':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r >= 0x20 && r < 0x7f && pdfFont.has[r]:
			b.WriteRune(r)
		case r >= 0xa0 && r <= 0xff && pdfFont.has[r]:
			fmt.Fprintf(&b, "\\%03o", r)
		default:
			b.WriteByte('?')
		}
	}
	return b.String()
}
