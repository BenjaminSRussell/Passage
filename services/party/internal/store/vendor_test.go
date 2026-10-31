package store_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/fieldcrypt"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/party/internal/domain"
	"github.com/BenjaminSRussell/passage/services/party/internal/store"
	"github.com/BenjaminSRussell/passage/services/party/migrations"
)

func testKeys(t *testing.T) *fieldcrypt.Keys {
	t.Helper()
	keys, err := fieldcrypt.NewKeys("k1", map[string][]byte{"k1": bytes.Repeat([]byte{7}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	return keys
}

func newVendor(group string) domain.NewVendor {
	return domain.NewVendor{AccountNumber: "V-1001", VendorGroupID: group, CurrencyCode: "USD"}
}

func TestVendor_CreateReadAndScope(t *testing.T) {
	app := migratedApp(t)
	s := store.New(app)
	partyID, err := insertParty(principalContext(1, ""), app, testkit.TenantID(1), "P-1")
	if err != nil {
		t.Fatal(err)
	}
	group := ids.New()
	ctxA, ctxB := asUser(1, legalEntityA, "buyer"), asUser(1, legalEntityB, "buyer")

	created, err := s.CreateVendor(ctxA, partyID, newVendor(group))
	if err != nil || created.VendorGroupID != group || created.PartyID != partyID || created.LegalEntityID != legalEntityA || created.Blocked != "none" {
		t.Fatalf("create = %+v, %v; want vendor group %s on %s in %s", created, err, group, partyID, legalEntityA)
	}
	got, err := s.GetVendor(ctxA, created.ID)
	if err != nil || got != created {
		t.Fatalf("get = %+v, %v; want %+v", got, err, created)
	}
	listed, err := s.ListPartyVendors(ctxA, partyID)
	if err != nil || len(listed) != 1 || listed[0].VendorGroupID != group {
		t.Fatalf("list = %+v, %v", listed, err)
	}

	if _, err := s.GetVendor(ctxB, created.ID); problemCode(t, err) != store.CodeVendorNotFound {
		t.Fatalf("other legal entity read = %v, want vendor not found", err)
	}
	if other, err := s.ListPartyVendors(ctxB, partyID); err != nil || len(other) != 0 {
		t.Fatalf("other legal entity list = %+v, %v; want empty", other, err)
	}
	if _, err := s.GetVendor(asUser(2, legalEntityA, "buyer"), created.ID); problemCode(t, err) != store.CodeVendorNotFound {
		t.Fatalf("other tenant read = %v, want vendor not found", err)
	}
	if _, err := s.GetVendor(ctxA, "not-a-uuid"); problemCode(t, err) != store.CodeVendorNotFound {
		t.Fatalf("malformed id = %v, want vendor not found", err)
	}
}

func TestVendor_CreateRefusals(t *testing.T) {
	app := migratedApp(t)
	s := store.New(app)
	partyID, err := insertParty(principalContext(1, ""), app, testkit.TenantID(1), "P-1")
	if err != nil {
		t.Fatal(err)
	}
	ctxA := asUser(1, legalEntityA, "buyer")
	if _, err := s.CreateVendor(ctxA, partyID, newVendor(ids.New())); err != nil {
		t.Fatal(err)
	}
	second := newVendor(ids.New())
	second.AccountNumber = "V-2002"
	missingTerm := newVendor(ids.New())
	missingTerm.AccountNumber = "V-3003"
	term := ids.New()
	missingTerm.PaymentTermID = &term

	for _, tc := range []struct {
		name, party string
		ctx         func() error
		code        string
	}{
		{"second role in the same legal entity", partyID, func() error { _, err := s.CreateVendor(ctxA, partyID, second); return err }, store.CodeVendorExists},
		{"account number taken by another party", "", nil, store.CodeVendorExists},
		{"no legal entity", partyID, func() error { _, err := s.CreateVendor(principalContext(1, ""), partyID, second); return err }, "PTY-LEGAL-ENTITY-REQUIRED"},
		{"unknown payment term", partyID, func() error {
			_, err := s.CreateVendor(asUser(1, legalEntityB, "buyer"), partyID, missingTerm)
			return err
		}, store.CodeVendorInvalid},
	} {
		if tc.ctx == nil {
			other, err := insertParty(principalContext(1, ""), app, testkit.TenantID(1), "P-2")
			if err != nil {
				t.Fatal(err)
			}
			tc.ctx = func() error { _, err := s.CreateVendor(ctxA, other, newVendor(ids.New())); return err }
		}
		if err := tc.ctx(); problemCode(t, err) != tc.code {
			t.Errorf("%s: %v, want %s", tc.name, err, tc.code)
		}
	}
	if _, err := s.CreateVendor(ctxA, ids.New(), second); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown party: %v, want ErrNotFound", err)
	}
}

func TestVendorBank_SealedAtRestMaskedByDefaultRevealedOnRequest(t *testing.T) {
	app := migratedApp(t)
	s := store.New(app).WithKeys(testKeys(t))
	partyID, err := insertParty(principalContext(1, ""), app, testkit.TenantID(1), "P-1")
	if err != nil {
		t.Fatal(err)
	}
	ctx := asUser(1, legalEntityA, "buyer")
	vendor, err := s.CreateVendor(ctx, partyID, newVendor(ids.New()))
	if err != nil {
		t.Fatal(err)
	}
	in := domain.NewBankAccount{AccountHolder: "Acme GmbH", BankCountry: "DE", CurrencyCode: "EUR", AccountNumber: "DE89370400440532013000", RoutingNumber: "COBADEFF"}.Clean()
	added, err := s.AddBankAccount(ctx, vendor.ID, in)
	if err != nil || added.AccountLast4 != "3000" || added.AccountNumber != "" || added.RoutingNumber != "" {
		t.Fatalf("add = %+v, %v; want last4 only", added, err)
	}

	f := mergeFixture{app: app, ctx: principalContext(1, "")}
	for _, column := range []string{"account_number_sealed", "routing_number_sealed"} {
		if got := scalar(t, f, `select (position(convert_to($1, 'UTF8') in `+column+`) > 0)::text from vendor_bank_account`, map[string]string{"account_number_sealed": in.AccountNumber, "routing_number_sealed": in.RoutingNumber}[column]); got != "false" {
			t.Errorf("%s contains the plaintext", column)
		}
	}

	masked, err := s.ListBankAccounts(ctx, vendor.ID, false)
	if err != nil || len(masked) != 1 || masked[0].AccountLast4 != "3000" || masked[0].AccountNumber != "" || masked[0].RoutingNumber != "" {
		t.Fatalf("masked = %+v, %v", masked, err)
	}
	revealed, err := s.ListBankAccounts(ctx, vendor.ID, true)
	if err != nil || len(revealed) != 1 || revealed[0].AccountNumber != in.AccountNumber || revealed[0].RoutingNumber != in.RoutingNumber {
		t.Fatalf("revealed = %+v, %v", revealed, err)
	}

	if _, err := s.ListBankAccounts(asUser(1, legalEntityB, "buyer"), vendor.ID, false); problemCode(t, err) != store.CodeVendorNotFound {
		t.Errorf("other legal entity = %v, want vendor not found", err)
	}
	if _, err := s.AddBankAccount(asUser(1, legalEntityB, "buyer"), vendor.ID, in); problemCode(t, err) != store.CodeVendorNotFound {
		t.Errorf("add from other legal entity = %v, want vendor not found", err)
	}
}

func TestVendorBank_CiphertextIsBoundToFieldAndTenant(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(principalContext(1, ""), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(app).WithKeys(testKeys(t))
	partyID, err := insertParty(principalContext(1, ""), app, testkit.TenantID(1), "P-1")
	if err != nil {
		t.Fatal(err)
	}
	ctx := asUser(1, legalEntityA, "buyer")
	vendor, err := s.CreateVendor(ctx, partyID, newVendor(ids.New()))
	if err != nil {
		t.Fatal(err)
	}
	in := domain.NewBankAccount{AccountHolder: "Acme", BankCountry: "US", CurrencyCode: "USD", AccountNumber: "000123456789", RoutingNumber: "021000021"}.Clean()
	if _, err := s.AddBankAccount(ctx, vendor.ID, in); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(context.Background(), `update vendor_bank_account set routing_number_sealed = account_number_sealed`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ListBankAccounts(ctx, vendor.ID, true); !errors.Is(err, fieldcrypt.ErrOpen) {
		t.Fatalf("a value moved to another field opened: %v", err)
	}
}

func TestVendorBank_RefusesWithoutKeysOrUser(t *testing.T) {
	app := migratedApp(t)
	partyID, err := insertParty(principalContext(1, ""), app, testkit.TenantID(1), "P-1")
	if err != nil {
		t.Fatal(err)
	}
	ctx := asUser(1, legalEntityA, "buyer")
	vendor, err := store.New(app).CreateVendor(ctx, partyID, newVendor(ids.New()))
	if err != nil {
		t.Fatal(err)
	}
	in := domain.NewBankAccount{AccountHolder: "Acme", BankCountry: "US", CurrencyCode: "USD", AccountNumber: "000123456789"}.Clean()
	var p *httpx.Problem
	if _, err := store.New(app).AddBankAccount(ctx, vendor.ID, in); !errors.As(err, &p) || p.Status != http.StatusServiceUnavailable || p.Code != store.CodeBankUnavailable {
		t.Errorf("add without keys = %v, want 503 %s", err, store.CodeBankUnavailable)
	}
	if _, err := store.New(app).ListBankAccounts(ctx, vendor.ID, true); !errors.As(err, &p) || p.Status != http.StatusServiceUnavailable {
		t.Errorf("reveal without keys = %v, want 503", err)
	}
	if masked, err := store.New(app).ListBankAccounts(ctx, vendor.ID, false); err != nil || len(masked) != 0 {
		t.Errorf("masked list without keys = %+v, %v; want empty and no error", masked, err)
	}
	if _, err := store.New(app).WithKeys(testKeys(t)).AddBankAccount(principalContext(1, legalEntityA), vendor.ID, in); !errors.As(err, &p) || p.Status != http.StatusUnauthorized {
		t.Errorf("add without a user = %v, want 401", err)
	}
	if got := count(principalContext(1, ""), t, app, "vendor_bank_account"); got != 0 {
		t.Errorf("bank accounts stored = %d, want 0", got)
	}
}

func TestMerge_MovesVendorRolesWithBankAccountsAndAuditStaysClean(t *testing.T) {
	f := seedMerge(t, legalEntityB)
	keys := testKeys(t)
	s := store.New(f.app).WithKeys(keys)
	loserCtx := asUser(1, legalEntityB, "buyer")
	vendor, err := s.CreateVendor(loserCtx, f.loser, newVendor(ids.New()))
	if err != nil {
		t.Fatal(err)
	}
	in := domain.NewBankAccount{AccountHolder: "Acme", BankCountry: "US", CurrencyCode: "USD", AccountNumber: "000123456789"}.Clean()
	if _, err := s.AddBankAccount(loserCtx, vendor.ID, in); err != nil {
		t.Fatal(err)
	}
	before, err := s.Audit(f.merger)
	if err != nil || !before.Clean() || before.VendorRoles != 1 {
		t.Fatalf("pre-merge audit = %+v, %v", before, err)
	}

	res, err := s.Merge(f.merger, store.MergeRequest{WinnerID: f.winner, LoserID: f.loser})
	if err != nil || res.RolesMoved != 2 {
		t.Fatalf("merge = %+v, %v; want customer and vendor roles moved", res, err)
	}
	after, err := s.Audit(f.merger)
	if err != nil || !after.Clean() || after.VendorRoles != 1 {
		t.Fatalf("post-merge audit = %+v, %v", after, err)
	}
	moved, err := s.GetVendor(loserCtx, vendor.ID)
	if err != nil || moved.PartyID != f.winner || moved.VendorGroupID != vendor.VendorGroupID || moved.AccountNumber != vendor.AccountNumber {
		t.Fatalf("vendor after merge = %+v, %v; want same role data on the survivor", moved, err)
	}
	revealed, err := s.ListBankAccounts(loserCtx, vendor.ID, true)
	if err != nil || len(revealed) != 1 || revealed[0].AccountNumber != in.AccountNumber {
		t.Fatalf("bank accounts after merge = %+v, %v", revealed, err)
	}
	var payload domain.PartyMerged
	if err := jsonPayload(t, f, &payload); err != nil {
		t.Fatal(err)
	}
	want := domain.RoleIDMapping{Role: domain.RoleVendor, FromID: vendor.ID, ToID: vendor.ID}
	if !slices.Contains(payload.RoleIDMap, want) {
		t.Errorf("roleIdMap = %+v, want it to contain %+v", payload.RoleIDMap, want)
	}
}

func TestMerge_VendorRoleConflictInSameLegalEntityIsRejected(t *testing.T) {
	f := seedMerge(t, legalEntityB)
	s := store.New(f.app)
	for account, party := range map[string]string{"V-W": f.winner, "V-L": f.loser} {
		v := newVendor(ids.New())
		v.AccountNumber = account
		if _, err := s.CreateVendor(asUser(1, legalEntityB, "buyer"), party, v); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Merge(f.merger, store.MergeRequest{WinnerID: f.winner, LoserID: f.loser}); !errors.Is(err, store.ErrRoleConflict) {
		t.Fatalf("merge error = %v, want ErrRoleConflict", err)
	}
	if got := scalar(t, f, `select count(*)::text from vendor_role where party_id = $1`, f.loser); got != "1" {
		t.Errorf("loser vendor roles = %s, want 1 untouched", got)
	}
}

func TestAudit_FlagsVendorRoleOwnedByMergedParty(t *testing.T) {
	f := seedMerge(t, legalEntityB)
	s := store.New(f.app)
	if _, err := s.CreateVendor(asUser(1, legalEntityB, "buyer"), f.loser, newVendor(ids.New())); err != nil {
		t.Fatal(err)
	}
	exec(t, f, `insert into party_merge (tenant_id, id, survivor_party_id, merged_party_id, field_resolution, roles_moved, references_rewritten) values ($1, $2, $3, $4, '{}', 0, 0)`, testkit.TenantID(1), ids.New(), f.winner, f.loser)
	exec(t, f, `update party set status = 'merged', merged_into_id = $2 where id = $1`, f.loser, f.winner)
	report, err := s.Audit(f.merger)
	if err != nil || report.Clean() || report.VendorRolesOwnedByMerged != 1 {
		t.Fatalf("audit = %+v, %v; want the stranded vendor role flagged", report, err)
	}
}

func jsonPayload(t *testing.T, f mergeFixture, into *domain.PartyMerged) error {
	t.Helper()
	return json.Unmarshal([]byte(scalar(t, f, `select (payload->'data')::text from outbox where type = 'dev.passage.party.party.merged.v1'`)), into)
}

func TestVendorBank_RevealWritesDurableAuditWithoutAccountData(t *testing.T) {
	app := migratedApp(t)
	s := store.New(app).WithKeys(testKeys(t))
	partyID, err := insertParty(principalContext(1, ""), app, testkit.TenantID(1), "P-1")
	if err != nil {
		t.Fatal(err)
	}
	ctx := asUser(1, legalEntityA, "buyer")
	vendor, err := s.CreateVendor(ctx, partyID, newVendor(ids.New()))
	if err != nil {
		t.Fatal(err)
	}
	in := domain.NewBankAccount{AccountHolder: "Acme", BankCountry: "US", CurrencyCode: "USD", AccountNumber: "000123456789"}.Clean()
	if _, err := s.AddBankAccount(ctx, vendor.ID, in); err != nil {
		t.Fatal(err)
	}
	f := mergeFixture{app: app, ctx: principalContext(1, "")}
	const revealed = `select count(*)::text from outbox where type = 'dev.passage.party.vendorbank.revealed.v1'`
	if _, err := s.ListBankAccounts(ctx, vendor.ID, false); err != nil || scalar(t, f, revealed) != "0" {
		t.Fatalf("a masked read wrote an audit event: %v", err)
	}
	service := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: legalEntityA, UserID: "approver", Service: "payables", Duties: testkit.ScopeDuties(legalEntityA)})
	if _, err := s.ListBankAccounts(service, vendor.ID, true); err != nil {
		t.Fatal(err)
	}
	want := `{"vendor_id": "` + vendor.ID + `", "legal_entity_id": "` + legalEntityA + `", "actor_user_id": "approver", "actor_service": "payables", "account_count": 1}`
	if got := scalar(t, f, `select (((payload->'data')::jsonb - 'revealed_at') = $1::jsonb)::text from outbox where type = 'dev.passage.party.vendorbank.revealed.v1'`, want); got != "true" {
		t.Errorf("audit payload does not match %s", want)
	}
	if got := scalar(t, f, `select (position('000123456789' in payload::text) > 0)::text from outbox where type = 'dev.passage.party.vendorbank.revealed.v1'`); got != "false" {
		t.Error("audit event carries account data")
	}
	if _, err := s.ListBankAccounts(asUser(1, legalEntityB, "buyer"), vendor.ID, true); err == nil || scalar(t, f, revealed) != "1" {
		t.Errorf("a refused reveal must not write an audit event: %v", err)
	}
}

func TestVendor_ListForUnknownPartyIsNotFound(t *testing.T) {
	s := store.New(migratedApp(t))
	ctx := asUser(1, legalEntityA, "buyer")
	if _, err := s.ListPartyVendors(ctx, ids.New()); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown party = %v, want ErrNotFound", err)
	}
	if _, err := s.ListPartyVendors(ctx, "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("malformed party id = %v, want ErrNotFound", err)
	}
}

func TestVendor_CreateWaitsForConcurrentMergeAndThenRefuses(t *testing.T) {
	f := seedMerge(t, legalEntityB)
	s := store.New(f.app)
	tx, err := f.app.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(f.ctx)
	if _, err := tx.Exec(f.ctx, `select set_config('passage.tenant_id', $1, true), set_config('passage.legal_entity_scopes', '*', true)`, testkit.TenantID(1)); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(f.ctx, `update party set status = 'merged', merged_into_id = $2 where id = $1`, f.loser, f.winner); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := s.CreateVendor(asUser(1, legalEntityB, "buyer"), f.loser, newVendor(ids.New()))
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("create returned %v while the merge held the party row", err)
	case <-time.After(300 * time.Millisecond):
	}
	if err := tx.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; problemCode(t, err) != store.CodePartyMerged {
		t.Fatalf("create after the merge = %v, want %s", err, store.CodePartyMerged)
	}
	if got := scalar(t, f, `select count(*)::text from vendor_role where party_id = $1`, f.loser); got != "0" {
		t.Errorf("vendor roles on the merged party = %s, want 0", got)
	}
}
