package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"
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
	return tenant.With(context.Background(), tenant.Principal{TenantID: tenantID, UserID: "user-test", Duties: map[string]struct{}{tenant.DutyAllScope: {}}})
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
	admin, pool := testkit.DBWithApp(t)
	ctx := context.Background()
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
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
	if err := admin.QueryRow(ctx, `select count(*) from outbox where type = 'dev.passage.billing.invoice.posted.v1'`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 2 {
		t.Errorf("outbox holds %d invoice.posted events, want 2", events)
	}
}

func TestPostProposal_VersionMismatchConsumesNoNumber(t *testing.T) {
	admin, pool := testkit.DBWithApp(t)
	ctx := context.Background()
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
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
	if err := admin.QueryRow(ctx, `select payload from outbox where aggregate_id = $1`, inv.ID).Scan(&raw); err != nil {
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
	admin, pool := testkit.DBWithApp(t)
	ctx := context.Background()
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
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
	page, err := s.ListInvoices(t1, ListFilter{Limit: 10})
	all := page.Items
	if err != nil || len(all) != 2 || all[0].Lines != nil || page.NextAfter != "" {
		t.Fatalf("list all: %d rows, next %q, err %v (headers only, no lines, one page)", len(all), page.NextAfter, err)
	}
	posted, err := s.ListInvoices(t1, ListFilter{Status: domain.StatusPosted, Limit: 10})
	if err != nil || len(posted.Items) != 1 || posted.Items[0].ID != a.ID || posted.Items[0].Number == "" {
		t.Fatalf("list posted: %+v %v", posted, err)
	}
	// Keyset pages of one walk both invoices newest first, each once.
	one, err := s.ListInvoices(t1, ListFilter{Limit: 1})
	if err != nil || len(one.Items) != 1 || one.Items[0].ID != all[0].ID || one.NextAfter != all[0].ID {
		t.Fatalf("limit 1: %+v, err %v, want the newest invoice and a cursor", one, err)
	}
	two, err := s.ListInvoices(t1, ListFilter{Limit: 1, After: one.NextAfter})
	if err != nil || len(two.Items) != 1 || two.Items[0].ID != all[1].ID || two.NextAfter != "" {
		t.Fatalf("second page: %+v, err %v, want the older invoice and no cursor", two, err)
	}
	if byCustomer, err := s.ListInvoices(t1, ListFilter{CustomerID: a.CustomerID, Limit: 10}); err != nil || len(byCustomer.Items) != 1 {
		t.Fatalf("by customer: %d rows, err %v", len(byCustomer.Items), err)
	}
}

// The document flow query (issue #534): invoices billed from one sales order or packing slip.
func TestListInvoices_ByOrderAndPackingSlip(t *testing.T) {
	admin, pool := testkit.DBWithApp(t)
	ctx := context.Background()
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := New(pool)
	t1 := ctxAllEntities(testkit.TenantID(1))
	le := ids.New()
	billed := newProposal(t, le)
	order, slip := ids.New(), ids.New()
	billed.Lines[0].Source = domain.Source{PackingSlipID: slip, PackingSlipLineID: ids.New(), PackingSlipDate: time.Date(2027, 3, 9, 0, 0, 0, 0, time.UTC), OrderID: order, OrderLineID: ids.New(), ProductID: ids.New()}
	if _, err := s.CreateProposal(t1, billed); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateProposal(t1, newProposal(t, le)); err != nil {
		t.Fatal(err)
	}
	for name, f := range map[string]ListFilter{"order": {OrderID: order, Limit: 10}, "packing slip": {PackingSlipID: slip, Limit: 10}} {
		page, err := s.ListInvoices(t1, f)
		if err != nil || len(page.Items) != 1 || page.Items[0].ID != billed.ID {
			t.Fatalf("by %s: %+v, err %v, want only the billed invoice", name, page.Items, err)
		}
	}
	if page, err := s.ListInvoices(t1, ListFilter{OrderID: ids.New(), Limit: 10}); err != nil || len(page.Items) != 0 {
		t.Fatalf("unknown order: %+v, err %v", page.Items, err)
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
	if other, err := s.ListInvoices(t2, ListFilter{Limit: 10}); err != nil || len(other.Items) != 0 {
		t.Fatalf("tenant 2 lists %d of tenant 1's invoices, err %v", len(other.Items), err)
	}
	if _, err := s.GetInvoice(t2, a.ID); err == nil {
		t.Fatal("tenant 2 read tenant 1's invoice")
	}
	scoped := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: ids.New(), UserID: "u", Duties: map[string]struct{}{}})
	if rows, err := s.ListInvoices(scoped, ListFilter{Limit: 10}); err != nil || len(rows.Items) != 0 {
		t.Fatalf("a caller scoped to another legal entity lists %d invoices, err %v", len(rows.Items), err)
	}
	if mine, err := s.ListInvoices(t1, ListFilter{Limit: 10}); err != nil || len(mine.Items) != 1 {
		t.Fatalf("tenant 1 lists %d invoices, err %v", len(mine.Items), err)
	}
	// Tenant 2 cannot credit tenant 1's invoice: the original is invisible to it.
	foreign := newProposal(t, le)
	foreign.Type, foreign.OriginalInvoiceID, foreign.Reason = domain.TypeCreditNote, a.ID, "x"
	foreign.Lines[0].OriginalLineNo = 1
	var p *httpx.Problem
	if _, err := s.CreateCreditNote(t2, foreign); !errors.As(err, &p) || p.Code != domain.CodeCreditExceeded {
		t.Fatalf("tenant 2 credit note on tenant 1's invoice: %v, want %s", err, domain.CodeCreditExceeded)
	}
}

// postedOriginal stores and posts a 2-line invoice (100 + 19 tax, 3 × 10 + 5.70
// tax) with 10.00 charges: gross 164.70.
func postedOriginal(t *testing.T, s *Postgres, ctx context.Context, le string) Invoice {
	t.Helper()
	inv := newProposal(t, le)
	second := domain.Line{No: 2, Description: "Parts", Qty: decimal.NewFromInt(3), UnitPrice: decimal.NewFromInt(10), RevenueAccount: "4010", TaxCode: "DE19"}
	inv.Lines = append(inv.Lines, Line{Line: second, Net: decimal.NewFromInt(30), Tax: decimal.RequireFromString("5.70")})
	inv.Totals = domain.Totals{Net: decimal.NewFromInt(130), Charges: decimal.NewFromInt(10), Tax: decimal.RequireFromString("24.70"), Gross: decimal.RequireFromString("164.70")}
	if _, err := s.CreateProposal(ctx, inv); err != nil {
		t.Fatal(err)
	}
	posted, err := s.PostProposal(ctx, inv.ID, time.Date(2027, 3, 12, 0, 0, 0, 0, time.UTC), 0)
	if err != nil {
		t.Fatal(err)
	}
	return posted
}

// creditNote plans a credit note against orig as the API does.
func creditNote(t *testing.T, s *Postgres, ctx context.Context, origID string, req []domain.CreditQty) (Invoice, error) {
	t.Helper()
	orig, err := s.GetInvoice(ctx, origID)
	if err != nil {
		t.Fatal(err)
	}
	original := make([]domain.PostedLine, len(orig.Lines))
	for i, l := range orig.Lines {
		original[i] = domain.PostedLine{Line: l.Line, Net: l.Net, Tax: l.Tax}
	}
	planned, totals, err := domain.PlanCredit(orig.Currency, original, orig.Totals, orig.Credited, req, nil)
	if err != nil {
		return Invoice{}, err
	}
	day := time.Date(2027, 3, 20, 0, 0, 0, 0, time.UTC)
	cn := Invoice{ID: ids.New(), LegalEntityID: orig.LegalEntityID, CustomerID: orig.CustomerID, InvoiceAccountID: orig.InvoiceAccountID,
		Type: domain.TypeCreditNote, Currency: orig.Currency, InvoiceDate: day, DueDate: day, Totals: totals,
		OriginalInvoiceID: orig.ID, Reason: "Damaged in transit", IdempotencyKey: ids.New(), RequestHash: "h"}
	for _, l := range planned {
		cn.Lines = append(cn.Lines, Line{Line: l.Line, Net: l.Net, Tax: l.Tax, OriginalLineNo: l.OriginalLineNo})
	}
	return s.CreateCreditNote(ctx, cn)
}

func TestCreditNote_PostNumbersChecksRemainderAndPublishes(t *testing.T) {
	admin, pool := testkit.DBWithApp(t)
	ctx := context.Background()
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	tctx := ctxAllEntities(testkit.TenantID(1))
	s := New(pool)
	le := ids.New()
	orig := postedOriginal(t, s, tctx, le)

	// Two proposals for 2 of the 3 parts each fit alone; only one can post.
	first, err := creditNote(t, s, tctx, orig.ID, []domain.CreditQty{{LineNo: 2, Qty: decimal.NewFromInt(2)}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := creditNote(t, s, tctx, orig.ID, []domain.CreditQty{{LineNo: 2, Qty: decimal.NewFromInt(2)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PostProposal(tctx, first.ID, time.Now(), 0); err == nil {
		t.Fatal("a credit note must not post through PostProposal")
	}
	postDay := time.Date(2027, 3, 21, 0, 0, 0, 0, time.UTC)
	if _, err := s.PostCreditNote(tctx, orig.ID, postDay, 0); err == nil {
		t.Fatal("an invoice must not post through PostCreditNote")
	}
	p1, err := s.PostCreditNote(tctx, first.ID, postDay, 1)
	if err != nil {
		t.Fatal(err)
	}
	if p1.Number != "CN-2027-0000001" || p1.OriginalNumber != orig.Number || !p1.Totals.Gross.Equal(decimal.RequireFromString("23.80")) {
		t.Fatalf("posted credit note %s for %s gross %s, want CN-2027-0000001 for %s gross 23.80", p1.Number, p1.OriginalNumber, p1.Totals.Gross, orig.Number)
	}
	_, err = s.PostCreditNote(tctx, second.ID, postDay, 0)
	var p *httpx.Problem
	if !errors.As(err, &p) || p.Status != http.StatusUnprocessableEntity || p.Code != domain.CodeCreditExceeded {
		t.Fatalf("overtaken credit note: got %v, want 422 %s", err, domain.CodeCreditExceeded)
	}

	after, err := s.GetInvoice(tctx, orig.ID)
	if err != nil {
		t.Fatal(err)
	}
	if c := after.Credited.Lines[2]; !c.Qty.Equal(decimal.NewFromInt(2)) || !c.Net.Equal(decimal.NewFromInt(20)) || !c.Tax.Equal(decimal.RequireFromString("3.80")) || !after.Credited.Gross.Equal(decimal.RequireFromString("23.80")) {
		t.Fatalf("credit usage %+v, want 2 parts, 20.00 net, 3.80 tax, 23.80 gross; the refused note must not count", after.Credited)
	}
	// The rest of the invoice credits to the cent and leaves nothing.
	rest, err := creditNote(t, s, tctx, orig.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !rest.Totals.Gross.Equal(decimal.RequireFromString("140.90")) {
		t.Fatalf("remainder %s, want 164.70 - 23.80 = 140.90", rest.Totals.Gross)
	}
	p2, err := s.PostCreditNote(tctx, rest.ID, postDay, 0)
	if err != nil || p2.Number != "CN-2027-0000002" {
		t.Fatalf("post the remainder: %s %v; the refused post must not consume a CN number", p2.Number, err)
	}
	if _, err := creditNote(t, s, tctx, orig.ID, nil); err == nil {
		t.Fatal("a fully credited invoice must not be credited again")
	}

	var payload struct {
		Type string `json:"type"`
		Data struct {
			CreditNoteID      string `json:"credit_note_id"`
			Number            string `json:"number"`
			OriginalInvoiceID string `json:"original_invoice_id"`
			OriginalNumber    string `json:"original_number"`
			Reason            string `json:"reason"`
			Lines             []struct {
				OriginalLineNo int    `json:"original_line_no"`
				Qty            string `json:"qty"`
				Tax            string `json:"tax"`
			} `json:"lines"`
			Totals struct {
				Gross string `json:"gross"`
			} `json:"totals"`
		} `json:"data"`
	}
	var raw []byte
	if err := admin.QueryRow(ctx, `select payload from outbox where aggregate_id = $1`, first.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	d := payload.Data
	if payload.Type != "dev.passage.billing.creditnote.posted.v1" || d.Number != p1.Number || d.OriginalInvoiceID != orig.ID || d.OriginalNumber != orig.Number ||
		d.Reason != "Damaged in transit" || d.Totals.Gross != "23.80" || len(d.Lines) != 1 || d.Lines[0].OriginalLineNo != 2 || d.Lines[0].Qty != "2" || d.Lines[0].Tax != "3.80" {
		t.Errorf("creditnote.posted payload %s %+v", payload.Type, d)
	}
	cnPage, err := s.ListInvoices(tctx, ListFilter{Type: domain.TypeCreditNote, OriginalInvoiceID: orig.ID, Limit: 10})
	cns := cnPage.Items
	if err != nil || len(cns) != 3 || cns[0].OriginalNumber != orig.Number {
		t.Fatalf("credit notes of the original: %d, err %v", len(cns), err)
	}
}

func TestCreditNote_OriginalMustBePosted(t *testing.T) {
	admin, pool := testkit.DBWithApp(t)
	ctx := context.Background()
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	tctx := ctxAllEntities(testkit.TenantID(1))
	s := New(pool)
	prop, err := s.CreateProposal(tctx, newProposal(t, ids.New()))
	if err != nil {
		t.Fatal(err)
	}
	cn := prop
	cn.ID, cn.Type, cn.OriginalInvoiceID, cn.Reason, cn.IdempotencyKey = ids.New(), domain.TypeCreditNote, prop.ID, "x", ids.New()
	for i := range cn.Lines {
		cn.Lines[i].OriginalLineNo = cn.Lines[i].No
	}
	_, err = s.CreateCreditNote(tctx, cn)
	var p *httpx.Problem
	if !errors.As(err, &p) || p.Code != domain.CodeCreditExceeded {
		t.Fatalf("credit note on a proposal: got %v, want %s", err, domain.CodeCreditExceeded)
	}
}

func TestInstallments_StoredAndPublished(t *testing.T) {
	admin, pool := testkit.DBWithApp(t)
	ctx := context.Background()
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	tctx := ctxAllEntities(testkit.TenantID(1))
	s := New(pool)
	inv := newProposal(t, ids.New())
	terms := []domain.InstallmentTerm{{Percent: decimal.NewFromInt(30), OffsetDays: 0}, {Percent: decimal.NewFromInt(70), OffsetDays: 30}}
	split, err := domain.SplitInstallments(inv.Currency, inv.Totals.Gross, inv.InvoiceDate, terms)
	if err != nil {
		t.Fatal(err)
	}
	for i, in := range split {
		inv.Installments = append(inv.Installments, Installment{Installment: in, Term: terms[i]})
	}
	if _, err := s.CreateProposal(tctx, inv); err != nil {
		t.Fatal(err)
	}
	posted, err := s.PostProposal(tctx, inv.ID, time.Date(2027, 3, 12, 0, 0, 0, 0, time.UTC), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(posted.Installments) != 2 || !posted.Installments[0].Amount.Equal(decimal.RequireFromString("35.70")) || posted.Installments[1].Term.OffsetDays != 30 {
		t.Fatalf("installments read back %+v", posted.Installments)
	}
	var payload struct {
		Data struct {
			Installments []struct {
				No      int    `json:"no"`
				Amount  string `json:"amount"`
				DueDate string `json:"due_date"`
			} `json:"installments"`
		} `json:"data"`
	}
	var raw []byte
	if err := admin.QueryRow(ctx, `select payload from outbox where aggregate_id = $1`, inv.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	got := payload.Data.Installments
	if len(got) != 2 || got[0].Amount != "35.70" || got[1].Amount != "83.30" || got[1].DueDate != "2027-04-09" {
		t.Errorf("invoice.posted installments %+v, want 35.70 due 03-10 and 83.30 due 04-09", got)
	}
}

func reserve(s *Postgres, ctx context.Context, origID, key string, lineNo int, qty int64) (CreditReservation, bool, error) {
	res := CreditReservation{ID: ids.New(), InvoiceID: origID, Reference: key, IdempotencyKey: key, RequestHash: "h-" + key}
	return s.ReserveCredit(ctx, res, []domain.CreditQty{{LineNo: lineNo, Qty: decimal.NewFromInt(qty)}}, nil)
}

func isCreditExceeded(err error) bool {
	var p *httpx.Problem
	return errors.As(err, &p) && p.Status == http.StatusUnprocessableEntity && p.Code == domain.CodeCreditExceeded
}

func TestCreditReservation_CountsAgainstCreditNotesBothWays(t *testing.T) {
	admin, pool := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	tctx := ctxAllEntities(testkit.TenantID(1))
	s := New(pool)
	orig := postedOriginal(t, s, tctx, ids.New())

	proposal, err := creditNote(t, s, tctx, orig.ID, []domain.CreditQty{{LineNo: 2, Qty: decimal.NewFromInt(2)}})
	if err != nil {
		t.Fatal(err)
	}
	res, replayed, err := reserve(s, tctx, orig.ID, "rma-1", 2, 2)
	if err != nil || replayed || res.Status != StatusReserved || !res.Totals.Gross.Equal(decimal.RequireFromString("23.80")) || len(res.Lines) != 1 {
		t.Fatalf("reserve: %+v replayed %v err %v, want 2 parts at 23.80 gross", res, replayed, err)
	}
	if _, err := s.PostCreditNote(tctx, proposal.ID, time.Date(2027, 3, 21, 0, 0, 0, 0, time.UTC), 0); !isCreditExceeded(err) {
		t.Fatalf("credit note proposed before the reservation posted after it: %v, want 422 %s", err, domain.CodeCreditExceeded)
	}
	var planErr *domain.Error
	if _, err := creditNote(t, s, tctx, orig.ID, []domain.CreditQty{{LineNo: 2, Qty: decimal.NewFromInt(2)}}); !errors.As(err, &planErr) || planErr.Code != domain.CodeCreditExceeded {
		t.Fatalf("credit note proposal over the reserved quantity: %v, want %s", err, domain.CodeCreditExceeded)
	}
	read, err := s.GetInvoice(tctx, orig.ID)
	if err != nil {
		t.Fatal(err)
	}
	if c := read.Credited.Lines[2]; !c.ReservedQty.Equal(decimal.NewFromInt(2)) || !read.Credited.Reserved.Equal(decimal.RequireFromString("23.80")) || !read.Credited.Gross.Equal(decimal.RequireFromString("23.80")) || !c.Qty.Equal(decimal.NewFromInt(2)) {
		t.Fatalf("invoice usage %+v, want 2 reserved parts and 23.80 reserved gross", read.Credited)
	}

	last, err := creditNote(t, s, tctx, orig.ID, []domain.CreditQty{{LineNo: 2, Qty: decimal.NewFromInt(1)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PostCreditNote(tctx, last.ID, time.Date(2027, 3, 21, 0, 0, 0, 0, time.UTC), 0); err != nil {
		t.Fatalf("the unreserved part posts: %v", err)
	}
	if _, _, err := reserve(s, tctx, orig.ID, "rma-2", 2, 1); !isCreditExceeded(err) {
		t.Fatalf("reserve after the credit note took the last part: %v, want 422 %s", err, domain.CodeCreditExceeded)
	}

	freed, changed, err := s.ReleaseCredit(tctx, res.ID)
	if err != nil || !changed || freed.Status != StatusReleased || freed.ReleasedAt.IsZero() {
		t.Fatalf("release: %+v %v", freed, err)
	}
	if again, changed, err := s.ReleaseCredit(tctx, res.ID); err != nil || changed || again.Status != StatusReleased {
		t.Fatalf("second release: %+v %v", again, err)
	}
	if _, _, err := reserve(s, tctx, orig.ID, "rma-2", 2, 2); err != nil {
		t.Fatalf("the released parts reserve again: %v", err)
	}
	var p *httpx.Problem
	if _, _, err := s.ReleaseCredit(tctx, ids.New()); !errors.As(err, &p) || p.Status != http.StatusNotFound {
		t.Fatalf("release unknown: %v, want 404", err)
	}
}

func TestCreditReservation_KeyReplayAndReuse(t *testing.T) {
	admin, pool := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	tctx := ctxAllEntities(testkit.TenantID(1))
	s := New(pool)
	orig := postedOriginal(t, s, tctx, ids.New())

	first, _, err := reserve(s, tctx, orig.ID, "rma-1", 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	again, replayed, err := reserve(s, tctx, orig.ID, "rma-1", 2, 1)
	if err != nil || !replayed || again.ID != first.ID || len(again.Lines) != 1 {
		t.Fatalf("replay: %+v replayed %v err %v, want the stored reservation %s", again, replayed, err, first.ID)
	}
	other := CreditReservation{ID: ids.New(), InvoiceID: orig.ID, Reference: "rma-1", IdempotencyKey: "rma-1", RequestHash: "another body"}
	var p *httpx.Problem
	if _, _, err := s.ReserveCredit(tctx, other, []domain.CreditQty{{LineNo: 2, Qty: decimal.NewFromInt(1)}}, nil); !errors.As(err, &p) || p.Code != CodeKeyReuse {
		t.Fatalf("same key, other body: %v, want %s", err, CodeKeyReuse)
	}
	var rows int
	if err := admin.QueryRow(context.Background(), `select count(*) from credit_reservation`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("%d reservations stored, want 1 (err %v)", rows, err)
	}
	if _, _, err := reserve(s, tctx, ids.New(), "rma-3", 2, 1); !errors.As(err, &p) || p.Status != http.StatusNotFound {
		t.Fatalf("unknown invoice: %v, want 404", err)
	}
	proposal := newProposal(t, ids.New())
	if _, err := s.CreateProposal(tctx, proposal); err != nil {
		t.Fatal(err)
	}
	if _, _, err := reserve(s, tctx, proposal.ID, "rma-4", 1, 1); !isCreditExceeded(err) {
		t.Fatalf("reserve against a proposal: %v, want 422 %s", err, domain.CodeCreditExceeded)
	}
}

func TestCreditReservation_ConcurrentReservationsAndCreditNotesNeverExceedTheInvoice(t *testing.T) {
	admin, pool := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	tctx := ctxAllEntities(testkit.TenantID(1))
	s := New(pool)
	orig := postedOriginal(t, s, tctx, ids.New())
	const attempts = 6
	proposals := make([]Invoice, attempts)
	for i := range proposals {
		cn, err := creditNote(t, s, tctx, orig.ID, []domain.CreditQty{{LineNo: 2, Qty: decimal.NewFromInt(1)}})
		if err != nil {
			t.Fatal(err)
		}
		proposals[i] = cn
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	won, refused := 0, 0
	record := func(err error) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case err == nil:
			won++
		case isCreditExceeded(err):
			refused++
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	for i := 0; i < attempts; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _, err := reserve(s, tctx, orig.ID, fmt.Sprintf("rma-%d", i), 2, 1)
			record(err)
		}()
		go func() {
			defer wg.Done()
			_, err := s.PostCreditNote(tctx, proposals[i].ID, time.Date(2027, 3, 21, 0, 0, 0, 0, time.UTC), 0)
			record(err)
		}()
	}
	wg.Wait()

	read, err := s.GetInvoice(tctx, orig.ID)
	if err != nil {
		t.Fatal(err)
	}
	c := read.Credited.Lines[2]
	if won != 3 || refused != 2*attempts-3 || !c.Qty.Equal(decimal.NewFromInt(3)) || c.Net.GreaterThan(decimal.NewFromInt(30)) || c.Tax.GreaterThan(decimal.RequireFromString("5.70")) || read.Credited.Gross.GreaterThan(orig.Totals.Gross) {
		t.Fatalf("%d credits won, %d refused, line usage %+v gross %s; the 3 parts of the line must be credited exactly once", won, refused, c, read.Credited.Gross)
	}
}

func TestCreditReservation_ConcurrentRetriesWithOneKeyReserveOnce(t *testing.T) {
	admin, pool := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	tctx := ctxAllEntities(testkit.TenantID(1))
	s := New(pool)
	orig := postedOriginal(t, s, tctx, ids.New())

	const retries = 8
	reservations := make([]CreditReservation, retries)
	errs := make([]error, retries)
	replays := make([]bool, retries)
	var wg sync.WaitGroup
	for i := 0; i < retries; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reservations[i], replays[i], errs[i] = reserve(s, tctx, orig.ID, "rma-same", 2, 2)
		}()
	}
	wg.Wait()
	created := 0
	for i := range reservations {
		if errs[i] != nil {
			t.Fatalf("retry %d: %v", i, errs[i])
		}
		if reservations[i].ID != reservations[0].ID {
			t.Errorf("retry %d got reservation %s, want %s", i, reservations[i].ID, reservations[0].ID)
		}
		if !replays[i] {
			created++
		}
	}
	read, err := s.GetInvoice(tctx, orig.ID)
	if err != nil {
		t.Fatal(err)
	}
	if created != 1 || !read.Credited.Lines[2].ReservedQty.Equal(decimal.NewFromInt(2)) {
		t.Fatalf("%d retries created a reservation, reserved quantity %s; want 1 and 2", created, read.Credited.Lines[2].ReservedQty)
	}
}

func isConflict(err error) bool {
	var p *httpx.Problem
	return errors.As(err, &p) && p.Status == http.StatusConflict && p.Code == CodeReservationState
}

func TestCreditReservation_ReleasedKeyRetriesUnderTheBound(t *testing.T) {
	admin, pool := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	tctx := ctxAllEntities(testkit.TenantID(1))
	s := New(pool)
	orig := postedOriginal(t, s, tctx, ids.New())

	first, _, err := reserve(s, tctx, orig.ID, "rma-1", 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ReleaseCredit(tctx, first.ID); err != nil {
		t.Fatal(err)
	}
	again, replayed, err := reserve(s, tctx, orig.ID, "rma-1", 2, 2)
	if err != nil || !replayed || again.ID != first.ID || again.Status != StatusReserved || !again.ReleasedAt.IsZero() || len(again.Lines) != 1 {
		t.Fatalf("retry after release: %+v replayed %v err %v, want %s RESERVED again", again, replayed, err, first.ID)
	}
	read, err := s.GetInvoice(tctx, orig.ID)
	if err != nil || !read.Credited.Lines[2].ReservedQty.Equal(decimal.NewFromInt(2)) {
		t.Fatalf("usage after re-reserve: %+v %v, want 2 reserved parts counted once", read.Credited, err)
	}

	if _, _, err := s.ReleaseCredit(tctx, first.ID); err != nil {
		t.Fatal(err)
	}
	taken, err := creditNote(t, s, tctx, orig.ID, []domain.CreditQty{{LineNo: 2, Qty: decimal.NewFromInt(2)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PostCreditNote(tctx, taken.ID, time.Date(2027, 3, 21, 0, 0, 0, 0, time.UTC), 0); err != nil {
		t.Fatalf("the released room posts: %v", err)
	}
	if _, _, err := reserve(s, tctx, orig.ID, "rma-1", 2, 2); !isCreditExceeded(err) {
		t.Fatalf("retry after a credit note used the room: %v, want 422 %s", err, domain.CodeCreditExceeded)
	}
	stored, _, err := s.ReleaseCredit(tctx, first.ID)
	if err != nil || stored.Status != StatusReleased {
		t.Fatalf("a refused retry must leave the reservation released: %+v %v", stored, err)
	}
}

func TestCreditReservation_CommitIsFinalAndKeyReplays(t *testing.T) {
	admin, pool := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	tctx := ctxAllEntities(testkit.TenantID(1))
	s := New(pool)
	orig := postedOriginal(t, s, tctx, ids.New())

	res, _, err := reserve(s, tctx, orig.ID, "rma-1", 2, 3)
	if err != nil {
		t.Fatal(err)
	}
	done, err := s.CommitCredit(tctx, res.ID)
	if err != nil || done.Status != StatusConsumed {
		t.Fatalf("commit: %+v %v", done, err)
	}
	if again, err := s.CommitCredit(tctx, res.ID); err != nil || again.Status != StatusConsumed {
		t.Fatalf("second commit: %+v %v", again, err)
	}
	if _, changed, err := s.ReleaseCredit(tctx, res.ID); !isConflict(err) || changed {
		t.Fatalf("release of a consumed reservation: %v, want 409 %s", err, CodeReservationState)
	}
	replay, replayed, err := reserve(s, tctx, orig.ID, "rma-1", 2, 3)
	if err != nil || !replayed || replay.Status != StatusConsumed {
		t.Fatalf("key on a consumed reservation: %+v replayed %v err %v, want the stored result", replay, replayed, err)
	}
	if _, _, err := reserve(s, tctx, orig.ID, "rma-2", 2, 1); !isCreditExceeded(err) {
		t.Fatalf("a consumed reservation keeps counting: %v, want 422 %s", err, domain.CodeCreditExceeded)
	}

	other, _, err := reserve(s, tctx, orig.ID, "rma-3", 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ReleaseCredit(tctx, other.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CommitCredit(tctx, other.ID); !isConflict(err) {
		t.Fatalf("commit of a released reservation: %v, want 409", err)
	}
	if _, err := s.CommitCredit(tctx, ids.New()); err == nil {
		t.Fatal("commit of an unknown reservation must fail")
	}
}

func TestCreditReservation_KeyOnAnotherInvoiceOrEntityIsReuse(t *testing.T) {
	admin, pool := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	tctx := ctxAllEntities(testkit.TenantID(1))
	s := New(pool)
	leA, leB := ids.New(), ids.New()
	a := postedOriginal(t, s, tctx, leA)
	b := postedOriginal(t, s, tctx, leB)
	if _, _, err := reserve(s, tctx, a.ID, "rma-1", 2, 1); err != nil {
		t.Fatal(err)
	}
	var p *httpx.Problem
	if _, _, err := reserve(s, tctx, b.ID, "rma-1", 2, 1); !errors.As(err, &p) || p.Status != http.StatusUnprocessableEntity || p.Code != CodeKeyReuse {
		t.Fatalf("key on another invoice: %v, want 422 %s", err, CodeKeyReuse)
	}
	scoped := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: leB, UserID: "u", Duties: testkit.ScopeDuties(leB)})
	if _, _, err := reserve(s, scoped, b.ID, "rma-1", 2, 1); !errors.As(err, &p) || p.Status != http.StatusUnprocessableEntity || p.Code != CodeKeyReuse {
		t.Fatalf("a key held in an entity the caller cannot see: %v, want the same 422 %s", err, CodeKeyReuse)
	}
}

func TestCreditReservation_ConcurrentCommitAndReleaseEndInOneState(t *testing.T) {
	admin, pool := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	tctx := ctxAllEntities(testkit.TenantID(1))
	s := New(pool)
	orig := postedOriginal(t, s, tctx, ids.New())
	for round := 0; round < 3; round++ {
		res, _, err := reserve(s, tctx, orig.ID, fmt.Sprintf("rma-%d", round), 2, 1)
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		var commitErr, releaseErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, commitErr = s.CommitCredit(tctx, res.ID)
		}()
		go func() {
			defer wg.Done()
			_, _, releaseErr = s.ReleaseCredit(tctx, res.ID)
		}()
		wg.Wait()
		var status string
		if err := admin.QueryRow(context.Background(), `select status from credit_reservation where id = $1`, res.ID).Scan(&status); err != nil {
			t.Fatal(err)
		}
		wonCommit, wonRelease := commitErr == nil, releaseErr == nil
		if wonCommit == wonRelease || (wonCommit && status != StatusConsumed) || (wonRelease && status != StatusReleased) || (!wonCommit && !isConflict(commitErr)) || (!wonRelease && !isConflict(releaseErr)) {
			t.Fatalf("round %d: commit %v release %v final %s; exactly one must win and the other get 409", round, commitErr, releaseErr, status)
		}
	}
}
