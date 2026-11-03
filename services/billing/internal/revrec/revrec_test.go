package revrec

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	tenantID  = "11111111-1111-4111-8111-111111111111"
	entityID  = "22222222-2222-4222-8222-222222222222"
	orderID   = "33333333-3333-4333-8333-333333333333"
	contract  = "44444444-4444-4444-8444-444444444444"
	productA  = "55555555-5555-4555-8555-555555555555"
	productB  = "66666666-6666-4666-8666-666666666666"
	otherID   = "77777777-7777-4777-8777-777777777777"
	headerFmt = `{"id":"%s","legalEntityId":"` + entityID + `","orderId":"` + orderID + `","currency":"USD","status":"%s"}`
)

func principalCtx() context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: tenantID, LegalEntityID: entityID, UserID: "biller"})
}

func header(id, status string) string {
	return fmt.Sprintf(headerFmt, id, status)
}

func clientFor(t *testing.T, handler http.HandlerFunc) (*Client, *tenant.ServiceVerifier) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := tenant.NewServiceSigner("billing", priv)
	if err != nil {
		t.Fatal(err)
	}
	c, err := New(srv.URL+"/", signer, nil)
	if err != nil {
		t.Fatal(err)
	}
	return c, tenant.NewServiceVerifier("revrec", map[string]ed25519.PublicKey{"billing": pub})
}

func TestAllocation_ReadsTheLiveContractWithASignedReadOnlyToken(t *testing.T) {
	var verifier *tenant.ServiceVerifier
	var paths []string
	var claims tenant.ServiceClaims
	c, v := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		var err error
		if claims, err = verifier.Verify(r.Header.Get(tenant.HeaderServiceToken)); err != nil {
			t.Errorf("token: %v", err)
		}
		if r.Header.Get(tenant.HeaderUser) != "" {
			t.Errorf("the end user's header was forwarded")
		}
		paths = append(paths, r.URL.Path+"?"+r.URL.RawQuery)
		if r.URL.Path == contractsPath {
			_, _ = w.Write([]byte(`{"items":[` + header(otherID, "CANCELLED") + `,` + header(contract, "OPEN") + `]}`))
			return
		}
		_, _ = w.Write([]byte(strings.Replace(header(contract, "OPEN"), "}", `,"obligations":[
			{"productId":"`+strings.ToUpper(productA)+`","allocated":"7200.00","method":"POINT_IN_TIME"},
			{"productId":"`+productB+`","allocated":"2250.00","method":"RATABLE"}]}`, 1)))
	})
	verifier = v
	got, err := c.Allocation(principalCtx(), strings.ToUpper(entityID), orderID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Currency != "USD" || len(got.Obligations) != 2 || got.Obligations[0].ProductID != productA || got.Obligations[0].Allocated.String() != "7200" || got.Obligations[1].Method != "RATABLE" {
		t.Fatalf("allocation %+v", got)
	}
	if len(paths) != 2 || !strings.Contains(paths[0], "orderId="+orderID) || !strings.Contains(paths[0], "legalEntityId="+entityID) || paths[1] != contractsPath+"/"+contract+"?orderId="+orderID {
		t.Errorf("requests %v", paths)
	}
	if len(claims.Duties) != 1 || claims.Duties[0] != dutyRead || claims.Aud != audience || claims.Le != entityID {
		t.Errorf("claims %+v, want only revrec.allocation.read for the legal entity", claims)
	}
}

func TestAllocation_FailsClosed(t *testing.T) {
	cases := map[string]struct {
		list, detail string
		status       int
		want         error
	}{
		"no contract":        {list: `{"items":[]}`, want: ErrNoContract},
		"only cancelled":     {list: `{"items":[` + header(contract, "CANCELLED") + `]}`, want: ErrNoContract},
		"two contracts":      {list: `{"items":[` + header(contract, "OPEN") + `,` + header(otherID, "COMPLETE") + `]}`, want: ErrAmbiguous},
		"more pages":         {list: `{"items":[` + header(contract, "OPEN") + `],"nextCursor":"x"}`, want: ErrAmbiguous},
		"revrec down":        {status: http.StatusServiceUnavailable},
		"other order":        {list: `{"items":[` + header(contract, "OPEN") + `]}`, detail: strings.Replace(header(contract, "OPEN"), orderID, otherID, 1)},
		"other legal entity": {list: `{"items":[` + header(contract, "OPEN") + `]}`, detail: strings.Replace(header(contract, "OPEN"), entityID, otherID, 1)},
		"bad amount":         {list: `{"items":[` + header(contract, "OPEN") + `]}`, detail: strings.Replace(header(contract, "OPEN"), "}", `,"obligations":[{"productId":"`+productA+`","allocated":"x","method":"RATABLE"}]}`, 1)},
	}
	for name, tc := range cases {
		c, _ := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
			if tc.status != 0 {
				w.WriteHeader(tc.status)
				return
			}
			body := tc.list
			if r.URL.Path != contractsPath {
				body = tc.detail
			}
			_, _ = w.Write([]byte(body))
		})
		_, err := c.Allocation(principalCtx(), entityID, orderID)
		if err == nil || tc.want != nil && !errors.Is(err, tc.want) {
			t.Errorf("%s: error %v, want %v", name, err, tc.want)
		}
	}
}

func TestAllocation_NeedsAPrincipalAndUUIDsAndRefusesRedirects(t *testing.T) {
	c, _ := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1:1/elsewhere", http.StatusFound)
	})
	if _, err := c.Allocation(context.Background(), entityID, orderID); err == nil {
		t.Error("no principal accepted")
	}
	if _, err := c.Allocation(principalCtx(), "x", orderID); err == nil {
		t.Error("bad legal entity accepted")
	}
	if _, err := c.Allocation(principalCtx(), entityID, "x"); err == nil {
		t.Error("bad order accepted")
	}
	if _, err := c.Allocation(principalCtx(), entityID, orderID); err == nil {
		t.Error("a redirect was followed")
	}
}

func TestNewAndFromEnv(t *testing.T) {
	if _, err := New("", nil, nil); err == nil {
		t.Error("client without a URL accepted")
	}
	if _, err := New("http://revrec", nil, nil); err == nil {
		t.Error("client without a signing key accepted")
	}
	if c, err := FromEnv(func(string) string { return "" }); c != nil || err != nil {
		t.Errorf("unset URL gave %v, %v, want nil, nil", c, err)
	}
	if _, err := FromEnv(func(k string) string {
		if k == "REVREC_URL" {
			return "http://revrec"
		}
		return ""
	}); err == nil {
		t.Error("URL without a signing key accepted")
	}
}
