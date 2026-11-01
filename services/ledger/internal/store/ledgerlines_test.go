package store

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

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

func TestPostgres_LedgerLinesMembershipIsCheckedBeforeTheDutyEveryTime(t *testing.T) {
	e := setupLedgerSet(t)
	in := vendorInvoice("", "LL-ORDER", "100.00")
	in.LedgerLines = map[string][]domain.LineInput{
		e.local.ID: {ledgerLine("7010", domain.SideDebit, "1.00")},
		e.tax.ID:   {ledgerLine("6010", domain.SideDebit, "1.00")},
		ids.New():  {ledgerLine("6010", domain.SideDebit, "1.00")},
	}
	for i := range 30 {
		_, err := e.store.PostSetEntry(e.ctx, e.set.ID, in)
		var rej *domain.RejectedError
		if !errors.As(err, &rej) || rej.Violations[0].Code != CodeLedgerLinesNotInSet {
			t.Fatalf("attempt %d: a ledger outside the set must win over the missing duty: %v", i, err)
		}
	}
}

func TestPostgres_LedgerLinesReplayWithoutTheDutyIsStillForbidden(t *testing.T) {
	e := setupLedgerSet(t)
	in := vendorInvoice("", "LL-REPLAY-403", "100.00")
	in.CanPostLedgerSpecific = true
	in.LedgerLines = map[string][]domain.LineInput{e.tax.ID: {ledgerLine("6010", domain.SideDebit, "5.00"), ledgerLine("2100", domain.SideCredit, "5.00")}}
	if _, err := e.store.PostSetEntry(e.ctx, e.set.ID, in); err != nil {
		t.Fatalf("first: %v", err)
	}
	in.CanPostLedgerSpecific = false
	if _, err := e.store.PostSetEntry(e.ctx, e.set.ID, in); !isProblem(err, http.StatusForbidden, "AUTH-0003") {
		t.Fatalf("a replay by a caller without the duty must not return the stored result: %v", err)
	}
}

func TestPostgres_LedgerLinesForOneLedgerOnlyLeaveTheOthersEmpty(t *testing.T) {
	e := setupLedgerSet(t)
	in := vendorInvoice("", "LL-PARTIAL", "100.00")
	in.Lines = nil
	in.CanPostLedgerSpecific = true
	in.LedgerLines = map[string][]domain.LineInput{e.ledger.ID: {ledgerLine("6010", domain.SideDebit, "5.00"), ledgerLine("2100", domain.SideCredit, "5.00")}}
	_, err := e.store.PostSetEntry(e.ctx, e.set.ID, in)
	var rej *domain.RejectedError
	if !errors.As(err, &rej) || rej.Violations[0].Code != "LEDGER_LINE_EMPTY" || !strings.HasPrefix(rej.Violations[0].Message, "ledger ") {
		t.Fatalf("a ledger left without any line must be rejected naming it: %v", err)
	}
	if n := e.entryCount(t); n != 0 {
		t.Fatalf("got %d entries", n)
	}
}

func TestPostgres_ListLedgerSetsAndAccountMappingsRespectScope(t *testing.T) {
	e := setupLedgerSet(t)
	sets, _, err := e.store.ListLedgerSets(e.ctx, "")
	if err != nil || len(sets) != 1 || sets[0].ID != e.set.ID || len(sets[0].LedgerIDs) != 3 {
		t.Fatalf("sets: %+v %v", sets, err)
	}
	mappings, _, err := e.store.ListAccountMappings(e.ctx, e.local.ID, "")
	if err != nil || len(mappings) != 1 || mappings[0].SourceAccount != "6010" || mappings[0].TargetAccount != "7010" || mappings[0].EffectiveFrom != "2026-01-01" {
		t.Fatalf("mappings: %+v %v", mappings, err)
	}
	if none, _, err := e.store.ListAccountMappings(e.ctx, e.tax.ID, ""); err != nil || len(none) != 0 {
		t.Fatalf("a ledger without mappings: %+v %v", none, err)
	}
	other := principalCtx(e.tenant, ids.New())
	if sets, _, err := e.store.ListLedgerSets(other, ""); err != nil || len(sets) != 0 {
		t.Errorf("another legal entity sees sets: %+v %v", sets, err)
	}
	if _, _, err := e.store.ListAccountMappings(other, e.local.ID, ""); problemCode(err) != "LEDGER_NOT_FOUND" {
		t.Errorf("another legal entity must get 404 for the mappings: %v", err)
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

func TestPostgres_ListAccountMappingsPagesByKeysetCursor(t *testing.T) {
	e := setupLedgerSet(t)
	start := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	for i := range 101 {
		m := AccountMapping{LedgerID: e.local.ID, SourceAccount: "6010", TargetAccount: "7010", EffectiveFrom: start.AddDate(0, 0, i).Format("2006-01-02")}
		if _, err := e.store.CreateAccountMapping(e.ctx, m); err != nil {
			t.Fatalf("mapping %d: %v", i, err)
		}
	}
	first, next, err := e.store.ListAccountMappings(e.ctx, e.local.ID, "")
	if err != nil || len(first) != 100 || next != first[99].ID {
		t.Fatalf("first page: %d items, next %q, %v", len(first), next, err)
	}
	second, next, err := e.store.ListAccountMappings(e.ctx, e.local.ID, next)
	if err != nil || len(second) != 2 || next != "" || second[0].EffectiveFrom <= first[99].EffectiveFrom {
		t.Fatalf("second page: %+v next %q %v", second, next, err)
	}
}
