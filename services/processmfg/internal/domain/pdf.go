package domain

import (
	"bytes"
	"fmt"
	"strings"
)

// PDF text layout: US Letter, Courier 8 pt, 11 pt leading.
const (
	pdfLineWidth    = 110
	pdfLinesPerPage = 64
)

// RenderTextPDF renders lines as a plain PDF 1.4 document in Courier, wrapping
// long lines and breaking pages, with a page footer. The output depends only on
// title and lines (no creation date), so the same sealed record always renders
// to the same bytes. It is NOT PDF/A: the standard Courier font is not embedded
// and there is no XMP metadata or output intent (see NOT-IMPLEMENTED.md).
// Characters outside Latin-1 are written as '?'.
func RenderTextPDF(title string, lines []string) []byte {
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

	// Objects: 1 catalog, 2 pages, 3 font, 4 info, then a page and its content per page.
	n := len(pages)
	objects := make([]string, 4+2*n)
	kids := make([]string, n)
	for i := range pages {
		kids[i] = fmt.Sprintf("%d 0 R", 5+2*i)
	}
	objects[0] = "<< /Type /Catalog /Pages 2 0 R >>"
	objects[1] = fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), n)
	objects[2] = "<< /Type /Font /Subtype /Type1 /BaseFont /Courier /Encoding /WinAnsiEncoding >>"
	objects[3] = "<< /Title (" + pdfString(title) + ") /Producer (Passage processmfg) >>"
	for i, page := range pages {
		var c bytes.Buffer
		c.WriteString("BT /F1 8 Tf 11 TL 36 756 Td\n")
		for _, l := range page {
			c.WriteString("(" + pdfString(l) + ") Tj T*\n")
		}
		c.WriteString("ET\nBT /F1 8 Tf 36 30 Td (" + pdfString(fmt.Sprintf("%s - page %d of %d", title, i+1, n)) + ") Tj ET")
		objects[4+2*i] = fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents %d 0 R /Resources << /Font << /F1 3 0 R >> >> >>", 6+2*i)
		objects[5+2*i] = fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", c.Len(), c.Bytes())
	}
	var out bytes.Buffer
	out.WriteString("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")
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
	fmt.Fprintf(&out, "trailer << /Size %d /Root 1 0 R /Info 4 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, startxref)
	return out.Bytes()
}

// pdfString escapes s for a PDF literal string in WinAnsi: Latin-1 letters are
// written as octal escapes, anything else outside printable ASCII as '?'.
func pdfString(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\\' || r == '(' || r == ')':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r >= 0x20 && r < 0x7f:
			b.WriteRune(r)
		case r >= 0xa0 && r <= 0xff:
			fmt.Fprintf(&b, "\\%03o", r)
		default:
			b.WriteByte('?')
		}
	}
	return b.String()
}
