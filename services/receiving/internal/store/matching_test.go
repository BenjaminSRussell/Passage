package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/BenjaminSRussell/depot/platform/events"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/platform/testkit"
	"github.com/BenjaminSRussell/depot/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/depot/services/receiving/internal/store"
)

func errCode(err error) string {
	var de *domain.Error
	if errors.As(err, &de) {
		return de.Code
	}
	return ""
}

func TestMatchingPolicyAndExceptionLifecycleAreScopedToTheLegalEntity(t *testing.T) {
	db := seedInvoice(t)
	seedOpenGrir(t, db)
	st := store.New(db.pool)
	ctx := tenant.With(context.Background(), db.one)
	elsewhere := tenant.With(context.Background(), tenant.Principal{TenantID: db.one.TenantID, LegalEntityID: testkit.TenantID(8)})
	policy := domain.MatchingPolicy{ID: "00000000-0000-0000-0000-00000000d001", LegalEntityID: db.one.LegalEntityID, VendorID: vendorID, OwnerUserID: "owner-1", Policy: domain.DefaultPolicy()}

	for _, owner := range []string{"owner-1", "owner-2"} {
		policy.OwnerUserID = owner
		if err := st.SetMatchingPolicy(ctx, policy, "admin"); err != nil {
			t.Fatal(err)
		}
	}
	got, err := st.ListMatchingPolicies(ctx, db.one.LegalEntityID)
	if err != nil || len(got) != 1 || got[0].OwnerUserID != "owner-2" || got[0].VendorID != vendorID || got[0].ItemID != "" || !got[0].Policy.PriceTolPct.Equal(domain.DefaultPolicy().PriceTolPct) {
		t.Fatalf("stored policies %+v, err %v: want the replaced scope only", got, err)
	}
	if hidden, err := st.ListMatchingPolicies(elsewhere, db.one.LegalEntityID); err != nil || len(hidden) != 0 {
		t.Fatalf("a caller outside the legal entity read %+v, err %v", hidden, err)
	}

	held := fullInvoice(db, "00000000-0000-0000-0000-00000000c600", "INV-X", domain.InvoiceSourceManual)
	hold := holdFunc(t, db, held, true)
	if _, err := st.PostInvoice(ctx, held, func(items []*domain.GrirItem) (store.InvoiceOutcome, error) {
		out, err := hold(items)
		out.Exception = &domain.MatchException{Assignee: "owner-2", Reasons: "line 1: PRICE_VARIANCE"}
		return out, err
	}); err != nil {
		t.Fatal(err)
	}
	open, err := st.ListExceptions(ctx, "", domain.ExceptionOpen)
	if err != nil || len(open) != 1 || open[0].InvoiceID != held.ID || open[0].Assignee != "owner-2" || open[0].Reasons != "line 1: PRICE_VARIANCE" {
		t.Fatalf("open exceptions %+v, err %v", open, err)
	}
	if hidden, err := st.ListExceptions(elsewhere, "", domain.ExceptionOpen); err != nil || len(hidden) != 0 {
		t.Fatalf("a caller outside the legal entity read %+v, err %v", hidden, err)
	}

	release := func(inv store.Invoice) (store.InvoiceOutcome, error) {
		return store.InvoiceOutcome{LedgerEntryID: "released", Total: inv.Total, Event: event(t, db.one, "vendorinvoice", "posted")}, nil
	}
	if _, err := st.ApproveException(ctx, held.ID, db.one.LegalEntityID, "bob", "fine", release); errCode(err) != domain.CodeExceptionNotAssignee {
		t.Fatalf("approval by a user who does not own the policy: %v", err)
	}
	if got := scalar(t, db, `select status from receiving_vendor_invoice where invoice_number = 'INV-X'`); got != "AUDIT_BLOCKED" {
		t.Fatalf("a refused approval left the invoice %s", got)
	}
	posted, err := st.ApproveException(ctx, held.ID, db.one.LegalEntityID, "owner-2", "agreed with the vendor", release)
	if err != nil || posted.Status != domain.InvoiceStatusPosted || posted.ReleasedBy != "owner-2" {
		t.Fatalf("approval %+v, err %v", posted, err)
	}
	if got := scalar(t, db, `select status || '/' || decided_by || '/' || decision_reason from receiving_invoice_exception`); got != "APPROVED/owner-2/agreed with the vendor" {
		t.Fatalf("stored exception %q", got)
	}
	if _, err := st.ApproveException(ctx, held.ID, db.one.LegalEntityID, "owner-2", "again", release); errCode(err) != domain.CodeExceptionNotOpen {
		t.Fatalf("a second approval: %v", err)
	}
}

func TestRejectingAHeldInvoiceClosesItsExceptionAsRejected(t *testing.T) {
	db := seedInvoice(t)
	seedOpenGrir(t, db)
	st := store.New(db.pool)
	ctx := tenant.With(context.Background(), db.one)
	held := fullInvoice(db, "00000000-0000-0000-0000-00000000c601", "INV-Y", domain.InvoiceSourceManual)
	hold := holdFunc(t, db, held, true)
	if _, err := st.PostInvoice(ctx, held, func(items []*domain.GrirItem) (store.InvoiceOutcome, error) {
		out, err := hold(items)
		out.Exception = &domain.MatchException{Assignee: "owner-2", Reasons: "line 1: QTY_VARIANCE"}
		return out, err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RejectInvoice(ctx, held.ID, db.one.LegalEntityID, "carol", "wrong quantity", func(store.Invoice) (events.Event, error) {
		return event(t, db.one, "invoice", "rejected"), nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := scalar(t, db, `select status || '/' || decided_by || '/' || decision_reason from receiving_invoice_exception`); got != "REJECTED/carol/wrong quantity" {
		t.Fatalf("stored exception %q", got)
	}
}
