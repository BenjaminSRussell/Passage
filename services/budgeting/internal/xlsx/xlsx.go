// Package xlsx writes and reads the plain SpreadsheetML workbooks the budget plan round trip needs: sheets of text and number cells.
// It uses only the standard library (archive/zip and encoding/xml), so no spreadsheet dependency is added. The reader accepts what
// Excel, LibreOffice and Google Sheets save: shared strings, inline strings, formula string results and numbers; it ignores styles,
// formulas themselves, merged cells and everything else. It refuses a workbook that is too large once unzipped.
package xlsx

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
	"time"
)

// Limits on what Read takes in; a plan workbook is far below them.
const (
	maxPartBytes = 32 << 20
	maxEntries   = 200
	maxRows      = 20000
	maxCols      = 64
)

// ErrUnreadable is returned for anything that is not a workbook this package can read.
var ErrUnreadable = errors.New("xlsx: not a readable workbook")

// Cell is one cell: text, or a number kept as the text the file holds so no precision is lost.
type Cell struct {
	Text     string
	IsNumber bool
}

// Str makes a text cell.
func Str(s string) Cell { return Cell{Text: s} }

// Num makes a number cell from decimal text.
func Num(s string) Cell { return Cell{Text: s, IsNumber: true} }

// Sheet is a named grid of cells; Rows[0] is row 1. A row may be shorter than the others and an empty Cell is a blank one.
type Sheet struct {
	Name string
	Rows [][]Cell
}

// Blank reports whether the cell holds nothing.
func (c Cell) Blank() bool { return strings.TrimSpace(c.Text) == "" }

// At returns the cell at a zero-based row and column, blank when outside the sheet.
func (s Sheet) At(row, col int) Cell {
	if row < 0 || row >= len(s.Rows) || col < 0 || col >= len(s.Rows[row]) {
		return Cell{}
	}
	return s.Rows[row][col]
}

// ColName converts a zero-based column index to its letters (0 is A, 26 is AA).
func ColName(i int) string {
	name := ""
	for i >= 0 {
		name = string(rune('A'+i%26)) + name
		i = i/26 - 1
	}
	return name
}

// Ref is the A1 reference of a zero-based row and column.
func Ref(row, col int) string { return ColName(col) + strconv.Itoa(row+1) }

var epoch = time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)

// Write makes a workbook of the sheets. Text is stored as inline strings, which a spreadsheet never evaluates as a formula, and a
// number as the exact text given. Equal input gives equal bytes.
func Write(sheets []Sheet) ([]byte, error) {
	if len(sheets) == 0 {
		return nil, errors.New("xlsx: a workbook needs a sheet")
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	put := func(name, body string) error {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: epoch})
		if err != nil {
			return err
		}
		_, err = io.WriteString(w, body)
		return err
	}
	var types, rels, wb, wbRels strings.Builder
	types.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n" + `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>`)
	rels.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n" + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>`)
	wb.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n" + `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets>`)
	wbRels.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n" + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">`)
	for i, sh := range sheets {
		n := i + 1
		if err := validName(sh.Name); err != nil {
			return nil, err
		}
		body, err := sheetXML(sh)
		if err != nil {
			return nil, err
		}
		if err := put(fmt.Sprintf("xl/worksheets/sheet%d.xml", n), body); err != nil {
			return nil, err
		}
		fmt.Fprintf(&types, `<Override PartName="/xl/worksheets/sheet%d.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>`, n)
		var name strings.Builder
		if err := xml.EscapeText(&name, []byte(sh.Name)); err != nil {
			return nil, err
		}
		fmt.Fprintf(&wb, `<sheet name="%s" sheetId="%d" r:id="rId%d"/>`, name.String(), n, n)
		fmt.Fprintf(&wbRels, `<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet%d.xml"/>`, n, n)
	}
	types.WriteString(`</Types>`)
	wb.WriteString(`</sheets></workbook>`)
	wbRels.WriteString(`</Relationships>`)
	for _, p := range []struct{ name, body string }{{"[Content_Types].xml", types.String()}, {"_rels/.rels", rels.String()}, {"xl/workbook.xml", wb.String()}, {"xl/_rels/workbook.xml.rels", wbRels.String()}} {
		// The package parts come first in a conventional workbook; order inside a zip does not matter to readers.
		if err := put(p.name, p.body); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func validName(n string) error {
	if n == "" || len([]rune(n)) > 31 || strings.ContainsAny(n, `[]:*?/\`) {
		return fmt.Errorf("xlsx: %q is not a valid sheet name", n)
	}
	return nil
}

func sheetXML(sh Sheet) (string, error) {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n" + `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><cols><col min="1" max="2" width="22" customWidth="1"/><col min="3" max="16" width="14" customWidth="1"/></cols><sheetData>`)
	for r, row := range sh.Rows {
		wrote := false
		for c, cell := range row {
			if cell.Text == "" {
				continue
			}
			if !wrote {
				fmt.Fprintf(&b, `<row r="%d">`, r+1)
				wrote = true
			}
			if cell.IsNumber {
				fmt.Fprintf(&b, `<c r="%s"><v>%s</v></c>`, Ref(r, c), cell.Text)
				continue
			}
			b.WriteString(`<c r="` + Ref(r, c) + `" t="inlineStr"><is><t xml:space="preserve">`)
			if err := xml.EscapeText(&b, []byte(cell.Text)); err != nil {
				return "", err
			}
			b.WriteString(`</t></is></c>`)
		}
		if wrote {
			b.WriteString(`</row>`)
		}
	}
	b.WriteString(`</sheetData></worksheet>`)
	return b.String(), nil
}

// Read parses a workbook into its sheets, in workbook order.
func Read(data []byte) ([]Sheet, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(zr.File) > maxEntries {
		return nil, ErrUnreadable
	}
	parts := map[string]*zip.File{}
	for _, f := range zr.File {
		parts[path.Clean(f.Name)] = f
	}
	wbPart, err := readPart(parts, "xl/workbook.xml")
	if err != nil {
		return nil, err
	}
	var wb struct {
		Sheets []struct {
			Name string `xml:"name,attr"`
			RID  string `xml:"id,attr"`
		} `xml:"sheets>sheet"`
	}
	if err := xml.Unmarshal(wbPart, &wb); err != nil || len(wb.Sheets) == 0 {
		return nil, ErrUnreadable
	}
	relPart, err := readPart(parts, "xl/_rels/workbook.xml.rels")
	if err != nil {
		return nil, err
	}
	var rels struct {
		Items []struct {
			ID     string `xml:"Id,attr"`
			Target string `xml:"Target,attr"`
		} `xml:"Relationship"`
	}
	if err := xml.Unmarshal(relPart, &rels); err != nil {
		return nil, ErrUnreadable
	}
	target := map[string]string{}
	for _, r := range rels.Items {
		t := r.Target
		if strings.HasPrefix(t, "/") {
			t = strings.TrimPrefix(t, "/")
		} else {
			t = path.Join("xl", t)
		}
		target[r.ID] = path.Clean(t)
	}
	shared, err := sharedStrings(parts)
	if err != nil {
		return nil, err
	}
	out := make([]Sheet, 0, len(wb.Sheets))
	for _, s := range wb.Sheets {
		name, ok := target[s.RID]
		if !ok {
			return nil, ErrUnreadable
		}
		raw, err := readPart(parts, name)
		if err != nil {
			return nil, err
		}
		rows, err := parseSheet(raw, shared)
		if err != nil {
			return nil, err
		}
		out = append(out, Sheet{Name: s.Name, Rows: rows})
	}
	return out, nil
}

func readPart(parts map[string]*zip.File, name string) ([]byte, error) {
	f, ok := parts[name]
	if !ok {
		return nil, ErrUnreadable
	}
	rc, err := f.Open()
	if err != nil {
		return nil, ErrUnreadable
	}
	defer func() { _ = rc.Close() }()
	raw, err := io.ReadAll(io.LimitReader(rc, maxPartBytes+1))
	if err != nil || len(raw) > maxPartBytes {
		return nil, ErrUnreadable
	}
	return raw, nil
}

func sharedStrings(parts map[string]*zip.File) ([]string, error) {
	if _, ok := parts["xl/sharedStrings.xml"]; !ok {
		return nil, nil
	}
	raw, err := readPart(parts, "xl/sharedStrings.xml")
	if err != nil {
		return nil, err
	}
	dec := xml.NewDecoder(bytes.NewReader(raw))
	var out []string
	var cur strings.Builder
	inSI, inT, inPhonetic := false, false, false
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, ErrUnreadable
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "si":
				inSI = true
				cur.Reset()
			case "t":
				inT = inSI && !inPhonetic
			case "rPh":
				inPhonetic = true
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "si":
				out = append(out, cur.String())
				inSI = false
			case "t":
				inT = false
			case "rPh":
				inPhonetic = false
			}
		case xml.CharData:
			if inT {
				cur.Write(t)
			}
		}
	}
}

type xmlCell struct {
	Ref    string `xml:"r,attr"`
	Type   string `xml:"t,attr"`
	Value  string `xml:"v"`
	Inline struct {
		Parts []string `xml:"t"`
		Runs  []struct {
			T string `xml:"t"`
		} `xml:"r"`
	} `xml:"is"`
}

func parseSheet(raw []byte, shared []string) ([][]Cell, error) {
	var ws struct {
		Rows []struct {
			Cells []xmlCell `xml:"c"`
		} `xml:"sheetData>row"`
	}
	if err := xml.Unmarshal(raw, &ws); err != nil {
		return nil, ErrUnreadable
	}
	var rows [][]Cell
	for _, r := range ws.Rows {
		for _, c := range r.Cells {
			row, col, ok := parseRef(c.Ref)
			if !ok || row >= maxRows || col >= maxCols {
				return nil, ErrUnreadable
			}
			cell := Cell{}
			switch c.Type {
			case "s":
				idx, err := strconv.Atoi(strings.TrimSpace(c.Value))
				if err != nil || idx < 0 || idx >= len(shared) {
					return nil, ErrUnreadable
				}
				cell.Text = shared[idx]
			case "inlineStr":
				var sb strings.Builder
				for _, p := range c.Inline.Parts {
					sb.WriteString(p)
				}
				for _, run := range c.Inline.Runs {
					sb.WriteString(run.T)
				}
				cell.Text = sb.String()
			case "str":
				cell.Text = c.Value
			case "b", "e", "d":
				cell.Text = c.Value // not numbers: the caller reads them as text and will not accept them as an amount
			default:
				cell.Text, cell.IsNumber = strings.TrimSpace(c.Value), strings.TrimSpace(c.Value) != ""
			}
			for len(rows) <= row {
				rows = append(rows, nil)
			}
			for len(rows[row]) <= col {
				rows[row] = append(rows[row], Cell{})
			}
			rows[row][col] = cell
		}
	}
	return rows, nil
}

// parseRef reads "C12" into zero-based row 11 and column 2.
func parseRef(ref string) (row, col int, ok bool) {
	i := 0
	for i < len(ref) && ref[i] >= 'A' && ref[i] <= 'Z' {
		col = col*26 + int(ref[i]-'A') + 1
		i++
	}
	if i == 0 || i == len(ref) {
		return 0, 0, false
	}
	n, err := strconv.Atoi(ref[i:])
	if err != nil || n < 1 {
		return 0, 0, false
	}
	return n - 1, col - 1, true
}
