package domain

import (
	"encoding/csv"
	"errors"
	"os"
	"testing"

	"github.com/shopspring/decimal"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// TestComputePOC_MatchesWorkbookFixture replays the PRD worked example (BR-41.1,
// AC-41.1) and the loss case (BR-41.2) from testdata/poc-worked-example.csv.
// The workbook named in the test plan is not in the repo yet; see NOT-IMPLEMENTED.md.
func TestComputePOC_MatchesWorkbookFixture(t *testing.T) {
	f, err := os.Open("../../testdata/poc-worked-example.csv")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows[1:] {
		in := POCInput{
			Currency:            "USD",
			ContractValue:       d(row[1]),
			CostToDate:          d(row[2]),
			EstimatedToComplete: d(row[3]),
			PriorRevenueToDate:  d(row[4]),
			BilledToDate:        d(row[5]),
		}
		got, err := ComputePOC(in)
		if err != nil {
			t.Fatalf("period %s: %v", row[0], err)
		}
		checks := []struct {
			name string
			got  decimal.Decimal
			want string
		}{
			{"revenue to date", got.RevenueToDate, row[6]},
			{"revenue this period", got.RevenueThisPeriod, row[7]},
			{"accrued revenue", got.AccruedRevenue, row[8]},
			{"deferred revenue", got.DeferredRevenue, row[9]},
			{"loss provision", got.LossProvision, row[10]},
		}
		for _, c := range checks {
			if !c.got.Equal(d(c.want)) {
				t.Errorf("period %s %s = %s, want %s", row[0], c.name, c.got, c.want)
			}
		}
	}
}

func TestComputePOC_WorkedExamplePoc(t *testing.T) {
	got, err := ComputePOC(POCInput{Currency: "USD", ContractValue: d("500000"), CostToDate: d("260000"), EstimatedToComplete: d("190000")})
	if err != nil {
		t.Fatal(err)
	}
	if !got.POC.Equal(d("0.5778")) || !got.EAC.Equal(d("450000")) {
		t.Fatalf("poc = %s eac = %s, want 0.5778 and 450000", got.POC, got.EAC)
	}
	if !got.GrossProfitToDate.Equal(d("28888.89")) {
		t.Fatalf("gross profit = %s, want 28888.89", got.GrossProfitToDate)
	}
}

func TestComputePOC_NoCostNoProgress(t *testing.T) {
	got, err := ComputePOC(POCInput{Currency: "EUR", ContractValue: d("1000"), EstimatedToComplete: d("0")})
	if err != nil {
		t.Fatal(err)
	}
	if !got.RevenueToDate.IsZero() || !got.POC.IsZero() {
		t.Fatalf("revenue %s poc %s, want zero", got.RevenueToDate, got.POC)
	}
}

func TestComputePOC_RejectsBadInput(t *testing.T) {
	cases := []POCInput{
		{Currency: "US", ContractValue: d("1")},
		{Currency: "USD", ContractValue: d("-1")},
		{Currency: "USD", CostToDate: d("-5")},
	}
	for _, in := range cases {
		if _, err := ComputePOC(in); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("ComputePOC(%+v) error = %v, want ErrInvalidInput", in, err)
		}
	}
}

func TestProvisionMovement_ReleasesNeverBelowZero(t *testing.T) {
	cases := []struct {
		prior, required, want string
	}{
		{"0", "20000", "20000"},
		{"20000", "20000", "0"},
		{"20000", "5000", "-15000"},
		{"20000", "0", "-20000"},
		{"0", "0", "0"},
	}
	for _, c := range cases {
		if got := ProvisionMovement(d(c.prior), d(c.required)); !got.Equal(d(c.want)) {
			t.Errorf("movement(%s→%s) = %s, want %s", c.prior, c.required, got, c.want)
		}
	}
}

func TestComputeEVM_AC841(t *testing.T) {
	got, err := ComputeEVM(EVMInput{Currency: "USD", BAC: d("400000"), PV: d("100000"), AC: d("120000"), POC: d("0.30")})
	if err != nil {
		t.Fatal(err)
	}
	if !got.EV.Equal(d("120000")) {
		t.Fatalf("EV = %s, want 120000", got.EV)
	}
	if got.CPI == nil || !got.CPI.Equal(d("1")) {
		t.Fatalf("CPI = %v, want 1.00", got.CPI)
	}
	if got.SPI == nil || !got.SPI.Equal(d("1.2")) {
		t.Fatalf("SPI = %v, want 1.20", got.SPI)
	}
	if got.EAC == nil || !got.EAC.Equal(d("400000")) || got.VAC == nil || !got.VAC.IsZero() {
		t.Fatalf("EAC = %v VAC = %v, want 400000 and 0", got.EAC, got.VAC)
	}
}

func TestComputeEVM_UndefinedIndicesAreNil(t *testing.T) {
	got, err := ComputeEVM(EVMInput{Currency: "USD", BAC: d("1000"), PV: d("0"), AC: d("0"), POC: d("0.5")})
	if err != nil {
		t.Fatal(err)
	}
	if got.CPI != nil || got.SPI != nil || got.EAC != nil || got.VAC != nil {
		t.Fatalf("indices must be nil when the denominator is zero: %+v", got)
	}
}

func TestComputeEVM_RejectsPOCOutOfRange(t *testing.T) {
	for _, poc := range []string{"-0.1", "1.01"} {
		if _, err := ComputeEVM(EVMInput{Currency: "USD", BAC: d("1"), POC: d(poc)}); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("poc %s: error = %v, want ErrInvalidInput", poc, err)
		}
	}
}
