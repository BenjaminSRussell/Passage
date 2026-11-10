package salesreport_test

import (
	"cmp"
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
	"github.com/BenjaminSRussell/passage/services/billing/internal/salesreport"
	"github.com/BenjaminSRussell/passage/services/billing/internal/store"
	"github.com/BenjaminSRussell/passage/services/billing/migrations"
)

func setup(t *testing.T) (*store.Postgres, *pgxpool.Pool, context.Context) {
	t.Helper()
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), UserID: "user-test", Duties: testkit.ScopeDuties("")})
	return store.New(app), admin, ctx
}

type billed struct{ order, orderLine, qty string }

// postInvoice stores and posts a shipment invoice with one line per entry of billed (a zero order is a line without a source).
func postInvoice(t *testing.T, s *store.Postgres, ctx context.Context, le string, lines ...billed) store.Invoice {
	t.Helper()
	day := time.Date(2027, 3, 10, 0, 0, 0, 0, time.UTC)
	inv := store.Invoice{ID: ids.New(), LegalEntityID: le, CustomerID: ids.New(), InvoiceAccountID: ids.New(), Type: domain.TypeShipment, Status: domain.StatusProposal,
		Currency: "EUR", InvoiceDate: day, DueDate: day.AddDate(0, 0, 30), PaymentTermDays: 30, IdempotencyKey: ids.New(), RequestHash: "h"}
	var taxes []decimal.Decimal
	var dl []domain.Line
	for i, b := range lines {
		b.qty = cmp.Or(b.qty, "1")
		line := domain.Line{No: i + 1, Description: "Goods", Qty: decimal.RequireFromString(b.qty), UnitPrice: decimal.NewFromInt(10), RevenueAccount: "4000", TaxCode: "DE19"}
		l := store.Line{Line: line, Net: line.Net("EUR"), Tax: decimal.Zero}
		if b.order != "" {
			l.Source = domain.Source{PackingSlipID: ids.New(), PackingSlipLineID: ids.New(), PackingSlipDate: day.AddDate(0, 0, -1), OrderID: b.order, OrderLineID: b.orderLine, ProductID: ids.New(), PriceExplainRef: "px"}
		}
		inv.Lines = append(inv.Lines, l)
		dl = append(dl, line)
		taxes = append(taxes, decimal.Zero)
	}
	_, totals, err := domain.Compute("EUR", dl, taxes, decimal.Zero)
	if err != nil {
		t.Fatal(err)
	}
	inv.Totals = totals
	if _, err := s.CreateProposal(ctx, inv); err != nil {
		t.Fatal(err)
	}
	posted, err := s.PostProposal(ctx, inv.ID, day.AddDate(0, 0, 2), 0)
	if err != nil {
		t.Fatal(err)
	}
	return posted
}

type call struct {
	path, key string
	body      map[string]any
	claims    tenant.ServiceClaims
}

type fakeSales struct {
	t        *testing.T
	verifier *tenant.ServiceVerifier
	mu       sync.Mutex
	statuses []int
	calls    []call
}

func (f *fakeSales) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	claims, err := f.verifier.Verify(r.Header.Get(tenant.HeaderServiceToken))
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		f.t.Errorf("body is not JSON: %s", raw)
	}
	f.mu.Lock()
	f.calls = append(f.calls, call{path: r.URL.Path, key: r.Header.Get("Idempotency-Key"), body: body, claims: claims})
	status := f.statuses[0]
	if len(f.statuses) > 1 {
		f.statuses = f.statuses[1:]
	}
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	switch {
	case status >= 500:
		_, _ = w.Write([]byte(`{"code":"COM-0500","detail":"sales is down"}`))
	case status >= 400:
		_, _ = w.Write([]byte(`{"code":"SLS-0059","detail":"invoice quantity is above what was delivered"}`))
	default:
		_, _ = w.Write([]byte(`{"id":"` + ids.New() + `"}`))
	}
}

func (f *fakeSales) seen() []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func newReporter(t *testing.T, s *store.Postgres, statuses ...int) (*salesreport.Reporter, *fakeSales) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := tenant.NewServiceSigner("billing", priv)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeSales{t: t, verifier: tenant.NewServiceVerifier("sales", map[string]ed25519.PublicKey{"billing": pub}), statuses: statuses}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	client, err := salesreport.New(srv.URL, signer, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	return &salesreport.Reporter{Store: s, Sales: client, MaxAttempts: 3}, fake
}

func runOnce(t *testing.T, r *salesreport.Reporter, ctx context.Context) int {
	t.Helper()
	n, err := r.RunOnce(context.WithoutCancel(ctx))
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	return n
}

func makeDue(t *testing.T, admin *pgxpool.Pool) {
	t.Helper()
	if _, err := admin.Exec(context.Background(), `update sales_invoice_report set next_attempt_at = now() where status = 'PENDING'`); err != nil {
		t.Fatal(err)
	}
}

type row struct {
	status, key, lastError string
	attempts               int
	lastStatus             *int
}

func reports(t *testing.T, admin *pgxpool.Pool, invoiceID string) map[string]row {
	t.Helper()
	rows, err := admin.Query(context.Background(), `select order_id::text, status, idempotency_key, attempts, last_status, coalesce(last_error, '') from sales_invoice_report where invoice_id = $1`, invoiceID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]row{}
	for rows.Next() {
		var order string
		var r row
		if err := rows.Scan(&order, &r.status, &r.key, &r.attempts, &r.lastStatus, &r.lastError); err != nil {
			t.Fatal(err)
		}
		out[order] = r
	}
	return out
}

// #29: posting an invoice queues one INVOICED report per order it bills lines of, in the post transaction: two invoice lines of
// one order line are summed (sales takes a line once per report), a line without an order source is left out, and an invoice
// without any order line queues nothing. The reporter sends each report signed as billing for service:billing-sales-report with
// only sales.order.record-invoice, for the invoice's tenant and legal entity, under the key billing-invoice:{invoice}:{order};
// a 503 is retried in back-off with the same body and key, and a report sales took is never sent again.
func TestReporter_ReportsAPostedInvoiceOncePerOrder(t *testing.T) {
	s, admin, ctx := setup(t)
	le := ids.New()
	orderA, orderB := ids.New(), ids.New()
	lineA1, lineA2, lineB := ids.New(), ids.New(), ids.New()
	inv := postInvoice(t, s, ctx, le, billed{orderA, lineA1, "2"}, billed{orderB, lineB, "1.5"}, billed{orderA, lineA1, "1"}, billed{orderA, lineA2, "4"}, billed{})
	plain := postInvoice(t, s, ctx, le, billed{})
	if got := reports(t, admin, plain.ID); len(got) != 0 {
		t.Fatalf("an invoice without order lines queued %v", got)
	}
	queued := reports(t, admin, inv.ID)
	if len(queued) != 2 || queued[orderA].status != store.SalesReportPending || queued[orderB].key != store.SalesReportKeyPrefix+inv.ID+":"+orderB {
		t.Fatalf("queued reports = %+v", queued)
	}

	r, fake := newReporter(t, s, http.StatusServiceUnavailable, http.StatusCreated)
	if n := runOnce(t, r, ctx); n != 2 {
		t.Fatalf("first run made %d attempts, want 2", n)
	}
	var retrying row
	pending := 0
	for _, got := range reports(t, admin, inv.ID) {
		if got.status == store.SalesReportPending {
			retrying = got
			pending++
		}
	}
	if pending != 1 || retrying.attempts != 1 || retrying.lastStatus == nil || *retrying.lastStatus != 503 || !strings.Contains(retrying.lastError, "sales is down") {
		t.Fatalf("after one 503 and one 201, pending = %d, %+v", pending, retrying)
	}
	if n := runOnce(t, r, ctx); n != 0 {
		t.Fatalf("a report in its back-off or lease was sent again (%d attempts)", n)
	}
	makeDue(t, admin)
	if n := runOnce(t, r, ctx); n != 1 {
		t.Fatalf("retry run made %d attempts, want 1", n)
	}
	makeDue(t, admin)
	if n := runOnce(t, r, ctx); n != 0 {
		t.Fatalf("a reported invoice was sent again (%d attempts)", n)
	}
	for order, got := range reports(t, admin, inv.ID) {
		if got.status != store.SalesReportReported {
			t.Errorf("order %s = %+v, want REPORTED", order, got)
		}
	}

	calls := fake.seen()
	if len(calls) != 3 {
		t.Fatalf("sales got %d calls, want 3", len(calls))
	}
	want := map[string]any{
		orderA: map[string]any{"kind": "INVOICED", "sourceRef": inv.Number, "lines": []any{
			map[string]any{"lineId": min(lineA1, lineA2), "quantity": map[string]string{lineA1: "3", lineA2: "4"}[min(lineA1, lineA2)]},
			map[string]any{"lineId": max(lineA1, lineA2), "quantity": map[string]string{lineA1: "3", lineA2: "4"}[max(lineA1, lineA2)]},
		}},
		orderB: map[string]any{"kind": "INVOICED", "sourceRef": inv.Number, "lines": []any{map[string]any{"lineId": lineB, "quantity": "1.5"}}},
	}
	keys := map[string][]string{}
	for i, c := range calls {
		order := strings.TrimSuffix(strings.TrimPrefix(c.path, "/v1/sales/orders/"), ":record-progress")
		wantJSON, _ := json.Marshal(want[order])
		if got, _ := json.Marshal(c.body); string(got) != string(wantJSON) {
			t.Errorf("call %d body:\n got %s\nwant %s", i, got, wantJSON)
		}
		if c.key != store.SalesReportKeyPrefix+inv.ID+":"+order {
			t.Errorf("call %d key %q", i, c.key)
		}
		if c.claims.Iss != "billing" || c.claims.Act != tenant.ServiceActor(salesreport.PrincipalName) || c.claims.Le != le ||
			c.claims.Tid != testkit.TenantID(1) || !slices.Equal(c.claims.Duties, []string{"sales.order.record-invoice"}) {
			t.Errorf("call %d token = %+v", i, c.claims)
		}
		keys[order] = append(keys[order], c.key)
	}
	if len(keys[orderA])+len(keys[orderB]) != 3 || len(keys[orderA]) == len(keys[orderB]) {
		t.Errorf("calls per order = %v, want one order sent twice (503 then 201) and the other once", keys)
	}
}

// #29: sales refusing a report (4xx) parks it at once with a plain reason; a refused token and a dead sales are retried in
// back-off and park once the attempts run out; none of it touches the posted invoice; a stale attempt cannot overwrite a newer
// lease.
func TestReporter_ParksRefusalsAndRunsOutOfAttempts(t *testing.T) {
	s, admin, ctx := setup(t)
	le := ids.New()

	refused := postInvoice(t, s, ctx, le, billed{ids.New(), ids.New(), "1"})
	r, _ := newReporter(t, s, http.StatusUnprocessableEntity)
	if n := runOnce(t, r, ctx); n != 1 {
		t.Fatalf("refusal run made %d attempts", n)
	}
	for _, got := range reports(t, admin, refused.ID) {
		if got.status != store.SalesReportParked || got.attempts != 1 || !strings.Contains(got.lastError, "sales refused the invoice report (422): SLS-0059 invoice quantity is above what was delivered") {
			t.Fatalf("after a 422 = %+v", got)
		}
	}
	makeDue(t, admin)
	if n := runOnce(t, r, ctx); n != 0 {
		t.Fatalf("a parked report was sent again (%d attempts)", n)
	}

	tokenRefused := postInvoice(t, s, ctx, le, billed{ids.New(), ids.New(), "2"})
	r, fake := newReporter(t, s, http.StatusForbidden)
	for range 3 {
		makeDue(t, admin)
		runOnce(t, r, ctx)
	}
	for _, got := range reports(t, admin, tokenRefused.ID) {
		if got.status != store.SalesReportParked || got.attempts != 3 || !strings.HasPrefix(got.lastError, "gave up after 3 attempts: sales answered 403") {
			t.Fatalf("after three 403s = %+v", got)
		}
	}
	if len(fake.seen()) != 3 {
		t.Fatalf("a refused token was attempted %d times, want 3", len(fake.seen()))
	}

	down := postInvoice(t, s, ctx, le, billed{ids.New(), ids.New(), "3"})
	dead := httptest.NewServer(http.NotFoundHandler())
	_, priv, _ := ed25519.GenerateKey(nil)
	signer, _ := tenant.NewServiceSigner("billing", priv)
	client, err := salesreport.New(dead.URL, signer, dead.Client())
	if err != nil {
		t.Fatal(err)
	}
	dead.Close()
	downReporter := &salesreport.Reporter{Store: s, Sales: client, MaxAttempts: 3}
	if n := runOnce(t, downReporter, ctx); n != 1 {
		t.Fatalf("outage run made %d attempts", n)
	}
	for _, got := range reports(t, admin, down.ID) {
		if got.status != store.SalesReportPending || got.attempts != 1 || got.lastStatus != nil || !strings.Contains(got.lastError, "sales request") {
			t.Fatalf("after an outage = %+v", got)
		}
	}
	if posted, err := s.GetInvoice(ctx, down.ID); err != nil || posted.Status != domain.StatusPosted {
		t.Fatalf("an unreachable sales changed the invoice: %+v, %v", posted, err)
	}

	stale := postInvoice(t, s, ctx, le, billed{ids.New(), ids.New(), "4"})
	tctx := store.SalesReportSystemContext(ctx, testkit.TenantID(1))
	if _, err := admin.Exec(ctx, `update sales_invoice_report set next_attempt_at = now() where invoice_id = $1`, stale.ID); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimSalesReports(tctx, 50, time.Minute)
	if err != nil || len(claimed) != 1 || claimed[0].Attempts != 1 {
		t.Fatalf("claim = %+v, %v", claimed, err)
	}
	if again, err := s.ClaimSalesReports(tctx, 50, time.Minute); err != nil || len(again) != 0 {
		t.Fatalf("a leased report was claimed twice: %+v, %v", again, err)
	}
	if _, err := admin.Exec(ctx, `update sales_invoice_report set attempts = attempts + 1 where id = $1`, claimed[0].ID); err != nil {
		t.Fatal(err)
	}
	err = s.FinishSalesReport(tctx, claimed[0].ID, claimed[0].Attempts, store.SalesReportOutcome{Status: store.SalesReportReported, HTTPStatus: 201})
	if err != store.ErrStaleSalesReport {
		t.Fatalf("a stale finish = %v, want ErrStaleSalesReport", err)
	}
}

func TestBackoff(t *testing.T) {
	cases := map[int]time.Duration{0: 30 * time.Second, 1: 30 * time.Second, 2: time.Minute, 3: 2 * time.Minute, 8: time.Hour, 20: time.Hour}
	for n, want := range cases {
		if got := salesreport.Backoff(n); got != want {
			t.Errorf("Backoff(%d) = %v, want %v", n, got, want)
		}
	}
}

func TestFromEnv(t *testing.T) {
	if c, err := salesreport.FromEnv(func(string) string { return "" }); c != nil || err != nil {
		t.Fatalf("no SALES_URL = %v, %v; want no client", c, err)
	}
	if _, err := salesreport.FromEnv(func(k string) string {
		if k == "SALES_URL" {
			return "http://sales"
		}
		return ""
	}); err == nil || !strings.Contains(err.Error(), "SIGNING_KEY") {
		t.Fatalf("SALES_URL without a signing key = %v", err)
	}
}
