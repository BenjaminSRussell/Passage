package store

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
	ledgerevents "github.com/BenjaminSRussell/passage/services/ledger/internal/events"
)

type fixtureLine struct {
	Account string `json:"account"`
	Side    string `json:"side"`
	Amount  string `json:"amount"`
}

type fixtureDocument struct {
	N           int           `json:"n"`
	PostingDate string        `json:"postingDate"`
	Target      string        `json:"target"`
	Lines       []fixtureLine `json:"lines"`
}

type fixtureExpectedRow struct {
	Account string `json:"account"`
	Debit   string `json:"debit"`
	Credit  string `json:"credit"`
	Net     string `json:"net"`
}

type fixtureExpected struct {
	Debit  string               `json:"debit"`
	Credit string               `json:"credit"`
	Net    string               `json:"net"`
	Rows   []fixtureExpectedRow `json:"rows"`
}

type ac124Fixture struct {
	Documents            []fixtureDocument          `json:"documents"`
	ExpectedTrialBalance map[string]fixtureExpected `json:"expectedTrialBalance"`
}

func loadAC124Fixture(t *testing.T) ac124Fixture {
	t.Helper()
	raw, err := os.ReadFile("testdata/ac124_fixture.json")
	if err != nil {
		t.Fatal(err)
	}
	var f ac124Fixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

func (d fixtureDocument) postInput(t *testing.T, ledgerID string) PostInput {
	t.Helper()
	date, err := time.Parse(time.DateOnly, d.PostingDate)
	if err != nil {
		t.Fatal(err)
	}
	key := fmt.Sprintf("AC124-%02d", d.N)
	hash, _ := domain.HashPayload(map[string]any{"key": key, "lines": d.Lines})
	lines := make([]domain.LineInput, len(d.Lines))
	for i, l := range d.Lines {
		lines[i] = domain.LineInput{Account: l.Account, Side: l.Side, Amount: decimal.RequireFromString(l.Amount), Currency: "USD"}
	}
	return PostInput{LedgerID: ledgerID, DocumentID: ids.New(), PostingDate: date, Description: "AC-12.4 document " + key,
		Source: ledgerevents.Source{System: "ap", Type: "vendor_invoice", ID: key, URI: "passage://ap/invoices/" + key},
		Lines:  lines, Key: key, Hash: hash, Actor: "test-user", TraceID: testTrace, Today: testToday}
}

func TestPostgres_AC124TwentyDocumentsKeepEveryLedgerBalancedWithExpectedBalances(t *testing.T) {
	f := loadAC124Fixture(t)
	if len(f.Documents) != 20 {
		t.Fatalf("fixture has %d documents, want 20", len(f.Documents))
	}
	e := setupLedgerSet(t)
	for ledgerCode, accounts := range map[string]map[string]Account{
		"STD": {"1000": {Name: "Cash", Type: "asset"}, "4000": {Name: "Revenue", Type: "revenue"}, "6020": {Name: "Rent expense", Type: "expense"}},
		"LOC": {"1000": {Name: "Cash", Type: "asset"}, "4000": {Name: "Revenue", Type: "revenue"}, "6020": {Name: "Rent expense", Type: "expense"},
			"7020": {Name: "Local rent expense", Type: "expense"}},
	} {
		for number, a := range accounts {
			a.Postable = true
			if _, _, err := e.store.PutAccount(e.ctx, ledgerCode, number, a); err != nil {
				t.Fatalf("put account %s/%s: %v", ledgerCode, number, err)
			}
		}
	}
	for _, m := range []AccountMapping{
		{LedgerID: e.local.ID, SourceAccount: "6020", TargetAccount: "7020", EffectiveFrom: "2026-02-01"},
		{LedgerID: e.tax.ID, SourceAccount: "6020", TargetAccount: "6010", EffectiveFrom: "2026-02-01"},
	} {
		if _, err := e.store.CreateAccountMapping(e.ctx, m); err != nil {
			t.Fatalf("mapping %s: %v", m.SourceAccount, err)
		}
	}
	ledgerOf := map[string]string{"GL": e.ledger.ID, "LOC": e.local.ID, "TAX": e.tax.ID}
	entries := 0
	for _, d := range f.Documents {
		if d.Target == "set" {
			out, err := e.store.PostSetEntry(e.ctx, e.set.ID, d.postInput(t, ""))
			if err != nil || len(out.Entries) != 3 {
				t.Fatalf("document %d through the set: %d entries, err %v", d.N, len(out.Entries), err)
			}
			entries += 3
			continue
		}
		in := d.postInput(t, ledgerOf[d.Target])
		in.CanPostLedgerSpecific = true
		if _, err := e.store.PostEntry(e.ctx, in); err != nil {
			t.Fatalf("document %d to %s only: %v", d.N, d.Target, err)
		}
		entries++
	}
	if got := e.entryCount(t); got != entries || entries != 5+15*3 {
		t.Fatalf("entries = %d (counted %d), want %d", got, entries, 5+15*3)
	}
	for code, want := range f.ExpectedTrialBalance {
		tb, err := e.store.TrialBalance(e.ctx, BalanceFilter{LedgerID: ledgerOf[code]})
		if err != nil {
			t.Fatalf("trial balance %s: %v", code, err)
		}
		if !tb.Balanced || tb.Net != "0.00" || tb.Debit != want.Debit || tb.Credit != want.Credit || tb.Net != want.Net {
			t.Errorf("%s totals = %s/%s/%s balanced %v; want %s/%s/%s", code, tb.Debit, tb.Credit, tb.Net, tb.Balanced, want.Debit, want.Credit, want.Net)
		}
		if len(tb.Rows) != len(want.Rows) {
			t.Fatalf("%s has %d account rows, want %d: %+v", code, len(tb.Rows), len(want.Rows), tb.Rows)
		}
		for i, row := range tb.Rows {
			w := want.Rows[i]
			if row.Account != w.Account || row.Debit != w.Debit || row.Credit != w.Credit || row.Net != w.Net {
				t.Errorf("%s account %s = %s/%s/%s; want %s %s/%s/%s", code, row.Account, row.Debit, row.Credit, row.Net, w.Account, w.Debit, w.Credit, w.Net)
			}
		}
	}
}

func TestPostgres_PostingToOneParallelLedgerNeedsTheLedgerSpecificDuty(t *testing.T) {
	e := setupLedgerSet(t)
	for name, tc := range map[string]struct {
		ledger  string
		granted bool
		denied  bool
	}{
		"parallel without duty":      {ledger: e.local.ID, denied: true},
		"parallel with duty":         {ledger: e.local.ID, granted: true},
		"second parallel no duty":    {ledger: e.tax.ID, denied: true},
		"leading without duty":       {ledger: e.ledger.ID},
		"leading with duty is no-op": {ledger: e.ledger.ID, granted: true},
	} {
		in := vendorInvoice(tc.ledger, "FB50L-"+name, "10.00")
		in.CanPostLedgerSpecific = tc.granted
		if tc.ledger == e.local.ID {
			in.Lines[0].Account = "7010"
		}
		_, err := e.store.PostEntry(e.ctx, in)
		if tc.denied {
			if !isProblem(err, http.StatusForbidden, "AUTH-0003") {
				t.Errorf("%s: want 403 AUTH-0003, got %v", name, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if n := e.entryCount(t); n != 3 {
		t.Fatalf("only the three permitted posts may land, got %d entries", n)
	}
}

func TestPostgres_LedgerNotInAnySetPostsWithoutTheLedgerSpecificDuty(t *testing.T) {
	e, _ := setup(t)
	if _, err := e.store.PostEntry(e.ctx, vendorInvoice(e.ledger.ID, "PLAIN-1", "10.00")); err != nil {
		t.Fatalf("a ledger outside any set needs no ledger-specific duty: %v", err)
	}
}
