package taxreport_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/billing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/billing/internal/store"
	"github.com/BenjaminSRussell/passage/services/billing/internal/taxreport"
	"github.com/BenjaminSRussell/passage/services/billing/migrations"
)

func setup(t *testing.T) (*store.Postgres, *pgxpool.Pool) {
	t.Helper()
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return store.New(app), admin
}

func tenantCtx(n int) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(n), UserID: "user-test", Duties: testkit.ScopeDuties("")})
}

func postedInvoice(t *testing.T, s *store.Postgres, ctx context.Context, le string) store.Invoice {
	t.Helper()
	day := time.Date(2027, 3, 10, 0, 0, 0, 0, time.UTC)
	line := domain.Line{No: 1, Description: "Support", Qty: decimal.NewFromInt(1), UnitPrice: decimal.NewFromInt(100), RevenueAccount: "4000", TaxCode: "DE19"}
	_, totals, err := domain.Compute("EUR", []domain.Line{line}, []decimal.Decimal{decimal.NewFromInt(19)}, decimal.Zero)
	if err != nil {
		t.Fatal(err)
	}
	inv := store.Invoice{ID: ids.New(), LegalEntityID: le, CustomerID: ids.New(), InvoiceAccountID: ids.New(), Type: domain.TypeFreeText, Status: domain.StatusProposal,
		Currency: "EUR", InvoiceDate: day, DueDate: day.AddDate(0, 0, 30), PaymentTermDays: 30, Totals: totals, IdempotencyKey: ids.New(), RequestHash: "h",
		Lines: []store.Line{{Line: line, Net: decimal.NewFromInt(100), Tax: decimal.NewFromInt(19)}}}
	if _, err := s.CreateProposal(ctx, inv); err != nil {
		t.Fatal(err)
	}
	posted, err := s.PostProposal(ctx, inv.ID, day.AddDate(0, 0, 2), 0)
	if err != nil {
		t.Fatal(err)
	}
	return posted
}

func bookedInvoice(t *testing.T, s *store.Postgres, ctx context.Context, le string) store.Invoice {
	t.Helper()
	inv := postedInvoice(t, s, ctx, le)
	if _, err := s.MarkGLBooked(ctx, inv.ID, ids.New(), nil); err != nil {
		t.Fatal(err)
	}
	return inv
}

type txn struct {
	ID        string `json:"id"`
	Code      string `json:"code"`
	Direction string `json:"direction"`
	TaxDate   string `json:"tax_date"`
	Amount    string `json:"amount"`
}

type call struct {
	path   string
	raw    []byte
	claims tenant.ServiceClaims
}

type fakeTax struct {
	verifier *tenant.ServiceVerifier
	mu       sync.Mutex
	statuses []int
	calls    []call
}

func (f *fakeTax) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	claims, err := f.verifier.Verify(r.Header.Get(tenant.HeaderServiceToken))
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	raw, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.calls = append(f.calls, call{path: r.URL.Path, raw: raw, claims: claims})
	status := f.statuses[0]
	if len(f.statuses) > 1 {
		f.statuses = f.statuses[1:]
	}
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if status >= 400 {
		_, _ = w.Write([]byte(`{"code":"COM-0001","detail":"the tax service says no"}`))
		return
	}
	_, _ = w.Write([]byte(`{"recorded":1,"duplicates":0}`))
}

func (f *fakeTax) seen() []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func newReporter(t *testing.T, s *store.Postgres, statuses ...int) (*taxreport.Reporter, *fakeTax) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := tenant.NewServiceSigner("billing", priv)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeTax{verifier: tenant.NewServiceVerifier("tax", map[string]ed25519.PublicKey{"billing": pub}), statuses: statuses}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	client, err := taxreport.New(srv.URL, signer, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	return &taxreport.Reporter{Store: s, Tax: client, MaxAttempts: 3}, fake
}

func runOnce(t *testing.T, r *taxreport.Reporter) int {
	t.Helper()
	n, err := r.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	return n
}

func makeDue(t *testing.T, admin *pgxpool.Pool) {
	t.Helper()
	if _, err := admin.Exec(context.Background(), `update tax_transaction_report set next_attempt_at = now() where status = 'PENDING'`); err != nil {
		t.Fatal(err)
	}
}

type row struct {
	status, lastError string
	attempts          int
}

func reportOf(t *testing.T, admin *pgxpool.Pool, invoiceID string) row {
	t.Helper()
	var r row
	if err := admin.QueryRow(context.Background(), `select status, attempts, coalesce(last_error, '') from tax_transaction_report where invoice_id = $1`, invoiceID).Scan(&r.status, &r.attempts, &r.lastError); err != nil {
		t.Fatal(err)
	}
	return r
}

// #29: a booked invoice is recorded with the tax service as service:billing-tax-report with only tax.transaction.record, for its
// tenant and legal entity; a 503 is retried in back-off with the same body (same transaction ids), and a report the tax service took
// is never sent again.
func TestReporter_BookedInvoiceIsRecordedOnceAcrossRetries(t *testing.T) {
	s, admin := setup(t)
	ctx := tenantCtx(1)
	le := ids.New()
	inv := bookedInvoice(t, s, ctx, le)
	r, fake := newReporter(t, s, http.StatusServiceUnavailable, http.StatusCreated)

	if n := runOnce(t, r); n != 1 {
		t.Fatalf("first run made %d attempts, want 1", n)
	}
	if got := reportOf(t, admin, inv.ID); got.status != store.TaxReportPending || got.attempts != 1 || !strings.Contains(got.lastError, "503") {
		t.Fatalf("after an outage: %+v", got)
	}
	if n := runOnce(t, r); n != 0 {
		t.Fatalf("a report in back-off was sent again (%d attempts)", n)
	}
	makeDue(t, admin)
	if n := runOnce(t, r); n != 1 {
		t.Fatalf("retry run made %d attempts, want 1", n)
	}
	if got := reportOf(t, admin, inv.ID); got.status != store.TaxReportReported || got.attempts != 2 {
		t.Fatalf("after the retry: %+v", got)
	}
	makeDue(t, admin)
	if n := runOnce(t, r); n != 0 {
		t.Fatalf("a reported document was sent again (%d attempts)", n)
	}

	calls := fake.seen()
	if len(calls) != 2 || !bytes.Equal(calls[0].raw, calls[1].raw) {
		t.Fatalf("calls = %d, bodies equal = %v", len(calls), len(calls) == 2 && bytes.Equal(calls[0].raw, calls[1].raw))
	}
	var body struct {
		Transactions []txn `json:"transactions"`
	}
	if err := json.Unmarshal(calls[0].raw, &body); err != nil || len(body.Transactions) != 1 {
		t.Fatalf("body %s: %v", calls[0].raw, err)
	}
	if tx := body.Transactions[0]; tx.Code != "DE19" || tx.Direction != "output" || tx.Amount != "19.00" || tx.TaxDate != "2027-03-12" || !ids.Valid(tx.ID) {
		t.Errorf("transaction = %+v", tx)
	}
	c := calls[0].claims
	if calls[0].path != "/v1/tax/transactions" || c.Tid != testkit.TenantID(1) || c.Le != le || !slices.Equal(c.Duties, []string{"tax.transaction.record"}) || c.Act != "service:billing-tax-report" {
		t.Errorf("path %s claims %+v", calls[0].path, c)
	}
}

// #29: a credit note reports its tax negative.
func TestReporter_CreditNoteIsRecordedNegative(t *testing.T) {
	s, _ := setup(t)
	ctx := tenantCtx(1)
	orig := bookedInvoice(t, s, ctx, ids.New())
	cn := creditNoteOf(t, s, ctx, orig)
	if _, err := s.MarkGLBooked(ctx, cn.ID, ids.New(), nil); err != nil {
		t.Fatal(err)
	}
	r, fake := newReporter(t, s, http.StatusCreated)
	if n := runOnce(t, r); n != 2 {
		t.Fatalf("attempts = %d, want the invoice's and the credit note's", n)
	}
	var amounts []string
	for _, c := range fake.seen() {
		var body struct {
			Transactions []txn `json:"transactions"`
		}
		if err := json.Unmarshal(c.raw, &body); err != nil || len(body.Transactions) != 1 {
			t.Fatalf("body %s: %v", c.raw, err)
		}
		amounts = append(amounts, body.Transactions[0].Amount)
	}
	slices.Sort(amounts)
	if !slices.Equal(amounts, []string{"-19.00", "19.00"}) {
		t.Fatalf("amounts = %v, want the invoice's 19.00 and the credit note's -19.00", amounts)
	}
}

func creditNoteOf(t *testing.T, s *store.Postgres, ctx context.Context, orig store.Invoice) store.Invoice {
	t.Helper()
	original, err := s.GetInvoice(ctx, orig.ID)
	if err != nil {
		t.Fatal(err)
	}
	posted := make([]domain.PostedLine, len(original.Lines))
	for i, l := range original.Lines {
		posted[i] = domain.PostedLine{Line: l.Line, Net: l.Net, Tax: l.Tax}
	}
	planned, totals, err := domain.PlanCredit(original.Currency, posted, original.Totals, original.Credited, []domain.CreditQty{{LineNo: 1, Qty: decimal.NewFromInt(1)}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	day := time.Date(2027, 3, 20, 0, 0, 0, 0, time.UTC)
	cn := store.Invoice{ID: ids.New(), LegalEntityID: original.LegalEntityID, CustomerID: original.CustomerID, InvoiceAccountID: original.InvoiceAccountID,
		Type: domain.TypeCreditNote, Currency: original.Currency, InvoiceDate: day, DueDate: day, Totals: totals, OriginalInvoiceID: original.ID,
		Reason: "Returned", IdempotencyKey: ids.New(), RequestHash: "h"}
	for _, l := range planned {
		cn.Lines = append(cn.Lines, store.Line{Line: l.Line, Net: l.Net, Tax: l.Tax, OriginalLineNo: l.OriginalLineNo})
	}
	if _, err := s.CreateCreditNote(ctx, cn); err != nil {
		t.Fatal(err)
	}
	postedCN, err := s.PostCreditNote(ctx, cn.ID, day, 0)
	if err != nil {
		t.Fatal(err)
	}
	return postedCN
}

// #29: a tax service that stays down is retried with back-off and parked when the attempts run out, visible to the operator with
// its reason; an operator retry puts it back and it is then recorded. A 400 is a refusal no retry can change and parks at once.
func TestReporter_OutageParksAfterTheAttemptsAndOperatorRetryRecoversIt(t *testing.T) {
	s, admin := setup(t)
	ctx := tenantCtx(1)
	inv := bookedInvoice(t, s, ctx, ids.New())
	r, fake := newReporter(t, s, http.StatusServiceUnavailable, http.StatusServiceUnavailable, http.StatusServiceUnavailable, http.StatusCreated)
	for range 3 {
		runOnce(t, r)
		makeDue(t, admin)
	}
	got := reportOf(t, admin, inv.ID)
	if got.status != store.TaxReportParked || got.attempts != 3 || !strings.Contains(got.lastError, "gave up after 3 attempts") {
		t.Fatalf("after three outages: %+v", got)
	}
	var sb strings.Builder
	r.WriteMetrics(&sb)
	if !strings.Contains(sb.String(), "billing_tax_reports_parked_total 1") {
		t.Errorf("metrics = %q", sb.String())
	}
	if n := runOnce(t, r); n != 0 {
		t.Fatalf("a parked report was sent (%d)", n)
	}
	parked, err := s.ListTaxReports(ctx, store.TaxReportParked, 10)
	if err != nil || len(parked) != 1 || parked[0].LastError == nil {
		t.Fatalf("operator list = %+v, %v", parked, err)
	}
	if _, err := s.RetryTaxReport(ctx, parked[0].ID); err != nil {
		t.Fatal(err)
	}
	if n := runOnce(t, r); n != 1 {
		t.Fatalf("attempts after the operator retry = %d", n)
	}
	if got := reportOf(t, admin, inv.ID); got.status != store.TaxReportReported {
		t.Fatalf("after the operator retry: %+v", got)
	}
	if len(fake.seen()) != 4 {
		t.Errorf("calls = %d, want 4", len(fake.seen()))
	}

	refused := bookedInvoice(t, s, ctx, ids.New())
	r2, fake2 := newReporter(t, s, http.StatusBadRequest)
	runOnce(t, r2)
	if got := reportOf(t, admin, refused.ID); got.status != store.TaxReportParked || got.attempts != 1 || !strings.Contains(got.lastError, "tax refused the transactions (400)") {
		t.Fatalf("after a refusal: %+v", got)
	}
	if len(fake2.seen()) != 1 {
		t.Errorf("a refusal was retried: %d calls", len(fake2.seen()))
	}
}

// #29: nothing is sent for a document that is posted but not booked to the GL, and each tenant's reports go out under its own
// tenant.
func TestReporter_SendsNothingUnbookedAndKeepsTenantsApart(t *testing.T) {
	s, _ := setup(t)
	postedInvoice(t, s, tenantCtx(1), ids.New())
	r, fake := newReporter(t, s, http.StatusCreated)
	if n := runOnce(t, r); n != 0 || len(fake.seen()) != 0 {
		t.Fatalf("an unbooked invoice was sent: %d attempts, %d calls", n, len(fake.seen()))
	}
	bookedInvoice(t, s, tenantCtx(1), ids.New())
	bookedInvoice(t, s, tenantCtx(2), ids.New())
	if n := runOnce(t, r); n != 2 {
		t.Fatalf("attempts = %d, want 2", n)
	}
	var tenants []string
	for _, c := range fake.seen() {
		tenants = append(tenants, c.claims.Tid)
	}
	slices.Sort(tenants)
	if !slices.Equal(tenants, []string{testkit.TenantID(1), testkit.TenantID(2)}) {
		t.Errorf("tenants = %v", tenants)
	}
}

func TestFromEnv_NeedsASigningKeyOnlyWhenConfigured(t *testing.T) {
	if c, err := taxreport.FromEnv(func(string) string { return "" }); c != nil || err != nil {
		t.Fatalf("unset TAX_URL = %v, %v", c, err)
	}
	if _, err := taxreport.FromEnv(func(k string) string {
		if k == "TAX_URL" {
			return "http://tax.invalid"
		}
		return ""
	}); err == nil {
		t.Fatal("TAX_URL without a signing key was accepted")
	}
}
