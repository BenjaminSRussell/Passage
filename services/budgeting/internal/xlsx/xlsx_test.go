package xlsx

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestWriteThenReadKeepsTextAndExactNumbers(t *testing.T) {
	in := []Sheet{
		{Name: "Plan", Rows: [][]Cell{{Str("Account"), Str("Cost <center> & \"co\""), Str("2027-01")}, {Str("6000"), Str("CC1"), Num("1234.5678")}, nil, {Str("=1+1"), {}, Num("0.1")}}},
		{Name: "Info", Rows: [][]Cell{{Str("Plan"), Str("abc")}}},
	}
	raw, err := Write(in)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := Write(in)
	if !bytes.Equal(raw, again) {
		t.Error("equal input must give equal bytes")
	}
	out, err := Read(context.Background(), raw)
	if err != nil || len(out) != 2 || out[0].Name != "Plan" || out[1].Name != "Info" {
		t.Fatalf("read = %+v, %v", out, err)
	}
	if got := out[0].At(0, 1); got.Text != `Cost <center> & "co"` || got.IsNumber {
		t.Errorf("B1 = %+v", got)
	}
	if got := out[0].At(1, 2); got.Text != "1234.5678" || !got.IsNumber {
		t.Errorf("C2 = %+v", got)
	}
	if got := out[0].At(3, 0); got.Text != "=1+1" || got.IsNumber {
		t.Errorf("a text that looks like a formula must stay text: %+v", got)
	}
	if !out[0].At(2, 0).Blank() || !out[0].At(9, 9).Blank() {
		t.Error("missing cells are blank")
	}
}

// excelStyle is what Excel writes: shared strings, rich text, a formula string result and a typed number.
func excelStyle(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	add := func(name, body string) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(body))
	}
	add("xl/workbook.xml", `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Plan" sheetId="1" r:id="rId3"/></sheets></workbook>`)
	add("xl/_rels/workbook.xml.rels", `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId3" Type="x" Target="worksheets/sheet1.xml"/></Relationships>`)
	add("xl/sharedStrings.xml", `<sst><si><t>Account</t></si><si><r><t>Cost </t></r><r><t>center</t></r><rPh><t>ignored</t></rPh></si></sst>`)
	add("xl/worksheets/sheet1.xml", `<worksheet><sheetData><row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>1</v></c><c r="AA1" t="str"><f>A1</f><v>calc</v></c></row><row r="3"><c r="C3" s="2"><v>1.5E3</v></c><c r="D3" t="b"><v>1</v></c></row></sheetData></worksheet>`)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestReadExcelStyleWorkbook(t *testing.T) {
	out, err := Read(context.Background(), excelStyle(t))
	if err != nil {
		t.Fatal(err)
	}
	s := out[0]
	if s.At(0, 0).Text != "Account" || s.At(0, 1).Text != "Cost center" || s.At(0, 26).Text != "calc" {
		t.Errorf("row 1 = %+v", s.Rows[0])
	}
	if c := s.At(2, 2); c.Text != "1.5E3" || !c.IsNumber {
		t.Errorf("C3 = %+v", c)
	}
	if c := s.At(2, 3); c.IsNumber {
		t.Errorf("a boolean is not a number: %+v", c)
	}
}

func TestReadRefusesWhatIsNotAWorkbook(t *testing.T) {
	for name, data := range map[string][]byte{"empty": nil, "text": []byte("a,b\n1,2\n"), "zip without a workbook": zipOf(t, map[string]string{"a.txt": "x"})} {
		if _, err := Read(context.Background(), data); !errors.Is(err, ErrUnreadable) {
			t.Errorf("%s: %v", name, err)
		}
	}
	huge := zipOf(t, map[string]string{
		"xl/workbook.xml":            `<workbook xmlns:r="r"><sheets><sheet name="A" r:id="rId1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="rId1" Target="worksheets/sheet1.xml"/></Relationships>`,
		"xl/worksheets/sheet1.xml":   `<worksheet><sheetData><row r="99999"><c r="A99999"><v>1</v></c></row></sheetData></worksheet>`,
	})
	if _, err := Read(context.Background(), huge); !errors.Is(err, ErrUnreadable) {
		t.Errorf("a cell far outside the limits: %v", err)
	}
	if _, err := Write([]Sheet{{Name: strings.Repeat("x", 32)}}); err == nil {
		t.Error("a sheet name over 31 characters must be refused")
	}
}

func zipOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for n, b := range files {
		w, _ := zw.Create(n)
		_, _ = w.Write([]byte(b))
	}
	_ = zw.Close()
	return buf.Bytes()
}

func TestRefsAndColumnNames(t *testing.T) {
	for i, want := range map[int]string{0: "A", 25: "Z", 26: "AA", 27: "AB", 51: "AZ", 52: "BA"} {
		if got := ColName(i); got != want {
			t.Errorf("ColName(%d) = %s, want %s", i, got, want)
		}
	}
	if r, c, ok := parseRef("AA12"); !ok || r != 11 || c != 26 {
		t.Errorf("AA12 = %d %d %v", r, c, ok)
	}
	if _, _, ok := parseRef("12"); ok {
		t.Error("a ref without a column must be refused")
	}
}

func TestRead_RefusesTheHostileShapes(t *testing.T) {
	ctx := context.Background()
	rels := `<Relationships><Relationship Id="rId1" Target="worksheets/sheet1.xml"/></Relationships>`
	sheet := `<worksheet><sheetData><row r="1"><c r="A1"><v>1</v></c></row></sheetData></worksheet>`
	// Many <sheet> elements pointing at one relationship id.
	var many strings.Builder
	many.WriteString(`<workbook xmlns:r="r"><sheets>`)
	for i := 0; i < 2000; i++ {
		fmt.Fprintf(&many, `<sheet name="S%d" r:id="rId1"/>`, i)
	}
	many.WriteString(`</sheets></workbook>`)
	dup := zipOf(t, map[string]string{"xl/workbook.xml": many.String(), "xl/_rels/workbook.xml.rels": rels, "xl/worksheets/sheet1.xml": sheet})
	if _, err := Read(ctx, dup); !errors.Is(err, ErrUnreadable) {
		t.Errorf("2000 sheets on one id: %v", err)
	}
	two := zipOf(t, map[string]string{
		"xl/workbook.xml":            `<workbook xmlns:r="r"><sheets><sheet name="A" r:id="rId1"/><sheet name="B" r:id="rId1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": rels, "xl/worksheets/sheet1.xml": sheet})
	if _, err := Read(ctx, two); !errors.Is(err, ErrUnreadable) {
		t.Errorf("a repeated relationship id: %v", err)
	}
	// Only the named sheets are opened.
	one := zipOf(t, map[string]string{
		"xl/workbook.xml":            `<workbook xmlns:r="r"><sheets><sheet name="A" r:id="rId1"/><sheet name="B" r:id="rId2"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="rId1" Target="worksheets/sheet1.xml"/><Relationship Id="rId2" Target="worksheets/missing.xml"/></Relationships>`,
		"xl/worksheets/sheet1.xml":   sheet})
	if got, err := Read(ctx, one, "A"); err != nil || len(got) != 1 {
		t.Errorf("named sheet only: %v %v", got, err)
	}
	// A part that decompresses past the budget.
	big := zipOf(t, map[string]string{
		"xl/workbook.xml":            `<workbook xmlns:r="r"><sheets><sheet name="A" r:id="rId1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": rels,
		"xl/worksheets/sheet1.xml":   `<worksheet><sheetData>` + strings.Repeat(" ", 40<<20) + `</sheetData></worksheet>`})
	if _, err := Read(ctx, big); !errors.Is(err, ErrUnreadable) {
		t.Errorf("an oversized part: %v", err)
	}
	// A long column reference is a cell error, not a panic.
	long := zipOf(t, map[string]string{
		"xl/workbook.xml":            `<workbook xmlns:r="r"><sheets><sheet name="A" r:id="rId1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": rels,
		"xl/worksheets/sheet1.xml":   `<worksheet><sheetData><row r="1"><c r="ZZZZZZZZZZZZZZZZ1"><v>1</v></c></row></sheetData></worksheet>`})
	if _, err := Read(ctx, long); !errors.Is(err, ErrUnreadable) || !strings.Contains(err.Error(), "ZZZZZZZZZZZZZZZZ1") {
		t.Errorf("a long column ref: %v", err)
	}
	if _, _, ok := parseRef("ZZZZZZZZZZZZZZZZ1"); ok {
		t.Error("parseRef accepted a 16-letter column")
	}
	// A cancelled context stops the read.
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := Read(cctx, zipOf(t, map[string]string{"xl/workbook.xml": `<workbook xmlns:r="r"><sheets><sheet name="A" r:id="rId1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": rels, "xl/worksheets/sheet1.xml": sheet})); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: %v", err)
	}
}
