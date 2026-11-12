package taxrate

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

func TestRatePct_AsksForTheTaxOfTenThousandWithTheStoredConfig(t *testing.T) {
	var got calculateBody
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != calculatePath || r.Header.Get(tenant.HeaderServiceToken) == "" {
			t.Errorf("path %s, token %q", r.URL.Path, r.Header.Get(tenant.HeaderServiceToken))
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"document_taxes_by_code":[{"code":"VAT-RED","tax_amount":"810.00"}]}`))
	}))
	defer srv.Close()
	_, priv, _ := ed25519.GenerateKey(nil)
	signer, err := tenant.NewServiceSigner("edi", priv)
	if err != nil {
		t.Fatal(err)
	}
	c, err := New(srv.URL, signer, nil)
	if err != nil {
		t.Fatal(err)
	}
	sp, err := tenant.ServicePrincipal("edi-selfbill", "00000000-0000-0000-0000-000000000007", "00000000-0000-0000-0000-0000000000e1")
	if err != nil {
		t.Fatal(err)
	}
	ctx := tenant.With(context.Background(), sp)
	rate, err := c.RatePct(ctx, Query{LegalEntityID: "00000000-0000-0000-0000-0000000000e1", Currency: "CHF", Date: time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC),
		Country: "CH", TaxGroup: "DOM", ItemTaxGroup: "RED"})
	if err != nil || rate.String() != "8.1" {
		t.Fatalf("rate %s, %v", rate, err)
	}
	if !got.StoredConfig || got.Direction != "output" || got.TaxDate != "2026-11-11" || got.Party.Country != "CH" || got.Lines[0].Amount != "10000" || got.Lines[0].ItemTaxGroup != "RED" {
		t.Fatalf("request %+v", got)
	}
}

func TestParseRate_RefusesWhatIsNotARate(t *testing.T) {
	for _, raw := range []string{`nope`, `{"document_taxes_by_code":[{"tax_amount":"x"}]}`, `{"document_taxes_by_code":[{"tax_amount":"-1"}]}`, `{"document_taxes_by_code":[{"tax_amount":"20000"}]}`} {
		if _, err := parseRate([]byte(raw)); err == nil {
			t.Errorf("%s accepted", raw)
		}
	}
	if r, err := parseRate([]byte(`{"document_taxes_by_code":[]}`)); err != nil || !r.IsZero() {
		t.Fatalf("no tax is a zero rate: %s %v", r, err)
	}
}
