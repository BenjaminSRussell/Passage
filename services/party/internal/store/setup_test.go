package store_test

import (
	"context"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/party/internal/domain"
	"github.com/BenjaminSRussell/passage/services/party/internal/store"
	"github.com/BenjaminSRussell/passage/services/party/migrations"
)

// Payment terms are tenant-wide; posting profiles are read only in the caller's legal entities.
func TestSetup_PaymentTermsAndPostingProfiles(t *testing.T) {
	app := migratedApp(t)
	s := store.New(app)
	term, profileA, profileB := ids.New(), ids.New(), ids.New()
	insert := func(le, sql string, args ...any) {
		t.Helper()
		ctx := principalContext(1, le)
		if err := dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error { _, err := tx.Exec(ctx, sql, args...); return err }); err != nil {
			t.Fatal(err)
		}
	}
	insert(legalEntityA, `insert into payment_term (tenant_id, id, code, name, net_days) values ($1, $2, 'NET30', 'Net 30 days', 30)`, testkit.TenantID(1), term)
	insert(legalEntityA, `insert into posting_profile (tenant_id, id, legal_entity_id, code, name) values ($1, $2, $3, 'DOM', 'Domestic')`, testkit.TenantID(1), profileA, legalEntityA)
	insert(legalEntityB, `insert into posting_profile (tenant_id, id, legal_entity_id, code, name) values ($1, $2, $3, 'EXP', 'Export')`, testkit.TenantID(1), profileB, legalEntityB)
	inA := asUser(1, legalEntityA, "clerk")
	terms, err := s.ListPaymentTerms(inA)
	if err != nil || len(terms) != 1 || terms[0].Code != "NET30" || terms[0].NetDays == nil || *terms[0].NetDays != 30 {
		t.Fatalf("terms = %+v, %v", terms, err)
	}
	if got, err := s.GetPaymentTerm(inA, strings.ToUpper(term)); err != nil || got.Name != "Net 30 days" {
		t.Fatalf("term = %+v, %v", got, err)
	}
	if _, err := s.GetPaymentTerm(asUser(2, legalEntityA, "clerk"), term); problemCode(t, err) != store.CodePaymentTermNotFound {
		t.Fatalf("another tenant's term = %v", err)
	}
	profiles, err := s.ListPostingProfiles(inA, "")
	if err != nil || len(profiles) != 1 || profiles[0].ID != profileA {
		t.Fatalf("profiles in A = %+v, %v; want only A's", profiles, err)
	}
	if _, err := s.GetPostingProfile(inA, profileB); problemCode(t, err) != store.CodePostingProfileNotFound {
		t.Fatalf("B's profile read from A = %v; want not found", err)
	}
	both := asUser(1, legalEntityA+","+legalEntityB, "clerk")
	if got, err := s.ListPostingProfiles(both, legalEntityB); err != nil || len(got) != 1 || got[0].ID != profileB {
		t.Fatalf("profiles of B = %+v, %v", got, err)
	}
	if _, err := s.GetPaymentTerm(inA, "not-an-id"); problemCode(t, err) != store.CodePaymentTermNotFound {
		t.Fatalf("malformed id = %v", err)
	}
}

func TestSetup_CustomerGroupCreateReplayAndTenant(t *testing.T) {
	app := migratedApp(t)
	s := store.New(app)
	ctx := asUser(1, legalEntityA, "clerk")
	in := store.NewCustomerGroupRequest{In: domain.NewCustomerGroup{Code: "KEY", Name: "Key accounts"}, Key: "k1", Hash: "h1"}
	g, created, err := s.CreateCustomerGroup(ctx, in)
	if err != nil || !created || g.Code != "KEY" {
		t.Fatalf("create = %+v created=%v err=%v", g, created, err)
	}
	if again, created, err := s.CreateCustomerGroup(ctx, in); err != nil || created || again != g {
		t.Fatalf("replay = %+v created=%v err=%v", again, created, err)
	}
	if _, _, err := s.CreateCustomerGroup(ctx, store.NewCustomerGroupRequest{In: domain.NewCustomerGroup{Code: "KEY", Name: "Again"}}); problemCode(t, err) != store.CodeCustomerGroupExists {
		t.Fatalf("duplicate code = %v", err)
	}
	if _, _, err := s.CreateCustomerGroup(ctx, store.NewCustomerGroupRequest{In: domain.NewCustomerGroup{Code: "OTH", Name: "Other"}, Key: "k1", Hash: "h2"}); problemCode(t, err) != store.CodeIdempotencyReuse {
		t.Fatalf("key reuse = %v", err)
	}
	if list, err := s.ListCustomerGroups(ctx); err != nil || len(list) != 1 || list[0] != g {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if _, err := s.GetCustomerGroup(asUser(2, legalEntityA, "clerk"), g.ID); problemCode(t, err) != store.CodeCustomerGroupNotFound {
		t.Fatalf("other tenant = %v", err)
	}
}

// Customer group ids already on customer roles become groups on migration 0016.
func TestSetup_CustomerGroupBackfill(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	ctx := principalContext(1, "")
	before := fstest.MapFS{}
	entries, err := fs.ReadDir(migrations.FS, ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") && e.Name() < "0016" {
			data, err := fs.ReadFile(migrations.FS, e.Name())
			if err != nil {
				t.Fatal(err)
			}
			before[e.Name()] = &fstest.MapFile{Data: data}
		}
	}
	if err := dbx.Migrate(ctx, admin, before); err != nil {
		t.Fatal(err)
	}
	group := "abcdef12-0000-4000-8000-0000000000c1"
	if err := seedCustomerRole(ctx, admin, group); err != nil {
		t.Fatal(err)
	}
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	g, err := store.New(app).GetCustomerGroup(asUser(1, legalEntityA, "clerk"), group)
	if err != nil || g.Code != "CGABCDEF12" || g.Name != "Customer group CGABCDEF12" {
		t.Fatalf("backfilled group = %+v, %v", g, err)
	}
}

// seedCustomerRole writes a customer role naming group (and the party, term and profile it needs) as the admin role.
func seedCustomerRole(ctx context.Context, admin *pgxpool.Pool, group string) error {
	partyID, err := insertParty(ctx, admin, testkit.TenantID(1), "P-1")
	if err != nil {
		return err
	}
	profileID, termID := ids.New(), ids.New()
	if _, err := admin.Exec(ctx, `insert into posting_profile (tenant_id, id, legal_entity_id, code, name) values ($1, $2, $3, 'PP', 'Default')`, testkit.TenantID(1), profileID, legalEntityA); err != nil {
		return err
	}
	if _, err := admin.Exec(ctx, `insert into payment_term (tenant_id, id, code, name, net_days) values ($1, $2, 'T', 'Net 30', 30)`, testkit.TenantID(1), termID); err != nil {
		return err
	}
	_, err = admin.Exec(ctx, `insert into customer_role (tenant_id, id, party_id, legal_entity_id, account_number, customer_group_id, payment_term_id, posting_profile_id, currency_code)
		values ($1, $2, $3, $4, 'CUST-1', $5, $6, $7, 'USD')`, testkit.TenantID(1), ids.New(), partyID, legalEntityA, group, termID, profileID)
	return err
}
