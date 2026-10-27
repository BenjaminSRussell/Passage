package domain

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// RenderXLSX writes a statement as a minimal SpreadsheetML workbook (FR-15.6,
// PRD §2.15.15) with one sheet, "Statement": the header block, a blank row, the
// grid, a blank row and the "as of" footer, with the same content as
// RenderCSV. Amounts are numeric cells holding the exact six-place decimal
// text; everything else is an inline string, which a spreadsheet never
// evaluates as a formula. Blank cells are left out. Zip entries carry a fixed
// timestamp, so equal inputs give equal bytes.
func RenderXLSX(def Definition, cells []Cell, st Stamp) ([]byte, error) {
	lay := buildLayout(def, cells, st)
	text := func(s string) layoutValue { return layoutValue{text: s} }
	var grid [][]layoutValue
	for _, h := range lay.header {
		grid = append(grid, []layoutValue{text(h[0]), text(h[1])})
	}
	grid = append(grid, nil)
	header := []layoutValue{text("row"), text("label")}
	for _, label := range lay.columns {
		header = append(header, text(label))
	}
	grid = append(grid, header)
	for _, r := range lay.rows {
		line := []layoutValue{text(r.code), text(strings.Repeat("  ", r.indent) + r.label)}
		grid = append(grid, append(line, r.values...))
	}
	grid = append(grid, nil)
	for _, f := range lay.footer {
		var line []layoutValue
		for _, s := range f {
			line = append(line, text(s))
		}
		grid = append(grid, line)
	}

	var sheet bytes.Buffer
	sheet.WriteString(xml.Header)
	sheet.WriteString(`<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>`)
	for i, line := range grid {
		if len(line) == 0 {
			continue
		}
		rowNo := strconv.Itoa(i + 1)
		sheet.WriteString(`<row r="` + rowNo + `">`)
		for j, v := range line {
			if v.text == "" {
				continue
			}
			ref := colName(j) + rowNo
			if v.numeric {
				fmt.Fprintf(&sheet, `<c r="%s"><v>%s</v></c>`, ref, v.text)
				continue
			}
			sheet.WriteString(`<c r="` + ref + `" t="inlineStr"><is><t xml:space="preserve">`)
			if err := xml.EscapeText(&sheet, []byte(v.text)); err != nil {
				return nil, err
			}
			sheet.WriteString(`</t></is></c>`)
		}
		sheet.WriteString(`</row>`)
	}
	sheet.WriteString(`</sheetData></worksheet>`)

	parts := []struct{ name, body string }{
		{"[Content_Types].xml", xlsxContentTypes},
		{"_rels/.rels", xlsxRootRels},
		{"xl/workbook.xml", xlsxWorkbook},
		{"xl/_rels/workbook.xml.rels", xlsxWorkbookRels},
		{"xl/worksheets/sheet1.xml", sheet.String()},
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, p := range parts {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: p.name, Method: zip.Deflate, Modified: xlsxEpoch})
		if err != nil {
			return nil, err
		}
		if _, err := w.Write([]byte(p.body)); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// xlsxEpoch is the timestamp of every zip entry: the earliest a zip can hold,
// fixed so the bytes do not depend on when the file was rendered.
var xlsxEpoch = time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)

// colName converts a zero-based column index to its spreadsheet letters (0 is A, 26 is AA).
func colName(i int) string {
	name := ""
	for i >= 0 {
		name = string(rune('A'+i%26)) + name
		i = i/26 - 1
	}
	return name
}

const xlsxContentTypes = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/></Types>`

const xlsxRootRels = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>`

const xlsxWorkbook = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Statement" sheetId="1" r:id="rId1"/></sheets></workbook>`

const xlsxWorkbookRels = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`
