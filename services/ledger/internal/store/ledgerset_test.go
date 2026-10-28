package store

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
	ledgerevents "github.com/BenjaminSRussell/passage/services/ledger/internal/events"
)

type setEnv struct {
	env
	local, tax Ledger
	set        LedgerSet
}

func setupLedgerSet(t *testing.T) setEnv {
	t.Helper()
	e, _ := setup(t)
	for num, a := range map[string]Account{
		"7010": {Name: "Local freight expense", Type: "expense", Postable: true},
		"2100": {Name: "Vendor payable", Type: "liability", Postable: true},
	} {
		if _, _, err := e.store.PutAccount(e.ctx, "LOC", num, a); err != nil {
			t.Fatalf("put local account %s: %v", num, err)
		}
	}
	local, err := e.store.CreateLedger(e.ctx, Ledger{LegalEntityID: e.le, Code: "HGB", Name: "Local GAAP", ChartCode: "LOC", Currency: "USD", DimensionAttributes: []string{"DEPT"}, FuturePostingDays: 366})
	if err != nil {
		t.Fatalf("create local ledger: %v", err)
	}
	tax, err := e.store.CreateLedger(e.ctx, Ledger{LegalEntityID: e.le, Code: "TAX", Name: "Tax", ChartCode: "STD", Currency: "USD", DimensionAttributes: []string{"DEPT"}, FuturePostingDays: 366})
	if err != nil {
		t.Fatalf("create tax ledger: %v", err)
	}
	if _, err := e.store.CreateAccountMapping(e.ctx, AccountMapping{LedgerID: local.ID, SourceAccount: "6010", TargetAccount: "7010", EffectiveFrom: "2026-01-01"}); err != nil {
		t.Fatalf("create mapping: %v", err)
	}
	set, err := e.store.CreateLedgerSet(e.ctx, LedgerSetInput{Code: "ALL", Name: "All ledgers", LeadingLedgerID: e.ledger.ID, ParallelLedgerIDs: []string{local.ID, tax.ID}})
	if err != nil {
		t.Fatalf("create set: %v", err)
	}
	return setEnv{env: e, local: local, tax: tax, set: set}
}

func (e setEnv) entryCount(t *testing.T) (n int) {
	t.Helper()
	err := dbx.WithTenantTx(e.ctx, e.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(e.ctx, `select count(*) from journal_entry`).Scan(&n)
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPostgres_SetPostingAppliesRulesPerLedgerAndBalancesEach(t *testing.T) {
	e := setupLedgerSet(t)
	in := vendorInvoice("", "SET-1", "1250.00")
	out, err := e.store.PostSetEntry(e.ctx, e.set.ID, in)
	if err != nil {
		t.Fatalf("post set: %v", err)
	}
	if len(out.Entries) != 3 {
		t.Fatalf("want one entry per ledger, got %d", len(out.Entries))
	}
	accountByLedger := map[string]string{}
	for _, entry := range out.Entries {
		if entry.DocumentID != in.DocumentID {
			t.Fatalf("entries must share the document id: %s != %s", entry.DocumentID, in.DocumentID)
		}
		accountByLedger[entry.LedgerID] = entry.Lines[0].Account
	}
	if accountByLedger[e.ledger.ID] != "6010" || accountByLedger[e.tax.ID] != "6010" || accountByLedger[e.local.ID] != "7010" {
		t.Fatalf("expense account per ledger: %v", accountByLedger)
	}
	for _, id := range []string{e.ledger.ID, e.local.ID, e.tax.ID} {
		tb, err := e.store.TrialBalance(e.ctx, BalanceFilter{LedgerID: id})
		if err != nil {
			t.Fatalf("trial balance %s: %v", id, err)
		}
		if !tb.Balanced || tb.Debit != "1250.00" || tb.Credit != "1250.00" || tb.Net != "0.00" {
			t.Fatalf("ledger %s trial balance: %+v", id, tb)
		}
	}
	var posted int
	err = dbx.WithTenantTx(e.ctx, e.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(e.ctx, `select count(*) from outbox where type = $1`, ledgerevents.JournalPosted).Scan(&posted)
	})
	if err != nil || posted != 3 {
		t.Fatalf("journal.posted events = %d (err %v); want 3", posted, err)
	}
}

func TestPostgres_SetPostingReplaysAndRejectsChangedPayload(t *testing.T) {
	e := setupLedgerSet(t)
	in := vendorInvoice("", "SET-2", "80.00")
	first, err := e.store.PostSetEntry(e.ctx, e.set.ID, in)
	if err != nil {
		t.Fatalf("post set: %v", err)
	}
	replay, err := e.store.PostSetEntry(e.ctx, e.set.ID, in)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !replay.Idempotency.Replayed || len(replay.Entries) != 3 || replay.Entries[0].ID != first.Entries[0].ID {
		t.Fatalf("replay must return the original entries: %+v", replay)
	}
	if n := e.entryCount(t); n != 3 {
		t.Fatalf("a replay must not post again, %d entries", n)
	}
	changed := in
	changed.Hash, _ = domain.HashPayload("different")
	if _, err := e.store.PostSetEntry(e.ctx, e.set.ID, changed); problemCode(err) != "LEDGER_IDEMPOTENCY_CONFLICT" {
		t.Fatalf("want LEDGER_IDEMPOTENCY_CONFLICT, got %v", err)
	}
}

func TestPostgres_SetPostingIsAllOrNothing(t *testing.T) {
	e := setupLedgerSet(t)
	in := vendorInvoice("", "SET-3", "40.00")
	in.Lines[1].Account = "2110"
	_, err := e.store.PostSetEntry(e.ctx, e.set.ID, in)
	var rej *domain.RejectedError
	if !errors.As(err, &rej) || rej.Violations[0].Code != "LEDGER_ACCOUNT_NOT_FOUND" {
		t.Fatalf("want LEDGER_ACCOUNT_NOT_FOUND from the local ledger, got %v", err)
	}
	if n := e.entryCount(t); n != 0 {
		t.Fatalf("a rejection on one ledger must leave %d entries on the others", n)
	}
	ok, err := e.store.PostEntry(e.ctx, vendorInvoice(e.ledger.ID, "AFTER", "5.00"))
	if err != nil || ok.Entries[0].PostingSequence != 1 {
		t.Fatalf("a rejected set must not consume sequence numbers: %v %+v", err, ok)
	}
}

func TestPostgres_LedgerSetValidation(t *testing.T) {
	e := setupLedgerSet(t)
	eur, err := e.store.CreateLedger(e.ctx, Ledger{LegalEntityID: e.le, Code: "EUR", Name: "Euro", ChartCode: "STD", Currency: "EUR", FuturePostingDays: 366})
	if err != nil {
		t.Fatal(err)
	}
	wide := principalCtx(e.tenant, "")
	otherEntity, err := e.store.CreateLedger(wide, Ledger{LegalEntityID: ids.New(), Code: "OTH", Name: "Other", ChartCode: "STD", Currency: "USD", FuturePostingDays: 366})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, code string
		in         LedgerSetInput
	}{
		{"currency", CodeSetCurrencyMismatch, LedgerSetInput{Code: "S1", Name: "s", LeadingLedgerID: e.ledger.ID, ParallelLedgerIDs: []string{eur.ID}}},
		{"legal entity", CodeSetEntityMismatch, LedgerSetInput{Code: "S2", Name: "s", LeadingLedgerID: e.ledger.ID, ParallelLedgerIDs: []string{otherEntity.ID}}},
		{"no parallel", CodeSetInvalid, LedgerSetInput{Code: "S3", Name: "s", LeadingLedgerID: e.ledger.ID}},
		{"leading is parallel", CodeSetInvalid, LedgerSetInput{Code: "S4", Name: "s", LeadingLedgerID: e.ledger.ID, ParallelLedgerIDs: []string{e.ledger.ID}}},
		{"repeated ledger", CodeSetInvalid, LedgerSetInput{Code: "S5", Name: "s", LeadingLedgerID: e.ledger.ID, ParallelLedgerIDs: []string{e.tax.ID, e.tax.ID}}},
	} {
		_, err := e.store.CreateLedgerSet(wide, tc.in)
		var rej *domain.RejectedError
		if !errors.As(err, &rej) || rej.Violations[0].Code != tc.code {
			t.Errorf("%s: want %s, got %v", tc.name, tc.code, err)
		}
	}
	if _, err := e.store.CreateLedgerSet(e.ctx, LedgerSetInput{Code: "ALL", Name: "dup", LeadingLedgerID: e.ledger.ID, ParallelLedgerIDs: []string{e.tax.ID}}); problemCode(err) != CodeSetCodeDuplicate {
		t.Errorf("duplicate code: %v", err)
	}
	if _, err := e.store.CreateAccountMapping(e.ctx, AccountMapping{LedgerID: e.local.ID, SourceAccount: "6010", TargetAccount: "7010", EffectiveFrom: "2026-01-01"}); problemCode(err) != CodeMappingDuplicate {
		t.Errorf("duplicate mapping: %v", err)
	}
	if _, err := e.store.GetLedgerSet(principalCtx(testkit.TenantID(2), e.le), e.set.ID); problemCode(err) != CodeSetNotFound {
		t.Errorf("another tenant must not see the set: %v", err)
	}
}

func TestPostgres_SetTablesAreAppendOnlyForTheAppRole(t *testing.T) {
	e := setupLedgerSet(t)
	for _, stmt := range []string{
		`update ledger_set set name = 'x'`,
		`delete from ledger_set_member`,
		`update ledger_account_mapping set target_account = 'x'`,
		`delete from ledger_account_mapping`,
	} {
		if _, err := e.pool.Exec(e.ctx, stmt); err == nil {
			t.Errorf("passage_app must not run %q", stmt)
		}
	}
}
