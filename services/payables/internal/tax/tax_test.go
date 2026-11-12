package tax_test

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/payables/internal/tax"
)

const entity = "00000000-0000-0000-0000-0000000000a1"

func ctx() context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: entity, UserID: "u1", Duties: map[string]struct{}{"payment-proposal.write": {}}})
}

func signed(t *testing.T) (*tenant.ServiceSigner, *tenant.ServiceVerifier) {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(nil)
	signer, err := tenant.NewServiceSigner("payables", priv)
	if err != nil {
		t.Fatal(err)
	}
	return signer, tenant.NewServiceVerifier("tax", map[string]ed25519.PublicKey{"payables": pub})
}

func request() tax.Request {
	return tax.Request{
		Currency: "USD", PaymentDate: "2026-01-10", Gross: decimal.RequireFromString("98"), PaidToDate: decimal.RequireFromString("20"),
		Code: tax.Code{Code: "WHT30", Jurisdiction: "US", IncomeType: "services", Rates: []tax.Rate{{ValidFrom: "2020-01-01", RatePct: "30", Threshold: ptr("10")}}},
	}
}

func ptr(s string) *string { return &s }

func TestWithholding_SendsTheCodeInlineWithASignedTokenAndParsesTheAmount(t *testing.T) {
	signer, verifier := signed(t)
	var got http.Header
	var body map[string]any
	var method, path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, method, path = r.Header.Clone(), r.Method, r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{"withholding_amount":"29.4","net_payable":"68.6"}`))
	}))
	defer srv.Close()

	amount, err := tax.New(srv.URL+"/", nil).WithSigner(signer).Withholding(ctx(), entity, request())

	if err != nil || amount.String() != "29.4" || method != http.MethodPost || path != "/v1/tax/withholding/calculate" {
		t.Fatalf("amount %s err %v %s %s", amount, err, method, path)
	}
	codes := body["codes"].([]any)
	rate := codes[0].(map[string]any)["rates"].([]any)[0].(map[string]any)
	if body["currency"] != "USD" || body["party_type"] != "vendor" || body["payment_date"] != "2026-01-10" || body["code"] != "WHT30" || body["gross_amount"] != "98" || body["paid_to_date"] != "20" || len(codes) != 1 ||
		rate["valid_from"] != "2020-01-01" || rate["rate_pct"] != "30" || rate["threshold"] != "10" || rate["valid_to"] != nil {
		t.Fatalf("body = %v", body)
	}
	for _, h := range []string{tenant.HeaderTenant, tenant.HeaderLegalEntity, tenant.HeaderUser, tenant.HeaderDuties} {
		if got.Get(h) != "" {
			t.Errorf("%s must not be sent with a service token", h)
		}
	}
	claims, err := verifier.Verify(got.Get(tenant.HeaderServiceToken))
	if err != nil || claims.Aud != "tax" || claims.Le != entity || len(claims.Duties) != 1 || claims.Duties[0] != "tax.withholding.calculate" {
		t.Fatalf("claims %+v err %v", claims, err)
	}
}

func TestWithholding_Failures(t *testing.T) {
	signer, _ := signed(t)
	tests := map[string]struct {
		status int
		body   string
		check  func(error) bool
	}{
		"rule problem":   {http.StatusUnprocessableEntity, `{"code":"TAX-0001","serviceCode":"TAX-0001"}`, func(err error) bool { var re *tax.RuleError; return errors.As(err, &re) && re.Code == "TAX-0001" }},
		"forbidden":      {http.StatusForbidden, "", func(err error) bool { var se *tax.StatusError; return errors.As(err, &se) && se.Status == 403 }},
		"server error":   {http.StatusInternalServerError, "", func(err error) bool { var se *tax.StatusError; return errors.As(err, &se) && se.Status == 500 }},
		"bad body":       {http.StatusOK, "nope", func(err error) bool { return err != nil }},
		"above gross":    {http.StatusOK, `{"withholding_amount":"99"}`, func(err error) bool { return err != nil }},
		"negative":       {http.StatusOK, `{"withholding_amount":"-1"}`, func(err error) bool { return err != nil }},
		"missing amount": {http.StatusOK, `{}`, func(err error) bool { return err != nil }},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			if _, err := tax.New(srv.URL, nil).WithSigner(signer).Withholding(ctx(), entity, request()); !tc.check(err) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestWithholding_ScopeRefusalAndMissingPrincipalAreConfigFaults(t *testing.T) {
	signer, _ := signed(t)
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer srv.Close()
	c := tax.New(srv.URL, nil).WithSigner(signer)
	if _, err := c.Withholding(ctx(), "00000000-0000-0000-0000-0000000000b2", request()); !errors.Is(err, tax.ErrConfig) || called {
		t.Fatalf("scope refusal = %v called=%v", err, called)
	}
	if _, err := c.Withholding(context.Background(), entity, request()); !errors.Is(err, tax.ErrConfig) || called {
		t.Fatalf("no principal = %v called=%v", err, called)
	}
}

func TestWithholding_NeedsASignerAndNeverFollowsRedirects(t *testing.T) {
	leaked := false
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { leaked = true }))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer redirector.Close()
	if _, err := tax.New(redirector.URL, nil).Withholding(ctx(), entity, request()); !errors.Is(err, tax.ErrUnsigned) || leaked {
		t.Fatalf("unsigned err = %v leaked=%v", err, leaked)
	}
	signer, _ := signed(t)
	_, err := tax.New(redirector.URL, nil).WithSigner(signer).Withholding(ctx(), entity, request())
	var se *tax.StatusError
	if !errors.As(err, &se) || se.Status != http.StatusFound || leaked {
		t.Fatalf("err=%v leaked=%v, want the 302 returned without following it", err, leaked)
	}
}

func TestCalculate_ReturnsTheTaxableBaseAndFallsBackToTheGross(t *testing.T) {
	signer, _ := signed(t)
	for name, tc := range map[string]struct {
		body, base string
		bad        bool
	}{
		"with a base":     {body: `{"withholding_amount":"3","taxable_base":"10"}`, base: "10"},
		"without a base":  {body: `{"withholding_amount":"3"}`, base: "98"},
		"base over gross": {body: `{"withholding_amount":"3","taxable_base":"99"}`, bad: true},
		"tax over base":   {body: `{"withholding_amount":"11","taxable_base":"10"}`, bad: true},
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(tc.body)) }))
			defer srv.Close()
			res, err := tax.New(srv.URL, nil).WithSigner(signer).Calculate(ctx(), entity, request())
			if tc.bad {
				if err == nil {
					t.Fatalf("an unusable base was accepted: %+v", res)
				}
				return
			}
			if err != nil || res.TaxableBase.String() != tc.base || res.Withheld.String() != "3" {
				t.Fatalf("result %+v err %v", res, err)
			}
		})
	}
}

func TestRecordWithholding_PostsTheEntriesWithTheRecordDutyOnly(t *testing.T) {
	signer, verifier := signed(t)
	var path string
	var body struct {
		Entries []map[string]any `json:"entries"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		if claims, err := verifier.Verify(r.Header.Get(tenant.HeaderServiceToken)); err != nil || len(claims.Duties) != 1 || claims.Duties[0] != "tax.withholding.record" {
			t.Errorf("token claims %+v err %v", claims, err)
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	e := tax.Entry{ID: "0190f0a0-0000-7000-8000-000000000001", PartyRef: "v1", Code: "WHT30", PaymentDate: "2026-01-10", Currency: "USD",
		Gross: decimal.RequireFromString("98"), TaxableBase: decimal.RequireFromString("98"), Withheld: decimal.RequireFromString("29.4")}
	if err := tax.New(srv.URL, nil).WithSigner(signer).RecordWithholding(ctx(), entity, []tax.Entry{e}); err != nil {
		t.Fatal(err)
	}
	if path != "/v1/tax/withholding/entries" || len(body.Entries) != 1 || body.Entries[0]["withholding_amount"] != "29.4" || body.Entries[0]["party_type"] != "vendor" {
		t.Errorf("path %s body %v", path, body)
	}
	if err := tax.New(srv.URL, nil).RecordWithholding(ctx(), entity, []tax.Entry{e}); !errors.Is(err, tax.ErrUnsigned) {
		t.Errorf("unsigned: %v", err)
	}
	if err := tax.New(srv.URL, nil).WithSigner(signer).RecordWithholding(ctx(), entity, nil); err != nil {
		t.Errorf("nothing to record: %v", err)
	}
}
