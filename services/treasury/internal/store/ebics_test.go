package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/domain"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/ebics"
)

func newSubscriber(acct BankAccount, le string) NewEBICSSubscriber {
	return NewEBICSSubscriber{LegalEntityID: le, BankAccountID: acct.ID, HostID: "BANKHOST", PartnerID: "PARTNER1", UserID: "USER1",
		URL: "https://ebics.bank.example/ebics"}
}

func ready(sub ebics.Subscriber) ebics.Subscriber {
	now := time.Now().UTC()
	sub.State, sub.KeysConfirmed, sub.FetchedBy, sub.ConfirmedBy, sub.ConfirmedAt = ebics.StateReady, true, "operator", "treasurer", &now
	sub.A006Version, sub.X002Version, sub.BankX002, sub.BankE002 = 1, 1, []byte{1}, []byte{2}
	return sub
}

func TestEBICSSubscriber_LifecyclePersistsKeysAndRefusesStaleWrites(t *testing.T) {
	s, _ := newStore(t)
	le := ids.New()
	ctx := ctxFor(1, le)
	acct := activeAccount(t, s, ctx, le)
	sub, err := s.CreateEBICSSubscriber(ctx, newSubscriber(acct, le))
	if err != nil || sub.State != ebics.StateNew || sub.Version != 1 {
		t.Fatalf("create: %v %+v", err, sub)
	}
	if _, err := s.CreateEBICSSubscriber(ctx, newSubscriber(acct, le)); problemCode(t, err) != CodeSubscriberDuplicate {
		t.Fatalf("duplicate: %v", err)
	}

	stale := sub
	sub.State = ebics.StateKeysPending
	sub.BankX002, sub.BankE002 = []byte{1}, []byte{2}
	sub, err = s.UpdateSubscriber(ctx, sub)
	if err != nil || sub.Version != 2 || string(sub.BankX002) != "\x01" {
		t.Fatalf("update: %v %+v", err, sub)
	}
	if _, err := s.UpdateSubscriber(ctx, stale); !errors.Is(err, ebics.ErrVersionConflict) {
		t.Fatalf("stale write: %v", err)
	}

	if sub, err = s.UpdateSubscriber(ctx, ready(sub)); err != nil {
		t.Fatal(err)
	}
	sub.ParkedCode, sub.ParkedText = "091118", "signature verification failed"
	if _, err := s.UpdateSubscriber(ctx, sub); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSubscriber(ctx, sub.ID)
	if err != nil || got.State != ebics.StateReady || got.ConfirmedBy != "treasurer" || got.ParkedCode != "091118" || got.Version != 4 {
		t.Fatalf("get: %v %+v", err, got)
	}
}

func TestEBICSSubscriber_ReadyNeedsConfirmedBankKeys(t *testing.T) {
	s, _ := newStore(t)
	le := ids.New()
	ctx := ctxFor(1, le)
	sub, err := s.CreateEBICSSubscriber(ctx, newSubscriber(activeAccount(t, s, ctx, le), le))
	if err != nil {
		t.Fatal(err)
	}
	sub.State = ebics.StateReady
	if code := sqlState(t, errOf(s.UpdateSubscriber(ctx, sub))); code != "23514" {
		t.Fatalf("READY without bank keys or confirmation: SQLSTATE %s; want 23514", code)
	}
	sub.BankX002, sub.BankE002 = []byte{1}, []byte{2}
	if code := sqlState(t, errOf(s.UpdateSubscriber(ctx, sub))); code != "23514" {
		t.Fatalf("READY with keys but no confirmation: SQLSTATE %s; want 23514", code)
	}
	sub = ready(sub)
	sub.ConfirmedBy = sub.FetchedBy
	if code := sqlState(t, errOf(s.UpdateSubscriber(ctx, sub))); code != "23514" {
		t.Fatalf("READY confirmed by the person who fetched the keys: SQLSTATE %s; want 23514", code)
	}
	sub = ready(sub)
	sub.FetchedBy = ""
	if code := sqlState(t, errOf(s.UpdateSubscriber(ctx, sub))); code != "23514" {
		t.Fatalf("READY without a recorded fetcher: SQLSTATE %s; want 23514", code)
	}
	sub = ready(sub)
	sub.A006Version = 0
	if code := sqlState(t, errOf(s.UpdateSubscriber(ctx, sub))); code != "23514" {
		t.Fatalf("READY without a pinned A006 key version: SQLSTATE %s; want 23514", code)
	}
	if _, err := s.UpdateSubscriber(ctx, ready(sub)); err != nil {
		t.Fatalf("READY with every obligation met: %v", err)
	}
}

func TestEBICSSubscriber_KeyNamesAreDerivedFromTenantLegalEntityAndId(t *testing.T) {
	s, admin := newStore(t)
	le := ids.New()
	ctx := ctxFor(1, le)
	sub, err := s.CreateEBICSSubscriber(ctx, newSubscriber(activeAccount(t, s, ctx, le), le))
	if err != nil {
		t.Fatal(err)
	}
	a006, x002, e002 := ebics.KeyNames(testkit.TenantID(1), le, sub.ID)
	if sub.A006Key != a006 || sub.X002Key != x002 || sub.E002KVPath != e002 {
		t.Fatalf("names %q %q %q; want the derived %q %q %q", sub.A006Key, sub.X002Key, sub.E002KVPath, a006, x002, e002)
	}
	otherTenantA, otherTenantX, otherTenantE := ebics.KeyNames(testkit.TenantID(2), le, sub.ID)
	otherLEA, otherLEX, otherLEE := ebics.KeyNames(testkit.TenantID(1), ids.New(), sub.ID)
	for name, set := range map[string]string{
		"another tenant's A006 key":       "a006_key_name = '" + otherTenantA + "'",
		"another tenant's X002 key":       "x002_key_name = '" + otherTenantX + "'",
		"another tenant's E002 path":      "e002_kv_path = '" + otherTenantE + "'",
		"another legal entity's A006 key": "a006_key_name = '" + otherLEA + "'",
		"another legal entity's X002 key": "x002_key_name = '" + otherLEX + "'",
		"another legal entity's E002":     "e002_kv_path = '" + otherLEE + "'",
		"a free-text name":                "a006_key_name = 'ebics-a006'",
	} {
		_, err := admin.Exec(context.Background(), "update ebics_subscriber set "+set+" where id = $1", sub.ID)
		if code := sqlState(t, err); code != "23514" {
			t.Errorf("%s: SQLSTATE %s; want 23514 (check violation)", name, code)
		}
	}
	other := ctxFor(2, "")
	otherLE := ids.New()
	otherAcct := activeAccount(t, s, ctxFor(2, otherLE), otherLE)
	theirs, err := s.CreateEBICSSubscriber(other, newSubscriber(otherAcct, otherLE))
	if err != nil {
		t.Fatal(err)
	}
	if theirs.A006Key == sub.A006Key || theirs.X002Key == sub.X002Key || theirs.E002KVPath == sub.E002KVPath || !strings.Contains(theirs.A006Key, testkit.TenantID(2)) {
		t.Fatalf("two tenants must never share key names: %+v / %+v", theirs, sub)
	}
}

func TestEBICSSubscriber_OneSubscriberPerBankAccount(t *testing.T) {
	s, _ := newStore(t)
	le := ids.New()
	ctx := ctxFor(1, le)
	acct := activeAccount(t, s, ctx, le)
	if _, err := s.CreateEBICSSubscriber(ctx, newSubscriber(acct, le)); err != nil {
		t.Fatal(err)
	}
	second := newSubscriber(acct, le)
	second.UserID = "USER2"
	if _, err := s.CreateEBICSSubscriber(ctx, second); problemCode(t, err) != CodeSubscriberDuplicate {
		t.Fatalf("a second subscriber for the same bank account: %v", err)
	}
}

func errOf(_ ebics.Subscriber, err error) error { return err }

func TestEBICSSubscriber_IsolatedByTenantAndLegalEntityAndValidated(t *testing.T) {
	s, _ := newStore(t)
	le := ids.New()
	ctx := ctxFor(1, le)
	acct := activeAccount(t, s, ctx, le)
	sub, err := s.CreateEBICSSubscriber(ctx, newSubscriber(acct, le))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetSubscriber(ctxFor(2, ""), sub.ID); problemCode(t, err) != CodeSubscriberNotFound {
		t.Fatalf("another tenant: %v", err)
	}
	if _, err := s.GetSubscriber(ctxFor(1, ids.New()), sub.ID); problemCode(t, err) != CodeSubscriberNotFound {
		t.Fatalf("another legal entity: %v", err)
	}
	spare := newAccount(le)
	spare.AccountCode, spare.IBAN = "OPS-02", "GB82WEST12345698765432"
	unbound, err := s.CreateBankAccount(ctx, spare, ids.New())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateEBICSSubscriber(ctxFor(1, ""), NewEBICSSubscriber{LegalEntityID: ids.New(), BankAccountID: unbound.ID, HostID: "H", PartnerID: "P", UserID: "U2", URL: "https://b.example"}); problemCode(t, err) != CodeAccountNotFound {
		t.Fatalf("account of another entity: %v", err)
	}
	bad := newSubscriber(acct, le)
	bad.UserID = "USER 2"
	if _, err := s.CreateEBICSSubscriber(ctx, bad); problemCode(t, err) != CodeSubscriberInvalid {
		t.Fatalf("bad user id: %v", err)
	}
	bad = newSubscriber(acct, le)
	bad.UserID, bad.URL = "USER3", "http://bank.example/ebics"
	if _, err := s.CreateEBICSSubscriber(ctx, bad); problemCode(t, err) != CodeSubscriberInvalid {
		t.Fatalf("plain http to a remote bank: %v", err)
	}
}

func TestImportStatementFile_FeedsTheStatementImportAndReplays(t *testing.T) {
	s, _ := newStore(t)
	le := ids.New()
	ctx := ctxFor(1, le)
	acct := activeAccount(t, s, ctx, le)
	file := camt("STMT-EBICS-1", 0, 3, "")
	if err := s.ImportStatementFile(ctx, acct.ID, "stmt.xml", file); err != nil {
		t.Fatal(err)
	}
	if err := s.ImportStatementFile(ctx, acct.ID, "stmt.xml", file); err != nil {
		t.Fatalf("a redelivered file must replay: %v", err)
	}
	list, err := s.ListStatements(ctx, acct.ID)
	if err != nil || len(list) != 1 {
		t.Fatalf("statements %d, %v", len(list), err)
	}
	if err := s.ImportStatementFile(ctx, acct.ID, "x", []byte("not a statement")); err == nil {
		t.Fatal("garbage must be refused")
	}

	var (
		_ ebics.SubscriberStore = (*Postgres)(nil)
		_ ebics.StatementSink   = (*Postgres)(nil).ImportStatementFile
		_ ebics.PollableLister  = (*Postgres)(nil)
		_ ebics.AccountLookup   = (*Postgres)(nil)
	)
}

const secondIBAN = "GB82WEST12345698765432"

func twoAccountFile(secondAccountIBAN string) []byte {
	first := string(camt("S-A", 0, 1, ""))
	second := strings.ReplaceAll(string(camt("S-B", 0, 2, "")), testIBAN, secondAccountIBAN)
	stmt := second[strings.Index(second, "<Stmt>") : strings.Index(second, "</Stmt>")+len("</Stmt>")]
	return []byte(strings.Replace(first, "</BkToCstmrStmt>", stmt+"</BkToCstmrStmt>", 1))
}

func TestImportStatementFile_RoutesEachStatementToTheAccountItNames(t *testing.T) {
	s, _ := newStore(t)
	le := ids.New()
	ctx := ctxFor(1, le)
	a1 := activeAccount(t, s, ctx, le)
	in2 := newAccount(le)
	in2.IBAN, in2.AccountCode = secondIBAN, "OPS-02"
	a2, err := s.CreateBankAccount(ctx, in2, ids.New())
	if err != nil {
		t.Fatal(err)
	}
	if a2, err = s.TransitionBankAccount(ctx, a2.ID, a2.Version, domain.AccountActive); err != nil {
		t.Fatal(err)
	}
	file := twoAccountFile(secondIBAN)
	for range 2 {
		if err := s.ImportStatementFile(ctx, a1.ID, "c53.xml", file); err != nil {
			t.Fatalf("a file with two accounts, imported twice (the second is a replay): %v", err)
		}
	}
	for acct, want := range map[string]string{a1.ID: "S-A", a2.ID: "S-B"} {
		list, err := s.ListStatements(ctx, acct)
		if err != nil || len(list) != 1 || list[0].StatementID != want {
			t.Fatalf("account %s: %v %+v", acct, err, list)
		}
	}
	err = s.ImportStatementFile(ctx, a1.ID, "c53.xml", twoAccountFile("FR1420041010050500013M02606"))
	if err == nil || !strings.Contains(err.Error(), domain.CodeStatementAccount) && !strings.Contains(err.Error(), "no single bank account") {
		t.Fatalf("a statement for an unregistered account must fail the file: %v", err)
	}
}

func TestEBICSSubscriber_PollableAndByAccount(t *testing.T) {
	s, _ := newStore(t)
	le := ids.New()
	ctx := ctxFor(1, le)
	acct := activeAccount(t, s, ctx, le)
	sub, err := s.CreateEBICSSubscriber(ctx, newSubscriber(acct, le))
	if err != nil {
		t.Fatal(err)
	}
	all := ctxFor(1, "")
	if got, err := s.PollableSubscribers(all); err != nil || len(got) != 0 {
		t.Fatalf("a NEW subscriber is not pollable: %v %v", got, err)
	}
	if sub, err = s.UpdateSubscriber(ctx, ready(sub)); err != nil {
		t.Fatal(err)
	}
	if got, err := s.PollableSubscribers(all); err != nil || len(got) != 1 || got[0].ID != sub.ID {
		t.Fatalf("READY subscriber: %v %v", got, err)
	}
	if got, err := s.PollableSubscribers(ctxFor(2, "")); err != nil || len(got) != 0 {
		t.Fatalf("another tenant: %v %v", got, err)
	}
	byAccount, err := s.SubscriberForAccount(ctx, acct.ID)
	if err != nil || byAccount.ID != sub.ID {
		t.Fatalf("by account: %v %+v", err, byAccount)
	}
	if _, err := s.SubscriberForAccount(ctx, ids.New()); problemCode(t, err) != CodeSubscriberNotFound {
		t.Fatalf("unknown account: %v", err)
	}
	sub.ParkedCode = "091118"
	if _, err := s.UpdateSubscriber(ctx, sub); err != nil {
		t.Fatal(err)
	}
	if got, err := s.PollableSubscribers(all); err != nil || len(got) != 0 {
		t.Fatalf("a parked subscriber is not pollable: %v %v", got, err)
	}
}

func TestEBICSSubscriber_PollLockExcludesASecondSession(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	id := ids.New()
	unlock, ok, err := s.TryLockSubscriber(ctx, id)
	if err != nil || !ok {
		t.Fatalf("first session: acquired=%v err=%v", ok, err)
	}
	if _, ok, err := s.TryLockSubscriber(ctx, id); err != nil || ok {
		t.Fatalf("a second session must not get the lock while the first holds it: acquired=%v err=%v", ok, err)
	}
	other, ok, err := s.TryLockSubscriber(ctx, ids.New())
	if err != nil || !ok {
		t.Fatalf("another subscriber is independent: acquired=%v err=%v", ok, err)
	}
	other()
	unlock()
	again, ok, err := s.TryLockSubscriber(ctx, id)
	if err != nil || !ok {
		t.Fatalf("the lock must be free after unlock: acquired=%v err=%v", ok, err)
	}
	again()
}

func TestImportStatementFile_MT940WithoutStatementNumberImportsEveryDay(t *testing.T) {
	s, _ := newStore(t)
	le := ids.New()
	ctx := ctxFor(1, le)
	acct := activeAccount(t, s, ctx, le)
	day := func(date, opening, closing string) []byte {
		return []byte(":20:DAILY\n:25:" + testIBAN + "\n:60F:C" + date + "EUR" + opening + "\n:62F:C" + date + "EUR" + closing + "\n")
	}
	for _, f := range [][]byte{day("261021", "1000,00", "1000,00"), day("261022", "1000,00", "1000,00")} {
		if err := s.ImportStatementFile(ctx, acct.ID, "sta.txt", f); err != nil {
			t.Fatalf("a bank that reuses :20: and sends no :28C: must still import both days: %v", err)
		}
	}
	list, err := s.ListStatements(ctx, acct.ID)
	if err != nil || len(list) != 2 {
		t.Fatalf("statements = %d, %v; want one per day", len(list), err)
	}
}

func TestImportStatementFile_MT940StoredUnderTheLegacyIDIsNotImportedTwice(t *testing.T) {
	for name, tc := range map[string]struct{ file, legacyID string }{
		"with :28C:":    {":20:STARTUMS\n:25:" + testIBAN + "\n:28C:00007/001\n:60F:C261021EUR1000,00\n:62F:C261021EUR1000,00\n", "STARTUMS/00007/001"},
		"without :28C:": {":20:DAILY\n:25:" + testIBAN + "\n:60F:C261021EUR1000,00\n:62F:C261021EUR1000,00\n", "DAILY"},
	} {
		t.Run(name, func(t *testing.T) {
			s, admin := newStore(t)
			le := ids.New()
			ctx := ctxFor(1, le)
			acct := activeAccount(t, s, ctx, le)
			if err := s.ImportStatementFile(ctx, acct.ID, "sta.txt", []byte(tc.file)); err != nil {
				t.Fatal(err)
			}
			if _, err := admin.Exec(context.Background(), `update bank_statement set statement_id = $1, file_hash = 'legacy-file-hash'`, tc.legacyID); err != nil {
				t.Fatal(err)
			}
			if err := s.ImportStatementFile(ctx, acct.ID, "sta.txt", []byte(strings.ReplaceAll(tc.file, "\n", "\r\n"))); err != nil {
				t.Fatalf("the same statement with other bytes must replay: %v", err)
			}
			if n := count(t, admin, `select count(*) from bank_statement`); n != 1 {
				t.Fatalf("statements = %d, want 1: a legacy-id row was imported again", n)
			}
		})
	}
}

func TestImportStatementFile_LegacyIDDoesNotSwallowAnotherDay(t *testing.T) {
	s, admin := newStore(t)
	le := ids.New()
	ctx := ctxFor(1, le)
	acct := activeAccount(t, s, ctx, le)
	day := func(date string) []byte {
		return []byte(":20:DAILY\n:25:" + testIBAN + "\n:60F:C" + date + "EUR1000,00\n:62F:C" + date + "EUR1000,00\n")
	}
	if err := s.ImportStatementFile(ctx, acct.ID, "sta.txt", day("261021")); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(context.Background(), `update bank_statement set statement_id = 'DAILY'`); err != nil {
		t.Fatal(err)
	}
	if err := s.ImportStatementFile(ctx, acct.ID, "sta.txt", day("261022")); err != nil {
		t.Fatal(err)
	}
	if n := count(t, admin, `select count(*) from bank_statement`); n != 2 {
		t.Fatalf("statements = %d, want one per day", n)
	}
}
