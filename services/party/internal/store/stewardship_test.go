package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/depot/platform/dbx"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/testkit"
	"github.com/BenjaminSRussell/depot/services/party/internal/domain"
	"github.com/BenjaminSRussell/depot/services/party/internal/store"
)

func namedParty(t *testing.T, app *pgxpool.Pool, number, name string) string {
	t.Helper()
	return insertNamedParty(t, principalContext(1, ""), app, number, name)
}

func vendorRole(account, reason string) store.NewVendorRole {
	return store.NewVendorRole{In: domain.NewVendor{AccountNumber: account, VendorGroupID: ids.New(), CurrencyCode: "USD"}, OverrideReason: reason}
}

func duplicateOf(t *testing.T, err error) *store.DuplicateError {
	t.Helper()
	var dup *store.DuplicateError
	if !errors.As(err, &dup) {
		t.Fatalf("want a duplicate error, got %v", err)
	}
	return dup
}

func TestVendorDuplicate_BlocksWithCandidatesThenOverrideQueuesATask(t *testing.T) {
	app := migratedApp(t)
	s := store.New(app)
	ctxA := asUser(1, legalEntityA, "buyer")
	first, subject := namedParty(t, app, "P-A", "Acme"), namedParty(t, app, "P-B", "Acme")
	if _, _, err := s.CreateVendorRole(ctxA, first, vendorRole("V-1", "")); err != nil {
		t.Fatal(err)
	}

	_, _, err := s.CreateVendorRole(ctxA, subject, vendorRole("V-2", ""))
	dup := duplicateOf(t, err)
	if problemCode(t, err) != store.CodeDupLikely || dup.Status != 409 || len(dup.Candidates) != 1 || dup.Candidates[0].PartyID != first || dup.Candidates[0].PartyNumber != "P-A" || dup.Candidates[0].Class == "" {
		t.Fatalf("refusal = %+v, %v; want 409 with candidate %s", dup, err, first)
	}
	if got := count(ctxA, t, app, "vendor_role"); got != 1 {
		t.Fatalf("vendor roles after refusal = %d, want 1", got)
	}
	if got := count(ctxA, t, app, "steward_task"); got != 0 {
		t.Fatalf("steward tasks after refusal = %d, want 0", got)
	}

	override := vendorRole("V-2", "separate subsidiary")
	override.Key, override.Hash = "k1", "h1"
	vendor, created, err := s.CreateVendorRole(ctxA, subject, override)
	if err != nil || !created || vendor.PartyID != subject {
		t.Fatalf("override = %+v, %v, %v; want created", vendor, created, err)
	}
	if _, created, err := s.CreateVendorRole(ctxA, subject, override); err != nil || created {
		t.Fatalf("replay = %v, %v; want the original, not created", created, err)
	}
	tasks, err := s.ListStewardTasks(ctxA, store.StewardOpen)
	if err != nil || len(tasks) != 1 || tasks[0].PartyID != subject || tasks[0].OverrideReason != "separate subsidiary" || len(tasks[0].Candidates) != 1 || tasks[0].Candidates[0].PartyID != first || tasks[0].Role != "vendor" {
		t.Fatalf("tasks = %+v, %v; want one open task for %s", tasks, err, subject)
	}
}

func TestVendorDuplicate_IgnoresUnrelatedMergedAndOtherLegalEntityParties(t *testing.T) {
	app := migratedApp(t)
	s := store.New(app)
	ctxA, ctxB := asUser(1, legalEntityA, "buyer"), asUser(1, legalEntityB, "buyer")
	other := namedParty(t, app, "P-A", "Acme")
	unrelated := namedParty(t, app, "P-C", "Zenith Logistics")
	subject := namedParty(t, app, "P-B", "Acme")
	if _, _, err := s.CreateVendorRole(ctxA, other, vendorRole("V-1", "")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateVendorRole(ctxB, unrelated, vendorRole("V-9", "")); err != nil {
		t.Fatalf("unrelated name: %v", err)
	}
	if _, _, err := s.CreateVendorRole(ctxB, subject, vendorRole("V-2", "")); err != nil {
		t.Fatalf("candidate in another legal entity must not block: %v", err)
	}
	if got := count(ctxA, t, app, "steward_task"); got != 0 {
		t.Fatalf("steward tasks = %d, want 0 when nothing matched", got)
	}

	err := dbx.WithTenantTx(principalContext(1, ""), app, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `update party set status = 'merged', merged_into_id = $2 where tenant_id = $1 and id = $3`, testkit.TenantID(1), unrelated, other)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	again := namedParty(t, app, "P-D", "Acme")
	if _, _, err := s.CreateVendorRole(ctxA, again, vendorRole("V-3", "")); err != nil {
		t.Fatalf("a merged party is never a candidate: %v", err)
	}
}

func TestElectronicAddress_AddListScopeAndBlockingKey(t *testing.T) {
	app := migratedApp(t)
	s := store.New(app)
	ctxA, ctxB := asUser(1, legalEntityA, "buyer"), asUser(1, legalEntityB, "buyer")
	mueller := namedParty(t, app, "P-A", "Mueller GmbH")
	plain := namedParty(t, app, "P-C", "Muller GmbH")
	sharing := namedParty(t, app, "P-B", "Muller GmbH")
	email := domain.NewElectronicAddress{Kind: "email", Value: " Info@Mueller.example "}.Clean()

	added, err := s.AddElectronicAddress(ctxA, mueller, email)
	if err != nil || added.Value != "info@mueller.example" || added.LegalEntityID != legalEntityA {
		t.Fatalf("add = %+v, %v", added, err)
	}
	if again, err := s.AddElectronicAddress(ctxA, mueller, email); err != nil || again.ID != added.ID {
		t.Fatalf("repeat = %+v, %v; want the same row", again, err)
	}
	listed, err := s.ListElectronicAddresses(ctxA, mueller)
	if err != nil || len(listed) != 1 {
		t.Fatalf("list = %+v, %v", listed, err)
	}
	if other, err := s.ListElectronicAddresses(ctxB, mueller); err != nil || len(other) != 0 {
		t.Fatalf("other legal entity list = %+v, %v; want empty", other, err)
	}
	if _, err := s.AddElectronicAddress(principalContext(1, ""), mueller, email); problemCode(t, err) != "PTY-LEGAL-ENTITY-REQUIRED" {
		t.Fatalf("no legal entity: %v", err)
	}
	if _, err := s.AddElectronicAddress(ctxA, ids.New(), email); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown party: %v", err)
	}
	if _, err := s.ListElectronicAddresses(ctxA, "not-a-uuid"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("malformed id: %v", err)
	}

	if _, _, err := s.CreateVendorRole(ctxA, mueller, vendorRole("V-1", "")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateVendorRole(ctxA, plain, vendorRole("V-2", "")); err != nil {
		t.Fatalf("a similar name with a different prefix and no shared address is not a candidate: %v", err)
	}
	if _, err := s.AddElectronicAddress(ctxA, sharing, email); err != nil {
		t.Fatal(err)
	}
	_, _, err = s.CreateVendorRole(ctxA, sharing, vendorRole("V-3", ""))
	dup := duplicateOf(t, err)
	var viaEmail bool
	for _, c := range dup.Candidates {
		viaEmail = viaEmail || c.PartyID == mueller
	}
	if !viaEmail {
		t.Fatalf("candidates = %+v, want %s found through the shared email", dup.Candidates, mueller)
	}
}

func TestStewardTask_ListResolveOnceAndScope(t *testing.T) {
	app := migratedApp(t)
	s := store.New(app)
	ctxA, ctxB := asUser(1, legalEntityA, "buyer"), asUser(1, legalEntityB, "buyer")
	if tasks, err := s.ListStewardTasks(ctxA, store.StewardOpen); err != nil || tasks == nil || len(tasks) != 0 {
		t.Fatalf("empty queue = %+v, %v; want an empty list", tasks, err)
	}
	first, subject := namedParty(t, app, "P-A", "Acme"), namedParty(t, app, "P-B", "Acme")
	if _, _, err := s.CreateVendorRole(ctxA, first, vendorRole("V-1", "")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateVendorRole(ctxA, subject, vendorRole("V-2", "different company")); err != nil {
		t.Fatal(err)
	}
	tasks, err := s.ListStewardTasks(ctxA, store.StewardOpen)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("tasks = %+v, %v", tasks, err)
	}
	task := tasks[0]
	if other, err := s.ListStewardTasks(ctxB, store.StewardOpen); err != nil || len(other) != 0 {
		t.Fatalf("other legal entity = %+v, %v; want empty", other, err)
	}
	if other, err := s.ListStewardTasks(asUser(2, legalEntityA, "buyer"), store.StewardOpen); err != nil || len(other) != 0 {
		t.Fatalf("other tenant = %+v, %v; want empty", other, err)
	}
	in := store.ResolveStewardTask{Resolution: "distinct", Reason: "different registered entity"}
	if _, err := s.ResolveStewardTask(ctxB, task.ID, in); problemCode(t, err) != store.CodeStewardNotFound {
		t.Fatalf("resolve from another legal entity: %v", err)
	}
	resolved, err := s.ResolveStewardTask(ctxA, task.ID, in)
	if err != nil || resolved.Status != store.StewardResolved || resolved.Resolution != "distinct" || resolved.ResolvedBy != "buyer" || resolved.ResolvedAt == nil {
		t.Fatalf("resolve = %+v, %v", resolved, err)
	}
	if _, err := s.ResolveStewardTask(ctxA, task.ID, in); problemCode(t, err) != store.CodeStewardResolved {
		t.Fatalf("second resolve: %v", err)
	}
	if open, err := s.ListStewardTasks(ctxA, store.StewardOpen); err != nil || len(open) != 0 {
		t.Fatalf("open after resolve = %+v, %v", open, err)
	}
	if done, err := s.ListStewardTasks(ctxA, store.StewardResolved); err != nil || len(done) != 1 {
		t.Fatalf("resolved list = %+v, %v", done, err)
	}
	for _, id := range []string{ids.New(), "not-a-uuid"} {
		if _, err := s.ResolveStewardTask(ctxA, id, in); problemCode(t, err) != store.CodeStewardNotFound {
			t.Fatalf("resolve %q: %v", id, err)
		}
	}
}
