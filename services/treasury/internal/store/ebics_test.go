package store

import (
	"errors"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/ebics"
)

func newSubscriber(acct BankAccount, le string) NewEBICSSubscriber {
	return NewEBICSSubscriber{LegalEntityID: le, BankAccountID: acct.ID, HostID: "BANKHOST", PartnerID: "PARTNER1", UserID: "USER1",
		URL: "https://ebics.bank.example/ebics", A006Key: "ebics-a006", X002Key: "ebics-x002", E002KVPath: "passage/ebics-e002"}
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

	now := time.Now().UTC()
	sub.State, sub.KeysConfirmed, sub.ConfirmedBy, sub.ConfirmedAt = ebics.StateReady, true, "treasurer", &now
	if sub, err = s.UpdateSubscriber(ctx, sub); err != nil {
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
	if _, err := s.CreateEBICSSubscriber(ctxFor(1, ""), NewEBICSSubscriber{LegalEntityID: ids.New(), BankAccountID: acct.ID, HostID: "H", PartnerID: "P", UserID: "U2", URL: "https://b.example", A006Key: "a", X002Key: "x", E002KVPath: "p"}); problemCode(t, err) != CodeAccountNotFound {
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
	)
}
