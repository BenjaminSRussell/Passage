package store

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
)

func ledgerLine(account, side, amount string) domain.LineInput {
	return domain.LineInput{Account: account, Side: side, Amount: decimal.RequireFromString(amount), Currency: "USD"}
}

func TestPostgres_LedgerLinesUnbalancedLedgerIsNamedAndNothingIsWritten(t *testing.T) {
	e := setupLedgerSet(t)
	for name, tc := range map[string]struct {
		ledger  string
		account string
		code    string
	}{
		"leading": {ledger: e.ledger.ID, account: "6010", code: "GL"},
		"local":   {ledger: e.local.ID, account: "7010", code: "HGB"},
		"tax":     {ledger: e.tax.ID, account: "6010", code: "TAX"},
	} {
		in := vendorInvoice("", "LL-UNBAL-"+name, "100.00")
		in.CanPostLedgerSpecific = true
		in.LedgerLines = map[string][]domain.LineInput{tc.ledger: {ledgerLine(tc.account, domain.SideDebit, "5.00")}}
		_, err := e.store.PostSetEntry(e.ctx, e.set.ID, in)
		var rej *domain.RejectedError
		if !errors.As(err, &rej) || rej.Violations[0].Code != "LEDGER_UNBALANCED" ||
			!strings.HasPrefix(rej.Violations[0].Message, "ledger "+tc.code+": entry does not balance in USD: debits 105.00, credits 100.00") {
			t.Errorf("%s: want LEDGER_UNBALANCED naming %s, got %v", name, tc.code, err)
		}
		if n := e.entryCount(t); n != 0 {
			t.Fatalf("%s: a rejected set post must write nothing, got %d entries", name, n)
		}
	}
}

func TestPostgres_LedgerLinesCommonLinesNeedNotBalanceUntilTheLedgerLinesAreAdded(t *testing.T) {
	e := setupLedgerSet(t)
	in := vendorInvoice("", "LL-ONESIDED", "100.00")
	in.Lines = in.Lines[:1]
	in.CanPostLedgerSpecific = true
	in.LedgerLines = map[string][]domain.LineInput{
		e.ledger.ID: {ledgerLine("2100", domain.SideCredit, "100.00")},
		e.local.ID:  {ledgerLine("2100", domain.SideCredit, "60.00"), ledgerLine("2100", domain.SideCredit, "40.00")},
	}
	_, err := e.store.PostSetEntry(e.ctx, e.set.ID, in)
	var rej *domain.RejectedError
	if !errors.As(err, &rej) || !strings.HasPrefix(rej.Violations[0].Message, "ledger TAX: entry does not balance in USD: debits 100.00, credits 0.00") {
		t.Fatalf("TAX has no ledger lines and must be the unbalanced ledger: %v", err)
	}
	in.LedgerLines[e.tax.ID] = []domain.LineInput{ledgerLine("2100", domain.SideCredit, "100.00")}
	out, err := e.store.PostSetEntry(e.ctx, e.set.ID, in)
	if err != nil || len(out.Entries) != 3 {
		t.Fatalf("post: %d entries, %v", len(out.Entries), err)
	}
	for _, entry := range out.Entries {
		wantLines := 2
		if entry.LedgerID == e.local.ID {
			wantLines = 3
		}
		if len(entry.Lines) != wantLines {
			t.Errorf("ledger %s has %d lines, want %d", entry.LedgerID, len(entry.Lines), wantLines)
		}
	}
}

func TestPostgres_LedgerLinesForAParallelLedgerNeedTheLedgerSpecificDuty(t *testing.T) {
	e := setupLedgerSet(t)
	leadingOnly := vendorInvoice("", "LL-DUTY-GL", "100.00")
	leadingOnly.LedgerLines = map[string][]domain.LineInput{e.ledger.ID: {ledgerLine("6010", domain.SideDebit, "10.00"), ledgerLine("2100", domain.SideCredit, "10.00")}}
	if _, err := e.store.PostSetEntry(e.ctx, e.set.ID, leadingOnly); err != nil {
		t.Fatalf("a set post with leading-ledger lines needs no ledger-specific duty: %v", err)
	}
	if n := e.entryCount(t); n != 3 {
		t.Fatalf("want 3 entries, got %d", n)
	}
	parallel := vendorInvoice("", "LL-DUTY-HGB", "100.00")
	parallel.LedgerLines = map[string][]domain.LineInput{e.local.ID: {ledgerLine("7010", domain.SideDebit, "10.00"), ledgerLine("2100", domain.SideCredit, "10.00")}}
	if _, err := e.store.PostSetEntry(e.ctx, e.set.ID, parallel); !isProblem(err, http.StatusForbidden, "AUTH-0003") {
		t.Fatalf("parallel ledger lines without the duty: want 403 AUTH-0003, got %v", err)
	}
	if n := e.entryCount(t); n != 3 {
		t.Fatalf("a denied set post must write nothing, got %d entries", n)
	}
	parallel.CanPostLedgerSpecific = true
	if _, err := e.store.PostSetEntry(e.ctx, e.set.ID, parallel); err != nil {
		t.Fatalf("parallel ledger lines with the duty: %v", err)
	}
	if n := e.entryCount(t); n != 6 {
		t.Fatalf("want 6 entries, got %d", n)
	}
}

func TestPostgres_LedgerLinesRefuseALedgerOutsideTheSet(t *testing.T) {
	e := setupLedgerSet(t)
	for name, id := range map[string]string{"unknown ledger": ids.New(), "not a uuid": "nope"} {
		in := vendorInvoice("", "LL-OUT-"+name, "100.00")
		in.CanPostLedgerSpecific = true
		in.LedgerLines = map[string][]domain.LineInput{id: {ledgerLine("6010", domain.SideDebit, "1.00")}}
		_, err := e.store.PostSetEntry(e.ctx, e.set.ID, in)
		var rej *domain.RejectedError
		if !errors.As(err, &rej) || rej.Violations[0].Code != CodeLedgerLinesNotInSet {
			t.Errorf("%s: want %s, got %v", name, CodeLedgerLinesNotInSet, err)
		}
	}
	if n := e.entryCount(t); n != 0 {
		t.Fatalf("got %d entries", n)
	}
}

func TestPostgres_LedgerLinesPostIsIdempotentAndRespectsLegalEntityScope(t *testing.T) {
	e := setupLedgerSet(t)
	in := vendorInvoice("", "LL-IDEM", "100.00")
	in.CanPostLedgerSpecific = true
	in.LedgerLines = map[string][]domain.LineInput{e.tax.ID: {ledgerLine("6010", domain.SideDebit, "5.00"), ledgerLine("2100", domain.SideCredit, "5.00")}}
	first, err := e.store.PostSetEntry(e.ctx, e.set.ID, in)
	if err != nil || first.Idempotency.Replayed {
		t.Fatalf("first: %+v %v", first, err)
	}
	replay, err := e.store.PostSetEntry(e.ctx, e.set.ID, in)
	if err != nil || !replay.Idempotency.Replayed || len(replay.Entries) != 3 || replay.Entries[0].ID != first.Entries[0].ID {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	if n := e.entryCount(t); n != 3 {
		t.Fatalf("a replay must not write, got %d entries", n)
	}
	other := principalCtx(e.tenant, ids.New())
	if _, err := e.store.PostSetEntry(other, e.set.ID, in); problemCode(err) != CodeSetNotFound {
		t.Errorf("a caller scoped to another legal entity must not post ledger lines: %v", err)
	}
}
