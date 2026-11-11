package store

import (
	"context"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/billing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/billing/migrations"
)

// The customer portal lists with the customer from its service token and PostedOnly: in the database, one customer never sees
// another's invoices, and no customer sees a proposal.
func TestListInvoices_CustomerAndPostedOnly(t *testing.T) {
	admin, pool := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := New(pool)
	tctx := ctxAllEntities(testkit.TenantID(1))
	le := ids.New()
	custA, custB := ids.New(), ids.New()
	mk := func(customer string) Invoice {
		inv := newProposal(t, le)
		inv.CustomerID = customer
		inv, err := s.CreateProposal(tctx, inv)
		if err != nil {
			t.Fatal(err)
		}
		return inv
	}
	aPosted, aProposal, bPosted := mk(custA), mk(custA), mk(custB)
	for _, inv := range []Invoice{aPosted, bPosted} {
		if _, err := s.PostProposal(tctx, inv.ID, time.Date(2027, 3, 12, 0, 0, 0, 0, time.UTC), 0); err != nil {
			t.Fatal(err)
		}
	}
	page, err := s.ListInvoices(tctx, ListFilter{CustomerID: custA, PostedOnly: true, Limit: 10})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != aPosted.ID {
		t.Fatalf("customer A posted: %+v %v, want only %s", page.Items, err, aPosted.ID)
	}
	for _, inv := range page.Items {
		if inv.ID == aProposal.ID || inv.ID == bPosted.ID || inv.Status == domain.StatusProposal || inv.CustomerID != custA {
			t.Fatalf("leaked %+v", inv)
		}
	}
	if all, err := s.ListInvoices(tctx, ListFilter{CustomerID: custA, Limit: 10}); err != nil || len(all.Items) != 2 {
		t.Fatalf("without PostedOnly the proposal is listed: %d %v", len(all.Items), err)
	}
}
