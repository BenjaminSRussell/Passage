package domain

import (
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

const (
	vendA = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	leID  = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
)

func apRecord(id, amount, cur string) (Record, TargetExtra) {
	rec := Record{LegacyID: id, Ledger: LedgerAP, Party: "V001", Account: "2100", Currency: cur, Amount: decimal.RequireFromString(amount)}
	return rec, TargetExtra{Ledger: LedgerAP, DocumentDate: "2026-01-01", DueDate: "2026-02-01", DocumentNumber: "INV-" + id}
}

func TestStageRows_ApStagedForPayablesWhenConfigured(t *testing.T) {
	cfg := TargetConfig{LegalEntityID: leID, ImportSource: "ax2012.usmf", Payables: &PayablesConfig{Vendors: map[string]string{"V001": strings.ToUpper(vendA)}}}
	good, gx := apRecord("AP-1", "-300.50", "USD")
	jpy, jx := apRecord("AP-2", "-1200", "JPY")
	debit, dx := apRecord("AP-3", "10.00", "USD")
	frac, fx := apRecord("AP-4", "-10.005", "USD")
	nodate, nx := apRecord("AP-5", "-1", "USD")
	nx.DueDate = ""
	other, ox := apRecord("AP-6", "-5", "USD")
	other.Party = "V404"
	rows := StageRows([]Record{good, jpy, debit, frac, nodate, other}, map[string]TargetExtra{
		OpenItemKey(LedgerAP, "AP-1"): gx, OpenItemKey(LedgerAP, "AP-2"): jx, OpenItemKey(LedgerAP, "AP-3"): dx,
		OpenItemKey(LedgerAP, "AP-4"): fx, OpenItemKey(LedgerAP, "AP-5"): nx, OpenItemKey(LedgerAP, "AP-6"): ox}, cfg)
	want := []struct{ status, code string }{
		{RowPending, ""}, {RowPending, ""}, {RowRejected, CodeAPDebitBalance}, {RowRejected, CodeAPPrecision}, {RowRejected, CodeFieldRequired}, {RowRejected, CodeRefUnresolved},
	}
	for i, w := range want {
		if rows[i].Status != w.status || rows[i].Code != w.code {
			t.Fatalf("row %d = %+v, want %v", i, rows[i], w)
		}
	}
	if ap := rows[0].AP; ap == nil || ap.AmountMinor != 30050 || ap.VendorID != vendA || ap.InvoiceNumber != "INV-AP-1" || ap.Reference != "AP-1" || rows[0].Hash == "" {
		t.Fatalf("good row %+v", rows[0].AP)
	}
	if ap := rows[1].AP; ap == nil || ap.AmountMinor != 1200 {
		t.Fatalf("JPY has no minor unit: %+v", rows[1].AP)
	}
}

func TestStageRows_ApStaysNotAvailableWithoutPayablesConfig(t *testing.T) {
	rec, x := apRecord("AP-1", "-300.00", "USD")
	rows := StageRows([]Record{rec}, map[string]TargetExtra{OpenItemKey(LedgerAP, "AP-1"): x}, TargetConfig{LegalEntityID: leID, ImportSource: "ax2012.usmf"})
	if rows[0].Status != RowNotAvailable || rows[0].AP != nil {
		t.Fatalf("row %+v", rows[0])
	}
}

func TestPayablesBatchKey(t *testing.T) {
	k := PayablesBatchKey("ax2012.usmf", []string{"h1", "h2"})
	if !IsPayablesBatchKey("ax2012.usmf", k) || len(k) > 100 {
		t.Fatalf("key %q", k)
	}
	if k != PayablesBatchKey("ax2012.usmf", []string{"h1", "h2"}) || k == PayablesBatchKey("ax2012.usmf", []string{"h1", "h3"}) {
		t.Fatal("the key must follow the rows")
	}
	// A source whose name the other's prefixes must not be read as its batches.
	if IsPayablesBatchKey("ax2012", k) || IsPayablesBatchKey("ax2012.usmf-x", k) {
		t.Fatalf("key %q matched another import source", k)
	}
	long := PayablesBatchKey(strings.Repeat("a", MaxImportSource), []string{"h"})
	if len(long) > 100 {
		t.Fatalf("a 40-character import source overflows payables' 100-character batch key: %d", len(long))
	}
}

func TestReconcilePayables(t *testing.T) {
	exp := PayablesTotals{Items: 3, TotalMinor: map[string]int64{"USD": 500, "EUR": 70}}
	got, err := ReconcilePayables("s", leID, exp, PayablesTotals{Items: 3, TotalMinor: map[string]int64{"USD": 500, "EUR": 70}})
	if err != nil {
		t.Fatal(err)
	}
	if match, mismatch := CountTotals(got); match != 3 || mismatch != 0 {
		t.Fatalf("%d match %d mismatch: %+v", match, mismatch, got)
	}
	got, _ = ReconcilePayables("s", leID, exp, PayablesTotals{Items: 2, TotalMinor: map[string]int64{"USD": 499, "GBP": 5}})
	// item count, USD, EUR (missing at payables), GBP (extra at payables) all differ.
	if match, mismatch := CountTotals(got); match != 0 || mismatch != 4 {
		t.Fatalf("%d match %d mismatch: %+v", match, mismatch, got)
	}
}
