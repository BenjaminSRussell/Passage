package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/testkit"
	"github.com/BenjaminSRussell/depot/services/party/internal/domain"
	"github.com/BenjaminSRussell/depot/services/party/internal/store"
)

type bankFixture struct {
	s         *store.Postgres
	f         mergeFixture
	vendorID  string
	requester context.Context
	second    context.Context
}

func newBankFixture(t *testing.T) bankFixture {
	t.Helper()
	app := migratedApp(t)
	s := store.New(app).WithKeys(testKeys(t))
	partyID, err := insertParty(principalContext(1, ""), app, testkit.TenantID(1), "P-1")
	if err != nil {
		t.Fatal(err)
	}
	requester := asUser(1, legalEntityA, "buyer")
	vendor, err := s.CreateVendor(requester, partyID, newVendor(ids.New()))
	if err != nil {
		t.Fatal(err)
	}
	return bankFixture{s: s, f: mergeFixture{app: app, ctx: principalContext(1, "")}, vendorID: vendor.ID, requester: requester, second: asUser(1, legalEntityA, "approver")}
}

func (b bankFixture) add(t *testing.T, number string) domain.BankAccount {
	t.Helper()
	added, err := b.s.AddBankAccount(b.requester, b.vendorID, domain.NewBankAccount{AccountHolder: "Acme", BankCountry: "US", CurrencyCode: "USD", AccountNumber: number}.Clean())
	if err != nil {
		t.Fatal(err)
	}
	return added
}

func (b bankFixture) revealed(t *testing.T) []domain.BankAccount {
	t.Helper()
	accounts, err := b.s.ListBankAccounts(b.requester, b.vendorID, true)
	if err != nil {
		t.Fatal(err)
	}
	return accounts
}

func approveBank(t *testing.T, s *store.Postgres, legalEntityID string, added domain.BankAccount) {
	t.Helper()
	if _, err := s.DecidePendingChange(asUser(1, legalEntityID, "approver"), added.PendingChangeID, true, ""); err != nil {
		t.Fatal(err)
	}
}

const changedEvents = `select count(*)::text from outbox where type = 'dev.depot.party.vendorbank.changed.v1'`

func TestVendorBankPending_AccountWaitsForASecondPerson(t *testing.T) {
	b := newBankFixture(t)
	added := b.add(t, "000123456789")
	if added.Status != "pending" || added.PendingChangeID == "" {
		t.Fatalf("add = %+v; want a pending account with a change id", added)
	}
	if got := b.revealed(t); len(got) != 0 {
		t.Fatalf("revealed = %+v; a pending account must never reach payables", got)
	}
	masked, err := b.s.ListBankAccounts(b.requester, b.vendorID, false)
	if err != nil || len(masked) != 1 || masked[0].Status != "pending" {
		t.Fatalf("masked = %+v, %v; want the pending account shown", masked, err)
	}
	queue, err := b.s.ListPendingChanges(b.second, 10, time.Time{}, "")
	if err != nil || len(queue) != 1 || queue[0].Field != domain.FieldVendorBank || queue[0].BankAccountID != added.ID || queue[0].VendorID != b.vendorID || queue[0].RequestedValue != "Acme (USD) ending 6789" {
		t.Fatalf("pending queue = %+v, %v", queue, err)
	}

	if _, err := b.s.DecidePendingChange(b.requester, added.PendingChangeID, true, ""); problemCode(t, err) != store.CodeSelfApproval {
		t.Fatalf("self approval = %v, want %s", err, store.CodeSelfApproval)
	}
	if got := b.revealed(t); len(got) != 0 || scalar(t, b.f, changedEvents) != "0" {
		t.Fatalf("a refused self approval activated the account or queued a notice: %+v", got)
	}

	decided, err := b.s.DecidePendingChange(b.second, added.PendingChangeID, true, "")
	if err != nil || decided.Status != domain.ChangeApproved || decided.BankAccountID != added.ID {
		t.Fatalf("approve = %+v, %v", decided, err)
	}
	got := b.revealed(t)
	if len(got) != 1 || got[0].ID != added.ID || got[0].Status != "active" || got[0].AccountNumber != "000123456789" {
		t.Fatalf("revealed after approval = %+v", got)
	}
	if _, err := b.s.DecidePendingChange(asUser(1, legalEntityA, "third"), added.PendingChangeID, true, ""); problemCode(t, err) != store.CodeChangeDecided {
		t.Fatalf("second decision = %v, want %s", err, store.CodeChangeDecided)
	}
}

func TestVendorBankPending_ApprovalQueuesNoticeWithOldAndNewLast4Only(t *testing.T) {
	b := newBankFixture(t)
	first := b.add(t, "000123456789")
	if _, err := b.s.DecidePendingChange(b.second, first.PendingChangeID, true, ""); err != nil {
		t.Fatal(err)
	}
	replacement := b.add(t, "000987654321")
	if _, err := b.s.DecidePendingChange(b.second, replacement.PendingChangeID, true, ""); err != nil {
		t.Fatal(err)
	}
	want := `{"vendor_id": "` + b.vendorID + `", "legal_entity_id": "` + legalEntityA + `", "bank_account_id": "` + replacement.ID + `", "change_id": "` + replacement.PendingChangeID + `", "new_last4": "4321", "replaced_last4": ["6789"], "requested_by": "buyer", "approved_by": "approver", "notice_required": true}`
	if got := scalar(t, b.f, `select ((payload->'data')::jsonb = $1::jsonb)::text from outbox where type = 'dev.depot.party.vendorbank.changed.v1' and payload->'data'->>'bank_account_id' = $2`, want, replacement.ID); got != "true" {
		t.Errorf("notice payload does not match %s", want)
	}
	if got := scalar(t, b.f, `select (position('000987654321' in payload::text) > 0 or position('000123456789' in payload::text) > 0)::text from outbox where type = 'dev.depot.party.vendorbank.changed.v1'`); got != "false" {
		t.Error("notice carries account data")
	}
}

func TestVendorBankPending_RejectedAccountNeverActivatesOrNotifies(t *testing.T) {
	b := newBankFixture(t)
	added := b.add(t, "000123456789")
	if _, err := b.s.DecidePendingChange(b.second, added.PendingChangeID, false, "wrong account"); err != nil {
		t.Fatal(err)
	}
	if got := b.revealed(t); len(got) != 0 || scalar(t, b.f, changedEvents) != "0" {
		t.Fatalf("rejected account revealed or notified: %+v", got)
	}
	if masked, err := b.s.ListBankAccounts(b.requester, b.vendorID, false); err != nil || len(masked) != 0 {
		t.Fatalf("masked = %+v, %v; a rejected account is not listed", masked, err)
	}
}

func TestVendorBankPending_ApprovalOfAnAccountNoLongerPendingIsStale(t *testing.T) {
	b := newBankFixture(t)
	added := b.add(t, "000123456789")
	exec(t, b.f, `update vendor_bank_account set status = 'rejected' where id = $1`, added.ID)
	if _, err := b.s.DecidePendingChange(b.second, added.PendingChangeID, true, ""); problemCode(t, err) != store.CodeChangeStale {
		t.Fatalf("approve = %v, want %s", err, store.CodeChangeStale)
	}
	if got := scalar(t, b.f, `select status from pending_change where id = $1`, added.PendingChangeID); got != "pending" {
		t.Errorf("change status = %s, want it left pending after the stale refusal", got)
	}
}
