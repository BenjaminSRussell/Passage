package provider_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/tax/internal/domain"
	"github.com/BenjaminSRussell/passage/services/tax/internal/provider"
)

// The fixtures in testdata are written from the providers' published API documentation, not recorded from a live account:
// re-record them against a sandbox when the tenant has credentials (tracked in #512).
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func usDoc() domain.Document {
	return domain.Document{
		Currency: "USD", Direction: domain.DirOutput, DocumentDate: domain.Day("2026-03-15"), DocumentRef: "INV-2026-0042",
		ShipFromCountry: "US", Party: domain.Party{TaxGroup: "RETAIL", Country: "US"},
		Lines: []domain.Line{
			{LineRef: "L1", Amount: domain.D("200.00"), ItemTaxGroup: "P0000000"},
			{LineRef: "L2", Amount: domain.D("100.00"), ItemTaxGroup: "P0000000"},
		},
	}
}

type recorded struct {
	method, path, auth string
	body               map[string]any
}

func serve(t *testing.T, status int, answer []byte, got *recorded) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		got.method, got.path, got.auth = r.Method, r.URL.RequestURI(), r.Header.Get("Authorization")
		got.body = nil
		_ = json.Unmarshal(raw, &got.body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(answer)
	}))
}

func totals(t *testing.T, res domain.Result) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, c := range res.DocumentTaxesByCode {
		out[c.Code] = c.TaxAmount.String()
	}
	return out
}

func TestAvalaraCalculateMapsTheRecordedAnswer(t *testing.T) {
	var got recorded
	srv := serve(t, http.StatusOK, fixture(t, "avalara_create_us.json"), &got)
	defer srv.Close()
	av, err := provider.NewAvalara(srv.URL, "1100000000", "license", "ACME", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	res, err := av.Calculate(context.Background(), usDoc())
	if err != nil {
		t.Fatal(err)
	}
	if got.method != http.MethodPost || got.path != "/api/v2/transactions/create" || !strings.HasPrefix(got.auth, "Basic ") {
		t.Errorf("request = %+v", got)
	}
	if got.body["type"] != "SalesInvoice" || got.body["companyCode"] != "ACME" || got.body["code"] != "INV-2026-0042" || got.body["commit"] != false || got.body["currencyCode"] != "USD" {
		t.Errorf("request body = %+v", got.body)
	}
	if lines, _ := got.body["lines"].([]any); len(lines) != 2 {
		t.Errorf("lines sent = %v", got.body["lines"])
	}
	if res.Engine != "avalara" || len(res.Lines) != 2 || res.Lines[0].Base.String() != "200" || len(res.Lines[0].Taxes) != 2 {
		t.Fatalf("result = %+v", res)
	}
	if first := res.Lines[0].Taxes[0]; first.Code != "OH STATE TAX" || first.RatePct.String() != "5.75" || first.TaxAmount.String() != "11.5" || first.Direction != domain.DirOutput {
		t.Errorf("first tax = %+v", first)
	}
	if want := map[string]string{"OH STATE TAX": "17.25", "OH COUNTY TAX": "5.44"}; totals(t, res)["OH STATE TAX"] != want["OH STATE TAX"] || totals(t, res)["OH COUNTY TAX"] != want["OH COUNTY TAX"] {
		t.Errorf("document totals = %v, want %v", totals(t, res), want)
	}
}

func TestAvalaraQuotesWithoutARefAndPassesInclusivePricesOn(t *testing.T) {
	var got recorded
	srv := serve(t, http.StatusOK, fixture(t, "avalara_create_us.json"), &got)
	defer srv.Close()
	av, _ := provider.NewAvalara(srv.URL, "a", "k", "ACME", srv.Client())
	doc := usDoc()
	doc.DocumentRef = ""
	doc.Lines[0].PriceIncludesTax = true
	res, err := av.Calculate(context.Background(), doc)
	if err != nil {
		t.Fatal(err)
	}
	if got.body["type"] != "SalesOrder" {
		t.Errorf("a quote was sent as %v", got.body["type"])
	}
	if res.Lines[0].Base.String() != "185.5" {
		t.Errorf("an inclusive line's base = %s, want the amount less its tax (185.5)", res.Lines[0].Base)
	}
	doc.Direction = domain.DirInput
	if _, err := av.Calculate(context.Background(), doc); err != nil || got.body["type"] != "PurchaseOrder" {
		t.Errorf("a purchase quote: %v, type %v", err, got.body["type"])
	}
}

func TestAvalaraRefusalIsAClientErrorAndAnOutageIsNot(t *testing.T) {
	var got recorded
	srv := serve(t, http.StatusBadRequest, fixture(t, "avalara_error.json"), &got)
	av, _ := provider.NewAvalara(srv.URL, "a", "k", "ACME", srv.Client())
	_, err := av.Calculate(context.Background(), usDoc())
	var pe *domain.ProviderError
	if !errors.As(err, &pe) || pe.Status != http.StatusBadRequest || !strings.Contains(pe.Error(), "Invalid country code") {
		t.Fatalf("a 400 answered %v", err)
	}
	srv.Close()

	down := serve(t, http.StatusServiceUnavailable, []byte(`{"error":{"message":"maintenance"}}`), &got)
	defer down.Close()
	av, _ = provider.NewAvalara(down.URL, "a", "k", "ACME", down.Client())
	cfg := domain.Config{Groups: map[string][]string{"RETAIL": {"VAT"}}, ItemGroups: map[string][]string{"P0000000": {"VAT"}},
		Codes: []domain.Code{{Code: "VAT", Jurisdiction: "US", Rates: []domain.Rate{{ValidFrom: domain.Day("2020-01-01"), RatePct: domain.D("10")}}}}}
	res, notice, err := domain.CalculateWithFailover(context.Background(), av, usDoc(), cfg, time.Second)
	if err != nil || !res.Fallback || notice == nil || notice.Provider != "avalara" {
		t.Errorf("a 503 did not fall back to the internal engine: %v %+v %+v", err, res.Fallback, notice)
	}
}

func TestAvalaraCommitVoidAndAddressRoutes(t *testing.T) {
	var got recorded
	srv := serve(t, http.StatusOK, []byte(`{}`), &got)
	defer srv.Close()
	av, _ := provider.NewAvalara(srv.URL, "a", "k", "ACME", srv.Client())
	ctx := context.Background()
	if err := av.Commit(ctx, "INV/1"); err != nil || got.path != "/api/v2/companies/ACME/transactions/INV%2F1/commit" || got.body["commit"] != true {
		t.Errorf("commit: %v %+v", err, got)
	}
	if err := av.Void(ctx, "INV-1"); err != nil || got.path != "/api/v2/companies/ACME/transactions/INV-1/void" || got.body["code"] != "DocVoided" {
		t.Errorf("void: %v %+v", err, got)
	}
	if err := av.Commit(ctx, ""); err == nil {
		t.Error("commit without a ref was accepted")
	}
	if err := av.ValidateAddress(ctx, domain.Address{Line1: "1 Main", City: "Columbus", Region: "OH", PostalCode: "43215", Country: "US"}); err != nil ||
		!strings.HasPrefix(got.path, "/api/v2/addresses/resolve?") || !strings.Contains(got.path, "postalCode=43215") {
		t.Errorf("resolve: %v %+v", err, got)
	}
	bad := serve(t, http.StatusOK, fixture(t, "avalara_resolve_error.json"), &got)
	defer bad.Close()
	av, _ = provider.NewAvalara(bad.URL, "a", "k", "ACME", bad.Client())
	var pe *domain.ProviderError
	if err := av.ValidateAddress(ctx, domain.Address{Line1: "1 Nowhere", Country: "US"}); !errors.As(err, &pe) || pe.Status != http.StatusUnprocessableEntity {
		t.Errorf("an undeliverable address: %v", err)
	}
}

func TestAvalaraFromEnv(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if av, err := provider.AvalaraFromEnv(env(nil)); av != nil || err != nil {
		t.Errorf("unset: %v %v", av, err)
	}
	if _, err := provider.AvalaraFromEnv(env(map[string]string{"AVALARA_URL": "https://x"})); err == nil {
		t.Error("a URL without credentials was accepted")
	}
	if av, err := provider.AvalaraFromEnv(env(map[string]string{"AVALARA_URL": "https://x", "AVALARA_ACCOUNT_ID": "a", "AVALARA_LICENSE_KEY": "k", "AVALARA_COMPANY_CODE": "C"})); err != nil || av.Name() != "avalara" {
		t.Errorf("configured: %v", err)
	}
}

func vertexServer(t *testing.T, tokens *atomic.Int32, sale *recorded, saleStatus int, answer []byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			tokens.Add(1)
			_ = r.ParseForm()
			if r.Form.Get("client_id") != "cid" || r.Form.Get("grant_type") != "password" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write(fixture(t, "vertex_token.json"))
			return
		}
		raw, _ := io.ReadAll(r.Body)
		sale.method, sale.path, sale.auth = r.Method, r.URL.Path, r.Header.Get("Authorization")
		sale.body = nil
		_ = json.Unmarshal(raw, &sale.body)
		w.WriteHeader(saleStatus)
		_, _ = w.Write(answer)
	}))
}

func TestVertexCalculateAuthenticatesOnceAndMapsTheAnswer(t *testing.T) {
	var tokens atomic.Int32
	var sale recorded
	srv := vertexServer(t, &tokens, &sale, http.StatusOK, fixture(t, "vertex_sale_us.json"))
	defer srv.Close()
	vx, err := provider.NewVertex(srv.URL, srv.URL+"/token", "cid", "secret", "user", "pw", "ACME", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		res, err := vx.Calculate(context.Background(), usDoc())
		if err != nil {
			t.Fatal(err)
		}
		if res.Engine != "vertex" || len(res.Lines) != 2 || res.Lines[1].Taxes[1].TaxAmount.String() != "2.44" || res.Lines[1].Taxes[1].RatePct.String() != "2.44" {
			t.Fatalf("result = %+v", res)
		}
		if got := totals(t, res); got["General Sales and Use Tax"] != "17.25" || got["County Sales and Use Tax"] != "5.44" {
			t.Fatalf("totals = %v", got)
		}
	}
	if tokens.Load() != 1 {
		t.Errorf("fetched %d tokens for two calls, want 1", tokens.Load())
	}
	if sale.method != http.MethodPost || sale.path != "/vertex-restapi/v2/sale" || sale.auth != "Bearer vx-token-1" || sale.body["saleMessageType"] != "INVOICE" || sale.body["documentNumber"] != "INV-2026-0042" {
		t.Errorf("sale request = %+v", sale)
	}
	doc := usDoc()
	doc.DocumentRef = ""
	if _, err := vx.Calculate(context.Background(), doc); err != nil || sale.body["saleMessageType"] != "QUOTATION" {
		t.Errorf("a quote: %v, type %v", err, sale.body["saleMessageType"])
	}
}

func TestVertexRefusedLoginIsAnOutageNotARefusalOfTheDocument(t *testing.T) {
	var tokens atomic.Int32
	var sale recorded
	srv := vertexServer(t, &tokens, &sale, http.StatusOK, nil)
	defer srv.Close()
	vx, _ := provider.NewVertex(srv.URL, srv.URL+"/token", "wrong", "secret", "user", "pw", "ACME", srv.Client())
	_, err := vx.Calculate(context.Background(), usDoc())
	var pe *domain.ProviderError
	if !errors.As(err, &pe) || pe.Status < 500 {
		t.Fatalf("a refused login = %v, want a 5xx the failover masks", err)
	}
	if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "pw") {
		t.Errorf("the error leaks a credential: %v", err)
	}
}

func TestVertexRefusalVoidAndUnsupportedAddress(t *testing.T) {
	var tokens atomic.Int32
	var sale recorded
	srv := vertexServer(t, &tokens, &sale, http.StatusBadRequest, []byte(`{"errors":[{"code":"INVALID_COUNTRY","message":"Unknown country"}]}`))
	defer srv.Close()
	vx, _ := provider.NewVertex(srv.URL, srv.URL+"/token", "cid", "secret", "user", "pw", "ACME", srv.Client())
	ctx := context.Background()
	var pe *domain.ProviderError
	if _, err := vx.Calculate(ctx, usDoc()); !errors.As(err, &pe) || pe.Status != http.StatusBadRequest {
		t.Errorf("a 400 answered %v", err)
	}
	if err := vx.Void(ctx, "INV-1"); !errors.As(err, &pe) {
		t.Errorf("void passed on the answer: %v", err)
	}
	if sale.body["saleMessageType"] != "ROLLBACK" || sale.body["documentNumber"] != "INV-1" {
		t.Errorf("void body = %+v", sale.body)
	}
	if err := vx.Commit(ctx, "INV-1"); err != nil {
		t.Errorf("commit: %v", err)
	}
	if err := vx.ValidateAddress(ctx, domain.Address{}); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("address validation: %v", err)
	}
}

func TestVertexFromEnv(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if vx, err := provider.VertexFromEnv(env(nil)); vx != nil || err != nil {
		t.Errorf("unset: %v %v", vx, err)
	}
	if _, err := provider.VertexFromEnv(env(map[string]string{"VERTEX_URL": "https://x"})); err == nil {
		t.Error("a URL without credentials was accepted")
	}
}

// A base URL or token URL that is not https would send licence keys and tax data in the clear (#615 review).
func TestProviders_RequireHTTPSExceptForLoopback(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	for _, base := range []string{"http://rest.avatax.com", "ftp://x", "rest.avatax.com", "https://", ""} {
		if _, err := provider.NewAvalara(base, "a", "k", "ACME", nil); err == nil {
			t.Errorf("avalara accepted base URL %q", base)
		}
		if _, err := provider.NewVertex(base, "https://tok.example/oauth", "c", "s", "u", "p", "CO", nil); err == nil {
			t.Errorf("vertex accepted base URL %q", base)
		}
	}
	if _, err := provider.NewVertex("https://vertex.example", "http://tok.example/oauth", "c", "s", "u", "p", "CO", nil); err == nil {
		t.Error("vertex accepted a plain http token URL")
	}
	for _, base := range []string{"https://rest.avatax.com", "http://127.0.0.1:8080", "http://localhost:9"} {
		if _, err := provider.NewAvalara(base, "a", "k", "ACME", nil); err != nil {
			t.Errorf("avalara refused %q: %v", base, err)
		}
	}
	if _, err := provider.AvalaraFromEnv(env(map[string]string{"AVALARA_URL": "http://tax.example.com", "AVALARA_ACCOUNT_ID": "a", "AVALARA_LICENSE_KEY": "k", "AVALARA_COMPANY_CODE": "C"})); err == nil {
		t.Error("AvalaraFromEnv accepted http")
	}
}
