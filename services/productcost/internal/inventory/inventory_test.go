package inventory

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	tenantID  = "11111111-1111-4111-8111-111111111111"
	entityID  = "22222222-2222-4222-8222-222222222222"
	otherLE   = "33333333-3333-4333-8333-333333333333"
	itemID    = "44444444-4444-4444-8444-444444444444"
	siteID    = "55555555-5555-4555-8555-555555555555"
	otherSite = "66666666-6666-4666-8666-666666666666"
	otherItem = "77777777-7777-4777-8777-777777777777"
)

func principalCtx() context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: tenantID, LegalEntityID: entityID, UserID: "cost.controller"})
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
	return c, tenant.NewServiceVerifier("inventory", map[string]ed25519.PublicKey{"productcost": pub})
}

var wantedPosition = Position{ItemID: itemID, SiteID: siteID}

func TestOnHand_SendsASignedReadOnlyRequestPerItemAndKeepsTheRequestedPositions(t *testing.T) {
	var verifier *tenant.ServiceVerifier
	var claims tenant.ServiceClaims
	var path, groupBy, asOf, product, userHeader string
	c, v := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		var err error
		if claims, err = verifier.Verify(r.Header.Get(tenant.HeaderServiceToken)); err != nil {
			t.Errorf("token: %v", err)
		}
		path, groupBy, asOf, product, userHeader = r.URL.Path, r.URL.Query().Get("groupBy"), r.URL.Query().Get("asOf"), r.URL.Query().Get("product"), r.Header.Get(tenant.HeaderUser)
		_, _ = w.Write([]byte(`{"items":[
			{"group":{"product":"` + itemID + `","site":"` + strings.ToUpper(siteID) + `","legal_entity":"` + entityID + `"},"physicalInvent":"6.5"},
			{"group":{"product":"` + itemID + `","site":"` + siteID + `","legal_entity":"` + entityID + `"},"physicalInvent":"3.5"},
			{"group":{"product":"` + itemID + `","site":"` + siteID + `","legal_entity":"` + otherLE + `"},"physicalInvent":"99"},
			{"group":{"product":"` + itemID + `","site":"MAIN-WAREHOUSE","legal_entity":"` + entityID + `"},"physicalInvent":"7"},
			{"group":{"product":"` + itemID + `","site":"` + otherSite + `","legal_entity":"` + entityID + `"},"physicalInvent":"8"}
		],"truncated":false}`))
	})
	verifier = v
	got, err := c.OnHand(principalCtx(), entityID, "2026-01-02", []Position{{ItemID: strings.ToUpper(itemID), SiteID: siteID}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[wantedPosition].String() != "10" {
		t.Fatalf("on-hand %v, want only the requested position with the two rows of the legal entity summed", got)
	}
	if path != onHandPath || groupBy != "product,site,legal_entity" || asOf != "2026-01-02" || product != itemID || userHeader != "" {
		t.Fatalf("request path=%s groupBy=%s asOf=%s product=%s user header=%q", path, groupBy, asOf, product, userHeader)
	}
	if len(claims.Duties) != 1 || claims.Duties[0] != dutyRead || claims.Aud != audience || claims.Le != entityID || claims.Act != "cost.controller" {
		t.Fatalf("claims %+v, want only inventory.read for the legal entity", claims)
	}
}

func TestOnHand_APositionInventoryHasNoRowForIsAbsentNotZero(t *testing.T) {
	requests := 0
	c, _ := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		_, _ = w.Write([]byte(`{"items":[{"group":{"product":"` + r.URL.Query().Get("product") + `","site":"WEST","legal_entity":"` + entityID + `"},"physicalInvent":"4"}],"truncated":false}`))
	})
	other := Position{ItemID: otherItem, SiteID: siteID}
	got, err := c.OnHand(principalCtx(), entityID, "", []Position{wantedPosition, {ItemID: itemID, SiteID: otherSite}, other})
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v; a site value that is not the productcost site id cannot match", got, err)
	}
	if requests != 2 {
		t.Fatalf("%d requests, want one per distinct item", requests)
	}
}

func TestOnHand_LiveReadSendsNoAsOf(t *testing.T) {
	var query map[string][]string
	c, _ := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.Query()
		_, _ = w.Write([]byte(`{"items":[],"truncated":false}`))
	})
	got, err := c.OnHand(principalCtx(), entityID, "", []Position{wantedPosition})
	if err != nil || len(got) != 0 {
		t.Fatalf("%v %v", got, err)
	}
	if _, set := query["asOf"]; set {
		t.Fatalf("a live read must not send asOf: %v", query)
	}
}

func TestOnHand_FailsClosedOnAnIncompleteOrUnusableAnswer(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
	}{
		"truncated":    {http.StatusOK, `{"items":[],"truncated":true}`},
		"non-200":      {http.StatusForbidden, `{"code":"AUTH-0002"}`},
		"server error": {http.StatusInternalServerError, ``},
		"bad quantity": {http.StatusOK, `{"items":[{"group":{"product":"` + itemID + `","site":"` + siteID + `","legal_entity":"` + entityID + `"},"physicalInvent":"ten"}]}`},
		"not json":     {http.StatusOK, `<html>`},
	} {
		t.Run(name, func(t *testing.T) {
			c, _ := clientFor(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			if got, err := c.OnHand(principalCtx(), entityID, "", []Position{wantedPosition}); err == nil || got != nil {
				t.Fatalf("got %v, %v; want an error and no quantities", got, err)
			}
		})
	}
}

func TestOnHand_NeedsAPrincipalValidPositionsAndRefusesRedirects(t *testing.T) {
	c, _ := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://attacker.invalid/steal", http.StatusFound)
	})
	if _, err := c.OnHand(context.Background(), entityID, "", []Position{wantedPosition}); err == nil {
		t.Fatal("no principal must fail before any request")
	}
	if _, err := c.OnHand(principalCtx(), entityID, "", []Position{{ItemID: "I1", SiteID: siteID}}); err == nil {
		t.Fatal("a position that is not a pair of UUIDs must fail before any request")
	}
	if _, err := c.OnHand(principalCtx(), entityID, "", []Position{wantedPosition}); err == nil {
		t.Fatal("a redirect answer must not be followed or accepted")
	}
}

func TestNewAndFromEnv(t *testing.T) {
	if _, err := New("", nil, nil); err == nil {
		t.Error("a client without a URL")
	}
	if _, err := New("http://inventory", nil, nil); err == nil {
		t.Error("a client without a signing key would forward the user's headers")
	}
	none := func(string) string { return "" }
	if c, err := FromEnv(none); c != nil || err != nil {
		t.Errorf("unset INVENTORY_URL: %v %v", c, err)
	}
	urlOnly := func(k string) string {
		if k == "INVENTORY_URL" {
			return "http://inventory"
		}
		return ""
	}
	if c, err := FromEnv(urlOnly); c != nil || err == nil {
		t.Errorf("INVENTORY_URL without a signing key: %v %v", c, err)
	}
}
