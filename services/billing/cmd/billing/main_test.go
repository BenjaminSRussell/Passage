package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/billing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/billing/internal/store"
	"github.com/BenjaminSRussell/passage/services/billing/migrations"
)

func runEnv(t *testing.T, extra map[string]string) func(string) string {
	t.Helper()
	url := os.Getenv("PASSAGE_TEST_APP_DATABASE_URL")
	if url == "" {
		t.Skip("PASSAGE_TEST_APP_DATABASE_URL is not set")
	}
	seed := make([]byte, ed25519.SeedSize)
	_, _ = rand.Read(seed)
	m := map[string]string{
		"DATABASE_URL": url, "HTTP_ADDR": "127.0.0.1:0", "ADMIN_ADDR": "127.0.0.1:0", "PASSAGE_SERVICE_NAME": "billing",
		"PASSAGE_SERVICE_SIGNING_KEY": base64.StdEncoding.EncodeToString(seed),
	}
	for k, v := range extra {
		m[k] = v
	}
	return func(k string) string { return m[k] }
}

func profilesJSON(t *testing.T, tenantID string) string {
	t.Helper()
	b, err := json.Marshal([]map[string]any{{
		"tenantId": tenantID, "legalEntityId": ids.New(), "ledgerId": ids.New(), "receivableAccount": "1200", "taxAccount": "2300",
		"chargeAccount": "4800", "deferredRevenueAccounts": []string{"4100"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRun_AutoBookEnvStartsTheWorkerAgainstTheConfiguredLedger(t *testing.T) {
	var asked atomic.Int64
	ledger := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer ledger.Close()
	tenantID := ids.New()
	getenv := runEnv(t, map[string]string{
		"LEDGER_URL": ledger.URL, "REVREC_URL": "http://revrec.invalid", "BILLING_GL_PROFILES": profilesJSON(t, tenantID),
		"BILLING_GL_AUTOBOOK_TENANT_IDS": tenantID, "BILLING_GL_AUTOBOOK_INTERVAL": "1m",
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, getenv) }()
	deadline := time.After(10 * time.Second)
	for asked.Load() == 0 {
		select {
		case err := <-done:
			t.Fatalf("run stopped before the worker reached the ledger: %v", err)
		case <-deadline:
			t.Fatal("the auto-book worker never called the ledger")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Errorf("run after cancel: %v", err)
	}
}

func TestRun_AutoBookEnvWithoutALedgerFailsStartup(t *testing.T) {
	getenv := runEnv(t, map[string]string{"BILLING_GL_AUTOBOOK_TENANT_IDS": ids.New()})
	err := run(context.Background(), getenv)
	if err == nil || !strings.Contains(err.Error(), "BILLING_GL_AUTOBOOK_TENANT_IDS needs the ledger configured") {
		t.Fatalf("err = %v, want the auto-book-needs-a-ledger error", err)
	}
}

func postedOrderInvoice(t *testing.T, st *store.Postgres, tenantID, orderID, orderLineID string) store.Invoice {
	t.Helper()
	day := time.Date(2027, 3, 10, 0, 0, 0, 0, time.UTC)
	line := domain.Line{No: 1, Description: "Goods", Qty: decimal.NewFromInt(2), UnitPrice: decimal.NewFromInt(10), RevenueAccount: "4000", TaxCode: "DE19"}
	_, totals, err := domain.Compute("EUR", []domain.Line{line}, []decimal.Decimal{decimal.Zero}, decimal.Zero)
	if err != nil {
		t.Fatal(err)
	}
	src := domain.Source{PackingSlipID: ids.New(), PackingSlipLineID: ids.New(), PackingSlipDate: day, OrderID: orderID, OrderLineID: orderLineID, ProductID: ids.New(), PriceExplainRef: "px"}
	inv := store.Invoice{ID: ids.New(), LegalEntityID: ids.New(), CustomerID: ids.New(), InvoiceAccountID: ids.New(), Type: domain.TypeShipment, Status: domain.StatusProposal,
		Currency: "EUR", InvoiceDate: day, DueDate: day.AddDate(0, 0, 30), PaymentTermDays: 30, Totals: totals, IdempotencyKey: ids.New(), RequestHash: "h",
		Lines: []store.Line{{Line: line, Net: line.Net("EUR"), Tax: decimal.Zero, Source: src}}}
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: tenantID, UserID: "user-test", Duties: testkit.ScopeDuties("")})
	if _, err := st.CreateProposal(ctx, inv); err != nil {
		t.Fatal(err)
	}
	posted, err := st.PostProposal(ctx, inv.ID, day.AddDate(0, 0, 2), 0)
	if err != nil {
		t.Fatal(err)
	}
	return posted
}

func TestRun_SalesURLStartsTheInvoiceReporterAgainstTheConfiguredSales(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	var schema string
	if err := admin.QueryRow(context.Background(), "select current_schema()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	tenantID, orderID, orderLineID := testkit.TenantID(1), ids.New(), ids.New()
	inv := postedOrderInvoice(t, store.New(app), tenantID, orderID, orderLineID)

	seed := make([]byte, ed25519.SeedSize)
	_, _ = rand.Read(seed)
	pub := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	verifier := tenant.NewServiceVerifier("sales", map[string]ed25519.PublicKey{"billing": pub})
	type received struct {
		path, key string
		body      map[string]any
		claims    tenant.ServiceClaims
	}
	got := make(chan received, 4)
	sales := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, err := verifier.Verify(r.Header.Get(tenant.HeaderServiceToken))
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		got <- received{path: r.URL.Path, key: r.Header.Get("Idempotency-Key"), body: body, claims: claims}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer sales.Close()

	getenv := runEnv(t, map[string]string{
		"SALES_URL": sales.URL, "BILLING_SALES_REPORT_INTERVAL": "1s", "PASSAGE_SERVICE_SIGNING_KEY": base64.StdEncoding.EncodeToString(seed),
		"DATABASE_URL": os.Getenv("PASSAGE_TEST_APP_DATABASE_URL") + "&search_path=" + schema + ",public",
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, getenv) }()
	select {
	case r := <-got:
		if r.path != "/v1/sales/orders/"+orderID+":record-progress" || r.key != store.SalesReportKeyPrefix+inv.ID+":"+orderID || r.body["sourceRef"] != inv.Number || r.body["kind"] != "INVOICED" ||
			r.claims.Tid != tenantID || r.claims.Le != inv.LegalEntityID || !slices.Equal(r.claims.Duties, []string{"sales.order.record-invoice"}) {
			t.Errorf("sales received %+v", r)
		}
	case err := <-done:
		t.Fatalf("run stopped before the reporter reached sales: %v", err)
	case <-time.After(15 * time.Second):
		t.Fatal("the invoice reporter never called sales")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		var status string
		if err := admin.QueryRow(context.Background(), `select status from sales_invoice_report where invoice_id = $1`, inv.ID).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status == store.SalesReportReported {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("report is %s after sales answered 201, want REPORTED", status)
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Errorf("run after cancel: %v", err)
	}
}

func TestRun_SalesReporterNeedsASigningKeyAndAValidInterval(t *testing.T) {
	err := run(context.Background(), runEnv(t, map[string]string{"SALES_URL": "http://sales.invalid", "PASSAGE_SERVICE_SIGNING_KEY": ""}))
	if err == nil || !strings.Contains(err.Error(), "sales client") {
		t.Fatalf("SALES_URL without a signing key: err = %v", err)
	}
	err = run(context.Background(), runEnv(t, map[string]string{"SALES_URL": "http://sales.invalid", "BILLING_SALES_REPORT_INTERVAL": "100ms"}))
	if err == nil || !strings.Contains(err.Error(), "BILLING_SALES_REPORT_INTERVAL") {
		t.Fatalf("a sub-second interval: err = %v", err)
	}
}
