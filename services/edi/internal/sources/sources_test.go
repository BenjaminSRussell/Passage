package sources

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	tid = "00000000-0000-0000-0000-000000000007"
	le  = "00000000-0000-0000-0000-0000000000e1"
	id  = "00000000-0000-0000-0000-0000000000a1"
)

func principal(scope string) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: tid, LegalEntityID: scope, UserID: "user-1", Duties: map[string]struct{}{"EDI_OPERATE": {}}})
}

func signerAndVerifier(t *testing.T, aud string) (*tenant.ServiceSigner, *tenant.ServiceVerifier) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := tenant.NewServiceSigner("edi", priv)
	if err != nil {
		t.Fatal(err)
	}
	return signer, tenant.NewServiceVerifier(aud, map[string]ed25519.PublicKey{"edi": pub})
}

func TestClient_SignsOnlyTheNarrowReadDutyForTheProfileLegalEntity(t *testing.T) {
	cases := []struct {
		aud, duty, path string
		call            func(*Client) error
	}{
		{"fulfillment", DutyShipmentRead, "/v1/fulfillment/shipments/" + id, func(c *Client) error { _, err := c.Shipment(principal(le), le, id); return err }},
		{"billing", DutyInvoiceRead, "/v1/billing/invoices/" + id, func(c *Client) error { _, err := c.Invoice(principal(le), le, id); return err }},
		{"sales", DutyOrderRead, "/v1/sales/orders/" + id, func(c *Client) error { _, err := c.Order(principal(le), le, id); return err }},
	}
	for _, tc := range cases {
		signer, verifier := signerAndVerifier(t, tc.aud)
		var claims tenant.ServiceClaims
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var err error
			if claims, err = verifier.Verify(r.Header.Get(tenant.HeaderServiceToken)); err != nil || r.URL.Path != tc.path || r.Method != http.MethodGet {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"id":"` + id + `"}`))
		}))
		defer srv.Close()
		c := New(signer, srv.URL, srv.URL, srv.URL, nil)
		if err := tc.call(c); err != nil {
			t.Fatalf("%s: %v", tc.aud, err)
		}
		if !slices.Equal(claims.Duties, []string{tc.duty}) || claims.Le != le || claims.Tid != tid || claims.Act != "user-1" {
			t.Errorf("%s: token must carry only %s for the profile legal entity: %+v", tc.aud, tc.duty, claims)
		}
	}
}

func TestClient_RefusesALegalEntityOutsideTheCallersScope(t *testing.T) {
	signer, _ := signerAndVerifier(t, "fulfillment")
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer srv.Close()
	other := "00000000-0000-0000-0000-0000000000ff"
	_, err := New(signer, srv.URL, srv.URL, srv.URL, nil).Shipment(principal(other), le, id)
	if !errors.Is(err, ErrForbidden) || called {
		t.Fatalf("a user scoped to another legal entity cannot mint a token for %s: %v (called=%v)", le, err, called)
	}
}

func TestClient_MapsStatusesAndRefusesUnconfiguredSources(t *testing.T) {
	signer, _ := signerAndVerifier(t, "billing")
	status := http.StatusNotFound
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }))
	defer srv.Close()
	c := New(signer, srv.URL, srv.URL, srv.URL, nil)
	if _, err := c.Invoice(principal(le), le, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("404: %v", err)
	}
	status = http.StatusForbidden
	if _, err := c.Invoice(principal(le), le, id); !errors.Is(err, ErrForbidden) {
		t.Errorf("403: %v", err)
	}
	status = http.StatusInternalServerError
	if _, err := c.Invoice(principal(le), le, id); err == nil || errors.Is(err, ErrNotFound) || errors.Is(err, ErrForbidden) {
		t.Errorf("500 is a plain failure: %v", err)
	}
	if _, err := New(signer, "", "", "", nil).Invoice(principal(le), le, id); err == nil {
		t.Error("an unset base URL is a failure, not a silent empty document")
	}
	if _, err := New(nil, srv.URL, srv.URL, srv.URL, nil).Invoice(principal(le), le, id); err == nil {
		t.Error("no signer must not fall back to forwarding the user")
	}
}
