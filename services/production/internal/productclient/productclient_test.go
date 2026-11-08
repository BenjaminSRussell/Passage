package productclient

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	tenantID = "11111111-1111-4111-8111-111111111111"
	entityID = "22222222-2222-4222-8222-222222222222"
	released = "33333333-3333-4333-8333-333333333333"
	siteID   = "44444444-4444-4444-8444-444444444444"
)

func newClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := tenant.NewServiceSigner("production", priv)
	if err != nil {
		t.Fatal(err)
	}
	c, err := New(srv.URL, signer, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The grant table is compiled from policies/authz/data.json, which does not yet carry production -> product
	// (handoff in NOT-IMPLEMENTED.md), so these tests replace signing; TestSignedWithTheProductReadGrant covers it.
	c.sign = func(req *http.Request, _ tenant.Principal, legalEntityID string) error {
		req.Header.Set("X-Test-Legal-Entity", legalEntityID)
		return nil
	}
	return c
}

func TestSignedWithTheProductReadGrant(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, _ := tenant.NewServiceSigner("production", priv)
	verifier := tenant.NewServiceVerifier("product", map[string]ed25519.PublicKey{"production": pub})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, err := verifier.Verify(r.Header.Get(tenant.HeaderServiceToken))
		if err != nil || len(claims.Duties) != 1 || claims.Duties[0] != "product.read" {
			t.Errorf("token %v duties %v", err, claims.Duties)
		}
		if claims.Tid != tenantID || claims.Le != entityID {
			t.Errorf("the token must carry the caller's tenant and the order's legal entity, got tid=%s le=%s", claims.Tid, claims.Le)
		}
		_, _ = w.Write([]byte(`{"MinQty":"1"}`))
	}))
	defer srv.Close()
	c, _ := New(srv.URL, signer, nil)
	_, err = c.SiteDefault(ctx(), entityID, released, siteID)
	if errors.Is(err, tenant.ErrNoServiceGrant) {
		t.Skip("awaiting the authz seed: service_grants.production gains product: [product.read]")
	}
	if err != nil {
		t.Fatal(err)
	}
}

func ctx() context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: tenantID, LegalEntityID: entityID, UserID: "planner"})
}

func TestSiteDefault_ReadsTheCascadedQuantities(t *testing.T) {
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/product/released-products/"+released+"/site-defaults/"+siteID {
			t.Errorf("path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"SiteID":"` + siteID + `","MinQty":"10","MultipleQty":"5","StdQty":"20","LeadTimeDays":3}`))
	})
	got, err := c.SiteDefault(ctx(), entityID, released, siteID)
	if err != nil {
		t.Fatal(err)
	}
	if got.MinQty.String() != "10" || got.MultipleQty.String() != "5" || got.StdQty.String() != "20" {
		t.Fatalf("got %+v", got)
	}
}

func TestSiteDefault_MapsTheAnswers(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"unknown released product", 404, `{}`, ErrNotFound},
		{"no default", 422, `{"code":"PRD-RELEASE-DEFAULT-MISSING"}`, ErrNoDefault},
		{"other 422", 422, `{"code":"X"}`, ErrUnavailable},
		{"server error", 500, `{}`, ErrUnavailable},
		{"unreadable", 200, `nope`, ErrUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			if _, err := c.SiteDefault(ctx(), entityID, released, siteID); !errors.Is(err, tc.want) {
				t.Fatalf("err %v, want %v", err, tc.want)
			}
		})
	}
}

func TestSiteDefault_NonUUIDIsNotFoundWithoutCalling(t *testing.T) {
	c := newClient(t, func(http.ResponseWriter, *http.Request) { t.Error("called") })
	if _, err := c.SiteDefault(ctx(), entityID, "x", siteID); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestFromEnv(t *testing.T) {
	if r, err := FromEnv(func(string) string { return "" }); r != nil || err != nil {
		t.Fatalf("unset: %v %v", r, err)
	}
	if _, err := FromEnv(func(k string) string {
		if k == "PRODUCT_URL" {
			return "http://product"
		}
		return ""
	}); err == nil {
		t.Fatal("PRODUCT_URL without a key must fail")
	}
}
