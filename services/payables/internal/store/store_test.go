package store

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/payables/migrations"
)

const (
	leA     = "00000000-0000-0000-0000-0000000000a1"
	leB     = "00000000-0000-0000-0000-0000000000a2"
	vendor1 = "00000000-0000-0000-0000-0000000000c1"
	vendor2 = "00000000-0000-0000-0000-0000000000c2"
)

func ctxFor(n int, le string) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(n), LegalEntityID: le, Duties: map[string]struct{}{}})
}

func setup(t *testing.T) *Postgres {
	t.Helper()
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return New(app)
}

func invoice(le, vendor, source, due string) OpenItem {
	return OpenItem{LegalEntityID: le, VendorID: vendor, SourceInvoiceID: source, InvoiceNumber: "INV-" + source[len(source)-2:],
		InvoiceDate: "2027-03-01", DueDate: due, Currency: "USD", Amount: decimal.RequireFromString("1000.00")}
}

const (
	src1 = "00000000-0000-0000-0000-0000000000f1"
	src2 = "00000000-0000-0000-0000-0000000000f2"
)

func TestCreateFromInvoice_IdempotentBySourceInvoice(t *testing.T) {
	s := setup(t)
	ctx := ctxFor(1, leA)
	first, created, err := s.CreateFromInvoice(ctx, invoice(leA, vendor1, src1, "2027-03-31"))
	if err != nil || !created {
		t.Fatalf("first create: created=%v err=%v", created, err)
	}
	again, created, err := s.CreateFromInvoice(ctx, invoice(leA, vendor1, src1, "2027-03-31"))
	if err != nil || created || again.ID != first.ID {
		t.Fatalf("replay: created=%v id=%s want %s err=%v", created, again.ID, first.ID, err)
	}
	items, _ := s.List(ctx, Filter{}, 10)
	if len(items) != 1 || items[0].Status != "OPEN" || !items[0].OpenAmount.Equal(items[0].Amount) {
		t.Fatalf("items = %+v", items)
	}
}

func TestCreateFromInvoice_EmitsOneEvent(t *testing.T) {
	s := setup(t)
	ctx := ctxFor(1, leA)
	for range 2 {
		if _, _, err := s.CreateFromInvoice(ctx, invoice(leA, vendor1, src1, "2027-03-31")); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select count(*) from outbox where type = 'dev.passage.payables.openitem.created.v1'`).Scan(&n)
	})
	if err != nil || n != 1 {
		t.Fatalf("outbox events = %d err=%v, want 1", n, err)
	}
}

func TestIsolation_TenantAndLegalEntity(t *testing.T) {
	s := setup(t)
	a := ctxFor(1, leA)
	created, _, err := s.CreateFromInvoice(a, invoice(leA, vendor1, src1, "2027-03-31"))
	if err != nil {
		t.Fatal(err)
	}
	for name, ctx := range map[string]context.Context{"other tenant": ctxFor(2, leA), "other legal entity": ctxFor(1, leB)} {
		items, err := s.List(ctx, Filter{}, 10)
		if err != nil || len(items) != 0 {
			t.Errorf("%s list = %d err=%v, want 0", name, len(items), err)
		}
		if _, err := s.Settle(ctx, created.ID, created.Version, decimal.NewFromInt(1)); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s settle err = %v, want ErrNotFound", name, err)
		}
	}
	if _, _, err := s.CreateFromInvoice(ctxFor(1, leB), invoice(leA, vendor1, src2, "2027-03-31")); err == nil {
		t.Error("insert into a legal entity outside the caller's scope succeeded")
	}
	otherTenant, created2, err := s.CreateFromInvoice(ctxFor(2, leA), invoice(leA, vendor1, src1, "2027-03-31"))
	if err != nil || !created2 || otherTenant.ID == created.ID {
		t.Fatalf("same source invoice in another tenant: created=%v err=%v", created2, err)
	}
}

func TestList_FiltersByVendorAndDueDate(t *testing.T) {
	s := setup(t)
	ctx := ctxFor(1, leA)
	for _, in := range []OpenItem{
		invoice(leA, vendor1, src1, "2027-04-15"),
		invoice(leA, vendor2, src2, "2027-03-31"),
		invoice(leA, vendor1, "00000000-0000-0000-0000-0000000000f3", "2027-03-10"),
	} {
		if _, _, err := s.CreateFromInvoice(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.List(ctx, Filter{VendorID: vendor1}, 10)
	if err != nil || len(got) != 2 || got[0].DueDate != "2027-03-10" {
		t.Fatalf("vendor1 = %+v err=%v", got, err)
	}
	got, _ = s.List(ctx, Filter{DueOnOrBy: "2027-03-31"}, 10)
	if len(got) != 2 {
		t.Fatalf("due by 03-31 = %d, want 2", len(got))
	}
	if got, _ = s.List(ctx, Filter{}, 1); len(got) != 1 {
		t.Fatalf("limit not applied: %d", len(got))
	}
}

func TestSettle_ReducesOpenAmountWithVersionCheck(t *testing.T) {
	s := setup(t)
	ctx := ctxFor(1, leA)
	item, _, _ := s.CreateFromInvoice(ctx, invoice(leA, vendor1, src1, "2027-03-31"))

	partial, err := s.Settle(ctx, item.ID, item.Version, decimal.RequireFromString("400.00"))
	if err != nil || partial.Status != "PARTIALLY_SETTLED" || !partial.OpenAmount.Equal(decimal.RequireFromString("600")) || partial.Version != item.Version+1 {
		t.Fatalf("partial = %+v err=%v", partial, err)
	}
	if _, err := s.Settle(ctx, item.ID, item.Version, decimal.NewFromInt(1)); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale version err = %v, want ErrVersionConflict", err)
	}
	if _, err := s.Settle(ctx, item.ID, partial.Version, decimal.RequireFromString("600.01")); !errors.Is(err, ErrOverSettled) {
		t.Fatalf("oversettle err = %v, want ErrOverSettled", err)
	}
	if _, err := s.Settle(ctx, item.ID, partial.Version, decimal.Zero); !errors.Is(err, ErrInvalidAmount) {
		t.Fatalf("zero amount err = %v, want ErrInvalidAmount", err)
	}
	done, err := s.Settle(ctx, item.ID, partial.Version, decimal.RequireFromString("600"))
	if err != nil || done.Status != "SETTLED" || !done.OpenAmount.IsZero() {
		t.Fatalf("final = %+v err=%v", done, err)
	}
	if open, _ := s.List(ctx, Filter{OpenOnly: true}, 10); len(open) != 0 {
		t.Fatalf("settled item listed as open: %+v", open)
	}
}
