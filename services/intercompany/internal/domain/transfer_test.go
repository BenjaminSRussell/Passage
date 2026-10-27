package domain

import (
	"errors"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/money"
)

func evidence(rows ...EvidenceRow) EvidenceFile {
	return EvidenceFile{SellerEntity: "P", BuyerEntity: "S", Period: "2026-11", Rows: rows}
}

// TestEvidenceCSV_AC4011Charge exports the AC-40.11 charge (8 h cost-plus 10 %:
// price 528.00, cost 480.00) and a zero-priced line.
func TestEvidenceCSV_AC4011Charge(t *testing.T) {
	out, err := EvidenceCSV(evidence(
		EvidenceRow{Item: "CONSULT", Quantity: d("8"), Price: money.Must("528", "EUR"), Cost: money.Must("480", "EUR"), Basis: "CostPlus"},
		EvidenceRow{Item: "SAMPLE", Quantity: d("2"), Price: money.Must("0", "JPY"), Cost: money.Must("150", "JPY"), Basis: "Cost"},
	))
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"seller_entity,buyer_entity,period,item,quantity,currency,price,cost,margin,margin_pct,basis",
		"P,S,2026-11,CONSULT,8,EUR,528.00,480.00,48.00,9.09,CostPlus",
		"P,S,2026-11,SAMPLE,2,JPY,0,150,-150,0.00,Cost",
		"",
	}, "\n")
	if out != want {
		t.Fatalf("csv =\n%s\nwant\n%s", out, want)
	}
}

func TestEvidenceCSV_NeutralisesFormulas(t *testing.T) {
	out, err := EvidenceCSV(evidence(EvidenceRow{Item: "=HYPERLINK(\"x\")", Quantity: d("1"), Price: money.Must("1", "USD"), Cost: money.Must("1", "USD"), Basis: "+Cost"}))
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Split(out, "\n")[1]
	if !strings.Contains(line, `"'=HYPERLINK(""x"")"`) || !strings.HasSuffix(line, ",'+Cost") {
		t.Fatalf("formula cells not neutralised: %s", line)
	}
}

func TestEvidenceCSV_Rejections(t *testing.T) {
	good := EvidenceRow{Item: "X", Quantity: d("1"), Price: money.Must("1", "USD"), Cost: money.Must("1", "USD"), Basis: "Cost"}
	cases := map[string]func(*EvidenceFile){
		"no item":          func(f *EvidenceFile) { f.Rows[0].Item = " " },
		"no basis":         func(f *EvidenceFile) { f.Rows[0].Basis = "" },
		"zero quantity":    func(f *EvidenceFile) { f.Rows[0].Quantity = d("0") },
		"negative price":   func(f *EvidenceFile) { f.Rows[0].Price = money.Must("-1", "USD") },
		"mixed currencies": func(f *EvidenceFile) { f.Rows[0].Cost = money.Must("1", "EUR") },
		"same entity":      func(f *EvidenceFile) { f.BuyerEntity = "P" },
		"no seller":        func(f *EvidenceFile) { f.SellerEntity = "" },
		"bad period":       func(f *EvidenceFile) { f.Period = "2026-13" },
		"no rows":          func(f *EvidenceFile) { f.Rows = nil },
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			f := evidence(good)
			edit(&f)
			_, err := EvidenceCSV(f)
			var de *Error
			if !errors.As(err, &de) || de.Code != CodeInvalid {
				t.Fatalf("err = %v; want COM-0001", err)
			}
		})
	}
}
