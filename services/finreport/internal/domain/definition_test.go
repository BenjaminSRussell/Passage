package domain

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// sampleDefinition builds a valid definition with n rows (headers, account ranges
// and totals over the two preceding rows) and twelve columns.
func sampleDefinition(n int) Definition {
	d := Definition{SchemaVersion: SchemaVersion, Code: "DEF-200X12", Name: "Designer fixture", Kind: KindCustom}
	for i := 0; i < n; i++ {
		code := fmt.Sprintf("R%d", i)
		r := Row{Code: code, Label: "row " + code, NormalBalance: "debit", Format: Format{Indent: i % 3}}
		switch {
		case i%10 == 0:
			r.Kind = RowHeader
		case i%10 == 9:
			r.Kind = RowTotal
			r.References = []string{fmt.Sprintf("R%d", i-1), fmt.Sprintf("R%d", i-2)}
		default:
			r.Kind = RowAccountRange
			acct := fmt.Sprintf("%06d", 100000+i)
			r.AccountFrom, r.AccountTo = acct, acct
		}
		d.Rows = append(d.Rows, r)
	}
	exprs := []string{"P", "P-1", "YTD", "PY", "PYTD", "Q", "FY", "P-2", "P-3", "P-4", "P-5"}
	for j, e := range exprs {
		d.Columns = append(d.Columns, Column{Code: fmt.Sprintf("C%d", j+1), Label: e, PeriodExpr: e, CurrencyType: "acct"})
	}
	d.Columns = append(d.Columns, Column{Code: "C12", Label: "Change", PeriodExpr: "CALC", CurrencyType: "acct", Expression: "C1-C4"})
	return d
}

func codesOf(issues []Issue) []string {
	var out []string
	for _, is := range issues {
		out = append(out, is.Code)
	}
	return out
}

func TestValidate(t *testing.T) {
	base := func() Definition {
		return Definition{
			Code: "TB-STD", Name: "Trial balance", Kind: KindTrialBalance,
			Rows: []Row{
				{Code: "A", Kind: RowAccountRange, Label: "Cash", AccountFrom: "100000", AccountTo: "100999", NormalBalance: "debit"},
				{Code: "B", Kind: RowAccountRange, Label: "Equity", AccountFrom: "300000", AccountTo: "300999", NormalBalance: "credit"},
				{Code: "T", Kind: RowTotal, Label: "Total", References: []string{"A", "B"}, NormalBalance: "debit"},
			},
			Columns: []Column{{Code: "C1", Label: "Closing", PeriodExpr: "P", CurrencyType: "rpt"}},
		}
	}
	tests := []struct {
		name   string
		mutate func(d *Definition)
		want   []string
	}{
		{"valid", func(d *Definition) {}, nil},
		{"bad code", func(d *Definition) { d.Code = "x" }, []string{ShapeCode}},
		{"unknown kind", func(d *Definition) { d.Kind = "pnl" }, []string{ShapeCode}},
		{"duplicate row code", func(d *Definition) { d.Rows[1].Code = "A" }, []string{ShapeCode, CodeExpression}},
		{"account range reversed", func(d *Definition) { d.Rows[0].AccountTo = "099999" }, []string{ShapeCode}},
		{"bad normal balance", func(d *Definition) { d.Rows[0].NormalBalance = "both" }, []string{ShapeCode}},
		{"unknown cash flow tag", func(d *Definition) { d.Rows[0].CashFlowTag = "misc" }, []string{ShapeCode}},
		{"total references a missing row", func(d *Definition) { d.Rows[2].References = []string{"A", "Z"} }, []string{CodeExpression}},
		{"total without references", func(d *Definition) { d.Rows[2].References = nil }, []string{ShapeCode}},
		{"calculation with bad syntax", func(d *Definition) {
			d.Rows = append(d.Rows, Row{Code: "K", Kind: RowCalculation, Label: "k", Expression: "A +", NormalBalance: "debit"})
		}, []string{CodeExpression}},
		{"calculation over rows", func(d *Definition) {
			d.Rows = append(d.Rows, Row{Code: "K", Kind: RowCalculation, Label: "k", Expression: "(A-B)/T*100", NormalBalance: "debit"})
		}, nil},
		{"row cycle", func(d *Definition) {
			d.Rows[0] = Row{Code: "A", Kind: RowTotal, Label: "Cash", References: []string{"T"}, NormalBalance: "debit"}
		}, []string{CodeExpression}},
		{"unknown period expression", func(d *Definition) { d.Columns[0].PeriodExpr = "MTD" }, []string{CodeExpression}},
		{"calc column without expression", func(d *Definition) {
			d.Columns[0].PeriodExpr = "CALC"
		}, []string{CodeExpression}},
		{"calc column referencing unknown column", func(d *Definition) {
			d.Columns = append(d.Columns, Column{Code: "C2", Label: "x", PeriodExpr: "CALC", CurrencyType: "acct", Expression: "C1-C9"})
		}, []string{CodeExpression}},
		{"expression on a non-calc column", func(d *Definition) { d.Columns[0].Expression = "C1" }, []string{ShapeCode}},
		{"no columns", func(d *Definition) { d.Columns = nil }, []string{CodeTooLarge}},
		{"too many rows", func(d *Definition) {
			d.Rows = make([]Row, MaxRows+1)
			for i := range d.Rows {
				d.Rows[i] = Row{Code: fmt.Sprintf("H%d", i), Kind: RowHeader, Label: "h", NormalBalance: "debit"}
			}
		}, []string{CodeTooLarge}},
		{"too many columns", func(d *Definition) {
			d.Columns = make([]Column, MaxColumns+1)
			for i := range d.Columns {
				d.Columns[i] = Column{Code: fmt.Sprintf("C%d", i+1), Label: "c", PeriodExpr: "P", CurrencyType: "acct"}
			}
		}, []string{CodeTooLarge}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := base()
			tc.mutate(&d)
			got := codesOf(d.Validate())
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			for _, w := range tc.want {
				if !containsCode(got, w) {
					t.Fatalf("issues %v do not include %s", d.Validate(), w)
				}
			}
			if len(tc.want) == 0 {
				t.Fatalf("unexpected issues: %v", d.Validate())
			}
		})
	}
}

func containsCode(codes []string, want string) bool {
	for _, c := range codes {
		if c == want {
			return true
		}
	}
	return false
}

// AC2: a 200-row, 12-column definition round-trips through JSON export and
// import without change, and validates before and after.
func TestDefinitionRoundTrip200x12(t *testing.T) {
	d := sampleDefinition(200)
	if issues := d.Validate(); len(issues) != 0 {
		t.Fatalf("fixture invalid: %v", issues)
	}
	first, err := d.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	back, err := Parse(first)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(d, back) {
		t.Fatal("parsed definition differs from the exported one")
	}
	second, err := back.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("export is not stable across a round trip")
	}
	back.Code = "DEF-COPY"
	if issues := back.Validate(); len(issues) != 0 {
		t.Fatalf("re-imported copy invalid: %v", issues)
	}
}

func TestParse_RejectsUnsupportedSchemaAndUnknownFields(t *testing.T) {
	d := sampleDefinition(3)
	b, _ := d.Marshal()
	future := strings.Replace(string(b), `"schemaVersion": "1"`, `"schemaVersion": "2"`, 1)
	_, err := Parse([]byte(future))
	var de *Error
	if !errors.As(err, &de) || de.Code != CodeImportSchema {
		t.Fatalf("schema 2: err = %v, want %s", err, CodeImportSchema)
	}
	unknown := strings.Replace(string(b), `"code": "DEF-200X12"`, `"code": "DEF-200X12", "surprise": true`, 1)
	if _, err := Parse([]byte(unknown)); err == nil {
		t.Fatal("unknown field must be rejected")
	}
}
