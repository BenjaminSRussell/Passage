package domain

import (
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

const (
	cust1 = "11111111-1111-4111-8111-111111111111"
	cust2 = "22222222-2222-4222-8222-222222222222"
	leA   = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
)

func rec(id, party string, amount string) Record {
	return Record{LegacyID: id, Party: party, Account: "1100", Currency: "USD", Amount: decimal.RequireFromString(amount)}
}

func extra(date, due string) TargetExtra { return TargetExtra{DocumentDate: date, DueDate: due} }

func TestStageRows_BuildsReceivablesRowsAndHoldsBackTheRest(t *testing.T) {
	cfg := TargetConfig{LegalEntityID: leA, ImportSource: "ax2012.usmf", Customers: map[string]string{"C-100": cust2}}
	accepted := []Record{
		rec("INV-1", "C-100", "125.50"), // mapped party, debit
		rec("CN-1", cust1, "-20.00"),    // party is itself a UUID, credit
		rec("AP-1", "V-200", "300.00"),  // open AP
		rec("INV-2", "C-404", "10.00"),  // no customer
		rec("INV-3", "C-100", "10.00"),  // no dates
		rec("PAY-1", "C-100", "-5.00"),  // explicit kind
	}
	extras := map[string]TargetExtra{
		"INV-1": extra("2026-01-05", "2026-02-04"),
		"CN-1":  extra("2026-01-06", "2026-01-06"),
		"AP-1":  {Ledger: LedgerAP, DocumentDate: "2026-01-01", DueDate: "2026-02-01"},
		"INV-2": extra("2026-01-05", "2026-02-04"),
		"INV-3": {},
		"PAY-1": {Kind: "PAYMENT", DocumentDate: "2026-01-07", DueDate: "2026-01-07"},
	}
	rows := StageRows(accepted, extras, cfg)
	if len(rows) != 6 {
		t.Fatalf("staged %d rows", len(rows))
	}
	inv := rows[0]
	if inv.Status != RowPending || inv.Row == nil || inv.Row.Side != "DEBIT" || inv.Row.Kind != "INVOICE" || inv.Row.CustomerID != cust2 ||
		!inv.Row.OpenAmount.Equal(decimal.RequireFromString("125.5")) || !inv.Row.OriginalAmount.Equal(inv.Row.OpenAmount) || inv.Row.DocumentNumber != "INV-1" || inv.Hash == "" {
		t.Fatalf("invoice row %+v", inv)
	}
	if cn := rows[1]; cn.Row == nil || cn.Row.Side != "CREDIT" || cn.Row.Kind != "CREDIT_NOTE" || !cn.Row.OpenAmount.Equal(decimal.NewFromInt(20)) || cn.Row.CustomerID != cust1 {
		t.Fatalf("credit row %+v", cn)
	}
	if ap := rows[2]; ap.Status != RowNotAvailable || ap.Code != CodeTargetNotAvailable || ap.Row != nil {
		t.Fatalf("AP row %+v", ap)
	}
	if r := rows[3]; r.Status != RowRejected || r.Code != CodeRefUnresolved || !strings.Contains(r.Detail, "C-404") {
		t.Fatalf("unmapped party %+v", r)
	}
	if r := rows[4]; r.Status != RowRejected || r.Code != CodeFieldRequired {
		t.Fatalf("missing dates %+v", r)
	}
	if r := rows[5]; r.Row == nil || r.Row.Kind != "PAYMENT" || r.Row.Side != "CREDIT" {
		t.Fatalf("payment row %+v", r)
	}
	for i, r := range rows {
		if r.Seq != i+1 {
			t.Fatalf("row %d has seq %d", i, r.Seq)
		}
	}
	if got := rows[1].Row.SignedAmount(); !got.Equal(decimal.NewFromInt(-20)) {
		t.Fatalf("signed amount %s", got)
	}
}

func TestRowHash_ChangesWithAnyField(t *testing.T) {
	cfg := TargetConfig{LegalEntityID: leA, ImportSource: "s", Customers: map[string]string{"C": cust1}}
	base := StageRows([]Record{rec("A", "C", "10")}, map[string]TargetExtra{"A": extra("2026-01-01", "2026-01-31")}, cfg)[0]
	same := StageRows([]Record{rec("A", "C", "10.000")}, map[string]TargetExtra{"A": extra("2026-01-01", "2026-01-31")}, cfg)[0]
	if base.Hash != same.Hash {
		t.Fatal("10 and 10.000 are the same row")
	}
	for name, mutate := range map[string]func() StagedRow{
		"amount": func() StagedRow {
			return StageRows([]Record{rec("A", "C", "10.01")}, map[string]TargetExtra{"A": extra("2026-01-01", "2026-01-31")}, cfg)[0]
		},
		"due": func() StagedRow {
			return StageRows([]Record{rec("A", "C", "10")}, map[string]TargetExtra{"A": extra("2026-01-01", "2026-02-01")}, cfg)[0]
		},
		"source": func() StagedRow {
			c := cfg
			c.ImportSource = "t"
			return StageRows([]Record{rec("A", "C", "10")}, map[string]TargetExtra{"A": extra("2026-01-01", "2026-01-31")}, c)[0]
		},
		"entity": func() StagedRow {
			c := cfg
			c.LegalEntityID = cust1
			return StageRows([]Record{rec("A", "C", "10")}, map[string]TargetExtra{"A": extra("2026-01-01", "2026-01-31")}, c)[0]
		},
	} {
		if mutate().Hash == base.Hash {
			t.Errorf("changing the %s did not change the hash", name)
		}
	}
}

func TestImportSource(t *testing.T) {
	if got := DefaultImportSource(SourceAX2012, "USMF"); got != "ax2012.usmf" {
		t.Fatalf("default import source %q", got)
	}
	if got := DefaultImportSource(SourceSAPECC, "1000 / DE"); ValidateImportSource(got) != nil {
		t.Fatalf("default %q is not a valid import source", got)
	}
	if ValidateImportSource(strings.Repeat("a", 41)) == nil || ValidateImportSource("") == nil || ValidateImportSource("-x") == nil || ValidateImportSource("a b") == nil {
		t.Fatal("invalid import sources accepted")
	}
	if ValidateImportSource(strings.Repeat("a", 40)) != nil {
		t.Fatal("40 characters is allowed")
	}
}

func totals(cur string, n int, debit, credit string) ImportTotals {
	d, c := decimal.RequireFromString(debit), decimal.RequireFromString(credit)
	return ImportTotals{Currency: cur, ItemCount: n, DebitOpen: d, CreditOpen: c, NetOpen: d.Add(c)}
}

func TestReconcileTarget(t *testing.T) {
	exp := []ImportTotals{totals("USD", 3, "100.50", "-20"), totals("EUR", 1, "5", "0")}
	got, err := ReconcileTarget("s", leA, exp, []ImportTotals{totals("USD", 3, "100.50", "-20"), totals("EUR", 1, "5", "0")})
	if err != nil || len(got) != 8 {
		t.Fatalf("%d totals, %v", len(got), err)
	}
	if _, mismatch := CountTotals(got); mismatch != 0 {
		t.Fatalf("equal totals mismatched: %+v", got)
	}
	got, _ = ReconcileTarget("s", leA, exp, []ImportTotals{totals("USD", 3, "100.49", "-20"), totals("EUR", 1, "5", "0")})
	_, mismatch := CountTotals(got)
	if mismatch != 2 { // debit and net differ
		t.Fatalf("a cent off should break debit and net, got %d mismatches", mismatch)
	}
	// A currency only the target holds, and one only the factory holds, both compare against zero.
	got, _ = ReconcileTarget("s", leA, exp[:1], []ImportTotals{totals("USD", 3, "100.50", "-20"), totals("GBP", 1, "9", "0")})
	if _, mismatch := CountTotals(got); mismatch != 3 { // GBP: count, debit, net
		t.Fatalf("unexpected currency should mismatch, got %d", mismatch)
	}
	got, _ = ReconcileTarget("s", leA, exp, nil)
	if _, mismatch := CountTotals(got); mismatch != 7 { // credit of EUR is 0 on both sides
		t.Fatalf("an empty target should mismatch, got %d", mismatch)
	}
	for _, c := range got {
		if !IsTargetTotal(c.Name) || c.Key["importSource"] != "s" || c.Key["legalEntityId"] != leA {
			t.Fatalf("control total %+v", c)
		}
	}
}

func TestTargetLoadClean(t *testing.T) {
	if !(TargetLoad{Status: LoadReconciled}).Clean() {
		t.Fatal("reconciled with nothing refused is clean")
	}
	for _, l := range []TargetLoad{{Status: LoadReconciled, Rejected: 1}, {Status: LoadReconciled, NotAvailable: 1}, {Status: LoadMismatch}, {Status: LoadPartial, Pending: 2}} {
		if l.Clean() {
			t.Fatalf("%+v is not clean", l)
		}
	}
}
