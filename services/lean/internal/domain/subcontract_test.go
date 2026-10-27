package domain

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func subDec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func subDay(s string) time.Time {
	t, _ := time.Parse("2006-01-02", s)
	return t
}

// subCode returns the domain code of err, or "" when err is not a domain error.
func subCode(err error) string {
	var de *Error
	if errors.As(err, &de) {
		return de.Code
	}
	return ""
}

var vendorAgreement = []Agreement{{VendorID: "V", ItemID: "svc-plating", UnitPrice: subDec("4.50"), Currency: "USD",
	ValidFrom: subDay("2027-01-01")}}

// AC-24.17: a job of 20 units on vendor V in po-per-kanban mode gives one line of
// 20 units at the agreement price, amount 90.00.
func TestSubcontract_AC2417PerKanban(t *testing.T) {
	lines, err := SubcontractLines(ModePOPerKanban, vendorAgreement,
		[]ServiceJob{{JobID: "job-1", VendorID: "V", ItemID: "svc-plating", Qty: subDec("20"), CompletedOn: subDay("2027-03-10")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 || !lines[0].Qty.Equal(subDec("20")) || !lines[0].UnitPrice.Equal(subDec("4.50")) ||
		lines[0].Amount.StringFixed(2) != "90.00" || lines[0].JobIDs[0] != "job-1" {
		t.Fatalf("lines = %+v", lines)
	}
}

func TestSubcontract_PeriodicAccumulates(t *testing.T) {
	jobs := []ServiceJob{
		{JobID: "j1", VendorID: "V", ItemID: "svc-plating", Qty: subDec("20"), CompletedOn: subDay("2027-03-10")},
		{JobID: "j2", VendorID: "V", ItemID: "svc-plating", Qty: subDec("5"), CompletedOn: subDay("2027-03-20")},
	}
	lines, err := SubcontractLines(ModePeriodicPO, vendorAgreement, jobs)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 || !lines[0].Qty.Equal(subDec("25")) || len(lines[0].JobIDs) != 2 ||
		!lines[0].PriceDate.Equal(subDay("2027-03-20")) || lines[0].Amount.StringFixed(2) != "112.50" {
		t.Fatalf("periodic = %+v", lines)
	}
}

func TestSubcontract_Refusals(t *testing.T) {
	job := ServiceJob{JobID: "j", VendorID: "V", ItemID: "svc-plating", Qty: subDec("1"), CompletedOn: subDay("2026-12-31")}
	if _, err := SubcontractLines(ModePOPerKanban, vendorAgreement, []ServiceJob{job}); subCode(err) != CodeNoAgreement {
		t.Fatalf("agreement not yet valid must give LEAN-0005: %v", err)
	}
	job.CompletedOn = subDay("2027-01-05")
	job.Qty = subDec("0")
	if _, err := SubcontractLines(ModePOPerKanban, vendorAgreement, []ServiceJob{job}); subCode(err) != CodeInvalid {
		t.Fatalf("zero quantity: %v", err)
	}
	if _, err := SubcontractLines("weekly", vendorAgreement, nil); subCode(err) != CodeInvalid {
		t.Fatalf("unknown mode: %v", err)
	}
}

func TestAgreement_ValidTo(t *testing.T) {
	a := Agreement{VendorID: "V", ItemID: "s", UnitPrice: subDec("1"), Currency: "USD",
		ValidFrom: subDay("2027-01-01"), ValidTo: subDay("2027-01-31")}
	if !a.covers(subDay("2027-01-31")) || a.covers(subDay("2027-02-01")) {
		t.Fatal("ValidTo is inclusive")
	}
	// A job completed in the afternoon of the last valid day is still priced.
	if !a.covers(subDay("2027-01-31").Add(15 * time.Hour)) {
		t.Fatal("ValidTo covers the whole day")
	}
}

// BR-24.16: the price is the agreement valid on the completion date; a newer
// overlapping agreement supersedes the older one.
func TestSubcontract_PriceByCompletionDate(t *testing.T) {
	ags := []Agreement{
		{VendorID: "V", ItemID: "s", UnitPrice: subDec("4.00"), Currency: "USD", ValidFrom: subDay("2027-01-01")},
		{VendorID: "V", ItemID: "s", UnitPrice: subDec("5.00"), Currency: "USD", ValidFrom: subDay("2027-03-01"), ValidTo: subDay("2027-03-31")},
	}
	lines, err := SubcontractLines(ModePeriodicPO, ags, []ServiceJob{
		{JobID: "feb", VendorID: "V", ItemID: "s", Qty: subDec("10"), CompletedOn: subDay("2027-02-15")},
		{JobID: "mar", VendorID: "V", ItemID: "s", Qty: subDec("10"), CompletedOn: subDay("2027-03-15")},
		{JobID: "apr", VendorID: "V", ItemID: "s", Qty: subDec("10"), CompletedOn: subDay("2027-04-15")},
	})
	if err != nil {
		t.Fatal(err)
	}
	// February and April at 4.00 accumulate; March at 5.00 is its own line.
	if len(lines) != 2 {
		t.Fatalf("lines = %+v", lines)
	}
	byPrice := map[string]POLine{}
	for _, l := range lines {
		byPrice[l.UnitPrice.StringFixed(2)] = l
	}
	if l := byPrice["4.00"]; !l.Qty.Equal(subDec("20")) || l.Amount.StringFixed(2) != "80.00" {
		t.Fatalf("4.00 line = %+v", l)
	}
	if l := byPrice["5.00"]; !l.Qty.Equal(subDec("10")) || l.JobIDs[0] != "mar" {
		t.Fatalf("5.00 line = %+v", l)
	}
}

// Amounts round to the currency's minor unit (platform money): JPY has none.
func TestSubcontract_AmountUsesCurrencyMinorUnit(t *testing.T) {
	ags := []Agreement{{VendorID: "V", ItemID: "s", UnitPrice: subDec("12.5"), Currency: "JPY", ValidFrom: subDay("2027-01-01")}}
	lines, err := SubcontractLines(ModePOPerKanban, ags, []ServiceJob{{JobID: "j", VendorID: "V", ItemID: "s", Qty: subDec("3"), CompletedOn: subDay("2027-01-02")}})
	if err != nil || lines[0].Amount.String() != "38" {
		t.Fatalf("lines = %+v, %v", lines, err)
	}
}

func TestSubcontract_RejectsBadInput(t *testing.T) {
	job := ServiceJob{JobID: "j", VendorID: "V", ItemID: "svc-plating", Qty: subDec("1"), CompletedOn: subDay("2027-01-05")}
	if _, err := SubcontractLines(ModePOPerKanban, vendorAgreement, []ServiceJob{job, job}); subCode(err) != CodeInvalid {
		t.Fatalf("duplicate job: %v", err)
	}
	bad := []Agreement{{VendorID: "V", ItemID: "svc-plating", UnitPrice: subDec("-1"), Currency: "USD", ValidFrom: subDay("2027-01-01")}}
	if _, err := SubcontractLines(ModePOPerKanban, bad, []ServiceJob{job}); subCode(err) != CodeInvalid {
		t.Fatalf("negative price: %v", err)
	}
	backwards := []Agreement{{VendorID: "V", ItemID: "svc-plating", UnitPrice: subDec("1"), Currency: "USD", ValidFrom: subDay("2027-02-01"), ValidTo: subDay("2027-01-01")}}
	if _, err := SubcontractLines(ModePOPerKanban, backwards, []ServiceJob{job}); subCode(err) != CodeInvalid {
		t.Fatalf("backwards validity: %v", err)
	}
}

// Property: periodic-po preserves the total completed quantity and the sum of
// its lines equals the sum of the per-job amounts.
func TestSubcontract_PeriodicConservesQty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 12).Draw(t, "n")
		var jobs []ServiceJob
		total := decimal.Zero
		perJob := decimal.Zero
		for i := 0; i < n; i++ {
			q := decimal.NewFromInt(rapid.Int64Range(1, 100).Draw(t, "q"))
			jobs = append(jobs, ServiceJob{JobID: fmt.Sprintf("j%d", i), VendorID: "V", ItemID: "svc-plating", Qty: q, CompletedOn: subDay("2027-03-01")})
			total = total.Add(q)
			perJob = perJob.Add(q.Mul(subDec("4.50")))
		}
		lines, err := SubcontractLines(ModePeriodicPO, vendorAgreement, jobs)
		if err != nil {
			t.Fatal(err)
		}
		qty, amt := decimal.Zero, decimal.Zero
		for _, l := range lines {
			qty = qty.Add(l.Qty)
			amt = amt.Add(l.Amount)
		}
		if !qty.Equal(total) || !amt.Equal(perJob) {
			t.Fatalf("qty %s vs %s, amount %s vs %s", qty, total, amt, perJob)
		}
	})
}
