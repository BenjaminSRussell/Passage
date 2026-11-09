package engineering

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	tenantID = "11111111-1111-4111-8111-111111111111"
	entityID = "22222222-2222-4222-8222-222222222222"
	product  = "44444444-4444-4444-8444-444444444444"
	site     = "55555555-5555-4555-8555-555555555555"
)

func principalCtx() context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: tenantID, LegalEntityID: entityID, UserID: "cost.roller"})
}

func clientFor(t *testing.T, handler http.HandlerFunc) (*Client, *tenant.ServiceVerifier) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := tenant.NewServiceSigner("productcost", priv)
	if err != nil {
		t.Fatal(err)
	}
	c, err := New(srv.URL+"/", signer, nil)
	if err != nil {
		t.Fatal(err)
	}
	return c, tenant.NewServiceVerifier("engineering", map[string]ed25519.PublicKey{"productcost": pub})
}

func TestActiveBOM_PicksTheCostingUsageOverProductionAndReadsItsLinesWithASignedReadOnlyToken(t *testing.T) {
	var verifier *tenant.ServiceVerifier
	var claims tenant.ServiceClaims
	var listQuery string
	c, v := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		var err error
		if claims, err = verifier.Verify(r.Header.Get(tenant.HeaderServiceToken)); err != nil {
			t.Errorf("token: %v", err)
		}
		switch r.URL.Path {
		case bomPath:
			listQuery = r.URL.RawQuery
			_, _ = w.Write([]byte(`{"items":[{"id":"prod","usage":"production","category":"material"},{"id":"cost","usage":"costing","category":"material"},{"id":"doc","usage":"costing","category":"document"},{"id":"eng","usage":"engineering","category":"material"}]}`))
		case bomPath + "/cost":
			_, _ = w.Write([]byte(`{"id":"cost","lines":[{"componentProductId":"c1","quantity":"2","series":"1"}]}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	})
	verifier = v
	bom, err := c.ActiveBOM(principalCtx(), entityID, product, site, "2026-01-02")
	if err != nil || bom == nil || bom.ID != "cost" || len(bom.Lines) != 1 {
		t.Fatalf("bom %+v err %v", bom, err)
	}
	if len(claims.Duties) != 1 || claims.Duties[0] != dutyRead || claims.Aud != audience || claims.Le != entityID {
		t.Fatalf("claims %+v, want only engineering.read", claims)
	}
	if listQuery == "" {
		t.Fatal("no filters sent")
	}
}

func TestActiveBOMProducts_PagesDistinctMaterialProductsAndStopsPastTheLimit(t *testing.T) {
	var cursors []string
	c, _ := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		cursor := r.URL.Query().Get("cursor")
		cursors = append(cursors, cursor)
		if r.URL.Query().Get("category") != "material" || r.URL.Query().Get("siteId") != site || r.URL.Query().Get("status") != "active" || r.URL.Query().Get("asOf") != "2026-01-02" {
			t.Errorf("filters %s", r.URL.RawQuery)
		}
		switch cursor {
		case "":
			_, _ = w.Write([]byte(`{"items":[{"id":"a1","productId":"p1","usage":"production","category":"material"},{"id":"a2","productId":"p1","usage":"costing","category":"material"},{"id":"d1","productId":"pdoc","usage":"production","category":"document"},{"id":"s1","productId":"psales","usage":"sales"}],"nextCursor":"c2"}`))
		case "c2":
			_, _ = w.Write([]byte(`{"items":[{"id":"b1","productId":"p2","usage":"production","category":"material"},{"id":"b2","productId":"p3","usage":"","category":""}]}`))
		default:
			t.Errorf("unexpected cursor %q", cursor)
		}
	})
	got, err := c.ActiveBOMProducts(principalCtx(), entityID, site, "2026-01-02", 10)
	if err != nil || len(got) != 3 || got[0] != "p1" || got[1] != "p2" || got[2] != "p3" || len(cursors) != 2 {
		t.Fatalf("products %v err %v cursors %v", got, err, cursors)
	}
	cursors = nil
	if got, err = c.ActiveBOMProducts(principalCtx(), entityID, site, "2026-01-02", 0); err != nil || len(got) != 1 || len(cursors) != 1 {
		t.Fatalf("past the limit: %v %v cursors %v, want to stop on the first page", got, err, cursors)
	}
	if _, err = c.ActiveBOMProducts(context.Background(), entityID, site, "2026-01-02", 1); err == nil {
		t.Fatal("no principal must fail the read")
	}
}

func TestActiveRoute_AnUnknownRouteIsNilAndAnEngineeringFailureIsAnError(t *testing.T) {
	status := http.StatusNotFound
	c, _ := clientFor(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) })
	if route, err := c.ActiveRoute(principalCtx(), entityID, product, site, "2026-01-02"); route != nil || err != nil {
		t.Fatalf("no route: %+v %v", route, err)
	}
	status = http.StatusInternalServerError
	if _, err := c.ActiveRoute(principalCtx(), entityID, product, site, "2026-01-02"); err == nil {
		t.Fatal("a 500 must fail the read")
	}
	if _, err := c.ActiveRoute(context.Background(), entityID, product, site, "2026-01-02"); err == nil {
		t.Fatal("no principal must fail the read")
	}
}

func TestNewAndFromEnv(t *testing.T) {
	if _, err := New("", nil, nil); err == nil {
		t.Fatal("no base URL")
	}
	if _, err := New("http://x", nil, nil); err == nil {
		t.Fatal("no signer")
	}
	if c, err := FromEnv(func(string) string { return "" }); c != nil || err != nil {
		t.Fatalf("unset: %v %v", c, err)
	}
	if _, err := FromEnv(func(k string) string {
		if k == "ENGINEERING_URL" {
			return "http://engineering"
		}
		return ""
	}); err == nil {
		t.Fatal("a URL without a signing key must fail")
	}
}
