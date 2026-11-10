package billingclient

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

func signedEnv(url string) func(string) string {
	seed := base64.StdEncoding.EncodeToString(make([]byte, ed25519.SeedSize))
	m := map[string]string{"BILLING_URL": url, tenant.EnvServiceName: "receivables", tenant.EnvServiceSigningKey: seed}
	return func(k string) string { return m[k] }
}

func TestTaxBasis_SumsBilledLessCreditedTaxPerCodeWithTheReadDuty(t *testing.T) {
	var path, token string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, token = r.URL.Path, r.Header.Get(tenant.HeaderServiceToken)
		_, _ = w.Write([]byte(`{"number":"INV-1","legalEntityId":"le","status":"POSTED","type":"SHIPMENT","lines":[
			{"taxCode":"DE19","tax":"190.00","creditedTax":"38.00"},{"taxCode":"DE19","tax":"10.00"},{"taxCode":"DE7","tax":"7.00"},{"tax":"0"}]}`))
	}))
	defer srv.Close()
	c, err := FromEnv(signedEnv(srv.URL))
	if err != nil || c == nil {
		t.Fatalf("client: %v %v", c, err)
	}
	le, id := ids.New(), ids.New()
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: ids.New(), LegalEntityID: le, UserID: "ar-clerk"})
	b, err := c.TaxBasis(ctx, le, id)
	if err != nil || path != "/v1/billing/invoices/"+id || token == "" || b.Number != "INV-1" ||
		!b.Remaining["DE19"].Equal(decimal.RequireFromString("162.00")) || !b.Remaining["DE7"].Equal(decimal.RequireFromString("7.00")) || len(b.Remaining) != 2 {
		t.Fatalf("basis %+v path %s token %q err %v", b, path, token, err)
	}
}

func TestTaxBasis_MissingCreditNoteAndErrorsAreDistinguished(t *testing.T) {
	status, body := http.StatusNotFound, `{}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	c, _ := FromEnv(signedEnv(srv.URL))
	le := ids.New()
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: ids.New(), LegalEntityID: le, UserID: "ar-clerk"})
	if _, err := c.TaxBasis(ctx, le, ids.New()); err != ErrNotFound {
		t.Fatalf("404: %v", err)
	}
	status, body = http.StatusOK, `{"number":"CN-1","type":"CREDIT_NOTE","lines":[]}`
	if _, err := c.TaxBasis(ctx, le, ids.New()); err != ErrNotFound {
		t.Fatalf("a credit note is not an invoice: %v", err)
	}
	status = http.StatusBadGateway
	if _, err := c.TaxBasis(ctx, le, ids.New()); err == nil || err == ErrNotFound {
		t.Fatalf("502: %v", err)
	}
}

func TestFromEnv_UnsetIsNilAndURLWithoutKeyIsRefused(t *testing.T) {
	if c, err := FromEnv(func(string) string { return "" }); c != nil || err != nil {
		t.Fatalf("unset: %v %v", c, err)
	}
	if c, err := FromEnv(func(k string) string {
		if k == "BILLING_URL" {
			return "http://billing"
		}
		return ""
	}); c != nil || err == nil {
		t.Fatalf("no signing key: %v %v", c, err)
	}
}
