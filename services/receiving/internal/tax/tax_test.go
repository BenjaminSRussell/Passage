package tax_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/tax"
)

func TestClient_CalculateInput(t *testing.T) {
	var sawDuty string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawDuty = r.Header.Get(tenant.HeaderDuties)
		body, _ := io.ReadAll(r.Body)
		if r.URL.Path != "/v1/tax/calculate" || !strings.Contains(string(body), `"direction":"input"`) || !strings.Contains(string(body), `"code":"VAT19"`) {
			t.Errorf("request %s %s", r.URL.Path, body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"document_taxes_by_code":[{"code":"VAT19","direction":"input","tax_amount":"95.00"}]}`))
	}))
	t.Cleanup(srv.Close)
	c := tax.New(srv.URL, tax.Config{Code: "VAT19", RatePct: decimal.RequireFromString("19")}, nil)
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: testkit.TenantID(9)})
	got, err := c.CalculateInput(ctx, testkit.TenantID(9), domain.TaxDocument{
		Currency: "EUR", Date: "2026-11-03", Lines: []domain.TaxDocumentLine{{Ref: "1", Amount: decimal.RequireFromString("500")}},
	})
	if err != nil || got.Code != "VAT19" || !got.Amount.Equal(decimal.RequireFromString("95.00")) || sawDuty != "Calculate tax" {
		t.Fatalf("got %+v err %v duty %q", got, err, sawDuty)
	}
}

func TestClient_CalculateInputSkipsWhenConfigIsNotReady(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("tax must not be called when the rate is not configured")
	}))
	t.Cleanup(srv.Close)
	c := tax.New(srv.URL, tax.Config{Code: "VAT19"}, nil)
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: testkit.TenantID(9)})
	got, err := c.CalculateInput(ctx, testkit.TenantID(9), domain.TaxDocument{Currency: "EUR", Date: "2026-11-03"})
	if err != nil || got.Code != "" || !got.Amount.IsZero() {
		t.Fatalf("got %+v err %v", got, err)
	}
}

func TestClient_RecordInput(t *testing.T) {
	var saw string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		saw = r.URL.Path + " " + r.Header.Get(tenant.HeaderDuties) + " " + string(body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"recorded":1}`))
	}))
	t.Cleanup(srv.Close)
	c := tax.New(srv.URL, tax.Config{Code: "VAT19", RatePct: decimal.RequireFromString("19")}, nil)
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: testkit.TenantID(9)})
	id := "11111111-1111-4111-8111-111111111111"
	if err := c.RecordInput(ctx, testkit.TenantID(9), domain.TaxTxn{ID: id, Code: "VAT19", Date: "2026-11-03", Amount: decimal.RequireFromString("95")}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(saw, "/v1/tax/transactions") || !strings.Contains(saw, "tax.transaction.record") || !strings.Contains(saw, id) {
		t.Fatalf("saw %s", saw)
	}
}
