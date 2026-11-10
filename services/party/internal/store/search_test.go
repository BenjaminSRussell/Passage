package store_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/party/internal/store"
)

func insertNamedParty(t *testing.T, ctx context.Context, app *pgxpool.Pool, number, name string) string {
	t.Helper()
	id := ids.New()
	err := dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `insert into party (tenant_id, id, party_number, party_type, name, search_name, org_legal_name)
			values ($1, $2, $3, 'organization', $4, lower($4), $4)`, testkit.TenantID(1), id, number, name)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestSearch_FindsPartiesByNameOrNumberAndPages(t *testing.T) {
	app := migratedApp(t)
	s := store.New(app)
	ctx := asUser(1, legalEntityA, "buyer")
	contoso := insertNamedParty(t, ctx, app, "P-00042", "Contoso Ltd")
	insertNamedParty(t, ctx, app, "P-00043", "Fabrikam 100% Inc")
	insertNamedParty(t, ctx, app, "P-00044", "Fabrikam Two")

	for query, want := range map[string]string{"contoso": contoso, "P-00042": contoso, "  CONTOSO ": contoso} {
		page, err := s.Search(ctx, store.SearchParties, query, "", store.SearchKey{}, 10)
		if err != nil || len(page.Items) != 1 || page.Items[0].ID != want || page.Items[0].Name != "Contoso Ltd" || page.Items[0].PartyNumber != "P-00042" {
			t.Fatalf("query %q = %+v, %v", query, page, err)
		}
	}
	if page, err := s.Search(ctx, store.SearchParties, "%", "", store.SearchKey{}, 10); err != nil || len(page.Items) != 1 || page.Items[0].Name != "Fabrikam 100% Inc" {
		t.Fatalf("a percent sign matches itself only: %+v, %v", page, err)
	}
	first, err := s.Search(ctx, store.SearchParties, "", "", store.SearchKey{}, 2)
	if err != nil || len(first.Items) != 2 || first.Next == nil {
		t.Fatalf("first page = %+v, %v; want two items and a next key", first, err)
	}
	second, err := s.Search(ctx, store.SearchParties, "", "", *first.Next, 2)
	if err != nil || len(second.Items) != 1 || second.Next != nil || second.Items[0].ID == first.Items[0].ID || second.Items[0].ID == first.Items[1].ID {
		t.Fatalf("second page = %+v, %v; want the remaining party once", second, err)
	}
	if other, err := s.Search(asUser(2, legalEntityA, "buyer"), store.SearchParties, "", "", store.SearchKey{}, 10); err != nil || len(other.Items) != 0 {
		t.Fatalf("another tenant must see nothing: %+v, %v", other, err)
	}
}

func TestSearch_VendorHitsCarryTheRoleIDAndStayInTheCallersLegalEntity(t *testing.T) {
	app := migratedApp(t)
	s := store.New(app)
	ctxA, ctxB := asUser(1, legalEntityA, "buyer"), asUser(1, legalEntityB, "buyer")
	partyID := insertNamedParty(t, ctxA, app, "P-00050", "Contoso Ltd")
	vendor, err := s.CreateVendor(ctxA, partyID, newVendor(ids.New()))
	if err != nil {
		t.Fatal(err)
	}

	page, err := s.Search(ctxA, store.SearchVendors, "contoso", "", store.SearchKey{}, 10)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != vendor.ID || page.Items[0].PartyID != partyID || page.Items[0].AccountNumber != vendor.AccountNumber {
		t.Fatalf("vendor search = %+v, %v; want the vendor role id %s", page, err, vendor.ID)
	}
	byAccount, err := s.Search(ctxA, store.SearchVendors, vendor.AccountNumber, "", store.SearchKey{}, 10)
	if err != nil || len(byAccount.Items) != 1 {
		t.Fatalf("vendor search by account number = %+v, %v", byAccount, err)
	}
	if other, err := s.Search(ctxB, store.SearchVendors, "contoso", "", store.SearchKey{}, 10); err != nil || len(other.Items) != 0 {
		t.Fatalf("another legal entity must not see the vendor: %+v, %v", other, err)
	}
	if customers, err := s.Search(ctxA, store.SearchCustomers, "contoso", "", store.SearchKey{}, 10); err != nil || len(customers.Items) != 0 {
		t.Fatalf("a vendor-only party is not a customer: %+v, %v", customers, err)
	}
}

func TestSearch_VendorHitsFilterByLegalEntityAndCarryIt(t *testing.T) {
	app := migratedApp(t)
	s := store.New(app)
	ctxA := asUser(1, legalEntityA, "buyer")
	partyID := insertNamedParty(t, ctxA, app, "P-00060", "Contoso Ltd")
	vendor, err := s.CreateVendor(ctxA, partyID, newVendor(ids.New()))
	if err != nil {
		t.Fatal(err)
	}
	wide := principalContext(1, "")

	page, err := s.Search(wide, store.SearchVendors, "contoso", legalEntityA, store.SearchKey{}, 10)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != vendor.ID || page.Items[0].LegalEntityID != legalEntityA {
		t.Fatalf("filter on the role's legal entity = %+v, %v; want the vendor with its legal entity", page, err)
	}
	if other, err := s.Search(wide, store.SearchVendors, "contoso", legalEntityB, store.SearchKey{}, 10); err != nil || len(other.Items) != 0 {
		t.Fatalf("filter on another legal entity must hide the vendor: %+v, %v", other, err)
	}
}
