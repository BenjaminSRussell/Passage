// Package xlsx writes and reads the plain SpreadsheetML workbooks the budget plan round trip needs: sheets of text and number cells.
// It uses only the standard library (archive/zip and encoding/xml), so no spreadsheet dependency is added. The reader accepts what
// Excel, LibreOffice and Google Sheets save: shared strings, inline strings, formula string results and numbers; it ignores styles,
// formulas themselves, merged cells and everything else. It refuses a workbook that is too large once unzipped.
package xlsx

import (
	"archive/zip"
	"bytes"
	"context"
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

// maxSheets bounds the <sheet> elements of a workbook; maxCells bounds the cells of one sheet; maxTotalBytes bounds everything
// decompressed across all parts of one workbook.
const (
	maxSheets     = 10
	maxCells      = 200000
	maxTotalBytes = 48 << 20
)

// budget is the decompressed bytes a Read may still take across all parts.
type budget struct{ left int64 }

// Read parses the named sheets of a workbook (every sheet when none is named), in workbook order. A workbook that lists more than
// 10 sheets or two sheets with one relationship id is refused, a sheet that is not named is never opened, and the decompressed size
// of all parts together is bounded, so a small file cannot make Read allocate a large one. Reading stops when ctx is done.
func Read(ctx context.Context, data []byte, only ...string) ([]Sheet, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(zr.File) > maxEntries {
		return nil, ErrUnreadable
	}
	parts := map[string]*zip.File{}
	for _, f := range zr.File {
		parts[path.Clean(f.Name)] = f
	}
	left := &budget{left: maxTotalBytes}
	wbPart, err := readPart(parts, "xl/workbook.xml", left)
	if err != nil {
		return nil, err
	}
	var wb struct {
		Sheets []struct {
			Name string `xml:"name,attr"`
			RID  string `xml:"id,attr"`
		} `xml:"sheets>sheet"`
	}
	if err := xml.Unmarshal(wbPart, &wb); err != nil || len(wb.Sheets) == 0 || len(wb.Sheets) > maxSheets {
		return nil, ErrUnreadable
	}
	seenRID := map[string]bool{}
	for _, s := range wb.Sheets {
		if seenRID[s.RID] {
			return nil, ErrUnreadable
		}
		seenRID[s.RID] = true
	}
	relPart, err := readPart(parts, "xl/_rels/workbook.xml.rels", left)
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
	wanted := func(name string) bool {
		if len(only) == 0 {
			return true
		}
		for _, o := range only {
			if o == name {
				return true
			}
		}
		return false
	}
	var shared []string
	sharedRead := false
	out := make([]Sheet, 0, len(wb.Sheets))
	for _, s := range wb.Sheets {
		if !wanted(s.Name) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name, ok := target[s.RID]
		if !ok {
			return nil, ErrUnreadable
		}
		if !sharedRead {
			if shared, err = sharedStrings(ctx, parts, left); err != nil {
				return nil, err
			}
			sharedRead = true
		}
		f, ok := parts[name]
		if !ok {
			return nil, ErrUnreadable
		}
		rows, err := parseSheet(ctx, f, shared, left)
		if err != nil {
			return nil, err
		}
		out = append(out, Sheet{Name: s.Name, Rows: rows})
	}
	return out, nil
}

// limited reads a part through the shared budget; going over it is ErrUnreadable.
type limited struct {
	r io.Reader
	b *budget
}

func (l *limited) Read(p []byte) (int, error) {
	if l.b.left <= 0 {
		return 0, ErrUnreadable
	}
	if int64(len(p)) > l.b.left {
		p = p[:l.b.left]
	}
	n, err := l.r.Read(p)
	l.b.left -= int64(n)
	return n, err
}

func openPart(parts map[string]*zip.File, name string, left *budget) (io.ReadCloser, io.Reader, error) {
	f, ok := parts[name]
	if !ok {
		return nil, nil, ErrUnreadable
	}
	if int64(f.UncompressedSize64) > maxPartBytes {
		return nil, nil, ErrUnreadable
	}
	rc, err := f.Open()
	if err != nil {
		return nil, nil, ErrUnreadable
	}
	return rc, &limited{r: io.LimitReader(rc, maxPartBytes+1), b: left}, nil
}

func readPart(parts map[string]*zip.File, name string, left *budget) ([]byte, error) {
	rc, r, err := openPart(parts, name, left)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	raw, err := io.ReadAll(r)
	if err != nil || len(raw) > maxPartBytes {
		return nil, ErrUnreadable
	}
	return raw, nil
}

func sharedStrings(ctx context.Context, parts map[string]*zip.File, left *budget) ([]string, error) {
	if _, ok := parts["xl/sharedStrings.xml"]; !ok {
		return nil, nil
	}
	rc, r, err := openPart(parts, "xl/sharedStrings.xml", left)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	dec := xml.NewDecoder(r)
	var out []string
	var cur strings.Builder
	inSI, inT, inPhonetic := false, false, false
	for n := 0; ; n++ {
		if n%1024 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil || len(out) > maxCells {
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

// parseSheet streams one worksheet, so memory follows the cells that are there and not the size a file claims.
func parseSheet(ctx context.Context, f *zip.File, shared []string, left *budget) ([][]Cell, error) {
	rc, r, err := openPart(map[string]*zip.File{"s": f}, "s", left)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	dec := xml.NewDecoder(r)
	var rows [][]Cell
	cells := 0
	var (
		cur      Cell
		ref, typ string
		inCell   bool
		inV, inT bool
		text     strings.Builder
		value    strings.Builder
	)
	for n := 0; ; n++ {
		if n%1024 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return rows, nil
		}
		if err != nil {
			return nil, ErrUnreadable
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "c":
				inCell, cur, ref, typ = true, Cell{}, "", ""
				text.Reset()
				value.Reset()
				for _, a := range t.Attr {
					switch a.Name.Local {
					case "r":
						ref = a.Value
					case "t":
						typ = a.Value
					}
				}
			case "v":
				inV = inCell
			case "t":
				inT = inCell
			}
		case xml.CharData:
			if inV {
				value.Write(t)
			} else if inT {
				text.Write(t)
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "v":
				inV = false
			case "t":
				inT = false
			case "c":
				inCell = false
				if cells++; cells > maxCells {
					return nil, fmt.Errorf("%w: more than %d cells", ErrUnreadable, maxCells)
				}
				row, col, ok := parseRef(ref)
				if !ok || row >= maxRows || col >= maxCols {
					return nil, fmt.Errorf("%w: cell %q is outside the supported range (columns up to %s, rows up to %d)", ErrUnreadable, ref, ColName(maxCols-1), maxRows)
				}
				v := strings.TrimSpace(value.String())
				switch typ {
				case "s":
					idx, err := strconv.Atoi(v)
					if err != nil || idx < 0 || idx >= len(shared) {
						return nil, ErrUnreadable
					}
					cur.Text = shared[idx]
				case "inlineStr":
					cur.Text = text.String()
				case "str", "b", "e", "d":
					cur.Text = value.String()
				default:
					cur.Text, cur.IsNumber = v, v != ""
				}
				for len(rows) <= row {
					rows = append(rows, nil)
				}
				for len(rows[row]) <= col {
					rows[row] = append(rows[row], Cell{})
				}
				rows[row][col] = cur
			}
		}
	}
}

// parseRef reads "C12" into zero-based row 11 and column 2. A reference with more than three column letters, or with a row that is
// not a number, is not ok.
func parseRef(ref string) (row, col int, ok bool) {
	i := 0
	for i < len(ref) && ref[i] >= 'A' && ref[i] <= 'Z' {
		if i >= 3 {
			return 0, 0, false
		}
		col = col*26 + int(ref[i]-'A') + 1
		i++
	}
	if i == 0 || i == len(ref) || len(ref)-i > 7 {
		return 0, 0, false
	}
	n, err := strconv.Atoi(ref[i:])
	if err != nil || n < 1 {
		return 0, 0, false
	}
	return n - 1, col - 1, true
}
