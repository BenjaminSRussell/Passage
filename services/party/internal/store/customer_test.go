package store_test

import (
	"context"
	"net/http"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/party/internal/domain"
	"github.com/BenjaminSRussell/passage/services/party/internal/store"
)

type customerSeed struct {
	term, profileA, profileB string
}

func seedCustomerRefs(t *testing.T, app *pgxpool.Pool) customerSeed {
	t.Helper()
	ctx := principalContext(1, "")
	seed := customerSeed{term: ids.New(), profileA: ids.New(), profileB: ids.New()}
	err := dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `insert into payment_term (tenant_id, id, code, name, net_days) values ($1, $2, 'N30', 'Net 30', 30)`, testkit.TenantID(1), seed.term); err != nil {
			return err
		}
		for id, le := range map[string]string{seed.profileA: legalEntityA, seed.profileB: legalEntityB} {
			if _, err := tx.Exec(ctx, `insert into posting_profile (tenant_id, id, legal_entity_id, code, name) values ($1, $2, $3, $4, 'Default')`, testkit.TenantID(1), id, le, "PP-"+le[len(le)-2:]); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return seed
}

// newCustomer is a loader-style create (Import) carrying a legacy credit limit.
func newCustomer(seed customerSeed, account string) store.NewCustomerRole {
	limit := "2500.50"
	return store.NewCustomerRole{Import: true, In: domain.NewCustomer{
		AccountNumber: account, CustomerGroupID: ids.New(), PaymentTermID: seed.term, PostingProfileID: seed.profileA, CurrencyCode: "USD", CreditLimit: &limit, Blocked: "none",
	}.Clean()}
}

// byPerson is the same create made by a person holding party.write.
func byPerson(in store.NewCustomerRole) store.NewCustomerRole {
	in.Import = false
	return in
}

func outboxCount(t *testing.T, app *pgxpool.Pool, eventType string) int {
	t.Helper()
	ctx := principalContext(1, "")
	var n int
	err := dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select count(*) from outbox where tenant_id = $1 and payload::text like '%'||$2||'%'`, testkit.TenantID(1), eventType).Scan(&n)
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestCustomer_CreateReadsBackScopedAndEmitsEvent(t *testing.T) {
	app := migratedApp(t)
	s := store.New(app)
	seed := seedCustomerRefs(t, app)
	partyID, err := insertParty(principalContext(1, ""), app, testkit.TenantID(1), "P-1")
	if err != nil {
		t.Fatal(err)
	}
	in := newCustomer(seed, "C-1001")
	c, created, err := s.CreateCustomer(asUser(1, legalEntityA, "loader"), partyID, in)
	if err != nil || !created {
		t.Fatalf("create = %+v, %v, %v", c, created, err)
	}
	if c.PartyID != partyID || c.LegalEntityID != legalEntityA || c.AccountNumber != "C-1001" || c.PaymentTermID != seed.term || c.PostingProfileID != seed.profileA ||
		c.CurrencyCode != "USD" || c.CreditLimit == nil || *c.CreditLimit != "2500.50" || c.CreditCurrency == nil || *c.CreditCurrency != "USD" || c.Blocked != "none" || c.CreditHold || c.Version != 1 {
		t.Fatalf("customer = %+v", c)
	}
	if got := outboxCount(t, app, "dev.passage.party.customer.created.v1"); got != 1 {
		t.Fatalf("created events = %d, want 1", got)
	}
	// GetCustomer reads the role back in its legal entity only; elsewhere it does not exist.
	if got, err := s.GetCustomer(asUser(1, legalEntityA, "loader"), c.ID); err != nil || !reflect.DeepEqual(got, c) {
		t.Fatalf("get = %+v, %v; want %+v", got, err, c)
	}
	for name, ctx := range map[string]context.Context{"other legal entity": asUser(1, legalEntityB, "x"), "other tenant": asUser(2, legalEntityA, "x")} {
		if _, err := s.GetCustomer(ctx, c.ID); problemCode(t, err) != store.CodeCustomerNotFound {
			t.Errorf("%s get: %v", name, err)
		}
	}
	if _, err := s.GetCustomer(asUser(1, legalEntityA, "x"), "not-a-uuid"); problemCode(t, err) != store.CodeCustomerNotFound {
		t.Errorf("bad id: %v", err)
	}
	// The role is invisible to another legal entity and tenant (RLS as passage_app).
	for name, ctx := range map[string]context.Context{"other legal entity": asUser(1, legalEntityB, "x"), "other tenant": asUser(2, legalEntityA, "x")} {
		var n int
		if err := dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `select count(*) from customer_role`).Scan(&n)
		}); err != nil || n != 0 {
			t.Errorf("%s sees %d customer roles, err %v", name, n, err)
		}
	}
}

func TestCustomer_Refusals(t *testing.T) {
	app := migratedApp(t)
	s := store.New(app)
	seed := seedCustomerRefs(t, app)
	ctx := principalContext(1, "")
	partyID, _ := insertParty(ctx, app, testkit.TenantID(1), "P-1")
	other, _ := insertParty(ctx, app, testkit.TenantID(1), "P-2")
	loader := asUser(1, legalEntityA, "loader")
	if _, _, err := s.CreateCustomer(loader, partyID, newCustomer(seed, "C-1")); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		ctx    context.Context
		party  string
		in     store.NewCustomerRole
		status int
		code   string
	}{
		{"second role for the party in the entity", loader, partyID, newCustomer(seed, "C-2"), http.StatusConflict, store.CodeCustomerExists},
		{"account number taken", loader, other, newCustomer(seed, "C-1"), http.StatusConflict, store.CodeCustomerExists},
		{"unknown party", loader, ids.New(), newCustomer(seed, "C-3"), http.StatusNotFound, "PTY-PARTY-NOT-FOUND"},
		{"no legal entity", principalContext(1, ""), other, newCustomer(seed, "C-4"), http.StatusUnprocessableEntity, "PTY-LEGAL-ENTITY-REQUIRED"},
	}
	badTerm := newCustomer(seed, "C-5")
	badTerm.In.PaymentTermID = ids.New()
	otherProfile := newCustomer(seed, "C-6")
	otherProfile.In.PostingProfileID = seed.profileB
	badInvoice := newCustomer(seed, "C-7")
	missing := ids.New()
	badInvoice.In.InvoiceAccountPartyID = &missing
	for name, in := range map[string]store.NewCustomerRole{"payment term": badTerm, "posting profile of another entity": otherProfile, "invoice account": badInvoice} {
		cases = append(cases, struct {
			name   string
			ctx    context.Context
			party  string
			in     store.NewCustomerRole
			status int
			code   string
		}{name, loader, other, in, http.StatusUnprocessableEntity, store.CodeCustomerInvalid})
	}
	for _, tc := range cases {
		_, _, err := s.CreateCustomer(tc.ctx, tc.party, tc.in)
		if tc.code == "PTY-PARTY-NOT-FOUND" {
			if err != store.ErrNotFound {
				t.Errorf("%s: %v, want ErrNotFound", tc.name, err)
			}
			continue
		}
		if got := problemCode(t, err); got != tc.code {
			t.Errorf("%s: code %q (%v), want %s", tc.name, got, err, tc.code)
		}
	}
	// Another legal entity may hold its own role for the same party.
	otherLE := newCustomer(seed, "C-1")
	otherLE.In.PostingProfileID = seed.profileB
	if _, created, err := s.CreateCustomer(asUser(1, legalEntityB, "loader"), partyID, otherLE); err != nil || !created {
		t.Fatalf("same party in legal entity B: %v %v", created, err)
	}
}

func TestCustomer_IdempotentReplayAndKeyReuse(t *testing.T) {
	app := migratedApp(t)
	s := store.New(app)
	seed := seedCustomerRefs(t, app)
	partyID, _ := insertParty(principalContext(1, ""), app, testkit.TenantID(1), "P-1")
	loader := asUser(1, legalEntityA, "loader")
	in := newCustomer(seed, "C-1")
	in.Key, in.Hash = "import-1", "h1"
	first, created, err := s.CreateCustomer(loader, partyID, in)
	if err != nil || !created {
		t.Fatalf("first: %v %v", created, err)
	}
	again, created, err := s.CreateCustomer(loader, partyID, in)
	if err != nil || created || again.ID != first.ID {
		t.Fatalf("replay = %+v created=%v err=%v, want the original", again, created, err)
	}
	if got := outboxCount(t, app, "dev.passage.party.customer.created.v1"); got != 1 {
		t.Fatalf("replay emitted: %d events", got)
	}
	changed := in
	changed.Hash = "h2"
	if _, _, err := s.CreateCustomer(loader, partyID, changed); problemCode(t, err) != store.CodeIdempotencyReuse {
		t.Fatalf("same key other body: %v", err)
	}
}

func TestCreateParty_CallerSuppliedID(t *testing.T) {
	app := migratedApp(t)
	s := store.New(app)
	ctx := asUser(1, legalEntityA, "loader")
	next := func(context.Context) (string, error) { return "P-1", nil }
	fields := domain.PartyFields{LegalName: ptr("Acme")}
	id := ids.New()
	p, created, err := s.CreateParty(ctx, store.NewParty{ID: id, PartyType: "organization", Fields: fields}, next)
	if err != nil || !created || p.ID != id {
		t.Fatalf("create with id = %+v %v %v", p, created, err)
	}
	if _, _, err := s.CreateParty(ctx, store.NewParty{ID: id, PartyType: "organization", Fields: fields}, func(context.Context) (string, error) { return "P-2", nil }); problemCode(t, err) != store.CodePartyIDTaken {
		t.Fatalf("taken id: %v", err)
	}
	if _, _, err := s.CreateParty(asUser(2, legalEntityA, "loader"), store.NewParty{ID: id, PartyType: "organization", Fields: fields}, next); err != nil {
		t.Fatalf("same id in another tenant: %v", err)
	}
}

func ptr(s string) *string { return &s }

// FR-7.10: credit_limit is protected per legal entity, so a person cannot set
// a positive limit at create and skip the approval a raise would need. The
// migration loader loads the legacy limit as is (Import).
func TestCustomer_InitialCreditLimitRespectsProtection(t *testing.T) {
	app := migratedApp(t)
	s := store.New(app)
	seed := seedCustomerRefs(t, app)
	ctx := principalContext(1, "")
	clerk := asUser(1, legalEntityA, "clerk")
	party := func(n string) string {
		id, err := insertParty(ctx, app, testkit.TenantID(1), n)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	if _, _, err := s.CreateCustomer(clerk, party("P-1"), byPerson(newCustomer(seed, "C-1"))); problemCode(t, err) != store.CodeCustomerInvalid {
		t.Fatalf("person, positive limit, protected by default: %v", err)
	}
	zero := byPerson(newCustomer(seed, "C-2"))
	limit := "0"
	zero.In.CreditLimit = &limit
	if _, created, err := s.CreateCustomer(clerk, party("P-2"), zero); err != nil || !created {
		t.Fatalf("person, zero limit: %v %v", created, err)
	}
	noLimit := byPerson(newCustomer(seed, "C-3"))
	noLimit.In.CreditLimit, noLimit.In.CreditCurrency = nil, nil
	if _, created, err := s.CreateCustomer(clerk, party("P-3"), noLimit); err != nil || !created {
		t.Fatalf("person, no limit: %v %v", created, err)
	}
	imported := newCustomer(seed, "C-4")
	if c, _, err := s.CreateCustomer(asUser(1, legalEntityA, "service:migration-loader"), party("P-4"), imported); err != nil || c.CreditLimit == nil || *c.CreditLimit != "2500.50" {
		t.Fatalf("import keeps the legacy limit: %+v %v", c, err)
	}
	// Unprotected in A: a person may set the limit, except the steward who
	// just unprotected it (the same 24-hour rule as the credit-limit route).
	if _, err := s.SetProtectedFields(asUser(1, legalEntityA, "steward"), legalEntityA, []string{}); err != nil {
		t.Fatal(err)
	}
	if _, created, err := s.CreateCustomer(clerk, party("P-5"), byPerson(newCustomer(seed, "C-5"))); err != nil || !created {
		t.Fatalf("person, unprotected entity: %v %v", created, err)
	}
	if _, _, err := s.CreateCustomer(asUser(1, legalEntityA, "steward"), party("P-6"), byPerson(newCustomer(seed, "C-6"))); problemCode(t, err) != store.CodeCustomerInvalid {
		t.Fatalf("steward within 24h of unprotecting: %v", err)
	}
}

// The same Idempotency-Key sent by the loader for the same party in two legal
// entities names two different requests: each creates its own role.
func TestCustomer_IdempotencyKeyIsScopedToTheLegalEntity(t *testing.T) {
	app := migratedApp(t)
	s := store.New(app)
	seed := seedCustomerRefs(t, app)
	partyID, _ := insertParty(principalContext(1, ""), app, testkit.TenantID(1), "P-1")
	a := newCustomer(seed, "C-1")
	a.Key, a.Hash = "row-1", "h"
	b := a
	b.In.PostingProfileID = seed.profileB
	first, _, err := s.CreateCustomer(asUser(1, legalEntityA, "loader"), partyID, a)
	if err != nil {
		t.Fatal(err)
	}
	second, created, err := s.CreateCustomer(asUser(1, legalEntityB, "loader"), partyID, b)
	if err != nil || !created || second.ID == first.ID || second.LegalEntityID != legalEntityB {
		t.Fatalf("same key in legal entity B = %+v created=%v err=%v", second, created, err)
	}
}

// A contact acts for its own customer and for the accounts tied to it by the invoice-account relation in the same customer group:
// the bill-to account of a ship-to customer, and the ship-to accounts of a bill-to customer. Sharing a group alone, another group,
// another legal entity or another tenant never relates two customers.
func TestCustomer_PortalAccountsFollowTheInvoiceAccountRelationInOneGroup(t *testing.T) {
	app := migratedApp(t)
	s := store.New(app)
	seed := seedCustomerRefs(t, app)
	ctx := principalContext(1, "")
	group, otherGroup := ids.New(), ids.New()
	mk := func(account, name, group string, invoiceParty *string, blocked string) (customer, party string) {
		t.Helper()
		partyID, err := insertParty(ctx, app, testkit.TenantID(1), "P-"+account)
		if err != nil {
			t.Fatal(err)
		}
		if err := dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `update party set name = $3 where tenant_id = $1 and id = $2`, testkit.TenantID(1), partyID, name)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		in := newCustomer(seed, account)
		in.In.CustomerGroupID, in.In.InvoiceAccountPartyID, in.In.Blocked = group, invoiceParty, blocked
		c, _, err := s.CreateCustomer(asUser(1, legalEntityA, "loader"), partyID, in)
		if err != nil {
			t.Fatal(err)
		}
		return c.ID, partyID
	}
	hq, hqParty := mk("C-HQ", "Head office", group, nil, "none")
	s1, _ := mk("C-S1", "Store one", group, &hqParty, "none")
	s2, _ := mk("C-S2", "Store two", group, &hqParty, "none")
	_, _ = mk("C-S3", "Store three, blocked", group, &hqParty, "all")
	_, _ = mk("C-S4", "Store four, other group", otherGroup, &hqParty, "none")
	loner, _ := mk("C-X", "Same group, unrelated", group, nil, "none")

	names := func(accounts []store.PortalAccount) string {
		out := ""
		for _, a := range accounts {
			out += a.AccountNumber + ":" + a.Relation + " "
		}
		return out
	}
	user := asUser(1, legalEntityA, "portal")
	got, err := s.PortalAccounts(user, hq)
	if err != nil || names(got) != "C-HQ:self C-S1:ship-to C-S2:ship-to " || got[0].Name != "Head office" {
		t.Fatalf("head office accounts: %s err %v", names(got), err)
	}
	got, err = s.PortalAccounts(user, s1)
	if err != nil || names(got) != "C-S1:self " {
		t.Fatalf("store one accounts (a sibling is not related, bill-to is opt-in): %s err %v", names(got), err)
	}
	// Access is downward only: a ship-to contact reaches its bill-to account only when the flag is set.
	if got, _ = s.PortalAccounts(user, s1); names(got) != "C-S1:self " {
		t.Fatalf("a ship-to customer sees %s by default, want only itself", names(got))
	}
	if err := s.SetPortalBillToAccess(user, s1, true); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.PortalAccounts(user, s1); names(got) != "C-S1:self C-HQ:bill-to " {
		t.Fatalf("with the flag a ship-to customer sees %s", names(got))
	}
	if got, _ = s.PortalAccounts(user, hq); names(got) != "C-HQ:self C-S1:ship-to C-S2:ship-to " {
		t.Fatalf("a bill-to customer's access does not depend on the flag: %s", names(got))
	}
	if got, _ = s.PortalAccounts(user, s2); names(got) != "C-S2:self " {
		t.Fatalf("store two accounts: %s", names(got))
	}
	if err := s.SetPortalBillToAccess(asUser(1, legalEntityB, "x"), s1, true); problemCode(t, err) != store.CodeCustomerNotFound {
		t.Errorf("another legal entity: %v", err)
	}
	if got, _ = s.PortalAccounts(user, loner); names(got) != "C-X:self " {
		t.Fatalf("an unrelated customer of the same group sees %s", names(got))
	}
	for name, c := range map[string]context.Context{"other legal entity": asUser(1, legalEntityB, "x"), "other tenant": asUser(2, legalEntityA, "x")} {
		if _, err := s.PortalAccounts(c, hq); problemCode(t, err) != store.CodeCustomerNotFound {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := s.PortalAccounts(user, "not-a-uuid"); problemCode(t, err) != store.CodeCustomerNotFound {
		t.Errorf("bad id: %v", err)
	}
}
