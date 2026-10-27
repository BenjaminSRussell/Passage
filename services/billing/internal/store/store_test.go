package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/billing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/billing/migrations"
)

// ctxAllEntities is a tenant principal with access to every legal entity.
func ctxAllEntities(tenantID string) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: tenantID, UserID: "user-test", Duties: map[string]struct{}{}})
}

func newProposal(t *testing.T, le string) Invoice {
	t.Helper()
	line := domain.Line{No: 1, Description: "Support", Qty: decimal.NewFromInt(1), UnitPrice: decimal.NewFromInt(100), RevenueAccount: "4000", TaxCode: "DE19"}
	_, totals, err := domain.Compute("EUR", []domain.Line{line}, []decimal.Decimal{decimal.NewFromInt(19)}, decimal.Zero)
	if err != nil {
		t.Fatal(err)
	}
	day := time.Date(2027, 3, 10, 0, 0, 0, 0, time.UTC)
	lines := []Line{{Line: line, Net: decimal.NewFromInt(100), Tax: decimal.NewFromInt(19)}}
	return Invoice{ID: ids.New(), LegalEntityID: le, CustomerID: ids.New(), InvoiceAccountID: ids.New(), Type: domain.TypeFreeText, Status: domain.StatusProposal, Currency: "EUR", InvoiceDate: day, DueDate: day.AddDate(0, 0, 30), PaymentTermDays: 30, Totals: totals, Lines: lines, IdempotencyKey: ids.New(), RequestHash: "h"}
}

func TestPostProposal_GapFreeNumbersAndOutbox(t *testing.T) {
	pool := testkit.DB(t)
	ctx := context.Background()
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	tctx := ctxAllEntities(testkit.TenantID(1))
	s := New(pool)
	le := ids.New()

	first, err := s.CreateProposal(tctx, newProposal(t, le))
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.CreateProposal(tctx, newProposal(t, le))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PostProposal(tctx, ids.New(), time.Now(), 0); err == nil {
		t.Fatal("posting an unknown invoice must fail")
	}
	postDay := time.Date(2027, 3, 12, 0, 0, 0, 0, time.UTC)
	p1, err := s.PostProposal(tctx, first.ID, postDay, 1)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := s.PostProposal(tctx, second.ID, postDay, 0)
	if err != nil {
		t.Fatal(err)
	}
	if p1.Number != "INV-2027-0000001" || p2.Number != "INV-2027-0000002" {
		t.Errorf("numbers %s and %s: a failed post must not consume a number", p1.Number, p2.Number)
	}
	if _, err := s.PostProposal(tctx, first.ID, postDay, 0); err == nil {
		t.Error("a posted invoice must not post again")
	}

	got, err := s.GetInvoice(tctx, first.ID)
	if err != nil || got.Status != domain.StatusPosted || len(got.Lines) != 1 || got.Lines[0].TaxCode != "DE19" || !got.Totals.Gross.Equal(decimal.NewFromInt(119)) {
		t.Fatalf("read back %+v, err %v", got, err)
	}
	var events int
	if err := pool.QueryRow(ctx, `select count(*) from outbox where type = 'dev.passage.billing.invoice.posted.v1'`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 2 {
		t.Errorf("outbox holds %d invoice.posted events, want 2", events)
	}
}

func TestPostProposal_VersionMismatchConsumesNoNumber(t *testing.T) {
	pool := testkit.DB(t)
	ctx := context.Background()
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	tctx := ctxAllEntities(testkit.TenantID(1))
	s := New(pool)
	le := ids.New()
	inv, err := s.CreateProposal(tctx, newProposal(t, le))
	if err != nil {
		t.Fatal(err)
	}
	postDay := time.Date(2027, 3, 12, 0, 0, 0, 0, time.UTC)
	_, err = s.PostProposal(tctx, inv.ID, postDay, 7)
	var p *httpx.Problem
	if !errors.As(err, &p) || p.Status != http.StatusPreconditionFailed || p.Code != CodeVersion {
		t.Fatalf("stale version: got %v, want 412 %s", err, CodeVersion)
	}
	posted, err := s.PostProposal(tctx, inv.ID, postDay, 1)
	if err != nil || posted.Number != "INV-2027-0000001" || posted.Version != 2 {
		t.Fatalf("post after a refused one: %+v %v; the refusal must not consume a number", posted, err)
	}
	var payload struct {
		Data struct {
			Number        string `json:"number"`
			LegalEntityID string `json:"legal_entity_id"`
			Totals        struct {
				Gross string `json:"gross"`
			} `json:"totals"`
			Lines []struct {
				TaxCode string `json:"tax_code"`
			} `json:"lines"`
		} `json:"data"`
	}
	var raw []byte
	if err := pool.QueryRow(ctx, `select payload from outbox where aggregate_id = $1`, inv.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	d := payload.Data
	if d.Number != posted.Number || d.LegalEntityID != le || d.Totals.Gross != "119.00" || len(d.Lines) != 1 || d.Lines[0].TaxCode != "DE19" {
		t.Errorf("invoice.posted payload %+v", d)
	}
}

func TestListInvoices_Filters(t *testing.T) {
	pool := testkit.DB(t)
	ctx := context.Background()
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := New(pool)
	t1 := ctxAllEntities(testkit.TenantID(1))
	le := ids.New()
	a, err := s.CreateProposal(t1, newProposal(t, le))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateProposal(t1, newProposal(t, le)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PostProposal(t1, a.ID, time.Date(2027, 3, 12, 0, 0, 0, 0, time.UTC), 0); err != nil {
		t.Fatal(err)
	}
	all, err := s.ListInvoices(t1, ListFilter{Limit: 10})
	if err != nil || len(all) != 2 || all[0].Lines != nil {
		t.Fatalf("list all: %d rows, err %v (headers only, no lines)", len(all), err)
	}
	posted, err := s.ListInvoices(t1, ListFilter{Status: domain.StatusPosted, Limit: 10})
	if err != nil || len(posted) != 1 || posted[0].ID != a.ID || posted[0].Number == "" {
		t.Fatalf("list posted: %+v %v", posted, err)
	}
	if one, err := s.ListInvoices(t1, ListFilter{Limit: 1}); err != nil || len(one) != 1 {
		t.Fatalf("limit 1: %d rows, err %v", len(one), err)
	}
	if byCustomer, err := s.ListInvoices(t1, ListFilter{CustomerID: a.CustomerID, Limit: 10}); err != nil || len(byCustomer) != 1 {
		t.Fatalf("by customer: %d rows, err %v", len(byCustomer), err)
	}
}

// TestRLS_TenantAndLegalEntityIsolation runs as the application role, where
// row-level security applies (the admin role bypasses it).
func TestRLS_TenantAndLegalEntityIsolation(t *testing.T) {
	if os.Getenv(testkit.EnvURL) == "" || os.Getenv(testkit.EnvAppURL) == "" {
		t.Skipf("%s and %s are needed for the RLS test", testkit.EnvURL, testkit.EnvAppURL)
	}
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := New(app)
	t1, t2 := ctxAllEntities(testkit.TenantID(1)), ctxAllEntities(testkit.TenantID(2))
	le := ids.New()
	a, err := s.CreateProposal(t1, newProposal(t, le))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PostProposal(t1, a.ID, time.Date(2027, 3, 12, 0, 0, 0, 0, time.UTC), 0); err != nil {
		t.Fatal(err)
	}
	if other, err := s.ListInvoices(t2, ListFilter{Limit: 10}); err != nil || len(other) != 0 {
		t.Fatalf("tenant 2 lists %d of tenant 1's invoices, err %v", len(other), err)
	}
	if _, err := s.GetInvoice(t2, a.ID); err == nil {
		t.Fatal("tenant 2 read tenant 1's invoice")
	}
	scoped := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: ids.New(), UserID: "u", Duties: map[string]struct{}{}})
	if rows, err := s.ListInvoices(scoped, ListFilter{Limit: 10}); err != nil || len(rows) != 0 {
		t.Fatalf("a caller scoped to another legal entity lists %d invoices, err %v", len(rows), err)
	}
	if mine, err := s.ListInvoices(t1, ListFilter{Limit: 10}); err != nil || len(mine) != 1 {
		t.Fatalf("tenant 1 lists %d invoices, err %v", len(mine), err)
	}
}
